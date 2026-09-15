package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// The classes one pass over the download inbox sorts a file into. The four that
// delete come first, in the order a real run works through them, and the five
// that keep follow. docs/decisions/0035 is the rule; the CHECK constraint in
// migration 00100 is the same list.
const (
	InboxImported        = "imported"
	InboxRefused         = "refused"
	InboxSettled         = "settled"
	InboxUnknown         = "unknown"
	InboxKeptQuestion    = "kept_question"
	InboxKeptOpen        = "kept_open"
	InboxKeptRecent      = "kept_recent"
	InboxKeptUnconfirmed = "kept_unconfirmed"
	InboxKeptUndecided   = "kept_undecided"
	InboxKeptUnsafePath  = "kept_unsafe_path"
)

// InboxDeleteClasses are the classes a real run unlinks, in the order it works
// through them.
var InboxDeleteClasses = []string{InboxImported, InboxRefused, InboxSettled, InboxUnknown}

// InboxKeepClasses are the classes a run leaves alone.
var InboxKeepClasses = []string{
	InboxKeptQuestion, InboxKeptOpen, InboxKeptRecent,
	InboxKeptUnconfirmed, InboxKeptUndecided, InboxKeptUnsafePath,
}

// ErrInboxCleanupRunning reports a pass over the inbox that is already going.
// Two passes would walk the same folders and race each other's unlinks.
var ErrInboxCleanupRunning = errors.New("a pass over the download inbox is already running")

// InboxCleanupCount is what one class came to.
//
// Failed is the files of a delete class the unlink refused, counted apart from
// Files because they are still there. A pass that could not remove something has
// not done what it says it did, and the row's error names how many.
type InboxCleanupCount struct {
	Files  int   `json:"files"`
	Bytes  int64 `json:"bytes"`
	Failed int   `json:"failed,omitempty"`
}

// InboxCleanupRow is one pass over the download inbox.
type InboxCleanupRow struct {
	ID          uuid.UUID
	RequestedBy string
	RequestedAt time.Time
	DryRun      bool
	StartedAt   *time.Time
	FinishedAt  *time.Time
	Counts      map[string]InboxCleanupCount
	Error       string
}

// InboxCleanupFile is one file the pass looked at.
type InboxCleanupFile struct {
	Path      string
	Class     string
	SizeBytes int64
}

// InboxCopyRow is one copy a peer sent for one want: where its bytes landed,
// what was decided about it, what became of the want, and the managed copy an
// accepted one turned into.
//
// SourceDirectory is read from the request the copy arrived through, because
// that is where validation has always read it. RemotePath is the fallback for a
// copy whose request row has since gone: it is the same folder in the peer's own
// separators, so it is resolved by the same derivation and not a second one.
type InboxCopyRow struct {
	SourceDirectory string
	RemotePath      string
	FileName        string
	Verdict         string
	WantStatus      string
	LibraryPath     string
}

// InboxRequestRow is one download request: the folder it was fetched into, what
// state it is in, the files it asked for, and where each one ended up in the
// library once it was imported.
type InboxRequestRow struct {
	SourceDirectory string
	Status          string
	ImportStatus    string
	Files           []DownloadRequestFile
	// Imported maps a remote path to the managed copy it became. A file that
	// was never imported is absent.
	Imported map[string]string
}

// QueueInboxCleanup asks for one pass over the download inbox and returns the
// row it will be recorded against.
//
// Only one pass runs at a time, and "running" means the job: a row left open by
// a worker that died with nothing queued behind it is closed here as interrupted
// rather than blocking every later pass forever.
func (q *Queries) QueueInboxCleanup(
	ctx context.Context, requestedBy string, dryRun bool, runAfter time.Time,
) (InboxCleanupRow, error) {
	var row InboxCleanupRow
	err := q.inTransaction(ctx, "asking for a pass over the download inbox", func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			UPDATE inbox_cleanups
			SET finished_at = now(), error = 'the run was interrupted'
			WHERE finished_at IS NULL
			  AND NOT EXISTS (
				SELECT 1 FROM jobs
				WHERE kind = 'clean_inbox'
				  AND status IN ('queued', 'running')
				  AND payload->>'cleanup_id' = inbox_cleanups.id::text
			)
		`); err != nil {
			return fmt.Errorf("close the passes nothing is working on: %w", err)
		}

		// Asked first so that the ordinary refusal reads as a refusal rather
		// than as a constraint. inbox_cleanups_live_idx is what actually holds
		// the rule: this is a read followed by a write, and two requests a
		// millisecond apart both pass it.
		var open bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM inbox_cleanups WHERE finished_at IS NULL)
		`).Scan(&open); err != nil {
			return fmt.Errorf("look for a pass already running: %w", err)
		}
		if open {
			return ErrInboxCleanupRunning
		}

		var counts []byte
		if err := tx.QueryRow(ctx, `
			INSERT INTO inbox_cleanups (requested_by, dry_run)
			VALUES ($1, $2)
			RETURNING id, requested_by, requested_at, dry_run,
			          started_at, finished_at, counts, error
		`, requestedBy, dryRun).Scan(
			&row.ID, &row.RequestedBy, &row.RequestedAt, &row.DryRun,
			&row.StartedAt, &row.FinishedAt, &counts, &row.Error,
		); err != nil {
			if isUniqueViolation(err) {
				return ErrInboxCleanupRunning
			}
			return fmt.Errorf("record the pass over the download inbox: %w", err)
		}
		if err := json.Unmarshal(counts, &row.Counts); err != nil {
			return fmt.Errorf("read what the pass counted: %w", err)
		}

		payload, err := json.Marshal(map[string]string{"cleanup_id": row.ID.String()})
		if err != nil {
			return fmt.Errorf("write the payload for pass %s: %w", row.ID, err)
		}
		// One attempt: the pass walks a folder of tens of thousands of files and
		// finishes its row with whatever went wrong, so a failure is a sentence
		// on screen and a press away from being asked again.
		if _, err := tx.Exec(ctx, `
			INSERT INTO jobs (kind, payload, max_attempts, run_after)
			VALUES ('clean_inbox', $1, 1, $2)
		`, payload, runAfter); err != nil {
			return fmt.Errorf("queue the pass over the download inbox: %w", err)
		}
		return nil
	})
	if err != nil {
		return InboxCleanupRow{}, err
	}
	return row, nil
}

// InboxCleanup reads one pass.
func (q *Queries) InboxCleanup(ctx context.Context, id uuid.UUID) (InboxCleanupRow, error) {
	row, err := scanInboxCleanup(q.db.QueryRow(ctx, `
		SELECT id, requested_by, requested_at, dry_run,
		       started_at, finished_at, counts, error
		FROM inbox_cleanups WHERE id = $1
	`, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return InboxCleanupRow{}, err
		}
		return InboxCleanupRow{}, fmt.Errorf("read the pass over the inbox %s: %w", id, err)
	}
	return row, nil
}

// ListInboxCleanups reads the most recent passes, newest first.
func (q *Queries) ListInboxCleanups(ctx context.Context, limit int32) ([]InboxCleanupRow, error) {
	rows, err := q.db.Query(ctx, `
		SELECT id, requested_by, requested_at, dry_run,
		       started_at, finished_at, counts, error
		FROM inbox_cleanups
		ORDER BY requested_at DESC, id
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("read the passes over the download inbox: %w", err)
	}
	defer rows.Close()

	cleanups := make([]InboxCleanupRow, 0, limit)
	for rows.Next() {
		row, err := scanInboxCleanup(rows)
		if err != nil {
			return nil, err
		}
		cleanups = append(cleanups, row)
	}
	return cleanups, rows.Err()
}

func scanInboxCleanup(row pgx.Row) (InboxCleanupRow, error) {
	var cleanup InboxCleanupRow
	var counts []byte
	if err := row.Scan(
		&cleanup.ID, &cleanup.RequestedBy, &cleanup.RequestedAt, &cleanup.DryRun,
		&cleanup.StartedAt, &cleanup.FinishedAt, &counts, &cleanup.Error,
	); err != nil {
		return InboxCleanupRow{}, err
	}
	if err := json.Unmarshal(counts, &cleanup.Counts); err != nil {
		return InboxCleanupRow{}, fmt.Errorf("read what pass %s counted: %w", cleanup.ID, err)
	}
	return cleanup, nil
}

// StartInboxCleanup stamps the moment the walk began. A pass that is run again
// after a crash keeps the first stamp.
func (q *Queries) StartInboxCleanup(ctx context.Context, id uuid.UUID) error {
	if _, err := q.db.Exec(ctx, `
		UPDATE inbox_cleanups
		SET started_at = coalesce(started_at, now())
		WHERE id = $1 AND finished_at IS NULL
	`, id); err != nil {
		return fmt.Errorf("start the pass over the inbox %s: %w", id, err)
	}
	return nil
}

// FinishInboxCleanup closes a pass with what it came to, or with what went
// wrong. A failure is finished too: the row is the only thing holding the next
// pass back.
func (q *Queries) FinishInboxCleanup(
	ctx context.Context, id uuid.UUID, counts map[string]InboxCleanupCount, failure string,
) error {
	if counts == nil {
		counts = map[string]InboxCleanupCount{}
	}
	encoded, err := json.Marshal(counts)
	if err != nil {
		return fmt.Errorf("write what pass %s counted: %w", id, err)
	}
	if _, err := q.db.Exec(ctx, `
		UPDATE inbox_cleanups
		SET started_at = coalesce(started_at, now()),
		    finished_at = now(), counts = $2, error = $3
		WHERE id = $1
	`, id, encoded, failure); err != nil {
		return fmt.Errorf("finish the pass over the inbox %s: %w", id, err)
	}
	return nil
}

// RecordInboxCleanupFiles writes down what the pass decided about a batch of
// files. Nothing is unlinked until these rows are committed.
func (q *Queries) RecordInboxCleanupFiles(
	ctx context.Context, id uuid.UUID, files []InboxCleanupFile,
) error {
	if len(files) == 0 {
		return nil
	}
	paths := make([]string, len(files))
	classes := make([]string, len(files))
	sizes := make([]int64, len(files))
	for index, file := range files {
		paths[index] = file.Path
		classes[index] = file.Class
		sizes[index] = file.SizeBytes
	}
	if _, err := q.db.Exec(ctx, `
		INSERT INTO inbox_cleanup_files (cleanup_id, path, class, size_bytes)
		SELECT $1, entry.path, entry.class, entry.size
		FROM unnest($2::text[], $3::text[], $4::bigint[])
		     AS entry(path, class, size)
		ON CONFLICT (cleanup_id, path) DO NOTHING
	`, id, paths, classes, sizes); err != nil {
		return fmt.Errorf("record %d files of pass %s: %w", len(files), id, err)
	}
	return nil
}

// ReclassifyInboxCleanupFiles moves files the pass chose for deletion into the
// class it ended up putting them in.
//
// The walk and the unlink are minutes apart on a folder of tens of thousands of
// files, and the pass looks again before it removes anything: a want pursued
// again in between, a file written to since, or a path that is no longer an
// ordinary file inside the inbox all move a row out of a delete class. A row
// that was already stamped as deleted is left alone, because that one happened.
func (q *Queries) ReclassifyInboxCleanupFiles(
	ctx context.Context, id uuid.UUID, paths []string, class string,
) error {
	if len(paths) == 0 {
		return nil
	}
	if _, err := q.db.Exec(ctx, `
		UPDATE inbox_cleanup_files
		SET class = $3
		WHERE cleanup_id = $1 AND path = ANY($2::text[]) AND deleted_at IS NULL
	`, id, paths, class); err != nil {
		return fmt.Errorf("move %d files of pass %s to %s: %w", len(paths), id, class, err)
	}
	return nil
}

// MarkInboxCleanupFilesDeleted stamps the files the pass has now unlinked.
func (q *Queries) MarkInboxCleanupFilesDeleted(
	ctx context.Context, id uuid.UUID, paths []string, at time.Time,
) error {
	if len(paths) == 0 {
		return nil
	}
	if _, err := q.db.Exec(ctx, `
		UPDATE inbox_cleanup_files
		SET deleted_at = $3
		WHERE cleanup_id = $1 AND path = ANY($2::text[])
	`, id, paths, at); err != nil {
		return fmt.Errorf("stamp %d deleted files of pass %s: %w", len(paths), id, err)
	}
	return nil
}

// InboxCleanupCopies reads every copy a peer ever sent for a want, with the
// want's state and the managed copy an accepted one became.
func (q *Queries) InboxCleanupCopies(ctx context.Context) ([]InboxCopyRow, error) {
	rows, err := q.db.Query(ctx, `
		SELECT coalesce(requests.source_directory, ''), copies.remote_path,
		       copies.file_name, copies.verdict, targets.status,
		       coalesce(files.path, '')
		FROM acquisition_target_files copies
		JOIN acquisition_targets targets ON targets.id = copies.acquisition_target_id
		LEFT JOIN download_requests requests ON requests.id = copies.download_request_id
		LEFT JOIN library_files files ON files.id = copies.library_file_id
	`)
	if err != nil {
		return nil, fmt.Errorf("read the copies the inbox may still hold: %w", err)
	}
	defer rows.Close()

	copies := make([]InboxCopyRow, 0, 256)
	for rows.Next() {
		var copied InboxCopyRow
		if err := rows.Scan(
			&copied.SourceDirectory, &copied.RemotePath, &copied.FileName,
			&copied.Verdict, &copied.WantStatus, &copied.LibraryPath,
		); err != nil {
			return nil, err
		}
		copies = append(copies, copied)
	}
	return copies, rows.Err()
}

// InboxCleanupRequests reads every download request with the files it asked for
// and where each imported one ended up.
func (q *Queries) InboxCleanupRequests(ctx context.Context) ([]InboxRequestRow, error) {
	rows, err := q.db.Query(ctx, `
		SELECT requests.source_directory, requests.status, requests.import_status,
		       requests.files,
		       coalesce(
		           jsonb_object_agg(transfers.remote_path, transfers.local_path)
		           FILTER (WHERE transfers.remote_path IS NOT NULL
		                     AND transfers.local_path IS NOT NULL),
		           '{}'::jsonb)
		FROM download_requests requests
		LEFT JOIN downloads transfers
		       ON transfers.download_request_id = requests.id
		GROUP BY requests.id
	`)
	if err != nil {
		return nil, fmt.Errorf("read the download requests the inbox may still hold: %w", err)
	}
	defer rows.Close()

	requests := make([]InboxRequestRow, 0, 256)
	for rows.Next() {
		var request InboxRequestRow
		var files, imported []byte
		if err := rows.Scan(
			&request.SourceDirectory, &request.Status, &request.ImportStatus,
			&files, &imported,
		); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(files, &request.Files); err != nil {
			return nil, fmt.Errorf("read the files of a download request: %w", err)
		}
		if err := json.Unmarshal(imported, &request.Imported); err != nil {
			return nil, fmt.Errorf("read where a download request was imported to: %w", err)
		}
		requests = append(requests, request)
	}
	return requests, rows.Err()
}
