package library

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/dbtest"
	"github.com/rs/zerolog"
)

// identifiedAsRecording records the decision that a file is one named
// MusicBrainz recording. It is one of the two routes RecordingsHeldTwice reads
// and the only one these tests need: the other, a mapping onto a catalogue
// track, is exercised where a mapping is what the test is about.
func identifiedAsRecording(t *testing.T, pool *pgxpool.Pool, fileID, recordingID uuid.UUID) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO library_file_identities (
			library_file_id, kind, musicbrainz_recording_id, method, summary
		) VALUES ($1, 'external', $2, 'acoustid', 'the audio is this recording')
	`, fileID, recordingID); err != nil {
		t.Fatal(err)
	}
}

// trackForRecording builds the smallest catalogue a mapping needs — an artist,
// a release and one track on it — for a named recording. It is catalogueTrack
// with the recording chosen rather than invented, because these tests are about
// which recording a file answers.
func trackForRecording(
	t *testing.T, pool *pgxpool.Pool, artist, album string, recordingID uuid.UUID,
) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	var artistID, albumID, trackID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO artists (musicbrainz_id, name, sort_name) VALUES ($1, $2, $2) RETURNING id
	`, uuid.New(), artist).Scan(&artistID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO albums (artist_id, musicbrainz_release_group_id, title)
		VALUES ($1, $2, $3) RETURNING id
	`, artistID, uuid.New(), album).Scan(&albumID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO tracks (album_id, musicbrainz_recording_id, title, disc_number, track_number)
		VALUES ($1, $2, 'A song', 1, 1) RETURNING id
	`, albumID, recordingID).Scan(&trackID); err != nil {
		t.Fatal(err)
	}
	return trackID
}

// removalRow is the licence: what was deleted, what it was a copy of, and what
// stayed. It is read back by these tests because a deletion that left no record
// is the failure ADR 0021 exists to stop.
type removalRow struct {
	path        string
	sizeBytes   int64
	keptFileID  uuid.NullUUID
	keptPath    string
	removedBy   string
	reason      string
	recordingID uuid.UUID
	unlinkedAt  pgtype.Timestamptz
	unlinkError string
}

func removals(t *testing.T, pool *pgxpool.Pool) []removalRow {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT library_path, size_bytes, kept_library_file_id, kept_path,
		       removed_by, reason, musicbrainz_recording_id,
		       unlinked_at, unlink_error
		FROM library_file_removals
		ORDER BY library_path
	`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	var found []removalRow
	for rows.Next() {
		var row removalRow
		if err := rows.Scan(&row.path, &row.sizeBytes, &row.keptFileID, &row.keptPath,
			&row.removedBy, &row.reason, &row.recordingID,
			&row.unlinkedAt, &row.unlinkError); err != nil {
			t.Fatal(err)
		}
		found = append(found, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return found
}

func filePresent(t *testing.T, pool *pgxpool.Pool, fileID uuid.UUID) bool {
	t.Helper()
	var present bool
	if err := pool.QueryRow(context.Background(),
		`SELECT EXISTS (SELECT 1 FROM library_files WHERE id = $1)`, fileID,
	).Scan(&present); err != nil {
		t.Fatal(err)
	}
	return present
}

// The whole feature in one pass: the surplus copies go, from the database and
// from the disc, the chosen copy stays, and the survivor is judged again so it
// can take the track its companions were contesting.
func TestKeepingOneCopyDeletesTheOtherCopiesOfThatRecording(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)
	recordingID := uuid.New()

	keeperPath := filepath.Join(rootPath, "Portishead", "Dummy", "roads.flac")
	sparePath := filepath.Join(rootPath, "Singles", "roads.mp3")
	keeperID := libraryFile(t, pool, rootID, keeperPath, "Portishead", "Dummy")
	spareID := libraryFile(t, pool, rootID, sparePath, "Portishead", "Roads")
	identifiedAsRecording(t, pool, keeperID, recordingID)
	identifiedAsRecording(t, pool, spareID, recordingID)

	matcher := &countingMatcher{}
	kept, err := NewRemover(pool, zerolog.Nop()).WithMatcher(matcher).
		KeepOne(ctx, recordingID, keeperID, []uuid.UUID{spareID}, DuplicateScreenRemovalActor)
	if err != nil {
		t.Fatal(err)
	}

	if kept.Kept != keeperID || kept.KeptPath != keeperPath {
		t.Fatalf("kept = %v at %s, want %v at %s", kept.Kept, kept.KeptPath, keeperID, keeperPath)
	}
	if len(kept.RemovedPaths) != 1 || kept.RemovedPaths[0] != sparePath {
		t.Fatalf("removed = %v, want [%s]", kept.RemovedPaths, sparePath)
	}
	if filePresent(t, pool, spareID) {
		t.Fatal("the surplus copy still has a library row")
	}
	if !filePresent(t, pool, keeperID) {
		t.Fatal("the copy that was kept was deleted")
	}
	if _, err := os.Stat(sparePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the surplus audio is still on disc: %v", err)
	}
	if _, err := os.Stat(keeperPath); err != nil {
		t.Fatalf("the kept audio is gone: %v", err)
	}

	// The survivor was one of several files answering one recording, which is
	// why assignment left it unmatched. It is now the only claimant, so it is
	// judged again.
	if len(matcher.reconciled) == 0 || matcher.reconciled[0] != keeperID {
		t.Fatalf("reconciled = %v, want the kept copy %v first", matcher.reconciled, keeperID)
	}
}

// The licence, per docs/decisions/0021. A row saying what went, what it was a
// copy of and what stayed, written in the same transaction as the delete.
func TestKeepingOneCopyRecordsWhyEachFileWent(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)
	recordingID := uuid.New()

	keeperPath := filepath.Join(rootPath, "Portishead", "Dummy", "roads.flac")
	sparePath := filepath.Join(rootPath, "Singles", "roads.mp3")
	keeperID := libraryFile(t, pool, rootID, keeperPath, "Portishead", "Dummy")
	spareID := libraryFile(t, pool, rootID, sparePath, "Portishead", "Roads")
	identifiedAsRecording(t, pool, keeperID, recordingID)
	identifiedAsRecording(t, pool, spareID, recordingID)

	if _, err := NewRemover(pool, zerolog.Nop()).KeepOne(ctx, recordingID, keeperID, []uuid.UUID{spareID}, DuplicateScreenRemovalActor); err != nil {
		t.Fatal(err)
	}

	written := removals(t, pool)
	if len(written) != 1 {
		t.Fatalf("removal rows = %d, want 1", len(written))
	}
	row := written[0]
	if row.path != sparePath {
		t.Fatalf("removal path = %s, want %s", row.path, sparePath)
	}
	if row.sizeBytes != 14 {
		t.Fatalf("removal size = %d, want 14", row.sizeBytes)
	}
	if row.recordingID != recordingID {
		t.Fatalf("removal recording = %v, want %v", row.recordingID, recordingID)
	}
	if !row.keptFileID.Valid || row.keptFileID.UUID != keeperID {
		t.Fatalf("removal kept file = %v, want %v", row.keptFileID, keeperID)
	}
	if row.keptPath != keeperPath {
		t.Fatalf("removal kept path = %s, want %s", row.keptPath, keeperPath)
	}
	if row.removedBy == "" || row.reason == "" {
		t.Fatalf("removal says neither who nor why: %+v", row)
	}
	if row.removedBy != DuplicateScreenRemovalActor {
		t.Fatalf("removed_by = %q, want %q", row.removedBy, DuplicateScreenRemovalActor)
	}
	// The audio went, so the row says when. An empty unlinked_at is what a file
	// still on disc looks like, and this one is not.
	if !row.unlinkedAt.Valid {
		t.Fatal("the removal does not say the audio left the disc")
	}
	if row.unlinkError != "" {
		t.Fatalf("unlink error = %q, want none", row.unlinkError)
	}
}

// The guarantee ADR 0021 asks for, under the failure that used to break it. Two
// surplus copies, and the second one cannot be unlinked. The first one is gone
// for good, so its record must survive: one file is one transaction, and the
// second file's trouble takes nothing back.
func TestACopyThatCannotBeDeletedLeavesTheOthersDeletedAndOnRecord(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)
	recordingID := uuid.New()

	keeperPath := filepath.Join(rootPath, "Portishead", "Dummy", "roads.flac")
	firstPath := filepath.Join(rootPath, "a-singles", "roads.mp3")
	stuckPath := filepath.Join(rootPath, "b-locked", "roads.mp3")
	keeperID := libraryFile(t, pool, rootID, keeperPath, "Portishead", "Dummy")
	firstID := libraryFile(t, pool, rootID, firstPath, "Portishead", "Roads")
	stuckID := libraryFile(t, pool, rootID, stuckPath, "Portishead", "Roads")
	identifiedAsRecording(t, pool, keeperID, recordingID)
	identifiedAsRecording(t, pool, firstID, recordingID)
	identifiedAsRecording(t, pool, stuckID, recordingID)

	// A folder nothing may unlink from. The file inside it stays whatever is
	// asked of it, which is the read-only mount this is standing in for.
	if err := os.Chmod(filepath.Dir(stuckPath), 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Dir(stuckPath), 0o755) })

	kept, err := NewRemover(pool, zerolog.Nop()).
		KeepOne(ctx, recordingID, keeperID, []uuid.UUID{firstID, stuckID}, DuplicateScreenRemovalActor)
	if err != nil {
		t.Fatal(err)
	}

	// Both left the library. One left the disc; the other is named as still on
	// it rather than quietly forgotten.
	if filePresent(t, pool, firstID) || filePresent(t, pool, stuckID) {
		t.Fatal("a copy kept its library row")
	}
	if _, err := os.Stat(firstPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the copy that could be deleted is still on disc: %v", err)
	}
	if len(kept.StillOnDisc) != 1 || kept.StillOnDisc[0] != stuckPath {
		t.Fatalf("still on disc = %v, want [%s]", kept.StillOnDisc, stuckPath)
	}

	written := removals(t, pool)
	if len(written) != 2 {
		t.Fatalf("removal rows = %d, want 2", len(written))
	}
	byPath := map[string]removalRow{}
	for _, row := range written {
		byPath[row.path] = row
	}
	if !byPath[firstPath].unlinkedAt.Valid {
		t.Fatal("the copy that was deleted is not recorded as gone from the disc")
	}
	if byPath[stuckPath].unlinkedAt.Valid {
		t.Fatal("a copy still on disc is recorded as gone from it")
	}
	if byPath[stuckPath].unlinkError == "" {
		t.Fatal("a copy still on disc does not say why")
	}
}

// The only thing to do about a file the disc kept is try again, and trying
// again has to actually finish the job once the reason is gone.
func TestTryingAgainDeletesAFileTheDiscKept(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)
	recordingID := uuid.New()

	keeperPath := filepath.Join(rootPath, "Portishead", "Dummy", "roads.flac")
	stuckPath := filepath.Join(rootPath, "locked", "roads.mp3")
	keeperID := libraryFile(t, pool, rootID, keeperPath, "Portishead", "Dummy")
	stuckID := libraryFile(t, pool, rootID, stuckPath, "Portishead", "Roads")
	identifiedAsRecording(t, pool, keeperID, recordingID)
	identifiedAsRecording(t, pool, stuckID, recordingID)

	if err := os.Chmod(filepath.Dir(stuckPath), 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Dir(stuckPath), 0o755) })

	remover := NewRemover(pool, zerolog.Nop())
	if _, err := remover.KeepOne(ctx, recordingID, keeperID, []uuid.UUID{stuckID}, DuplicateScreenRemovalActor); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stuckPath); err != nil {
		t.Fatalf("the file was deleted from a folder that forbids it: %v", err)
	}

	// Somebody fixed the mount.
	if err := os.Chmod(filepath.Dir(stuckPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if deleted := remover.RetryUnlinks(ctx); deleted != 1 {
		t.Fatalf("deleted = %d, want 1", deleted)
	}
	if _, err := os.Stat(stuckPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the audio is still on disc: %v", err)
	}
	written := removals(t, pool)
	if len(written) != 1 || !written[0].unlinkedAt.Valid || written[0].unlinkError != "" {
		t.Fatalf("removal after the retry = %+v", written)
	}
}

// The screen showed one set of files and the library would delete another. The
// person read a promise about particular files, so nothing goes.
func TestKeepingOneCopyRefusesWhenTheCopiesHaveChanged(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)
	recordingID := uuid.New()

	keeperPath := filepath.Join(rootPath, "Portishead", "Dummy", "roads.flac")
	sparePath := filepath.Join(rootPath, "Singles", "roads.mp3")
	keeperID := libraryFile(t, pool, rootID, keeperPath, "Portishead", "Dummy")
	spareID := libraryFile(t, pool, rootID, sparePath, "Portishead", "Roads")
	identifiedAsRecording(t, pool, keeperID, recordingID)
	identifiedAsRecording(t, pool, spareID, recordingID)

	// The screen was read before this copy arrived, so it named only one file.
	latePath := filepath.Join(rootPath, "Rips", "roads.mp3")
	lateID := libraryFile(t, pool, rootID, latePath, "Portishead", "Roads")
	identifiedAsRecording(t, pool, lateID, recordingID)

	_, err := NewRemover(pool, zerolog.Nop()).
		KeepOne(ctx, recordingID, keeperID, []uuid.UUID{spareID}, DuplicateScreenRemovalActor)
	if !errors.Is(err, ErrCopiesChanged) {
		t.Fatalf("err = %v, want ErrCopiesChanged", err)
	}
	if !filePresent(t, pool, spareID) || !filePresent(t, pool, lateID) {
		t.Fatal("a copy was deleted against a list the person never read")
	}
	if len(removals(t, pool)) != 0 {
		t.Fatal("a removal was recorded when nothing was removed")
	}
}

// A file that is also a copy of other music is not surplus. The press was about
// one recording, and deleting that file would take music nobody asked about.
func TestKeepingOneCopyLeavesAFileThatIsAlsoOtherMusic(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)
	recordingID := uuid.New()

	keeperPath := filepath.Join(rootPath, "Portishead", "Dummy", "roads.flac")
	keeperID := libraryFile(t, pool, rootID, keeperPath, "Portishead", "Dummy")
	identifiedAsRecording(t, pool, keeperID, recordingID)

	// The second copy reaches this recording through a catalogue track, and
	// reaches different music through a second one.
	otherPath := filepath.Join(rootPath, "Compilations", "roads.mp3")
	otherID := libraryFile(t, pool, rootID, otherPath, "Various", "Trip Hop")
	trackID := trackForRecording(t, pool, "Portishead", "Roads", recordingID)
	elsewhere := trackForRecording(t, pool, "Portishead", "Glory Box", uuid.New())
	mapFileToTrack(t, pool, otherID, trackID)
	mapFileToTrack(t, pool, otherID, elsewhere)

	// Every other copy being other music is a state with a reason of its own,
	// and it is not the same answer as "there is no second copy".
	_, err := NewRemover(pool, zerolog.Nop()).KeepOne(ctx, recordingID, keeperID, nil, DuplicateScreenRemovalActor)
	if !errors.Is(err, ErrOnlyOtherMusicLeft) {
		t.Fatalf("err = %v, want ErrOnlyOtherMusicLeft", err)
	}
	if !filePresent(t, pool, otherID) {
		t.Fatal("a file that is also a copy of other music was deleted")
	}
	if len(removals(t, pool)) != 0 {
		t.Fatal("a removal was recorded when nothing was removed")
	}
}

// A different recording's copies are somebody else's question. Nothing about
// this press reaches them.
func TestKeepingOneCopyLeavesOtherRecordingsAlone(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)
	recordingID, elsewhere := uuid.New(), uuid.New()

	keeperPath := filepath.Join(rootPath, "Portishead", "Dummy", "roads.flac")
	sparePath := filepath.Join(rootPath, "Singles", "roads.mp3")
	keeperID := libraryFile(t, pool, rootID, keeperPath, "Portishead", "Dummy")
	spareID := libraryFile(t, pool, rootID, sparePath, "Portishead", "Roads")
	identifiedAsRecording(t, pool, keeperID, recordingID)
	identifiedAsRecording(t, pool, spareID, recordingID)

	strangerPath := filepath.Join(rootPath, "Massive Attack", "Mezzanine", "teardrop.flac")
	strangerID := libraryFile(t, pool, rootID, strangerPath, "Massive Attack", "Mezzanine")
	identifiedAsRecording(t, pool, strangerID, elsewhere)

	if _, err := NewRemover(pool, zerolog.Nop()).KeepOne(ctx, recordingID, keeperID, []uuid.UUID{spareID}, DuplicateScreenRemovalActor); err != nil {
		t.Fatal(err)
	}
	if !filePresent(t, pool, strangerID) {
		t.Fatal("a copy of different music was deleted")
	}
	if _, err := os.Stat(strangerPath); err != nil {
		t.Fatalf("the audio of different music is gone: %v", err)
	}
}

// The worst outcome this route has: every other copy deleted in favour of one
// that is not on disc. Nothing goes.
func TestKeepingACopyThatIsNotOnDiscIsRefused(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)
	recordingID := uuid.New()

	keeperPath := filepath.Join(rootPath, "Portishead", "Dummy", "roads.flac")
	sparePath := filepath.Join(rootPath, "Singles", "roads.mp3")
	keeperID := libraryFile(t, pool, rootID, keeperPath, "Portishead", "Dummy")
	spareID := libraryFile(t, pool, rootID, sparePath, "Portishead", "Roads")
	identifiedAsRecording(t, pool, keeperID, recordingID)
	identifiedAsRecording(t, pool, spareID, recordingID)

	// The row still says the file is present; the audio went by another hand.
	if err := os.Remove(keeperPath); err != nil {
		t.Fatal(err)
	}

	_, err := NewRemover(pool, zerolog.Nop()).KeepOne(ctx, recordingID, keeperID, []uuid.UUID{spareID}, DuplicateScreenRemovalActor)
	if !errors.Is(err, ErrKeeperMissing) {
		t.Fatalf("err = %v, want ErrKeeperMissing", err)
	}
	if !filePresent(t, pool, spareID) {
		t.Fatal("the surplus copy was deleted for a keeper that is not there")
	}
	if _, err := os.Stat(sparePath); err != nil {
		t.Fatalf("the surplus audio is gone: %v", err)
	}
	if len(removals(t, pool)) != 0 {
		t.Fatal("a removal was recorded when nothing was removed")
	}
}

// A file that is no copy of this recording is not a choice this press can make.
func TestKeepingAFileThatIsNotACopyIsRefused(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)
	recordingID := uuid.New()

	firstPath := filepath.Join(rootPath, "Portishead", "Dummy", "roads.flac")
	secondPath := filepath.Join(rootPath, "Singles", "roads.mp3")
	firstID := libraryFile(t, pool, rootID, firstPath, "Portishead", "Dummy")
	secondID := libraryFile(t, pool, rootID, secondPath, "Portishead", "Roads")
	identifiedAsRecording(t, pool, firstID, recordingID)
	identifiedAsRecording(t, pool, secondID, recordingID)

	strangerPath := filepath.Join(rootPath, "Massive Attack", "Mezzanine", "teardrop.flac")
	strangerID := libraryFile(t, pool, rootID, strangerPath, "Massive Attack", "Mezzanine")

	_, err := NewRemover(pool, zerolog.Nop()).KeepOne(ctx, recordingID, strangerID, []uuid.UUID{firstID, secondID}, DuplicateScreenRemovalActor)
	if !errors.Is(err, ErrNotACopy) {
		t.Fatalf("err = %v, want ErrNotACopy", err)
	}
	if !filePresent(t, pool, firstID) || !filePresent(t, pool, secondID) {
		t.Fatal("a copy was deleted for a keeper that is not one of them")
	}
}

// One copy is not a duplicate, so there is nothing to keep one of.
func TestKeepingTheOnlyCopyIsRefused(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)
	recordingID := uuid.New()

	onlyPath := filepath.Join(rootPath, "Portishead", "Dummy", "roads.flac")
	onlyID := libraryFile(t, pool, rootID, onlyPath, "Portishead", "Dummy")
	identifiedAsRecording(t, pool, onlyID, recordingID)

	_, err := NewRemover(pool, zerolog.Nop()).KeepOne(ctx, recordingID, onlyID, nil, DuplicateScreenRemovalActor)
	if !errors.Is(err, ErrNotHeldTwice) {
		t.Fatalf("err = %v, want ErrNotHeldTwice", err)
	}
	if !filePresent(t, pool, onlyID) {
		t.Fatal("the only copy was deleted")
	}
}

// A want is one recording somebody asked for. Before the acquisition loop goes
// looking it asks the library whether the recording is already there, through
// OwnedFileForRecording, and settles the want against the file that has it. The
// copies this press deletes must not take that answer with them: the survivor
// is the copy, and a want for this recording is satisfied by it.
func TestAWantForTheRecordingIsSatisfiedByTheSurvivingCopy(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)
	recordingID := uuid.New()

	// The surplus copy is the older row, so it is the one
	// OwnedFileForRecording would have answered with before the press.
	sparePath := filepath.Join(rootPath, "Singles", "roads.mp3")
	keeperPath := filepath.Join(rootPath, "Portishead", "Dummy", "roads.flac")
	spareID := libraryFile(t, pool, rootID, sparePath, "Portishead", "Roads")
	keeperID := libraryFile(t, pool, rootID, keeperPath, "Portishead", "Dummy")
	identifiedAsRecording(t, pool, spareID, recordingID)
	identifiedAsRecording(t, pool, keeperID, recordingID)

	if _, err := NewRemover(pool, zerolog.Nop()).KeepOne(ctx, recordingID, keeperID, []uuid.UUID{spareID}, DuplicateScreenRemovalActor); err != nil {
		t.Fatal(err)
	}

	owned, err := db.New(pool).OwnedFileForRecording(ctx, recordingID)
	if err != nil {
		t.Fatal(err)
	}
	if !owned.Valid || owned.UUID != keeperID {
		t.Fatalf("owned file = %v, want the surviving copy %v", owned, keeperID)
	}
}

// The tags Schall writes into a file describe the track it answers. The
// survivor is about to answer a track it did not answer a moment ago, so the
// write is asked for.
func TestKeepingOneCopyAsksForTheSurvivorsTagsToBeWritten(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)
	recordingID := uuid.New()

	keeperPath := filepath.Join(rootPath, "Portishead", "Dummy", "roads.flac")
	sparePath := filepath.Join(rootPath, "Singles", "roads.mp3")
	keeperID := libraryFile(t, pool, rootID, keeperPath, "Portishead", "Dummy")
	spareID := libraryFile(t, pool, rootID, sparePath, "Portishead", "Roads")
	identifiedAsRecording(t, pool, keeperID, recordingID)
	identifiedAsRecording(t, pool, spareID, recordingID)

	remover := NewRemover(pool, zerolog.Nop()).WithMatcher(&countingMatcher{})
	if _, err := remover.KeepOne(ctx, recordingID, keeperID, []uuid.UUID{spareID}, DuplicateScreenRemovalActor); err != nil {
		t.Fatal(err)
	}

	var queued int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM jobs
		WHERE kind = 'tag_file' AND payload->>'fileId' = $1::text AND status = 'queued'
	`, keeperID).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 1 {
		t.Fatalf("queued tag_file jobs = %d, want 1", queued)
	}
}

// The player is showing every copy this press just deleted, so keeping one
// copy queues the same job an import does rather than telling it directly.
func TestKeepingOneCopyTellsThePlayer(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)
	recordingID := uuid.New()

	keeperPath := filepath.Join(rootPath, "Portishead", "Dummy", "roads.flac")
	sparePath := filepath.Join(rootPath, "Singles", "roads.mp3")
	keeperID := libraryFile(t, pool, rootID, keeperPath, "Portishead", "Dummy")
	spareID := libraryFile(t, pool, rootID, sparePath, "Portishead", "Roads")
	identifiedAsRecording(t, pool, keeperID, recordingID)
	identifiedAsRecording(t, pool, spareID, recordingID)

	remover := NewRemover(pool, zerolog.Nop())
	if _, err := remover.KeepOne(ctx, recordingID, keeperID, []uuid.UUID{spareID}, DuplicateScreenRemovalActor); err != nil {
		t.Fatal(err)
	}

	var queued int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM jobs WHERE kind = 'notify_player' AND status IN ('queued', 'running')
	`).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 1 {
		t.Fatalf("queued notify_player jobs = %d, want 1", queued)
	}
}

// A want that stopped because its copy is one of the surplus files this press
// deletes is given a next look back, the same way Remove gives one back — the
// music that want was told about is not in the library any more.
func TestKeepingOneCopyLetsAWantHoldingTheDoomedFileLookAgain(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)
	recordingID := uuid.New()

	keeperPath := filepath.Join(rootPath, "Portishead", "Dummy", "roads.flac")
	sparePath := filepath.Join(rootPath, "Singles", "roads.mp3")
	keeperID := libraryFile(t, pool, rootID, keeperPath, "Portishead", "Dummy")
	spareID := libraryFile(t, pool, rootID, sparePath, "Portishead", "Roads")
	identifiedAsRecording(t, pool, keeperID, recordingID)
	identifiedAsRecording(t, pool, spareID, recordingID)
	targetID := wantStoppedOnFile(t, pool, spareID)

	if _, err := NewRemover(pool, zerolog.Nop()).KeepOne(ctx, recordingID, keeperID, []uuid.UUID{spareID}, DuplicateScreenRemovalActor); err != nil {
		t.Fatal(err)
	}

	var due bool
	var summary string
	if err := pool.QueryRow(ctx, `
		SELECT next_attempt_at IS NOT NULL, summary
		FROM acquisition_targets WHERE id = $1
	`, targetID).Scan(&due, &summary); err != nil {
		t.Fatal(err)
	}
	if !due {
		t.Fatal("the want has no next look, so nothing will ever look for it again")
	}
	if summary != db.RearmedByADeletionSummary {
		t.Errorf("summary = %q, want %q", summary, db.RearmedByADeletionSummary)
	}
}

// The row going and the unlink are two separate commits. If a library file now
// owns the path — a scan re-indexed it before the unlink ran — the unlink must
// refuse rather than delete audio out from under a row nobody decided to remove.
func TestRetryingAnUnlinkRefusesWhenALibraryFileNowOwnsThePath(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)
	recordingID := uuid.New()

	keeperPath := filepath.Join(rootPath, "Portishead", "Dummy", "roads.flac")
	stuckPath := filepath.Join(rootPath, "locked", "roads.mp3")
	keeperID := libraryFile(t, pool, rootID, keeperPath, "Portishead", "Dummy")
	stuckID := libraryFile(t, pool, rootID, stuckPath, "Portishead", "Roads")
	identifiedAsRecording(t, pool, keeperID, recordingID)
	identifiedAsRecording(t, pool, stuckID, recordingID)

	if err := os.Chmod(filepath.Dir(stuckPath), 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Dir(stuckPath), 0o755) })

	remover := NewRemover(pool, zerolog.Nop())
	if _, err := remover.KeepOne(ctx, recordingID, keeperID, []uuid.UUID{stuckID}, DuplicateScreenRemovalActor); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stuckPath); err != nil {
		t.Fatalf("the file was deleted from a folder that forbids it: %v", err)
	}

	// The mount is fixed, but a scan reached this path first and gave it a row
	// again — standing in for a scan that ran before this fix would have taught
	// it to skip a pending removal.
	if err := os.Chmod(filepath.Dir(stuckPath), 0o755); err != nil {
		t.Fatal(err)
	}
	newOwnerID := libraryFile(t, pool, rootID, stuckPath, "Portishead", "Roads")

	if deleted := remover.RetryUnlinks(ctx); deleted != 0 {
		t.Fatalf("deleted = %d, want 0: the retry deleted audio a live row owns", deleted)
	}
	if _, err := os.Stat(stuckPath); err != nil {
		t.Fatalf("audio a library file owns was deleted: %v", err)
	}
	if !filePresent(t, pool, newOwnerID) {
		t.Fatal("the row that now owns the path was deleted")
	}

	written := removals(t, pool)
	if len(written) != 1 || written[0].unlinkedAt.Valid || written[0].unlinkError == "" {
		t.Fatalf("removal after the refused retry = %+v", written)
	}
}

// A failure partway through the doomed files must not make the caller believe
// nothing happened. The first surplus copy really is deleted and committed
// before the second one is even looked at; the loop stopping on the second
// cannot take that back, so the result names it. A trigger stands in for
// another route deleting the keeper file mid-press — a real race the two
// separate transactions this loop uses cannot itself rule out.
func TestKeepingOneCopyReportsWhatWentWhenALaterFileFails(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)
	recordingID := uuid.New()

	keeperPath := filepath.Join(rootPath, "Portishead", "Dummy", "roads.flac")
	firstPath := filepath.Join(rootPath, "a-singles", "roads.mp3")
	secondPath := filepath.Join(rootPath, "b-rips", "roads.mp3")
	keeperID := libraryFile(t, pool, rootID, keeperPath, "Portishead", "Dummy")
	firstID := libraryFile(t, pool, rootID, firstPath, "Portishead", "Roads")
	secondID := libraryFile(t, pool, rootID, secondPath, "Portishead", "Roads")
	identifiedAsRecording(t, pool, keeperID, recordingID)
	identifiedAsRecording(t, pool, firstID, recordingID)
	identifiedAsRecording(t, pool, secondID, recordingID)

	// Once the first surplus copy's row is gone, the keeper's goes with it —
	// standing in for somebody deleting the keeper file through another route
	// while this press is still running.
	if _, err := pool.Exec(ctx, `
		CREATE OR REPLACE FUNCTION test_remove_keeper_after_first_delete()
		RETURNS trigger AS $BODY$
		BEGIN
			DELETE FROM library_files WHERE id = '`+keeperID.String()+`';
			RETURN OLD;
		END;
		$BODY$ LANGUAGE plpgsql
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		CREATE TRIGGER trg_remove_keeper_after_first_delete
		AFTER DELETE ON library_files
		FOR EACH ROW WHEN (OLD.id = '`+firstID.String()+`')
		EXECUTE FUNCTION test_remove_keeper_after_first_delete()
	`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DROP TRIGGER IF EXISTS trg_remove_keeper_after_first_delete ON library_files`)
		_, _ = pool.Exec(context.Background(), `DROP FUNCTION IF EXISTS test_remove_keeper_after_first_delete()`)
	})

	matcher := &countingMatcher{}
	kept, err := NewRemover(pool, zerolog.Nop()).WithMatcher(matcher).
		KeepOne(ctx, recordingID, keeperID, []uuid.UUID{firstID, secondID}, DuplicateScreenRemovalActor)
	if !errors.Is(err, ErrKeeperMissing) {
		t.Fatalf("err = %v, want ErrKeeperMissing", err)
	}

	// The first file really is gone, and the result says so rather than
	// reading as an empty, failed press.
	if len(kept.RemovedPaths) != 1 || kept.RemovedPaths[0] != firstPath {
		t.Fatalf("removed = %v, want [%s]", kept.RemovedPaths, firstPath)
	}
	if filePresent(t, pool, firstID) {
		t.Fatal("the first surplus copy still has a library row")
	}
	if _, err := os.Stat(firstPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the first surplus copy's audio is still on disc: %v", err)
	}

	// The loop stopped before the second file was ever touched.
	if !filePresent(t, pool, secondID) {
		t.Fatal("the second surplus copy was deleted after the loop had already failed")
	}
	if _, err := os.Stat(secondPath); err != nil {
		t.Fatalf("the second surplus copy's audio is gone: %v", err)
	}

	// What did go is still on record, and the survivor's matches are still
	// judged again for it.
	written := removals(t, pool)
	if len(written) != 1 || written[0].path != firstPath {
		t.Fatalf("removal rows = %+v, want one for %s", written, firstPath)
	}
	if len(matcher.reconciled) == 0 {
		t.Fatal("nothing was judged again after the file that did go")
	}
}

// The doomed copy can itself be a witness another file borrowed its identity
// from (ROADMAP 10b). Keeping one copy and deleting the rest must not silently
// sever that: withdrawing it here is the same rule Remove follows, because
// KeepOne deletes files exactly as Remove does, one at a time.
func TestKeepingOneCopyWithdrawsWhatTheDoomedFileAdmitted(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)
	recordingID := uuid.New()
	witnessRecordingID := uuid.New()

	keeperPath := filepath.Join(rootPath, "Portishead", "Dummy", "roads.flac")
	sparePath := filepath.Join(rootPath, "Singles", "roads.mp3")
	keeperID := libraryFile(t, pool, rootID, keeperPath, "Portishead", "Dummy")
	spareID := libraryFile(t, pool, rootID, sparePath, "Portishead", "Roads")
	identifiedAsRecording(t, pool, keeperID, recordingID)
	identifiedAsRecording(t, pool, spareID, recordingID)

	admittedID := libraryFile(t, pool, rootID,
		filepath.Join(rootPath, "Anetha", "Continuum", "candy.flac"), "Anetha", "Continuum")
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_file_identities (
		    library_file_id, kind, musicbrainz_recording_id, method, confidence,
		    is_manual, summary, admitted_by_file_id, admitted_by_proof
		) VALUES ($1, 'external', $2, 'library-witness', 1, false,
		          'Proven by a file you already own.', $3, 'manual')
	`, admittedID, witnessRecordingID, spareID); err != nil {
		t.Fatal(err)
	}

	if _, err := NewRemover(pool, zerolog.Nop()).
		KeepOne(ctx, recordingID, keeperID, []uuid.UUID{spareID}, DuplicateScreenRemovalActor); err != nil {
		t.Fatal(err)
	}

	var exists bool
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM library_file_identities WHERE library_file_id = $1)`,
		admittedID,
	).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Error("the file admitted by the deleted witness still has an identity, want it withdrawn")
	}
	var status string
	if err := pool.QueryRow(ctx,
		`SELECT resolution_status FROM library_files WHERE id = $1`, admittedID,
	).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "needs_review" {
		t.Errorf("status = %q, want the admitted file to be a question again", status)
	}
}
