package acquisition

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/dbtest"
	"github.com/pxldi/schall/internal/musicbrainz"
)

// singleAndAlbumTrack is "HARDX" by yaego as MusicBrainz holds it: the single
// and the album track are two rows with one artist, one title and one length,
// and no registration code on either.
func singleAndAlbumTrack(single, album uuid.UUID, albumLengthMS int) *catalogueProvider {
	return &catalogueProvider{recordings: map[uuid.UUID]musicbrainz.Recording{
		single: credited(recording(single, "Glory Box", "Portishead", 301000), "Portishead"),
		album:  credited(recording(album, "Glory Box", "Portishead", albumLengthMS), "Portishead"),
	}}
}

// ownedFile is a present library file the library has proven to be a recording.
func ownedFile(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool, recordingID uuid.UUID,
) uuid.UUID {
	t.Helper()
	fileID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_files (id, path, size_bytes, modified_at, resolution_status)
		VALUES ($1, '/music/Portishead/Dummy/05 Glory Box.flac', 25000000, now(), 'resolved')
	`, fileID); err != nil {
		t.Fatalf("seed the owned file: %v", err)
	}
	filedAs(ctx, t, pool, fileID, recordingID, false)
	return fileID
}

// The library holds the album track, and the want names the single: a second
// MusicBrainz row of the same song. The want settles against the file the
// library has before anything is fetched. Before this it went looking and
// fetched the same audio again.
func TestAWantSettlesAgainstAnOwnedSecondRowOfItsSong(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	single, album := uuid.New(), uuid.New()
	service := filingService(pool, singleAndAlbumTrack(single, album, 301000))
	fileID := ownedFile(ctx, t, pool, album)
	target := createWant(ctx, t, service, single)

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	settled := targetRow(ctx, t, service, target.ID)
	if settled.Status != "acquired" {
		t.Fatalf("status = %s summary = %q, want acquired", settled.Status, settled.Summary)
	}
	if !settled.AcquiredLibraryFileID.Valid || settled.AcquiredLibraryFileID.UUID != fileID {
		t.Errorf("acquired file = %v, want %s", settled.AcquiredLibraryFileID, fileID)
	}
	stored, _, _ := identityOfFile(ctx, t, pool, fileID)
	if stored != album {
		t.Errorf("identity = %s, want the library's own answer %s untouched", stored, album)
	}
	if owned := ownerOf(ctx, t, pool, single); owned != fileID {
		t.Errorf("file owning the wanted row = %s, want %s", owned, fileID)
	}
}

// A row of the same title that runs a different length is another performance,
// a live take or an edit, and the want for it is left to look.
func TestAnOwnedRowOfAnotherLengthDoesNotSettleTheWant(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	single, live := uuid.New(), uuid.New()
	service := filingService(pool, singleAndAlbumTrack(single, live, 330000))
	ownedFile(ctx, t, pool, live)
	target := createWant(ctx, t, service, single)

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	if row := targetRow(ctx, t, service, target.ID); row.Status == "acquired" {
		t.Fatalf("status = acquired, want the want left looking")
	}
}

// A want that stopped while a copy waits for a person is never due. The first
// sweep of a process asks the library about it again, and it settles against
// the owned second row instead of staying in Review.
func TestTheFirstSweepSettlesAWantWaitingOnACopyOfASongItOwns(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	single, album := uuid.New(), uuid.New()
	service := filingService(pool, singleAndAlbumTrack(single, album, 301000))
	target := createWant(ctx, t, service, single)
	if _, err := pool.Exec(ctx, `
		INSERT INTO acquisition_target_files (
			acquisition_target_id, provider, source_username, remote_path, file_name,
			verdict, summary
		)
		VALUES ($1, 'slskd', 'peer', 'Music\Portishead\Dummy\05 Glory Box.flac',
		        '05 Glory Box.flac', 'held', 'Tags match, audio unchecked. Listen to decide.')
	`, target.ID); err != nil {
		t.Fatalf("seed the waiting copy: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE acquisition_targets
		SET next_attempt_at = NULL, summary = 'A copy is waiting. Listen to decide.'
		WHERE id = $1
	`, target.ID); err != nil {
		t.Fatalf("stop the want on its waiting copy: %v", err)
	}
	fileID := ownedFile(ctx, t, pool, album)

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	settled := targetRow(ctx, t, service, target.ID)
	if settled.Status != "acquired" {
		t.Fatalf("status = %s summary = %q, want acquired", settled.Status, settled.Summary)
	}
	if !settled.AcquiredLibraryFileID.Valid || settled.AcquiredLibraryFileID.UUID != fileID {
		t.Errorf("acquired file = %v, want %s", settled.AcquiredLibraryFileID, fileID)
	}
}

// ownerOf is the file the library counts as holding a recording.
func ownerOf(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool, recordingID uuid.UUID,
) uuid.UUID {
	t.Helper()
	owned, err := db.New(pool).OwnedFileForRecording(ctx, recordingID)
	if err != nil {
		t.Fatalf("OwnedFileForRecording() error = %v", err)
	}
	return owned.UUID
}
