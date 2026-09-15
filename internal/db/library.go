package db

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type LibraryRootRow struct {
	ID            uuid.UUID
	Path          string
	Enabled       bool
	LastScannedAt pgtype.Timestamptz
	CreatedAt     pgtype.Timestamptz
}

type LibrarySummaryRow struct {
	RootCount      int64
	FileCount      int64
	MissingCount   int64
	ErrorCount     int64
	MatchedCount   int64
	UnmatchedCount int64
	AmbiguousCount int64
	DuplicateCount int64
	// DuplicateToDecideCount is the part of DuplicateCount nobody has answered
	// yet. It is what a queue counts; the other is how much of the library is a
	// second copy, which is a fact about the library rather than a question.
	DuplicateToDecideCount int64
	// The resolution counts describe what Schall knows about the identity of
	// the music itself, which is a different question from whether the local
	// catalogue happens to hold a track to map it to.
	ResolvedCount    int64
	NeedsReviewCount int64
	ConflictCount    int64
	LocalOnlyCount   int64
	PendingCount     int64
	FailedCount      int64
	TotalSizeBytes   int64
	ScanStatus       string
	ScanError        pgtype.Text
	ScanStartedAt    pgtype.Timestamptz
	ScanCompletedAt  pgtype.Timestamptz
}

type LibraryFileRow struct {
	ID                     uuid.UUID
	Path                   string
	SizeBytes              int64
	ModifiedAt             time.Time
	ArtistTag              pgtype.Text
	AlbumTag               pgtype.Text
	TitleTag               pgtype.Text
	DiscNumber             pgtype.Int4
	TrackNumber            pgtype.Int4
	DurationMS             pgtype.Int4
	MusicBrainzRecordingID uuid.NullUUID
	ISRC                   pgtype.Text
	MissingAt              pgtype.Timestamptz
	ScanError              pgtype.Text
	MatchStatus            string
	MatchCandidateCount    int32
	// DuplicateKeptAt is when somebody said they meant to have this twice. Read
	// only while MatchStatus is "duplicate": it answers the duplicate question
	// and nothing else.
	DuplicateKeptAt   pgtype.Timestamptz
	MappingMethod     pgtype.Text
	MappingConfidence pgtype.Float4
	MappingManual     bool
	MappedTrackID     uuid.NullUUID
	ResolutionStatus  string
	ResolutionSummary pgtype.Text
	IdentityTitle     pgtype.Text
	IdentityArtist    pgtype.Text
	IdentityManual    bool
	// IdentitySource is the service a file Schall holds by address lives on —
	// "soundcloud" — and IdentityURL is the page it is published at. Both are
	// empty for a file the catalogue can name, which is every other file.
	IdentitySource string
	IdentityURL    string
	// IdentityRemixer is who edited the track, where the person naming it said
	// somebody other than the uploader recorded it. Empty everywhere else.
	IdentityRemixer        string
	IdentityCandidateCount int32
	SetAside               bool
	// What this file was before an import re-encoded it, and how big it was
	// then. Both are empty on every file that arrived as it is — which is every
	// file, unless somebody turned transcoding on.
	TranscodedFromFormat pgtype.Text
	OriginalSizeBytes    pgtype.Int8
}

// The two states a file can be in that are not a matching state.
//
// Every other value of Status names what matching concluded about a file. These
// two name what the scanner found, and they are in the same parameter because
// they answer the same question a reader is asking — which files am I looking
// at — and because the alternative was a second filter beside the first that
// only ever made sense one at a time.
//
// They exist because the library summary counts both and nothing could list
// either. A number on a screen that leads nowhere is a number nobody can act
// on: fifteen files the scanner could not read is a real problem, and until now
// the only way to find out which fifteen was to read the server's log.
const (
	// StatusMissing is a file the last scan did not find. The row is the record
	// that it was there.
	StatusMissing = "missing"
	// StatusUnreadable is a file that is on disc and that the scanner could not
	// read — a truncated download, a permission the volume does not grant, a
	// container nothing can decode.
	StatusUnreadable = "unreadable"
)

type ListLibraryFilesParams struct {
	Limit  int32
	Offset int32
	// Status is a matching state, or one of StatusMissing and StatusUnreadable.
	Status string
	// Resolution filters by what is known about the file's external identity,
	// which is a separate question from Status.
	Resolution string
	Query      string
	// Path asks for one exact file path. It is used when another feature already
	// knows where bytes landed; a fuzzy search would be a different file's answer.
	Path string
	// ID narrows the page to one file. Review is where every decision about a
	// file is made, so it has to be able to open a file that is not in its
	// queue — one somebody chose from the library rather than one that stopped
	// and asked. Filtering the list is how it reads that file, rather than a
	// second route to the same row under a different name.
	ID string
	// SetAside narrows the list to files with or without a set-aside row. A nil
	// value includes both, which is what the Library page needs to offer the
	// person a way to take the row back.
	SetAside *bool
	// Unidentified narrows the list to present files that are not set aside and
	// still need an identity or matching decision.
	Unidentified bool
}

type LibraryFilePage struct {
	Items []LibraryFileRow
	Total int64
}

type LibraryScanJobRow struct {
	ID        uuid.UUID
	Status    string
	CreatedAt time.Time
}

// LibraryFileRefRow is a file named only by what identifies it: which row it is
// and where it sits. It is what a caller holding a path wants back, and
// deliberately not the whole file — nothing that reads it is deciding anything
// about the music.
type LibraryFileRefRow struct {
	ID   uuid.UUID
	Path string
}

// LibraryFileByPath finds the present file at exactly this path, or
// pgx.ErrNoRows.
//
// Exact string equality and nothing else: this answers "which file is the one
// at this path", and the only honest answers are one file or none. A file the
// last scan found missing is none — the row is a record of something that was
// there, and pointing a playlist entry at it would claim the library holds
// music it does not.
func (q *Queries) LibraryFileByPath(ctx context.Context, path string) (LibraryFileRefRow, error) {
	var row LibraryFileRefRow
	err := q.db.QueryRow(ctx, `
		SELECT id, path FROM library_files WHERE path = $1 AND missing_at IS NULL
	`, path).Scan(&row.ID, &row.Path)
	return row, err
}

// LibraryFileByID names one present file, or pgx.ErrNoRows.
//
// Same rule as LibraryFileByPath, asked the other way round: a file the last
// scan found missing is no answer at all. The row for it records something that
// used to be on disc, and every caller here wants the bytes rather than the
// history — a player pointed at a file that is not there would spend a
// transcode finding that out.
func (q *Queries) LibraryFileByID(ctx context.Context, id uuid.UUID) (LibraryFileRefRow, error) {
	var row LibraryFileRefRow
	err := q.db.QueryRow(ctx, `
		SELECT id, path FROM library_files WHERE id = $1 AND missing_at IS NULL
	`, id).Scan(&row.ID, &row.Path)
	return row, err
}

func (q *Queries) ListLibraryRoots(ctx context.Context) ([]LibraryRootRow, error) {
	rows, err := q.db.Query(ctx, `
		SELECT id, path, enabled, last_scanned_at, created_at
		FROM library_roots
		ORDER BY path
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]LibraryRootRow, 0)
	for rows.Next() {
		var row LibraryRootRow
		if err := rows.Scan(&row.ID, &row.Path, &row.Enabled, &row.LastScannedAt, &row.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (q *Queries) CreateLibraryRoot(ctx context.Context, path string) (LibraryRootRow, error) {
	var row LibraryRootRow
	err := q.db.QueryRow(ctx, `
		INSERT INTO library_roots (path)
		VALUES ($1)
		RETURNING id, path, enabled, last_scanned_at, created_at
	`, path).Scan(&row.ID, &row.Path, &row.Enabled, &row.LastScannedAt, &row.CreatedAt)
	return row, err
}

func (q *Queries) LibrarySummary(ctx context.Context) (LibrarySummaryRow, error) {
	var row LibrarySummaryRow
	err := q.db.QueryRow(ctx, `
		WITH latest_scan AS (
			SELECT status, error_message, started_at, completed_at
			FROM jobs
			WHERE kind = 'scan_library'
			ORDER BY created_at DESC
			LIMIT 1
		)
		SELECT
			(SELECT count(*) FROM library_roots WHERE enabled),
			(SELECT count(*) FROM library_files WHERE missing_at IS NULL),
			(SELECT count(*) FROM library_files WHERE missing_at IS NOT NULL),
			(SELECT count(*) FROM library_files WHERE missing_at IS NULL AND scan_error IS NOT NULL),
			(SELECT count(*) FROM library_files WHERE missing_at IS NULL AND match_status = 'matched'),
			(SELECT count(*) FROM library_files WHERE missing_at IS NULL AND match_status = 'unmatched'),
			(SELECT count(*) FROM library_files WHERE missing_at IS NULL AND match_status = 'ambiguous'),
			-- Files whose track another file already holds. Counted apart from
			-- the ambiguous ones because nobody is being asked about them: it is
			-- how much of the library is a second copy of something.
			(SELECT count(*) FROM library_files WHERE missing_at IS NULL AND match_status = 'duplicate'),
			-- And the ones still waiting on somebody. Keeping a second copy on
			-- purpose is an answer, so it comes out of the count a queue shows.
			(SELECT count(*) FROM library_files
			 WHERE missing_at IS NULL AND match_status = 'duplicate'
			   AND duplicate_kept_at IS NULL),
			-- A file matched to a catalogue track has a proven identity by that
			-- fact, whether or not a provider was ever asked about it.
			(SELECT count(*) FROM library_files
			 WHERE missing_at IS NULL
			   AND (match_status = 'matched' OR resolution_status = 'resolved')),
			(SELECT count(*) FROM library_files
			 WHERE missing_at IS NULL AND match_status <> 'matched'
			   AND resolution_status = 'needs_review'),
			(SELECT count(*) FROM library_files
			 WHERE missing_at IS NULL AND match_status <> 'matched'
			   AND resolution_status = 'conflict'),
			(SELECT count(*) FROM library_files
			 WHERE missing_at IS NULL AND match_status <> 'matched'
			   AND resolution_status = 'local_only'),
			(SELECT count(*) FROM library_files
			 WHERE missing_at IS NULL AND match_status <> 'matched'
			   AND resolution_status = 'pending'),
			(SELECT count(*) FROM library_files
			 WHERE missing_at IS NULL AND match_status <> 'matched'
			   AND resolution_status = 'failed'),
			(SELECT coalesce(sum(size_bytes), 0) FROM library_files WHERE missing_at IS NULL),
			coalesce((SELECT status FROM latest_scan), 'idle'),
			(SELECT error_message FROM latest_scan),
			(SELECT started_at FROM latest_scan),
			(SELECT completed_at FROM latest_scan)
	`).Scan(
		&row.RootCount, &row.FileCount, &row.MissingCount, &row.ErrorCount,
		&row.MatchedCount, &row.UnmatchedCount, &row.AmbiguousCount,
		&row.DuplicateCount, &row.DuplicateToDecideCount,
		&row.ResolvedCount, &row.NeedsReviewCount, &row.ConflictCount,
		&row.LocalOnlyCount, &row.PendingCount, &row.FailedCount,
		&row.TotalSizeBytes, &row.ScanStatus, &row.ScanError,
		&row.ScanStartedAt, &row.ScanCompletedAt,
	)
	return row, err
}

// fileStatusPredicate is how $1 narrows the list.
//
// Three different columns answer to one parameter, so the branch is written out
// rather than assembled: an empty status is every file, the two scanner states
// read missing_at and scan_error, and everything else is a matching state. A
// file the scan lost is excluded from the unreadable list because it is not on
// disc to be unreadable — the row is a record of something that was there, and
// it has its own answer.
//
// prefix is empty in the count, where library_files is the only table, and
// "library_files." in the page, where it is not.
func fileStatusPredicate(prefix string) string {
	return `(
		    $1 = ''
		    OR ($1 = '` + StatusMissing + `' AND ` + prefix + `missing_at IS NOT NULL)
		    OR ($1 = '` + StatusUnreadable + `' AND ` + prefix + `missing_at IS NULL
		        AND ` + prefix + `scan_error IS NOT NULL)
		    OR ($1 NOT IN ('` + StatusMissing + `', '` + StatusUnreadable + `')
		        AND ` + prefix + `match_status = $1)
		  )`
}

func (q *Queries) ListLibraryFiles(ctx context.Context, params ListLibraryFilesParams) (LibraryFilePage, error) {
	var result LibraryFilePage
	if err := q.db.QueryRow(ctx, `
		SELECT count(*)
		FROM library_files
		WHERE`+fileStatusPredicate("")+`
		  AND ($2 = '' OR resolution_status = $2)
		  -- A search box is not a pattern language, so % and _ have to be
		  -- escaped to match themselves, and so does the escape character or
		  -- it would eat the escapes put in beside it. One regexp pass over
		  -- all three is what keeps that last part from depending on the order.
		  AND ($3 = '' OR concat_ws(' ', path, artist_tag, album_tag, title_tag)
		      ILIKE '%' || regexp_replace($3, '([\\%_])', '\\\1', 'g') || '%' ESCAPE '\')
			AND ($4 = '' OR id::text = $4)
			AND ($5 = '' OR path = $5)
			AND ($6::boolean IS NULL OR EXISTS (
				SELECT 1 FROM library_file_set_asides
				WHERE library_file_set_asides.library_file_id = library_files.id
			) = $6)
			AND (NOT $7::boolean OR (
				NOT EXISTS (SELECT 1 FROM library_file_set_asides WHERE library_file_set_asides.library_file_id = library_files.id)
				AND missing_at IS NULL
				AND (resolution_status IN ('needs_review', 'conflict') OR match_status = 'ambiguous')
			))
	`, params.Status, params.Resolution, params.Query, params.ID, params.Path,
		params.SetAside, params.Unidentified).Scan(&result.Total); err != nil {
		return result, err
	}
	rows, err := q.db.Query(ctx, `
		-- The page is chosen from the files alone and only then described. What
		-- describes a row is per-file work, and doing it before the limit would
		-- pay for the whole library to show twenty-five of it.
		WITH page AS (
			SELECT library_files.*
			FROM library_files
			WHERE`+fileStatusPredicate("library_files.")+`
			  AND ($2 = '' OR library_files.resolution_status = $2)
			  -- Escaped the same way as the count above.
			  AND ($3 = '' OR concat_ws(
			      ' ', library_files.path, library_files.artist_tag,
			      library_files.album_tag, library_files.title_tag
			  ) ILIKE '%' || regexp_replace($3, '([\\%_])', '\\\1', 'g') || '%' ESCAPE '\')
			AND ($6 = '' OR library_files.id::text = $6)
			AND ($7 = '' OR library_files.path = $7)
			AND ($8::boolean IS NULL OR EXISTS (
				SELECT 1 FROM library_file_set_asides
				WHERE library_file_set_asides.library_file_id = library_files.id
			) = $8)
			AND (NOT $9::boolean OR (
				NOT EXISTS (SELECT 1 FROM library_file_set_asides WHERE library_file_set_asides.library_file_id = library_files.id)
				AND library_files.missing_at IS NULL
				AND (library_files.resolution_status IN ('needs_review', 'conflict') OR library_files.match_status = 'ambiguous')
			))
			ORDER BY library_files.missing_at NULLS FIRST,
			         coalesce(library_files.artist_tag, ''),
			         coalesce(library_files.album_tag, ''),
			         coalesce(library_files.disc_number, 1),
			         library_files.track_number NULLS LAST, library_files.path
			LIMIT $4 OFFSET $5
		)
		SELECT page.id, page.path, page.size_bytes,
		       page.modified_at, page.artist_tag,
		       page.album_tag, page.title_tag,
		       page.disc_number, page.track_number,
		       page.duration_ms, page.musicbrainz_recording_id,
		       page.isrc, page.missing_at, page.scan_error,
		       page.match_status, page.match_candidate_count,
		       page.duplicate_kept_at,
		       mapping.method, mapping.confidence,
		       coalesce(mapping.is_manual, false), mapping.track_id,
		       page.resolution_status, page.resolution_summary,
		       page.transcoded_from_format, page.original_size_bytes,
		       library_file_identities.track_title, library_file_identities.artist_name,
		       coalesce(library_file_identities.is_manual, false),
		       -- Where a file Schall holds by address rather than by recording
		       -- came from, and the page it is published at. Both are empty for
		       -- every other file, which is every file the catalogue can name.
		       coalesce(library_file_identities.source, ''),
		       coalesce(library_file_identities.external_url, ''),
		       coalesce(library_file_identities.remixer_name, ''),
		       EXISTS (SELECT 1 FROM library_file_set_asides
		               WHERE library_file_set_asides.library_file_id = page.id),
		       (SELECT count(*)::int FROM library_file_candidates
		        WHERE library_file_candidates.library_file_id = page.id)
		FROM page
		-- One file answers the same recording on every release that lists it
		-- (migration 00033), so joining the mappings outright would list the file
		-- once per release it answers, against a count that only ever counted
		-- files. This is a list of files: one mapping describes the row, a
		-- decision made by hand before one made automatically, then the oldest —
		-- the order 00033's own down-migration keeps a file's mapping by.
		LEFT JOIN LATERAL (
		    SELECT track_mappings.method, track_mappings.confidence,
		           track_mappings.is_manual, track_mappings.track_id
		    FROM track_mappings
		    WHERE track_mappings.library_file_id = page.id
		    ORDER BY track_mappings.is_manual DESC, track_mappings.created_at,
		             track_mappings.id
		    LIMIT 1
		) AS mapping ON true
		LEFT JOIN library_file_identities
		       ON library_file_identities.library_file_id = page.id
		ORDER BY page.missing_at NULLS FIRST,
		         coalesce(page.artist_tag, ''),
		         coalesce(page.album_tag, ''),
		         coalesce(page.disc_number, 1),
		         page.track_number NULLS LAST, page.path
	`, params.Status, params.Resolution, params.Query, params.Limit, params.Offset,
		params.ID, params.Path, params.SetAside, params.Unidentified)
	if err != nil {
		return result, err
	}
	defer rows.Close()

	result.Items = make([]LibraryFileRow, 0)
	for rows.Next() {
		var row LibraryFileRow
		if err := rows.Scan(
			&row.ID, &row.Path, &row.SizeBytes, &row.ModifiedAt,
			&row.ArtistTag, &row.AlbumTag, &row.TitleTag, &row.DiscNumber,
			&row.TrackNumber, &row.DurationMS, &row.MusicBrainzRecordingID,
			&row.ISRC, &row.MissingAt, &row.ScanError, &row.MatchStatus,
			&row.MatchCandidateCount, &row.DuplicateKeptAt,
			&row.MappingMethod, &row.MappingConfidence,
			&row.MappingManual, &row.MappedTrackID, &row.ResolutionStatus,
			&row.ResolutionSummary,
			&row.TranscodedFromFormat, &row.OriginalSizeBytes,
			&row.IdentityTitle, &row.IdentityArtist,
			&row.IdentityManual, &row.IdentitySource, &row.IdentityURL,
			&row.IdentityRemixer,
			&row.SetAside,
			&row.IdentityCandidateCount,
		); err != nil {
			return result, err
		}
		result.Items = append(result.Items, row)
	}
	return result, rows.Err()
}

// SetDuplicateKept records, or withdraws, the answer that a second copy is
// meant to be there.
//
// It writes nothing about the music and moves no track: the file stays the
// duplicate it was found to be, and only stops being asked about. Withdrawing
// puts the question back exactly as it was, which is why the answer is a
// timestamp that can be cleared rather than a state the file is moved into.
func (q *Queries) SetDuplicateKept(ctx context.Context, fileID uuid.UUID, kept bool) error {
	tag, err := q.db.Exec(ctx, `
		UPDATE library_files
		SET duplicate_kept_at = CASE WHEN $2 THEN now() ELSE NULL END,
		    updated_at = now()
		WHERE id = $1
	`, fileID, kept)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// QueueLibraryWitnessFingerprintSweep makes sure the coordinator job exists.
// The coordinator only queues per-file jobs; it never decodes audio itself.
func (q *Queries) QueueLibraryWitnessFingerprintSweep(
	ctx context.Context, runAfter time.Time,
) error {
	_, err := q.db.Exec(ctx, `
		INSERT INTO jobs (kind, payload, max_attempts, run_after)
		VALUES ('sweep_library_witnesses', '{}'::jsonb, 3, $1)
		ON CONFLICT (kind)
		    WHERE kind = 'sweep_library_witnesses' AND status IN ('queued', 'running')
		DO UPDATE
		SET run_after = CASE WHEN jobs.status = 'queued'
		                      THEN least(jobs.run_after, EXCLUDED.run_after)
		                      ELSE jobs.run_after END,
		    payload = CASE WHEN jobs.status = 'running'
		                    THEN jsonb_set(jobs.payload, '{woken_while_running}', 'true'::jsonb, true)
		                    ELSE jobs.payload END,
		    updated_at = now()
		WHERE jobs.status IN ('queued', 'running')
	`, runAfter)
	return err
}

// QueueLibraryWitnessFingerprintJobs adds one decoding job per eligible library file.
// The active-file index prevents a coordinator and a scanner from scheduling
// the same file twice, while each job still decodes only one file.
func (q *Queries) QueueLibraryWitnessFingerprintJobs(
	ctx context.Context, limit int32,
) (int64, error) {
	rows, err := q.db.Query(ctx, `
		WITH waiting AS (
			SELECT library_files.id
			FROM library_files
			WHERE library_files.missing_at IS NULL
			  AND library_files.resolution_status = 'resolved'
			  AND library_files.witness_fingerprint IS NULL
			  AND EXISTS (
			      SELECT 1 FROM library_file_identities
			      WHERE library_file_identities.library_file_id = library_files.id
			        AND library_file_identities.kind = 'external'
			        AND (
			            library_file_identities.is_manual
			            OR regexp_replace(library_file_identities.method, '-and-release$', '')
			               = ANY ($2::text[])
			        )
			  )
			  -- A file whose decode failed for good stays out: queuing it again
			  -- every sweep would re-queue the same batch for ever once twenty
			  -- such files exist. It comes back when its failed job is revived
			  -- from the failed list or removed.
			  AND NOT EXISTS (
			      SELECT 1 FROM jobs
			      WHERE jobs.kind = 'fingerprint_library_witness'
			        AND jobs.status IN ('queued', 'running', 'failed')
			        AND jobs.payload->>'fileId' = library_files.id::text
			  )
			ORDER BY library_files.created_at, library_files.id
			LIMIT $1
		)
		INSERT INTO jobs (kind, payload, max_attempts)
		SELECT 'fingerprint_library_witness',
		       jsonb_build_object('fileId', waiting.id), 3
		FROM waiting
		ON CONFLICT (kind, (payload->>'fileId'))
		    WHERE kind = 'fingerprint_library_witness' AND status IN ('queued', 'running')
		DO NOTHING
		RETURNING id
	`, limit, settledProofs)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var queued int64
	for rows.Next() {
		queued++
	}
	return queued, rows.Err()
}

// QueueFileResolutions asks the providers about the files nobody has asked
// about yet. It is bounded on purpose: every job is at least one rate-limited
// provider request, so a first scan of a large library queues work in batches
// rather than filling the queue with thousands of jobs at once. The next scan
// picks up where this one left off.
//
// Files whose provider could not be reached are asked again too. That failure
// says nothing about the music, and leaving it to sit forever would turn an
// outage into a permanent state.
func (q *Queries) QueueFileResolutions(ctx context.Context, limit int32) (int64, error) {
	tag, err := q.db.Exec(ctx, `
		WITH unasked AS (
			SELECT id
			FROM library_files
			WHERE missing_at IS NULL
			  AND resolution_status IN ('pending', 'failed')
			ORDER BY created_at
			LIMIT $1
		)
		INSERT INTO jobs (kind, payload, max_attempts)
		SELECT 'resolve_library_file', jsonb_build_object('fileId', unasked.id), 3
		FROM unasked
		ON CONFLICT DO NOTHING
	`, limit)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// QueueFileResolution asks about one file on request. A resolution already
// queued or running for that file is returned as it is, because asking the
// provider the same question twice answers it no faster.
func (q *Queries) QueueFileResolution(ctx context.Context, fileID uuid.UUID) (LibraryScanJobRow, error) {
	var row LibraryScanJobRow
	err := q.db.QueryRow(ctx, `
		WITH file AS (
			SELECT id FROM library_files WHERE id = $1 AND missing_at IS NULL
		), inserted AS (
			INSERT INTO jobs (kind, payload, max_attempts)
			SELECT 'resolve_library_file', jsonb_build_object('fileId', file.id), 3
			FROM file
			ON CONFLICT DO NOTHING
			RETURNING id, status, created_at
		)
		SELECT id, status, created_at FROM inserted
		UNION ALL
		SELECT id, status, created_at
		FROM jobs
		WHERE kind = 'resolve_library_file'
		  AND payload->>'fileId' = $1::text
		  AND status IN ('queued', 'running')
		ORDER BY created_at
		LIMIT 1
	`, fileID).Scan(&row.ID, &row.Status, &row.CreatedAt)
	return row, err
}

func (q *Queries) QueueLibraryScan(ctx context.Context) (LibraryScanJobRow, error) {
	var row LibraryScanJobRow
	err := q.db.QueryRow(ctx, `
		WITH inserted AS (
			INSERT INTO jobs (kind, payload, max_attempts)
			VALUES ('scan_library', '{}'::jsonb, 1)
			ON CONFLICT (kind)
			    WHERE kind = 'scan_library' AND status IN ('queued', 'running')
			DO NOTHING
			RETURNING id, status, created_at
		)
		SELECT id, status, created_at FROM inserted
		UNION ALL
		SELECT id, status, created_at
		FROM jobs
		WHERE kind = 'scan_library' AND status IN ('queued', 'running')
		ORDER BY created_at
		LIMIT 1
	`).Scan(&row.ID, &row.Status, &row.CreatedAt)
	return row, err
}
