package soundcloud

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/pxldi/schall/internal/library"
)

// uploadedUnknown puts the fixture file where the scan files an upload with no
// tags, under Unknown in a library root, and gives its row that root.
func uploadedUnknown(t *testing.T, ctx context.Context, fixture adoptFixture) (string, string) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "Unknown", "[1bebfa12]", "PTK - not like us.flac")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(fixture.path, path); err != nil {
		t.Fatal(err)
	}
	var rootID uuid.UUID
	if err := fixture.pool.QueryRow(ctx,
		`INSERT INTO library_roots (path) VALUES ($1) RETURNING id`, root,
	).Scan(&rootID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `
		UPDATE library_files SET path = $2, library_root_id = $3 WHERE id = $1
	`, fixture.fileID, path, rootID); err != nil {
		t.Fatal(err)
	}
	return root, path
}

func TestANamedUploadIsFiledInItsArtistsFolderUnderItsName(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := setupAdopt(t, ctx)
	root, uploaded := uploadedUnknown(t, ctx, fixture)
	service := NewService(fixture.pool, &stubTracks{track: anEdit()}, zerolog.Nop()).
		WithFiler(library.NewMover(fixture.pool, zerolog.Nop()))

	adopted, err := service.Adopt(ctx, fixture.fileID, "https://soundcloud.com/abo/illegal", Naming{})
	if err != nil {
		t.Fatalf("Adopt() error = %v", err)
	}

	want := filepath.Join(root, "PinkPantheress", "Illegal (Abo Edit) [soundcloud-293]",
		"PinkPantheress - Illegal (Abo Edit).flac")
	if adopted.Path != want {
		t.Fatalf("filed at %q, want %q", adopted.Path, want)
	}
	var path string
	if err := fixture.pool.QueryRow(ctx,
		`SELECT path FROM library_files WHERE id = $1`, fixture.fileID).Scan(&path); err != nil {
		t.Fatal(err)
	}
	if path != want {
		t.Fatalf("the row says %q, want %q", path, want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("the audio is not at its new path: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(uploaded)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the emptied upload folder is still there: %v", err)
	}
}

func TestAFiledUploadLeavesNoRunOnTheLayoutPage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := setupAdopt(t, ctx)
	uploadedUnknown(t, ctx, fixture)
	mover := library.NewMover(fixture.pool, zerolog.Nop())
	service := NewService(fixture.pool, &stubTracks{track: anEdit()}, zerolog.Nop()).WithFiler(mover)

	if _, err := service.Adopt(ctx, fixture.fileID, "https://soundcloud.com/abo/illegal", Naming{}); err != nil {
		t.Fatalf("Adopt() error = %v", err)
	}

	if _, err := mover.LatestRun(ctx); !errors.Is(err, library.ErrRunNotFound) {
		t.Fatalf("LatestRun() error = %v, want no run", err)
	}
}

func TestAnUploadIsNotFiledOverAFileAlreadyAtItsDestination(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := setupAdopt(t, ctx)
	root, uploaded := uploadedUnknown(t, ctx, fixture)
	taken := filepath.Join(root, "PinkPantheress", "Illegal (Abo Edit) [soundcloud-293]",
		"PinkPantheress - Illegal (Abo Edit).flac")
	if err := os.MkdirAll(filepath.Dir(taken), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(taken, []byte("somebody else's audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	service := NewService(fixture.pool, &stubTracks{track: anEdit()}, zerolog.Nop()).
		WithFiler(library.NewMover(fixture.pool, zerolog.Nop()))

	adopted, err := service.Adopt(ctx, fixture.fileID, "https://soundcloud.com/abo/illegal", Naming{})
	if err != nil {
		t.Fatalf("Adopt() error = %v", err)
	}

	if adopted.Path != uploaded {
		t.Fatalf("filed at %q, want it left at %q", adopted.Path, uploaded)
	}
	if content, _ := os.ReadFile(taken); string(content) != "somebody else's audio" {
		t.Fatal("the file already at the destination was overwritten")
	}
}
