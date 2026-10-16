package library

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/dbtest"
	"github.com/rs/zerolog"
)

// The damaged Hum FLAC: it holds the same recording as the MP3 beside it, and
// the person chose it to go so the MP3 can take the track.
func TestRemovingAChosenCopyDeletesItBesideTheKeptFile(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)
	recordingID := uuid.New()
	album := filepath.Join(rootPath, "Hum", "Electra 2000 [4e1612a2]")
	flacPath := filepath.Join(album, "05 - Scraper.flac")
	mp3Path := filepath.Join(album, "5 - Scraper.mp3")
	flacID := libraryFile(t, pool, rootID, flacPath, "Hum", "Electra 2000")
	mp3ID := libraryFile(t, pool, rootID, mp3Path, "Hum", "Electra 2000")
	identifiedAsRecording(t, pool, flacID, recordingID)
	identifiedAsRecording(t, pool, mp3ID, recordingID)

	matcher := &countingMatcher{}
	swept, err := NewRemover(pool, zerolog.Nop()).WithMatcher(matcher).
		RemoveChosenCopies(ctx, []ChosenCopy{{Remove: flacPath, Keep: mp3Path}})
	if err != nil {
		t.Fatal(err)
	}

	if len(swept.RemovedPaths) != 1 || filePresent(t, pool, flacID) {
		t.Fatalf("removed = %v, want the FLAC gone", swept.RemovedPaths)
	}
	if _, err := os.Stat(flacPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the FLAC is still on disc: %v", err)
	}
	licences := removals(t, pool)
	if len(licences) != 1 || licences[0].removedBy != ChosenCopyRemovalActor {
		t.Fatalf("licences = %+v, want one by %q", licences, ChosenCopyRemovalActor)
	}
	if len(matcher.reconciled) == 0 || matcher.reconciled[0] != mp3ID {
		t.Fatalf("reconciled = %v, want the kept MP3 %v first", matcher.reconciled, mp3ID)
	}
}

// "07 who do you love.mp3": a failed import placed it and nothing recorded
// what it is. It goes on the person's choice, with a licence.
func TestRemovingAChosenCopyWithNoIdentityWritesItsLicence(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)
	recordingID := uuid.New()
	album := filepath.Join(rootPath, "Pashanim", "grünewürfelflow [f5c05ff4]")
	extraPath := filepath.Join(album, "07 who do you love.mp3")
	keptPath := filepath.Join(album, "0107 - who do you love.mp3")
	extraID := libraryFile(t, pool, rootID, extraPath, "Pashanim", "grünewürfelflow")
	keptID := libraryFile(t, pool, rootID, keptPath, "Pashanim", "grünewürfelflow")
	identifiedAsRecording(t, pool, keptID, recordingID)

	if _, err := NewRemover(pool, zerolog.Nop()).
		RemoveChosenCopies(ctx, []ChosenCopy{{Remove: extraPath, Keep: keptPath}}); err != nil {
		t.Fatal(err)
	}

	if filePresent(t, pool, extraID) {
		t.Fatal("the chosen copy still has a library row")
	}
	if _, err := os.Stat(extraPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the chosen copy is still on disc: %v", err)
	}
	licences := removals(t, pool)
	if len(licences) != 1 || !licences[0].keptFileID.Valid || licences[0].keptFileID.UUID != keptID ||
		!licences[0].recordingID.Valid || licences[0].recordingID.UUID != recordingID ||
		!licences[0].unlinkedAt.Valid {
		t.Fatalf("licences = %+v, want one keeping %v for recording %v", licences, keptID, recordingID)
	}
}

// A chosen file that turns out to be other music is not the copy the person
// was shown, so it stays.
func TestRemovingAChosenCopyLeavesAFileOfAnotherRecording(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)
	otherPath := filepath.Join(rootPath, "a", "other.mp3")
	keptPath := filepath.Join(rootPath, "a", "kept.mp3")
	otherID := libraryFile(t, pool, rootID, otherPath, "Hum", "Electra 2000")
	keptID := libraryFile(t, pool, rootID, keptPath, "Hum", "Electra 2000")
	identifiedAsRecording(t, pool, otherID, uuid.New())
	identifiedAsRecording(t, pool, keptID, uuid.New())

	swept, err := NewRemover(pool, zerolog.Nop()).
		RemoveChosenCopies(ctx, []ChosenCopy{{Remove: otherPath, Keep: keptPath}})
	if err != nil {
		t.Fatal(err)
	}
	if !filePresent(t, pool, otherID) || swept.LeftAlone != 1 {
		t.Fatalf("removed = %v, left alone = %d; want the other recording kept", swept.RemovedPaths, swept.LeftAlone)
	}
}
