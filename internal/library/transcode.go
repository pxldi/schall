package library

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	"github.com/pxldi/schall/internal/filecopy"
	"github.com/pxldi/schall/internal/tagging"
	"github.com/pxldi/schall/internal/transcode"
)

// The transcoding setting shrinks a file the moment it is imported, so it only
// ever reaches music that arrives after it was turned on. This is the pass that
// catches up with everything that arrived before: the same setting, read again
// for every file already on disc.
//
// It re-encodes strictly proven files and nothing else. A file the resolver has
// not settled — pending, needing review, in conflict — is never opened by this
// pass, for the same reason the acoustic check never runs on one: shrinking it
// changes bytes an identity decision has not yet been made against, and a
// decision made afterwards would be made against bytes a re-encode altered
// rather than the ones that arrived. Only a file with a MusicBrainz recording
// already resolved onto it is eligible.
//
// transcodeBatch is smaller than the loudness pass's: that one decodes a file,
// this one decodes it and re-encodes it and writes it back to disc, several
// times the work per file for the one goroutine sharing the worker with every
// import, scan and resolution in progress.
const transcodeBatch = 5

// TranscodeRun is one pass over the library, carried in the job's payload the
// same way TagRun is: nothing here outlives the run, so there is nothing to
// keep beyond it.
type TranscodeRun struct {
	RunID uuid.UUID `json:"runId"`
	// After is the last file the run reached, in id order — the same cursor
	// TagRun uses and for the same reason: a run killed halfway resumes where
	// it left off rather than from the top.
	After      uuid.UUID `json:"after,omitempty"`
	Transcoded int       `json:"transcoded"`
	Skipped    int       `json:"skipped"`
	Failed     int       `json:"failed"`
}

// TranscodeStatus is a run as the settings page reads it.
type TranscodeStatus struct {
	Status      string
	Error       string
	StartedAt   *time.Time
	CompletedAt *time.Time
	Run         TranscodeRun
	// Eligible is how many files in the library could be shrunk under the
	// policy stored right now: proven, present, not already re-encoded. It
	// moves with the setting, not with any one run.
	Eligible int
}

// TranscodeJobRow is the queued pass, answered to whoever asked for one.
type TranscodeJobRow struct {
	ID        uuid.UUID
	Status    string
	CreatedAt time.Time
}

// TranscodeSweep shrinks files already in the library that the operator's
// policy asks for.
type TranscodeSweep struct {
	pool    *pgxpool.Pool
	service *transcode.Service
	logger  zerolog.Logger
	place   func(string, string, string) error
	// toldNoEncoder keeps the pass from writing the same line to the log every
	// few files. It is reset by nothing because an installation missing ffmpeg
	// is not going to grow one between batches; a restart with an encoder
	// configured starts a fresh sweep and a fresh Sweep instance either way.
	toldNoEncoder atomic.Bool
}

func NewTranscodeSweep(pool *pgxpool.Pool, service *transcode.Service, logger zerolog.Logger) *TranscodeSweep {
	return &TranscodeSweep{pool: pool, service: service, logger: logger, place: filecopy.Place}
}

// QueueSweep asks for a pass over the library, and answers with the one
// already under way where there is one — a second pass beside the first would
// re-encode the same files twice and race each other renaming the same paths.
func (sweep *TranscodeSweep) QueueSweep(ctx context.Context) (TranscodeJobRow, error) {
	var row TranscodeJobRow
	err := sweep.pool.QueryRow(ctx, `
		WITH queued AS (
			INSERT INTO jobs (kind, payload, max_attempts)
			SELECT 'sweep_transcode', jsonb_build_object('runId', gen_random_uuid()::text), 5
			WHERE NOT EXISTS (
				SELECT 1 FROM jobs
				WHERE kind = 'sweep_transcode' AND status IN ('queued', 'running')
			)
			RETURNING id, status, created_at
		)
		SELECT id, status, created_at FROM queued
		UNION ALL
		SELECT id, status, created_at
		FROM jobs
		WHERE kind = 'sweep_transcode' AND status IN ('queued', 'running')
		ORDER BY created_at
		LIMIT 1
	`).Scan(&row.ID, &row.Status, &row.CreatedAt)
	if err != nil {
		return TranscodeJobRow{}, fmt.Errorf("queue a pass over the library to shrink: %w", err)
	}
	return row, nil
}

// QueueNextSweep carries a run on into another job, from exactly where this one
// left it.
func (sweep *TranscodeSweep) QueueNextSweep(ctx context.Context, jobID uuid.UUID) error {
	_, err := sweep.pool.Exec(ctx, `
		INSERT INTO jobs (kind, payload, max_attempts)
		SELECT 'sweep_transcode', jobs.payload, jobs.max_attempts
		FROM jobs
		WHERE jobs.id = $1
		  AND NOT EXISTS (
		      SELECT 1 FROM jobs others
		      WHERE others.kind = 'sweep_transcode'
		        AND others.status IN ('queued', 'running')
		  )
	`, jobID)
	if err != nil {
		return fmt.Errorf("queue the rest of the pass to shrink the library: %w", err)
	}
	return nil
}

// Sweep shrinks one batch of the library and reports whether more is waiting.
//
// It never fails the job over one file: a file that could not be re-encoded, or
// that the policy no longer wants shrunk, is left exactly as it was and the
// pass moves on. The one thing that stops the whole pass is the policy being
// off or the encoder being missing — walking the rest of the library to learn
// that on every remaining file would be work with no answer at the end.
func (sweep *TranscodeSweep) Sweep(ctx context.Context, jobID uuid.UUID) (bool, error) {
	run, err := sweep.run(ctx, jobID)
	if err != nil {
		return false, err
	}

	policy, err := sweep.service.Policy(ctx)
	if err != nil {
		sweep.logger.Error().Err(err).
			Msg("the transcoding policy could not be read; the pass over the library stopped")
		return false, nil
	}
	if !policy.Enabled {
		return false, nil
	}
	if !sweep.service.EncoderAvailable() {
		if sweep.toldNoEncoder.CompareAndSwap(false, true) {
			sweep.logger.Info().Msg("the library will not be shrunk: no audio encoder is available")
		}
		return false, nil
	}

	files, err := sweep.next(ctx, run.After)
	if err != nil {
		return false, err
	}
	if len(files) == 0 {
		return false, nil
	}

	for _, file := range files {
		if err := ctx.Err(); err != nil {
			// The process is stopping. Nothing is recorded for a file the
			// re-encode was cut off part way through: it is still on disc,
			// untouched, and the next pass reaches it again.
			return false, nil
		}
		outcome := sweep.shrink(ctx, policy, file)
		switch outcome {
		case shrunk:
			run.Transcoded++
		case failed:
			run.Failed++
		default:
			run.Skipped++
		}
		run.After = file.id
		if err := sweep.record(ctx, jobID, run); err != nil {
			return false, err
		}
	}
	if run.Transcoded > 0 || run.Failed > 0 {
		sweep.logger.Info().Int("transcoded", run.Transcoded).Int("skipped", run.Skipped).
			Int("failed", run.Failed).Msg("the library was shrunk")
	}
	return len(files) == transcodeBatch, nil
}

// shrinkOutcome says what became of one file, for the run's counts.
type shrinkOutcome int

const (
	skipped shrinkOutcome = iota
	shrunk
	failed
)

// existingFile is one library file the pass has not looked at yet.
type existingFile struct {
	id          uuid.UUID
	path        string
	sizeBytes   int64
	durationMS  *int
	recordingID uuid.UUID
	rootPath    string
}

// shrink re-encodes one file and files the smaller copy in its place, or
// leaves the file untouched and marks it checked so the pass does not open it
// again on every future run.
//
// The steps are ordered so that nothing is ever missing: the smaller copy is
// written and verified before anything about the original changes, the library
// row is moved onto it before the original bytes are removed, and a removal
// record is committed before the removal happens. A crash at any point before
// the record commits leaves the original exactly as it was; a crash after
// leaves a removal row saying what was meant to happen and unlinked_at empty,
// which is what the retry and the screen both read.
func (sweep *TranscodeSweep) shrink(
	ctx context.Context, policy transcode.Policy, file existingFile,
) shrinkOutcome {
	if file.rootPath == "" || containedIn(file.rootPath, file.path) != nil {
		sweep.logger.Warn().Str("path", file.path).
			Msg("a library file outside every configured root will not be shrunk")
		sweep.markChecked(ctx, file.id)
		return skipped
	}

	placements, cleanup := sweep.service.Plan(ctx, []transcode.Request{{
		Source: file.path, Name: filepath.Base(file.path), SizeBytes: file.sizeBytes,
	}})
	defer cleanup()
	placement := placements[0]
	if !placement.Transcoded() {
		sweep.markChecked(ctx, file.id)
		return skipped
	}

	destination := filepath.Join(filepath.Dir(file.path), placement.Name)
	if _, err := os.Stat(destination); err == nil {
		sweep.logger.Warn().Str("path", file.path).Str("wanted", destination).
			Msg("a smaller copy would have taken the name of another file already in the library")
		sweep.markChecked(ctx, file.id)
		return skipped
	} else if !errors.Is(err, os.ErrNotExist) {
		sweep.logger.Error().Err(err).Str("path", destination).
			Msg("check whether a smaller copy's name is already taken")
		return failed
	}

	// The smaller copy is placed beside the original — both are on disc at
	// once — before either the database or the original bytes are touched.
	if err := sweep.place(placement.Source, destination, ".schall-transcode-"); err != nil {
		sweep.logger.Error().Err(err).Str("path", file.path).
			Msg("file the smaller copy of a library file")
		return failed
	}

	// Read back what was actually filed, the same way a scan would read it. The
	// row is about to say this path holds different bytes than the ones every
	// other audio column on it describes, so those columns are refreshed from
	// the file that is really there rather than left to say what the original
	// used to measure as.
	written, err := tagging.ReadProperties(destination)
	if err != nil || !written.Known() {
		sweep.logger.Error().Err(err).Str("path", destination).
			Msg("read back the smaller copy of a library file")
		_ = os.Remove(destination)
		return failed
	}
	originalDuration := file.durationMS
	if originalDuration == nil || *originalDuration <= 0 {
		original, readErr := tagging.ReadProperties(file.path)
		if readErr != nil || !original.Known() || original.DurationMS <= 0 {
			sweep.logger.Warn().Err(readErr).Str("path", file.path).
				Msg("the original duration could not be read, so the smaller copy will not be filed")
			_ = os.Remove(destination)
			sweep.markChecked(ctx, file.id)
			return skipped
		}
		originalDuration = &original.DurationMS
	}
	if durationDifference(written.DurationMS, *originalDuration) > transcode.ToleranceMS {
		sweep.logger.Warn().Str("path", destination).
			Int("written_ms", written.DurationMS).Int("original_ms", *originalDuration).
			Msg("the smaller copy has a different length, so it will not replace the original")
		_ = os.Remove(destination)
		sweep.markChecked(ctx, file.id)
		return skipped
	}

	removalID, err := sweep.recordShrink(ctx, file, placement, destination, written)
	if err != nil {
		sweep.logger.Error().Err(err).Str("path", file.path).
			Msg("record that a library file was shrunk")
		_ = os.Remove(destination)
		return failed
	}

	if err := sweep.unlink(ctx, removalID, file.path); err != nil {
		// The library row already points at the smaller copy and the smaller
		// copy is already on disc, so this is not a failed shrink — it is
		// bytes the retry now knows to remove. See keepone.go's unlink for the
		// same shape of the same problem.
		sweep.logger.Warn().Err(err).Str("path", file.path).
			Msg("the original could not be removed; it is recorded to be retried")
	}
	return shrunk
}

// markChecked notes that the pass looked at this file and found nothing to
// shrink, so the next pass does not open it again. A file that changes on disc
// clears this the way it clears every other measurement: the scan that reads it
// again writes a new row.
func (sweep *TranscodeSweep) markChecked(ctx context.Context, fileID uuid.UUID) {
	if _, err := sweep.pool.Exec(ctx, `
		UPDATE library_files SET transcode_checked_at = now(), updated_at = now()
		WHERE id = $1
	`, fileID); err != nil {
		sweep.logger.Error().Err(err).Str("file_id", fileID.String()).
			Msg("record that a library file was checked for shrinking")
	}
}

// recordShrink records the original bytes for retry together with moving the
// library row onto the smaller copy, in one transaction. The
// row keeps its identifier: everything that names a file by that id — a
// mapping, a playlist entry, a lease — still names the same file, now smaller.
func (sweep *TranscodeSweep) recordShrink(
	ctx context.Context, file existingFile, placement transcode.Placement,
	destination string, written tagging.AudioProperties,
) (uuid.UUID, error) {
	transaction, err := sweep.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return uuid.Nil, err
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	var removalID uuid.UUID
	if err := transaction.QueryRow(ctx, `
		INSERT INTO library_file_removals (
		    library_file_id, library_path, size_bytes,
		    musicbrainz_recording_id, kept_path, removed_by, reason
		) VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id
	`, file.id, file.path, file.sizeBytes, file.recordingID, destination,
		"the shrink-the-library sweep",
		// From then to, in that order. The two were the other way round and the
		// sentence read "re-encoded to flac at mp3", which says nothing at all
		// — and it says it on a removal record, which is the permanent record
		// of why a piece of music was deleted.
		fmt.Sprintf("re-encoded from %s to %s under the transcoding setting",
			placement.FromFormat, transcode.TargetMP3),
	).Scan(&removalID); err != nil {
		return uuid.Nil, fmt.Errorf("record the removal of %s: %w", file.path, err)
	}

	// Every other column that describes the audio is refreshed or cleared here,
	// the same way scanner.go's upsertFile treats a file whose bytes changed
	// underneath its row: what was measured off the FLAC is not true of the MP3
	// that now lives at this path, and a column left holding it would be wrong
	// rather than merely stale. Loudness and the spectrum are cleared rather
	// than guessed at, so their own sweeps measure the file that is really
	// there; nothing here reopens the identity this file was already proven
	// against, and nothing here is read as evidence of anything.
	if _, err := transaction.Exec(ctx, `
		UPDATE library_files
		SET path = $2, size_bytes = $3, duration_ms = $4,
		    transcoded_from_format = $5, original_size_bytes = $6,
		    bit_rate_kbps = $7, sample_rate_hz = $8, channels = $9,
		    measured_duration_ms = $4, properties_read_at = now(),
		    fingerprint = NULL,
		    replaygain_track_gain = NULL, replaygain_track_peak = NULL,
		    loudness_measured_at = NULL,
		    spectral_cutoff_hz = NULL, audio_encoder = NULL,
		    transcode_suspected = NULL, spectrum_read_at = NULL,
		    updated_at = now()
		WHERE id = $1
	`, file.id, destination, placement.SizeBytes, written.DurationMS,
		placement.FromFormat, placement.OriginalSizeBytes,
		written.BitRateKbps, written.SampleRateHz, written.Channels,
	); err != nil {
		return uuid.Nil, fmt.Errorf("move the library row for %s onto its smaller copy: %w", file.path, err)
	}

	if err := transaction.Commit(ctx); err != nil {
		return uuid.Nil, err
	}
	return removalID, nil
}

func durationDifference(left, right int) int {
	if left > right {
		return left - right
	}
	return right - left
}

// unlink deletes the original file's bytes and stamps the removal row with
// what happened. It is releaseCopy's unlink from keepone.go, read again: a
// library_files row now at this exact path is refused first, because the row
// going and this unlink are two separate commits and a scan between them could
// have already claimed this path as different music.
func (sweep *TranscodeSweep) unlink(ctx context.Context, removalID uuid.UUID, path string) error {
	var owned bool
	if err := sweep.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM library_files WHERE path = $1)`, path,
	).Scan(&owned); err != nil {
		return fmt.Errorf("check whether a path is still owned before deleting its audio: %w", err)
	}
	if owned {
		sweep.recordUnlinkError(ctx, removalID, "a library file now owns this path")
		return errors.New("a library file now owns this path")
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		sweep.recordUnlinkError(ctx, removalID, err.Error())
		return err
	}
	if _, err := sweep.pool.Exec(ctx, `
		UPDATE library_file_removals SET unlinked_at = now(), unlink_error = '' WHERE id = $1
	`, removalID); err != nil {
		return fmt.Errorf("record that the original left the disc: %w", err)
	}
	return nil
}

func (sweep *TranscodeSweep) recordUnlinkError(ctx context.Context, removalID uuid.UUID, detail string) {
	if _, err := sweep.pool.Exec(ctx, `
		UPDATE library_file_removals SET unlink_error = $2 WHERE id = $1
	`, removalID, detail); err != nil {
		sweep.logger.Error().Err(err).Str("removal_id", removalID.String()).
			Msg("record why an original file was left on disc")
	}
}

// next reads the next batch of files eligible to be shrunk: present, proven —
// a MusicBrainz recording is resolved onto them, which is the one thing that
// makes shrinking them safe to do without asking matching anything — and not
// already looked at by this pass.
func (sweep *TranscodeSweep) next(ctx context.Context, after uuid.UUID) ([]existingFile, error) {
	rows, err := sweep.pool.Query(ctx, `
		SELECT files.id, files.path, files.size_bytes, files.duration_ms,
		       files.musicbrainz_recording_id, roots.path
		FROM library_files files
		LEFT JOIN library_roots roots ON files.path LIKE roots.path || '/%'
		WHERE files.missing_at IS NULL
		  AND files.resolution_status = 'resolved'
		  AND files.musicbrainz_recording_id IS NOT NULL
		  AND files.transcoded_from_format IS NULL
		  AND files.transcode_checked_at IS NULL
		  AND files.id > $1
		ORDER BY files.id
		LIMIT $2
	`, after, transcodeBatch)
	if err != nil {
		return nil, fmt.Errorf("read the files a shrink pass has not looked at: %w", err)
	}
	defer rows.Close()

	var files []existingFile
	for rows.Next() {
		var file existingFile
		var root *string
		if err := rows.Scan(
			&file.id, &file.path, &file.sizeBytes, &file.durationMS,
			&file.recordingID, &root,
		); err != nil {
			return nil, err
		}
		if root != nil {
			file.rootPath = *root
		}
		files = append(files, file)
	}
	return files, rows.Err()
}

// LatestSweep is the most recent pass: what it is doing or what it did, and how
// much of the library is eligible under the policy stored right now.
func (sweep *TranscodeSweep) LatestSweep(ctx context.Context) (TranscodeStatus, error) {
	status := TranscodeStatus{Status: "idle"}
	var payload []byte
	err := sweep.pool.QueryRow(ctx, `
		WITH latest AS (
			SELECT status, error_message, completed_at, payload
			FROM jobs
			WHERE kind = 'sweep_transcode'
			ORDER BY created_at DESC
			LIMIT 1
		)
		SELECT latest.status, coalesce(latest.error_message, ''),
		       (SELECT min(started_at) FROM jobs
		        WHERE kind = 'sweep_transcode'
		          AND payload->>'runId' = latest.payload->>'runId'),
		       latest.completed_at, latest.payload
		FROM latest
	`).Scan(&status.Status, &status.Error, &status.StartedAt, &status.CompletedAt, &payload)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return TranscodeStatus{}, fmt.Errorf("read the last pass to shrink the library: %w", err)
	}
	if err == nil && len(payload) > 0 {
		if err := json.Unmarshal(payload, &status.Run); err != nil {
			return TranscodeStatus{}, fmt.Errorf("read the pass to shrink the library: %w", err)
		}
	}
	if err := sweep.pool.QueryRow(ctx, `
		SELECT count(*)
		FROM library_files
		WHERE missing_at IS NULL
		  AND resolution_status = 'resolved'
		  AND musicbrainz_recording_id IS NOT NULL
		  AND transcoded_from_format IS NULL
		  AND transcode_checked_at IS NULL
	`).Scan(&status.Eligible); err != nil {
		return TranscodeStatus{}, fmt.Errorf("count the files a shrink pass covers: %w", err)
	}
	return status, nil
}

func (sweep *TranscodeSweep) run(ctx context.Context, jobID uuid.UUID) (TranscodeRun, error) {
	var payload []byte
	if err := sweep.pool.QueryRow(ctx, `
		SELECT payload FROM jobs WHERE id = $1
	`, jobID).Scan(&payload); err != nil {
		return TranscodeRun{}, fmt.Errorf("read the pass to shrink the library %s: %w", jobID, err)
	}
	var run TranscodeRun
	if err := json.Unmarshal(payload, &run); err != nil {
		return TranscodeRun{}, fmt.Errorf("read the pass to shrink the library %s: %w", jobID, err)
	}
	return run, nil
}

func (sweep *TranscodeSweep) record(ctx context.Context, jobID uuid.UUID, run TranscodeRun) error {
	payload, err := json.Marshal(run)
	if err != nil {
		return fmt.Errorf("record the pass to shrink the library %s: %w", jobID, err)
	}
	if _, err := sweep.pool.Exec(ctx, `
		UPDATE jobs SET payload = $2, updated_at = now() WHERE id = $1
	`, jobID, payload); err != nil {
		return fmt.Errorf("record the pass to shrink the library %s: %w", jobID, err)
	}
	return nil
}
