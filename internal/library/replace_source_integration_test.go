package library

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/dbtest"
	"github.com/rs/zerolog"
)

// identifiedAsSourceTrack records that a file is the track published at a
// service's address, the identity a keyed want's copy is written.
func identifiedAsSourceTrack(t *testing.T, pool *pgxpool.Pool, fileID uuid.UUID, externalID string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO library_file_identities (
			library_file_id, kind, provider, source, external_id, external_url,
			method, is_manual, summary
		) VALUES ($1, 'source', 'soundcloud', 'soundcloud', $2,
		          'https://soundcloud.com/abo/illegal-edit', 'source_url', true, 'the track at the address')
	`, fileID, externalID); err != nil {
		t.Fatal(err)
	}
}

func TestReplaceIfBetterReplacesAServiceTracksFileWithABetterCopyOfTheSameTrack(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	switchOnUpgrades(t, pool)
	rootID, rootPath := libraryRoot(t, pool)

	oldPath := filepath.Join(rootPath, "old.m4a")
	newPath := filepath.Join(rootPath, "new.mp3")
	oldID := upgradeableFile(t, pool, rootID, oldPath, 160)
	newID := upgradeableFile(t, pool, rootID, newPath, 320)
	identifiedAsSourceTrack(t, pool, oldID, "soundcloud:293")
	identifiedAsSourceTrack(t, pool, newID, "soundcloud:293")

	replaced, err := NewRemover(pool, zerolog.Nop()).ReplaceIfBetter(ctx, oldID, newID)
	if err != nil {
		t.Fatal(err)
	}
	if !replaced {
		t.Fatal("a better copy of the same track did not replace the old file")
	}
	if _, err := os.Stat(oldPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the old audio is still on disc: %v", err)
	}
	written := removals(t, pool)
	if len(written) != 1 {
		t.Fatalf("removal rows = %d, want 1", len(written))
	}
	if written[0].sourceKey != "soundcloud:293" || written[0].recordingID.Valid {
		t.Fatalf("removal names %q and recording %v, want the track's key and no recording",
			written[0].sourceKey, written[0].recordingID)
	}
	if !written[0].keptFileID.Valid || written[0].keptFileID.UUID != newID {
		t.Fatalf("removal kept file = %v, want %v", written[0].keptFileID, newID)
	}
}

func TestReplaceIfBetterLeavesAFileOfADifferentServiceTrack(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	switchOnUpgrades(t, pool)
	rootID, rootPath := libraryRoot(t, pool)

	oldPath := filepath.Join(rootPath, "old.m4a")
	oldID := upgradeableFile(t, pool, rootID, oldPath, 160)
	newID := upgradeableFile(t, pool, rootID, filepath.Join(rootPath, "new.mp3"), 320)
	identifiedAsSourceTrack(t, pool, oldID, "soundcloud:293")
	identifiedAsSourceTrack(t, pool, newID, "soundcloud:294")

	replaced, err := NewRemover(pool, zerolog.Nop()).ReplaceIfBetter(ctx, oldID, newID)
	if err != nil {
		t.Fatal(err)
	}
	if replaced {
		t.Fatal("a copy of another track replaced the file")
	}
	if _, err := os.Stat(oldPath); err != nil {
		t.Fatalf("the old audio is gone: %v", err)
	}
	if len(removals(t, pool)) != 0 {
		t.Fatal("a removal was recorded for a different track's file")
	}
}

// A service track's file that is also mapped onto a catalogue track is that
// track's copy too, so a better copy of the service track is no licence to
// delete it.
func TestReplaceIfBetterKeepsAServiceTracksFileThatAnswersACatalogueTrack(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	switchOnUpgrades(t, pool)
	rootID, rootPath := libraryRoot(t, pool)

	oldPath := filepath.Join(rootPath, "old.m4a")
	oldID := upgradeableFile(t, pool, rootID, oldPath, 160)
	newID := upgradeableFile(t, pool, rootID, filepath.Join(rootPath, "new.mp3"), 320)
	identifiedAsSourceTrack(t, pool, oldID, "soundcloud:293")
	identifiedAsSourceTrack(t, pool, newID, "soundcloud:293")
	mapTo(t, pool, catalogueTrack(t, pool, "Another Artist", "Another Album"), oldID)

	replaced, err := NewRemover(pool, zerolog.Nop()).ReplaceIfBetter(ctx, oldID, newID)
	if err != nil {
		t.Fatal(err)
	}
	if replaced {
		t.Fatal("a file that answers a catalogue track was replaced")
	}
	if _, err := os.Stat(oldPath); err != nil {
		t.Fatalf("the old audio is gone: %v", err)
	}
}

// A file with a recording and a file with only a service key do not agree on
// what they are, whatever their bit rates say.
func TestReplaceIfBetterLeavesARecordingsFileForAServiceTrackCopy(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	switchOnUpgrades(t, pool)
	rootID, rootPath := libraryRoot(t, pool)

	oldPath := filepath.Join(rootPath, "old.mp3")
	oldID := upgradeableFile(t, pool, rootID, oldPath, 128)
	newID := upgradeableFile(t, pool, rootID, filepath.Join(rootPath, "new.mp3"), 320)
	identifiedAsRecording(t, pool, oldID, uuid.New())
	identifiedAsSourceTrack(t, pool, newID, "soundcloud:293")

	replaced, err := NewRemover(pool, zerolog.Nop()).ReplaceIfBetter(ctx, oldID, newID)
	if err != nil {
		t.Fatal(err)
	}
	if replaced {
		t.Fatal("a service track's copy replaced a recording's file")
	}
	if _, err := os.Stat(oldPath); err != nil {
		t.Fatalf("the old audio is gone: %v", err)
	}
}
