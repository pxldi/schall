package db

import (
	"context"
	"testing"
	"time"

	"github.com/pxldi/schall/internal/dbtest"
)

// An installation that has never opened the setting re-encodes nothing, and
// says which policy it would use if it were turned on.
func TestImportSettingsDefaultToLeavingMusicAsItArrived(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	queries := New(dbtest.Setup(t))

	stored, err := queries.ImportSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stored.TranscodeEnabled {
		t.Fatal("music is re-encoded on an installation that never asked for it")
	}
	if stored.TranscodeTarget != "mp3" || stored.TranscodeBitrate != "320" ||
		stored.TranscodeWhen != "lossless" {
		t.Fatalf("stored = %#v", stored)
	}
}

func TestSaveImportSettingsStoresTheReEncodingPolicy(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	queries := New(dbtest.Setup(t))

	stored, err := queries.SaveImportSettings(ctx, SaveImportSettingsParams{
		SourceRetention:  SourceRetentionKeep,
		TranscodeEnabled: ptr(true),
		TranscodeTarget:  "mp3",
		TranscodeBitrate: "V0",
		TranscodeWhen:    "above_target",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !stored.TranscodeEnabled || stored.TranscodeBitrate != "V0" ||
		stored.TranscodeWhen != "above_target" {
		t.Fatalf("stored = %#v", stored)
	}

	read, err := queries.ImportSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if read != stored {
		t.Fatalf("read back %#v, stored %#v", read, stored)
	}
}

// The table is the authority on which policies exist, so a value the interface
// would refuse is refused again here.
func TestSaveImportSettingsRefusesABitrateThatIsNotOnTheList(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	queries := New(dbtest.Setup(t))

	if _, err := queries.SaveImportSettings(ctx, SaveImportSettingsParams{
		SourceRetention:  SourceRetentionKeep,
		TranscodeEnabled: ptr(true),
		TranscodeTarget:  "mp3",
		TranscodeBitrate: "999",
		TranscodeWhen:    "lossless",
	}); err == nil {
		t.Fatal("999 kbit/s was stored")
	}
}

// The note an import leaves for the scan that has not happened yet. The library
// row for that path does not exist when the import writes it, and the scan is
// what turns the two into one row.
func TestRecordTranscodedFileNotesWhatTheFileUsedToBe(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	if err := queries.RecordTranscodedFile(ctx, RecordTranscodeParams{
		LocalPath:       "/music/Artist/Album/01 First.mp3",
		SourceFormat:    "flac",
		SourceSizeBytes: 40_000_000,
		TargetFormat:    "mp3",
	}); err != nil {
		t.Fatal(err)
	}

	var format string
	var size int64
	if err := pool.QueryRow(ctx, `
		SELECT source_format, source_size_bytes FROM import_transcodes WHERE local_path = $1
	`, "/music/Artist/Album/01 First.mp3").Scan(&format, &size); err != nil {
		t.Fatal(err)
	}
	if format != "flac" || size != 40_000_000 {
		t.Fatalf("format = %q, size = %d", format, size)
	}
}

// ptr is a pointer to a copy of value, for a field like TranscodeEnabled that
// distinguishes "explicitly this" from "no opinion" and so cannot be a bare
// literal.
func ptr[T any](value T) *T { return &value }
