package soundcloud

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	"github.com/pxldi/schall/internal/dbtest"
	"github.com/pxldi/schall/internal/identity"
)

// stubTracks answers the way SoundCloud does, without SoundCloud.
type stubTracks struct {
	track   Track
	image   []byte
	lookErr error
	artErr  error
}

func (stub *stubTracks) Lookup(context.Context, string) (Track, error) {
	if stub.lookErr != nil {
		return Track{}, stub.lookErr
	}
	return stub.track, nil
}

func (stub *stubTracks) Artwork(context.Context, string) ([]byte, string, error) {
	if stub.artErr != nil {
		return nil, "", stub.artErr
	}
	if len(stub.image) == 0 {
		return nil, "", nil
	}
	return stub.image, "image/jpeg", nil
}

func anEdit() Track {
	return Track{
		ID:          "293",
		ExternalID:  "soundcloud:293",
		Title:       "PinkPantheress - Illegal (Abo Edit) by Abo",
		Uploader:    "Abo",
		UploaderURL: "https://soundcloud.com/abo",
		ArtworkURL:  "https://i1.sndcdn.com/artworks-abc-t500x500.jpg",
		Permalink:   "https://soundcloud.com/abo/illegal",
	}
}

type adoptFixture struct {
	pool   *pgxpool.Pool
	fileID uuid.UUID
	path   string
}

// setupAdopt puts one real audio file on disc and one library_files row
// pointing at it, which is what a file uploaded through the browser and scanned
// into the library looks like by the time somebody names it.
func setupAdopt(t *testing.T, ctx context.Context) adoptFixture {
	t.Helper()
	pool := dbtest.Setup(t)

	content, err := os.ReadFile(filepath.Join("..", "tagging", "testdata", "silence.flac"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "illegal.flac")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}

	fileID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_files (id, path, size_bytes, modified_at, title_tag)
		VALUES ($1, $2, 1024, now(), 'illegal')
	`, fileID, path); err != nil {
		t.Fatal(err)
	}
	return adoptFixture{pool: pool, fileID: fileID, path: path}
}

func TestNamingAFileFromSoundCloudRecordsASourceIdentity(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := setupAdopt(t, ctx)

	tracks := &stubTracks{track: anEdit(), image: []byte("a square")}
	service := NewService(fixture.pool, tracks, zerolog.Nop())

	adopted, err := service.Adopt(ctx, fixture.fileID, "https://soundcloud.com/abo/illegal", Naming{})
	if err != nil {
		t.Fatalf("Adopt() error = %v", err)
	}
	if adopted.TrackTitle != "Illegal (Abo Edit)" {
		t.Fatalf("title = %q, want the track without the artist in front of it",
			adopted.TrackTitle)
	}

	var kind, source, externalID, externalURL, artist, title string
	var remixer *string
	var manual bool
	var recordingID *uuid.UUID
	if err := fixture.pool.QueryRow(ctx, `
		SELECT kind, source, external_id, external_url, artist_name, track_title,
		       remixer_name, is_manual, musicbrainz_recording_id
		FROM library_file_identities WHERE library_file_id = $1
	`, fixture.fileID).Scan(&kind, &source, &externalID, &externalURL, &artist,
		&title, &remixer, &manual, &recordingID); err != nil {
		t.Fatalf("read the identity: %v", err)
	}
	if kind != "source" || source != "soundcloud" || externalID != "soundcloud:293" {
		t.Fatalf("identity = %q/%q/%q", kind, source, externalID)
	}
	// The uploader edited it; PinkPantheress recorded it. Recording Abo as the
	// artist read in the library as a track called "PinkPantheress - Illegal
	// (Abo Edit) [FREE DL]" by Abo (issue #490).
	if externalURL != "https://soundcloud.com/abo/illegal" || artist != "PinkPantheress" {
		t.Fatalf("identity points at %q by %q", externalURL, artist)
	}
	if remixer == nil || *remixer != "Abo" {
		t.Fatalf("remixer = %v, want the uploader", remixer)
	}
	if !manual {
		t.Fatal("a source identity is a decision somebody made; it must be manual")
	}
	if recordingID != nil {
		t.Fatalf("the identity claims recording %s; it must claim none", recordingID)
	}

	var status string
	if err := fixture.pool.QueryRow(ctx,
		`SELECT resolution_status FROM library_files WHERE id = $1`,
		fixture.fileID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != identity.StatusSource {
		t.Fatalf("resolution status = %q, want %q", status, identity.StatusSource)
	}
}

// The uploader becomes an artist row of their own. They are not followed, they
// are keyed by their page on SoundCloud, and a MusicBrainz artist of the same
// name is a different person until somebody says otherwise.
func TestTheUploaderBecomesAnArtistNobodyFollows(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := setupAdopt(t, ctx)

	musicBrainzAbo := uuid.New()
	if _, err := fixture.pool.Exec(ctx, `
		INSERT INTO artists (musicbrainz_id, name, sort_name, followed_at)
		VALUES ($1, 'Abo', 'Abo', now())
	`, musicBrainzAbo); err != nil {
		t.Fatal(err)
	}

	service := NewService(fixture.pool, &stubTracks{track: anEdit()}, zerolog.Nop())
	adopted, err := service.Adopt(ctx, fixture.fileID, "https://soundcloud.com/abo/illegal", Naming{})
	if err != nil {
		t.Fatalf("Adopt() error = %v", err)
	}

	var source, externalID, summary string
	var followedAt *time.Time
	var mbid *uuid.UUID
	if err := fixture.pool.QueryRow(ctx, `
		SELECT coalesce(source, ''), coalesce(external_id, ''),
		       coalesce(catalogue_summary, ''), followed_at, musicbrainz_id
		FROM artists WHERE id = $1
	`, adopted.ArtistID).Scan(&source, &externalID, &summary, &followedAt, &mbid); err != nil {
		t.Fatal(err)
	}
	if source != "soundcloud" || externalID != "soundcloud:https://soundcloud.com/abo" {
		t.Fatalf("artist keyed by %q/%q", source, externalID)
	}
	if followedAt != nil {
		t.Fatal("the uploader was followed; there is no discography to keep complete")
	}
	if mbid != nil {
		t.Fatalf("the uploader was given the MusicBrainz artist %s", mbid)
	}
	if summary == "" {
		t.Fatal("an artist nobody followed has to say why they are in the library")
	}
	if adopted.ArtistID.String() == musicBrainzAbo.String() {
		t.Fatal("the uploader was merged with the MusicBrainz artist of the same name")
	}

	// A second track by the same uploader finds the row rather than making one.
	second := uuid.New()
	if _, err := fixture.pool.Exec(ctx, `
		INSERT INTO library_files (id, path, size_bytes, modified_at)
		VALUES ($1, $2, 1024, now())
	`, second, fixture.path+".2"); err != nil {
		t.Fatal(err)
	}
	again, err := service.Adopt(ctx, second, "https://soundcloud.com/abo/illegal", Naming{})
	if err != nil {
		t.Fatalf("Adopt() error = %v", err)
	}
	if again.ArtistID != adopted.ArtistID {
		t.Fatalf("a second track made a second artist row: %s then %s",
			adopted.ArtistID, again.ArtistID)
	}
}

// The split is a suggestion. The file is the person's own and the address is
// their word about it, so what they confirmed is what is written, even where it
// disagrees with everything the title says.
func TestWhatThePersonConfirmedIsWhatIsWritten(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := setupAdopt(t, ctx)

	service := NewService(fixture.pool, &stubTracks{track: anEdit()}, zerolog.Nop())
	corrected := Naming{Artist: "Pink Pantheress", Title: "Illegal", Remixer: "abo_music"}

	adopted, err := service.Adopt(ctx, fixture.fileID,
		"https://soundcloud.com/abo/illegal", corrected)
	if err != nil {
		t.Fatalf("Adopt() error = %v", err)
	}
	if adopted.Naming != corrected {
		t.Fatalf("naming = %+v, want %+v", adopted.Naming, corrected)
	}

	var artist, title string
	var remixer *string
	if err := fixture.pool.QueryRow(ctx, `
		SELECT artist_name, track_title, remixer_name
		FROM library_file_identities WHERE library_file_id = $1
	`, fixture.fileID).Scan(&artist, &title, &remixer); err != nil {
		t.Fatalf("read the identity: %v", err)
	}
	if artist != "Pink Pantheress" || title != "Illegal" || remixer == nil || *remixer != "abo_music" {
		t.Fatalf("identity = %q / %q / %v, want what was confirmed", artist, title, remixer)
	}
}

// The file itself ends up saying what it is, so a player shows the track under
// its own name. The comment carries the address it came from.
func TestTheFileIsTaggedWithWhatSoundCloudPublished(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := setupAdopt(t, ctx)

	service := NewService(fixture.pool, &stubTracks{track: anEdit()}, zerolog.Nop())
	if _, err := service.Adopt(ctx, fixture.fileID, "https://soundcloud.com/abo/illegal", Naming{}); err != nil {
		t.Fatalf("Adopt() error = %v", err)
	}

	tags := readTags(t, fixture.path)
	if got := tags["TITLE"]; len(got) != 1 || got[0] != "Illegal (Abo Edit)" {
		t.Fatalf("TITLE = %q", got)
	}
	// A single belongs to no release, so the album is the track.
	if got := tags["ALBUM"]; len(got) != 1 || got[0] != "Illegal (Abo Edit)" {
		t.Fatalf("ALBUM = %q", got)
	}
	// The artist is who the track is by. The edit is named in the title, which
	// is where a player shows it.
	if got := tags["ARTIST"]; len(got) != 1 || got[0] != "PinkPantheress" {
		t.Fatalf("ARTIST = %q", got)
	}
	if got := tags["COMMENT"]; len(got) != 1 || got[0] != "https://soundcloud.com/abo/illegal" {
		t.Fatalf("COMMENT = %q", got)
	}
}

// A track SoundCloud has no picture for, and one whose picture could not be
// fetched, are the same to the decision: it is made either way, and the absence
// is written down so it is not asked about again.
func TestATrackWithNoPictureIsStillNamed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := setupAdopt(t, ctx)

	tracks := &stubTracks{track: anEdit(), artErr: errors.New("sndcdn is down")}
	service := NewService(fixture.pool, tracks, zerolog.Nop())
	adopted, err := service.Adopt(ctx, fixture.fileID, "https://soundcloud.com/abo/illegal", Naming{})
	if err != nil {
		t.Fatalf("Adopt() error = %v", err)
	}
	if adopted.Pictured {
		t.Fatal("a picture that could not be fetched was reported as one that was")
	}

	var contentType string
	var image []byte
	if err := fixture.pool.QueryRow(ctx,
		`SELECT coalesce(image, ''::bytea), content_type FROM file_cover_art WHERE library_file_id = $1`,
		fixture.fileID).Scan(&image, &contentType); err != nil {
		t.Fatalf("the absence of a picture was not written down: %v", err)
	}
	if len(image) != 0 || contentType != "" {
		t.Fatalf("picture = %q/%q, want the row that records there is none", image, contentType)
	}
}

// A file the library does not hold cannot be named, and a link that is not a
// track is refused before anything is written.
func TestNothingIsWrittenForAFileOrATrackThatIsNotThere(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := setupAdopt(t, ctx)

	service := NewService(fixture.pool, &stubTracks{track: anEdit()}, zerolog.Nop())
	if _, err := service.Adopt(ctx, uuid.New(), "https://soundcloud.com/abo/illegal", Naming{}); !errors.Is(err, ErrFileNotFound) {
		t.Fatalf("Adopt() on a file nobody holds = %v, want ErrFileNotFound", err)
	}

	gone := NewService(fixture.pool, &stubTracks{lookErr: ErrTrackGone}, zerolog.Nop())
	if _, err := gone.Adopt(ctx, fixture.fileID, "https://soundcloud.com/abo/illegal", Naming{}); !errors.Is(err, ErrTrackGone) {
		t.Fatalf("Adopt() on a track SoundCloud lost = %v, want ErrTrackGone", err)
	}

	var identities int
	if err := fixture.pool.QueryRow(ctx,
		`SELECT count(*) FROM library_file_identities`).Scan(&identities); err != nil {
		t.Fatal(err)
	}
	if identities != 0 {
		t.Fatalf("%d identities were written for decisions that were refused", identities)
	}
}

// Between Preview showing what an address holds and Adopt confirming it, a
// scan can run and give the same file a track_mappings row from tags or audio
// that happen to agree with an unrelated catalogue recording. Adopt must
// refuse rather than write a source identity on top of that: nothing in this
// package touches track_mappings, so the file would end up carrying both a
// person's word that it holds no recording and a mapping saying which one it
// holds.
func TestAdoptRefusesAFileAScanAlreadyMatched(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := setupAdopt(t, ctx)

	var trackID uuid.UUID
	var artistID uuid.UUID
	if err := fixture.pool.QueryRow(ctx, `
		INSERT INTO artists (musicbrainz_id, name, sort_name) VALUES ($1, $2, $2) RETURNING id
	`, uuid.New(), "Unrelated Artist").Scan(&artistID); err != nil {
		t.Fatal(err)
	}
	var albumID uuid.UUID
	if err := fixture.pool.QueryRow(ctx, `
		INSERT INTO albums (artist_id, musicbrainz_release_group_id, title)
		VALUES ($1, $2, $3) RETURNING id
	`, artistID, uuid.New(), "Unrelated Album").Scan(&albumID); err != nil {
		t.Fatal(err)
	}
	if err := fixture.pool.QueryRow(ctx, `
		INSERT INTO tracks (album_id, musicbrainz_recording_id, title, disc_number, track_number)
		VALUES ($1, $2, $3, 1, 1) RETURNING id
	`, albumID, uuid.New(), "Unrelated Track").Scan(&trackID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `
		INSERT INTO track_mappings (track_id, library_file_id, method, confidence)
		VALUES ($1, $2, 'exact_tags', 1)
	`, trackID, fixture.fileID); err != nil {
		t.Fatal(err)
	}

	service := NewService(fixture.pool, &stubTracks{track: anEdit()}, zerolog.Nop())
	if _, err := service.Adopt(ctx, fixture.fileID, "https://soundcloud.com/abo/illegal", Naming{}); !errors.Is(err, ErrFileAlreadyMatched) {
		t.Fatalf("Adopt() on a file a scan already matched = %v, want ErrFileAlreadyMatched", err)
	}

	var identities, mappings int
	if err := fixture.pool.QueryRow(ctx,
		`SELECT count(*) FROM library_file_identities WHERE library_file_id = $1`,
		fixture.fileID).Scan(&identities); err != nil {
		t.Fatal(err)
	}
	if identities != 0 {
		t.Fatalf("%d identities were written for a decision that was refused", identities)
	}
	if err := fixture.pool.QueryRow(ctx,
		`SELECT count(*) FROM track_mappings WHERE library_file_id = $1`,
		fixture.fileID).Scan(&mappings); err != nil {
		t.Fatal(err)
	}
	if mappings != 1 {
		t.Fatalf("mappings = %d, want the existing mapping left exactly as it was", mappings)
	}
}

// A local-only identity somebody recorded is a settled answer that the file
// has no catalogue recording. Adopt must leave it in place for the person to
// review instead of replacing it with a source identity.
func TestAdoptRefusesAFileWithAManualLocalOnlyIdentity(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := setupAdopt(t, ctx)

	if _, err := fixture.pool.Exec(ctx, `
		INSERT INTO library_file_identities (library_file_id, kind, method, is_manual, summary)
		VALUES ($1, 'local_only', 'manual', true, 'recorded by hand')
	`, fixture.fileID); err != nil {
		t.Fatal(err)
	}

	service := NewService(fixture.pool, &stubTracks{track: anEdit()}, zerolog.Nop())
	if _, err := service.Adopt(ctx, fixture.fileID, "https://soundcloud.com/abo/illegal", Naming{}); !errors.Is(err, ErrFileAlreadyMatched) {
		t.Fatalf("Adopt() on a file with a manual local-only identity = %v, want ErrFileAlreadyMatched", err)
	}

	var kind string
	if err := fixture.pool.QueryRow(ctx, `
		SELECT kind FROM library_file_identities WHERE library_file_id = $1
	`, fixture.fileID).Scan(&kind); err != nil {
		t.Fatal(err)
	}
	if kind != "local_only" {
		t.Fatalf("identity kind = %q, want the manual local-only decision left in place", kind)
	}
}
