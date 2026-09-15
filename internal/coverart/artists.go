package coverart

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/musicbrainz"
)

const (
	// NameCommons says the picture came from Wikimedia Commons, by way of an
	// encyclopaedia's statement about who this is.
	NameCommons = "wikimedia"
	// NameDeezer says the picture came from the artist's own page on a streaming
	// service, which is where anybody without an encyclopaedia entry has one.
	NameDeezer = "deezer"
	// NameAppleMusic says the picture came from the artist's page on the other
	// streaming service MusicBrainz links by identifier. It is not NameITunes,
	// which is the same company answering a text search about a release: that one
	// is a guess and recorded as one, and this one is the page MusicBrainz says is
	// this artist's.
	NameAppleMusic = "applemusic"
	// NameBandcamp and NameSoundCloud say the picture came from the page the
	// artist keeps for themself, which is where anybody no streaming service has
	// ever carried has one.
	NameBandcamp   = "bandcamp"
	NameSoundCloud = "soundcloud"
	// NameNobody is what an absence is recorded under: every rung was asked and
	// none of them had a picture. It exists because the column is NOT NULL and
	// the source has to say something — and what it used to say was whichever
	// service happened to be first in the chain, so a row nobody answered about
	// read as though Wikimedia had looked and declined.
	NameNobody = "none"
)

const (
	// commonsFilePath is Commons' own way of asking for the file behind a file
	// page. It redirects to wherever the bytes actually live, which is a detail
	// that has changed over the years and is not worth encoding here.
	commonsFilePath = "https://commons.wikimedia.org/wiki/Special:FilePath/"
	// commonsWidth asks for a rendition rather than the original. Commons holds
	// photographs at whatever the photographer uploaded — the first artist
	// pictured this way arrived as six megabytes for a thumbnail beside a name —
	// and the width is the same lever the release covers pull at 500 pixels.
	commonsWidth = "?width=500"
	// wikidataEntity is the plain-JSON view of one Wikidata entity.
	wikidataEntity = "https://www.wikidata.org/wiki/Special:EntityData/"
	// imageProperty is Wikidata's "image" statement.
	imageProperty = "P18"
	// deezerArtist is the public description of one artist, which needs no key
	// and includes the picture that artist's own page shows.
	deezerArtist = "https://api.deezer.com/artist/"
	// deezerBlank and deezerBlankHash are what a picture address looks like when
	// there is no picture: the address is built from the image's hash, and there
	// is no image. Deezer names the hash of nothing rather than leaving the
	// segment empty, so the address in practice carries the MD5 of the empty
	// string; the empty segment is the older shape and both are refused. Deezer
	// serves a grey silhouette at either rather than a 404, and a grey silhouette
	// cached forever is worse than no face at all.
	deezerBlank     = "/artist//"
	deezerBlankHash = "d41d8cd98f00b204e9800998ecf8427e"
	// openGraphImage is the one thing read out of a page: the tag by which a page
	// says which picture represents it.
	openGraphImage = "og:image"
	// bandcampImages and soundcloudImages are where those two services serve the
	// pictures their pages declare. A page is believed about which picture is of
	// the artist, because it is the artist's own page; it is not believed about
	// where Schall should go, so an address it declares that the service is not
	// itself serving is passed over rather than fetched.
	bandcampImages   = "bcbits.com"
	soundcloudImages = "sndcdn.com"
	// appleMusicHost is where Apple Music keeps an artist's page, and
	// appleStoreHost is where it used to: MusicBrainz holds both, the older one
	// under "purchase for download", and it redirects to the newer.
	appleMusicHost = "music.apple.com"
	appleStoreHost = "itunes.apple.com"
	// appleStore is the storefront asked when a link names none. An artist page is
	// per country, and the link MusicBrainz holds says which country's catalogue
	// the artist is in; where it says nothing, the largest one is asked rather
	// than none at all.
	appleStore = "us"
	// appleImages is where Apple serves the pictures its pages declare.
	appleImages = "mzstatic.com"
	// appleSquareSize is the rendition asked for instead of the one an Apple page
	// declares. That address is the picture with its size written into the last
	// segment — the same lever the iTunes cover search pulls at 600x600 — and what
	// a page declares is a 1200x630 crop meant for a link preview, which is a
	// banner rather than a face. "bb" fits the picture into the square where "cw"
	// crops to the shape asked for. It is a display size and nothing more: the
	// picture is the same picture either way.
	appleSquareSize = "600x600bb.jpg"
)

// chainChanged is the day the chain below last gained or changed a rung, and
// bumping it is the whole ritual for adding one.
//
// A row in artist_images with no image is the answer "asked, and nobody had a
// picture of them". That answers the chain that asked, and stops being an answer
// the moment the chain grows: an artist written off before a rung existed would
// otherwise never be listed as missing a picture again, so the new rung would
// picture nobody who needed it. Adding the last rung took a migration deleting
// every recorded absence by hand (00043) to get around that. This is that
// migration written once — ArtistsMissingImage compares fetched_at against this
// date, so an absence older than the chain is asked again and a newer one is
// left alone.
//
// A date rather than a version because a date is what fetched_at is. Nothing
// records which chain answered, and when one changed is enough to tell an answer
// from before it from an answer from after.
//
// It carries an hour, and that is not fussiness. Midnight on the day the rung
// ships is the obvious value and it is wrong whenever the old chain also ran
// that day: this one was set to 2026-08-04 00:00, hours after migration 00043
// had cleared the recorded absences and the old chain had promptly written them
// again at 17:20 UTC. Those absences were newer than the constant meant to
// invalidate them, so the Apple rung shipped and reached nobody — the same
// failure it exists to prevent, wearing a different hat.
//
// So the value is a moment after the last absence the previous chain wrote, and
// it must be in the past when the code runs. A time still to come would make
// every absence written before it stale on the spot, and the sweep would ask,
// record, find its own answer stale and ask again until the clock caught up.
var chainChanged = time.Date(2026, 8, 4, 18, 0, 0, 0, time.UTC)

// ImageRelations is the part of the metadata provider that says what an artist is
// linked to. It is deliberately this narrow: a picture is display, and nothing
// that decides anything is reachable from here.
type ImageRelations interface {
	ArtistURLRelations(context.Context, uuid.UUID) ([]musicbrainz.URLRelation, error)
}

// Artists fetches the picture of an artist.
//
// It is identifier-keyed like the release archive and unlike the name search: the
// chain starts at the artist MusicBrainz ID the catalogue already holds, and every
// step is one service's own statement about the entity the step before named. So
// there is never a question of whose face this is.
//
// MusicBrainz itself stores no images. Occasionally it names a Commons file
// outright; far more often it names the artist's Wikidata entity, and the picture
// is that entity's "image" statement. Both are followed, in that order, and both
// end at Commons.
//
// Failing that, MusicBrainz names the artist's page on a streaming service, and
// the picture is the one that page shows. That rung exists because the
// encyclopaedia route only pictures artists an encyclopaedia has heard of: a band
// with a Wikipedia article is pictured at once, and an underground producer with
// nine releases and a Bandcamp is not, which is most of a collection like this
// one. A streaming service has a page for both.
//
// Failing that again, MusicBrainz names the artist's page on Apple Music, and the
// picture is the one that page declares. Apple publishes no free description of
// an artist to ask, so this rung reads a page where Deezer's reads a field, which
// is why it sits underneath — but what an Apple artist page declares is an artist
// image the service holds for them, which is the same kind of thing Deezer
// answers with and not the same kind of thing a page below declares.
//
// Failing even that, MusicBrainz names the page the artist keeps for themself,
// and the picture is the one that page declares as its own. That rung exists
// because a streaming service has a page only for artists it carries: the ones
// left faceless here put their music on Bandcamp and SoundCloud, which is what
// MusicBrainz has for them and all it has for them. It is last because what such
// a page declares is whatever the artist last put out, which is as often a sleeve
// as a face.
//
// Nothing else is followed. Fetching an arbitrary address because a database
// mentioned it is a different thing from asking a known service for a file, and
// only the second is worth doing for a picture. The page rung reads one tag out
// of one page, and goes to the address that tag names only while the service is
// serving it itself.
type Artists struct {
	relations ImageRelations
	images    *Client
}

func NewArtists(relations ImageRelations, images *Client) *Artists {
	return &Artists{relations: relations, images: images}
}

// Picture returns the artist's image, or ErrNoArtwork when nothing names one that
// can be fetched.
func (artists *Artists) Picture(ctx context.Context, artistID uuid.UUID) (Artwork, error) {
	if artistID == uuid.Nil {
		return Artwork{}, ErrNoArtwork
	}
	found, err := artists.relations.ArtistURLRelations(ctx, artistID)
	if err != nil {
		return Artwork{}, err
	}

	for _, relation := range found {
		if relation.Type != "image" {
			continue
		}
		if fileURL, ok := commonsFileURL(relation.Resource); ok {
			artwork, err := artists.fetch(ctx, fileURL, NameCommons)
			if err == nil {
				return artwork, nil
			}
			if !errors.Is(err, ErrNoArtwork) {
				return Artwork{}, err
			}
		}
	}

	for _, relation := range found {
		if relation.Type != "wikidata" {
			continue
		}
		fileName, err := artists.wikidataImage(ctx, relation.Resource)
		if errors.Is(err, ErrNoArtwork) {
			continue
		}
		if err != nil {
			return Artwork{}, err
		}
		artwork, err := artists.fetch(
			ctx, commonsFilePath+url.PathEscape(fileName)+commonsWidth, NameCommons)
		if errors.Is(err, ErrNoArtwork) {
			continue
		}
		if err != nil {
			return Artwork{}, err
		}
		return artwork, nil
	}

	for _, relation := range found {
		artistID, ok := deezerArtistID(relation.Resource)
		if !ok {
			continue
		}
		pictureURL, err := artists.deezerPicture(ctx, artistID)
		if errors.Is(err, ErrNoArtwork) {
			continue
		}
		if err != nil {
			return Artwork{}, err
		}
		artwork, err := artists.fetch(ctx, pictureURL, NameDeezer)
		if errors.Is(err, ErrNoArtwork) {
			continue
		}
		if err != nil {
			return Artwork{}, err
		}
		return artwork, nil
	}

	artwork, err := artists.fromPages(ctx, found, appleArtistPage)
	if err == nil {
		return artwork, nil
	}
	if !errors.Is(err, ErrNoArtwork) {
		return Artwork{}, err
	}

	artwork, err = artists.fromPages(ctx, found, artistPage)
	if err == nil {
		return artwork, nil
	}
	if !errors.Is(err, ErrNoArtwork) {
		return Artwork{}, err
	}
	return Artwork{}, ErrNoArtwork
}

// fromPages walks the relations one kind of page recognises, and returns the
// first picture one of those pages declares.
//
// A page that will not answer is passed over rather than reported, which is what
// lets the next page and the next rung be reached; only something that is not an
// answer at all — a service failing, or asking us to slow down — stops the chain.
func (artists *Artists) fromPages(
	ctx context.Context, found []musicbrainz.URLRelation,
	recognise func(string) (artistLink, bool),
) (Artwork, error) {
	asked := make(map[string]bool)
	for _, relation := range found {
		link, ok := recognise(relation.Resource)
		if !ok {
			continue
		}
		// One artist can be linked several times over to the same page: Apple has
		// a URL per storefront and MusicBrainz keeps every one it is told about,
		// so the artist whose face this rung was written for arrives twice. Those
		// are one page declaring one picture, and asking a service the same
		// question again once it has answered is only rudeness.
		if asked[link.artist] {
			continue
		}
		asked[link.artist] = true
		pictureURL, err := artists.pagePicture(ctx, link.page, link.imageHost)
		if errors.Is(err, ErrNoArtwork) {
			continue
		}
		if err != nil {
			return Artwork{}, err
		}
		artwork, err := artists.fetch(ctx, squareAppleImage(pictureURL), link.source)
		if errors.Is(err, ErrNoArtwork) {
			continue
		}
		if err != nil {
			return Artwork{}, err
		}
		return artwork, nil
	}
	return Artwork{}, ErrNoArtwork
}

func (artists *Artists) fetch(ctx context.Context, address, source string) (Artwork, error) {
	artwork, err := artists.images.fetch(ctx, artists.images.httpClient, address, maxImageBytes)
	if err != nil {
		return Artwork{}, err
	}
	artwork.Source = source
	return artwork, nil
}

// deezerPicture asks Deezer what picture is on this artist's page. The artist is
// the one MusicBrainz linked to, so the page is not being searched for by name —
// it is the page MusicBrainz says is theirs.
//
// The size asked for is the same 500 pixels the other two sources are asked for,
// and for the same reason: this is a thumbnail beside a name.
func (artists *Artists) deezerPicture(ctx context.Context, artistID string) (string, error) {
	response, err := artists.images.get(ctx, artists.images.httpClient, deezerArtist+artistID)
	if err != nil {
		return "", err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxImageBytes))
		_ = response.Body.Close()
	}()
	if response.StatusCode == http.StatusNotFound {
		return "", ErrNoArtwork
	}
	if response.StatusCode != http.StatusOK {
		return "", errors.New("Deezer returned " + response.Status)
	}
	var payload struct {
		// Deezer reports a missing artist in the body of a 200, so an error here
		// is an answer about the artist rather than a failure of the request.
		Error   *json.RawMessage `json:"error"`
		Picture string           `json:"picture_big"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxImageBytes)).Decode(&payload); err != nil {
		return "", err
	}
	if payload.Error != nil {
		return "", ErrNoArtwork
	}
	picture := strings.TrimSpace(payload.Picture)
	if picture == "" ||
		strings.Contains(picture, deezerBlank) ||
		strings.Contains(picture, deezerBlankHash) {
		return "", ErrNoArtwork
	}
	return picture, nil
}

// deezerArtistID reads the artist number out of a Deezer address, and refuses
// anything that is not one. The relation type is not consulted: MusicBrainz files
// Deezer under "free streaming" alongside every other service, so what identifies
// the link is the address.
func deezerArtistID(resource string) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(resource))
	if err != nil || !strings.HasSuffix(parsed.Host, "deezer.com") {
		return "", false
	}
	// The path may carry a language prefix, as /en/artist/123 does.
	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(segments) < 2 || segments[len(segments)-2] != "artist" {
		return "", false
	}
	artistID := segments[len(segments)-1]
	if artistID == "" {
		return "", false
	}
	for _, symbol := range artistID {
		if symbol < '0' || symbol > '9' {
			return "", false
		}
	}
	return artistID, true
}

// pagePicture asks the artist's own page which picture is of them. The page is
// the one MusicBrainz linked, so it is nobody's page but theirs, and the answer
// is the one tag by which a page says what picture represents it. Nothing else on
// the page is read and no other address on it is followed.
//
// On Bandcamp that picture is usually a photograph of the band and is sometimes
// the cover of their latest release. There is no way to tell a face from a cover
// that is not a guess, so neither is guessed at: what the page declares is what
// is shown, and a cover is still a picture of that artist's work. This rung is
// reached only where the encyclopaedia and the streaming service both had
// nothing.
func (artists *Artists) pagePicture(ctx context.Context, page, imageHost string) (string, error) {
	response, err := artists.images.get(ctx, artists.images.httpClient, page)
	if err != nil {
		return "", err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxImageBytes))
		_ = response.Body.Close()
	}()
	// Anything the service says about the request itself is an answer, not a
	// failure, and the answer is that this page will not tell us. A 404 is the
	// page being gone; a 403 is the service declining to serve whoever is asking,
	// which is what Bandcamp does to a datacentre address however politely the
	// request identifies itself — the same page answers 200 from a home
	// connection. Reporting either as a failure stopped the chain before
	// SoundCloud was reached and left the sweep retrying an answer that will not
	// change, which is both useless and rude at one request every ten seconds.
	//
	// A 5xx is the service failing rather than answering, and stays a failure so
	// it is retried and never written down as an absence. So does 429, which is
	// the one 4xx that means "not now" rather than "not you": recording an
	// absence because we asked too quickly would make our own haste permanent.
	if response.StatusCode >= 400 && response.StatusCode < 500 &&
		response.StatusCode != http.StatusTooManyRequests {
		return "", ErrNoArtwork
	}
	if response.StatusCode != http.StatusOK {
		return "", errors.New(page + " returned " + response.Status)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxImageBytes))
	if err != nil {
		return "", fmt.Errorf("read %s: %w", page, err)
	}
	picture, ok := ogImage(string(body))
	if !ok {
		return "", ErrNoArtwork
	}
	parsed, err := url.Parse(picture)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return "", ErrNoArtwork
	}
	host := strings.ToLower(parsed.Hostname())
	if host != imageHost && !strings.HasSuffix(host, "."+imageHost) {
		return "", ErrNoArtwork
	}
	return picture, nil
}

// artistLink is one page MusicBrainz links an artist to: where to ask, where that
// service serves the pictures its pages declare, what to call an answer from it,
// and which artist the page is of. The last is separate from the address because
// one artist can be linked more than once, and their pages are then all the same
// page and all declare the same picture.
type artistLink struct {
	page      string
	imageHost string
	source    string
	artist    string
}

// artistPage recognises the page an artist keeps for themself out of a relation,
// and says where that service serves its pictures from. As with Deezer the
// relation type is not what decides it: MusicBrainz does have a type for each of
// these, but a type is a word somebody chose on a form and the address is what
// the link actually is.
//
// The page is what identifies the artist here, because on both of these services
// an artist is their page.
func artistPage(resource string) (artistLink, bool) {
	parsed, err := url.Parse(strings.TrimSpace(resource))
	if err != nil {
		return artistLink{}, false
	}
	host := strings.ToLower(parsed.Hostname())
	segments := pathSegments(parsed.Path)
	switch {
	// A Bandcamp artist has a subdomain to themselves and their page is its root.
	// Anything deeper is one release, whose picture is that release's cover and
	// not a picture of the artist at all.
	case strings.HasSuffix(host, ".bandcamp.com") && host != "www.bandcamp.com" && len(segments) == 0:
		page := "https://" + host + "/"
		return artistLink{page, bandcampImages, NameBandcamp, page}, true
	// A SoundCloud artist is one segment under the site. Anything deeper is one
	// track or one set.
	case (host == "soundcloud.com" || strings.HasSuffix(host, ".soundcloud.com")) && len(segments) == 1:
		page := "https://soundcloud.com/" + segments[0]
		return artistLink{page, soundcloudImages, NameSoundCloud, page}, true
	}
	return artistLink{}, false
}

// appleArtistPage recognises Apple Music's page for an artist. Both of Apple's
// hosts are read, because MusicBrainz holds both: the newer writes the artist as
// music.apple.com/us/artist/1665306505, sometimes with their name as a segment
// before the number, and the older as itunes.apple.com/jp/artist/id1113388221,
// which is the same number written the way that store wrote them.
//
// The address asked for is rebuilt from the number rather than followed as given,
// the way every rung here is built from an identifier: a link may carry a name
// segment, a query, or the older host that only redirects to the newer one, and
// none of that is what says who this is. The number is, and the artist is that
// number — not the storefront it was linked under, so the same artist linked in
// two countries is one page to ask about.
func appleArtistPage(resource string) (artistLink, bool) {
	parsed, err := url.Parse(strings.TrimSpace(resource))
	if err != nil {
		return artistLink{}, false
	}
	if host := strings.ToLower(parsed.Hostname()); host != appleMusicHost && host != appleStoreHost {
		return artistLink{}, false
	}
	segments := pathSegments(parsed.Path)
	last := len(segments) - 1
	// The number is last, under an "artist" segment that a name may sit between.
	if last < 1 || (segments[last-1] != "artist" && (last < 2 || segments[last-2] != "artist")) {
		return artistLink{}, false
	}
	artistID := strings.TrimPrefix(segments[last], "id")
	if !onlyDigits(artistID) {
		return artistLink{}, false
	}
	store := appleStore
	if front := strings.ToLower(segments[0]); len(front) == 2 && onlyLetters(front) {
		store = front
	}
	return artistLink{
		page:      "https://" + appleMusicHost + "/" + store + "/artist/" + artistID,
		imageHost: appleImages,
		source:    NameAppleMusic,
		artist:    NameAppleMusic + ":" + artistID,
	}, true
}

// appleSizedSegment is the last segment of an address on Apple's image host when
// it carries a size: two numbers, the two-letter code for how the picture was
// fitted into them, and the format.
var appleSizedSegment = regexp.MustCompile(`(?i)^[0-9]{2,4}x[0-9]{2,4}[a-z]{2}\.(?:png|jpe?g)$`)

// squareAppleImage asks Apple's image host for the square rendition of the
// picture an Apple page declares, and leaves every other address exactly as
// declared.
//
// A page declares its picture for a link preview, so Apple serves a wide crop of
// it, which is the wrong shape for a face beside a name. The size lives in the
// last segment of the address and the host renders whichever is asked for, so a
// square is the same picture asked for differently. An address not written in
// that shape is fetched as it is: this is a display size and not evidence, and
// guessing at an address Apple has not shown us it serves would be a fetch of
// something we were never told about.
func squareAppleImage(picture string) string {
	parsed, err := url.Parse(picture)
	if err != nil {
		return picture
	}
	host := strings.ToLower(parsed.Hostname())
	if host != appleImages && !strings.HasSuffix(host, "."+appleImages) {
		return picture
	}
	cut := strings.LastIndex(picture, "/")
	if cut < 0 || !appleSizedSegment.MatchString(picture[cut+1:]) {
		return picture
	}
	return picture[:cut+1] + appleSquareSize
}

func onlyDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, symbol := range value {
		if symbol < '0' || symbol > '9' {
			return false
		}
	}
	return true
}

func onlyLetters(value string) bool {
	if value == "" {
		return false
	}
	for _, symbol := range value {
		if symbol < 'a' || symbol > 'z' {
			return false
		}
	}
	return true
}

func pathSegments(path string) []string {
	trimmed := strings.Trim(path, "/")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "/")
}

var (
	// metaTags finds the elements by which a page describes itself. They are the
	// only part of a page that is looked at.
	metaTags = regexp.MustCompile(`(?is)<meta\s[^>]*>`)
	// metaAttributes reads the quoted attributes out of one of them. A value
	// written without quotes is not read at all: neither service writes one, and
	// a picture that cannot be read is an absence, which is the harmless end of
	// this.
	metaAttributes = regexp.MustCompile(`(?is)([a-z][a-z0-9:._-]*)\s*=\s*(?:"([^"]*)"|'([^']*)')`)
)

// ogImage reads the picture a page declares as its own, and reads nothing else
// out of the page.
//
// The tag is found rather than parsed: an HTML document is a great deal of shape
// to take on for one attribute of one element, and everything this needs to tell
// apart is told apart by exact names — og:image is the picture and og:image:width
// is a number about it.
func ogImage(page string) (string, bool) {
	for _, tag := range metaTags.FindAllString(page, -1) {
		var property, content string
		for _, attribute := range metaAttributes.FindAllStringSubmatch(tag, -1) {
			value := attribute[2] + attribute[3]
			switch strings.ToLower(attribute[1]) {
			case "property", "name":
				property = strings.ToLower(strings.TrimSpace(value))
			case "content":
				content = strings.TrimSpace(value)
			}
		}
		if property != openGraphImage || content == "" {
			continue
		}
		return html.UnescapeString(content), true
	}
	return "", false
}

// wikidataImage asks Wikidata what picture is of this entity. The entity is the
// one MusicBrainz named, and the picture is the one Wikidata names: two
// statements, no guessing between them.
func (artists *Artists) wikidataImage(ctx context.Context, resource string) (string, error) {
	entity, ok := wikidataEntityID(resource)
	if !ok {
		return "", ErrNoArtwork
	}
	response, err := artists.images.get(ctx, artists.images.httpClient, wikidataEntity+entity+".json")
	if err != nil {
		return "", err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxImageBytes))
		_ = response.Body.Close()
	}()
	if response.StatusCode == http.StatusNotFound {
		return "", ErrNoArtwork
	}
	if response.StatusCode != http.StatusOK {
		return "", errors.New("Wikidata returned " + response.Status)
	}
	var payload wikidataEntities
	if err := json.NewDecoder(io.LimitReader(response.Body, maxImageBytes)).Decode(&payload); err != nil {
		return "", err
	}
	for _, claim := range payload.Entities[entity].Claims[imageProperty] {
		var name string
		if err := json.Unmarshal(claim.MainSnak.DataValue.Value, &name); err != nil {
			continue
		}
		if name = strings.TrimSpace(name); name != "" {
			return name, nil
		}
	}
	return "", ErrNoArtwork
}

// commonsFileURL turns a Commons file page into the file itself. Anything that is
// not a Commons file page is not turned into anything.
func commonsFileURL(resource string) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(resource))
	if err != nil || !strings.HasSuffix(parsed.Host, "commons.wikimedia.org") {
		return "", false
	}
	name := strings.TrimPrefix(parsed.Path, "/wiki/")
	if !strings.HasPrefix(name, "File:") {
		return "", false
	}
	return commonsFilePath + url.PathEscape(strings.TrimPrefix(name, "File:")) + commonsWidth, true
}

// wikidataEntityID reads the Q-number out of a Wikidata address, and refuses
// anything that is not one.
func wikidataEntityID(resource string) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(resource))
	if err != nil || !strings.HasSuffix(parsed.Host, "wikidata.org") {
		return "", false
	}
	entity := strings.TrimPrefix(parsed.Path, "/wiki/")
	if len(entity) < 2 || entity[0] != 'Q' {
		return "", false
	}
	for _, symbol := range entity[1:] {
		if symbol < '0' || symbol > '9' {
			return "", false
		}
	}
	return entity, true
}

// wikidataEntities is one entity as Wikidata prints it. A claim's value is left
// undecoded on purpose: an entity carries claims of every kind, and their values
// are strings, objects and numbers by turns — reading them all as one shape fails
// on the first statement that is not an image, which is how the second deployment
// of artist pictures failed. Only the image statement is ever decoded, and only
// as what an image statement is.
type wikidataEntities struct {
	Entities map[string]struct {
		Claims map[string][]struct {
			MainSnak struct {
				DataValue struct {
					Value json.RawMessage `json:"value"`
				} `json:"datavalue"`
			} `json:"mainsnak"`
		} `json:"claims"`
	} `json:"entities"`
}
