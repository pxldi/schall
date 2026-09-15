package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pxldi/schall/internal/dbtest"
)

func TestQueueUploadImportRecordsWhatTheBrowserSent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	queries := New(dbtest.Setup(t))
	uploadID := uuid.New()

	job, err := queries.QueueUploadImport(
		ctx, uploadID, []string{"01 Mysterons.flac", "02 Sour Times.flac"}, 512, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != "queued" {
		t.Fatalf("status = %q, want queued", job.Status)
	}

	rows, err := queries.UploadImports(ctx, 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %#v", rows)
	}
	if rows[0].UploadID != uploadID || rows[0].SizeBytes != 512 || len(rows[0].Files) != 2 {
		t.Fatalf("row = %#v", rows[0])
	}
}

// A restart may ask about every folder in staging, so asking twice must not
// import an upload twice.
func TestQueueUploadImportAsksOnceForOneUpload(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	queries := New(dbtest.Setup(t))
	uploadID := uuid.New()

	first, err := queries.QueueUploadImport(ctx, uploadID, []string{"01.flac"}, 8, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := queries.QueueUploadImport(ctx, uploadID, []string{"01.flac"}, 8, nil)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID {
		t.Fatalf("second job = %s, want the one already queued (%s)", second.ID, first.ID)
	}
}

func TestUploadImportStatusReportsWhatTheJobIsDoing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	queries := New(dbtest.Setup(t))
	uploadID := uuid.New()
	if _, err := queries.QueueUploadImport(ctx, uploadID, []string{"01.flac"}, 8, nil); err != nil {
		t.Fatal(err)
	}

	status, err := queries.UploadImportStatus(ctx, uploadID)
	if err != nil {
		t.Fatal(err)
	}
	if status != "queued" {
		t.Fatalf("status = %q, want queued", status)
	}
}

func TestUploadImportStatusReportsAnUploadNothingWasQueuedFor(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	queries := New(dbtest.Setup(t))

	if _, err := queries.UploadImportStatus(ctx, uuid.New()); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("err = %v, want pgx.ErrNoRows", err)
	}
}

// A file the library does not index is not a file the library has, so the
// managed folder becoming a music folder and a scan being owed are part of the
// same commit as where the upload landed.
func TestCompleteUploadImportMakesTheLibraryLearnAboutTheFiles(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)
	uploadID := uuid.New()
	if _, err := queries.QueueUploadImport(ctx, uploadID, []string{"01.flac"}, 8, nil); err != nil {
		t.Fatal(err)
	}

	importPath := "/music/Portishead/Dummy [abcdef12]"
	if err := queries.CompleteUploadImport(ctx, uploadID, "/music", importPath); err != nil {
		t.Fatal(err)
	}

	rows, err := queries.UploadImports(ctx, 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ImportPath != importPath {
		t.Fatalf("rows = %#v", rows)
	}
	// The payload the worker started from is still intact beside it.
	if rows[0].UploadID != uploadID || len(rows[0].Files) != 1 {
		t.Fatalf("row = %#v", rows[0])
	}

	var enabled bool
	if err := pool.QueryRow(ctx,
		"SELECT enabled FROM library_roots WHERE path = '/music'").Scan(&enabled); err != nil {
		t.Fatal(err)
	}
	if !enabled {
		t.Fatal("the managed library is not an enabled music folder")
	}
	var scans int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM jobs WHERE kind = 'scan_library' AND status = 'queued'
	`).Scan(&scans); err != nil {
		t.Fatal(err)
	}
	if scans != 1 {
		t.Fatalf("queued scans = %d, want one", scans)
	}
}

func TestCompleteUploadImportRefusesAnUploadNothingWasQueuedFor(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	queries := New(dbtest.Setup(t))

	err := queries.CompleteUploadImport(ctx, uuid.New(), "/music", "/music/Somewhere")
	if err == nil {
		t.Fatal("an upload with no import job was recorded as imported")
	}
}

// An upload still holding bytes is asked about by name, so a refusal that keeps
// its files does not fall out of the page of history and come back as a folder
// nothing accounts for — losing the reason it was refused.
func TestUploadImportsReachesPastTheWindowForAnUploadStillStaged(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	queries := New(dbtest.Setup(t))
	refused := uuid.New()
	if _, err := queries.QueueUploadImport(ctx, refused, []string{"broken.flac"}, 8, nil); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := queries.QueueUploadImport(ctx, uuid.New(), []string{"01.flac"}, 8, nil); err != nil {
			t.Fatal(err)
		}
	}

	// A window of two would not reach the oldest job on its own.
	rows, err := queries.UploadImports(ctx, 2, []string{refused.String()})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range rows {
		if row.UploadID == refused {
			found = true
		}
	}
	if !found {
		t.Fatalf("rows = %#v, want the staged upload among them", rows)
	}
}

// A payload this build cannot read is a job nothing can be said about, and
// reporting it as an upload with no name would be worse than saying so.
func TestUploadImportsRefusesAPayloadItCannotRead(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	if _, err := pool.Exec(ctx, `
		INSERT INTO jobs (kind, payload) VALUES ('import_upload', '{"uploadId": 7}'::jsonb)
	`); err != nil {
		t.Fatal(err)
	}

	if _, err := New(pool).UploadImports(ctx, 10, nil); err == nil {
		t.Fatal("a payload nothing could read was listed as an upload")
	}
}

func TestUploadImportsReportsAConnectionItCannotUse(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	transaction, err := dbtest.Setup(t).Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := transaction.Rollback(ctx); err != nil {
		t.Fatal(err)
	}

	if _, err := New(transaction).UploadImports(ctx, 10, nil); err == nil {
		t.Fatal("a closed transaction answered for the uploads")
	}
}

func TestCompleteUploadImportReportsAConnectionItCannotUse(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	transaction, err := dbtest.Setup(t).Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := transaction.Rollback(ctx); err != nil {
		t.Fatal(err)
	}

	err = New(transaction).CompleteUploadImport(ctx, uuid.New(), "/music", "/music/Somewhere")
	if err == nil {
		t.Fatal("a closed transaction recorded an import")
	}
}

// The files are already on disc when this runs, so a lost response must not
// leave an import that cannot be finished.
func TestCompleteUploadImportCanBeRepeated(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	queries := New(dbtest.Setup(t))
	uploadID := uuid.New()
	if _, err := queries.QueueUploadImport(ctx, uploadID, []string{"01.flac"}, 8, nil); err != nil {
		t.Fatal(err)
	}
	importPath := "/music/Portishead/Dummy [abcdef12]"
	if err := queries.CompleteUploadImport(ctx, uploadID, "/music", importPath); err != nil {
		t.Fatal(err)
	}

	if err := queries.CompleteUploadImport(ctx, uploadID, "/music", importPath); err != nil {
		t.Fatal(err)
	}
}
