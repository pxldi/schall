package db

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// FileToMeasureRow is one library file the spectrum sweep still owes a look, and
// the two numbers the decode needs: the sample rate it must not resample away
// from, and roughly where the middle of the audio is.
type FileToMeasureRow struct {
	ID           uuid.UUID
	Path         string
	SampleRateHz int
	DurationMS   int
	BitRateKbps  int
}

// FilesToMeasure lists files whose audio has never been decoded for its
// spectrum, oldest first.
//
// Only a file whose headers were read already is offered. A file with no sample
// rate is one taglib could make no sense of, and decoding it would be asking a
// second program the question the first one already failed to answer — and
// without a sample rate the decode would have to resample, which removes the top
// of the range itself and would measure Schall rather than the file.
func (q *Queries) FilesToMeasure(ctx context.Context, limit int32) ([]FileToMeasureRow, error) {
	rows, err := q.db.Query(ctx, `
		SELECT id, path, sample_rate_hz,
		       coalesce(measured_duration_ms, duration_ms, 0),
		       coalesce(bit_rate_kbps, 0)
		FROM library_files
		WHERE spectrum_read_at IS NULL
		  AND sample_rate_hz IS NOT NULL
		  AND missing_at IS NULL
		ORDER BY scanned_at
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	files := []FileToMeasureRow{}
	for rows.Next() {
		var file FileToMeasureRow
		if err := rows.Scan(&file.ID, &file.Path, &file.SampleRateHz,
			&file.DurationMS, &file.BitRateKbps); err != nil {
			return nil, err
		}
		files = append(files, file)
	}
	return files, rows.Err()
}

// FileSpectrumParams is what one look at a file's audio found. A look that
// concluded nothing passes Judged false, and the three facts are stored as null:
// nothing was learned, which is not the same as a file that is fine.
type FileSpectrumParams struct {
	FileID             uuid.UUID
	Judged             bool
	CutoffHz           int
	Encoder            string
	TranscodeSuspected bool
}

// RecordFileSpectrum writes down what the audio in one file turned out to be.
//
// The row is stamped as looked at whichever way the look went, so a file that
// says nothing is asked once rather than every pass. updated_at is deliberately
// left alone: this changes nothing anybody is waiting on, and touching it would
// send every file in the library through whatever watches that column.
func (q *Queries) RecordFileSpectrum(ctx context.Context, params FileSpectrumParams) error {
	cutoff := 0
	encoder := ""
	var suspected *bool
	if params.Judged {
		cutoff, encoder = params.CutoffHz, params.Encoder
		suspected = &params.TranscodeSuspected
	}
	_, err := q.db.Exec(ctx, `
		UPDATE library_files
		SET spectral_cutoff_hz = nullif($2::integer, 0),
		    audio_encoder = nullif($3::text, ''),
		    transcode_suspected = $4::boolean,
		    spectrum_read_at = now()
		WHERE id = $1
	`, params.FileID, cutoff, encoder, suspected)
	return err
}

// QueueSpectrumSweep schedules the next pass over the library's audio.
//
// One pass at a time, enforced by the partial unique index migration 00060
// adds. A pass that is running writes nothing and reports no error: the row the
// index found is 'running', so the WHERE on the update matches nothing, and the
// pass under way is the answer the caller wanted.
func (q *Queries) QueueSpectrumSweep(ctx context.Context, runAfter time.Time) error {
	_, err := q.db.Exec(ctx, `
		INSERT INTO jobs (kind, payload, max_attempts, run_after)
		VALUES ('sweep_audio_spectrum', '{}'::jsonb, 3, $1)
		ON CONFLICT (kind)
		    WHERE kind = 'sweep_audio_spectrum' AND status IN ('queued', 'running')
		DO UPDATE
		SET run_after = least(jobs.run_after, EXCLUDED.run_after),
		    updated_at = now()
		WHERE jobs.status = 'queued'
	`, runAfter)
	return err
}

// EnsureSpectrumSweepQueued adds a pass only when none is queued and none is
// running. An existing pass keeps the time it holds.
//
// Startup is the only caller. It means "there must be a pass on the books", not
// "measure the library now": a pass that found no decoder waits before asking
// again, and a pod replaced inside that wait must not cancel it.
func (q *Queries) EnsureSpectrumSweepQueued(ctx context.Context, runAfter time.Time) error {
	_, err := q.db.Exec(ctx, `
		INSERT INTO jobs (kind, payload, max_attempts, run_after)
		VALUES ('sweep_audio_spectrum', '{}'::jsonb, 3, $1)
		ON CONFLICT (kind)
		    WHERE kind = 'sweep_audio_spectrum' AND status IN ('queued', 'running')
		DO NOTHING
	`, runAfter)
	return err
}
