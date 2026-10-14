package acquisition

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/dbtest"
	"github.com/pxldi/schall/internal/musicbrainz"
)

// settledYesterday is a want that settled against a file a day ago, when the
// library still called that file the want's recording.
func settledYesterday(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool, targetID, fileID uuid.UUID,
) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		UPDATE acquisition_targets
		SET status = 'acquired', acquired_at = now() - interval '1 day',
		    acquired_library_file_id = $2, next_attempt_at = NULL, summary = 'In your library.'
		WHERE id = $1
	`, targetID, fileID); err != nil {
		t.Fatalf("settle want %s: %v", targetID, err)
	}
}

// answeredBefore moves the library's answer about a file to before the want
// settled, so it is not a change the want has missed.
func answeredBefore(ctx context.Context, t *testing.T, pool *pgxpool.Pool, fileID uuid.UUID) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		UPDATE library_file_identities SET created_at = now() - interval '2 days'
		WHERE library_file_id = $1
	`, fileID); err != nil {
		t.Fatalf("age the identity of file %s: %v", fileID, err)
	}
}

// Placebo "Every You Every Me" settled on its copy, and AcoustID later filed the
// file as "Bright Lights". The want stayed acquired and its track said "Fetched
// once, file gone" over a file that is there. Now the copy is judged again: two
// rows that share no registration stop for a person in Review, and nothing is
// fetched.
func TestASettledWantWhoseFileIsFiledAsAnotherSongIsAskedInReview(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	wanted, filed := uuid.New(), uuid.New()
	provider := &catalogueProvider{recordings: map[uuid.UUID]musicbrainz.Recording{
		wanted: recording(wanted, "Glory Box", "Portishead", 301000, "GBAAA9400001"),
		filed:  recording(filed, "Glory Box (Live)", "Portishead", 305000, "GBAAA9400002"),
	}}
	service := filingService(pool, provider)
	target := createWant(ctx, t, service, wanted)
	fileID := acceptedCopy(ctx, t, pool, target.ID, wanted)
	alreadyCompleted(ctx, t, pool, target.ID, fileID)
	settledYesterday(ctx, t, pool, target.ID, fileID)
	filedAs(ctx, t, pool, fileID, filed, false)

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	stopped := targetRow(ctx, t, service, target.ID)
	if stopped.Status != "pending" || stopped.NextAttemptAt.Valid {
		t.Fatalf("status = %s next = %v (%q), want a want stopped for a person",
			stopped.Status, stopped.NextAttemptAt, stopped.Summary)
	}
	if !strings.Contains(stopped.Summary, "Glory Box") {
		t.Errorf("summary = %q, want it to name the two songs", stopped.Summary)
	}
	if stopped.AcquiredLibraryFileID.Valid {
		t.Errorf("acquired file = %v, want none on a want that is not settled",
			stopped.AcquiredLibraryFileID)
	}
}

// The file was later filed under a second row of the same song. The copy's own
// proof and the two rows put it back under the wanted row, and the want stays
// settled with its song held.
func TestASettledWantWhoseFileIsFiledUnderASecondRowStaysHeld(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	wanted, filed := uuid.New(), uuid.New()
	service := filingService(pool, twoRowsOfOneName(wanted, filed))
	target := createWant(ctx, t, service, wanted)
	fileID := acceptedCopy(ctx, t, pool, target.ID, wanted)
	alreadyCompleted(ctx, t, pool, target.ID, fileID)
	settledYesterday(ctx, t, pool, target.ID, fileID)
	filedAs(ctx, t, pool, fileID, filed, false)

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	settled := targetRow(ctx, t, service, target.ID)
	if settled.Status != "acquired" {
		t.Fatalf("status = %s (%q), want acquired", settled.Status, settled.Summary)
	}
	if owned := ownerOf(ctx, t, pool, wanted); owned != fileID {
		t.Errorf("file holding the wanted row = %s, want %s", owned, fileID)
	}
}

// A person filed the file under a row of the same registration after the want
// settled. The want is looked at again: the person's answer stays and the file
// counts for the want's row as a second row.
func TestAPersonsAnswerAfterTheWantSettledStaysAndCountsAsASecondRow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	wanted, filed := uuid.New(), uuid.New()
	service := filingService(pool, twoRowsOf(wanted, filed, "GBAAA9400001"))
	target := createWant(ctx, t, service, wanted)
	fileID := acceptedCopy(ctx, t, pool, target.ID, wanted)
	anchorOf(ctx, t, pool, target.ID, 0.07)
	alreadyCompleted(ctx, t, pool, target.ID, fileID)
	settledYesterday(ctx, t, pool, target.ID, fileID)
	filedAs(ctx, t, pool, fileID, filed, true)

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	settled := targetRow(ctx, t, service, target.ID)
	if settled.Status != "acquired" {
		t.Fatalf("status = %s (%q), want acquired", settled.Status, settled.Summary)
	}
	stored, _, manual := identityOfFile(ctx, t, pool, fileID)
	if stored != filed || !manual {
		t.Errorf("identity = %s manual=%v, want the person's answer %s", stored, manual, filed)
	}
	if owned := ownerOf(ctx, t, pool, wanted); owned != fileID {
		t.Errorf("file holding the wanted row = %s, want %s", owned, fileID)
	}
}

// A want with no copy of its own settled on a file the library then filed
// under the album track of the wanted single. MusicBrainz holds the two as one
// artist, one title and one length, so the file counts for the single too and
// the want stays settled. Nothing is fetched.
func TestASettledWantWithNoCopyStaysSettledOnASecondRow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	single, album := uuid.New(), uuid.New()
	service := filingService(pool, singleAndAlbumTrack(single, album, 301000))
	fileID := ownedFile(ctx, t, pool, album)
	target := createWant(ctx, t, service, single)
	settledYesterday(ctx, t, pool, target.ID, fileID)

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	settled := targetRow(ctx, t, service, target.ID)
	if settled.Status != "acquired" {
		t.Fatalf("status = %s (%q), want acquired", settled.Status, settled.Summary)
	}
	if owned := ownerOf(ctx, t, pool, single); owned != fileID {
		t.Errorf("file holding the single = %s, want %s", owned, fileID)
	}
}

// A sweep after the first reads only the wants whose file was answered again
// since they settled. A want that settled on purpose against a file of another
// row would otherwise open again on every sweep.
func TestOnlyAnAnswerAfterTheWantSettledListsItAgain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := db.New(pool)

	wanted, filed := uuid.New(), uuid.New()
	service := newTestService(pool)
	target := createWant(ctx, t, service, wanted)
	fileID := acceptedCopy(ctx, t, pool, target.ID, wanted)
	alreadyCompleted(ctx, t, pool, target.ID, fileID)
	settledYesterday(ctx, t, pool, target.ID, fileID)
	filedAs(ctx, t, pool, fileID, filed, false)

	changed, err := queries.LostWants(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 1 || changed[0].Target.ID != target.ID || !changed[0].HoldsCopy {
		t.Fatalf("lost wants = %+v, want the one answered again, holding its copy", changed)
	}

	answeredBefore(ctx, t, pool, fileID)
	changed, err = queries.LostWants(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 0 {
		t.Errorf("lost wants = %+v, want none answered since they settled", changed)
	}
}

// A want with no copy of its own settled on a file the library then filed as
// another song, and no row MusicBrainz holds makes the two one song. Placebo
// "Every You Every Me" settled on what AcoustID calls "Bright Lights". The want
// is looked for again, and only a copy AcoustID proves will settle it.
func TestASettledWantWithNoCopyIsLookedForWhenItsFileIsAnotherSong(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	wanted, filed := uuid.New(), uuid.New()
	hunter := &fakeHunter{}
	service := hunting(pool, hunter, &fakeFetcher{}, true)
	fileID := ownedFile(ctx, t, pool, filed)
	target := createWant(ctx, t, service, wanted)
	settledYesterday(ctx, t, pool, target.ID, fileID)

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	looked := targetRow(ctx, t, service, target.ID)
	if looked.Status != "pending" || looked.AcquiredLibraryFileID.Valid {
		t.Fatalf("status = %s file = %v (%q), want a pending want holding no file",
			looked.Status, looked.AcquiredLibraryFileID, looked.Summary)
	}
	if len(hunter.queries) == 0 {
		t.Errorf("searches = 0, want the song looked for again")
	}
}

// A file the library has no answer for is silence. A want with no copy that
// settled on it stays settled and is not looked for.
func TestASettledWantWithNoCopyStaysOnAFileWithNoAnswer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	wanted := uuid.New()
	hunter := &fakeHunter{}
	service := hunting(pool, hunter, &fakeFetcher{}, true)
	fileID := ownedFile(ctx, t, pool, wanted)
	if _, err := pool.Exec(ctx, `DELETE FROM library_file_identities WHERE library_file_id = $1`,
		fileID); err != nil {
		t.Fatal(err)
	}
	target := createWant(ctx, t, service, wanted)
	settledYesterday(ctx, t, pool, target.ID, fileID)

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	if settled := targetRow(ctx, t, service, target.ID); settled.Status != "acquired" {
		t.Errorf("status = %s (%q), want acquired", settled.Status, settled.Summary)
	}
	if len(hunter.queries) != 0 {
		t.Errorf("searches = %d, want none", len(hunter.queries))
	}
}

// attemptsOf counts the attempt rows a want has written.
func attemptsOf(ctx context.Context, t *testing.T, pool *pgxpool.Pool, targetID uuid.UUID) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM acquisition_target_attempts WHERE acquisition_target_id = $1
	`, targetID).Scan(&count); err != nil {
		t.Fatalf("count the attempts of want %s: %v", targetID, err)
	}
	return count
}

// mappedOnto puts a library file under a catalogue track carrying a recording,
// with no identity beside it, the way tag matching maps a file.
func mappedOnto(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool, fileID, recordingID uuid.UUID,
) {
	t.Helper()
	var artistID, albumID, trackID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO artists (musicbrainz_id, name, sort_name)
		VALUES ($1, 'Portishead', 'Portishead')
		RETURNING id
	`, uuid.New()).Scan(&artistID); err != nil {
		t.Fatalf("seed artist: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO albums (artist_id, musicbrainz_release_group_id, title, album_type, secondary_types)
		VALUES ($1, $2, 'Dummy', 'album', '{}')
		RETURNING id
	`, artistID, uuid.New()).Scan(&albumID); err != nil {
		t.Fatalf("seed album: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO tracks (album_id, musicbrainz_recording_id, title, disc_number, track_number, duration_ms)
		VALUES ($1, $2, 'Glory Box', 1, 11, 301000)
		RETURNING id
	`, albumID, recordingID).Scan(&trackID); err != nil {
		t.Fatalf("seed track: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO track_mappings (track_id, library_file_id, method, is_manual)
		VALUES ($1, $2, 'musicbrainz_recording_id', false)
	`, trackID, fileID); err != nil {
		t.Fatalf("seed track mapping: %v", err)
	}
}

// RIN "UP IN SMOKE" settled on its copy, and tag matching mapped the file onto
// the album row of the same song with no identity beside it. There is no
// identity to hang the single's row off, so nothing recorded the settlement and
// every sweep opened the want again and asked MusicBrainz about it, about once a
// minute live on 2026-10-14. The want now stays settled.
func TestAWantSettledOnAFileMappedOntoAnotherRowOfItsSongStaysSettled(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	single, album := uuid.New(), uuid.New()
	service := filingService(pool, singleAndAlbumTrack(single, album, 301000))
	target := createWant(ctx, t, service, single)
	fileID := acceptedCopy(ctx, t, pool, target.ID, single)
	alreadyCompleted(ctx, t, pool, target.ID, fileID)
	settledYesterday(ctx, t, pool, target.ID, fileID)
	mappedOnto(ctx, t, pool, fileID, album)
	before := attemptsOf(ctx, t, pool, target.ID)

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	settled := targetRow(ctx, t, service, target.ID)
	if settled.Status != "acquired" {
		t.Fatalf("status = %s (%q), want acquired", settled.Status, settled.Summary)
	}
	if after := attemptsOf(ctx, t, pool, target.ID); after != before {
		t.Errorf("attempts = %d, want %d: the want was opened and settled again", after, before)
	}
}

// Placebo "Every You Every Me" settled on purpose against a file the library
// had already filed under another row, as a clean edition held as the explicit
// one is (ADR 0032). The first sweep of every process opened it again and
// settled it again, eight wants at each start live on 2026-10-14. Only an answer
// given after the want settled opens it now.
func TestAWantSettledOnAFileAnsweredBeforeItSettledIsNotOpenedAgain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	wanted, filed := uuid.New(), uuid.New()
	provider := &catalogueProvider{recordings: map[uuid.UUID]musicbrainz.Recording{
		wanted: recording(wanted, "Glory Box", "Portishead", 301000, "GBAAA9400001"),
		filed:  recording(filed, "Glory Box (Live)", "Portishead", 305000, "GBAAA9400002"),
	}}
	service := filingService(pool, provider)
	target := createWant(ctx, t, service, wanted)
	fileID := acceptedCopy(ctx, t, pool, target.ID, wanted)
	alreadyCompleted(ctx, t, pool, target.ID, fileID)
	settledYesterday(ctx, t, pool, target.ID, fileID)
	filedAs(ctx, t, pool, fileID, filed, false)
	answeredBefore(ctx, t, pool, fileID)
	before := attemptsOf(ctx, t, pool, target.ID)

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	settled := targetRow(ctx, t, service, target.ID)
	if settled.Status != "acquired" {
		t.Fatalf("status = %s (%q), want acquired", settled.Status, settled.Summary)
	}
	if after := attemptsOf(ctx, t, pool, target.ID); after != before {
		t.Errorf("attempts = %d, want %d: the want was opened and settled again", after, before)
	}
}
