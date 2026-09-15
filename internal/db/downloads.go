package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// beginner is the part of a pool that a multi-statement query needs. Queries
// itself only requires DBTX, so a transaction-bound Queries cannot start
// another transaction, and says so rather than doing something surprising.
type beginner interface {
	Begin(context.Context) (pgx.Tx, error)
}

// DownloadRequestFile is one remote file a request covers. The JSON tags are
// the stored shape, so a request always reports the files the user inspected
// rather than whatever a later search happens to return.
type DownloadRequestFile struct {
	// Path is the remote name exactly as the peer reported it, which is what a
	// transfer has to ask for. Requests recorded before transfers existed have
	// no path, and cannot be started.
	Path            string `json:"path,omitempty"`
	Name            string `json:"name"`
	Extension       string `json:"extension"`
	SizeBytes       int64  `json:"sizeBytes"`
	BitRate         int    `json:"bitRate,omitempty"`
	DurationSeconds int    `json:"durationSeconds,omitempty"`
}

type DownloadRequestRow struct {
	ID uuid.UUID
	// A request is about a release somebody chose a folder for, or about a want
	// somebody is looking for one file for. Exactly one of the two is set, and
	// the schema refuses a request that is about neither.
	AlbumID    uuid.NullUUID
	AlbumTitle string
	ArtistID   uuid.NullUUID
	ArtistName string
	// AcquisitionTargetID is the want this request serves, with the entry it was
	// asked for as. A want carries no catalogue release, so the entry is what
	// names the music anywhere a release title would.
	AcquisitionTargetID uuid.NullUUID
	EntryArtist         string
	EntryTitle          string
	Provider            string
	SourceUsername      string
	SourceDirectory     string
	Status              string
	FileCount           int32
	ExpectedTrackCount  int32
	TotalSizeBytes      int64
	Format              string
	AverageBitRate      pgtype.Int4
	Score               float64
	Reasons             []string
	Files               []DownloadRequestFile
	RequestedAt         pgtype.Timestamptz
	StartedAt           pgtype.Timestamptz
	CancelledAt         pgtype.Timestamptz
	ErrorMessage        pgtype.Text
	ImportStatus        string
	ImportError         pgtype.Text
	ImportPath          pgtype.Text
	ImportedAt          pgtype.Timestamptz
	// ImportReviews is the append-only history behind ImportError, oldest first.
	ImportReviews []ImportReview
	// ImportDecisions are the per-track judgements recorded for this request
	// and not yet withdrawn. They are applied the next time validation runs.
	ImportDecisions []ImportDecision
	// DuplicateAcknowledgedAt and DuplicateEvidence record that the user was
	// shown what the library already holds against this release and asked for
	// the download anyway. Together they are why this request is allowed to
	// start; without them, a request that duplicates owned or unresolved music
	// is refused until someone answers for it.
	DuplicateAcknowledgedAt pgtype.Timestamptz
	DuplicateEvidence       *DuplicateEvidence
	// Progress, aggregated from the transfer rows belonging to this request.
	TransferCount    int32
	CompletedCount   int32
	FailedCount      int32
	QueuedCount      int32
	TransferredBytes int64
	// Transfers is the state of each file's transfer, so a request that settled
	// with one file missing can say which one rather than only how many.
	Transfers []FileTransfer
}

// FileTransfer is one file's transfer as the provider last reported it. Error
// is kept after a failure and survives a retry being queued, so the reason the
// last attempt failed stays readable while the next one is in flight.
type FileTransfer struct {
	Path             string `json:"path"`
	Status           string `json:"status"`
	TransferredBytes int64  `json:"transferredBytes"`
	Error            string `json:"error,omitempty"`
	// Attempt is which try this row is on: 1 until a retry requeues it.
	Attempt int32 `json:"attempt"`
	// Attempts is what every settled attempt came to, oldest first. The attempt
	// in flight is not in here — an attempt joins the history once it has
	// settled, so the last entry is the last thing that actually happened.
	Attempts []TransferAttempt `json:"attempts"`
}

// TransferAttempt is what one attempt at one file came to, in the provider's
// own words. It is written once and never revised: a later attempt appends
// rather than correcting an earlier one, because both of them happened.
type TransferAttempt struct {
	Attempt          int32     `json:"attempt"`
	Outcome          string    `json:"outcome"`
	Detail           string    `json:"detail,omitempty"`
	TransferredBytes int64     `json:"transferredBytes"`
	RecordedAt       time.Time `json:"recordedAt"`
}

type CreateDownloadRequestParams struct {
	AlbumID            uuid.UUID
	Provider           string
	SourceUsername     string
	SourceDirectory    string
	FileCount          int32
	ExpectedTrackCount int32
	TotalSizeBytes     int64
	Format             string
	AverageBitRate     pgtype.Int4
	Score              float64
	Reasons            []string
	Files              []DownloadRequestFile
	// DuplicateEvidence is what the user was shown and accepted, when the
	// library held anything against this release. It is nil when there was
	// nothing to answer for.
	DuplicateEvidence *DuplicateEvidence
}

type ListDownloadRequestsParams struct {
	AlbumID uuid.NullUUID
	Status  string
	// View is one of the piles the Downloads screen is read in, or empty for
	// every request. It is not the same thing as Status: what became of a copy
	// after it arrived is recorded in import_status, so "imported" and
	// "discarded" are answers from a second column.
	View   string
	Limit  int32
	Offset int32
}

type DownloadRequestPage struct {
	Items []DownloadRequestRow
	Total int64
	// Counts is how many requests each view holds under the same album and
	// status scope this page was read under. Every count and the page itself
	// come from one pass over one filter, so the figure on a view and the list
	// behind it cannot say different things.
	Counts DownloadRequestCounts
}

// The five named piles of the download list, as SQL. They do not overlap, so a
// request is in one of them or in none, and 'all' is always offered beside them
// so that a request in none is still reachable — an open transfer older than
// the hundred most recent rows could not be reached from the screen at all,
// which is what these views replaced.
//
// downloadViewOpen is word for word the rule the navigation badge counts by
// (active_download_count, queries/dashboard.sql). The badge and this list are
// the same question asked in two places, so they are written as one sentence.
const (
	downloadViewOpen   = `download_requests.status IN ('requested', 'started')`
	downloadViewFailed = `download_requests.status = 'failed'`
	// A copy is judged after it arrives, which is why these three read the
	// second column and still insist on a finished transfer.
	downloadViewReview    = `download_requests.status = 'completed' AND download_requests.import_status = 'needs_review'`
	downloadViewImported  = `download_requests.status = 'completed' AND download_requests.import_status = 'imported'`
	downloadViewDiscarded = `download_requests.status = 'completed' AND download_requests.import_status = 'discarded'`
)

var downloadRequestViews = map[string]string{
	"open":      downloadViewOpen,
	"review":    downloadViewReview,
	"imported":  downloadViewImported,
	"discarded": downloadViewDiscarded,
	"failed":    downloadViewFailed,
	"all":       "",
}

// ErrUnknownDownloadView is asked for a pile of the download list that does not
// exist. Handlers check the name before they ask, so this is the guard rather
// than the message a person reads.
var ErrUnknownDownloadView = errors.New("that is not a view of the download list")

// DownloadRequestView reports the condition behind a named view and whether the
// name is one at all. An empty name, like "all", is every request.
func DownloadRequestView(name string) (string, bool) {
	if name == "" {
		return "", true
	}
	condition, ok := downloadRequestViews[name]
	return condition, ok
}

// DownloadRequestCounts is how many requests each view of the list holds.
type DownloadRequestCounts struct {
	Open      int64 `json:"open"`
	Review    int64 `json:"review"`
	Imported  int64 `json:"imported"`
	Discarded int64 `json:"discarded"`
	Failed    int64 `json:"failed"`
	All       int64 `json:"all"`
}

// forView is the count of the view the page was read under, which is the total
// that view's pager is measured against.
func (counts DownloadRequestCounts) forView(name string) int64 {
	switch name {
	case "open":
		return counts.Open
	case "review":
		return counts.Review
	case "imported":
		return counts.Imported
	case "discarded":
		return counts.Discarded
	case "failed":
		return counts.Failed
	default:
		return counts.All
	}
}

const downloadRequestColumns = `
	download_requests.id, download_requests.album_id, coalesce(albums.title, ''),
	albums.artist_id, coalesce(artists.name, ''),
	download_requests.acquisition_target_id,
	coalesce(acquisition_targets.entry_artist, ''),
	coalesce(acquisition_targets.entry_title, ''),
	download_requests.provider,
	download_requests.source_username, download_requests.source_directory,
	download_requests.status, download_requests.file_count,
	download_requests.expected_track_count, download_requests.total_size_bytes,
	download_requests.format, download_requests.average_bit_rate,
	download_requests.score, download_requests.reasons, download_requests.files,
	download_requests.requested_at, download_requests.started_at,
	download_requests.cancelled_at, download_requests.error_message,
	download_requests.import_status, download_requests.import_error,
	download_requests.import_path, download_requests.imported_at,
	transfers.total, transfers.completed, transfers.failed, transfers.queued,
	transfers.transferred,
	reviews.entries, decisions.entries,
	download_requests.duplicate_acknowledged_at, download_requests.duplicate_evidence,
	file_transfers.entries
`

// ImportReview is one entry in an import's review history. Kind is 'paused',
// 'revalidated', or 'imported'; Detail carries the reason or the import path.
type ImportReview struct {
	Kind       string    `json:"kind"`
	Detail     string    `json:"detail"`
	RecordedAt time.Time `json:"recordedAt"`
	// Evidence is the full comparison behind a 'paused' entry, when validation
	// got far enough to compare files against the catalogue edition.
	Evidence *ImportEvidence `json:"evidence,omitempty"`
}

// The release and the want are both outer joins, because a request has one of
// them and not the other. An inner join on the album is how a single-file
// acquisition would silently disappear from every view that reads a request.
const downloadRequestJoins = `
	FROM download_requests
	LEFT JOIN albums ON albums.id = download_requests.album_id
	LEFT JOIN artists ON artists.id = albums.artist_id
	LEFT JOIN acquisition_targets
		ON acquisition_targets.id = download_requests.acquisition_target_id
	LEFT JOIN LATERAL (
		SELECT count(*)::int AS total,
		       count(*) FILTER (WHERE status = 'completed')::int AS completed,
		       count(*) FILTER (WHERE status = 'failed')::int AS failed,
		       -- A file the peer has accepted but not started sending. Schall
		       -- already knew this and threw it away, so a transfer sitting in
		       -- somebody's upload queue read as one that was moving.
		       count(*) FILTER (WHERE status = 'queued')::int AS queued,
		       coalesce(sum(transferred_bytes), 0)::bigint AS transferred
		FROM downloads
		WHERE downloads.download_request_id = download_requests.id
	) transfers ON true
	LEFT JOIN LATERAL (
		SELECT coalesce(
			json_agg(
				json_build_object(
					'kind', kind, 'detail', detail,
					'recordedAt', recorded_at, 'evidence', evidence
				)
				ORDER BY recorded_at, id
			),
			'[]'::json
		) AS entries
		FROM download_import_reviews
		WHERE download_import_reviews.download_request_id = download_requests.id
	) reviews ON true
	LEFT JOIN LATERAL (
		SELECT coalesce(
			json_agg(
				json_build_object(
					'id', download_import_decisions.id,
					'fileName', file_name, 'trackId', track_id,
					'trackTitle', tracks.title, 'note', coalesce(note, ''),
					'decidedAt', decided_at
				)
				ORDER BY file_name
			),
			'[]'::json
		) AS entries
		FROM download_import_decisions
		JOIN tracks ON tracks.id = download_import_decisions.track_id
		WHERE download_import_decisions.download_request_id = download_requests.id
	) decisions ON true
	LEFT JOIN LATERAL (
		SELECT coalesce(
			json_agg(
				json_build_object(
					'path', downloads.remote_path, 'status', downloads.status,
					'transferredBytes', downloads.transferred_bytes,
					'error', coalesce(downloads.error_message, ''),
					'attempt', downloads.attempt,
					'attempts', coalesce(attempts.entries, '[]'::json)
				)
				ORDER BY downloads.remote_path
			),
			'[]'::json
		) AS entries
		FROM downloads
		LEFT JOIN LATERAL (
			SELECT json_agg(
				json_build_object(
					'attempt', attempt, 'outcome', outcome,
					'detail', coalesce(detail, ''),
					'transferredBytes', transferred_bytes,
					'recordedAt', recorded_at
				)
				ORDER BY attempt
			) AS entries
			FROM download_transfer_attempts
			WHERE download_transfer_attempts.download_id = downloads.id
		) attempts ON true
		WHERE downloads.download_request_id = download_requests.id
	) file_transfers ON true
`

// CreateDownloadRequest records one chosen source for one release. It reports
// whether the request was newly created: a request for a folder that is already
// open returns the open one instead of a second row, so repeating the decision
// cannot turn into two acquisitions of the same music.
//
// The identifier is returned rather than the whole row, because a row inserted
// by this statement is not visible to a join in the same statement.
func (q *Queries) CreateDownloadRequest(ctx context.Context, params CreateDownloadRequestParams) (uuid.UUID, bool, error) {
	files, err := json.Marshal(params.Files)
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("encode requested files: %w", err)
	}
	if params.Reasons == nil {
		params.Reasons = []string{}
	}
	// A request recorded without duplicate evidence is one nothing stood
	// against, so there is nothing to have acknowledged.
	var duplicates []byte
	if params.DuplicateEvidence != nil {
		if duplicates, err = encodeDuplicateEvidence(*params.DuplicateEvidence); err != nil {
			return uuid.Nil, false, err
		}
	}

	var (
		id      uuid.UUID
		created bool
	)
	err = q.db.QueryRow(ctx, `
		WITH inserted AS (
			INSERT INTO download_requests (
				album_id, provider, source_username, source_directory,
				file_count, expected_track_count, total_size_bytes, format,
				average_bit_rate, score, reasons, files,
				duplicate_evidence,
				duplicate_acknowledged_at
			)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12::jsonb,
			        $13::jsonb,
			        CASE WHEN $13::jsonb IS NULL THEN NULL ELSE now() END)
			ON CONFLICT (album_id, provider, source_username, source_directory)
				WHERE status IN ('requested', 'started')
			DO NOTHING
			RETURNING id
		)
		SELECT id, true FROM inserted
		UNION ALL
		SELECT id, false
		FROM download_requests
		WHERE album_id = $1 AND provider = $2 AND source_username = $3
		  AND source_directory = $4 AND status IN ('requested', 'started')
		  AND NOT EXISTS (SELECT 1 FROM inserted)
		LIMIT 1
	`,
		params.AlbumID, params.Provider, params.SourceUsername, params.SourceDirectory,
		params.FileCount, params.ExpectedTrackCount, params.TotalSizeBytes, params.Format,
		params.AverageBitRate, params.Score, params.Reasons, files, duplicates,
	).Scan(&id, &created)
	// The fallback above reads the snapshot this statement began with, so a row
	// another caller committed while ON CONFLICT was waiting on its lock is
	// invisible to it and the statement returns nothing at all. Two clicks on
	// one candidate close enough together is exactly that race, and it must not
	// read as "there is no such request". A second statement sees the committed
	// row.
	if errors.Is(err, pgx.ErrNoRows) {
		err = q.db.QueryRow(ctx, `
			SELECT id
			FROM download_requests
			WHERE album_id = $1 AND provider = $2 AND source_username = $3
			  AND source_directory = $4 AND status IN ('requested', 'started')
		`,
			params.AlbumID, params.Provider, params.SourceUsername, params.SourceDirectory,
		).Scan(&id)
		return id, false, err
	}
	return id, created, err
}

// DownloadRequest returns one request, or pgx.ErrNoRows when it does not exist.
func (q *Queries) DownloadRequest(ctx context.Context, id uuid.UUID) (DownloadRequestRow, error) {
	return scanDownloadRequest(q.db.QueryRow(ctx, `
		SELECT`+downloadRequestColumns+downloadRequestJoins+`
		WHERE download_requests.id = $1
	`, id))
}

// CancelDownloadRequest withdraws an open request, whether or not its transfers
// were started, and marks any transfer that had not settled as cancelled. The
// row is kept as a record that the decision was made and then withdrawn. A
// request that is not open returns pgx.ErrNoRows.
//
// Stopping the transfers at the provider is the caller's job, and must happen
// before this, so a cancelled request cannot leave slskd downloading.
func (q *Queries) CancelDownloadRequest(ctx context.Context, id uuid.UUID) (DownloadRequestRow, error) {
	var cancelled uuid.UUID
	if err := q.db.QueryRow(ctx, `
		WITH withdrawn AS (
			UPDATE download_requests
			SET status = 'cancelled', cancelled_at = now(), updated_at = now()
			WHERE id = $1 AND status IN ('requested', 'started')
			RETURNING id
		), stopped AS (
			UPDATE downloads
			SET status = 'cancelled', completed_at = now(), updated_at = now()
			WHERE download_request_id = (SELECT id FROM withdrawn)
			  AND status NOT IN ('completed', 'failed', 'cancelled')
			RETURNING id, attempt, transferred_bytes
		), recorded AS (
			INSERT INTO download_transfer_attempts (
				download_id, attempt, outcome, detail, transferred_bytes
			)
			SELECT id, attempt, 'cancelled', 'withdrawn by request', transferred_bytes
			FROM stopped
			ON CONFLICT (download_id, attempt) DO NOTHING
			RETURNING id
		)
		SELECT id FROM withdrawn
	`, id).Scan(&cancelled); err != nil {
		return DownloadRequestRow{}, err
	}
	return q.DownloadRequest(ctx, cancelled)
}

// StartDownloadRequest moves a recorded request into the started state and
// creates one transfer row per requested file. It is the only place a request
// becomes startable, and it claims the request under a row lock, so two callers
// cannot enqueue the same folder twice.
//
// The rows are written before the provider is asked to enqueue anything: a
// transfer that exists at the provider but not in Schall would be invisible,
// while the reverse is recoverable and is what RevertDownloadRequestStart
// undoes. pgx.ErrNoRows means the request was not waiting to be started.
func (q *Queries) StartDownloadRequest(ctx context.Context, id uuid.UUID) (DownloadRequestRow, error) {
	connection, ok := q.db.(beginner)
	if !ok {
		return DownloadRequestRow{}, errors.New("starting a download requires a transactional connection")
	}
	tx, err := connection.Begin(ctx)
	if err != nil {
		return DownloadRequestRow{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	queries := q.WithTx(tx)
	var files []byte
	if scanErr := tx.QueryRow(ctx, `
		SELECT files
		FROM download_requests
		WHERE id = $1 AND status = 'requested'
		FOR UPDATE
	`, id).Scan(&files); scanErr != nil {
		return DownloadRequestRow{}, scanErr
	}

	var requested []DownloadRequestFile
	if unmarshalErr := json.Unmarshal(files, &requested); unmarshalErr != nil {
		return DownloadRequestRow{}, fmt.Errorf("decode requested files: %w", unmarshalErr)
	}
	if len(requested) == 0 {
		return DownloadRequestRow{}, errors.New("the request covers no files")
	}
	for _, file := range requested {
		if strings.TrimSpace(file.Path) == "" {
			return DownloadRequestRow{}, ErrNoRemotePath
		}
		if _, execErr := tx.Exec(ctx, `
			INSERT INTO downloads (
				download_request_id, album_id, provider, remote_path,
				source_username, source_path, size_bytes, status
			)
			SELECT $1, album_id, provider, $2, source_username, source_directory, $3, 'queued'
			FROM download_requests
			WHERE id = $1
			ON CONFLICT (download_request_id, remote_path)
				WHERE download_request_id IS NOT NULL
			DO NOTHING
		`, id, file.Path, file.SizeBytes); execErr != nil {
			return DownloadRequestRow{}, fmt.Errorf("record transfer for %s: %w", file.Name, execErr)
		}
	}
	if _, execErr := tx.Exec(ctx, `
		UPDATE download_requests
		SET status = 'started', started_at = now(), error_message = NULL, updated_at = now()
		WHERE id = $1
	`, id); execErr != nil {
		return DownloadRequestRow{}, execErr
	}

	row, rowErr := queries.DownloadRequest(ctx, id)
	if rowErr != nil {
		return DownloadRequestRow{}, rowErr
	}
	return row, tx.Commit(ctx)
}

// ErrNoRemotePath reports a request stored before Schall recorded remote paths.
// Such a request cannot be started, because the exact name a peer advertised is
// the only thing it will serve.
var ErrNoRemotePath = errors.New("this request was recorded without remote file paths; search again and request the source once more")

// RevertDownloadRequestStart returns a request to the recorded state after the
// provider refused to enqueue it, keeping the reason so the interface can say
// what happened rather than silently showing a request that is not running.
func (q *Queries) RevertDownloadRequestStart(ctx context.Context, id uuid.UUID, message string) (DownloadRequestRow, error) {
	if _, err := q.db.Exec(ctx, `
		WITH reverted AS (
			UPDATE download_requests
			SET status = 'requested', started_at = NULL,
			    error_message = nullif($2, ''), updated_at = now()
			WHERE id = $1 AND status = 'started'
			RETURNING id
		)
		DELETE FROM downloads
		WHERE download_request_id IN (SELECT id FROM reverted)
		  AND status = 'queued'
	`, id, message); err != nil {
		return DownloadRequestRow{}, err
	}
	return q.DownloadRequest(ctx, id)
}

// ErrNothingToRetry reports a request that has no failed transfer to send back
// to the provider. A request that finished, was withdrawn, or is still moving
// is not something to retry.
var ErrNothingToRetry = errors.New("this request has no failed transfer to retry")

// ErrNotRetryable reports a named file that is not a failed transfer of this
// request. Naming one is refused rather than quietly skipped: a retry that
// silently covers fewer files than was asked for is a retry nobody can trust.
var ErrNotRetryable = errors.New("that file is not a failed transfer of this request")

// ErrWantHasAnotherCopy reports a copy refused because the want has already
// moved on to a different one — a retry pressed after the loop found another
// (RetryDownloadRequestFiles), or a first fetch whose want opened its one
// request while this pass was still choosing (FetchAcquiredFile). Nothing is
// lost by refusing: a copy that only failed to arrive is never struck off, so
// the want asks this peer for it again itself once the copy it is following
// settles.
var ErrWantHasAnotherCopy = errors.New("this wishlist entry is already following another copy")

// RetryDownloadRequestFiles sends failed transfers back to the provider without
// touching the ones that already arrived. Naming no path retries every failed
// file; naming paths retries exactly those, and refuses if any of them did not
// fail.
//
// The rows are updated rather than inserted, because a request already has one
// row per remote path and the unique index says so. The failure reason is
// deliberately left on the row: while the new attempt is queued, why the last
// one failed is still the only thing anyone knows about that file. It clears
// itself the moment the provider reports something newer.
//
// pgx.ErrNoRows means the request is not in a state a retry applies to.
func (q *Queries) RetryDownloadRequestFiles(
	ctx context.Context, id uuid.UUID, paths []string,
) (DownloadRequestRow, []DownloadRequestFile, error) {
	connection, ok := q.db.(beginner)
	if !ok {
		return DownloadRequestRow{}, nil, errors.New("retrying a download requires a transactional connection")
	}
	tx, err := connection.Begin(ctx)
	if err != nil {
		return DownloadRequestRow{}, nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	queries := q.WithTx(tx)
	var (
		stored   []byte
		targetID uuid.NullUUID
	)
	if scanErr := tx.QueryRow(ctx, `
		SELECT files, acquisition_target_id
		FROM download_requests
		WHERE id = $1 AND status = 'failed'
		FOR UPDATE
	`, id).Scan(&stored, &targetID); scanErr != nil {
		return DownloadRequestRow{}, nil, scanErr
	}
	// A want follows one copy at a time. While this request sat failed the loop
	// was free to move on, and sending it back to 'started' now would be a second
	// copy arriving for one want — which download_requests_open_target_idx
	// refuses as a constraint violation nobody can read. The predicate is the one
	// OpenTargetRequest uses, because a copy whose transfer finished is not done
	// with either: it is waiting to be checked.
	if targetID.Valid {
		var elsewhere bool
		if scanErr := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM download_requests
				WHERE acquisition_target_id = $1 AND id <> $2
				  AND (status IN ('requested', 'started')
				       OR (status = 'completed' AND import_status IN ('pending', 'validating')))
			)
		`, targetID.UUID, id).Scan(&elsewhere); scanErr != nil {
			return DownloadRequestRow{}, nil, scanErr
		}
		if elsewhere {
			return DownloadRequestRow{}, nil, ErrWantHasAnotherCopy
		}
	}
	var requested []DownloadRequestFile
	if unmarshalErr := json.Unmarshal(stored, &requested); unmarshalErr != nil {
		return DownloadRequestRow{}, nil, fmt.Errorf("decode requested files: %w", unmarshalErr)
	}

	failed, err := failedTransferPaths(ctx, tx, id)
	if err != nil {
		return DownloadRequestRow{}, nil, err
	}
	if len(failed) == 0 {
		return DownloadRequestRow{}, nil, ErrNothingToRetry
	}
	// An explicit list is checked against what actually failed before anything
	// is changed, so a request naming one good file and one bad one does not
	// half-happen.
	retry := failed
	if len(paths) > 0 {
		retry = make(map[string]bool, len(paths))
		for _, path := range paths {
			if !failed[path] {
				return DownloadRequestRow{}, nil, fmt.Errorf("%w: %q", ErrNotRetryable, path)
			}
			retry[path] = true
		}
	}

	files := make([]DownloadRequestFile, 0, len(retry))
	for _, file := range requested {
		if retry[file.Path] {
			files = append(files, file)
		}
	}
	// A failed transfer whose path the stored request no longer lists cannot be
	// asked for again: a peer serves only the name it advertised.
	if len(files) != len(retry) {
		return DownloadRequestRow{}, nil, ErrNoRemotePath
	}

	// attempt moves on, so what the last one came to stays readable as the
	// attempt it was rather than being overwritten by this one. queued_at moves
	// too: it is asked to enqueue again, now, and a stale queued_at would read
	// this retry as already stalled the moment the queue patience sweep next
	// looks at it.
	if _, execErr := tx.Exec(ctx, `
		UPDATE downloads
		SET status = 'queued',
		    attempt = attempt + 1,
		    provider_transfer_id = NULL,
		    transferred_bytes = 0,
		    started_at = NULL,
		    completed_at = NULL,
		    queued_at = now(),
		    updated_at = now()
		WHERE download_request_id = $1 AND status = 'failed' AND remote_path = ANY($2)
	`, id, keysOf(retry)); execErr != nil {
		return DownloadRequestRow{}, nil, fmt.Errorf("requeue failed transfers: %w", execErr)
	}
	// started_at is left as it was: the request did start when it says it did,
	// and a retry is another attempt at it rather than a new one.
	if _, execErr := tx.Exec(ctx, `
		UPDATE download_requests
		SET status = 'started', error_message = NULL, updated_at = now()
		WHERE id = $1
	`, id); execErr != nil {
		// The loop can open a request for this want between the check above and
		// this write. The index catches what the check would have, and it means
		// the same thing.
		if targetID.Valid && isUniqueViolation(execErr) {
			return DownloadRequestRow{}, nil, ErrWantHasAnotherCopy
		}
		return DownloadRequestRow{}, nil, execErr
	}

	row, rowErr := queries.DownloadRequest(ctx, id)
	if rowErr != nil {
		return DownloadRequestRow{}, nil, rowErr
	}
	return row, files, tx.Commit(ctx)
}

// RevertDownloadRequestRetry returns a refused retry to the failed state it came
// from, with the provider's reason against every file it covered.
//
// It is deliberately not RevertDownloadRequestStart: that one returns the whole
// request to 'requested' and deletes the queued rows, which here would erase
// the record of files that had already been transferred and failed once.
func (q *Queries) RevertDownloadRequestRetry(
	ctx context.Context, id uuid.UUID, paths []string, message string,
) (DownloadRequestRow, error) {
	// A provider that refuses the retry is a real outcome of that attempt, and
	// is recorded as one. Otherwise the history would show the first failure
	// twice over and never say that asking again was turned down.
	if _, err := q.db.Exec(ctx, `
		WITH reverted AS (
			UPDATE downloads
			SET status = 'failed',
			    error_message = nullif($3, ''),
			    completed_at = now(),
			    updated_at = now()
			WHERE download_request_id = $1 AND status = 'queued' AND remote_path = ANY($2)
			RETURNING id, attempt, transferred_bytes
		), recorded AS (
			INSERT INTO download_transfer_attempts (
				download_id, attempt, outcome, detail, transferred_bytes
			)
			SELECT id, attempt, 'failed', nullif($3, ''), transferred_bytes
			FROM reverted
			ON CONFLICT (download_id, attempt) DO NOTHING
			RETURNING id
		)
		UPDATE download_requests
		SET status = 'failed', error_message = nullif($3, ''), updated_at = now()
		WHERE id = $1 AND status = 'started'
	`, id, paths, message); err != nil {
		return DownloadRequestRow{}, err
	}
	return q.DownloadRequest(ctx, id)
}

// failedTransferPaths reports the remote paths of this request's failed
// transfers.
func failedTransferPaths(ctx context.Context, tx pgx.Tx, id uuid.UUID) (map[string]bool, error) {
	rows, err := tx.Query(ctx, `
		SELECT remote_path
		FROM downloads
		WHERE download_request_id = $1 AND status = 'failed' AND remote_path IS NOT NULL
	`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	failed := make(map[string]bool)
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, err
		}
		failed[path] = true
	}
	return failed, rows.Err()
}

func keysOf(set map[string]bool) []string {
	result := make([]string, 0, len(set))
	for key := range set {
		result = append(result, key)
	}
	return result
}

// TransferProgress is one provider transfer, as reported by the provider.
type TransferProgress struct {
	ProviderID       string
	Path             string
	State            string
	Detail           string
	SizeBytes        int64
	TransferredBytes int64
}

// RecordTransferProgress stores what the provider reports and settles the
// request when nothing is moving any more. A request whose transfers all
// completed is completed; one where nothing is left to wait for and something
// failed is failed. Anything else stays started, because it is still going.
func (q *Queries) RecordTransferProgress(ctx context.Context, requestID uuid.UUID, progress []TransferProgress) (DownloadRequestRow, error) {
	for _, transfer := range progress {
		// The attempt is appended in the same statement that settles the
		// transfer, so an outcome cannot be recorded for a state the row never
		// reached. The poller reports the same settled state on every pass until
		// the provider forgets the transfer, which is what the conflict clause
		// is for: the first report of an outcome is the one that is kept.
		if _, err := q.db.Exec(ctx, `
			WITH settled AS (
				UPDATE downloads
				SET status = $3,
				    provider_transfer_id = nullif($4, ''),
				    provider_state = nullif($5, ''),
				    transferred_bytes = $6,
				    size_bytes = CASE WHEN $7 > 0 THEN $7 ELSE size_bytes END,
				    error_message = CASE WHEN $3 = 'failed' THEN nullif($5, '') ELSE NULL END,
				    started_at = CASE
				        WHEN started_at IS NOT NULL THEN started_at
				        WHEN $3 IN ('downloading', 'completed') THEN now()
				        ELSE NULL
				    END,
				    completed_at = CASE
				        WHEN $3 IN ('completed', 'failed', 'cancelled') THEN now()
				        ELSE NULL
				    END,
				    updated_at = now()
				WHERE download_request_id = $1 AND remote_path = $2
				RETURNING id, attempt, status, transferred_bytes
			)
			INSERT INTO download_transfer_attempts (
				download_id, attempt, outcome, detail, transferred_bytes
			)
			SELECT id, attempt, status, nullif($5, ''), transferred_bytes
			FROM settled
			WHERE status IN ('completed', 'failed', 'cancelled')
			ON CONFLICT (download_id, attempt) DO NOTHING
		`, requestID, transfer.Path, transfer.State, transfer.ProviderID,
			transfer.Detail, transfer.TransferredBytes, transfer.SizeBytes); err != nil {
			return DownloadRequestRow{}, fmt.Errorf("record transfer progress: %w", err)
		}
	}

	if _, err := q.db.Exec(ctx, `
		WITH progress AS (
			SELECT count(*) AS total,
			       count(*) FILTER (WHERE status = 'completed') AS completed,
			       count(*) FILTER (WHERE status IN ('completed', 'failed', 'cancelled')) AS settled
			FROM downloads
			WHERE download_request_id = $1
		)
		UPDATE download_requests
		SET status = CASE
		        WHEN (SELECT total FROM progress) = 0 THEN 'started'
		        WHEN (SELECT completed FROM progress) = (SELECT total FROM progress) THEN 'completed'
		        WHEN (SELECT settled FROM progress) = (SELECT total FROM progress) THEN 'failed'
		        ELSE 'started'
		    END,
		    error_message = CASE
		        WHEN (SELECT settled FROM progress) = (SELECT total FROM progress)
		         AND (SELECT completed FROM progress) < (SELECT total FROM progress)
		        THEN (
		            SELECT string_agg(DISTINCT coalesce(provider_state, 'transfer failed'), '; ')
		            FROM downloads
		            WHERE download_request_id = $1 AND status <> 'completed'
		        )
		        ELSE NULL
		    END,
		    updated_at = now()
		WHERE id = $1 AND status = 'started'
	`, requestID); err != nil {
		return DownloadRequestRow{}, fmt.Errorf("settle download request: %w", err)
	}
	return q.DownloadRequest(ctx, requestID)
}

// OpenFilesByPeer counts, for each peer, how many files Schall has asked that
// peer for and not yet heard the end of.
//
// A Soulseek client caps how much one stranger may have queued with it at once
// and refuses everything past that, so what has to be bounded is not how many
// wants are being pursued but how many files any one person is being asked for.
// Nothing else knows that number: a want knows only itself, and the sweep's
// workers know only how many of them there are.
//
// It counts what was asked for rather than what is still moving, which
// over-counts a request most of whose files have already arrived. That is the
// safe direction — the cost of over-counting is asking a peer for the rest of a
// folder a little later, and the cost of under-counting is the refusal this
// exists to avoid.
func (q *Queries) OpenFilesByPeer(ctx context.Context) (map[Peer]int, error) {
	rows, err := q.db.Query(ctx, `
		SELECT provider, source_username, sum(file_count)::bigint
		FROM download_requests
		WHERE status IN ('requested', 'started')
		GROUP BY provider, source_username
	`)
	if err != nil {
		return nil, fmt.Errorf("count the files open at each peer: %w", err)
	}
	defer rows.Close()

	open := make(map[Peer]int)
	for rows.Next() {
		var peer Peer
		var count int64
		if err := rows.Scan(&peer.Provider, &peer.Username, &count); err != nil {
			return nil, fmt.Errorf("count the files open at each peer: %w", err)
		}
		open[peer] = int(count)
	}
	return open, rows.Err()
}

// Peer is one person at one provider. Two providers may well have a user of the
// same name, and they are not the same person to ask.
type Peer struct {
	Provider string
	Username string
}

// StartedDownloadRequests lists the requests whose transfers are still being
// followed, so the poller knows whether there is anything left to do.
func (q *Queries) StartedDownloadRequests(ctx context.Context) ([]DownloadRequestRow, error) {
	page, err := q.ListDownloadRequests(ctx, ListDownloadRequestsParams{Status: "started", Limit: 200})
	return page.Items, err
}

// RequestedTransferIDs returns the provider transfer identifiers of everything
// still moving for a request, so they can be stopped at the provider.
func (q *Queries) RequestedTransferIDs(ctx context.Context, requestID uuid.UUID) ([]string, error) {
	rows, err := q.db.Query(ctx, `
		SELECT provider_transfer_id
		FROM downloads
		WHERE download_request_id = $1
		  AND provider_transfer_id IS NOT NULL
		  AND status NOT IN ('completed', 'failed', 'cancelled')
	`, requestID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		result = append(result, id)
	}
	return result, rows.Err()
}

// QueueDownloadPoll queues the transfer poller unless it is already queued or
// running, mirroring how a library scan is queued.
func (q *Queries) QueueDownloadPoll(ctx context.Context, runAfter time.Time) error {
	_, err := q.db.Exec(ctx, `
		INSERT INTO jobs (kind, payload, max_attempts, run_after)
		VALUES ('poll_downloads', '{}'::jsonb, 1, $1)
		ON CONFLICT (kind)
		    WHERE kind = 'poll_downloads' AND status IN ('queued', 'running')
		DO NOTHING
	`, runAfter)
	return err
}

func (q *Queries) ListDownloadRequests(ctx context.Context, params ListDownloadRequestsParams) (DownloadRequestPage, error) {
	var result DownloadRequestPage
	view, known := DownloadRequestView(params.View)
	if !known {
		return result, fmt.Errorf("list download requests %q: %w", params.View, ErrUnknownDownloadView)
	}
	// The scope is what the caller asked of the whole list: one release, one
	// status, or neither. Every count below is taken inside it, and so is the
	// page, which is how a view's figure and the rows behind it are the same
	// question. The conditions are constants from downloadRequestViews and
	// never carry a value from the request, so they are safe to write into the
	// statement; the scope's own values stay parameters.
	scope := `
		WHERE (NOT $1::boolean OR download_requests.album_id = $2)
		  AND ($3 = '' OR download_requests.status = $3)
	`
	if err := q.db.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE `+downloadViewOpen+`)::bigint,
		       count(*) FILTER (WHERE `+downloadViewReview+`)::bigint,
		       count(*) FILTER (WHERE `+downloadViewImported+`)::bigint,
		       count(*) FILTER (WHERE `+downloadViewDiscarded+`)::bigint,
		       count(*) FILTER (WHERE `+downloadViewFailed+`)::bigint,
		       count(*)::bigint
		FROM download_requests`+scope,
		params.AlbumID.Valid, params.AlbumID.UUID, params.Status,
	).Scan(
		&result.Counts.Open, &result.Counts.Review, &result.Counts.Imported,
		&result.Counts.Discarded, &result.Counts.Failed, &result.Counts.All,
	); err != nil {
		return result, err
	}
	// The pager measures the view it is under, and it reads that figure from
	// the counts rather than from a count of its own.
	result.Total = result.Counts.forView(params.View)

	filter := scope
	if view != "" {
		filter += "\n\t\t  AND (" + view + ")"
	}

	rows, err := q.db.Query(ctx, `
		SELECT`+downloadRequestColumns+downloadRequestJoins+filter+`
		ORDER BY download_requests.requested_at DESC, download_requests.id
		LIMIT NULLIF($4::int, 0) OFFSET $5
	`, params.AlbumID.Valid, params.AlbumID.UUID, params.Status, params.Limit, params.Offset)
	if err != nil {
		return result, err
	}
	defer rows.Close()

	result.Items = make([]DownloadRequestRow, 0)
	for rows.Next() {
		row, err := scanDownloadRequest(rows)
		if err != nil {
			return result, err
		}
		result.Items = append(result.Items, row)
	}
	return result, rows.Err()
}

func scanDownloadRequest(row pgx.Row) (DownloadRequestRow, error) {
	var result DownloadRequestRow
	var files, reviews, decisions, duplicates, transfers []byte
	if err := row.Scan(
		&result.ID, &result.AlbumID, &result.AlbumTitle, &result.ArtistID, &result.ArtistName,
		&result.AcquisitionTargetID, &result.EntryArtist, &result.EntryTitle,
		&result.Provider, &result.SourceUsername, &result.SourceDirectory, &result.Status,
		&result.FileCount, &result.ExpectedTrackCount, &result.TotalSizeBytes, &result.Format,
		&result.AverageBitRate, &result.Score, &result.Reasons, &files,
		&result.RequestedAt, &result.StartedAt, &result.CancelledAt, &result.ErrorMessage,
		&result.ImportStatus, &result.ImportError, &result.ImportPath, &result.ImportedAt,
		&result.TransferCount, &result.CompletedCount, &result.FailedCount,
		&result.QueuedCount, &result.TransferredBytes, &reviews, &decisions,
		&result.DuplicateAcknowledgedAt, &duplicates, &transfers,
	); err != nil {
		return result, err
	}
	if err := json.Unmarshal(files, &result.Files); err != nil {
		return result, fmt.Errorf("decode requested files: %w", err)
	}
	if len(duplicates) > 0 {
		var evidence DuplicateEvidence
		if err := json.Unmarshal(duplicates, &evidence); err != nil {
			return result, fmt.Errorf("decode acknowledged duplicate evidence: %w", err)
		}
		result.DuplicateEvidence = &evidence
	}
	if err := json.Unmarshal(reviews, &result.ImportReviews); err != nil {
		return result, fmt.Errorf("decode import review history: %w", err)
	}
	if err := json.Unmarshal(decisions, &result.ImportDecisions); err != nil {
		return result, fmt.Errorf("decode import decisions: %w", err)
	}
	if err := json.Unmarshal(transfers, &result.Transfers); err != nil {
		return result, fmt.Errorf("decode transfer states: %w", err)
	}
	for index := range result.Transfers {
		if result.Transfers[index].Attempts == nil {
			result.Transfers[index].Attempts = []TransferAttempt{}
		}
	}
	return result, nil
}
