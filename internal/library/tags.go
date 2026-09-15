package library

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	"github.com/pxldi/schall/internal/loudness"
	"github.com/pxldi/schall/internal/tagging"
)

// An import ends by writing into the file what Schall proved it to be, and
// everything that was in the library before that was written stayed as its
// sender left it: one album under two spellings, a credit nobody has heard of,
// a track with no album at all. The music server groups by tags, so what the
// user sees of a library Schall has matched track for track is still the
// stranger's account of it.
//
// This is the same write, run over the music already here. Nothing decides
// anything: a file is written from the catalogue track it is already mapped to,
// and a file mapped to nothing is not read, not copied and not touched.

// tagBatch is how many files one pass writes before it goes back to the queue.
// Each one is a copy of the whole file, so this is a fraction of the layout
// migration's batch: the worker running it is one goroutine working every other
// kind of job in turn, and a library is tens of thousands of files. A batch
// that finds more waiting queues its own replacement immediately, so a run
// still proceeds at full speed and yields between batches.
const tagBatch = 100

// TagRun is one pass over the library as it stands: where it has reached and
// what it has done. It travels in the job's payload, which is where a run that
// spans many jobs can keep count without a table of its own — nothing here
// outlives the run, and a run is read while it happens rather than afterwards.
type TagRun struct {
	RunID uuid.UUID `json:"runId"`
	// After is the last file the run reached. Files are taken in order of their
	// identifier, which nothing changes, so a run killed halfway resumes at the
	// file it had got to rather than at the beginning.
	After uuid.UUID `json:"after,omitempty"`
	// Written is files rewritten; Unchanged is files that already said exactly
	// what Schall would have written; Skipped is files it would have had to
	// guess about; Failed is files that would not take the write.
	Written   int `json:"written"`
	Unchanged int `json:"unchanged"`
	Skipped   int `json:"skipped"`
	Failed    int `json:"failed"`
	// Skipped counts two different silences, and a run that only gives the total
	// leaves the reader to guess which one they are looking at. SkippedAmbiguous
	// is a file that answers the same recording on more than one release with
	// nothing to say which of them it is a copy of; SkippedUnproved is a file the
	// catalogue holds too little about to describe at all. Both are counted in
	// Skipped as well, so the total stands on its own where the reasons are not
	// wanted.
	SkippedAmbiguous int `json:"skippedAmbiguous"`
	SkippedUnproved  int `json:"skippedUnproved"`
}

// TagStatus is a run as the settings page reads it.
type TagStatus struct {
	Status      string
	Error       string
	StartedAt   *time.Time
	CompletedAt *time.Time
	Run         TagRun
	// Eligible is how many files the library holds that are mapped to a
	// catalogue track — what a run covers, whether or not one has been asked
	// for. It is the denominator the counts above are read against.
	Eligible int
}

// TagJobRow is the queued pass, answered to whoever asked for one.
type TagJobRow struct {
	ID        uuid.UUID
	Status    string
	CreatedAt time.Time
}

// Tagger writes into the files already in the library what Schall proved them
// to be.
type Tagger struct {
	pool   *pgxpool.Pool
	logger zerolog.Logger
}

func NewTagger(pool *pgxpool.Pool, logger zerolog.Logger) *Tagger {
	return &Tagger{pool: pool, logger: logger}
}

// QueueTagging asks for a pass over the library, and answers with the one
// already under way where there is one. A second pass beside the first would
// write the same files from the same catalogue at the same time, which is two
// workers doing one job's work and two processes copying one file.
//
// The partial unique index migration 00077 adds is what makes the second half
// of that safe: this asks and the weekly sweep below asks the same way, so a
// press landing beside a scheduled run is the database refusing the second
// insert rather than two rows racing each other into the table.
func (tagger *Tagger) QueueTagging(ctx context.Context) (TagJobRow, error) {
	var row TagJobRow
	err := tagger.pool.QueryRow(ctx, `
		WITH queued AS (
			INSERT INTO jobs (kind, payload, max_attempts)
			VALUES ('tag_library', jsonb_build_object('runId', gen_random_uuid()::text), 5)
			ON CONFLICT (kind)
			    WHERE kind = 'tag_library' AND status IN ('queued', 'running')
			DO NOTHING
			RETURNING id, status, created_at
		)
		SELECT id, status, created_at FROM queued
		UNION ALL
		SELECT id, status, created_at
		FROM jobs
		WHERE kind = 'tag_library' AND status IN ('queued', 'running')
		ORDER BY created_at
		LIMIT 1
	`).Scan(&row.ID, &row.Status, &row.CreatedAt)
	if err != nil {
		return TagJobRow{}, fmt.Errorf("queue a pass over the library's tags: %w", err)
	}
	return row, nil
}

// QueueNextTagging carries a run on into another job, from exactly where this
// one left it. The run is the payload, so the next job is this one's payload
// and nothing else has to know what a run is.
func (tagger *Tagger) QueueNextTagging(ctx context.Context, jobID uuid.UUID) error {
	_, err := tagger.pool.Exec(ctx, `
		INSERT INTO jobs (kind, payload, max_attempts)
		SELECT 'tag_library', jobs.payload, jobs.max_attempts
		FROM jobs
		WHERE jobs.id = $1
		ON CONFLICT (kind)
		    WHERE kind = 'tag_library' AND status IN ('queued', 'running')
		DO NOTHING
	`, jobID)
	if err != nil {
		return fmt.Errorf("queue the rest of the pass over the library's tags: %w", err)
	}
	return nil
}

// QueueLibrarySweep asks for a pass over the library's tags at a moment,
// moving an existing request earlier rather than adding a second one — the
// same shape QueueLyricsSweep and QueueSpectrumSweep give their own sweeps.
// It is how a finished pass schedules its successor a week out.
func (tagger *Tagger) QueueLibrarySweep(ctx context.Context, runAfter time.Time) error {
	_, err := tagger.pool.Exec(ctx, `
		INSERT INTO jobs (kind, payload, max_attempts, run_after)
		VALUES ('tag_library', jsonb_build_object('runId', gen_random_uuid()::text), 5, $1)
		ON CONFLICT (kind)
		    WHERE kind = 'tag_library' AND status IN ('queued', 'running')
		DO UPDATE
		SET run_after = least(jobs.run_after, EXCLUDED.run_after),
		    updated_at = now()
		WHERE jobs.status = 'queued'
	`, runAfter)
	if err != nil {
		return fmt.Errorf("queue the next pass over the library's tags: %w", err)
	}
	return nil
}

// EnsureLibrarySweepQueued adds a pass only when none is queued and none is
// running, and leaves an existing one exactly where it is.
//
// Startup is the only caller. It means "there must be a weekly pass on the
// books", not "write the library's tags now": a pass already queued, whether
// by the button or by last week's sweep, keeps the time it holds.
func (tagger *Tagger) EnsureLibrarySweepQueued(ctx context.Context, runAfter time.Time) error {
	_, err := tagger.pool.Exec(ctx, `
		INSERT INTO jobs (kind, payload, max_attempts, run_after)
		VALUES ('tag_library', jsonb_build_object('runId', gen_random_uuid()::text), 5, $1)
		ON CONFLICT (kind)
		    WHERE kind = 'tag_library' AND status IN ('queued', 'running')
		DO NOTHING
	`, runAfter)
	if err != nil {
		return fmt.Errorf("queue the library's weekly tagging pass: %w", err)
	}
	return nil
}

// Tag writes one batch of files and reports whether more are waiting.
//
// An error here is the run being unworkable — the database is gone, the job is
// not there — and never a file that could not be written. A file is a line in
// the log and the run moves past it: a pass that stopped at the first file with
// a read-only bit would leave the library in two accounts of itself, and the
// file it stopped at is exactly the one nobody can do anything about.
func (tagger *Tagger) Tag(ctx context.Context, jobID uuid.UUID) (bool, error) {
	run, err := tagger.run(ctx, jobID)
	if err != nil {
		return false, err
	}
	files, err := tagger.mapped(ctx, run.After)
	if err != nil {
		return false, err
	}
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		tagger.tag(ctx, &run, file)
		// Recorded a file at a time rather than a batch at a time, so that a
		// pass killed halfway is resumed rather than repeated, and so that what
		// the settings page shows moves while the run does.
		run.After = file.fileID
		if err := tagger.record(ctx, jobID, run); err != nil {
			return false, err
		}
	}
	return len(files) == tagBatch, nil
}

// TagFile writes the catalogue's account into one file, and reports whether the
// file was rewritten.
//
// A pass covers the library as it stood when somebody pressed the button, and
// matching does not stop when they let go of it. A file matched a minute later
// keeps the account it arrived with until the next press: one file left saying
// 2023 beside an album that says 2023-06-09 is the same album twice in a music
// server, and nobody finds that out except by noticing it. So a file is written
// when it becomes a catalogue track's, rather than when a pass happens to reach
// it.
//
// It is the pass's own write, on one file, and it decides nothing the pass does
// not: a file that answers the same recording on more than one release is left
// exactly as it is, a file that already says what the catalogue says is not
// touched, and a file that went missing between the match and this job is not
// read at all. A run's counts are kept here only so the same write can be
// reused; nothing reads them, because a single file is a line in the log rather
// than a pass to be watched.
//
// A file that is mapped to no catalogue track at all is the same question
// answered the other way, and it is answered here rather than by a job of its
// own. What a file is mapped to is read when this job runs, not when it was
// asked for, so a file matched again in between is written from the catalogue
// and a file that stayed unmapped is given its own tags back. See untag.
func (tagger *Tagger) TagFile(ctx context.Context, fileID uuid.UUID) (bool, error) {
	files, err := tagger.one(ctx, fileID)
	if err != nil {
		return false, err
	}
	if len(files) == 0 {
		return tagger.untag(ctx, fileID)
	}
	var run TagRun
	for _, file := range files {
		tagger.tag(ctx, &run, file)
	}
	return run.Written > 0, nil
}

// untag gives a file back the tags it arrived with, and reports whether it had
// any to give back.
//
// Schall writes the catalogue's account into a file it proved to be a
// catalogue track's. A file can stop being that track's: somebody withdraws the
// decision they made by hand, somebody gives the track to another file, or a
// re-evaluation leaves the file answering nothing. The account in the file
// outlives the mapping it came from, so a music server — which groups by tags
// and knows nothing of mappings — goes on filing the file under an album
// nothing says it is from. The file is given back what it said before Schall
// wrote, which the write itself recorded inside it.
//
// Only a file Schall tagged is written. A file the library lost is left alone:
// there is nothing on disc to restore, and the mapping it lost is not the
// reason it is gone.
func (tagger *Tagger) untag(ctx context.Context, fileID uuid.UUID) (bool, error) {
	var file mappedFile
	err := tagger.pool.QueryRow(ctx, `
		SELECT files.id, files.path
		FROM library_files files
		WHERE files.missing_at IS NULL
		  AND files.id = $1
		  AND NOT EXISTS (
		      SELECT 1 FROM track_mappings
		      WHERE track_mappings.library_file_id = files.id
		  )
		  -- A file Schall holds by its address on another service was never a
		  -- catalogue track's, so it cannot have stopped being one. Its tags
		  -- were written from what that service published, by somebody who
		  -- pasted the address, and having no mapping is the ordinary state of
		  -- such a file rather than a decision that fell away. Giving it back
		  -- what it arrived with would take the track's name off it for good.
		  AND NOT EXISTS (
		      SELECT 1 FROM library_file_identities
		      WHERE library_file_identities.library_file_id = files.id
		        AND library_file_identities.kind = 'source'
		  )
	`, fileID).Scan(&file.fileID, &file.path)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read the file that is a catalogue track's no longer %s: %w", fileID, err)
	}
	restored, err := tagging.Revert(file.path)
	// A file the scan has not caught up with yet is nothing to do rather than a
	// failure. Every other refusal is worth another attempt: until the write
	// lands, the file goes on telling the music server it is a track the
	// catalogue does not give it.
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("give %s back the tags it arrived with: %w", file.path, err)
	}
	if !restored {
		return false, nil
	}
	tagger.follow(ctx, file)
	return true, nil
}

// LatestTagging is the most recent pass: what it is doing or what it did, and
// how much of the library it covers. A library nobody has ever asked about is
// idle rather than an error.
func (tagger *Tagger) LatestTagging(ctx context.Context) (TagStatus, error) {
	status := TagStatus{Status: "idle"}
	var payload []byte
	err := tagger.pool.QueryRow(ctx, `
		WITH latest AS (
			SELECT status, error_message, completed_at, payload
			FROM jobs
			WHERE kind = 'tag_library'
			ORDER BY created_at DESC
			LIMIT 1
		)
		SELECT latest.status, coalesce(latest.error_message, ''),
		       -- A run is many jobs, so it started when the first of them did.
		       (SELECT min(started_at) FROM jobs
		        WHERE kind = 'tag_library'
		          AND payload->>'runId' = latest.payload->>'runId'),
		       latest.completed_at, latest.payload
		FROM latest
	`).Scan(&status.Status, &status.Error, &status.StartedAt, &status.CompletedAt, &payload)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return TagStatus{}, fmt.Errorf("read the last pass over the library's tags: %w", err)
	}
	if err == nil && len(payload) > 0 {
		if err := json.Unmarshal(payload, &status.Run); err != nil {
			return TagStatus{}, fmt.Errorf("read the pass over the library's tags: %w", err)
		}
	}
	if err := tagger.pool.QueryRow(ctx, `
		SELECT count(*)
		FROM library_files files
		WHERE files.missing_at IS NULL
		  AND EXISTS (
		      SELECT 1 FROM track_mappings
		      WHERE track_mappings.library_file_id = files.id
		  )
	`).Scan(&status.Eligible); err != nil {
		return TagStatus{}, fmt.Errorf("count the files a pass over the tags covers: %w", err)
	}
	return status, nil
}

// mappedFile is one library file with one catalogue track it answers.
type mappedFile struct {
	fileID uuid.UUID
	path   string
	tags   tagging.Tags
	// ambiguous is a file that answers releases which disagree about what to
	// write. See choose.
	ambiguous bool
	// identityReleaseGroup is the release group Schall resolved this file to,
	// where one was resolved. It is not evidence of anything here; it is what
	// tells two releases carrying the same recording apart when the file
	// answers both. See choose.
	identityReleaseGroup *uuid.UUID
}

// mapped reads the next batch of files that are mapped to a catalogue track,
// with every track each of them answers.
//
// A file with no mapping is not read, so nothing further down has to remember
// to leave it alone. A file the library has lost is not read either: there is
// nothing on disc to write into. Nor is a file resolved to 'source': somebody
// decided by hand what track it holds by address rather than by recording, and
// writing catalogue tags over it would overwrite that decision's own tags with
// an unrelated match — matching's ReconcileFile already refuses to create a
// mapping for such a file, and this is the second place that would act on one
// if it ever existed anyway.
func (tagger *Tagger) mapped(ctx context.Context, after uuid.UUID) ([]mappedFile, error) {
	return tagger.describe(ctx, `
		SELECT files.id, files.path
		FROM library_files files
		WHERE files.missing_at IS NULL
		  AND files.resolution_status <> 'source'
		  AND files.id > $1
		  AND EXISTS (
		      SELECT 1 FROM track_mappings
		      WHERE track_mappings.library_file_id = files.id
		  )
		ORDER BY files.id
		LIMIT $2
	`, after, tagBatch)
}

// one reads a single file the same way a pass reads a batch of them.
//
// It is the same selection with a different way of naming which files: the
// tracks a file answers, the release each of them is on and the credits are
// read by one query for both, so a file written the moment it is matched and a
// file written by a pass are described identically. A file with no mapping, one
// the library has lost, or one resolved to 'source', selects nothing here
// exactly as it does there.
func (tagger *Tagger) one(ctx context.Context, fileID uuid.UUID) ([]mappedFile, error) {
	return tagger.describe(ctx, `
		SELECT files.id, files.path
		FROM library_files files
		WHERE files.missing_at IS NULL
		  AND files.resolution_status <> 'source'
		  AND files.id = $1
		  AND EXISTS (
		      SELECT 1 FROM track_mappings
		      WHERE track_mappings.library_file_id = files.id
		  )
	`, fileID)
}

// describe reads what the catalogue says about the files a selection names.
func (tagger *Tagger) describe(ctx context.Context, selection string, args ...any) ([]mappedFile, error) {
	rows, err := tagger.pool.Query(ctx, `
		WITH batch AS (`+selection+`)
		SELECT batch.id, batch.path,
		       tracks.title, tracks.disc_number, coalesce(tracks.track_number, 0),
		       tracks.musicbrainz_recording_id,
		       artists.name, albums.title, albums.musicbrainz_release_group_id,
		       editions.musicbrainz_release_id, coalesce(editions.release_date, ''),
		       identities.musicbrainz_release_group_id,
		       track_credit.entries, album_credit.entries,
		       -- What the MusicBrainz community voted this music is: the
		       -- release group's leading genre, and the artist's where the
		       -- release group has no votes. One genre, because a music server
		       -- browses by one. Empty where neither has been voted for, which
		       -- clears the tag rather than keeping a stranger's word for it.
		       coalesce(albums.genres[1], artists.genres[1], ''),
		       this_file.replaygain_track_gain,
		       coalesce(this_file.replaygain_track_peak, 0),
		       album_loudness.entries
		FROM batch
		-- The file's own row again, for how loud it was measured to be. It is
		-- read here rather than carried through the selection above so that
		-- both ways of naming a batch stay two lines of SQL each.
		JOIN library_files this_file ON this_file.id = batch.id
		JOIN track_mappings ON track_mappings.library_file_id = batch.id
		JOIN tracks ON tracks.id = track_mappings.track_id
		JOIN albums ON albums.id = tracks.album_id
		JOIN artists ON artists.id = albums.artist_id
		-- The edition is the release Schall chose for this album, and it is
		-- where both the identifier a music server groups by and the date come
		-- from. An album whose edition has not been chosen has neither, and
		-- writes neither.
		LEFT JOIN release_editions editions ON editions.album_id = albums.id
		LEFT JOIN library_file_identities identities ON identities.library_file_id = batch.id
		-- Who the catalogue says the track is by, and who it says the album is
		-- by: the two lists the importer writes into a file arriving today, so
		-- that a pass over the music already here describes it identically. The
		-- order is carried in the query, because the position in the list is
		-- what pairs each name with its own identifier when it is written.
		LEFT JOIN LATERAL (
			SELECT json_agg(
				json_build_object(
					'name', credited_name, 'artistId', musicbrainz_artist_id
				)
				ORDER BY position
			) AS entries
			FROM track_artist_credits
			WHERE track_artist_credits.track_id = tracks.id
		) track_credit ON true
		LEFT JOIN LATERAL (
			SELECT json_agg(
				json_build_object(
					'name', credited_name, 'artistId', musicbrainz_artist_id
				)
				ORDER BY position
			) AS entries
			FROM album_artist_credits
			WHERE album_artist_credits.album_id = albums.id
		) album_credit ON true
		-- How loud the whole record is, gathered as the measurements it would
		-- be worked out from rather than as a number. The arithmetic lives in
		-- internal/loudness and is not written twice.
		--
		-- "tracks" is how many the catalogue says the record has and "measured"
		-- is one entry for each track the library holds a measured file of, so
		-- the two being equal is the whole test of whether the record is here.
		-- A track the library holds twice contributes once: two copies of one
		-- track is one track of the record, and the lower of the two
		-- identifiers is picked so the answer does not change between passes.
		LEFT JOIN LATERAL (
			SELECT json_build_object(
				'tracks', (
					SELECT count(*) FROM tracks counted
					WHERE counted.album_id = albums.id
				),
				'measured', json_agg(json_build_object(
					'gain', loud.replaygain_track_gain,
					'peak', coalesce(loud.replaygain_track_peak, 0),
					'durationMs', loud.measured_duration_ms
				))
			) AS entries
			FROM (
				SELECT DISTINCT ON (album_tracks.id)
				       measured_files.replaygain_track_gain,
				       measured_files.replaygain_track_peak,
				       measured_files.measured_duration_ms
				FROM tracks album_tracks
				JOIN track_mappings album_mappings
				    ON album_mappings.track_id = album_tracks.id
				JOIN library_files measured_files
				    ON measured_files.id = album_mappings.library_file_id
				WHERE album_tracks.album_id = albums.id
				  AND measured_files.missing_at IS NULL
				  AND measured_files.replaygain_track_gain IS NOT NULL
				  AND measured_files.measured_duration_ms > 0
				ORDER BY album_tracks.id, measured_files.id
			) loud
		) album_loudness ON true
		ORDER BY batch.id, albums.id, tracks.id
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("read the library's mapped files: %w", err)
	}
	defer rows.Close()

	var (
		files   []mappedFile
		current []mappedFile
	)
	for rows.Next() {
		var (
			file         mappedFile
			trackNumber  int
			discNumber   int
			recordingID  *uuid.UUID
			groupID      *uuid.UUID
			releaseID    *uuid.UUID
			artist       string
			album        string
			title        string
			date         string
			trackCredits []byte
			albumCredits []byte
			genre        string
			trackGain    *float64
			trackPeak    float64
			albumMeasure []byte
		)
		if err := rows.Scan(
			&file.fileID, &file.path,
			&title, &discNumber, &trackNumber, &recordingID,
			&artist, &album, &groupID,
			&releaseID, &date, &file.identityReleaseGroup,
			&trackCredits, &albumCredits, &genre,
			&trackGain, &trackPeak, &albumMeasure,
		); err != nil {
			return nil, err
		}
		// The album's artist as one string, on the track as well as on the
		// album, because an album is filed under one artist in this catalogue;
		// and beside it the credits themselves, the track's own and the
		// album's. It is the same account the importer writes, so a file
		// already here and one arriving today are described identically.
		track, err := credits(trackCredits)
		if err != nil {
			return nil, fmt.Errorf("read the credit on the track %q answers: %w", file.path, err)
		}
		credited, err := credits(albumCredits)
		if err != nil {
			return nil, fmt.Errorf("read the credit on the album %q answers: %w", file.path, err)
		}
		file.tags = tagging.Tags{
			Title:        title,
			Album:        album,
			Artist:       artist,
			Artists:      track,
			AlbumArtist:  artist,
			AlbumArtists: credited,
			TrackNumber:  trackNumber,
			DiscNumber:   discNumber,
			Date:         date,
			Genre:        genre,
		}
		if recordingID != nil {
			file.tags.RecordingID = *recordingID
		}
		if releaseID != nil {
			file.tags.ReleaseID = *releaseID
		}
		if groupID != nil {
			file.tags.ReleaseGroupID = *groupID
		}
		if trackGain != nil {
			file.tags.ReplayGain = &tagging.ReplayGain{
				TrackGainDB: *trackGain,
				TrackPeak:   trackPeak,
				Album:       albumGain(albumMeasure),
			}
		}
		if len(current) > 0 && current[0].fileID != file.fileID {
			files = append(files, choose(current))
			current = current[:0]
		}
		current = append(current, file)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(current) > 0 {
		files = append(files, choose(current))
	}
	return files, nil
}

// albumMeasurement is how loud a record is, as the query hands the question
// over: how many tracks the catalogue says the record has, and the measurement
// of every track the library holds a measured file of.
type albumMeasurement struct {
	Tracks   int `json:"tracks"`
	Measured []struct {
		Gain       float64 `json:"gain"`
		Peak       float64 `json:"peak"`
		DurationMS int     `json:"durationMs"`
	} `json:"measured"`
}

// albumGain is one record levelled as one performance, or nothing.
//
// A record is only levelled when the whole of it is here and measured. Album
// gain is the loudness of the record played end to end, so a number worked out
// from nine tracks of a twelve-track record is the loudness of a record that
// does not exist — and a player following it would level the whole album by it.
// A record that is not all here writes its tracks' own gains and no album gain,
// which is exactly what a player falls back to.
//
// Anything unreadable is no album gain, not an error. The tags a file gets are
// what Schall proved about it, and a record it cannot level is a record it says
// nothing about; failing the write would cost the file its title as well.
func albumGain(measurement []byte) *tagging.AlbumGain {
	if len(measurement) == 0 {
		return nil
	}
	var held albumMeasurement
	if err := json.Unmarshal(measurement, &held); err != nil {
		return nil
	}
	if held.Tracks == 0 || len(held.Measured) != held.Tracks {
		return nil
	}
	tracks := make([]loudness.TrackOf, 0, len(held.Measured))
	for _, track := range held.Measured {
		tracks = append(tracks, loudness.TrackOf{
			Measured:   loudness.Measured{GainDB: track.Gain, Peak: track.Peak},
			DurationMS: track.DurationMS,
		})
	}
	album, ok := loudness.AlbumOf(tracks)
	if !ok {
		return nil
	}
	return &tagging.AlbumGain{GainDB: album.GainDB, Peak: album.Peak}
}

// creditEntry is one artist of a credit as the query hands it over: the name
// the release prints, and the artist MusicBrainz says that name is where it has
// one at all.
type creditEntry struct {
	Name     string     `json:"name"`
	ArtistID *uuid.UUID `json:"artistId"`
}

// credits is the catalogue's credit as the tagger writes it. Nothing held for
// the entity is no list, which is silence: the joined string stands on its own,
// and one cut out of it at the commas would turn a band with a comma in its
// name into two artists forever. An artist MusicBrainz has no entity for keeps
// its place with no identifier: the gap is the tagger's to answer, and it
// answers it by writing the names alone.
func credits(entries []byte) ([]tagging.Credit, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	var held []creditEntry
	if err := json.Unmarshal(entries, &held); err != nil {
		return nil, err
	}
	if len(held) == 0 {
		return nil, nil
	}
	written := make([]tagging.Credit, 0, len(held))
	for _, entry := range held {
		credit := tagging.Credit{Name: entry.Name}
		if entry.ArtistID != nil {
			credit.MusicBrainzArtistID = *entry.ArtistID
		}
		written = append(written, credit)
	}
	return written, nil
}

// choose settles which release a file is written as, where it answers more than
// one.
//
// One file answers the same recording on every release that lists it — an EP
// and the compilation that gathers it, a single and the album it was lifted
// from — and those releases disagree about the album name, the date and the
// position on the disc. Which of them the file is a copy of is a question
// nothing in a mapping answers, and the resolved identity of the file is the
// one thing in Schall that does: it names the release group the audio was
// identified against.
//
// Where that does not single one out, the file is left exactly as it is. An
// album picked from two would be a guess about which release the user owns,
// written into their file, and a wrong one files the same album under two names
// — the whole thing this exists to stop.
func choose(answers []mappedFile) mappedFile {
	if len(answers) == 1 {
		return answers[0]
	}
	identity := answers[0].identityReleaseGroup
	if identity != nil {
		var named []mappedFile
		for _, answer := range answers {
			if answer.tags.ReleaseGroupID == *identity {
				named = append(named, answer)
			}
		}
		if len(named) == 1 {
			return named[0]
		}
	}
	return mappedFile{fileID: answers[0].fileID, path: answers[0].path, ambiguous: true}
}

// tag writes one file, counting what became of it.
func (tagger *Tagger) tag(ctx context.Context, run *TagRun, file mappedFile) {
	if file.ambiguous {
		run.Skipped++
		run.SkippedAmbiguous++
		tagger.logger.Debug().Str("path", file.path).
			Msg("this file answers the same recording on more than one release, and nothing says which of them it is a copy of")
		return
	}
	if tagging.AlreadySays(file.path, file.tags) {
		run.Unchanged++
		return
	}
	err := tagging.Write(file.path, file.tags)
	// Too little proved is the ordinary case of a file nobody could describe —
	// a track with no title, an album with no artist — and it is not a failure.
	// The file keeps everything it came with, which is the point of the
	// refusal.
	if errors.Is(err, tagging.ErrNothingProved) {
		run.Skipped++
		run.SkippedUnproved++
		tagger.logger.Debug().Str("path", file.path).
			Msg("too little is proved about this file to write anything into it")
		return
	}
	if err != nil {
		run.Failed++
		tagger.logger.Warn().Err(err).Str("path", file.path).
			Msg("what Schall proved this file to be could not be written into it")
		return
	}
	run.Written++
	tagger.follow(ctx, file)
}

// follow tells the library what the file now weighs and when it was last
// written.
//
// The bytes changed, so the next scan would otherwise read the file as modified
// by somebody: it would re-read the tags, put the file back in the resolution
// queue and reconcile it again — for every file in the library, over an
// external provider that answers one request at a time. None of that would
// learn anything. What a scan reads out of a managed copy is the account the
// file arrived with, which this write preserved exactly, and how long the audio
// is, which no tag write touches. So the row is brought up to date with the
// write that Schall itself made, and the scan finds the file as it left it.
//
// A row that could not be updated is not a failed write. The file on disc is
// tagged, and the worst that follows is the scan doing that work anyway.
func (tagger *Tagger) follow(ctx context.Context, file mappedFile) {
	info, err := os.Stat(file.path)
	if err != nil {
		tagger.logger.Warn().Err(err).Str("path", file.path).
			Msg("read back the file that was tagged")
		return
	}
	if _, err := tagger.pool.Exec(ctx, `
		UPDATE library_files
		SET size_bytes = $2, modified_at = $3, updated_at = now()
		WHERE id = $1
	`, file.fileID, info.Size(), databaseTime(info.ModTime())); err != nil {
		tagger.logger.Warn().Err(err).Str("path", file.path).
			Msg("record what the tagged file now weighs")
	}
}

// run reads the pass this job is part of out of its own payload.
func (tagger *Tagger) run(ctx context.Context, jobID uuid.UUID) (TagRun, error) {
	var payload []byte
	if err := tagger.pool.QueryRow(ctx, `
		SELECT payload FROM jobs WHERE id = $1
	`, jobID).Scan(&payload); err != nil {
		return TagRun{}, fmt.Errorf("read the pass over the library's tags %s: %w", jobID, err)
	}
	var run TagRun
	if err := json.Unmarshal(payload, &run); err != nil {
		return TagRun{}, fmt.Errorf("read the pass over the library's tags %s: %w", jobID, err)
	}
	return run, nil
}

// record writes the run back onto the job carrying it, which is what the next
// job in the run is started from and what the settings page reads.
func (tagger *Tagger) record(ctx context.Context, jobID uuid.UUID, run TagRun) error {
	payload, err := json.Marshal(run)
	if err != nil {
		return fmt.Errorf("record the pass over the library's tags %s: %w", jobID, err)
	}
	if _, err := tagger.pool.Exec(ctx, `
		UPDATE jobs SET payload = $2, updated_at = now() WHERE id = $1
	`, jobID, payload); err != nil {
		return fmt.Errorf("record the pass over the library's tags %s: %w", jobID, err)
	}
	return nil
}
