package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// UploadImportKind is the job that validates one staged upload and takes it
// into the library. It is the only place an upload is recorded: the folder on
// disc holds the bytes and this job holds what became of them, so there is no
// third thing that can disagree with either.
const UploadImportKind = "import_upload"

// UploadImportRow is one upload as the job queue remembers it. The payload
// carries what the browser sent — enough to name the upload in a list after its
// staging folder has been cleared away — and, once the import commits, where it
// landed.
type UploadImportRow struct {
	UploadID    uuid.UUID
	JobID       uuid.UUID
	Status      string
	Files       []string
	SizeBytes   int64
	ImportPath  string
	Error       pgtype.Text
	CreatedAt   time.Time
	CompletedAt pgtype.Timestamptz
}

// uploadPayload is what an upload's job carries. It is built in SQL at queue
// time and gains ImportPath when the import commits, and this is how it is read
// back.
type uploadPayload struct {
	UploadID   uuid.UUID `json:"uploadId"`
	Files      []string  `json:"files"`
	SizeBytes  int64     `json:"sizeBytes"`
	ImportPath string    `json:"importPath,omitempty"`
}

// UploadNaming is the SoundCloud address and the naming a person confirmed for
// it, carried in an upload's job payload under the "naming" key. It is present
// only when the browser sent an address and exactly one file was staged for
// it — one address names one track, so a payload naming a batch would be a
// claim about files nobody looked at.
type UploadNaming struct {
	URL     string `json:"sourceUrl"`
	Artist  string `json:"artist"`
	Title   string `json:"title"`
	Remixer string `json:"remixer,omitempty"`
}

// QueueUploadImport asks for a staged upload to be validated and imported.
// naming is nil for the ordinary upload, which carries no address; where it is
// not nil, the payload gains a "naming" object the worker reads once the file
// it names has a row to write against.
//
// It refuses to queue a second job for an upload that already has one, so a
// restart may ask about every folder in staging without re-importing the ones
// already accounted for. There is no unique index behind this — a job kind is
// plain text and an upload needs no schema of its own — so the guard is the
// query's own, which is enough for work only ever queued by one request or one
// startup.
func (q *Queries) QueueUploadImport(
	ctx context.Context, uploadID uuid.UUID, files []string, sizeBytes int64, naming *UploadNaming,
) (LibraryScanJobRow, error) {
	var encodedNaming []byte
	if naming != nil {
		encoded, err := json.Marshal(naming)
		if err != nil {
			return LibraryScanJobRow{}, fmt.Errorf(
				"encode the SoundCloud naming for upload %s: %w", uploadID, err)
		}
		encodedNaming = encoded
	}
	var row LibraryScanJobRow
	err := q.db.QueryRow(ctx, `
		WITH inserted AS (
			INSERT INTO jobs (kind, payload, max_attempts)
			SELECT 'import_upload',
			       jsonb_build_object(
			           'uploadId', $1::text,
			           'files', to_jsonb(coalesce($2::text[], ARRAY[]::text[])),
			           'sizeBytes', $3::bigint
			       ) || CASE WHEN $4::jsonb IS NULL THEN '{}'::jsonb
			                 ELSE jsonb_build_object('naming', $4::jsonb) END,
			       3
			WHERE NOT EXISTS (
			    SELECT 1 FROM jobs
			    WHERE kind = 'import_upload' AND payload->>'uploadId' = $1
			)
			RETURNING id, status, created_at
		)
		SELECT id, status, created_at FROM inserted
		UNION ALL
		SELECT id, status, created_at
		FROM jobs
		WHERE kind = 'import_upload' AND payload->>'uploadId' = $1
		ORDER BY created_at
		LIMIT 1
	`, uploadID.String(), files, sizeBytes, encodedNaming).Scan(&row.ID, &row.Status, &row.CreatedAt)
	return row, err
}

// UploadImports is what every upload came to, newest first. It is read from the
// job rows rather than from a table of uploads, because the job is already the
// record of one attempt to import one folder, and a second record of the same
// thing would only be a second thing to keep in step.
//
// The window is a page of history, plus every upload still holding bytes
// whatever its age. Without that second half a refused upload — which keeps its
// files on purpose, so the reason and the files it names are both still there —
// would fall out of the page after a hundred later uploads and come back as a
// folder nothing accounts for, losing the reason it was refused.
func (q *Queries) UploadImports(
	ctx context.Context, limit int32, staged []string,
) ([]UploadImportRow, error) {
	rows, err := q.db.Query(ctx, `
		SELECT id, status, payload, error_message, created_at, completed_at
		FROM jobs
		WHERE kind = 'import_upload'
		  AND (
		      id IN (
		          SELECT id FROM jobs
		          WHERE kind = 'import_upload'
		          ORDER BY created_at DESC
		          LIMIT $1
		      )
		      OR payload->>'uploadId' = ANY($2::text[])
		  )
		ORDER BY created_at DESC
	`, limit, staged)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]UploadImportRow, 0)
	for rows.Next() {
		var row UploadImportRow
		var raw []byte
		if err := rows.Scan(
			&row.JobID, &row.Status, &raw, &row.Error, &row.CreatedAt, &row.CompletedAt,
		); err != nil {
			return nil, err
		}
		var payload uploadPayload
		if err := json.Unmarshal(raw, &payload); err != nil {
			return nil, fmt.Errorf("decode upload payload of job %s: %w", row.JobID, err)
		}
		row.UploadID = payload.UploadID
		row.Files = payload.Files
		row.SizeBytes = payload.SizeBytes
		row.ImportPath = payload.ImportPath
		result = append(result, row)
	}
	return result, rows.Err()
}

// UploadImportStatus is what one upload's job is doing, or pgx.ErrNoRows when
// nothing has been queued for it. It exists so that discarding a staged folder
// can refuse while a worker is reading it.
func (q *Queries) UploadImportStatus(ctx context.Context, uploadID uuid.UUID) (string, error) {
	var status string
	err := q.db.QueryRow(ctx, `
		SELECT status
		FROM jobs
		WHERE kind = 'import_upload' AND payload->>'uploadId' = $1
		ORDER BY created_at DESC
		LIMIT 1
	`, uploadID.String()).Scan(&status)
	return status, err
}

// CompleteUploadImport commits everything an imported upload changes, in one
// transaction: where it landed, that the managed library is a music folder, and
// that a scan of it is owed.
//
// The files are already on disc when this runs, so repeating it is safe. It is
// the same pair of statements a completed download import commits, for the same
// reason: a file the library does not index is not a file the library has, and
// the scan is what turns copied bytes into a row the resolver can be asked
// about.
// It is one statement rather than a transaction of three, so that the three
// cannot come apart: a data-modifying CTE always runs to completion, and a
// statement either happens or does not.
func (q *Queries) CompleteUploadImport(
	ctx context.Context, uploadID uuid.UUID, rootPath, importPath string,
) error {
	var recorded int64
	err := q.db.QueryRow(ctx, `
		WITH recorded AS (
			UPDATE jobs
			SET payload = payload || jsonb_build_object('importPath', $3::text),
			    updated_at = now()
			WHERE kind = 'import_upload' AND payload->>'uploadId' = $1
			RETURNING id
		), root AS (
			INSERT INTO library_roots (path) VALUES ($2)
			ON CONFLICT (path) DO UPDATE SET enabled = true, updated_at = now()
			RETURNING id
		), scan AS (
			INSERT INTO jobs (kind, payload, max_attempts)
			SELECT 'scan_library', '{}'::jsonb, 1
			WHERE EXISTS (SELECT 1 FROM root)
			ON CONFLICT (kind)
			    WHERE kind = 'scan_library' AND status IN ('queued', 'running')
			DO NOTHING
			RETURNING id
		)
		SELECT count(*) FROM recorded
	`, uploadID.String(), rootPath, importPath).Scan(&recorded)
	if err != nil {
		return fmt.Errorf("record the import of upload %s: %w", uploadID, err)
	}
	if recorded == 0 {
		return errors.New("no import job was found for this upload")
	}
	return nil
}

// MarkUploadedFiles records which upload put the scanned paths into the
// library. The identity resolver clears the marker after its one automatic
// replacement check.
func (q *Queries) MarkUploadedFiles(
	ctx context.Context, uploadID uuid.UUID, paths []string,
) error {
	_, err := q.db.Exec(ctx, `
		UPDATE library_files
		SET arrived_by_upload_id = $1, updated_at = now()
		WHERE path = ANY($2::text[]) AND missing_at IS NULL
	`, uploadID, paths)
	if err != nil {
		return fmt.Errorf("mark the files from upload %s: %w", uploadID, err)
	}
	return nil
}
