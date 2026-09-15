package lyrics

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/lrclib"
	"github.com/rs/zerolog"
)

// The words written beside the music. What is pinned here is what the feature
// must never do: overwrite a lyric file somebody put there, write an empty one
// for a song that has no words, touch the audio, or ask about a file the
// catalogue cannot describe.

type fakeStore struct {
	rows []db.TracksToLyricRow
	one  db.TracksToLyricRow
	err  error
	// after records the cursor each page was asked for.
	after []uuid.UUID
}

func (store *fakeStore) TracksToLyric(
	_ context.Context, params db.TracksToLyricParams,
) ([]db.TracksToLyricRow, error) {
	store.after = append(store.after, params.After)
	return store.rows, store.err
}

func (store *fakeStore) TrackToLyric(context.Context, uuid.UUID) (db.TrackToLyricRow, error) {
	if store.err != nil {
		return db.TrackToLyricRow{}, store.err
	}
	return db.TrackToLyricRow(store.one), nil
}

type fakeLRCLIB struct {
	lyrics lrclib.Lyrics
	err    error
	asked  []lrclib.Song
}

func (service *fakeLRCLIB) Get(_ context.Context, song lrclib.Song) (lrclib.Lyrics, error) {
	service.asked = append(service.asked, song)
	return service.lyrics, service.err
}

func track(t *testing.T, root, name string) db.TracksToLyricRow {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("audio bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	return db.TracksToLyricRow{
		LibraryFileID: uuid.New(),
		Path:          path,
		Title:         "EARFQUAKE",
		ArtistName:    "Tyler, the Creator",
		AlbumTitle:    "IGOR",
		DurationMs:    pgtype.Int4{Int32: 190_000, Valid: true},
	}
}

func serviceFor(store Store, fetcher Fetcher) *Service {
	service := NewService(store, fetcher, zerolog.Nop())
	service.pause = 0
	return service
}

func TestTheWordsAreWrittenBesideTheMusic(t *testing.T) {
	root := t.TempDir()
	row := track(t, root, "Tyler, the Creator/IGOR [a]/02 EARFQUAKE.flac")
	store := &fakeStore{rows: []db.TracksToLyricRow{row}}
	fetcher := &fakeLRCLIB{lyrics: lrclib.Lyrics{Synced: "[00:12.00]Don't leave"}}

	result, err := serviceFor(store, fetcher).Sweep(context.Background(), Position{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Written != 1 {
		t.Fatalf("wrote %d sidecars", result.Written)
	}

	written, err := os.ReadFile(filepath.Join(filepath.Dir(row.Path), "02 EARFQUAKE.lrc"))
	if err != nil {
		t.Fatalf("no sidecar was written: %v", err)
	}
	if string(written) != "[00:12.00]Don't leave\n" {
		t.Fatalf("sidecar = %q", written)
	}
}

// The audio is the one thing this feature must not touch.
func TestTheAudioIsUnchanged(t *testing.T) {
	root := t.TempDir()
	row := track(t, root, "An Artist/A Release [a]/01 A Song.flac")
	before, err := os.ReadFile(row.Path)
	if err != nil {
		t.Fatal(err)
	}
	beforeInfo, err := os.Stat(row.Path)
	if err != nil {
		t.Fatal(err)
	}
	store := &fakeStore{rows: []db.TracksToLyricRow{row}}
	fetcher := &fakeLRCLIB{lyrics: lrclib.Lyrics{Synced: "[00:01.00]words"}}

	if _, err := serviceFor(store, fetcher).Sweep(context.Background(), Position{}); err != nil {
		t.Fatal(err)
	}

	after, err := os.ReadFile(row.Path)
	if err != nil {
		t.Fatal(err)
	}
	afterInfo, err := os.Stat(row.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("the audio file's bytes changed")
	}
	if !beforeInfo.ModTime().Equal(afterInfo.ModTime()) {
		t.Fatal("the audio file's modified time changed")
	}
}

// LRCLIB is asked with the catalogue's account of the recording, never with the
// file's own tags, which are whatever a peer typed.
func TestTheServiceIsAskedWithTheCataloguesAccount(t *testing.T) {
	root := t.TempDir()
	row := track(t, root, "An Artist/A Release [a]/01 A Song.flac")
	store := &fakeStore{rows: []db.TracksToLyricRow{row}}
	fetcher := &fakeLRCLIB{lyrics: lrclib.Lyrics{Plain: "words"}}

	if _, err := serviceFor(store, fetcher).Sweep(context.Background(), Position{}); err != nil {
		t.Fatal(err)
	}

	if len(fetcher.asked) != 1 {
		t.Fatalf("asked %d times", len(fetcher.asked))
	}
	asked := fetcher.asked[0]
	if asked.Artist != "Tyler, the Creator" || asked.Title != "EARFQUAKE" || asked.Album != "IGOR" {
		t.Fatalf("asked about %+v", asked)
	}
	if asked.Duration.Seconds() != 190 {
		t.Fatalf("asked with a length of %v", asked.Duration)
	}
}

func TestASidecarSomebodyElseWroteIsNeverOverwritten(t *testing.T) {
	root := t.TempDir()
	row := track(t, root, "An Artist/A Release [a]/01 A Song.flac")
	mine := filepath.Join(filepath.Dir(row.Path), "01 A Song.lrc")
	if err := os.WriteFile(mine, []byte("the words I typed"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := &fakeStore{rows: []db.TracksToLyricRow{row}}
	fetcher := &fakeLRCLIB{lyrics: lrclib.Lyrics{Synced: "[00:01.00]theirs"}}

	if _, err := serviceFor(store, fetcher).Sweep(context.Background(), Position{}); err != nil {
		t.Fatal(err)
	}

	kept, err := os.ReadFile(mine)
	if err != nil {
		t.Fatal(err)
	}
	if string(kept) != "the words I typed" {
		t.Fatalf("sidecar = %q; the user's words were replaced", kept)
	}
	if len(fetcher.asked) != 0 {
		t.Fatal("asked LRCLIB about a song whose words are already beside it")
	}
}

// An instrumental has no words and never will. Nothing is written, because an
// empty file would tell the player there are lyrics and then show none.
func TestAnInstrumentalGetsNoSidecar(t *testing.T) {
	root := t.TempDir()
	row := track(t, root, "An Artist/A Release [a]/01 A Song.flac")
	store := &fakeStore{rows: []db.TracksToLyricRow{row}}
	fetcher := &fakeLRCLIB{lyrics: lrclib.Lyrics{Instrumental: true}}

	result, err := serviceFor(store, fetcher).Sweep(context.Background(), Position{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Instrumental != 1 {
		t.Fatalf("counted %d instrumentals", result.Instrumental)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(row.Path), "01 A Song.lrc")); err == nil {
		t.Fatal("a sidecar was written for a recording with no words")
	}
}

// A miss writes nothing and is not a failure.
func TestASongTheServiceHasNoWordsForIsSilence(t *testing.T) {
	root := t.TempDir()
	row := track(t, root, "An Artist/A Release [a]/01 A Song.flac")
	store := &fakeStore{rows: []db.TracksToLyricRow{row}}
	fetcher := &fakeLRCLIB{err: lrclib.ErrNoLyrics}

	result, err := serviceFor(store, fetcher).Sweep(context.Background(), Position{})
	if err != nil {
		t.Fatalf("a miss failed the pass: %v", err)
	}
	if result.Written != 0 {
		t.Fatalf("wrote %d sidecars for a song with no words", result.Written)
	}
}

// Untimed words still reach the phone; they simply do not scroll. They are worth
// writing rather than throwing away.
func TestPlainWordsAreWrittenWhenThereAreNoTimedOnes(t *testing.T) {
	root := t.TempDir()
	row := track(t, root, "An Artist/A Release [a]/01 A Song.flac")
	store := &fakeStore{rows: []db.TracksToLyricRow{row}}
	fetcher := &fakeLRCLIB{lyrics: lrclib.Lyrics{Plain: "just the words"}}

	if _, err := serviceFor(store, fetcher).Sweep(context.Background(), Position{}); err != nil {
		t.Fatal(err)
	}

	written, err := os.ReadFile(filepath.Join(filepath.Dir(row.Path), "01 A Song.lrc"))
	if err != nil {
		t.Fatalf("no sidecar was written: %v", err)
	}
	if string(written) != "just the words\n" {
		t.Fatalf("sidecar = %q", written)
	}
}

// A full page says where it stopped, so the next pass carries on rather than
// walking the same files again.
func TestAFullPageCarriesThePositionForward(t *testing.T) {
	root := t.TempDir()
	first := track(t, root, "An Artist/A Release [a]/01 One.flac")
	second := track(t, root, "An Artist/A Release [a]/02 Two.flac")
	store := &fakeStore{rows: []db.TracksToLyricRow{first, second}}
	service := serviceFor(store, &fakeLRCLIB{err: lrclib.ErrNoLyrics})
	service.batch = 2

	result, err := service.Sweep(context.Background(), Position{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.More {
		t.Fatal("a full page said there was nothing left")
	}
	if result.Next.AfterFileID != second.LibraryFileID {
		t.Fatalf("next position = %v; want the last file of the page", result.Next.AfterFileID)
	}
}

// The end of the library starts the walk again, which is what picks up songs
// LRCLIB has gained since.
func TestTheEndOfTheLibraryStartsTheWalkAgain(t *testing.T) {
	root := t.TempDir()
	only := track(t, root, "An Artist/A Release [a]/01 One.flac")
	store := &fakeStore{rows: []db.TracksToLyricRow{only}}
	service := serviceFor(store, &fakeLRCLIB{err: lrclib.ErrNoLyrics})
	service.batch = 50

	result, err := service.Sweep(context.Background(), Position{})
	if err != nil {
		t.Fatal(err)
	}
	if result.More {
		t.Fatal("a page shorter than the batch said there was more")
	}
	if result.Next != (Position{}) {
		t.Fatalf("next position = %+v; want the beginning", result.Next)
	}
}

// A service that has stopped answering ends the pass, and the pass keeps the
// position it reached so nothing is walked twice.
func TestAServiceThatStoppedAnsweringEndsThePassWhereItGotTo(t *testing.T) {
	root := t.TempDir()
	rows := []db.TracksToLyricRow{}
	for index := range consecutiveFailures + 3 {
		rows = append(rows, track(t, root,
			filepath.Join("An Artist/A Release [a]", string(rune('a'+index))+".flac")))
	}
	store := &fakeStore{rows: rows}
	service := serviceFor(store, &fakeLRCLIB{err: errors.New("lrclib is down")})

	result, err := service.Sweep(context.Background(), Position{})
	if err == nil {
		t.Fatal("a service that answered nothing ended the pass successfully")
	}
	if result.Next.AfterFileID != rows[consecutiveFailures-1].LibraryFileID {
		t.Fatalf("next position = %v; want the file it stopped on", result.Next.AfterFileID)
	}
}

// A file the catalogue cannot describe is not asked about at all.
func TestAFileTheCatalogueCannotDescribeIsNotAskedAbout(t *testing.T) {
	fetcher := &fakeLRCLIB{lyrics: lrclib.Lyrics{Plain: "words"}}
	service := serviceFor(&fakeStore{err: errors.New("no such row")}, fetcher)

	service.ForFile(context.Background(), uuid.New())

	if len(fetcher.asked) != 0 {
		t.Fatal("asked LRCLIB about a file nothing has said what it is")
	}
}

func TestAnImportedFileIsWordedOnItsOwn(t *testing.T) {
	root := t.TempDir()
	row := track(t, root, "An Artist/A Release [a]/01 A Song.flac")
	store := &fakeStore{one: row}
	fetcher := &fakeLRCLIB{lyrics: lrclib.Lyrics{Synced: "[00:01.00]words"}}

	serviceFor(store, fetcher).ForFile(context.Background(), row.LibraryFileID)

	if _, err := os.Stat(filepath.Join(filepath.Dir(row.Path), "01 A Song.lrc")); err != nil {
		t.Fatalf("no sidecar was written: %v", err)
	}
}
