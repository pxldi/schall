package anchor

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/applemusic"
	"github.com/pxldi/schall/internal/deezer"
	"github.com/pxldi/schall/internal/identity"
	"github.com/pxldi/schall/internal/musicbrainz"
	"github.com/pxldi/schall/internal/tracksource"
)

// A want with no ISRC has no distributor preview, and until now the only other
// reference was an upload found by its name, which never proves a copy on its
// own (ADR 0029). MusicBrainz editors link many recordings and releases to the
// pages that sell or stream them: a Deezer track, an Apple song, a Bandcamp
// album. Each link is an editor tying that page to this recording, which makes
// it a key rather than a search, so the audio behind it can prove a copy.
//
// A link to a release names the album and not the track, so the track is read
// at the recording's place on that release, and only when the store's album
// holds as many tracks as the release does. A length that disagrees with the
// want, or a Deezer ISRC that disagrees with the want's, sets the page aside.
// None of this ranks anything: a page either fits on every count or is not
// read.

// ErrNothingLinked reports that MusicBrainz links no page Schall can take a
// preview from for this recording. It is an answer about the catalogue.
var ErrNothingLinked = errors.New("MusicBrainz links no page with a preview of this track")

// errUnfit is a linked page that turned out not to fit the want: a length, a
// position or a code that disagrees, or no preview behind it. The next link is
// tried.
var errUnfit = errors.New("the linked page does not fit the want")

// maxLinkedReleases bounds how many releases one want's links are read from.
// Each is one more request to MusicBrainz at one a second.
const maxLinkedReleases = 8

// Linked is audio fetched from a page MusicBrainz links the want's recording to.
type Linked struct {
	Audio []byte
	// Reference is the address of the track the audio is from, and Label says in
	// words which store and how the page was reached.
	Reference string
	Label     string
}

// LinkFinder fetches audio from a page MusicBrainz links a recording to.
type LinkFinder interface {
	Find(ctx context.Context, recordingID uuid.UUID, durationMS int, isrc string) (Linked, error)
}

// LinkCatalogue is the part of MusicBrainz the links are read from.
type LinkCatalogue interface {
	RecordingLinks(ctx context.Context, id uuid.UUID) (musicbrainz.RecordingLinks, error)
	ReleaseLinks(ctx context.Context, id uuid.UUID) (musicbrainz.ReleaseLinks, error)
}

// LinkedDeezer reads a Deezer track or album by Deezer's own number.
type LinkedDeezer interface {
	TrackByID(ctx context.Context, id int64) (deezer.Track, error)
	AlbumTracks(ctx context.Context, id int64) ([]deezer.Track, error)
	FetchPreview(ctx context.Context, previewURL string) ([]byte, error)
}

// LinkedApple reads an Apple song or album by Apple's own number.
type LinkedApple interface {
	Song(ctx context.Context, id int64, storefront string) (applemusic.Song, error)
	Album(ctx context.Context, id int64, storefront string) (applemusic.Album, error)
	FetchPreview(ctx context.Context, previewURL string) ([]byte, error)
}

// LinkedTracks reads a track page on Bandcamp, SoundCloud or YouTube.
type LinkedTracks interface {
	Available() bool
	Lookup(ctx context.Context, address string) (tracksource.Track, error)
	AlbumTrackAddresses(ctx context.Context, address string) ([]string, error)
	Excerpt(ctx context.Context, track tracksource.Track) ([]byte, error)
}

// Linker follows a recording's MusicBrainz links to a preview.
type Linker struct {
	catalogue LinkCatalogue
	deezer    LinkedDeezer
	apple     LinkedApple
	tracks    LinkedTracks
}

// NewLinker builds a Linker. tracks may be nil, and then Bandcamp, SoundCloud
// and YouTube links are not read.
func NewLinker(
	catalogue LinkCatalogue, deezer LinkedDeezer, apple LinkedApple, tracks LinkedTracks,
) *Linker {
	return &Linker{catalogue: catalogue, deezer: deezer, apple: apple, tracks: tracks}
}

// want is what a linked page is checked against.
type wantedTrack struct {
	durationMS int
	isrc       string
}

// Find returns the audio of the first linked page that fits the want. Links on
// the recording come first, because they name the track itself; links on its
// releases follow, official releases first.
//
// A page that could not be read this minute is not a page that does not fit,
// so when no page fitted and one could not be read, that failure is returned
// and the want is asked about again later.
func (linker *Linker) Find(
	ctx context.Context, recordingID uuid.UUID, durationMS int, isrc string,
) (Linked, error) {
	if recordingID == uuid.Nil {
		return Linked{}, ErrNothingLinked
	}
	links, err := linker.catalogue.RecordingLinks(ctx, recordingID)
	if errors.Is(err, musicbrainz.ErrNotFound) {
		return Linked{}, ErrNothingLinked
	}
	if err != nil {
		return Linked{}, err
	}
	wanted := wantedTrack{durationMS: durationMS, isrc: strings.TrimSpace(isrc)}

	var unread error
	for _, link := range links.Links {
		linked, err := linker.fromTrackLink(ctx, link.Resource, wanted)
		if err == nil {
			return linked, nil
		}
		if stop, keep := linker.weigh(err); stop {
			return Linked{}, err
		} else if keep {
			unread = err
		}
	}

	for _, placement := range releasesToRead(links.Placements) {
		release, err := linker.catalogue.ReleaseLinks(ctx, placement.ReleaseID)
		if errors.Is(err, musicbrainz.ErrNotFound) {
			continue
		}
		if err != nil {
			return Linked{}, err
		}
		for _, link := range release.Links {
			linked, err := linker.fromReleaseLink(ctx, link.Resource, release, placement, wanted)
			if err == nil {
				return linked, nil
			}
			if stop, keep := linker.weigh(err); stop {
				return Linked{}, err
			} else if keep {
				unread = err
			}
		}
	}
	if unread != nil {
		return Linked{}, unread
	}
	return Linked{}, ErrNothingLinked
}

// weigh sorts what one link came to. A service refusing for load stops the
// search, because every other link to it would meet the same refusal. A page
// that does not fit, or holds nothing, is passed over. Anything else is a page
// that could not be read, which is kept so the want is asked again.
func (linker *Linker) weigh(err error) (stop, keep bool) {
	switch {
	case errors.Is(err, deezer.ErrRateLimited), errors.Is(err, applemusic.ErrRateLimited),
		errors.Is(err, tracksource.ErrRateLimited):
		return true, false
	case errors.Is(err, errUnfit), errors.Is(err, deezer.ErrNoTrack),
		errors.Is(err, deezer.ErrNoPreview), errors.Is(err, applemusic.ErrNotFound),
		errors.Is(err, applemusic.ErrNoPreview), errors.Is(err, tracksource.ErrNotATrack),
		errors.Is(err, tracksource.ErrGone), errors.Is(err, tracksource.ErrUnavailable):
		return false, false
	}
	return false, true
}

// releasesToRead is each release the recording is on once, official releases
// first, at most maxLinkedReleases of them.
func releasesToRead(placements []musicbrainz.Placement) []musicbrainz.Placement {
	seen := make(map[uuid.UUID]bool, len(placements))
	official := make([]musicbrainz.Placement, 0, len(placements))
	other := make([]musicbrainz.Placement, 0)
	for _, placement := range placements {
		if seen[placement.ReleaseID] {
			continue
		}
		seen[placement.ReleaseID] = true
		if strings.EqualFold(placement.Status, "Official") {
			official = append(official, placement)
		} else {
			other = append(other, placement)
		}
	}
	ordered := append(official, other...)
	if len(ordered) > maxLinkedReleases {
		ordered = ordered[:maxLinkedReleases]
	}
	return ordered
}

// fromTrackLink reads a page linked to the recording itself.
func (linker *Linker) fromTrackLink(ctx context.Context, address string, wanted wantedTrack) (Linked, error) {
	page, err := url.Parse(strings.TrimSpace(address))
	if err != nil || page.Host == "" {
		return Linked{}, errUnfit
	}
	host := strings.ToLower(strings.TrimPrefix(page.Hostname(), "www."))
	switch {
	case host == "deezer.com":
		if id, ok := numberAfter(page.Path, "track"); ok {
			track, err := linker.deezer.TrackByID(ctx, id)
			if err != nil {
				return Linked{}, err
			}
			return linker.deezerPreview(ctx, track, wanted, "Deezer track linked to this recording in MusicBrainz")
		}
	case host == "music.apple.com" || host == "itunes.apple.com":
		storefront := storefrontOf(page.Path)
		id, ok := numberAfter(page.Path, "song")
		if !ok {
			id, ok = songInAlbumAddress(page)
		}
		if ok {
			song, err := linker.apple.Song(ctx, id, storefront)
			if err != nil {
				return Linked{}, err
			}
			return linker.applePreview(ctx, song, wanted, "Apple Music song linked to this recording in MusicBrainz")
		}
	case strings.HasSuffix(host, ".bandcamp.com") && strings.Contains(page.Path, "/track/"),
		host == "soundcloud.com" && soundCloudTrack(page.Path),
		isYouTube(host):
		return linker.trackPage(ctx, page.String(), wanted, "linked to this recording in MusicBrainz")
	}
	return Linked{}, errUnfit
}

// fromReleaseLink reads a page linked to one release, at the recording's place
// on it.
func (linker *Linker) fromReleaseLink(
	ctx context.Context, address string, release musicbrainz.ReleaseLinks,
	placement musicbrainz.Placement, wanted wantedTrack,
) (Linked, error) {
	if release.Tracks() == 0 || placement.Medium > len(release.MediaTracks) ||
		placement.Track > release.MediaTracks[placement.Medium-1] {
		return Linked{}, errUnfit
	}
	page, err := url.Parse(strings.TrimSpace(address))
	if err != nil || page.Host == "" {
		return Linked{}, errUnfit
	}
	host := strings.ToLower(strings.TrimPrefix(page.Hostname(), "www."))
	label := fmt.Sprintf("track %d of the release linked in MusicBrainz", placement.Track)
	switch {
	case host == "deezer.com":
		id, ok := numberAfter(page.Path, "album")
		if !ok {
			return Linked{}, errUnfit
		}
		tracks, err := linker.deezer.AlbumTracks(ctx, id)
		if err != nil {
			return Linked{}, err
		}
		if len(tracks) != release.Tracks() {
			return Linked{}, errUnfit
		}
		index := slices.IndexFunc(tracks, func(track deezer.Track) bool {
			return track.Disk == placement.Medium && track.Position == placement.Track
		})
		if index < 0 {
			return Linked{}, errUnfit
		}
		// The album list can leave the code out, so the track is read again on
		// its own before its code is compared.
		track, err := linker.deezer.TrackByID(ctx, tracks[index].ID)
		if err != nil {
			return Linked{}, err
		}
		return linker.deezerPreview(ctx, track, wanted, "Deezer "+label)
	case host == "music.apple.com" || host == "itunes.apple.com":
		id, ok := lastNumber(page.Path)
		if !ok || !strings.Contains(page.Path, "/album/") {
			return Linked{}, errUnfit
		}
		album, err := linker.apple.Album(ctx, id, storefrontOf(page.Path))
		if err != nil {
			return Linked{}, err
		}
		if album.Tracks != release.Tracks() {
			return Linked{}, errUnfit
		}
		index := slices.IndexFunc(album.Songs, func(song applemusic.Song) bool {
			return song.Disc == placement.Medium && song.Track == placement.Track
		})
		if index < 0 {
			return Linked{}, errUnfit
		}
		return linker.applePreview(ctx, album.Songs[index], wanted, "Apple Music "+label)
	case strings.HasSuffix(host, ".bandcamp.com") && strings.Contains(page.Path, "/album/"):
		// Bandcamp numbers an album's tracks straight through, so only a release
		// on one medium can be read by position.
		if linker.tracks == nil || !linker.tracks.Available() || len(release.MediaTracks) != 1 {
			return Linked{}, errUnfit
		}
		addresses, err := linker.tracks.AlbumTrackAddresses(ctx, page.String())
		if err != nil {
			return Linked{}, err
		}
		if len(addresses) != release.Tracks() {
			return Linked{}, errUnfit
		}
		return linker.trackPage(ctx, addresses[placement.Track-1], wanted, label)
	}
	return Linked{}, errUnfit
}

func (linker *Linker) deezerPreview(
	ctx context.Context, track deezer.Track, wanted wantedTrack, label string,
) (Linked, error) {
	if !lengthFits(track.DurationSeconds*1000, wanted.durationMS) {
		return Linked{}, errUnfit
	}
	if wanted.isrc != "" && track.ISRC != "" && !strings.EqualFold(
		strings.ReplaceAll(wanted.isrc, "-", ""), strings.ReplaceAll(track.ISRC, "-", "")) {
		return Linked{}, errUnfit
	}
	audio, err := linker.deezer.FetchPreview(ctx, track.PreviewURL)
	if err != nil {
		return Linked{}, err
	}
	return Linked{
		Audio: audio, Label: label,
		Reference: "https://www.deezer.com/track/" + strconv.FormatInt(track.ID, 10),
	}, nil
}

func (linker *Linker) applePreview(
	ctx context.Context, song applemusic.Song, wanted wantedTrack, label string,
) (Linked, error) {
	if !lengthFits(song.DurationMS, wanted.durationMS) {
		return Linked{}, errUnfit
	}
	audio, err := linker.apple.FetchPreview(ctx, song.PreviewURL)
	if err != nil {
		return Linked{}, err
	}
	return Linked{
		Audio: audio, Label: label,
		Reference: "https://music.apple.com/song/" + strconv.FormatInt(song.ID, 10),
	}, nil
}

func (linker *Linker) trackPage(
	ctx context.Context, address string, wanted wantedTrack, label string,
) (Linked, error) {
	if linker.tracks == nil || !linker.tracks.Available() {
		return Linked{}, errUnfit
	}
	track, err := linker.tracks.Lookup(ctx, address)
	if err != nil {
		return Linked{}, err
	}
	if !lengthFits(track.DurationMS, wanted.durationMS) {
		return Linked{}, errUnfit
	}
	audio, err := linker.tracks.Excerpt(ctx, track)
	if err != nil {
		return Linked{}, err
	}
	return Linked{Audio: audio, Reference: track.URL, Label: storeName(track.Source) + " " + label}, nil
}

// lengthFits reports a linked track whose length does not contradict the
// want's. A length either side does not know is silence.
func lengthFits(linkedMS, wantedMS int) bool {
	if linkedMS <= 0 || wantedMS <= 0 {
		return true
	}
	delta := linkedMS - wantedMS
	if delta < 0 {
		delta = -delta
	}
	return delta <= identity.DurationToleranceMS
}

func storeName(source string) string {
	switch source {
	case tracksource.Bandcamp:
		return "Bandcamp"
	case tracksource.SoundCloud:
		return "SoundCloud"
	case tracksource.YouTube:
		return "YouTube"
	}
	return source
}

// numberAfter reads the number in the path segment after the one named, as in
// /en/track/123 or /album/9.
func numberAfter(path, segment string) (int64, bool) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	for index, part := range parts {
		if part != segment || index+1 >= len(parts) {
			continue
		}
		for _, next := range parts[index+1:] {
			if id, err := strconv.ParseInt(strings.TrimPrefix(next, "id"), 10, 64); err == nil && id > 0 {
				return id, true
			}
		}
	}
	return 0, false
}

// lastNumber reads Apple's number off the end of a path, as in
// /gb/album/currents/1440838039 or /us/album/name/id1440838039.
func lastNumber(path string) (int64, bool) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 0 {
		return 0, false
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(parts[len(parts)-1], "id"), 10, 64)
	return id, err == nil && id > 0
}

// songInAlbumAddress reads the song an Apple album address points into with
// its ?i= parameter.
func songInAlbumAddress(page *url.URL) (int64, bool) {
	if !strings.Contains(page.Path, "/album/") {
		return 0, false
	}
	id, err := strconv.ParseInt(page.Query().Get("i"), 10, 64)
	return id, err == nil && id > 0
}

// storefrontOf reads the two-letter storefront Apple puts first in a path.
func storefrontOf(path string) string {
	first, _, _ := strings.Cut(strings.Trim(path, "/"), "/")
	if len(first) == 2 {
		return first
	}
	return ""
}

func soundCloudTrack(path string) bool {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	return len(parts) == 2 && parts[1] != "sets" && parts[0] != ""
}

func isYouTube(host string) bool {
	return host == "youtube.com" || host == "music.youtube.com" || host == "youtu.be" ||
		host == "m.youtube.com"
}
