// Package coverart fetches the picture of a release from the Cover Art Archive.
//
// It is the one kind of external data in Schall that decides nothing. Artwork is
// display: it is keyed by a MusicBrainz release or release group that identity
// resolution already settled, so there is no guess about which release it belongs
// to, and nothing here is read by matching, by identity, or by any import grader.
// A picture cannot say what a file is, and none of it may ever be treated as
// though it could.
//
// The archive answers by identifier and only by identifier. There is deliberately
// no search: a cover found by searching an artist and title string would be the
// fuzzy text agreement the rest of Schall exists to refuse, and a wrong picture is
// harmless right up until somebody believes it.
package coverart

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/gif"  // registered so the formats an archive may answer in all decode
	_ "image/jpeg" // the same, and the one almost every cover arrives as
	_ "image/png"  // the same
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	_ "golang.org/x/image/bmp"  // the same, for the formats the standard library leaves out
	_ "golang.org/x/image/webp" // the same
)

// ErrNoArtwork reports that the archive has no front cover for this release. It
// is an answer about the release rather than a failure, so a caller can record it
// and stop asking.
var ErrNoArtwork = errors.New("the Cover Art Archive has no front cover for this release")

const (
	// Name says which archive answered, recorded beside what it returned.
	Name = "coverartarchive"

	defaultBaseURL = "https://coverartarchive.org"
	defaultTimeout = 10 * time.Second
	// fetchAttempts is how many times one address is asked before the answer is
	// believed to be a failure. The Cover Art Archive answers 500 intermittently
	// for pictures it holds — the same URL returned 500, 200, 200, 500, 500 to
	// five requests one after another — so a single 500 says nothing about
	// whether the picture exists.
	//
	// Only a 5xx is asked again. Those come back in milliseconds, so three
	// attempts cost a fraction of one request's timeout, where a transport
	// failure has already spent the whole of it and asking again inside the
	// budget one release is given would push the pass past it.
	fetchAttempts = 3
	// betweenAttempts is the pause between them. Long enough that a server
	// shedding load is not asked three times in one instant, short enough that
	// a release which simply has no picture is still answered about quickly.
	betweenAttempts = 500 * time.Millisecond
	// The two sizes the archive is asked for, and what a download of each may
	// weigh. The 500-pixel front cover is tens of kilobytes; anything approaching
	// its bound is not the picture that was asked for. The full-size front is the
	// scan itself and runs to a few megabytes, so its bound is wider — wide
	// enough for a real cover and narrow enough that one release cannot fill a
	// disc on its own.
	thumbnail         = "front-500"
	fullSize          = "front"
	maxImageBytes     = 8 << 20
	maxFullImageBytes = 32 << 20
)

// Artwork is one image, as bytes to be stored and served by Schall itself rather
// than as a URL for a browser to go and fetch.
type Artwork struct {
	Image       []byte
	ContentType string
	Source      string
}

type Options struct {
	BaseURL string
	// UserAgent identifies this installation to the archive, which asks for one
	// the same way MusicBrainz does.
	UserAgent  string
	HTTPClient *http.Client
	// AllowLocalPictureAddresses lifts the guard that keeps FromURL — the one
	// place an address comes from a person rather than from an archive — from
	// reaching this machine or this network.
	//
	// It exists for tests, which serve their picture from a loopback address.
	// Nothing in Schall sets it, and nothing should: what it turns off is the
	// only thing standing between a text field and the router's admin page.
	AllowLocalPictureAddresses bool
}

type Client struct {
	baseURL    string
	userAgent  string
	httpClient *http.Client
	// pictureClient is the guarded client FromURL uses, and only FromURL. The
	// archive's own fetches keep the client above: they go to one address that
	// this installation was configured with, not to one somebody typed.
	pictureClient *http.Client
	// betweenAttempts is a field rather than the constant so a test does not
	// wait out the real pause.
	betweenAttempts time.Duration
}

func NewClient(options Options) *Client {
	client := &Client{
		baseURL:         strings.TrimRight(strings.TrimSpace(options.BaseURL), "/"),
		userAgent:       strings.TrimSpace(options.UserAgent),
		httpClient:      options.HTTPClient,
		betweenAttempts: betweenAttempts,
	}
	if client.baseURL == "" {
		client.baseURL = defaultBaseURL
	}
	if client.httpClient == nil {
		client.httpClient = &http.Client{Timeout: defaultTimeout}
	}
	client.pictureClient = guarded(defaultTimeout, options.AllowLocalPictureAddresses)
	return client
}

// Front fetches the front cover of a release, falling back to the release group
// when the edition Schall settled on has no picture of its own.
//
// Both identifiers are ones the catalogue already holds. A release group is not a
// guess either: it is the same work, and the archive's group cover is the cover of
// one of its editions.
func (client *Client) Front(
	ctx context.Context, releaseID, releaseGroupID uuid.NullUUID,
) (Artwork, error) {
	return client.frontAt(ctx, releaseID, releaseGroupID, thumbnail, maxImageBytes)
}

// FullFront fetches the same picture at the size the archive holds it.
//
// The difference is who is going to look at it. Front's five hundred pixels are
// for Schall's own pages, where a cover is a tile in a grid and the bytes live in
// Postgres beside every other row. This one is for the copy written next to the
// music, which a phone opens full-screen on its now-playing screen — a thumbnail
// blown up to that size is visibly soft, and the archive is holding the real
// thing for free.
//
// It is asked for once per release, ever: the file on disc is what stops it being
// asked for again.
func (client *Client) FullFront(
	ctx context.Context, releaseID, releaseGroupID uuid.NullUUID,
) (Artwork, error) {
	return client.frontAt(ctx, releaseID, releaseGroupID, fullSize, maxFullImageBytes)
}

func (client *Client) frontAt(
	ctx context.Context, releaseID, releaseGroupID uuid.NullUUID, size string, cap int64,
) (Artwork, error) {
	var lastErr error = ErrNoArtwork
	for _, attempt := range []struct {
		kind string
		id   uuid.NullUUID
	}{
		{"release", releaseID},
		{"release-group", releaseGroupID},
	} {
		if !attempt.id.Valid {
			continue
		}
		artwork, err := client.front(ctx, attempt.kind, attempt.id.UUID, size, cap)
		if err == nil {
			return artwork, nil
		}
		if !errors.Is(err, ErrNoArtwork) {
			// A transport failure is not the answer "there is none". Saying so
			// would cache an outage as a permanent absence.
			return Artwork{}, err
		}
		lastErr = err
	}
	return Artwork{}, lastErr
}

func (client *Client) front(
	ctx context.Context, kind string, id uuid.UUID, size string, cap int64,
) (Artwork, error) {
	artwork, err := client.fetch(
		ctx, client.httpClient, client.baseURL+"/"+kind+"/"+id.String()+"/"+size, cap)
	if err != nil && !errors.Is(err, ErrNoArtwork) {
		return Artwork{}, fmt.Errorf("fetch cover art for %s %s: %w", kind, id, err)
	}
	return artwork, err
}

// fetch reads one picture from one address, whatever service is behind it. The
// answer is bytes, an absence, or a failure — and an absence is never a failure,
// because a caller may write it down and stop asking.
//
// A 5xx is asked again rather than reported, because the archive returns one for
// pictures it holds and the caller above cannot tell that from a picture it does
// not. Everything else is answered on the first reply: a 404 is the absence
// itself, and asking twice would not make it truer.
func (client *Client) fetch(
	ctx context.Context, over *http.Client, address string, cap int64,
) (Artwork, error) {
	var artwork Artwork
	var err error
	for attempt := 1; ; attempt++ {
		artwork, err = client.fetchOnce(ctx, over, address, cap)
		if err == nil || attempt >= fetchAttempts || !serverError(err) {
			return artwork, err
		}
		if !wait(ctx, client.betweenAttempts) {
			return Artwork{}, ctx.Err()
		}
	}
}

// errServer marks a reply the archive itself failed to produce, which is the one
// kind of failure worth asking about again.
var errServer = errors.New("the archive could not answer")

func serverError(err error) bool { return errors.Is(err, errServer) }

func (client *Client) fetchOnce(
	ctx context.Context, over *http.Client, address string, cap int64,
) (Artwork, error) {
	response, err := client.get(ctx, over, address)
	if err != nil {
		return Artwork{}, err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, cap))
		_ = response.Body.Close()
	}()

	if response.StatusCode == http.StatusNotFound {
		return Artwork{}, ErrNoArtwork
	}
	if response.StatusCode >= 500 {
		return Artwork{}, fmt.Errorf("fetch %s: HTTP %d: %w",
			address, response.StatusCode, errServer)
	}
	if response.StatusCode != http.StatusOK {
		return Artwork{}, fmt.Errorf("fetch %s: HTTP %d", address, response.StatusCode)
	}
	image, err := io.ReadAll(io.LimitReader(response.Body, cap))
	if err != nil {
		return Artwork{}, fmt.Errorf("read %s: %w", address, err)
	}
	contentType := response.Header.Get("Content-Type")
	if !strings.HasPrefix(contentType, "image/") || len(image) == 0 {
		// Something that is not a picture is not a cover, whatever it says.
		return Artwork{}, ErrNoArtwork
	}
	return Artwork{Image: image, ContentType: contentType, Source: Name}, nil
}

// pictured reports whether an answer is a picture: bytes something can open,
// with a name for the format they are in.
//
// A cover that will not decode is a cover nothing can show. It arrives as a page
// an archive labelled image/jpeg, and it is worse than no cover at all — once it
// is in the cache the row says "this release has its picture", and nothing looks
// for one again. So the source that gave it is treated as having given nothing:
// the next source is asked, and what is stored is an absence rather than a
// picture nobody can open.
//
// Only the head of the file is read, which is what DecodeConfig does: the width
// and the height, from the few bytes at the front that name the format and the
// size. So this answers whether the bytes begin as a picture, and a picture that
// begins well and stops halfway still passes. That other half is somebody else's
// job — a download cut short does not match the length the sender promised, and
// the read fails before these bytes are ever offered here.
func pictured(artwork Artwork) bool {
	if len(artwork.Image) == 0 || artwork.ContentType == "" {
		return false
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(artwork.Image))
	return err == nil && config.Width > 0 && config.Height > 0
}

// Saved is the row a fetch becomes: the picture, or the fact that there is none.
//
// The cache holds one row per release, and a row means one of two things. It
// carries the bytes an archive sent, under the name of the archive that sent
// them; or it carries no bytes at all, which is the answer "everybody was asked
// and nobody has one" and is what stops the same release being asked about
// forever.
//
// A third shape used to get written: an archive's name with no bytes beside it.
// That row is neither answer. It says a picture was found, so nothing looks for
// one again, and it holds nothing to show — a release stuck with no cover that
// nothing will ever fetch. This is where that cannot be written down.
func Saved(artwork Artwork) Artwork {
	if !pictured(artwork) {
		return Artwork{Source: NameNobody}
	}
	return artwork
}

// get asks one address, saying who is asking.
//
// The name matters: Wikimedia refuses a request with no user agent outright, with
// a 403 that reads like a permissions problem and is really a politeness one —
// which is exactly how the first deployment of artist pictures failed. Every
// request from this package goes through here so that cannot happen once per
// service.
func (client *Client) get(
	ctx context.Context, over *http.Client, address string,
) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, fmt.Errorf("build picture request: %w", err)
	}
	if client.userAgent != "" {
		request.Header.Set("User-Agent", client.userAgent)
	}
	response, err := over.Do(request)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", address, err)
	}
	return response, nil
}

// Release is what an archive is asked about: the identifiers Schall resolved, the
// release's own row, and the words for the sources that only understand words.
type Release struct {
	AlbumID                   uuid.UUID
	MusicBrainzReleaseID      uuid.NullUUID
	MusicBrainzReleaseGroupID uuid.NullUUID
	Artist                    string
	Title                     string
}

// ByName is an archive keyed by text rather than by an identifier.
type ByName interface {
	Cover(ctx context.Context, artist, title string) (Artwork, error)
}

// FromLibrary is the picture a release's own files are carrying.
type FromLibrary interface {
	Cover(ctx context.Context, albumID uuid.UUID) (Artwork, error)
}

// Sources asks the archives in the order of how much they can be believed.
//
// The Cover Art Archive goes first because it answers about the very release
// identity resolution settled on: there is no question of which release the
// picture belongs to.
//
// The release's own files come next. A picture inside one of them is there
// because a track mapping tied that file to this release, and a track mapping is
// a match this project proved before it would write it down — so the picture is
// as well attached as the archive's is, and it is a copy somebody already has.
//
// Only then is a name-keyed archive asked, because that one is a guess. What
// comes back is labelled with where it came from so an interface can say so.
//
// A source that could not be reached stops the chain. Falling through to the
// guess because the certain answer was unavailable would quietly swap one for the
// other, and the swap would be invisible ever after.
//
// A source that answered with bytes nobody can open has answered nothing, and
// the next one is asked. That is not the same as being unreachable: the reply
// arrived and it was not a picture.
type Sources struct {
	Archive     *Client
	FromLibrary FromLibrary
	ByName      ByName
}

func (sources Sources) Front(ctx context.Context, release Release) (Artwork, error) {
	if sources.Archive != nil {
		artwork, err := sources.Archive.Front(
			ctx, release.MusicBrainzReleaseID, release.MusicBrainzReleaseGroupID)
		if err == nil && pictured(artwork) {
			return artwork, nil
		}
		if err != nil && !errors.Is(err, ErrNoArtwork) {
			return Artwork{}, err
		}
	}
	if sources.FromLibrary != nil && release.AlbumID != uuid.Nil {
		artwork, err := sources.FromLibrary.Cover(ctx, release.AlbumID)
		if err == nil && pictured(artwork) {
			return artwork, nil
		}
		if err != nil && !errors.Is(err, ErrNoArtwork) {
			return Artwork{}, err
		}
	}
	if sources.ByName == nil {
		return Artwork{}, ErrNoArtwork
	}
	artwork, err := sources.ByName.Cover(ctx, release.Artist, release.Title)
	if err == nil && !pictured(artwork) {
		return Artwork{}, ErrNoArtwork
	}
	return artwork, err
}

// NameUser is the source of a cover a person supplied themselves, by choosing a
// file or by giving an address. It is not an archive's answer and it is not a
// guess: somebody looked at the record and said this is the picture.
//
// That is why it is never asked about again. Every other source is a source
// Schall asks; this one is an answer already given, and a sweep that overwrote
// it would be a program contradicting the person using it.
const NameUser = "user"

// MaxImageBytes is the most one picture may weigh, wherever it comes from. A
// cover is tens of kilobytes and a generous scan is a couple of megabytes;
// anything past this bound is not a cover.
const MaxImageBytes = maxImageBytes

// ErrNotAPicture reports that bytes offered as a cover are not a picture: they
// do not decode, or there are none of them.
//
// It is an error rather than a recorded absence on purpose. A source Schall
// asked and got rubbish from is written down as having nothing, so the next
// source is tried; a picture a person handed over is a request that either
// worked or did not, and a failed one must leave the release exactly as it was.
var ErrNotAPicture = errors.New("that file is not an image Schall can read")

// ErrNotAnAddress reports that what was given is not a web address Schall can
// fetch a picture from.
var ErrNotAnAddress = errors.New("that is not a web address")

// Supplied is the picture a person gave, checked before it is kept.
//
// The check is the one every fetched cover goes through: the bytes have to
// begin as an image of a format Schall can decode. The type is read from the
// bytes rather than taken from the browser or from the name of the file,
// because the type is what the extension of the file written beside the music
// is chosen from — a PNG called cover.jpg is a cover some players will not
// open.
func Supplied(image []byte) (Artwork, error) {
	if len(image) == 0 {
		return Artwork{}, ErrNotAPicture
	}
	artwork := Artwork{
		Image:       image,
		ContentType: http.DetectContentType(image),
		Source:      NameUser,
	}
	if !pictured(artwork) {
		return Artwork{}, ErrNotAPicture
	}
	return artwork, nil
}

// FromURL fetches one picture from an address a person typed.
//
// The reading of what comes back is the archive's: the same size bound, the
// same user agent, the same "a picture or nothing at all". The reaching for it
// is not. This address was chosen by whoever is using Schall, and Schall runs
// inside a network they did not choose — a container on somebody's home server
// sits beside the router's admin page, beside the metadata service of a cloud
// host, beside every other machine on the subnet. An address here is a request
// Schall makes on their behalf, and none of those are places it may be made to
// make one.
//
// So this goes out through a guarded client rather than the archive's: only
// http and https are asked for at all, every hop of a redirect is checked
// again, and the guard itself sits at the dial, where the name has already been
// resolved to the number actually being connected to. That last part is what
// makes it hold. A name is not an address until it is looked up, and a name can
// answer with a public number the first time and a private one the second, so
// the only honest moment to ask where a request is going is the moment the
// connection opens.
//
// A refused address is answered exactly as an address with no picture at it is.
// Telling those two apart tells somebody which of this network's machines and
// ports exist, one address at a time, and a cover dialog is not a port scanner.
func (client *Client) FromURL(ctx context.Context, address string) (Artwork, error) {
	address = strings.TrimSpace(address)
	if !fetchable(address) {
		return Artwork{}, fmt.Errorf("fetch %s: %w", address, ErrNotAnAddress)
	}
	// One byte past the bound is asked for, so a picture exactly at the bound is
	// kept and the first byte over it is a refusal. Reading up to the bound and
	// stopping would store half a picture as somebody's permanent cover, which
	// is worse than refusing it: nothing would ever replace it.
	artwork, err := client.fetch(ctx, client.pictureClient, address, maxImageBytes+1)
	if err != nil {
		return Artwork{}, err
	}
	if len(artwork.Image) > maxImageBytes {
		return Artwork{}, fmt.Errorf("fetch %s: %w", address, ErrPictureTooLarge)
	}
	return Supplied(artwork.Image)
}

// ErrPictureTooLarge reports that a picture weighs more than a cover may.
var ErrPictureTooLarge = errors.New("that picture is larger than a cover may be")

// ErrAddressRefused reports an address Schall will not fetch, because it names
// this machine or this network rather than somewhere on the internet.
//
// A caller must answer it exactly as it answers "there is no picture there".
// Two different answers would say which addresses on the network exist.
var ErrAddressRefused = errors.New("that address cannot be fetched")

// fetchable reports whether an address is one worth resolving at all: a web
// address, with somewhere to send the request.
func fetchable(address string) bool {
	parsed, err := url.Parse(address)
	if err != nil {
		return false
	}
	return (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != ""
}

// pictureRedirects is how many hops one address may take. A picture that moved
// is ordinary; a chain longer than this is not a picture being served.
const pictureRedirects = 5

// guarded is the client a person's own address is fetched through.
//
// Two checks at two moments. Every hop of a redirect is looked at as an
// address, which stops a chain leaving http for something else or going round
// forever. The dial is looked at as a number, which is the check that holds: by
// then the name has been resolved, so a name that answers with a private number
// — on this lookup or the next one — is refused at the moment it would have
// been connected to.
func guarded(timeout time.Duration, allowLocal bool) *http.Client {
	dialer := &net.Dialer{
		Timeout:   timeout,
		KeepAlive: 30 * time.Second,
		Control: func(_, endpoint string, _ syscall.RawConn) error {
			if allowLocal || reachable(endpoint) {
				return nil
			}
			return fmt.Errorf("dial %s: %w", endpoint, ErrAddressRefused)
		},
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: &http.Transport{DialContext: dialer.DialContext},
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= pictureRedirects || !fetchable(request.URL.String()) {
				return fmt.Errorf("fetch %s: %w", request.URL, ErrAddressRefused)
			}
			return nil
		},
	}
}

// reachable reports whether a resolved endpoint is somewhere on the internet,
// rather than somewhere on this machine or this network.
//
// Refused: this machine itself (127.0.0.0/8, ::1, and the unspecified address,
// which a connect reads as "here"), the addresses a machine gives itself when
// there is no network (169.254.0.0/16 and fe80::/10 — a cloud host's metadata
// service lives on one of them), the ranges a private network is built from
// (10/8, 172.16/12, 192.168/16, fc00::/7), the range a carrier hands out behind
// its own NAT (100.64/10), and anything addressed to many machines at once.
func reachable(endpoint string) bool {
	host, _, err := net.SplitHostPort(endpoint)
	if err != nil {
		return false
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	ip = ip.Unmap()
	switch {
	case ip.IsLoopback(), ip.IsUnspecified(), ip.IsPrivate(),
		ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast(),
		ip.IsInterfaceLocalMulticast(), ip.IsMulticast():
		return false
	}
	if ip.Is4() {
		octets := ip.As4()
		// 100.64.0.0/10 is the range a carrier's own NAT hands out, and
		// 0.0.0.0/8 is this network, which is not routed anywhere.
		if octets[0] == 100 && octets[1]&0xc0 == 64 {
			return false
		}
		if octets[0] == 0 {
			return false
		}
	}
	return true
}
