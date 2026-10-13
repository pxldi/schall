package anchor

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/applemusic"
	"github.com/pxldi/schall/internal/deezer"
	"github.com/pxldi/schall/internal/musicbrainz"
	"github.com/pxldi/schall/internal/tracksource"
)

type fakeCatalogue struct {
	links        musicbrainz.RecordingLinks
	linksErr     error
	releases     map[uuid.UUID]musicbrainz.ReleaseLinks
	releaseErr   error
	askedRelease []uuid.UUID
}

func (catalogue *fakeCatalogue) RecordingLinks(context.Context, uuid.UUID) (musicbrainz.RecordingLinks, error) {
	return catalogue.links, catalogue.linksErr
}

func (catalogue *fakeCatalogue) ReleaseLinks(_ context.Context, id uuid.UUID) (musicbrainz.ReleaseLinks, error) {
	catalogue.askedRelease = append(catalogue.askedRelease, id)
	if catalogue.releaseErr != nil {
		return musicbrainz.ReleaseLinks{}, catalogue.releaseErr
	}
	release, ok := catalogue.releases[id]
	if !ok {
		return musicbrainz.ReleaseLinks{}, musicbrainz.ErrNotFound
	}
	return release, nil
}

type fakeLinkedDeezer struct {
	tracks   map[int64]deezer.Track
	albums   map[int64][]deezer.Track
	trackErr error
	fetched  []string
}

func (store *fakeLinkedDeezer) TrackByID(_ context.Context, id int64) (deezer.Track, error) {
	if store.trackErr != nil {
		return deezer.Track{}, store.trackErr
	}
	track, ok := store.tracks[id]
	if !ok {
		return deezer.Track{}, deezer.ErrNoTrack
	}
	return track, nil
}

func (store *fakeLinkedDeezer) AlbumTracks(_ context.Context, id int64) ([]deezer.Track, error) {
	tracks, ok := store.albums[id]
	if !ok {
		return nil, deezer.ErrNoTrack
	}
	return tracks, nil
}

func (store *fakeLinkedDeezer) FetchPreview(_ context.Context, address string) ([]byte, error) {
	store.fetched = append(store.fetched, address)
	return []byte("deezer:" + address), nil
}

type fakeLinkedApple struct {
	songs  map[int64]applemusic.Song
	albums map[int64]applemusic.Album
}

func (store *fakeLinkedApple) Song(_ context.Context, id int64, _ string) (applemusic.Song, error) {
	song, ok := store.songs[id]
	if !ok {
		return applemusic.Song{}, applemusic.ErrNotFound
	}
	return song, nil
}

func (store *fakeLinkedApple) Album(_ context.Context, id int64, _ string) (applemusic.Album, error) {
	album, ok := store.albums[id]
	if !ok {
		return applemusic.Album{}, applemusic.ErrNotFound
	}
	return album, nil
}

func (store *fakeLinkedApple) FetchPreview(_ context.Context, address string) ([]byte, error) {
	return []byte("apple:" + address), nil
}

type fakeLinkedTracks struct {
	pages  map[string]tracksource.Track
	albums map[string][]string
}

func (tracks *fakeLinkedTracks) Available() bool { return true }

func (tracks *fakeLinkedTracks) Lookup(_ context.Context, address string) (tracksource.Track, error) {
	track, ok := tracks.pages[address]
	if !ok {
		return tracksource.Track{}, tracksource.ErrGone
	}
	return track, nil
}

func (tracks *fakeLinkedTracks) AlbumTrackAddresses(_ context.Context, address string) ([]string, error) {
	addresses, ok := tracks.albums[address]
	if !ok {
		return nil, tracksource.ErrNotATrack
	}
	return addresses, nil
}

func (tracks *fakeLinkedTracks) Excerpt(_ context.Context, track tracksource.Track) ([]byte, error) {
	return []byte("page:" + track.URL), nil
}

var linkedRelease = uuid.MustParse("6438e9ba-41ad-478c-81f1-323dcb212903")

func link(resource string) musicbrainz.URLRelation {
	return musicbrainz.URLRelation{Type: "free streaming", Resource: resource}
}

func onTrackSeven() []musicbrainz.Placement {
	return []musicbrainz.Placement{{ReleaseID: linkedRelease, Status: "Official", Medium: 1, Track: 7}}
}

func findLinked(t *testing.T, linker *Linker, durationMS int, isrc string) (Linked, error) {
	t.Helper()
	return linker.Find(context.Background(), uuid.New(), durationMS, isrc)
}

func TestADeezerTrackLinkedToTheRecordingIsItsPreview(t *testing.T) {
	store := &fakeLinkedDeezer{tracks: map[int64]deezer.Track{
		103052662: {ID: 103052662, DurationSeconds: 216, PreviewURL: "p"},
	}}
	catalogue := &fakeCatalogue{links: musicbrainz.RecordingLinks{
		Links: []musicbrainz.URLRelation{link("https://www.deezer.com/track/103052662")},
	}}

	linked, err := findLinked(t, NewLinker(catalogue, store, &fakeLinkedApple{}, nil), 216000, "")
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if string(linked.Audio) != "deezer:p" || linked.Reference != "https://www.deezer.com/track/103052662" {
		t.Errorf("linked = %+v", linked)
	}
}

func TestALinkedTrackWhoseLengthContradictsTheWantIsNotRead(t *testing.T) {
	store := &fakeLinkedDeezer{tracks: map[int64]deezer.Track{
		1: {ID: 1, DurationSeconds: 180, PreviewURL: "p"},
	}}
	catalogue := &fakeCatalogue{links: musicbrainz.RecordingLinks{
		Links: []musicbrainz.URLRelation{link("https://www.deezer.com/en/track/1")},
	}}

	_, err := findLinked(t, NewLinker(catalogue, store, &fakeLinkedApple{}, nil), 216000, "")
	if !errors.Is(err, ErrNothingLinked) {
		t.Fatalf("err = %v, want ErrNothingLinked", err)
	}
}

func TestALinkedDeezerTrackUnderAnotherISRCIsNotRead(t *testing.T) {
	store := &fakeLinkedDeezer{tracks: map[int64]deezer.Track{
		1: {ID: 1, DurationSeconds: 216, PreviewURL: "p", ISRC: "AUUM71500999"},
	}}
	catalogue := &fakeCatalogue{links: musicbrainz.RecordingLinks{
		Links: []musicbrainz.URLRelation{link("https://www.deezer.com/track/1")},
	}}

	_, err := findLinked(t, NewLinker(catalogue, store, &fakeLinkedApple{}, nil), 216000, "AUUM71500298")
	if !errors.Is(err, ErrNothingLinked) {
		t.Fatalf("err = %v, want ErrNothingLinked", err)
	}
}

func TestAnAppleSongLinkedToTheRecordingIsItsPreview(t *testing.T) {
	apple := &fakeLinkedApple{songs: map[int64]applemusic.Song{
		1440838488: {ID: 1440838488, DurationMS: 216320, PreviewURL: "a"},
	}}
	catalogue := &fakeCatalogue{links: musicbrainz.RecordingLinks{
		Links: []musicbrainz.URLRelation{link("https://music.apple.com/gb/song/1440838488")},
	}}

	linked, err := findLinked(t, NewLinker(catalogue, &fakeLinkedDeezer{}, apple, nil), 216000, "")
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if string(linked.Audio) != "apple:a" {
		t.Errorf("linked = %+v", linked)
	}
}

func TestADeezerAlbumLinkedToTheReleaseIsReadAtTheRecordingsPlace(t *testing.T) {
	album := make([]deezer.Track, 13)
	for index := range album {
		album[index] = deezer.Track{ID: int64(100 + index), Disk: 1, Position: index + 1}
	}
	store := &fakeLinkedDeezer{
		albums: map[int64][]deezer.Track{9: album},
		tracks: map[int64]deezer.Track{106: {ID: 106, DurationSeconds: 216, PreviewURL: "seven"}},
	}
	catalogue := &fakeCatalogue{
		links: musicbrainz.RecordingLinks{Placements: onTrackSeven()},
		releases: map[uuid.UUID]musicbrainz.ReleaseLinks{linkedRelease: {
			Links: []musicbrainz.URLRelation{link("https://www.deezer.com/album/9")}, MediaTracks: []int{13},
		}},
	}

	linked, err := findLinked(t, NewLinker(catalogue, store, &fakeLinkedApple{}, nil), 216000, "")
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if linked.Reference != "https://www.deezer.com/track/106" {
		t.Errorf("read %q, want track 7 of the album", linked.Reference)
	}
}

// An album with a bonus track is another edition, and its seventh track may be
// some other song.
func TestALinkedAlbumHoldingAnotherNumberOfTracksIsNotRead(t *testing.T) {
	album := make([]deezer.Track, 14)
	for index := range album {
		album[index] = deezer.Track{ID: int64(100 + index), Disk: 1, Position: index + 1}
	}
	store := &fakeLinkedDeezer{
		albums: map[int64][]deezer.Track{9: album},
		tracks: map[int64]deezer.Track{106: {ID: 106, DurationSeconds: 216, PreviewURL: "seven"}},
	}
	catalogue := &fakeCatalogue{
		links: musicbrainz.RecordingLinks{Placements: onTrackSeven()},
		releases: map[uuid.UUID]musicbrainz.ReleaseLinks{linkedRelease: {
			Links: []musicbrainz.URLRelation{link("https://www.deezer.com/album/9")}, MediaTracks: []int{13},
		}},
	}

	_, err := findLinked(t, NewLinker(catalogue, store, &fakeLinkedApple{}, nil), 216000, "")
	if !errors.Is(err, ErrNothingLinked) {
		t.Fatalf("err = %v, want ErrNothingLinked", err)
	}
}

func TestAnAppleAlbumLinkedToTheReleaseIsReadAtTheRecordingsPlace(t *testing.T) {
	songs := make([]applemusic.Song, 13)
	for index := range songs {
		songs[index] = applemusic.Song{ID: int64(200 + index), Disc: 1, Track: index + 1, PreviewURL: "x"}
	}
	songs[6].PreviewURL = "seven"
	apple := &fakeLinkedApple{albums: map[int64]applemusic.Album{1440838039: {Tracks: 13, Songs: songs}}}
	catalogue := &fakeCatalogue{
		links: musicbrainz.RecordingLinks{Placements: onTrackSeven()},
		releases: map[uuid.UUID]musicbrainz.ReleaseLinks{linkedRelease: {
			Links:       []musicbrainz.URLRelation{link("https://music.apple.com/gb/album/currents/1440838039")},
			MediaTracks: []int{13},
		}},
	}

	linked, err := findLinked(t, NewLinker(catalogue, &fakeLinkedDeezer{}, apple, nil), 216000, "")
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if string(linked.Audio) != "apple:seven" {
		t.Errorf("read %q, want track 7 of the album", linked.Audio)
	}
}

func TestABandcampAlbumLinkedToTheReleaseIsReadAtTheRecordingsPlace(t *testing.T) {
	addresses := make([]string, 13)
	for index := range addresses {
		addresses[index] = "https://x.bandcamp.com/track/" + string(rune('a'+index))
	}
	tracks := &fakeLinkedTracks{
		albums: map[string][]string{"https://x.bandcamp.com/album/y": addresses},
		pages: map[string]tracksource.Track{addresses[6]: {
			Source: tracksource.Bandcamp, URL: addresses[6], DurationMS: 216500,
		}},
	}
	catalogue := &fakeCatalogue{
		links: musicbrainz.RecordingLinks{Placements: onTrackSeven()},
		releases: map[uuid.UUID]musicbrainz.ReleaseLinks{linkedRelease: {
			Links: []musicbrainz.URLRelation{link("https://x.bandcamp.com/album/y")}, MediaTracks: []int{13},
		}},
	}

	linked, err := findLinked(t, NewLinker(catalogue, &fakeLinkedDeezer{}, &fakeLinkedApple{}, tracks), 216000, "")
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if linked.Reference != addresses[6] {
		t.Errorf("read %q, want %q", linked.Reference, addresses[6])
	}
}

func TestARecordingWithNoLinksHasNothingLinked(t *testing.T) {
	_, err := findLinked(t, NewLinker(&fakeCatalogue{}, &fakeLinkedDeezer{}, &fakeLinkedApple{}, nil), 216000, "")
	if !errors.Is(err, ErrNothingLinked) {
		t.Fatalf("err = %v, want ErrNothingLinked", err)
	}
}

// A store that could not be reached has said nothing about the track, so the
// want is asked about again rather than written off.
func TestAPageThatCouldNotBeReadIsNotReadAsNothingLinked(t *testing.T) {
	store := &fakeLinkedDeezer{trackErr: errors.New("connection reset")}
	catalogue := &fakeCatalogue{links: musicbrainz.RecordingLinks{
		Links: []musicbrainz.URLRelation{link("https://www.deezer.com/track/1")},
	}}

	_, err := findLinked(t, NewLinker(catalogue, store, &fakeLinkedApple{}, nil), 216000, "")
	if err == nil || errors.Is(err, ErrNothingLinked) {
		t.Fatalf("err = %v, want the failure itself", err)
	}
}
