package library

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/dbtest"
	"github.com/rs/zerolog"
)

// countingMatcher records which files were judged again, and stands in for the
// peers a removal disturbs.
type countingMatcher struct {
	peers      []uuid.UUID
	reconciled []uuid.UUID
	err        error
}

func (matcher *countingMatcher) Peers(context.Context, uuid.UUID) ([]uuid.UUID, error) {
	return matcher.peers, matcher.err
}

func (matcher *countingMatcher) ReconcileFile(_ context.Context, fileID uuid.UUID) error {
	matcher.reconciled = append(matcher.reconciled, fileID)
	return nil
}

// A row alone would leave music the library has forgotten sitting on the disc,
// which the next scan adds back as a new file: the duplicate somebody just
// resolved would return under a new id.
func TestRemovingAFileLeavesNoAudioBehind(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)

	path := filepath.Join(rootPath, "Portishead", "Dummy", "roads.mp3")
	fileID := libraryFile(t, pool, rootID, path, "Portishead", "Dummy")

	removed, err := NewRemover(pool, zerolog.Nop()).Remove(ctx, fileID)
	if err != nil {
		t.Fatal(err)
	}
	if removed != path {
		t.Fatalf("removed = %s, want %s", removed, path)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the audio is still there: %v", err)
	}
}

// A want that stopped is one whose copy is on the disc and whose file the
// library calls a different recording: it stays wanted, and it has no time to be
// looked at again, so nothing reaches it on its own. Deleting that file — which
// is what somebody does with a surplus copy — takes its music away, so the want
// has something to look for and is given a next look here.
//
// Without this it would sit pending forever: the copy's link to the file is set
// to nothing by the delete, so afterwards nothing could even say which want to
// wake.
func TestRemovingAFileLetsTheWantThatStoppedOnItLookAgain(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)
	path := filepath.Join(rootPath, "Portishead", "Dummy", "glory box.mp3")
	fileID := libraryFile(t, pool, rootID, path, "Portishead", "Dummy")
	targetID := wantStoppedOnFile(t, pool, fileID)

	if _, err := NewRemover(pool, zerolog.Nop()).Remove(ctx, fileID); err != nil {
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

// wantStoppedOnFile records a want that has this file as its accepted copy and
// no time to be looked at again, which is where a disagreement leaves one.
func wantStoppedOnFile(t *testing.T, pool *pgxpool.Pool, fileID uuid.UUID) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	targetID, requestID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO acquisition_targets (
			id, origin, entry_artist, entry_title, musicbrainz_recording_id,
			status, resolution_method, resolved_at, next_attempt_at, attempts, summary
		)
		VALUES ($1, 'manual', 'Portishead', 'Glory Box', $2, 'pending', 'recording_id',
		        now(), NULL, 1, 'your library calls that file a different recording')
	`, targetID, uuid.New()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO download_requests (
			id, acquisition_target_id, provider, source_username, source_directory,
			file_count, expected_track_count, total_size_bytes, format, score, files,
			status, import_status, import_path, imported_at
		)
		VALUES ($1, $2, 'slskd', 'peer', 'Music/Portishead', 1, 1, 25000000,
		        'flac', 0.8, '[]'::jsonb, 'completed', 'imported', '/music', now())
	`, requestID, targetID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO acquisition_target_files (
			acquisition_target_id, provider, source_username, remote_path, file_name,
			verdict, summary, download_request_id, library_file_id
		)
		VALUES ($1, 'slskd', 'peer', 'Music\Portishead\05.flac', '05.flac',
		        'accepted', 'Proven by its audio.', $2, $3)
	`, targetID, requestID, fileID); err != nil {
		t.Fatal(err)
	}
	return targetID
}

// What was decided about a file goes with the file. A mapping left pointing at
// a row that is gone is not a decision anybody can act on.
func TestRemovingAFileTakesWhatHangsOffItsRow(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)

	path := filepath.Join(rootPath, "Portishead", "Dummy", "roads.mp3")
	fileID := libraryFile(t, pool, rootID, path, "Portishead", "Dummy")
	trackID := catalogueTrack(t, pool, "Portishead", "Dummy")
	if _, err := pool.Exec(ctx, `
		INSERT INTO track_mappings (track_id, library_file_id, method, is_manual)
		VALUES ($1, $2, 'manual', true)
	`, trackID, fileID); err != nil {
		t.Fatal(err)
	}

	if _, err := NewRemover(pool, zerolog.Nop()).Remove(ctx, fileID); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := pool.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM library_files WHERE id = $1)
		     + (SELECT count(*) FROM track_mappings WHERE library_file_id = $1)
	`, fileID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("rows left behind = %d, want 0", rows)
	}
}

// The copy that stays was told a track was taken and a copy was already in
// place. Neither is true once the other file is gone, so it is judged again.
func TestRemovingAFileHasTheCopyThatStaysJudgedAgain(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)

	folder := filepath.Join(rootPath, "Portishead", "Dummy")
	fileID := libraryFile(t, pool, rootID, filepath.Join(folder, "roads.mp3"), "Portishead", "Dummy")
	keptPath := filepath.Join(folder, "roads (1).mp3")
	keptID := libraryFile(t, pool, rootID, keptPath, "Portishead", "Dummy")
	matcher := &countingMatcher{peers: []uuid.UUID{keptID}}

	if _, err := NewRemover(pool, zerolog.Nop()).WithMatcher(matcher).Remove(ctx, fileID); err != nil {
		t.Fatal(err)
	}
	if len(matcher.reconciled) != 1 || matcher.reconciled[0] != keptID {
		t.Fatalf("reconciled = %v, want one %s", matcher.reconciled, keptID)
	}
	// It shares the folder, so nothing may be tidied away under it either.
	if _, err := os.Stat(keptPath); err != nil {
		t.Fatalf("the copy that stays was disturbed: %v", err)
	}
}

// The folder a deleted file leaves behind goes with it when it is empty, and
// only then. Otherwise a library that started out one file per folder would keep
// every folder it ever had.
func TestRemovingTheLastFileInAFolderDiscardsIt(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)

	folder := filepath.Join(rootPath, "Portishead", "Dummy")
	fileID := libraryFile(t, pool, rootID, filepath.Join(folder, "roads.mp3"), "Portishead", "Dummy")

	if _, err := NewRemover(pool, zerolog.Nop()).Remove(ctx, fileID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(folder); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the emptied folder is still there: %v", err)
	}
	// Up to the root and no further: the root itself is the library, not a
	// folder a file happened to leave behind.
	if _, err := os.Stat(rootPath); err != nil {
		t.Fatalf("the root was removed: %v", err)
	}
}

// A folder holding anything at all survives — artwork, a cue sheet, a file
// Schall never knew about. The emptiness is the permission and it is never
// assumed.
func TestRemovingAFileLeavesAFolderThatStillHoldsSomething(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)

	folder := filepath.Join(rootPath, "Portishead", "Dummy")
	fileID := libraryFile(t, pool, rootID, filepath.Join(folder, "roads.mp3"), "Portishead", "Dummy")
	cover := filepath.Join(folder, "cover.jpg")
	if err := os.WriteFile(cover, []byte("a picture"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := NewRemover(pool, zerolog.Nop()).Remove(ctx, fileID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cover); err != nil {
		t.Fatalf("the artwork went with it: %v", err)
	}
}

// Asked to delete something that is not there, the answer is that it is not
// there — not a success that quietly did nothing.
func TestRemovingAFileTheLibraryDoesNotHold(t *testing.T) {
	pool := dbtest.Setup(t)
	_, err := NewRemover(pool, zerolog.Nop()).Remove(context.Background(), uuid.New())
	if !errors.Is(err, ErrFileGone) {
		t.Fatalf("err = %v, want %v", err, ErrFileGone)
	}
}

// A file already gone from the disc still has its row taken out. The caller
// asked for the file not to be there, and it is not.
func TestRemovingAFileWhoseAudioIsAlreadyGone(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)

	path := filepath.Join(rootPath, "Portishead", "Dummy", "roads.mp3")
	fileID := libraryFile(t, pool, rootID, path, "Portishead", "Dummy")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}

	if _, err := NewRemover(pool, zerolog.Nop()).Remove(ctx, fileID); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM library_files WHERE id = $1`, fileID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("library_files rows = %d, want 0", rows)
	}
}

// A file can be a witness other files borrowed their identity from (ROADMAP
// 10b): a copy that agreed with its audio was admitted on the strength of
// whatever proved this one. Deleting the file must withdraw what it admitted —
// otherwise a mistaken decision could never be corrected once the file that
// made it is gone, because there would be nothing left to correct through.
func TestRemovingAFileWithdrawsWhatItAdmitted(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)

	decidedPath := filepath.Join(rootPath, "Anetha", "Continuum", "candy.flac")
	decidedID := libraryFile(t, pool, rootID, decidedPath, "Anetha", "Continuum")
	admittedID := libraryFile(t, pool, rootID,
		filepath.Join(rootPath, "Anetha", "Continuum", "candy (2).flac"), "Anetha", "Continuum")
	recordingID := uuid.New()

	if _, err := pool.Exec(ctx, `
		INSERT INTO library_file_identities (
		    library_file_id, kind, musicbrainz_recording_id, method, confidence,
		    is_manual, summary
		) VALUES ($1, 'external', $2, 'manual', 1, true, 'Resolved by hand.')
	`, decidedID, recordingID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_file_identities (
		    library_file_id, kind, musicbrainz_recording_id, method, confidence,
		    is_manual, summary, admitted_by_file_id, admitted_by_proof
		) VALUES ($1, 'external', $2, 'library-witness', 1, false,
		          'Proven by a file you already own.', $3, 'manual')
	`, admittedID, recordingID, decidedID); err != nil {
		t.Fatal(err)
	}
	targetID := wantStoppedOnFile(t, pool, admittedID)

	if _, err := NewRemover(pool, zerolog.Nop()).Remove(ctx, decidedID); err != nil {
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

	var due bool
	var summary string
	if err := pool.QueryRow(ctx, `
		SELECT next_attempt_at IS NOT NULL, summary
		FROM acquisition_targets WHERE id = $1
	`, targetID).Scan(&due, &summary); err != nil {
		t.Fatal(err)
	}
	if !due {
		t.Fatal("the want holding the withdrawn file has no next look, want it rearmed")
	}
	if summary != db.RearmedByADeletionSummary {
		t.Errorf("summary = %q, want %q", summary, db.RearmedByADeletionSummary)
	}
}
