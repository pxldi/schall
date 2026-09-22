package tracksource

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/db"
	"github.com/rs/zerolog"
)

type fakeNameStore struct {
	file  db.SourceFileToName
	named *db.NameSourceFileParams
}

func (store *fakeNameStore) KeyedFileToName(context.Context, uuid.UUID) (db.SourceFileToName, error) {
	return store.file, nil
}

func (store *fakeNameStore) NameSourceFile(_ context.Context, params db.NameSourceFileParams) (uuid.UUID, error) {
	store.named = &params
	return uuid.New(), nil
}

type fakeArtwork struct {
	image []byte
	err   error
}

func (artwork fakeArtwork) Artwork(context.Context, string) ([]byte, string, error) {
	return artwork.image, "image/jpeg", artwork.err
}

func TestNamingAKeyedFileWritesWhatThePersonConfirmed(t *testing.T) {
	store := &fakeNameStore{file: db.SourceFileToName{
		LibraryFileID: uuid.New(), Path: filepath.Join(t.TempDir(), "missing.mp3"),
		Source: SoundCloud, ExternalID: "soundcloud:293",
		ExternalURL: "https://soundcloud.com/abo/illegal-edit",
		Lookup: db.SourceLookup{
			Title: "PinkPantheress - Illegal (Abo Edit)", Uploader: "Abo",
			AccountID:  "soundcloud:https://soundcloud.com/abo",
			ArtworkURL: "https://i1.sndcdn.com/a.jpg",
		},
	}}
	namer := NewNamer(store, fakeArtwork{image: []byte{0xff, 0xd8}}, zerolog.Nop())

	// The file is not there, so the tag write fails and is logged. The rows are
	// written first and stand.
	if err := namer.NameKeyedFile(context.Background(), uuid.New()); err != nil {
		t.Fatalf("NameKeyedFile() error = %v", err)
	}
	named := store.named
	if named == nil {
		t.Fatal("nothing was named")
	}
	if named.Title != "PinkPantheress - Illegal (Abo Edit)" || named.Artist != "Abo" ||
		named.AccountID != "soundcloud:https://soundcloud.com/abo" || len(named.Image) != 2 ||
		named.UploaderSummary != "In the library because a track they uploaded to SoundCloud is." {
		t.Errorf("named = %+v, want the title whole, the uploader as artist and the artwork", named)
	}
}

func TestArtworkThatCannotBeFetchedStillNamesTheFile(t *testing.T) {
	store := &fakeNameStore{file: db.SourceFileToName{
		LibraryFileID: uuid.New(), Path: filepath.Join(t.TempDir(), "missing.mp3"),
		Source: YouTube, ExternalID: "youtube:x",
		Lookup: db.SourceLookup{Title: "Illegal", Artist: "PinkPantheress", ArtworkURL: "https://i.ytimg.com/x.jpg"},
	}}
	namer := NewNamer(store, fakeArtwork{err: errors.New("status 404")}, zerolog.Nop())

	if err := namer.NameKeyedFile(context.Background(), uuid.New()); err != nil {
		t.Fatalf("NameKeyedFile() error = %v", err)
	}
	if store.named == nil || store.named.Image != nil || store.named.Artist != "PinkPantheress" {
		t.Fatalf("named = %+v, want the names without a picture", store.named)
	}
}
