package db

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pxldi/schall/internal/dbtest"
)

// The sweep that measures the library's audio reads and writes these two
// queries and nothing else. A file is offered once, what was found is written
// down, and a file that was looked at is never offered again.
func TestFilesToMeasureAndRecordFileSpectrum(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	store := New(pool)

	measured, unread, missing := uuid.New(), uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_files (
		    id, path, size_bytes, modified_at, sample_rate_hz, bit_rate_kbps,
		    measured_duration_ms, properties_read_at, scanned_at, missing_at
		)
		VALUES
		    -- Ready to be measured: its headers were read, its audio was not.
		    ($1, '/music/a.flac', 1, now(), 44100, 900, 240000, now(), now(), NULL),
		    -- Nothing could read this one's headers, so nothing decodes it: a
		    -- decode with no stated sample rate would have to resample, and a
		    -- resampler removes the top of the range itself.
		    ($2, '/music/b.m4a', 1, now(), NULL, NULL, NULL, now(), now(), NULL),
		    -- Gone from disc. There is nothing to decode.
		    ($3, '/music/c.flac', 1, now(), 44100, 900, 240000, now(), now(), now())
	`, measured, unread, missing); err != nil {
		t.Fatal(err)
	}

	due, err := store.FilesToMeasure(ctx, 10)
	if err != nil {
		t.Fatalf("list the files to measure: %v", err)
	}
	if len(due) != 1 || due[0].ID != measured {
		t.Fatalf("offered %+v, want only the file whose headers were read", due)
	}
	if due[0].SampleRateHz != 44100 || due[0].DurationMS != 240000 || due[0].BitRateKbps != 900 {
		t.Fatalf("offered %+v, want the numbers the decode needs", due[0])
	}

	if err := store.RecordFileSpectrum(ctx, FileSpectrumParams{
		FileID: measured, Judged: true, CutoffHz: 15750,
		Encoder: "LAME3.100", TranscodeSuspected: true,
	}); err != nil {
		t.Fatalf("record what the audio is: %v", err)
	}

	var cutoff pgtype.Int4
	var encoder pgtype.Text
	var suspected pgtype.Bool
	var readAt pgtype.Timestamptz
	if err := pool.QueryRow(ctx, `
		SELECT spectral_cutoff_hz, audio_encoder, transcode_suspected, spectrum_read_at
		FROM library_files WHERE id = $1
	`, measured).Scan(&cutoff, &encoder, &suspected, &readAt); err != nil {
		t.Fatal(err)
	}
	if cutoff.Int32 != 15750 || encoder.String != "LAME3.100" ||
		!suspected.Bool || !readAt.Valid {
		t.Fatalf("stored cutoff %v encoder %v suspected %v read %v, want the measurement",
			cutoff, encoder, suspected, readAt)
	}

	after, err := store.FilesToMeasure(ctx, 10)
	if err != nil {
		t.Fatalf("list the files to measure again: %v", err)
	}
	if len(after) != 0 {
		t.Fatalf("a file that was measured was offered again: %+v", after)
	}
}

// A look that concluded nothing leaves the three facts null and stamps the row
// as looked at. Null is not agreement that a file is fine, and a file that says
// nothing is asked once rather than on every pass forever.
func TestRecordFileSpectrumStoresNothingItDidNotFind(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	store := New(pool)

	fileID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_files (id, path, size_bytes, modified_at, sample_rate_hz, scanned_at)
		VALUES ($1, '/music/silent.flac', 1, now(), 44100, now())
	`, fileID); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordFileSpectrum(ctx, FileSpectrumParams{FileID: fileID}); err != nil {
		t.Fatalf("record a look that concluded nothing: %v", err)
	}

	var cutoff pgtype.Int4
	var encoder pgtype.Text
	var suspected pgtype.Bool
	var readAt pgtype.Timestamptz
	if err := pool.QueryRow(ctx, `
		SELECT spectral_cutoff_hz, audio_encoder, transcode_suspected, spectrum_read_at
		FROM library_files WHERE id = $1
	`, fileID).Scan(&cutoff, &encoder, &suspected, &readAt); err != nil {
		t.Fatal(err)
	}
	if cutoff.Valid || encoder.Valid || suspected.Valid {
		t.Fatalf("stored cutoff %v encoder %v suspected %v, want nothing to be claimed",
			cutoff, encoder, suspected)
	}
	if !readAt.Valid {
		t.Fatal("a file that was looked at was not stamped as looked at")
	}
}

// One sweep at a time, enforced by the table. Two passes decoding the same
// library at once is the most expensive way to learn nothing new.
func TestQueueSpectrumSweepKeepsOnePassOnTheBooks(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	store := New(pool)

	later := time.Now().Add(time.Hour)
	if err := store.EnsureSpectrumSweepQueued(ctx, later); err != nil {
		t.Fatalf("queue the first pass: %v", err)
	}
	if err := store.EnsureSpectrumSweepQueued(ctx, time.Now()); err != nil {
		t.Fatalf("ask for a pass that is already on the books: %v", err)
	}

	var queued int
	var runAfter time.Time
	if err := pool.QueryRow(ctx, `
		SELECT count(*)::int, min(run_after)
		FROM jobs
		WHERE kind = 'sweep_audio_spectrum' AND status IN ('queued', 'running')
	`).Scan(&queued, &runAfter); err != nil {
		t.Fatal(err)
	}
	if queued != 1 {
		t.Fatalf("%d passes are on the books, want one", queued)
	}
	if runAfter.Sub(later).Abs() > time.Second {
		t.Fatalf("the pass on the books moved to %s, want it to keep %s", runAfter, later)
	}

	// A pass that is asked for sooner is brought forward rather than doubled.
	sooner := time.Now().Add(time.Minute)
	if err := store.QueueSpectrumSweep(ctx, sooner); err != nil {
		t.Fatalf("bring the pass forward: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(*)::int, min(run_after)
		FROM jobs
		WHERE kind = 'sweep_audio_spectrum' AND status IN ('queued', 'running')
	`).Scan(&queued, &runAfter); err != nil {
		t.Fatal(err)
	}
	if queued != 1 || runAfter.Sub(sooner).Abs() > time.Second {
		t.Fatalf("%d passes at %s, want one at %s", queued, runAfter, sooner)
	}
}
