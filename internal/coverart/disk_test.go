package coverart

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/db"
	"github.com/rs/zerolog"
)

// The picture written beside the music, for the playback server that cannot read
// a Postgres table. What is pinned here is what it must never do: overwrite a
// picture somebody put there, fetch the same picture twice, or write anything at
// the top of the collection.

type fakeDiskStore struct {
	releases    []db.ReleaseFoldersToPictureRow
	cached      db.ReleaseCoverArtRow
	artists     []db.ArtistFoldersToPictureRow
	image       db.ArtistImageRow
	roots       []db.LibraryRootRow
	files       []db.SourceFileToPictureRow
	fileCover   db.FileCoverArtRow
	releasesErr error
	// cachedReads counts how often the cache was read for a release, which is
	// what says whether the archive was gone to instead.
	cachedReads int
}

func (store *fakeDiskStore) ReleaseCoverArt(
	context.Context, uuid.UUID,
) (db.ReleaseCoverArtRow, error) {
	store.cachedReads++
	return store.cached, nil
}

func (store *fakeDiskStore) ReleaseFoldersToPicture(context.Context) ([]db.ReleaseFoldersToPictureRow, error) {
	return store.releases, store.releasesErr
}

func (store *fakeDiskStore) ArtistFoldersToPicture(context.Context) ([]db.ArtistFoldersToPictureRow, error) {
	return store.artists, nil
}

func (store *fakeDiskStore) ArtistImage(context.Context, uuid.UUID) (db.ArtistImageRow, error) {
	return store.image, nil
}

func (store *fakeDiskStore) ListLibraryRoots(context.Context) ([]db.LibraryRootRow, error) {
	return store.roots, nil
}

func (store *fakeDiskStore) SourceFilesToPicture(
	context.Context,
) ([]db.SourceFileToPictureRow, error) {
	return store.files, nil
}

func (store *fakeDiskStore) FileCoverArt(
	context.Context, uuid.UUID,
) (db.FileCoverArtRow, error) {
	return store.fileCover, nil
}

type fakeArchive struct {
	artwork Artwork
	err     error
	asked   int
}

func (archive *fakeArchive) FullFront(
	context.Context, uuid.NullUUID, uuid.NullUUID,
) (Artwork, error) {
	archive.asked++
	if archive.err != nil {
		return Artwork{}, archive.err
	}
	return archive.artwork, nil
}

func folder(t *testing.T, root, name string) string {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestACoverIsWrittenIntoAReleaseFolderThatHasNone(t *testing.T) {
	root := t.TempDir()
	music := folder(t, root, "Tyler, the Creator/IGOR [abc]")
	store := &fakeDiskStore{
		releases: []db.ReleaseFoldersToPictureRow{{ID: uuid.New(), Source: Name, Folder: music}},
		roots:    []db.LibraryRootRow{{Path: root}},
	}
	archive := &fakeArchive{artwork: Artwork{Image: aPicture(1), ContentType: "image/jpeg"}}

	if _, err := NewDiskWriter(store, archive, zerolog.Nop()).Write(context.Background()); err != nil {
		t.Fatal(err)
	}

	written, err := os.ReadFile(filepath.Join(music, "cover.jpg"))
	if err != nil {
		t.Fatalf("no cover was written: %v", err)
	}
	if !bytes.Equal(written, aPicture(1)) {
		t.Fatalf("cover = %q, want the picture the archive sent", written)
	}
	// The archive holds the full-size copy of what it gave, and that is the one
	// worth writing. The cached five hundred pixels are not read at all.
	if store.cachedReads != 0 {
		t.Fatalf("read the cache %d times for a picture the archive answered with",
			store.cachedReads)
	}
}

// A page an archive labelled image/jpeg is not a picture. Written as cover.jpg
// it would be a broken frame the folder keeps for good, because the file on disc
// is what stops this being looked at again — so the cached picture is written
// instead.
func TestBytesTheArchiveSentThatNobodyCanOpenAreNotWritten(t *testing.T) {
	root := t.TempDir()
	music := folder(t, root, "An Artist/A Release [abc]")
	store := &fakeDiskStore{
		releases: []db.ReleaseFoldersToPictureRow{{ID: uuid.New(), Source: Name, Folder: music}},
		cached: db.ReleaseCoverArtRow{
			Image: aPicture(2), ContentType: "image/png", Source: Name,
		},
		roots: []db.LibraryRootRow{{Path: root}},
	}
	archive := &fakeArchive{artwork: Artwork{
		Image: []byte("<html>an outage page</html>"), ContentType: "image/jpeg",
	}}

	if _, err := NewDiskWriter(store, archive, zerolog.Nop()).Write(context.Background()); err != nil {
		t.Fatal(err)
	}

	written, err := os.ReadFile(filepath.Join(music, "cover.png"))
	if err != nil {
		t.Fatalf("no cover was written: %v", err)
	}
	if !bytes.Equal(written, aPicture(2)) {
		t.Fatalf("cover = %q, want the picture Schall is showing", written)
	}
}

// And with nothing to fall back on, nothing is written: an unopenable cover is
// worse than none, and the next pass asks again.
func TestNothingIsWrittenWhenNeitherCopyIsAPicture(t *testing.T) {
	root := t.TempDir()
	music := folder(t, root, "An Artist/A Release [abc]")
	store := &fakeDiskStore{
		releases: []db.ReleaseFoldersToPictureRow{{ID: uuid.New(), Source: Name, Folder: music}},
		cached: db.ReleaseCoverArtRow{
			Image: []byte("<html>an outage page</html>"), ContentType: "image/jpeg",
			Source: Name,
		},
		roots: []db.LibraryRootRow{{Path: root}},
	}
	archive := &fakeArchive{artwork: Artwork{
		Image: []byte("<html>an outage page</html>"), ContentType: "image/jpeg",
	}}

	if _, err := NewDiskWriter(store, archive, zerolog.Nop()).Write(context.Background()); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(music)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("wrote %d files for a release neither copy of which is a picture", len(entries))
	}
}

// The extension names the bytes, because a picture called .jpg that is a PNG is
// a picture some players will not open.
func TestACoverIsNamedAfterWhatTheArchiveSent(t *testing.T) {
	root := t.TempDir()
	music := folder(t, root, "An Artist/A Release [abc]")
	store := &fakeDiskStore{
		releases: []db.ReleaseFoldersToPictureRow{{ID: uuid.New(), Source: Name, Folder: music}},
		roots:    []db.LibraryRootRow{{Path: root}},
	}
	archive := &fakeArchive{artwork: Artwork{Image: aPicture(1), ContentType: "image/png"}}

	if _, err := NewDiskWriter(store, archive, zerolog.Nop()).Write(context.Background()); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(music, "cover.png")); err != nil {
		t.Fatalf("no cover.png was written: %v", err)
	}
}

func TestAPictureTheUserPutThereIsNeverOverwritten(t *testing.T) {
	root := t.TempDir()
	music := folder(t, root, "An Artist/A Release [abc]")
	mine := filepath.Join(music, "cover.jpg")
	if err := os.WriteFile(mine, []byte("the one I chose"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := &fakeDiskStore{
		releases: []db.ReleaseFoldersToPictureRow{{ID: uuid.New(), Source: Name, Folder: music}},
		roots:    []db.LibraryRootRow{{Path: root}},
	}
	archive := &fakeArchive{artwork: Artwork{Image: aPicture(1), ContentType: "image/jpeg"}}

	if _, err := NewDiskWriter(store, archive, zerolog.Nop()).Write(context.Background()); err != nil {
		t.Fatal(err)
	}

	kept, err := os.ReadFile(mine)
	if err != nil {
		t.Fatal(err)
	}
	if string(kept) != "the one I chose" {
		t.Fatalf("cover = %q; the user's picture was replaced", kept)
	}
	if archive.asked != 0 {
		t.Fatalf("asked the archive %d times for a folder that was already answered", archive.asked)
	}
}

// Navidrome reads folder.* and front.* as well, so a picture under either of
// those names is an answer this must not override — writing cover.jpg beside
// folder.jpg changes what the player shows without deleting anything.
func TestAPictureUnderAnyNameThePlayerReadsIsLeftAlone(t *testing.T) {
	for _, name := range []string{"folder.jpeg", "Front.PNG", "COVER.jpg"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			music := folder(t, root, "An Artist/A Release [abc]")
			if err := os.WriteFile(filepath.Join(music, name), []byte("theirs"), 0o644); err != nil {
				t.Fatal(err)
			}
			store := &fakeDiskStore{
				releases: []db.ReleaseFoldersToPictureRow{{ID: uuid.New(), Source: Name, Folder: music}},
				roots:    []db.LibraryRootRow{{Path: root}},
			}
			archive := &fakeArchive{artwork: Artwork{Image: aPicture(1), ContentType: "image/jpeg"}}

			if _, err := NewDiskWriter(store, archive, zerolog.Nop()).Write(context.Background()); err != nil {
				t.Fatal(err)
			}

			if archive.asked != 0 {
				t.Fatalf("asked the archive for a folder that already has %s", name)
			}
		})
	}
}

// The file on disc is the whole record of what was written, so the second pass
// costs the archive nothing.
func TestASecondPassAsksTheArchiveForNothing(t *testing.T) {
	root := t.TempDir()
	music := folder(t, root, "An Artist/A Release [abc]")
	store := &fakeDiskStore{
		releases: []db.ReleaseFoldersToPictureRow{{ID: uuid.New(), Source: Name, Folder: music}},
		roots:    []db.LibraryRootRow{{Path: root}},
	}
	archive := &fakeArchive{artwork: Artwork{Image: aPicture(1), ContentType: "image/jpeg"}}
	writer := NewDiskWriter(store, archive, zerolog.Nop())

	for range 3 {
		if _, err := writer.Write(context.Background()); err != nil {
			t.Fatal(err)
		}
	}

	if archive.asked != 1 {
		t.Fatalf("asked the archive %d times; want once, ever", archive.asked)
	}
}

// The archive holds one size of the picture it gave and none of any other, so
// when it has no full-size front the cached copy is written instead. Schall is
// showing that copy on its own pages already, and the player showing nothing
// beside music Schall has pictured is what this file exists to stop.
func TestTheCachedPictureIsWrittenWhenTheArchiveHasNoFullSizeFront(t *testing.T) {
	root := t.TempDir()
	music := folder(t, root, "An Artist/A Release [abc]")
	store := &fakeDiskStore{
		releases: []db.ReleaseFoldersToPictureRow{{ID: uuid.New(), Source: Name, Folder: music}},
		cached: db.ReleaseCoverArtRow{
			Image: aPicture(2), ContentType: "image/png", Source: Name,
		},
		roots: []db.LibraryRootRow{{Path: root}},
	}
	writer := NewDiskWriter(store, &fakeArchive{err: ErrNoArtwork}, zerolog.Nop())

	if _, err := writer.Write(context.Background()); err != nil {
		t.Fatalf("an absence failed the pass: %v", err)
	}

	written, err := os.ReadFile(filepath.Join(music, "cover.png"))
	if err != nil {
		t.Fatalf("no cover was written: %v", err)
	}
	if !bytes.Equal(written, aPicture(2)) {
		t.Fatalf("cover = %q, want the picture Schall is showing", written)
	}
}

// A cover found by name at iTunes, or read out of one of the release's own
// files, exists nowhere but the cache. Asking the Cover Art Archive for it would
// ask about a picture the archive never had.
func TestAPictureTheArchiveNeverHadIsWrittenWithoutAskingIt(t *testing.T) {
	for _, source := range []string{NameITunes, NameEmbedded} {
		t.Run(source, func(t *testing.T) {
			root := t.TempDir()
			music := folder(t, root, "An Artist/A Release [abc]")
			store := &fakeDiskStore{
				releases: []db.ReleaseFoldersToPictureRow{
					{ID: uuid.New(), Source: source, Folder: music},
				},
				cached: db.ReleaseCoverArtRow{
					Image: aPicture(2), ContentType: "image/jpeg", Source: source,
				},
				roots: []db.LibraryRootRow{{Path: root}},
			}
			archive := &fakeArchive{artwork: Artwork{
				Image: []byte("somebody else's"), ContentType: "image/jpeg",
			}}

			if _, err := NewDiskWriter(store, archive, zerolog.Nop()).
				Write(context.Background()); err != nil {
				t.Fatal(err)
			}

			written, err := os.ReadFile(filepath.Join(music, "cover.jpg"))
			if err != nil {
				t.Fatalf("no cover was written: %v", err)
			}
			if !bytes.Equal(written, aPicture(2)) {
				t.Fatalf("cover = %q, want the cached picture", written)
			}
			if archive.asked != 0 {
				t.Fatalf("asked the archive %d times about a picture it never had",
					archive.asked)
			}
			if store.cachedReads != 1 {
				t.Fatalf("read the cache %d times, want the one place the picture is",
					store.cachedReads)
			}
		})
	}
}

// Nothing at all to write is not a failure and writes nothing.
func TestAReleaseWithNoPictureAnywhereIsSkipped(t *testing.T) {
	root := t.TempDir()
	music := folder(t, root, "An Artist/A Release [abc]")
	store := &fakeDiskStore{
		releases: []db.ReleaseFoldersToPictureRow{{ID: uuid.New(), Source: Name, Folder: music}},
		roots:    []db.LibraryRootRow{{Path: root}},
	}
	writer := NewDiskWriter(store, &fakeArchive{err: ErrNoArtwork}, zerolog.Nop())

	if _, err := writer.Write(context.Background()); err != nil {
		t.Fatalf("an absence failed the pass: %v", err)
	}

	entries, err := os.ReadDir(music)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("wrote %d files for a release with no picture", len(entries))
	}
}

// One release that cannot be fetched costs that release and nothing else.
func TestOneReleaseThatCouldNotBeFetchedDoesNotStopTheRest(t *testing.T) {
	root := t.TempDir()
	first := folder(t, root, "An Artist/One [a]")
	second := folder(t, root, "Another Artist/Two [b]")
	store := &fakeDiskStore{
		releases: []db.ReleaseFoldersToPictureRow{
			{ID: uuid.New(), Source: Name, Folder: first},
			{ID: uuid.New(), Source: Name, Folder: second},
		},
		roots: []db.LibraryRootRow{{Path: root}},
	}
	// The first call fails and every later one answers.
	archive := &failingOnce{artwork: Artwork{Image: aPicture(1), ContentType: "image/jpeg"}}

	if _, err := NewDiskWriter(store, archive, zerolog.Nop()).Write(context.Background()); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(second, "cover.jpg")); err != nil {
		t.Fatalf("the second release was not pictured: %v", err)
	}
}

type failingOnce struct {
	artwork Artwork
	asked   int
}

func (archive *failingOnce) FullFront(
	context.Context, uuid.NullUUID, uuid.NullUUID,
) (Artwork, error) {
	archive.asked++
	if archive.asked == 1 {
		return Artwork{}, errors.New("the archive could not be reached")
	}
	return archive.artwork, nil
}

func TestAnArtistPictureIsWrittenAboveTheirReleases(t *testing.T) {
	root := t.TempDir()
	artistID := uuid.New()
	first := folder(t, root, "Tyler, the Creator/IGOR [a]")
	second := folder(t, root, "Tyler, the Creator/Flower Boy [b]")
	store := &fakeDiskStore{
		artists: []db.ArtistFoldersToPictureRow{
			{ID: artistID, Folder: first},
			{ID: artistID, Folder: second},
		},
		image: db.ArtistImageRow{Image: []byte("a face"), ContentType: "image/jpeg"},
		roots: []db.LibraryRootRow{{Path: root}},
	}

	if _, err := NewDiskWriter(store, &fakeArchive{}, zerolog.Nop()).Write(context.Background()); err != nil {
		t.Fatal(err)
	}

	written, err := os.ReadFile(filepath.Join(root, "Tyler, the Creator", "artist.jpg"))
	if err != nil {
		t.Fatalf("no artist picture was written: %v", err)
	}
	if string(written) != "a face" {
		t.Fatalf("artist picture = %q", written)
	}
}

// An artist with one release still has a folder above it, and that is where the
// picture goes.
func TestAnArtistWithOneReleaseIsPicturedInTheFolderAboveIt(t *testing.T) {
	root := t.TempDir()
	only := folder(t, root, "An Artist/Their Only Record [a]")
	store := &fakeDiskStore{
		artists: []db.ArtistFoldersToPictureRow{{ID: uuid.New(), Folder: only}},
		image:   db.ArtistImageRow{Image: []byte("a face"), ContentType: "image/jpeg"},
		roots:   []db.LibraryRootRow{{Path: root}},
	}

	if _, err := NewDiskWriter(store, &fakeArchive{}, zerolog.Nop()).Write(context.Background()); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(root, "An Artist", "artist.jpg")); err != nil {
		t.Fatalf("no artist picture was written: %v", err)
	}
}

// Nothing is written at the top of somebody's music volume. A picture there
// would claim the whole collection belongs to one artist.
func TestNoArtistPictureIsWrittenAtTheLibraryRoot(t *testing.T) {
	root := t.TempDir()
	flat := folder(t, root, "A Release [a]")
	store := &fakeDiskStore{
		artists: []db.ArtistFoldersToPictureRow{{ID: uuid.New(), Folder: flat}},
		image:   db.ArtistImageRow{Image: []byte("a face"), ContentType: "image/jpeg"},
		roots:   []db.LibraryRootRow{{Path: root}},
	}

	if _, err := NewDiskWriter(store, &fakeArchive{}, zerolog.Nop()).Write(context.Background()); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(root, "artist.jpg")); err == nil {
		t.Fatal("an artist picture was written at the top of the library")
	}
}

// Nothing is written outside the library at all, whatever a row says.
func TestNoPictureIsWrittenOutsideTheLibrary(t *testing.T) {
	root := t.TempDir()
	elsewhere := t.TempDir()
	outside := folder(t, elsewhere, "Somebody/A Release [a]")
	store := &fakeDiskStore{
		artists: []db.ArtistFoldersToPictureRow{{ID: uuid.New(), Folder: outside}},
		image:   db.ArtistImageRow{Image: []byte("a face"), ContentType: "image/jpeg"},
		roots:   []db.LibraryRootRow{{Path: root}},
	}

	if _, err := NewDiskWriter(store, &fakeArchive{}, zerolog.Nop()).Write(context.Background()); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(elsewhere, "Somebody", "artist.jpg")); err == nil {
		t.Fatal("a picture was written outside the library")
	}
}

func TestAnArtistPictureAlreadyThereIsLeftAlone(t *testing.T) {
	root := t.TempDir()
	artistFolder := folder(t, root, "An Artist")
	mine := filepath.Join(artistFolder, "artist.png")
	if err := os.WriteFile(mine, []byte("the one I chose"), 0o644); err != nil {
		t.Fatal(err)
	}
	release := folder(t, root, "An Artist/A Release [a]")
	store := &fakeDiskStore{
		artists: []db.ArtistFoldersToPictureRow{{ID: uuid.New(), Folder: release}},
		image:   db.ArtistImageRow{Image: []byte("theirs"), ContentType: "image/jpeg"},
		roots:   []db.LibraryRootRow{{Path: root}},
	}

	if _, err := NewDiskWriter(store, &fakeArchive{}, zerolog.Nop()).Write(context.Background()); err != nil {
		t.Fatal(err)
	}

	kept, err := os.ReadFile(mine)
	if err != nil {
		t.Fatal(err)
	}
	if string(kept) != "the one I chose" {
		t.Fatalf("artist picture = %q", kept)
	}
	if _, err := os.Stat(filepath.Join(artistFolder, "artist.jpg")); err == nil {
		t.Fatal("a second artist picture was written beside the user's")
	}
}

// A pass that reaches its budget says there is more to do, so the next one
// carries on rather than the rest waiting for a new release to be added.
func TestAFullPassSaysThereIsMoreToDo(t *testing.T) {
	root := t.TempDir()
	store := &fakeDiskStore{roots: []db.LibraryRootRow{{Path: root}}}
	for index := range 3 {
		store.releases = append(store.releases, db.ReleaseFoldersToPictureRow{
			ID:     uuid.New(),
			Source: Name,
			Folder: folder(t, root, "An Artist/Release "+string(rune('a'+index))+" [x]"),
		})
	}
	writer := NewDiskWriter(store, &fakeArchive{
		artwork: Artwork{Image: aPicture(1), ContentType: "image/jpeg"},
	}, zerolog.Nop())
	writer.budget = 2

	more, err := writer.Write(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !more {
		t.Fatal("a pass that filled its budget said there was nothing left")
	}
}

// A cover the person supplied is the one exception to "a folder that has a
// cover is done". They looked at the record, saw the wrong sleeve, and said
// this is the picture — so the file Schall wrote earlier is exactly what they
// were correcting, and it goes.
func TestASuppliedCoverReplacesTheCoverSchallWroteBefore(t *testing.T) {
	root := t.TempDir()
	music := folder(t, root, "femtanyl/ANTHERO, TERRIBLE! [abc]")
	if err := os.WriteFile(filepath.Join(music, "cover.jpg"), aPicture(2), 0o644); err != nil {
		t.Fatal(err)
	}
	store := &fakeDiskStore{
		releases: []db.ReleaseFoldersToPictureRow{
			{ID: uuid.New(), Source: NameUser, Folder: music},
		},
		cached: db.ReleaseCoverArtRow{
			Image: aPicture(3), ContentType: "image/png", Source: NameUser,
		},
		roots: []db.LibraryRootRow{{Path: root}},
	}
	archive := &fakeArchive{}

	if _, err := NewDiskWriter(store, archive, zerolog.Nop()).Write(context.Background()); err != nil {
		t.Fatal(err)
	}

	written, err := os.ReadFile(filepath.Join(music, "cover.png"))
	if err != nil {
		t.Fatalf("the supplied cover was not written: %v", err)
	}
	if !bytes.Equal(written, aPicture(3)) {
		t.Fatalf("cover.png is not the picture the person supplied")
	}
	// One cover to a folder. Two would leave the player to pick.
	if _, err := os.Stat(filepath.Join(music, "cover.jpg")); !os.IsNotExist(err) {
		t.Fatalf("the cover it replaced is still there: %v", err)
	}
	// The archive has nothing to say about a picture it never had.
	if archive.asked != 0 {
		t.Fatalf("the archive was asked %d times about a supplied cover", archive.asked)
	}
}

// The same cover, pass after pass, is the same bytes. Written every time it
// would rewrite somebody's library once an hour forever, so the file is looked
// at and left alone.
func TestASuppliedCoverAlreadyOnDiscIsNotWrittenAgain(t *testing.T) {
	root := t.TempDir()
	music := folder(t, root, "femtanyl/ANTHERO, TERRIBLE! [abc]")
	cover := filepath.Join(music, "cover.png")
	if err := os.WriteFile(cover, aPicture(3), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(cover)
	if err != nil {
		t.Fatal(err)
	}
	when := before.ModTime().Add(-time.Hour)
	if err := os.Chtimes(cover, when, when); err != nil {
		t.Fatal(err)
	}
	store := &fakeDiskStore{
		releases: []db.ReleaseFoldersToPictureRow{
			{ID: uuid.New(), Source: NameUser, Folder: music},
		},
		cached: db.ReleaseCoverArtRow{
			Image: aPicture(3), ContentType: "image/png", Source: NameUser,
		},
		roots: []db.LibraryRootRow{{Path: root}},
	}

	if _, err := NewDiskWriter(store, &fakeArchive{}, zerolog.Nop()).
		Write(context.Background()); err != nil {
		t.Fatal(err)
	}

	after, err := os.Stat(cover)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(when) {
		t.Fatalf("the cover was written again: %s, want it left at %s",
			after.ModTime(), when)
	}
}

// cover.* is the one name this package writes, so it is the one name it may
// replace. A folder.jpg or a front.png was put there by a person or by another
// tool, and a folder holding one is left exactly as it is.
func TestASuppliedCoverLeavesAPictureSomebodyElsePlaced(t *testing.T) {
	root := t.TempDir()
	music := folder(t, root, "femtanyl/ANTHERO, TERRIBLE! [abc]")
	if err := os.WriteFile(filepath.Join(music, "front.jpg"), aPicture(2), 0o644); err != nil {
		t.Fatal(err)
	}
	store := &fakeDiskStore{
		releases: []db.ReleaseFoldersToPictureRow{
			{ID: uuid.New(), Source: NameUser, Folder: music},
		},
		cached: db.ReleaseCoverArtRow{
			Image: aPicture(3), ContentType: "image/png", Source: NameUser,
		},
		roots: []db.LibraryRootRow{{Path: root}},
	}

	if _, err := NewDiskWriter(store, &fakeArchive{}, zerolog.Nop()).
		Write(context.Background()); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(music, "cover.png")); !os.IsNotExist(err) {
		t.Fatalf("a cover was written into a folder somebody had already answered")
	}
	kept, err := os.ReadFile(filepath.Join(music, "front.jpg"))
	if err != nil || !bytes.Equal(kept, aPicture(2)) {
		t.Fatalf("the picture somebody placed was not left alone: %v", err)
	}
}
