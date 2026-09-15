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

// libraryFile lays a file down inside a root and records it the way a scan
// would, so a move has both a row and something to rename.
func libraryFile(t *testing.T, pool *pgxpool.Pool, rootID uuid.UUID, path, artist, album string) uuid.UUID {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("broken fixture"), 0o644); err != nil {
		t.Fatal(err)
	}
	var fileID uuid.UUID
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO library_files (
			library_root_id, path, size_bytes, modified_at, artist_tag, album_tag, title_tag
		)
		VALUES ($1, $2, 14, now(), nullif($3, ''), nullif($4, ''), 'A song')
		RETURNING id
	`, rootID, path, artist, album).Scan(&fileID); err != nil {
		t.Fatal(err)
	}
	return fileID
}

func libraryRoot(t *testing.T, pool *pgxpool.Pool) (uuid.UUID, string) {
	t.Helper()
	rootPath := t.TempDir()
	rootID := uuid.New()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO library_roots (id, path) VALUES ($1, $2)`, rootID, rootPath); err != nil {
		t.Fatal(err)
	}
	return rootID, rootPath
}

// catalogueTrack builds the smallest catalogue a manual mapping needs: an
// artist, a release and one track on it.
func catalogueTrack(t *testing.T, pool *pgxpool.Pool, artist, album string) uuid.UUID {
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
	`, albumID, uuid.New()).Scan(&trackID); err != nil {
		t.Fatal(err)
	}
	return trackID
}

func setLayout(t *testing.T, pool *pgxpool.Pool, template string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO library_layout_settings (template) VALUES ($1)`, template); err != nil {
		t.Fatal(err)
	}
}

// TestLayoutMoveCarriesEverythingAttached is the whole promise of 0014: the row
// is the same row afterwards, so the decisions somebody made about the file are
// still attached to it, and the provenance still points at where the file is.
func TestLayoutMoveCarriesEverythingAttached(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)
	setLayout(t, pool, "{artist}/{album}")

	from := filepath.Join(rootPath, "loose", "roads.mp3")
	fileID := libraryFile(t, pool, rootID, from, "Portishead", "Dummy")
	want := filepath.Join(rootPath, "Portishead", "Dummy", "roads.mp3")

	// A decision about what this file is, made by hand.
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_file_identities (
			library_file_id, kind, musicbrainz_recording_id, method, is_manual, summary
		)
		VALUES ($1, 'external', $2, 'manual', true, 'decided by hand')
	`, fileID, uuid.New()); err != nil {
		t.Fatal(err)
	}
	// A track somebody matched it to by hand.
	trackID := catalogueTrack(t, pool, "Portishead", "Dummy")
	if _, err := pool.Exec(ctx, `
		INSERT INTO track_mappings (track_id, library_file_id, method, is_manual)
		VALUES ($1, $2, 'manual', true)
	`, trackID, fileID); err != nil {
		t.Fatal(err)
	}
	// And the download that brought it in, pointing at where it landed.
	requestID := downloadProvenance(t, pool, fileID, from, filepath.Dir(from), "Portishead", "Dummy")

	mover := NewMover(pool, zerolog.Nop())
	plan, err := mover.Plan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Planned != 1 {
		t.Fatalf("plan = %#v", plan)
	}
	// Nothing has been touched yet: a plan is readable before it is agreed to.
	if _, err := os.Stat(from); err != nil {
		t.Fatalf("planning moved the file: %v", err)
	}

	more, err := mover.Apply(ctx, plan.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if more {
		t.Fatal("Apply() reported more work after a one-file run")
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("the file is not at its destination: %v", err)
	}
	if _, err := os.Stat(from); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the file is still where it was: %v", err)
	}

	var (
		path        string
		manualID    bool
		manualMatch bool
		localPath   string
		importPath  string
		status      string
	)
	if err := pool.QueryRow(ctx, `
		SELECT files.path,
		       (SELECT is_manual FROM library_file_identities WHERE library_file_id = files.id),
		       (SELECT is_manual FROM track_mappings WHERE library_file_id = files.id),
		       (SELECT local_path FROM downloads WHERE library_file_id = files.id),
		       (SELECT import_path FROM download_requests WHERE id = $2),
		       (SELECT status FROM library_file_moves WHERE library_file_id = files.id)
		FROM library_files files
		WHERE files.id = $1
	`, fileID, requestID).Scan(&path, &manualID, &manualMatch, &localPath, &importPath, &status); err != nil {
		t.Fatal(err)
	}
	if path != want || localPath != want {
		t.Errorf("after the move: path %q, local_path %q, want %q", path, localPath, want)
	}
	if !manualID || !manualMatch {
		t.Errorf("manual decisions did not survive: identity %v, mapping %v", manualID, manualMatch)
	}
	if importPath != filepath.Dir(want) {
		t.Errorf("import_path = %q, want %q", importPath, filepath.Dir(want))
	}
	if status != "moved" {
		t.Errorf("journal status = %q, want moved", status)
	}
}

// downloadProvenance records an import that put this file where it is, and
// hands back the request it belonged to.
func downloadProvenance(
	t *testing.T, pool *pgxpool.Pool, fileID uuid.UUID, localPath, importPath, artist, album string,
) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	var artistID, albumID, requestID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO artists (musicbrainz_id, name, sort_name)
		VALUES ($1, $2, $2) RETURNING id
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
		INSERT INTO download_requests (
			album_id, provider, source_username, source_directory, file_count,
			expected_track_count, total_size_bytes, format, score, status,
			import_status, import_path, imported_at
		)
		VALUES ($1, 'slskd', 'peer', 'share', 1, 1, 14, 'mp3', 0.9, 'requested',
		        'imported', $2, now())
		RETURNING id
	`, albumID, importPath).Scan(&requestID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO downloads (
			album_id, download_request_id, remote_path, local_path, library_file_id, status
		)
		VALUES ($1, $2, 'peer\share\roads.mp3', $3, $4, 'completed')
	`, albumID, requestID, localPath, fileID); err != nil {
		t.Fatal(err)
	}
	return requestID
}

// TestTheImportFolderFollowsOnlyWhenTheLastFileLeaves pins the one path that is
// a folder rather than a file. A release half of whose files have moved is not
// somewhere else, it is scattered, and naming either half would be a claim
// about the release that is not true.
// A folder a migration empties goes with the files. Leaving it standing left the
// whole shape of the old layout on disk beside the new one — after one run the
// library held every folder it used to have plus every folder it now had, and
// nothing said which was which.
func TestAnEmptiedFolderIsTakenWithTheFile(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)
	setLayout(t, pool, "{artist}/{album}")

	folder := filepath.Join(rootPath, "Slowdive", "Souvlaki [deadbeef]")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	from := filepath.Join(folder, "01 alison.mp3")
	if err := os.WriteFile(from, []byte("music"), 0o644); err != nil {
		t.Fatal(err)
	}
	fileID := libraryFile(t, pool, rootID, from, "Slowdive", "Souvlaki")
	downloadProvenance(t, pool, fileID, from, folder, "Slowdive", "Souvlaki")

	mover := NewMover(pool, zerolog.Nop())
	plan, err := mover.Plan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mover.Apply(ctx, plan.RunID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(folder); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the emptied folder is still there: %v", err)
	}
	// The artist folder is where the file went, so it stays.
	if _, err := os.Stat(filepath.Join(rootPath, "Slowdive", "Souvlaki", "01 alison.mp3")); err != nil {
		t.Fatalf("the file is not at its destination: %v", err)
	}
}

// Emptiness is the permission. A folder still holding anything Schall does not
// know about — a cue sheet, a scan of the sleeve, a file nobody scanned — is
// left alone with it.
//
// The file used here is a cue sheet rather than the cover it used to be.
// Schall writes covers itself now and takes them along with the audio, so a
// cover is no longer an example of something it does not know about.
func TestAFolderHoldingSomethingElseIsLeftAlone(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)
	setLayout(t, pool, "{artist}/{album}")

	folder := filepath.Join(rootPath, "Slowdive", "Souvlaki [deadbeef]")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	from := filepath.Join(folder, "01 alison.mp3")
	if err := os.WriteFile(from, []byte("music"), 0o644); err != nil {
		t.Fatal(err)
	}
	sheet := filepath.Join(folder, "Souvlaki.cue")
	if err := os.WriteFile(sheet, []byte("REM nothing Schall wrote"), 0o644); err != nil {
		t.Fatal(err)
	}
	fileID := libraryFile(t, pool, rootID, from, "Slowdive", "Souvlaki")
	downloadProvenance(t, pool, fileID, from, folder, "Slowdive", "Souvlaki")

	mover := NewMover(pool, zerolog.Nop())
	plan, err := mover.Plan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mover.Apply(ctx, plan.RunID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sheet); err != nil {
		t.Fatalf("the cue sheet went with the folder: %v", err)
	}
}

func TestTheImportFolderFollowsOnlyWhenTheLastFileLeaves(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)
	setLayout(t, pool, "{artist}/{album}")

	// Two files of one import, in one folder, going to one new folder.
	folder := filepath.Join(rootPath, "inbox copy")
	first := filepath.Join(folder, "01 one.mp3")
	second := filepath.Join(folder, "02 two.mp3")
	firstID := libraryFile(t, pool, rootID, first, "Slowdive", "Souvlaki")
	secondID := libraryFile(t, pool, rootID, second, "Slowdive", "Souvlaki")
	requestID := downloadProvenance(t, pool, firstID, first, folder, "Slowdive", "Souvlaki")
	addDownload(t, pool, requestID, secondID, second)

	mover := NewMover(pool, zerolog.Nop())
	plan, err := mover.Plan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Planned != 2 {
		t.Fatalf("plan = %#v", plan)
	}

	// One file out of two: the folder still holds the other one, so it is still
	// where the import put it.
	if _, err := mover.applyNext(ctx, plan.RunID); err != nil {
		t.Fatal(err)
	}
	if got := importFolder(t, pool, requestID); got != folder {
		t.Fatalf("import_path after one of two moves = %q, want %q", got, folder)
	}

	if _, err := mover.applyNext(ctx, plan.RunID); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(rootPath, "Slowdive", "Souvlaki")
	if got := importFolder(t, pool, requestID); got != want {
		t.Fatalf("import_path after the last move = %q, want %q", got, want)
	}
}

func importFolder(t *testing.T, pool *pgxpool.Pool, requestID uuid.UUID) string {
	t.Helper()
	var path string
	if err := pool.QueryRow(context.Background(),
		`SELECT import_path FROM download_requests WHERE id = $1`, requestID).Scan(&path); err != nil {
		t.Fatal(err)
	}
	return path
}

// addDownload records a second file of the same import.
func addDownload(t *testing.T, pool *pgxpool.Pool, requestID, fileID uuid.UUID, localPath string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO downloads (album_id, download_request_id, remote_path, local_path, library_file_id, status)
		SELECT album_id, id, 'peer\share\' || $3, $2, $4, 'completed'
		FROM download_requests WHERE id = $1
	`, requestID, localPath, filepath.Base(localPath), fileID); err != nil {
		t.Fatal(err)
	}
}

// TestLayoutPlanRefusesACollision pins the refusal: two files that render to
// one path do not overwrite each other, and the second is told why before
// anything is touched.
func TestLayoutPlanRefusesACollision(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)
	setLayout(t, pool, "{artist}/{album}")

	first := filepath.Join(rootPath, "one", "song.mp3")
	second := filepath.Join(rootPath, "two", "song.mp3")
	libraryFile(t, pool, rootID, first, "Boards of Canada", "Geogaddi")
	libraryFile(t, pool, rootID, second, "Boards of Canada", "Geogaddi")

	mover := NewMover(pool, zerolog.Nop())
	plan, err := mover.Plan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Planned != 1 || plan.Colliding != 1 {
		t.Fatalf("plan = %#v", plan)
	}
	if _, err := mover.Apply(ctx, plan.RunID); err != nil {
		t.Fatal(err)
	}

	summary, err := mover.Run(ctx, plan.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Moved != 1 || summary.Failed != 1 || summary.Planned != 0 {
		t.Fatalf("run = %#v", summary)
	}
	var reason string
	if err := pool.QueryRow(ctx, `
		SELECT error FROM library_file_moves WHERE run_id = $1 AND status = 'failed'
	`, plan.RunID).Scan(&reason); err != nil {
		t.Fatal(err)
	}
	if reason == "" {
		t.Error("a refused move records no reason")
	}
	// Both files are still on the disc: the loser of a collision keeps its
	// bytes, because nothing about a layout decides which copy the library
	// keeps.
	survivors := 0
	for _, path := range []string{first, second} {
		if _, err := os.Stat(path); err == nil {
			survivors++
		}
	}
	if _, err := os.Stat(filepath.Join(rootPath, "Boards of Canada", "Geogaddi", "song.mp3")); err != nil {
		t.Fatalf("the first file did not move: %v", err)
	}
	if survivors != 1 {
		t.Errorf("%d of the two original files are still there, want 1", survivors)
	}
}

// TestLayoutMoveRefusesAnOccupiedDestination covers the file the plan could not
// know about: something arrived at the destination between planning and moving.
func TestLayoutMoveRefusesAnOccupiedDestination(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)
	setLayout(t, pool, "{artist}/{album}")

	from := filepath.Join(rootPath, "loose", "song.mp3")
	fileID := libraryFile(t, pool, rootID, from, "Aphex Twin", "Drukqs")
	destination := filepath.Join(rootPath, "Aphex Twin", "Drukqs", "song.mp3")

	mover := NewMover(pool, zerolog.Nop())
	plan, err := mover.Plan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, []byte("somebody else's copy"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := mover.Apply(ctx, plan.RunID); err != nil {
		t.Fatal(err)
	}
	var status, reason, path string
	if err := pool.QueryRow(ctx, `
		SELECT moves.status, coalesce(moves.error, ''), files.path
		FROM library_file_moves moves
		JOIN library_files files ON files.id = moves.library_file_id
		WHERE moves.run_id = $1
	`, plan.RunID).Scan(&status, &reason, &path); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || reason == "" {
		t.Errorf("occupied destination = %q (%q), want a refusal with a reason", status, reason)
	}
	if path != from {
		t.Errorf("the row moved anyway: %q", path)
	}
	if content, err := os.ReadFile(destination); err != nil || string(content) != "somebody else's copy" {
		t.Errorf("the file that was already there = %q (%v)", content, err)
	}
	_ = fileID
}

// TestLayoutPlanLeavesWhatSchallDidNotFile pins the one thing planning refuses
// to invent: a file Schall never filed has no identifier, and the template asks
// for one.
func TestLayoutPlanLeavesWhatSchallDidNotFile(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)

	libraryFile(t, pool, rootID, filepath.Join(rootPath, "loose", "song.mp3"), "Autechre", "Tri Repetae")

	mover := NewMover(pool, zerolog.Nop())
	plan, err := mover.Plan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Planned != 0 || plan.Unfiled != 1 {
		t.Fatalf("plan under the default template = %#v", plan)
	}
	if _, err := mover.Run(ctx, plan.RunID); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("Run() of an empty plan = %v, want ErrRunNotFound", err)
	}

	// Dropping {id} is what migrates a collection Schall never filed.
	setLayout(t, pool, "{artist}/{album}")
	plan, err = mover.Plan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Planned != 1 || plan.Unfiled != 0 {
		t.Fatalf("plan without {id} = %#v", plan)
	}
}

// TestLayoutPlanMovesNothingUnderAnUntouchedTemplate is the other half of that:
// a file Schall filed is already where the default template puts it, so the
// first run against a template nobody edited has nothing to do.
func TestLayoutPlanMovesNothingUnderAnUntouchedTemplate(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)

	// Filed the way the importers file: artist, then release and the short form
	// of the release it is. The folder cannot be built until the release exists,
	// so the import is recorded first and the file moved under its name after.
	requestID := uuid.New()
	releaseGroup := importedBy(t, pool, requestID, uuid.Nil, "", "")
	folder := filepath.Join(rootPath, "Imported artist",
		"Imported release ["+releaseGroup.String()[:8]+"]")
	path := filepath.Join(folder, "roads.mp3")
	fileID := libraryFile(t, pool, rootID, path, "imported ARTIST", "imported RELEASE")
	attachImport(t, pool, requestID, fileID, path, folder)

	mover := NewMover(pool, zerolog.Nop())
	plan, err := mover.Plan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Planned != 0 || plan.InPlace != 1 {
		t.Fatalf("plan = %#v", plan)
	}
}

// importedBy records the catalogue release and the request that fetched it, and
// answers the release group — which is the identifier the folder is named after,
// so a test cannot build the path it expects until it has this.
func importedBy(
	t *testing.T, pool *pgxpool.Pool, requestID, fileID uuid.UUID, localPath, importPath string,
) uuid.UUID {
	t.Helper()
	releaseGroup := uuid.New()
	ctx := context.Background()
	var artistID, albumID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO artists (musicbrainz_id, name, sort_name)
		VALUES ($1, 'Imported artist', 'Imported artist') RETURNING id
	`, uuid.New()).Scan(&artistID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO albums (artist_id, musicbrainz_release_group_id, title)
		VALUES ($1, $2, 'Imported release') RETURNING id
	`, artistID, releaseGroup).Scan(&albumID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO download_requests (
			id, album_id, provider, source_username, source_directory, file_count,
			expected_track_count, total_size_bytes, format, score, status,
			import_status, import_path, imported_at
		)
		VALUES ($1, $2, 'slskd', 'peer', 'share', 1, 1, 14, 'mp3', 0.9, 'requested',
		        'imported', $3, now())
	`, requestID, albumID, importPath); err != nil {
		t.Fatal(err)
	}
	if fileID != uuid.Nil {
		attachImport(t, pool, requestID, fileID, localPath, importPath)
	}
	return releaseGroup
}

// attachImport records which library file one request produced. It is apart
// from importedBy because the folder is named after the release, so a test has
// to know the release before it can put a file in the right place.
func attachImport(
	t *testing.T, pool *pgxpool.Pool, requestID, fileID uuid.UUID, localPath, importPath string,
) {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		UPDATE download_requests SET import_path = $2 WHERE id = $1
	`, requestID, importPath); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO downloads (
			album_id, download_request_id, remote_path, local_path, library_file_id, status
		)
		SELECT album_id, id, 'peer\share\roads.mp3', $2, $3, 'completed'
		FROM download_requests WHERE id = $1
	`, requestID, localPath, fileID); err != nil {
		t.Fatal(err)
	}
}

// TestAFileFetchedForAWantKeepsItsIdentifier covers the import that names no
// release: a copy fetched for a want answers a recording, so its request has no
// album on it. The identifier is the release the want resolved through, and
// losing it with the name that was never there would leave the file unplanned
// under the template that asks for it.
func TestAFileFetchedForAWantKeepsItsIdentifier(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)
	releaseGroup := uuid.New()

	path := filepath.Join(rootPath, "Talk Talk", "Ascension Day [deadbeef]", "song.mp3")
	fileID := libraryFile(t, pool, rootID, path, "Talk Talk", "Laughing Stock")
	wantProvenance(t, pool, fileID, path, "Ascension Day", releaseGroup)

	mover := NewMover(pool, zerolog.Nop())
	plan, err := mover.Plan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Planned != 1 || plan.Unfiled != 0 {
		t.Fatalf("plan = %#v, want the file planned rather than left unfiled", plan)
	}
	var to string
	if err := pool.QueryRow(ctx,
		`SELECT to_path FROM library_file_moves WHERE run_id = $1`, plan.RunID).Scan(&to); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(rootPath, "Talk Talk",
		"Laughing Stock ["+releaseGroup.String()[:8]+"]", "song.mp3")
	if to != want {
		t.Fatalf("destination = %q, want %q", to, want)
	}
}

// wantProvenance records the import of one file fetched for a wanted recording:
// a request against an acquisition target, naming no catalogue release.

// nullableUUID writes a zero uuid as SQL NULL, because a want that resolved
// through no release has none rather than one made of zeroes.
func nullableUUID(value uuid.UUID) any {
	if value == uuid.Nil {
		return nil
	}
	return value
}
func wantProvenance(
	t *testing.T, pool *pgxpool.Pool, fileID uuid.UUID, localPath, entryTitle string,
	releaseGroup uuid.UUID,
) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	var targetID, requestID uuid.UUID
	// The release the want resolved through is what its copy is filed under, so
	// two wants off one album render one folder however far apart they arrived.
	if err := pool.QueryRow(ctx, `
		INSERT INTO acquisition_targets (
			origin, entry_artist, entry_title, musicbrainz_release_group_id
		)
		VALUES ('manual', 'Talk Talk', $1, $2) RETURNING id
	`, entryTitle, nullableUUID(releaseGroup)).Scan(&targetID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO download_requests (
			acquisition_target_id, provider, source_username, source_directory, file_count,
			expected_track_count, total_size_bytes, format, score, status,
			import_status, import_path, imported_at
		)
		VALUES ($1, 'slskd', 'peer', 'share', 1, 1, 14, 'mp3', 0.9, 'requested',
		        'imported', $2, now())
		RETURNING id
	`, targetID, filepath.Dir(localPath)).Scan(&requestID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO downloads (
			track_id, download_request_id, remote_path, local_path, library_file_id, status
		)
		SELECT NULL, $1, 'peer\share\' || $4, $2, $3, 'completed'
	`, requestID, localPath, fileID, filepath.Base(localPath)); err != nil {
		t.Fatal(err)
	}
	// What the copy was proven to be. A want names no catalogue release, so this
	// is the only thing that says which release its file belongs to — and it is
	// what the importer filed it under.
	if releaseGroup != uuid.Nil {
		if _, err := pool.Exec(ctx, `
			INSERT INTO library_file_identities (
				library_file_id, kind, musicbrainz_recording_id,
				musicbrainz_release_group_id, artist_name, release_title, method
			)
			VALUES ($1, 'external', $2, $3, 'Talk Talk', 'Laughing Stock', 'fingerprint')
		`, fileID, uuid.New(), releaseGroup); err != nil {
			t.Fatal(err)
		}
	}
	return requestID
}

// Two recordings of one album, wanted separately and fetched a week apart, are
// one release and belong in one folder. {id} names the release, so the default
// template files them together rather than keeping the folder-per-download each
// of them arrived in.
func TestRecordingsFetchedSeparatelyAreFiledUnderOneRelease(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)
	releaseGroup := uuid.New()

	firstPath := filepath.Join(rootPath, "Talk Talk", "Ascension Day [deadbeef]", "one.mp3")
	secondPath := filepath.Join(rootPath, "Talk Talk", "After the Flood [feedface]", "two.mp3")
	firstID := libraryFile(t, pool, rootID, firstPath, "Talk Talk", "Laughing Stock")
	secondID := libraryFile(t, pool, rootID, secondPath, "Talk Talk", "Laughing Stock")
	wantProvenance(t, pool, firstID, firstPath, "Ascension Day", releaseGroup)
	wantProvenance(t, pool, secondID, secondPath, "After the Flood", releaseGroup)

	mover := NewMover(pool, zerolog.Nop())
	plan, err := mover.Plan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Planned != 2 || plan.Colliding != 0 {
		t.Fatalf("plan = %#v, want both files planned", plan)
	}
	if folders := destinationFolders(t, pool, plan.RunID); folders != 1 {
		t.Fatalf("the destinations span %d folders, want one release in one folder", folders)
	}
}

// Two different releases keep a folder each, which is the whole reason {id} is
// in the default template: it is what tells two albums sharing an artist and a
// title apart, and it must not have become a name every release shares.
func TestDifferentReleasesKeepAFolderEach(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)

	firstPath := filepath.Join(rootPath, "Talk Talk", "Laughing Stock [deadbeef]", "one.mp3")
	secondPath := filepath.Join(rootPath, "Talk Talk", "Laughing Stock [feedface]", "two.mp3")
	firstID := libraryFile(t, pool, rootID, firstPath, "Talk Talk", "Laughing Stock")
	secondID := libraryFile(t, pool, rootID, secondPath, "Talk Talk", "Laughing Stock")
	wantProvenance(t, pool, firstID, firstPath, "Ascension Day", uuid.New())
	wantProvenance(t, pool, secondID, secondPath, "After the Flood", uuid.New())

	mover := NewMover(pool, zerolog.Nop())
	plan, err := mover.Plan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if folders := destinationFolders(t, pool, plan.RunID); folders != 2 {
		t.Fatalf("the destinations span %d folders, want a folder each", folders)
	}
}

// destinationFolders is how many folders a run's destinations land in.
func destinationFolders(t *testing.T, pool *pgxpool.Pool, runID uuid.UUID) int {
	t.Helper()
	var folders int
	if err := pool.QueryRow(context.Background(), `
		SELECT count(DISTINCT regexp_replace(to_path, '/[^/]+$', ''))
		FROM library_file_moves WHERE run_id = $1
	`, runID).Scan(&folders); err != nil {
		t.Fatal(err)
	}
	return folders
}

// TestLayoutPlanRefusesASecondOpenRun pins the claim a plan makes on the files
// in it: one open plan at a time, in the shape the schema already enforces.
func TestLayoutPlanRefusesASecondOpenRun(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)
	setLayout(t, pool, "{artist}/{album}")
	libraryFile(t, pool, rootID, filepath.Join(rootPath, "loose", "song.mp3"), "Burial", "Untrue")

	mover := NewMover(pool, zerolog.Nop())
	plan, err := mover.Plan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mover.Plan(ctx); !errors.Is(err, ErrRunOpen) {
		t.Fatalf("second Plan() = %v, want ErrRunOpen", err)
	}

	// Discarding the plan releases the files, and keeps nothing that never
	// happened.
	if _, err := mover.Discard(ctx, plan.RunID); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("Discard() = %v, want ErrRunNotFound for a run that is now empty", err)
	}
	if _, err := mover.Plan(ctx); err != nil {
		t.Fatalf("Plan() after a discard: %v", err)
	}
}

// TestLayoutMoveFinishesARenameThatOutlivedItsTransaction covers the one state
// the filesystem can be in that the database does not know about: the rename
// ran, the commit did not.
func TestLayoutMoveFinishesARenameThatOutlivedItsTransaction(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)
	setLayout(t, pool, "{artist}/{album}")

	from := filepath.Join(rootPath, "loose", "song.mp3")
	fileID := libraryFile(t, pool, rootID, from, "Four Tet", "Rounds")
	destination := filepath.Join(rootPath, "Four Tet", "Rounds", "song.mp3")

	mover := NewMover(pool, zerolog.Nop())
	plan, err := mover.Plan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// The rename, without the transaction that should have carried it.
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(from, destination); err != nil {
		t.Fatal(err)
	}

	if _, err := mover.Apply(ctx, plan.RunID); err != nil {
		t.Fatal(err)
	}
	var status, path string
	if err := pool.QueryRow(ctx, `
		SELECT moves.status, files.path
		FROM library_file_moves moves
		JOIN library_files files ON files.id = moves.library_file_id
		WHERE moves.library_file_id = $1
	`, fileID).Scan(&status, &path); err != nil {
		t.Fatal(err)
	}
	if status != "moved" || path != destination {
		t.Errorf("resumed move = %q at %q, want moved at %q", status, path, destination)
	}
}

// A release's folder holds two things Schall wrote that are not library files.
// The cover pass writes a picture named cover, folder or front, so a player has
// something to show. The lyrics pass writes an .lrc sidecar beside each audio
// file, so a player has words to display. A layout migration moves library
// files and knew about neither.
//
// sidecarFixture lays a release down with both beside it and migrates it,
// answering with the folder it went to and the folder it came from.
func sidecarFixture(t *testing.T) (destination, folder string) {
	t.Helper()
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)
	setLayout(t, pool, "{artist}/{album}")

	folder = filepath.Join(rootPath, "Slowdive", "Souvlaki [deadbeef]")
	from := filepath.Join(folder, "01 alison.mp3")
	fileID := libraryFile(t, pool, rootID, from, "Slowdive", "Souvlaki")
	downloadProvenance(t, pool, fileID, from, folder, "Slowdive", "Souvlaki")

	if err := os.WriteFile(filepath.Join(folder, "cover.jpg"), []byte("art"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, "01 alison.lrc"), []byte("[00:12.00] to a friend"), 0o644); err != nil {
		t.Fatal(err)
	}

	mover := NewMover(pool, zerolog.Nop())
	plan, err := mover.Plan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mover.Apply(ctx, plan.RunID); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(rootPath, "Slowdive", "Souvlaki"), folder
}

// The words are per file: same basename, same folder as the audio.
func TestALayoutMoveTakesTheLyricsWithTheAudio(t *testing.T) {
	destination, _ := sidecarFixture(t)

	if _, err := os.Stat(filepath.Join(destination, "01 alison.lrc")); err != nil {
		t.Fatalf("the lyrics did not follow the audio: %v", err)
	}
}

// The picture is per folder, and goes to the folder the audio went to.
func TestALayoutMoveTakesTheCoverToTheNewFolder(t *testing.T) {
	destination, _ := sidecarFixture(t)

	if _, err := os.Stat(filepath.Join(destination, "cover.jpg")); err != nil {
		t.Fatalf("the cover did not follow the audio: %v", err)
	}
}

// And with both of them gone the old folder is empty, so it goes too. Before
// this it survived every migration, holding pictures and lyrics for music that
// was no longer there — one such folder per release.
func TestTheOldFolderGoesOnceItsSidecarsHaveFollowed(t *testing.T) {
	_, folder := sidecarFixture(t)

	if _, err := os.Stat(folder); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the old folder is still there: %v", err)
	}
}

// The cover belongs to the folder rather than to one file, so it travels once.
// A folder that already holds a picture keeps the one it has: a migration is
// not the place to decide between two covers.
func TestALayoutMoveLeavesACoverThatIsAlreadyThere(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)
	setLayout(t, pool, "{artist}/{album}")

	folder := filepath.Join(rootPath, "Slowdive", "Souvlaki [deadbeef]")
	from := filepath.Join(folder, "01 alison.mp3")
	fileID := libraryFile(t, pool, rootID, from, "Slowdive", "Souvlaki")
	downloadProvenance(t, pool, fileID, from, folder, "Slowdive", "Souvlaki")
	if err := os.WriteFile(filepath.Join(folder, "cover.jpg"), []byte("the one moving"), 0o644); err != nil {
		t.Fatal(err)
	}

	destination := filepath.Join(rootPath, "Slowdive", "Souvlaki")
	if err := os.MkdirAll(destination, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(destination, "cover.jpg"), []byte("the one already here"), 0o644); err != nil {
		t.Fatal(err)
	}

	mover := NewMover(pool, zerolog.Nop())
	plan, err := mover.Plan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mover.Apply(ctx, plan.RunID); err != nil {
		t.Fatal(err)
	}

	kept, err := os.ReadFile(filepath.Join(destination, "cover.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	if string(kept) != "the one already here" {
		t.Fatalf("the destination's cover was overwritten with %q", kept)
	}
}

// catalogueTrackOn is catalogueTrack with the release group named, because a
// test about which of two albums a file is filed under has to say which release
// the audio was identified against.
func catalogueTrackOn(
	t *testing.T, pool *pgxpool.Pool, artist, album string, releaseGroup uuid.UUID,
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
	`, artistID, releaseGroup, album).Scan(&albumID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO tracks (album_id, musicbrainz_recording_id, title, disc_number, track_number)
		VALUES ($1, $2, 'A song', 1, 1) RETURNING id
	`, albumID, uuid.New()).Scan(&trackID); err != nil {
		t.Fatal(err)
	}
	return trackID
}

// mapTo matches one file to one catalogue track, the way resolution does.
func mapTo(t *testing.T, pool *pgxpool.Pool, trackID, fileID uuid.UUID) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO track_mappings (track_id, library_file_id, method, is_manual)
		VALUES ($1, $2, 'resolved_identity', false)
	`, trackID, fileID); err != nil {
		t.Fatal(err)
	}
}

// identifiedAs records what the audio of one file was proven to be: the release
// group it was identified against, under the credit that release printed.
func identifiedAs(
	t *testing.T, pool *pgxpool.Pool, fileID uuid.UUID,
	releaseGroup uuid.UUID, credit, release string,
) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO library_file_identities (
			library_file_id, kind, musicbrainz_recording_id,
			musicbrainz_release_group_id, artist_name, release_title, method
		)
		VALUES ($1, 'external', $2, $3, $4, $5, 'fingerprint')
	`, fileID, uuid.New(), releaseGroup, credit, release); err != nil {
		t.Fatal(err)
	}
}

// A file can be matched to two albums at once: one recording is listed by every
// release that carries it, and migration 00033 allows exactly that. The two
// albums are filed under different artists, so which of them names the folder
// has to be settled rather than taken in whatever order the database returned.
// The audio settles it — it is the one thing that says which release the file is
// a copy of, the same question internal/library/tags.go choose() answers.
func TestAFileOnTwoAlbumsIsFiledUnderTheOneTheAudioNamed(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)
	own, gathered := uuid.New(), uuid.New()

	path := filepath.Join(rootPath, "loose", "song.mp3")
	fileID := libraryFile(t, pool, rootID, path, "", "")
	mapTo(t, pool, catalogueTrackOn(t, pool, "Anetha", "Mata Hari", own), fileID)
	mapTo(t, pool, catalogueTrackOn(
		t, pool, "Various Artists", "Rave or Die 10", gathered), fileID)
	identifiedAs(t, pool, fileID, own, "Anetha feat. Nene H", "Mata Hari (deluxe)")

	mover := NewMover(pool, zerolog.Nop())
	plan, err := mover.Plan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Planned != 1 {
		t.Fatalf("plan = %#v, want the file planned", plan)
	}
	var to string
	if err := pool.QueryRow(ctx,
		`SELECT to_path FROM library_file_moves WHERE run_id = $1`, plan.RunID).Scan(&to); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(rootPath, "Anetha", "Mata Hari ["+own.String()[:8]+"]", "song.mp3")
	if to != want {
		t.Fatalf("destination = %q, want %q", to, want)
	}
}

// The same file, with nothing to choose between its two albums: the audio was
// identified against a release neither of them is. Nothing says which album the
// file is on, so no album names the folder and what the audio proved names it
// instead. Picking one of the two would file a release under a name chosen at
// random, and a second planning run could choose the other.
func TestAFileOnTwoAlbumsNothingChoosesBetweenKeepsWhatTheAudioProved(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)
	elsewhere := uuid.New()

	path := filepath.Join(rootPath, "loose", "song.mp3")
	fileID := libraryFile(t, pool, rootID, path, "", "")
	mapTo(t, pool, catalogueTrackOn(t, pool, "Anetha", "Mata Hari", uuid.New()), fileID)
	mapTo(t, pool, catalogueTrackOn(
		t, pool, "Various Artists", "Rave or Die 10", uuid.New()), fileID)
	identifiedAs(t, pool, fileID, elsewhere, "Anetha", "Sunset in the Twelfth House")

	mover := NewMover(pool, zerolog.Nop())
	plan, err := mover.Plan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Planned != 1 {
		t.Fatalf("plan = %#v, want the file planned", plan)
	}
	var to string
	if err := pool.QueryRow(ctx,
		`SELECT to_path FROM library_file_moves WHERE run_id = $1`, plan.RunID).Scan(&to); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(rootPath, "Anetha",
		"Sunset in the Twelfth House ["+elsewhere.String()[:8]+"]", "song.mp3")
	if to != want {
		t.Fatalf("destination = %q, want %q", to, want)
	}
}

// A track named from its page on SoundCloud is filed under the person who
// recorded it, like every other file.
//
// The move used to work from a MusicBrainz identity alone, so a source-keyed
// file stayed in /music/Unknown, where the scanner filed it when it had no
// tags. The row said "PinkPantheress — Illegal (Abo Edit)" and the folder said
// the artist was unknown, which is what Navidrome, a backup and a person in a
// shell all read (issue #491).
func TestATrackNamedFromItsAddressIsFiledUnderItsArtist(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)

	from := filepath.Join(rootPath, "Unknown", "[1bebfa12]", "illegal.wav")
	fileID := libraryFile(t, pool, rootID, from, "", "")
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_file_identities (
			library_file_id, kind, provider, source, external_id, external_url,
			artist_name, release_title, track_title, remixer_name, method,
			confidence, is_manual, summary
		)
		VALUES ($1, 'source', 'soundcloud', 'soundcloud', 'soundcloud:293',
		        'https://soundcloud.com/abo/illegal', 'PinkPantheress',
		        'Illegal (Abo Edit)', 'Illegal (Abo Edit)', 'Abo', 'source_url',
		        1, true, 'named by hand')
	`, fileID); err != nil {
		t.Fatal(err)
	}

	mover := NewMover(pool, zerolog.Nop())
	plan, err := mover.Plan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Planned != 1 {
		t.Fatalf("plan = %#v, want the named track planned", plan)
	}
	if _, err := mover.Apply(ctx, plan.RunID); err != nil {
		t.Fatal(err)
	}

	want := filepath.Join(rootPath, "PinkPantheress",
		"Illegal (Abo Edit) [soundcloud-293]", "illegal.wav")
	var path string
	if err := pool.QueryRow(ctx,
		`SELECT path FROM library_files WHERE id = $1`, fileID).Scan(&path); err != nil {
		t.Fatal(err)
	}
	if path != want {
		t.Fatalf("recorded path = %s, want %s", path, want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("the file is not at %s: %v", want, err)
	}
}
