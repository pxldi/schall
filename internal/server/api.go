package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pxldi/schall/internal/coverart"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/downloads"
	"github.com/pxldi/schall/internal/events"
	"github.com/pxldi/schall/internal/identity"
	"github.com/pxldi/schall/internal/library"
	"github.com/pxldi/schall/internal/matching"
	"github.com/pxldi/schall/internal/musicbrainz"
	"github.com/pxldi/schall/internal/sources"
	"github.com/rs/zerolog"
)

const maxRequestBody = 1 << 20

type Store interface {
	ListArtists(context.Context, db.ListArtistsParams) ([]db.ListArtistsRow, error)
	ArtistListTotals(context.Context, db.ArtistListTotalsParams) (db.ArtistListTotalsRow, error)
	CatalogueGenres(context.Context) ([]string, error)
	GetArtist(context.Context, uuid.UUID) (db.GetArtistRow, error)
	ArtistDiscography(context.Context, uuid.UUID) (db.ArtistDiscography, error)
	ArtistMusicBrainzIDsByName(context.Context, []string) (map[string]uuid.UUID, error)
	CreateArtist(context.Context, db.CreateArtistParams) (db.CreateArtistRow, error)
	FollowArtistByID(context.Context, uuid.UUID) (db.FollowArtistByIDRow, error)
	UnfollowArtist(context.Context, uuid.UUID) (db.UnfollowArtistRow, error)
	QueueArtistRefresh(context.Context, uuid.UUID) (db.QueueArtistRefreshRow, error)
	SetArtistMonitorLevel(context.Context, db.SetArtistMonitorLevelParams) (db.SetArtistMonitorLevelRow, error)
	SetArtistWantMissing(context.Context, db.SetArtistWantMissingParams) (db.SetArtistWantMissingRow, error)
	ListAlbums(context.Context, db.ListAlbumsParams) (db.AlbumPage, error)
	GetAlbum(context.Context, uuid.UUID) (db.GetAlbumRow, error)
	ListAlbumTracks(context.Context, uuid.UUID) ([]db.ListAlbumTracksRow, error)
	GetTrackForWanting(context.Context, uuid.UUID) (db.GetTrackForWantingRow, error)
	ArtistTracksForWanting(context.Context, uuid.UUID) ([]db.ArtistTracksForWantingRow, error)
	DashboardSummary(context.Context) (db.DashboardSummaryRow, error)
	QueueAlbumRefresh(context.Context, uuid.UUID) (bool, bool, error)
}

type LibraryStore interface {
	ListLibraryRoots(context.Context) ([]db.LibraryRootRow, error)
	CreateLibraryRoot(context.Context, string) (db.LibraryRootRow, error)
	LibrarySummary(context.Context) (db.LibrarySummaryRow, error)
	ListLibraryFiles(context.Context, db.ListLibraryFilesParams) (db.LibraryFilePage, error)
	LibraryFileByID(context.Context, uuid.UUID) (db.LibraryFileRefRow, error)
	SetLibraryFileAside(context.Context, uuid.UUID) error
	ClearLibraryFileSetAside(context.Context, uuid.UUID) error
	QueueLibraryScan(context.Context) (db.LibraryScanJobRow, error)
	QueueFileResolution(context.Context, uuid.UUID) (db.LibraryScanJobRow, error)
	SetDuplicateKept(context.Context, uuid.UUID, bool) error
	RecordingsHeldTwice(context.Context, bool, int32, int32) (db.HeldTwice, error)
	KeepRecordingHeldTwice(context.Context, uuid.UUID, bool) (int64, error)
}

// LibraryFileRemover takes a file out of the library, disc and all. Optional:
// without it the route reports itself unavailable and nothing else changes,
// which is the right shape for the one irreversible thing Schall does.
type LibraryFileRemover interface {
	Remove(context.Context, uuid.UUID) (string, error)
	// KeepOne deletes the other present copies of one recording and leaves the
	// copy named. It is licensed the same way: a row saying what went and what
	// stayed, committed before the audio is unlinked. The last argument is the
	// set of files the caller was shown as going, and the final string names the
	// actor in the removal licence.
	KeepOne(context.Context, uuid.UUID, uuid.UUID, []uuid.UUID, string) (library.KeptCopy, error)
	// KeepOneOfEach makes the same press once for every recording the library
	// holds more than once. It chooses only where the copies are provably the
	// same audio; everything else it names and leaves alone.
	KeepOneOfEach(context.Context, string) (library.SweptDuplicates, error)
	// RetryUnlinks tries again on every file the library has let go of and
	// could not delete, and reports how many left the disc.
	RetryUnlinks(context.Context) int
}

type LibraryPathValidator interface {
	Validate(string) (string, error)
}

type MatchResolver interface {
	Candidates(context.Context, uuid.UUID) ([]matching.Candidate, error)
	SearchTracks(context.Context, uuid.UUID, string, int) ([]matching.Candidate, error)
	SetManual(context.Context, uuid.UUID, uuid.UUID, bool) error
	ClearManual(context.Context, uuid.UUID) error
	ReconcileFile(context.Context, uuid.UUID) error
}

// CoverArtStore caches the picture of a release. It is display and never
// evidence: nothing read from here reaches matching, identity, or an import
// grader, and a cover cannot say what a file is.
type CoverArtStore interface {
	ReleaseCoverArt(context.Context, uuid.UUID) (db.ReleaseCoverArtRow, error)
	SaveReleaseCoverArt(context.Context, db.SaveReleaseCoverArtParams) error
	DeleteUserReleaseCoverArt(context.Context, uuid.UUID) (int64, error)
	ArtistImage(context.Context, uuid.UUID) (db.ArtistImageRow, error)
}

// FileCoverArtStore caches the picture of a file that belongs to no release — a
// track Schall holds by its address on another service. It is held apart from
// CoverArtStore above so that a store which knows about releases and artists,
// and nothing else, still satisfies that one.
type FileCoverArtStore interface {
	FileCoverArt(context.Context, uuid.UUID) (db.FileCoverArtRow, error)
}

type EditionStore interface {
	SelectAlbumEdition(context.Context, uuid.UUID, uuid.UUID) (db.LibraryScanJobRow, error)
}

// LibraryLayoutStore is where Schall files the music it imports. It is
// separate from SettingsStore because it describes the library rather than a
// service Schall talks to, and because an installation without it still has a
// layout — the default — rather than a missing connection.
type LibraryLayoutStore interface {
	LibraryLayoutSettings(context.Context) (db.LibraryLayoutRow, error)
	SaveLibraryLayoutSettings(context.Context, string) (db.LibraryLayoutRow, error)
}

// LibraryLayoutMover plans and applies the migration that files the library the
// way the template says. It is optional: without it the layout is a setting
// that describes what future imports do, which is what it was before there was
// a mover.
type LibraryLayoutMover interface {
	Plan(context.Context) (library.PlanResult, error)
	Run(context.Context, uuid.UUID) (library.RunSummary, error)
	LatestRun(context.Context) (library.RunSummary, error)
	Discard(context.Context, uuid.UUID) (library.RunSummary, error)
	QueueApply(context.Context, uuid.UUID) error
}

// LibraryTagWriter writes into the files already in the library what Schall
// proved them to be, and reports what the last pass did. It is optional:
// without it the tags Schall writes are the ones an import writes, and this
// route says so rather than half-working.
type LibraryTagWriter interface {
	QueueTagging(context.Context) (library.TagJobRow, error)
	LatestTagging(context.Context) (library.TagStatus, error)
}

// TranscodeSweepStore starts and reports on the pass that shrinks files
// already in the library under the transcoding setting. It is optional in the
// same way LibraryTagWriter is: without it, the setting only ever reaches
// music imported after it was turned on, and this route says so rather than
// half-working.
type TranscodeSweepStore interface {
	QueueSweep(context.Context) (library.TranscodeJobRow, error)
	LatestSweep(context.Context) (library.TranscodeStatus, error)
}

// InboxCleanups asks for and reports the passes that delete the downloaded
// files nothing needs any more (ADR 0035). Optional, and the only
// thing in Schall that deletes anything from the download inbox on request:
// without it these routes report themselves unavailable.
type InboxCleanups interface {
	QueueInboxCleanup(
		ctx context.Context, requestedBy string, dryRun bool, runAfter time.Time,
	) (db.InboxCleanupRow, error)
	InboxCleanup(context.Context, uuid.UUID) (db.InboxCleanupRow, error)
	ListInboxCleanups(context.Context, int32) ([]db.InboxCleanupRow, error)
}

type SettingsStore interface {
	SlskdSettings(context.Context) (db.SlskdSettingsRow, error)
	SaveSlskdSettings(context.Context, db.SaveSlskdSettingsParams) (db.SlskdSettingsRow, error)
	RecordSlskdConnection(context.Context, string, string, string) (db.SlskdSettingsRow, error)
	ImportSettings(context.Context) (db.ImportSettingsRow, error)
	SaveImportSettings(context.Context, db.SaveImportSettingsParams) (db.ImportSettingsRow, error)
}

type DownloadStore interface {
	CreateDownloadRequest(context.Context, db.CreateDownloadRequestParams) (uuid.UUID, bool, error)
	DownloadRequest(context.Context, uuid.UUID) (db.DownloadRequestRow, error)
	ListDownloadRequests(context.Context, db.ListDownloadRequestsParams) (db.DownloadRequestPage, error)
	CancelDownloadRequest(context.Context, uuid.UUID) (db.DownloadRequestRow, error)
	RevalidateDownloadImport(context.Context, uuid.UUID) (db.DownloadRequestRow, error)
	RecordImportDecision(context.Context, db.RecordImportDecisionParams) (db.DownloadRequestRow, error)
	WithdrawImportDecision(context.Context, uuid.UUID, uuid.UUID) (db.DownloadRequestRow, error)
	AlbumDuplicateEvidence(context.Context, uuid.UUID) (db.DuplicateEvidence, error)
	AcknowledgeDuplicates(context.Context, uuid.UUID, db.DuplicateEvidence) (db.DownloadRequestRow, error)
}

// SourceSearchStore records and reads bulk source searches. It is separate from
// DownloadStore because a search records no decision: nothing here can create a
// download request, which is what keeps searching in bulk from becoming
// requesting in bulk.
type SourceSearchStore interface {
	CreateSourceSearchRun(context.Context, []uuid.UUID, bool) (uuid.UUID, int, error)
	SourceSearchRun(context.Context, uuid.UUID) ([]db.SourceSearchRow, error)
	CancelSourceSearchRun(context.Context, uuid.UUID) ([]db.SourceSearchRow, int, error)
}

// TransferController starts and stops the transfers behind a download request.
// It is optional: without it, requests can still be recorded and withdrawn.
type TransferController interface {
	// Start takes an acknowledgement of a source that has changed since it was
	// recorded, for the same reason the duplicate one exists: Schall refuses
	// and asks rather than deciding for the user.
	Start(ctx context.Context, requestID uuid.UUID, acknowledgeSourceChange bool) (db.DownloadRequestRow, error)
	Retry(context.Context, uuid.UUID, []string) (db.DownloadRequestRow, error)
	Cancel(context.Context, uuid.UUID) (db.DownloadRequestRow, error)
	CheckSource(context.Context, uuid.UUID) (downloads.SourceOffer, error)
}

// PeerChallenges answers the human check a Soulseek peer sends before it will
// send a file. It is optional: without it a peer's question is neither read nor
// shown, which is what the Downloads page did before.
type PeerChallenges interface {
	// Questions is the last thing each of these peers asked that nobody here
	// could read, keyed by peer.
	Questions(context.Context, []string) (map[string]db.UnansweredPeerChallengesRow, error)
	// Reply sends a peer what a person typed, and asks that peer again for the
	// files it refused.
	Reply(ctx context.Context, username, message string) error
}

type EditionSearcher interface {
	ListReleaseEditions(context.Context, uuid.UUID) ([]musicbrainz.ReleaseEdition, error)
}

type ArtistSearcher interface {
	SearchArtists(context.Context, string, int) ([]musicbrainz.Artist, error)
}

type Database interface {
	Ping(context.Context) error
}

type API struct {
	store        Store
	overviews    OverviewReader
	database     Database
	artists      ArtistSearcher
	library      LibraryStore
	paths        LibraryPathValidator
	matches      MatchResolver
	identities   IdentityResolver
	editions     EditionSearcher
	editionStore EditionStore
	covers       CoverArtStore
	soundcloud   SoundCloudTracks
	sourceKeys   SourceKeys
	// labels holds the record labels a person follows, and labelSearch is what
	// asks MusicBrainz about labels Schall does not hold. Both optional and
	// separate, the way artists and search already are: an installation
	// without them simply has no labels.
	labels      LabelStore
	labelSearch LabelSearcher
	fileCovers  FileCoverArtStore
	settings    SettingsStore
	layout      LibraryLayoutStore
	mover       LibraryLayoutMover
	tagger      LibraryTagWriter
	shrink      TranscodeSweepStore
	// remover deletes a file from the library. Optional, and held apart from
	// everything else that touches library files, because it is the only one
	// that destroys anything.
	remover   LibraryFileRemover
	downloads DownloadStore
	// search is the one question asked of everything at once. Optional, and
	// deliberately not the same thing as artists, which asks MusicBrainz about
	// artists Schall does not hold.
	search   SearchStore
	searches SourceSearchStore
	// queue is the job table read as a queue rather than as the work behind it.
	queue     JobStore
	transfers TransferController
	// challenges reads and answers what a peer asked in private chat.
	challenges PeerChallenges
	wants      AcquisitionTargets
	playlists  PlaylistService
	spotify    SpotifySettingsStore
	players    NavidromeSettingsStore
	playerSync NavidromePlaylistSyncStore
	// playback is what actually asks the player to look at the library again.
	// Optional: without it the manual "tell it now" route reports itself
	// unavailable, and the settings still read and save.
	playback PlaybackNotifier
	// listenbrainz holds the account recommendations are read under, and
	// recommendations is what can go and ask it. Both optional and separate:
	// the settings can be read and saved on an installation that cannot check
	// them, which is what a first-time configuration is.
	listenbrainz ListenBrainzSettingsStore
	// notifications holds where Schall sends word that the review queue has a
	// question, and notifier is what sends it. Both optional: without them
	// nothing is sent anywhere, which is also what the setting says by default.
	notifications NotificationSettingsStore
	// preferences is what the user said to prefer among the copies peers are
	// sharing, and what never to fetch. Optional: without it the routes report
	// themselves unavailable and every search ranks as it always did.
	preferences     SourcePreferenceStore
	notifier        QueueNotifier
	recommendations RecommendationSource
	// recommendationList reads the suggestions a sweep already stored and takes
	// the reader's decisions about them. Separate from recommendations again:
	// the stored list is readable whether or not ListenBrainz can be reached.
	recommendationList      Recommendations
	recommendationSnapshots RecommendationSnapshotStore
	// weekly is the playlist that obtains a few suggestions a week and removes
	// the ones nobody kept. Optional: without it those routes report themselves
	// unavailable and nothing on this installation is ever deleted by a job.
	weekly WeeklyPlaylist

	// newReleases is the listening surface of the follow feed: one playlist
	// holding the tracks the library owns from the releases the feed found
	// lately. Optional, like every other collaborator here.
	newReleases NewReleasesPlaylist
	// duplicateResolution says whether the recordings held more than once are
	// settled without asking. Optional: without it the setting reads as
	// unavailable and nothing is ever settled on its own.
	duplicateResolution DuplicateResolution
	// activity is the reader behind the Overview feed: what arrived, what was
	// refused, what is waiting and what was deleted. Optional, and it decides
	// nothing — everything it returns already happened.
	activity ActivityReader
	// upgrades is the sweep that looks for library files below the stored
	// bit-rate floor and raises a want for each one. Optional: without it
	// those routes report themselves unavailable and the sweep never runs.
	upgrades UpgradeService
	// playerPairing reads what the player holds for a list. Optional: without
	// it that one route reports itself unavailable and nothing else changes.
	playerPairing PlayerPairing
	oauthStates   oauthStates
	providers     sources.Builder
	// searchPause reports whether the download source has stopped searching
	// because Soulseek is answering this account with silence. Optional:
	// without it the Overview says nothing about a pause, which is what it said
	// before the pause existed.
	searchPause SearchPause
	// previews turns a held copy into audio a browser will play. Optional.
	previews Previews
	// waveforms draws the shape of a copy held before the importer stored one.
	// Optional.
	waveforms Waveforms
	// artwork fetches the picture of a release from an archive keyed by an
	// identifier already resolved. Optional: without it a release simply has no
	// cover, which is what it had before.
	artwork CoverArtFetcher
	// coverPictures fetches a picture from an address a person typed, for the
	// cover somebody sets by hand. Optional: without it only the upload half of
	// setting a cover works.
	coverPictures CoverPictureFetcher
	// inboxPath is the completed-download folder, known here only so the import
	// retention policy can be checked against the filesystem it would act on.
	inboxPath string
	// cleanups asks for and reports the passes that delete what the inbox no
	// longer needs. Optional: without it that block reports itself unavailable
	// and nothing is ever deleted from the inbox.
	cleanups InboxCleanups
	// encoderPath is the ffmpeg the imports would re-encode music with, known
	// here only so that turning re-encoding on can be refused on a machine that
	// has no encoder. Empty means "look for ffmpeg on PATH".
	encoderPath string
	// uploads is where a browser upload lands before anything is decided about
	// it, and uploadStore is what records that one is waiting. Both are
	// optional and go together: without them the upload routes report
	// themselves unavailable.
	uploads     UploadStaging
	uploadStore UploadStore
	// events fans change notices out to connected interfaces. It is optional:
	// without it the stream reports itself unavailable and the views fall back
	// to asking, which is what they did before it existed.
	events *events.Hub
	// decisions announces the per-file decisions a reader makes by hand. It is
	// the same TopicLibrary the rest of this file publishes, rationed: somebody
	// working down a list of unresolved files presses these as fast as they can
	// click, and every raw notice costs each other open interface a refetch. It
	// is built with the hub, so it is a no-op when there is none.
	decisions *events.Rationed
	// auth says who Schall believes, and tokens holds the tokens a phone
	// authenticates with. The zero mode is open, which is what every
	// installation had before there was a mode.
	auth   AuthSettings
	tokens AppTokenStore
	// tokenUses is when each token's use was last written down, so a phone
	// polling every fifteen seconds costs one UPDATE a minute.
	tokenUses tokenUses
	logger    zerolog.Logger
}

type Option func(*API)

func WithLibraryPaths(paths LibraryPathValidator) Option {
	return func(api *API) {
		api.paths = paths
	}
}

// CoverArtFetcher fetches a release's picture from an archive. It is keyed by
// identifiers the catalogue already holds and never by a text search, because a
// cover found by matching strings is the fuzzy agreement the rest of Schall
// refuses — harmless as a picture right up until somebody believes it.
type CoverArtFetcher interface {
	Front(ctx context.Context, release coverart.Release) (coverart.Artwork, error)
}

// CoverPictureFetcher fetches one picture from one address, for the case where
// the address came from the person using Schall rather than from an archive.
//
// It is separate from CoverArtFetcher because the two are asked different
// questions. That one is asked about a release and decides for itself where to
// look; this one is told exactly where to look and asks nothing.
type CoverPictureFetcher interface {
	FromURL(ctx context.Context, address string) (coverart.Artwork, error)
}

// WithLabels registers the record labels a person follows. Without it the
// label routes report themselves unavailable, which is what an installation
// that has never followed one already has.
func WithLabels(labels LabelStore) Option {
	return func(api *API) {
		api.labels = labels
	}
}

// WithLabelSearch registers what asks MusicBrainz about labels Schall does not
// hold. Without it a label cannot be found to follow.
func WithLabelSearch(labelSearch LabelSearcher) Option {
	return func(api *API) {
		api.labelSearch = labelSearch
	}
}

// WithCoverArt registers the archive releases are pictured from. Without it a
// release has no cover and nothing is asked of anybody.
func WithCoverArt(artwork CoverArtFetcher) Option {
	return func(api *API) {
		api.artwork = artwork
	}
}

// WithCoverPictures registers what fetches a picture from an address somebody
// typed. Without it a cover can still be uploaded, and only the address half of
// setting one is unavailable.
func WithCoverPictures(pictures CoverPictureFetcher) Option {
	return func(api *API) {
		api.coverPictures = pictures
	}
}

// WithLayoutMover registers what migrates the library to the stored layout.
func WithLayoutMover(mover LibraryLayoutMover) Option {
	return func(api *API) {
		api.mover = mover
	}
}

// WithLibraryTagger registers what writes the catalogue's account of a file
// into the file itself.
func WithLibraryTagger(tagger LibraryTagWriter) Option {
	return func(api *API) {
		api.tagger = tagger
	}
}

// WithTranscodeSweep registers what shrinks proven files already in the
// library. Without it the button in Settings that asks for a pass over the
// existing collection is unavailable.
func WithTranscodeSweep(shrink TranscodeSweepStore) Option {
	return func(api *API) {
		api.shrink = shrink
	}
}

// WithFileRemover registers what deletes a library file. Without it the route
// answers that deleting is unavailable, and every other way of resolving a
// duplicate still works.
func WithFileRemover(remover LibraryFileRemover) Option {
	return func(api *API) {
		api.remover = remover
	}
}

func WithMatchResolver(matches MatchResolver) Option {
	return func(api *API) {
		api.matches = matches
	}
}

// WithSourceProvider registers the builder used to construct a search-only
// download provider from the stored settings.
func WithSourceProvider(providers sources.Builder) Option {
	return func(api *API) {
		api.providers = providers
	}
}

// SearchPause reports when searching resumes, for a source that has stopped
// asking because Soulseek went quiet on this account. The second result is
// false while searching is going ahead.
type SearchPause interface {
	SilencedUntil() (time.Time, bool)
}

// WithSearchPause registers the thing that knows whether searching is paused.
// It is the search gate, which is the only object in the process that outlives
// a single search.
func WithSearchPause(pause SearchPause) Option {
	return func(api *API) {
		api.searchPause = pause
	}
}

// WithEventHub registers the hub that tells connected interfaces something
// changed. Without it the views keep asking on a timer, which still works.
func WithEventHub(hub *events.Hub) Option {
	return func(api *API) {
		api.events = hub
		api.decisions = events.NewRationed(hub, events.TopicLibrary, decisionNotice)
	}
}

// How often a burst of hand-made file decisions may announce itself. Long
// enough that a reader clicking through a queue does not make every other open
// interface refetch per click, short enough that a single decision reaches the
// other tabs while the reader is still looking at the same screen. It is the
// gap the scanner and the resolution lane already use.
const decisionNotice = 2 * time.Second

// WithTransferController registers the component that starts transfers. Nothing
// else in the API may start one.
func WithTransferController(transfers TransferController) Option {
	return func(api *API) {
		api.transfers = transfers
	}
}

// WithPeerChallenges registers the component that answers a peer's human check
// and reports the questions nobody could read.
func WithPeerChallenges(challenges PeerChallenges) Option {
	return func(api *API) {
		api.challenges = challenges
	}
}

// WithDownloadInbox registers the completed-download folder. The API never
// reads music from it; it needs the path only to answer whether a retention
// policy that removes imported files could be carried out at all.
func WithDownloadInbox(path string) Option {
	return func(api *API) {
		api.inboxPath = path
	}
}

// WithInboxCleanups registers what asks for and reports the passes that delete
// the downloaded files nothing needs any more.
func WithInboxCleanups(cleanups InboxCleanups) Option {
	return func(api *API) {
		api.cleanups = cleanups
	}
}

// WithEncoder registers the ffmpeg imports re-encode music with. Empty means
// it is looked for on PATH, which is what an image that ships one wants.
func WithEncoder(binary string) Option {
	return func(api *API) {
		api.encoderPath = binary
	}
}

// WithPlaylists registers playlist ingestion. Without it the playlist routes
// report themselves unavailable rather than half-working.
func WithPlaylists(service PlaylistService) Option {
	return func(api *API) {
		api.playlists = service
	}
}

// WithPlayerPairing registers what can ask the player which of a list's files
// it holds. It is the same component the job lane pushes with, asked a question
// that changes nothing.
func WithPlayerPairing(pairing PlayerPairing) Option {
	return func(api *API) {
		api.playerPairing = pairing
	}
}

func NewAPI(store Store, database Database, artists ArtistSearcher, logger zerolog.Logger, options ...Option) http.Handler {
	api := &API{store: store, database: database, artists: artists, logger: logger}
	api.library, _ = store.(LibraryStore)
	api.editionStore, _ = store.(EditionStore)
	api.covers, _ = store.(CoverArtStore)
	api.fileCovers, _ = store.(FileCoverArtStore)
	api.settings, _ = store.(SettingsStore)
	api.layout, _ = store.(LibraryLayoutStore)
	api.spotify, _ = store.(SpotifySettingsStore)
	api.players, _ = store.(NavidromeSettingsStore)
	api.listenbrainz, _ = store.(ListenBrainzSettingsStore)
	api.notifications, _ = store.(NotificationSettingsStore)
	api.preferences, _ = store.(SourcePreferenceStore)
	api.recommendationSnapshots, _ = store.(RecommendationSnapshotStore)
	api.playerSync, _ = store.(NavidromePlaylistSyncStore)
	api.downloads, _ = store.(DownloadStore)
	api.search, _ = store.(SearchStore)
	api.searches, _ = store.(SourceSearchStore)
	api.queue, _ = store.(JobStore)
	api.uploadStore, _ = store.(UploadStore)
	api.tokens, _ = store.(AppTokenStore)
	api.editions, _ = artists.(EditionSearcher)
	for _, option := range options {
		option(api)
	}

	router := chi.NewRouter()
	router.Use(middleware.RequestID)
	// The log is outside the credential check, so a refused request is logged
	// too; the check is outside RealIP, because the trusted-network mode reads
	// the TCP peer address and RealIP replaces it with what a header claimed.
	router.Use(api.logRequests)
	router.Use(api.authenticate)
	router.Use(middleware.RealIP)
	router.Use(middleware.Recoverer)

	router.Route("/api/v1", func(router chi.Router) {
		// Registered before the timeout below, because a stream that is cut
		// off after thirty seconds is a slower poll rather than a push.
		router.Get("/events", api.streamEvents)
		// And an upload for the same reason in reverse: half a gigabyte over a
		// domestic upstream takes minutes, and thirty seconds of it is a
		// truncated file. It sets a bound of its own instead.
		router.Post("/uploads", api.createUpload)

		router.Group(func(router chi.Router) {
			router.Use(middleware.Timeout(30 * time.Second))

			router.Get("/health", api.health)
			// Who Schall took this request to be. The app calls it after a
			// scan to confirm the token works.
			router.Get("/me", api.whoAmI)
			router.Get("/dashboard", api.dashboard)
			router.Get("/overview", api.overview)
			// Not "/activity". A path with that word in it is on the filter
			// lists content blockers ship with — the ones written for the
			// analytics and telemetry endpoints that are usually called that —
			// and a blocked request does not come back as a status a screen can
			// read. The browser refuses it before it leaves, fetch throws, and
			// the page reports that the server did not answer when the server
			// was never asked. The name says what the screen calls it instead.
			router.Get("/what-happened", api.listActivity)
			// One question over everything Schall holds, and beside it the
			// older route that asks MusicBrainz about artists it does not.
			router.Get("/search", api.globalSearch)
			// The queue everything else here is eventually run by, read as a
			// queue rather than as the work behind it. The two presses that
			// change anything put jobs that spent their attempts back where
			// the recovery pass puts one a shutdown interrupted — one job, or
			// every job one cause stopped — and either can be pressed twice
			// without the queue noticing.
			router.Get("/jobs", api.listJobs)
			router.Get("/jobs/failed", api.listFailedJobs)
			router.Get("/jobs/schedule", api.jobSchedule)
			router.Post("/jobs/failed/retry", api.retryFailedCause)
			router.Post("/jobs/judge-copies-again", api.judgeCopiesAgain)
			router.Post("/jobs/{jobID}/retry", api.retryJob)
			router.Post("/jobs/{jobID}/cancel", api.cancelJob)
			router.Post("/jobs/queued/cancel", api.cancelQueuedJobsByKind)
			router.Get("/search/artists", api.searchArtists)
			router.Get("/artists", api.listArtists)
			router.Get("/artists/genres", api.listCatalogueGenres)
			router.Post("/artists", api.createArtist)
			router.Get("/artists/{artistID}", api.getArtist)
			router.Post("/artists/{artistID}/follow", api.followArtist)
			router.Delete("/artists/{artistID}/follow", api.unfollowArtist)
			router.Post("/artists/{artistID}/refresh", api.refreshArtist)
			router.Put("/artists/{artistID}/monitor", api.setArtistMonitorLevel)
			router.Post("/artists/{artistID}/wanted", api.wantArtist)
			router.Put("/artists/{artistID}/want-missing", api.setArtistWantMissing)
			router.Get("/search/labels", api.searchLabels)
			router.Get("/labels", api.listLabels)
			router.Post("/labels", api.followLabel)
			router.Get("/labels/{labelID}", api.getLabel)
			router.Delete("/labels/{labelID}/follow", api.unfollowLabel)
			router.Post("/labels/{labelID}/refresh", api.refreshLabel)
			router.Put("/labels/{labelID}/monitor", api.setLabelMonitorLevel)
			router.Get("/albums", api.listAlbums)
			router.Get("/albums/{albumID}", api.getAlbum)
			router.Get("/albums/{albumID}/tracks", api.listAlbumTracks)
			router.Get("/albums/{albumID}/editions", api.listAlbumEditions)
			router.Post("/albums/{albumID}/edition", api.selectAlbumEdition)
			router.Get("/albums/{albumID}/sources", api.listAlbumSources)
			router.Get("/artists/{artistID}/image", api.artistImage)
			router.Get("/albums/{albumID}/cover", api.albumCover)
			router.Put("/albums/{albumID}/cover", api.setAlbumCover)
			router.Delete("/albums/{albumID}/cover", api.removeAlbumCover)
			router.Post("/albums/{albumID}/wanted", api.wantAlbum)
			router.Post("/albums/{albumID}/not-wanted", api.dismissAlbumRemainder)
			// The same three decisions, about many releases at once. The
			// Releases browser holds sixty thousand rows and per-row clicking
			// stops scaling long before that; each answers per release, because
			// a selection of forty is forty separate outcomes.
			router.Post("/releases/ignore", api.ignoreReleases)
			router.Post("/releases/rematch", api.rematchReleases)
			router.Post("/releases/retry", api.retryReleases)
			router.Post("/tracks/{trackID}/wanted", api.wantOneTrack)
			router.Post("/tracks/{trackID}/not-wanted", api.dismissTrack)
			router.Post("/acquisition-targets", api.createAcquisitionTarget)
			router.Get("/acquisition-targets", api.listAcquisitionTargets)
			router.Get("/acquisition-targets/{targetID}", api.acquisitionTarget)
			router.Post("/acquisition-targets/{targetID}/not-wanted", api.stopPursuingAcquisitionTarget)
			router.Delete("/acquisition-targets/{targetID}/not-wanted", api.pursueAcquisitionTargetAgain)
			router.Post("/acquisition-targets/{targetID}/take-best-available", api.takeBestAvailableAcquisitionTarget)
			router.Delete("/acquisition-targets/{targetID}/take-best-available", api.keepAcquisitionTargetFloor)
			router.Post("/acquisition-targets/{targetID}/wrong-recording", api.rejectAcquisitionTargetResolution)
			router.Post("/acquisition-targets/{targetID}/resolution", api.chooseAcquisitionTargetResolution)
			// Keying a want to the address of a track, and that want's floor
			// (ADR 0038). The lookup changes nothing; the key is permanent.
			router.Get("/source-tracks", api.lookUpSourceTrack)
			router.Post("/acquisition-targets/{targetID}/source", api.keyAcquisitionTargetToSource)
			router.Put("/acquisition-targets/{targetID}/minimum-bitrate", api.setAcquisitionTargetMinimumBitrate)
			router.Get("/review-queue", api.reviewQueue)
			router.Get("/review-queue/copies/{copyID}/audio", api.reviewCopyAudio)
			router.Get("/review-queue/copies/{copyID}/waveform", api.reviewCopyWaveform)
			router.Post("/review-queue/copies/{copyID}/accept", api.acceptReviewCopy)
			// The one answer a stopped want takes: the library file it holds is
			// the recording it asked for.
			router.Post("/review-queue/wants/{targetID}/accept-file", api.acceptWantFile)
			router.Post("/acquisition-targets/{targetID}/none-of-these", api.refuseReviewCopies)
			// The way back from any of those four, for as long as the thing the
			// answer asked for has not happened. One route rather than four,
			// because the answer names itself in what it sends.
			router.Get("/review-queue/undo", api.answerIsReversible)
			router.Post("/review-queue/undo", api.takeBackAnswer)

			router.Post("/source-searches", api.createSourceSearch)
			router.Get("/source-searches/{runID}", api.getSourceSearch)
			router.Post("/source-searches/{runID}/cancel", api.cancelSourceSearch)
			router.Get("/albums/{albumID}/duplicates", api.listAlbumDuplicates)
			router.Get("/albums/{albumID}/downloads", api.listAlbumDownloads)
			router.Post("/albums/{albumID}/downloads", api.requestAlbumDownload)
			router.Get("/downloads", api.listDownloads)
			router.Get("/downloads/{requestID}/source", api.checkDownloadSource)
			router.Post("/downloads/{requestID}/start", api.startDownload)
			router.Post("/downloads/{requestID}/retry", api.retryDownload)
			router.Post("/downloads/{requestID}/peer-reply", api.replyToPeer)
			router.Post("/downloads/{requestID}/revalidate", api.revalidateDownloadImport)
			router.Post("/downloads/{requestID}/resolutions", api.resolveImportTrack)
			router.Delete("/downloads/{requestID}/resolutions/{decisionID}", api.withdrawImportDecision)
			router.Delete("/downloads/{requestID}", api.cancelDownload)
			router.Get("/inbox/cleanups", api.listInboxCleanups)
			router.Post("/inbox/cleanups", api.createInboxCleanup)
			router.Get("/inbox/cleanups/{cleanupID}", api.getInboxCleanup)
			router.Get("/playlists", api.listPlaylists)
			router.Post("/playlists", api.followPlaylist)
			router.Post("/playlists/file/preview", api.previewPlaylistFile)
			router.Post("/playlists/file", api.importPlaylistFile)
			router.Get("/playlists/{playlistID}", api.getPlaylist)
			router.Post("/playlists/{playlistID}/import", api.importPlaylist)
			router.Post("/playlists/{playlistID}/entries/{entryID}/wanted", api.wantPlaylistEntry)
			router.Post("/playlists/{playlistID}/navidrome-sync", api.syncPlaylistToNavidrome)
			router.Get("/playlists/{playlistID}/navidrome-pairing", api.playlistPlayerPairing)
			router.Post("/navidrome/sync", api.syncNavidromePlaylists)
			router.Delete("/playlists/{playlistID}", api.deletePlaylist)
			// The phones that hold a token. Minting and removing need the
			// browser identity: a token that could mint one is a stolen phone
			// that can make itself a second key.
			router.Get("/settings/phones", api.listPhones)
			router.Post("/settings/phones", api.addPhone)
			router.Delete("/settings/phones/{tokenID}", api.removePhone)
			router.Get("/settings/spotify", api.getSpotifySettings)
			router.Put("/settings/spotify", api.saveSpotifySettings)
			router.Post("/settings/spotify/authorize", api.authorizeSpotify)
			router.Get("/spotify/callback", api.spotifyCallback)
			router.Get("/settings/slskd", api.getSlskdSettings)
			router.Put("/settings/slskd", api.saveSlskdSettings)
			router.Post("/settings/slskd/test", api.testSlskdConnection)
			router.Get("/settings/navidrome", api.getNavidromeSettings)
			router.Put("/settings/navidrome", api.saveNavidromeSettings)
			router.Post("/settings/navidrome/test", api.testNavidromeConnection)
			router.Post("/settings/navidrome/rescan", api.rescanNavidrome)
			// Where the listening history recommendations are drawn from
			// lives. Read-only towards ListenBrainz: nothing Schall decides
			// about anybody's taste is published under their name.
			router.Get("/settings/listenbrainz", api.getListenBrainzSettings)
			router.Put("/settings/listenbrainz", api.saveListenBrainzSettings)
			router.Post("/settings/listenbrainz/test", api.testListenBrainzConnection)
			// Where Schall says that work has stopped and is waiting on a
			// person. Off until somebody enters an address, and proved by
			// sending a real message rather than by a check that resembles one.
			router.Get("/settings/source-preferences", api.getSourcePreferences)
			router.Put("/settings/source-preferences", api.saveSourcePreferences)
			// The sweep that looks back at files acquired before the bit-rate
			// floor above was set, or before it was raised, and asks the
			// ordinary acquisition loop for a better copy of each one.
			router.Get("/settings/upgrade", api.getUpgradeSettings)
			router.Put("/settings/upgrade", api.saveUpgradeSettings)
			router.Post("/settings/upgrade/scan", api.scanForUpgrades)
			router.Get("/settings/notifications", api.getNotificationSettings)
			router.Put("/settings/notifications", api.saveNotificationSettings)
			router.Post("/settings/notifications/test", api.testNotification)
			// And what that account produced: music the library does not hold,
			// with the hard suppression rules applied as it is read. Reading
			// decides nothing — the two writes are the reader saying "not
			// interested", and the count of what has been put in front of them.
			router.Get("/recommendations", api.listRecommendations)
			router.Post("/recommendations/dismissals", api.dismissRecommendation)
			router.Post("/recommendations/feedback", api.recordRecommendationFeedback)
			router.Delete("/recommendations/feedback", api.clearRecommendationFeedback)
			router.Post("/recommendations/impressions", api.recordRecommendationImpressions)

			// The weekly playlist: the part of the same engine that acts on
			// its own. It obtains a few suggestions a week and removes the
			// ones nobody kept, so every route here is either the account of
			// what it did or the one control that stops it — Keep.
			router.Get("/weekly", api.getWeeklyPlaylist)
			router.Put("/weekly/settings", api.saveWeeklyPlaylistSettings)
			router.Post("/weekly/songs/{fileID}/keep", api.keepWeeklySong)
			router.Get("/weekly/leases/{leaseID}/reads", api.weeklyLeaseEvidence)

			// The new-releases playlist: what the follow feed found, as
			// something to press play on. It rotates playlist rows and
			// nothing else, so there is no control here beyond the window
			// and the switch.
			router.Get("/new-releases", api.getNewReleasesPlaylist)
			router.Put("/new-releases/settings", api.saveNewReleasesPlaylistSettings)

			router.Get("/settings/imports", api.getImportSettings)
			router.Put("/settings/imports", api.saveImportSettings)

			// Where music is filed. Saving one moves nothing: the library on
			// disc stays where it is until a migration is asked for.
			router.Get("/settings/library-layout", api.getLibraryLayout)
			router.Put("/settings/library-layout", api.saveLibraryLayout)
			// And this is where it is asked for. Planning writes a journal and
			// touches nothing; applying is a job, because a library is tens of
			// thousands of renames.
			router.Get("/library/layout/moves", api.getLayoutRun)
			router.Post("/library/layout/moves", api.planLayoutRun)
			router.Post("/library/layout/moves/{runID}/apply", api.applyLayoutRun)
			router.Delete("/library/layout/moves/{runID}", api.discardLayoutRun)
			// What the files themselves say. Schall writes its own account
			// into the music it imports; this is the same write, run over the
			// music that was here before it.
			router.Get("/library/tags", api.getLibraryTags)
			router.Post("/library/tags", api.queueLibraryTags)
			router.Get("/library/transcode", api.getTranscodeSweep)
			router.Post("/library/transcode", api.queueTranscodeSweep)
			router.Get("/library", api.librarySummary)
			router.Get("/library/files", api.listLibraryFiles)
			router.Get("/library/files/{fileID}/matches", api.listMatchCandidates)
			router.Get("/library/files/{fileID}/tracks", api.searchTracks)
			router.Post("/library/files/{fileID}/match", api.setManualMatch)
			router.Delete("/library/files/{fileID}/match", api.clearManualMatch)
			router.Post("/library/files/{fileID}/set-aside", api.setLibraryFileAside)
			router.Delete("/library/files/{fileID}/set-aside", api.clearLibraryFileSetAside)
			router.Post("/library/files/{fileID}/reconcile", api.reconcileLibraryFile)
			// The file itself, as sound. Every other route about a file
			// describes it; this one plays it, because the questions asked
			// here — what is this, and which of these two copies is better —
			// are answered by the ear and by nothing on the page.
			router.Get("/library/files/{fileID}/audio", api.libraryFileAudio)
			// The two ways a second copy of the same music ends. Deleting is
			// the usual one and cannot be undone; keeping is for the recording
			// that honestly belongs on two releases, and is withdrawn by the
			// same route it was set with.
			router.Delete("/library/files/{fileID}", api.removeLibraryFile)
			router.Post("/library/files/{fileID}/keep", api.keepDuplicate)
			router.Delete("/library/files/{fileID}/keep", api.askAboutDuplicateAgain)
			// The same music twice, seen from the music rather than from the
			// file: one recording, every copy of it on disc. Reading it
			// changes nothing, and the answer is given about the recording
			// because that is what was asked about.
			router.Get("/library/duplicates", api.listRecordingsHeldTwice)
			router.Post("/library/duplicates/{recordingID}/keep", api.keepRecordingHeldTwice)
			router.Delete("/library/duplicates/{recordingID}/keep", api.askAboutRecordingHeldTwiceAgain)
			router.Get("/soundcloud/track", api.lookUpSoundCloudTrack)
			router.Post("/library/files/{fileID}/soundcloud", api.nameFileFromSoundCloud)
			router.Get("/library/files/{fileID}/cover", api.libraryFileCover)
			router.Post("/library/duplicates/{recordingID}/keep-one", api.keepOneCopy)
			router.Post("/library/duplicates/keep-one-each", api.keepOneOfEachCopy)
			router.Get("/settings/duplicates", api.getDuplicateResolution)
			router.Put("/settings/duplicates", api.saveDuplicateResolution)
			router.Post("/library/removals/retry", api.retryRemovals)
			router.Get("/library/files/{fileID}/identity", api.getFileIdentity)
			router.Post("/library/files/{fileID}/identity", api.decideFileIdentity)
			router.Delete("/library/files/{fileID}/identity", api.clearFileIdentity)
			router.Post("/library/files/{fileID}/resolve", api.resolveLibraryFile)
			router.Get("/library/roots", api.listLibraryRoots)
			router.Get("/library/storage", api.storage)
			router.Post("/library/roots", api.createLibraryRoot)
			router.Post("/library/scan", api.queueLibraryScan)
			router.Post("/scan", api.queueLibraryScan)
			router.Get("/uploads", api.listUploads)
			router.Delete("/uploads/{uploadID}", api.discardUpload)
		})
	})

	router.Handle("/*", spaHandler())
	return router
}

func (api *API) health(response http.ResponseWriter, request *http.Request) {
	ctx, cancel := context.WithTimeout(request.Context(), 2*time.Second)
	defer cancel()

	if err := api.database.Ping(ctx); err != nil {
		api.problem(response, http.StatusServiceUnavailable, "database unavailable", nil)
		return
	}
	api.writeJSON(response, http.StatusOK, map[string]string{"status": "ok"})
}

// dashboardResponse is the front page's numbers, plus the one fact that is not
// a count: whether the download source is currently refusing searches because
// it is not logged in to Soulseek, and since when (#495). It replaced a plain
// map[string]int64 because that fact is a nullable time, not another count.
type dashboardResponse struct {
	ArtistCount int64 `json:"artistCount"`
	// Artists held because the library contains their music, counted apart
	// from the artists the user chose to follow.
	CatalogueArtistCount int64 `json:"catalogueArtistCount"`
	AlbumCount           int64 `json:"albumCount"`
	TrackCount           int64 `json:"trackCount"`
	OpenWantCount        int64 `json:"openWantCount"`
	ActiveDownloadCount  int64 `json:"activeDownloadCount"`
	RunningJobCount      int64 `json:"runningJobCount"`
	QueuedJobCount       int64 `json:"queuedJobCount"`
	// SourceLoggedOutSince is nil unless acquisition has heard a real search
	// refused because slskd was not logged in to Soulseek, and no search has
	// gone through since. Never set from a single failed connection check —
	// see MarkSourceLoggedOut.
	SourceLoggedOutSince *time.Time `json:"sourceLoggedOutSince"`
	// SourceSilencedUntil is when searching resumes, and nil while it is going
	// ahead. It is the other way the source stops searching without anything
	// looking broken: slskd stays connected and every search comes back empty,
	// which is what a Soulseek ban looks like from here.
	SourceSilencedUntil *time.Time `json:"sourceSilencedUntil"`
}

func (api *API) dashboard(response http.ResponseWriter, request *http.Request) {
	summary, err := api.store.DashboardSummary(request.Context())
	if err != nil {
		api.internalError(response, request, err)
		return
	}

	// Read from settings rather than folded into DashboardSummary's one SQL
	// query: the fact lives in slskd_settings, which is already read through
	// the handwritten settings store, and api.settings is nil on an
	// installation with no source configured at all.
	var loggedOutSince *time.Time
	if api.settings != nil {
		if slskd, err := api.settings.SlskdSettings(request.Context()); err == nil {
			// A source somebody switched off is not refusing anything; a clock
			// an old outage left running would only ever be cleared by a search
			// that is never going to run again, so it is not read for one.
			if slskd.Enabled {
				loggedOutSince = nullableTime(slskd.LoggedOutSince.Valid, slskd.LoggedOutSince.Time)
			}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			api.logger.Warn().Err(err).Msg("could not read slskd settings for the dashboard")
		}
	}

	// Read from the search gate rather than the database, because that is where
	// it lives: the pause is one process's decision about what to ask next, and
	// nothing writes it down.
	var silencedUntil *time.Time
	if api.searchPause != nil {
		if until, paused := api.searchPause.SilencedUntil(); paused {
			silencedUntil = &until
		}
	}

	api.writeJSON(response, http.StatusOK, dashboardResponse{
		ArtistCount:          summary.ArtistCount,
		CatalogueArtistCount: summary.CatalogueArtistCount,
		AlbumCount:           summary.AlbumCount,
		TrackCount:           summary.TrackCount,
		OpenWantCount:        summary.OpenWantCount,
		ActiveDownloadCount:  summary.ActiveDownloadCount,
		RunningJobCount:      summary.RunningJobCount,
		QueuedJobCount:       summary.QueuedJobCount,
		SourceLoggedOutSince: loggedOutSince,
		SourceSilencedUntil:  silencedUntil,
	})
}

type libraryRootResponse struct {
	ID            uuid.UUID  `json:"id"`
	Path          string     `json:"path"`
	Enabled       bool       `json:"enabled"`
	LastScannedAt *time.Time `json:"lastScannedAt"`
	CreatedAt     time.Time  `json:"createdAt"`
}

func (api *API) listLibraryRoots(response http.ResponseWriter, request *http.Request) {
	if !api.libraryAvailable(response) {
		return
	}
	roots, err := api.library.ListLibraryRoots(request.Context())
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	result := make([]libraryRootResponse, 0, len(roots))
	for _, root := range roots {
		result = append(result, libraryRootResponse{
			ID: root.ID, Path: root.Path, Enabled: root.Enabled,
			LastScannedAt: nullableTime(root.LastScannedAt.Valid, root.LastScannedAt.Time),
			CreatedAt:     root.CreatedAt.Time,
		})
	}
	api.writeJSON(response, http.StatusOK, map[string]any{"items": result})
}

type createLibraryRootRequest struct {
	Path string `json:"path"`
}

func (api *API) createLibraryRoot(response http.ResponseWriter, request *http.Request) {
	if !api.libraryAvailable(response) {
		return
	}
	if api.paths == nil {
		api.problem(response, http.StatusServiceUnavailable, "library paths are not configured", nil)
		return
	}
	var input createLibraryRootRequest
	if err := decodeJSON(response, request, &input); err != nil {
		api.problem(response, http.StatusBadRequest, "invalid request body", []string{err.Error()})
		return
	}
	path, err := api.paths.Validate(input.Path)
	if err != nil {
		api.problem(response, http.StatusUnprocessableEntity, "validation failed", []string{err.Error()})
		return
	}
	root, err := api.library.CreateLibraryRoot(request.Context(), path)
	if isUniqueViolation(err) {
		api.problem(response, http.StatusConflict, "music folder already exists", nil)
		return
	}
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	api.writeJSON(response, http.StatusCreated, libraryRootResponse{
		ID: root.ID, Path: root.Path, Enabled: root.Enabled,
		LastScannedAt: nullableTime(root.LastScannedAt.Valid, root.LastScannedAt.Time),
		CreatedAt:     root.CreatedAt.Time,
	})
}

type librarySummaryResponse struct {
	RootCount      int64 `json:"rootCount"`
	FileCount      int64 `json:"fileCount"`
	MissingCount   int64 `json:"missingCount"`
	ErrorCount     int64 `json:"errorCount"`
	MatchedCount   int64 `json:"matchedCount"`
	UnmatchedCount int64 `json:"unmatchedCount"`
	AmbiguousCount int64 `json:"ambiguousCount"`
	DuplicateCount int64 `json:"duplicateCount"`
	// The duplicates still waiting on somebody, which is what a queue counts.
	DuplicatesToDecideCount int64      `json:"duplicatesToDecideCount"`
	ResolvedCount           int64      `json:"resolvedCount"`
	NeedsReviewCount        int64      `json:"needsReviewCount"`
	ConflictCount           int64      `json:"conflictCount"`
	LocalOnlyCount          int64      `json:"localOnlyCount"`
	PendingCount            int64      `json:"pendingCount"`
	FailedCount             int64      `json:"failedCount"`
	TotalSizeBytes          int64      `json:"totalSizeBytes"`
	ScanStatus              string     `json:"scanStatus"`
	ScanError               *string    `json:"scanError,omitempty"`
	ScanStartedAt           *time.Time `json:"scanStartedAt"`
	ScanCompletedAt         *time.Time `json:"scanCompletedAt"`
}

func (api *API) librarySummary(response http.ResponseWriter, request *http.Request) {
	if !api.libraryAvailable(response) {
		return
	}
	summary, err := api.library.LibrarySummary(request.Context())
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	api.writeJSON(response, http.StatusOK, librarySummaryResponse{
		RootCount: summary.RootCount, FileCount: summary.FileCount,
		MissingCount: summary.MissingCount, ErrorCount: summary.ErrorCount,
		MatchedCount: summary.MatchedCount, UnmatchedCount: summary.UnmatchedCount,
		AmbiguousCount: summary.AmbiguousCount, DuplicateCount: summary.DuplicateCount,
		DuplicatesToDecideCount: summary.DuplicateToDecideCount,
		ResolvedCount:           summary.ResolvedCount,
		NeedsReviewCount:        summary.NeedsReviewCount, ConflictCount: summary.ConflictCount,
		LocalOnlyCount: summary.LocalOnlyCount, PendingCount: summary.PendingCount,
		FailedCount:    summary.FailedCount,
		TotalSizeBytes: summary.TotalSizeBytes, ScanStatus: summary.ScanStatus,
		ScanError:       textPointer(summary.ScanError),
		ScanStartedAt:   nullableTime(summary.ScanStartedAt.Valid, summary.ScanStartedAt.Time),
		ScanCompletedAt: nullableTime(summary.ScanCompletedAt.Valid, summary.ScanCompletedAt.Time),
	})
}

type libraryFileResponse struct {
	ID                     uuid.UUID  `json:"id"`
	Path                   string     `json:"path"`
	SizeBytes              int64      `json:"sizeBytes"`
	ModifiedAt             time.Time  `json:"modifiedAt"`
	ArtistTag              *string    `json:"artistTag,omitempty"`
	AlbumTag               *string    `json:"albumTag,omitempty"`
	TitleTag               *string    `json:"titleTag,omitempty"`
	DiscNumber             *int32     `json:"discNumber,omitempty"`
	TrackNumber            *int32     `json:"trackNumber,omitempty"`
	DurationMS             *int32     `json:"durationMs,omitempty"`
	MusicBrainzRecordingID *uuid.UUID `json:"musicbrainzRecordingId,omitempty"`
	ISRC                   *string    `json:"isrc,omitempty"`
	MissingAt              *time.Time `json:"missingAt,omitempty"`
	ScanError              *string    `json:"scanError,omitempty"`
	MatchStatus            string     `json:"matchStatus"`
	MatchCandidateCount    int32      `json:"matchCandidateCount"`
	DuplicateKeptAt        *time.Time `json:"duplicateKeptAt,omitempty"`
	MappingMethod          *string    `json:"mappingMethod,omitempty"`
	MappingConfidence      *float32   `json:"mappingConfidence,omitempty"`
	MappingManual          bool       `json:"mappingManual"`
	MappedTrackID          *uuid.UUID `json:"mappedTrackId,omitempty"`
	ResolutionStatus       string     `json:"resolutionStatus"`
	ResolutionSummary      *string    `json:"resolutionSummary,omitempty"`
	IdentityTitle          *string    `json:"identityTitle,omitempty"`
	IdentityArtist         *string    `json:"identityArtist,omitempty"`
	IdentityManual         bool       `json:"identityManual"`
	// IdentitySource is the service a file Schall holds by address lives on,
	// and IdentityURL is the page it is published at. Both are absent for a
	// file the catalogue can name.
	IdentitySource string `json:"identitySource,omitempty"`
	IdentityURL    string `json:"identityUrl,omitempty"`
	// IdentityRemixer is who edited the track. Present only where the person
	// naming it said somebody other than the uploader recorded it.
	IdentityRemixer        string `json:"identityRemixer,omitempty"`
	IdentityCandidateCount int32  `json:"identityCandidateCount"`
	SetAside               bool   `json:"setAside"`
	// What the file was before an import re-encoded it, and how big it was
	// then. Absent on a file nobody re-encoded, which is every file until
	// somebody turns transcoding on.
	TranscodedFromFormat *string `json:"transcodedFromFormat,omitempty"`
	OriginalSizeBytes    *int64  `json:"originalSizeBytes,omitempty"`
}

// validResolutions are the states a file's external identity can be filtered
// by. They are the states the resolver records, and nothing else.
var validResolutions = map[string]bool{
	identity.StatusPending: true, identity.StatusResolved: true,
	identity.StatusNeedsReview: true, identity.StatusConflict: true,
	identity.StatusLocalOnly: true, identity.StatusFailed: true,
	identity.StatusSource: true,
}

// validFileStatuses are the states the file list can be narrowed to.
//
// The first four are what matching concluded. The last two are what the scanner
// found, and they are here rather than in a filter of their own because a
// reader picking one of these is asking one question — which files am I looking
// at — and because the library summary counts both of them and, until they were
// added, nothing could list either.
var validFileStatuses = map[string]bool{
	"matched": true, "unmatched": true, "ambiguous": true, "duplicate": true,
	db.StatusMissing: true, db.StatusUnreadable: true,
}

func (api *API) listLibraryFiles(response http.ResponseWriter, request *http.Request) {
	if !api.libraryAvailable(response) {
		return
	}
	limit := int32(200)
	if raw := strings.TrimSpace(request.URL.Query().Get("limit")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 500 {
			api.problem(response, http.StatusUnprocessableEntity, "validation failed", []string{"limit must be between 1 and 500"})
			return
		}
		limit = int32(value)
	}
	offset := int32(0)
	if raw := strings.TrimSpace(request.URL.Query().Get("offset")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 0 {
			api.problem(response, http.StatusUnprocessableEntity, "validation failed", []string{"offset must be zero or greater"})
			return
		}
		offset = int32(value)
	}
	status := strings.TrimSpace(request.URL.Query().Get("status"))
	if status != "" && !validFileStatuses[status] {
		api.problem(response, http.StatusUnprocessableEntity, "validation failed",
			[]string{"status must be matched, unmatched, ambiguous, duplicate, missing, or unreadable"})
		return
	}
	resolution := strings.TrimSpace(request.URL.Query().Get("resolution"))
	if resolution != "" && !validResolutions[resolution] {
		api.problem(response, http.StatusUnprocessableEntity, "validation failed",
			[]string{"resolution must be pending, resolved, needs_review, conflict, local_only, failed, or source"})
		return
	}
	query := strings.TrimSpace(request.URL.Query().Get("q"))
	var setAside *bool
	if raw := strings.TrimSpace(request.URL.Query().Get("setAside")); raw != "" {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			api.problem(response, http.StatusUnprocessableEntity, "validation failed",
				[]string{"setAside must be true or false"})
			return
		}
		setAside = &value
	}
	// One file by name. Review decides about files that never entered its queue —
	// one picked out of the library by hand — so it has to be able to read a
	// single row. A malformed id is nothing rather than an error: it filters to
	// no files, which is what asking for a file that cannot exist means.
	fileID := strings.TrimSpace(request.URL.Query().Get("id"))
	if fileID != "" {
		if _, err := uuid.Parse(fileID); err != nil {
			api.problem(response, http.StatusUnprocessableEntity, "validation failed",
				[]string{"id must be a uuid"})
			return
		}
	}
	files, err := api.library.ListLibraryFiles(request.Context(), db.ListLibraryFilesParams{
		Limit: limit, Offset: offset, Status: status, Resolution: resolution, Query: query,
		ID: fileID, SetAside: setAside, Unidentified: request.URL.Query().Get("unidentified") == "true",
	})
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	result := make([]libraryFileResponse, 0, len(files.Items))
	for _, file := range files.Items {
		result = append(result, libraryFileResponseFrom(file))
	}
	api.writeJSON(response, http.StatusOK, map[string]any{
		"items": result, "total": files.Total, "limit": limit, "offset": offset,
	})
}

func libraryFileResponseFrom(file db.LibraryFileRow) libraryFileResponse {
	return libraryFileResponse{
		ID: file.ID, Path: file.Path, SizeBytes: file.SizeBytes, ModifiedAt: file.ModifiedAt,
		ArtistTag: textPointer(file.ArtistTag), AlbumTag: textPointer(file.AlbumTag),
		TitleTag: textPointer(file.TitleTag), DiscNumber: int32Pointer(file.DiscNumber),
		TrackNumber: int32Pointer(file.TrackNumber), DurationMS: int32Pointer(file.DurationMS),
		MusicBrainzRecordingID: nullableUUID(file.MusicBrainzRecordingID),
		ISRC:                   textPointer(file.ISRC), MissingAt: nullableTime(file.MissingAt.Valid, file.MissingAt.Time),
		ScanError:   textPointer(file.ScanError),
		MatchStatus: file.MatchStatus, MatchCandidateCount: file.MatchCandidateCount,
		DuplicateKeptAt:   nullableTime(file.DuplicateKeptAt.Valid, file.DuplicateKeptAt.Time),
		MappingMethod:     textPointer(file.MappingMethod),
		MappingConfidence: float32Pointer(file.MappingConfidence),
		MappingManual:     file.MappingManual, MappedTrackID: nullableUUID(file.MappedTrackID),
		ResolutionStatus:       file.ResolutionStatus,
		ResolutionSummary:      textPointer(file.ResolutionSummary),
		IdentityTitle:          textPointer(file.IdentityTitle),
		IdentityArtist:         textPointer(file.IdentityArtist),
		IdentityManual:         file.IdentityManual,
		IdentitySource:         file.IdentitySource,
		IdentityURL:            file.IdentityURL,
		IdentityRemixer:        file.IdentityRemixer,
		IdentityCandidateCount: file.IdentityCandidateCount,
		SetAside:               file.SetAside,
		TranscodedFromFormat:   textPointer(file.TranscodedFromFormat),
		OriginalSizeBytes:      int64Pointer(file.OriginalSizeBytes),
	}
}

type matchCandidateResponse struct {
	TrackID        uuid.UUID `json:"trackId"`
	Title          string    `json:"title"`
	AlbumTitle     string    `json:"albumTitle"`
	ArtistName     string    `json:"artistName"`
	DiscNumber     int32     `json:"discNumber"`
	TrackNumber    *int32    `json:"trackNumber,omitempty"`
	Method         string    `json:"method"`
	Confidence     float32   `json:"confidence"`
	AlreadyMapped  bool      `json:"alreadyMapped"`
	ManuallyMapped bool      `json:"manuallyMapped"`
	// HeldByPath names the file this track already belongs to, so that being
	// asked to take it away from something says what that something is. Empty
	// when the track is free or already this file's.
	HeldByFileID *uuid.UUID `json:"heldByFileId,omitempty"`
	HeldByPath   string     `json:"heldByPath,omitempty"`
}

func (api *API) listMatchCandidates(response http.ResponseWriter, request *http.Request) {
	fileID, err := uuid.Parse(chi.URLParam(request, "fileID"))
	if err != nil || api.matches == nil {
		api.problem(response, http.StatusNotFound, "library file not found", nil)
		return
	}
	candidates, err := api.matches.Candidates(request.Context(), fileID)
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	result := make([]matchCandidateResponse, 0, len(candidates))
	for _, item := range candidates {
		result = append(result, matchCandidateResponse{
			TrackID: item.TrackID, Title: item.Title, AlbumTitle: item.AlbumTitle,
			ArtistName: item.ArtistName, DiscNumber: item.DiscNumber,
			TrackNumber: item.TrackNumber, Method: item.Method,
			Confidence: item.Confidence, AlreadyMapped: item.AlreadyMapped,
			ManuallyMapped: item.ManuallyMapped,
		})
	}
	api.writeJSON(response, http.StatusOK, map[string]any{"items": result})
}

func (api *API) searchTracks(response http.ResponseWriter, request *http.Request) {
	fileID, err := uuid.Parse(chi.URLParam(request, "fileID"))
	if err != nil || api.matches == nil {
		api.problem(response, http.StatusNotFound, "library file not found", nil)
		return
	}
	query := strings.TrimSpace(request.URL.Query().Get("q"))
	if len([]rune(query)) < 2 {
		api.problem(response, http.StatusUnprocessableEntity, "validation failed", []string{"q must contain at least 2 characters"})
		return
	}
	items, err := api.matches.SearchTracks(request.Context(), fileID, query, 25)
	if errors.Is(err, pgx.ErrNoRows) {
		api.problem(response, http.StatusNotFound, "library file not found", nil)
		return
	}
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	result := make([]matchCandidateResponse, 0, len(items))
	for _, item := range items {
		result = append(result, matchCandidateResponse{
			TrackID: item.TrackID, Title: item.Title, AlbumTitle: item.AlbumTitle,
			ArtistName: item.ArtistName, DiscNumber: item.DiscNumber,
			TrackNumber: item.TrackNumber, Method: item.Method,
			AlreadyMapped: item.AlreadyMapped, ManuallyMapped: item.ManuallyMapped,
		})
	}
	api.writeJSON(response, http.StatusOK, map[string]any{"items": result})
}

type setManualMatchRequest struct {
	TrackID  uuid.UUID `json:"trackId"`
	Reassign bool      `json:"reassign"`
}

func (api *API) setManualMatch(response http.ResponseWriter, request *http.Request) {
	fileID, err := uuid.Parse(chi.URLParam(request, "fileID"))
	if err != nil || api.matches == nil {
		api.problem(response, http.StatusNotFound, "library file not found", nil)
		return
	}
	var input setManualMatchRequest
	if err := decodeJSON(response, request, &input); err != nil {
		api.problem(response, http.StatusBadRequest, "invalid request body", []string{err.Error()})
		return
	}
	if input.TrackID == uuid.Nil {
		api.problem(response, http.StatusUnprocessableEntity, "validation failed", []string{"trackId is required"})
		return
	}
	err = api.matches.SetManual(request.Context(), fileID, input.TrackID, input.Reassign)
	if errors.Is(err, pgx.ErrNoRows) {
		api.problem(response, http.StatusNotFound, "library file or track not found", nil)
		return
	}
	if errors.Is(err, matching.ErrManualConflict) {
		api.problem(response, http.StatusConflict, err.Error(), nil)
		return
	}
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	// The file is matched from here on, and its track is owned. Every other open
	// interface counts both, and its own list would otherwise keep the file
	// among the unanswered until somebody reloaded it.
	api.decisions.Publish()
	response.WriteHeader(http.StatusNoContent)
}

func (api *API) clearManualMatch(response http.ResponseWriter, request *http.Request) {
	fileID, err := uuid.Parse(chi.URLParam(request, "fileID"))
	if err != nil || api.matches == nil {
		api.problem(response, http.StatusNotFound, "library file not found", nil)
		return
	}
	if err := api.matches.ClearManual(request.Context(), fileID); errors.Is(err, pgx.ErrNoRows) {
		api.problem(response, http.StatusNotFound, "manual match not found", nil)
	} else if err != nil {
		api.internalError(response, request, err)
	} else {
		// Withdrawing a match puts the file and its track back to where they
		// were, which is a change of the same size as making one.
		api.decisions.Publish()
		response.WriteHeader(http.StatusNoContent)
	}
}

// libraryFileAudio plays one file the library holds.
//
// The library asks two permanent questions about a file that no number on the
// page answers. The identity panel asks what an unresolved file actually is,
// between candidates that all carry plausible titles and durations. The
// duplicate question asks which of two copies of one recording to delete. Both
// were being asked with everything except the audio in front of the reader,
// which is the mistake the review queue made before it got a player.
//
// It is the whole file rather than a fixed window, for the same reason the
// review queue serves the whole one: somebody unsure about a file needs to move
// around inside it. Where playback opens is the page's business, and both pages
// open it in the middle.
//
// Nothing here is evidence. A file being played changes no state, and nothing
// about playback reaches matching, identity, or any decision store.
func (api *API) libraryFileAudio(response http.ResponseWriter, request *http.Request) {
	if !api.libraryAvailable(response) {
		return
	}
	if api.previews == nil {
		api.problem(response, http.StatusServiceUnavailable, "audio preview is unavailable",
			[]string{"This installation was started without a transcoder."})
		return
	}
	fileID, err := uuid.Parse(chi.URLParam(request, "fileID"))
	if err != nil {
		api.problem(response, http.StatusNotFound, "library file not found", nil)
		return
	}
	file, err := api.library.LibraryFileByID(request.Context(), fileID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		api.problem(response, http.StatusNotFound, "library file not found", nil)
		return
	case err != nil:
		api.internalError(response, request, err)
		return
	}
	if _, err := os.Stat(file.Path); err != nil {
		api.problem(response, http.StatusNotFound, "the file is no longer there",
			[]string{"The library records it at " + file.Path + ". Scan the library to catch up."})
		return
	}
	api.servePlayable(response, request, file.Path)
}

// removeLibraryFile deletes a file from the library and from the disc.
//
// It is the only route in Schall that destroys anything, so it says so plainly
// and does nothing clever: no confirmation token, no soft delete, no rule about
// which of two copies is the better one. The caller has already decided; what
// is left is to carry it out and tell the truth about whether it happened.
func (api *API) removeLibraryFile(response http.ResponseWriter, request *http.Request) {
	fileID, err := uuid.Parse(chi.URLParam(request, "fileID"))
	if err != nil {
		api.problem(response, http.StatusNotFound, "library file not found", nil)
		return
	}
	if api.remover == nil {
		api.problem(response, http.StatusServiceUnavailable, "deleting files is unavailable",
			[]string{"This installation cannot remove files from the library."})
		return
	}
	path, err := api.remover.Remove(request.Context(), fileID)
	if errors.Is(err, library.ErrFileGone) {
		api.problem(response, http.StatusNotFound, "library file not found", nil)
		return
	}
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	api.logger.Info().Str("file_id", fileID.String()).Str("path", path).
		Msg("library file deleted on request")
	api.events.Publish(events.TopicLibrary)
	response.WriteHeader(http.StatusNoContent)
}

// keepDuplicate records that a second copy is meant to be there, and its
// companion puts the question back. Neither touches the music.
func (api *API) keepDuplicate(response http.ResponseWriter, request *http.Request) {
	api.setDuplicateKept(response, request, true)
}

func (api *API) askAboutDuplicateAgain(response http.ResponseWriter, request *http.Request) {
	api.setDuplicateKept(response, request, false)
}

func (api *API) setDuplicateKept(response http.ResponseWriter, request *http.Request, kept bool) {
	if !api.libraryAvailable(response) {
		return
	}
	fileID, err := uuid.Parse(chi.URLParam(request, "fileID"))
	if err != nil {
		api.problem(response, http.StatusNotFound, "library file not found", nil)
		return
	}
	if err := api.library.SetDuplicateKept(request.Context(), fileID, kept); errors.Is(err, pgx.ErrNoRows) {
		api.problem(response, http.StatusNotFound, "library file not found", nil)
		return
	} else if err != nil {
		api.internalError(response, request, err)
		return
	}
	api.events.Publish(events.TopicLibrary)
	response.WriteHeader(http.StatusNoContent)
}

// listRecordingsHeldTwice says where the library holds the same recording more
// than once. It is a read: nothing here removes, moves or retires a file, and
// which copy to keep is a question for the person looking at the list.
//
// ?kept=true asks the other half — the pairs somebody said they meant to have.
// That is where an answer is looked at again, and withdrawn.
func (api *API) listRecordingsHeldTwice(response http.ResponseWriter, request *http.Request) {
	if !api.libraryAvailable(response) {
		return
	}
	kept := request.URL.Query().Get("kept") == "true"
	limit := int32(200)
	if raw := strings.TrimSpace(request.URL.Query().Get("limit")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 500 {
			api.problem(response, http.StatusUnprocessableEntity, "validation failed", []string{"limit must be between 1 and 500"})
			return
		}
		limit = int32(value)
	}
	offset := int32(0)
	if raw := strings.TrimSpace(request.URL.Query().Get("offset")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 0 {
			api.problem(response, http.StatusUnprocessableEntity, "validation failed", []string{"offset must be zero or greater"})
			return
		}
		offset = int32(value)
	}
	held, err := api.library.RecordingsHeldTwice(request.Context(), kept, limit, offset)
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	api.writeJSON(response, http.StatusOK, map[string]any{
		"recordings": held.Recordings, "total": held.Total, "stillOnDisc": held.StillOnDisc,
		"limit": limit, "offset": offset,
	})
}

// keepRecordingHeldTwice records that every copy of this recording is meant to
// be there, and its companion puts the question back. Neither touches the music.
func (api *API) keepRecordingHeldTwice(response http.ResponseWriter, request *http.Request) {
	api.setRecordingHeldTwiceKept(response, request, true)
}

func (api *API) askAboutRecordingHeldTwiceAgain(response http.ResponseWriter, request *http.Request) {
	api.setRecordingHeldTwiceKept(response, request, false)
}

func (api *API) setRecordingHeldTwiceKept(
	response http.ResponseWriter, request *http.Request, kept bool,
) {
	if !api.libraryAvailable(response) {
		return
	}
	recordingID, err := uuid.Parse(chi.URLParam(request, "recordingID"))
	if err != nil {
		api.problem(response, http.StatusNotFound, "recording not found", nil)
		return
	}
	answered, err := api.library.KeepRecordingHeldTwice(request.Context(), recordingID, kept)
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	// Nothing answered means there was nothing to answer: no present file
	// carries this recording, or only one does and keeping a lone copy is not
	// an answer to anything.
	if answered == 0 {
		api.problem(response, http.StatusNotFound,
			"the library does not hold that recording more than once", nil)
		return
	}
	api.events.Publish(events.TopicLibrary)
	response.WriteHeader(http.StatusNoContent)
}

// keepOneCopy deletes the other present copies of a recording the library holds
// more than once, and leaves the one the person picked.
//
// It is the second route in Schall that destroys anything, and the only one
// that destroys more than one file at a time. The choice was made on the screen
// and this carries it out: nothing here weighs the copies against each other.
//
// The request names the copies the screen showed as going as well as the one
// that stays. That is not belt and braces — the dialog promises that nothing is
// deleted which the person did not read, and the only way to keep that promise
// is for the server to refuse when the two lists have come apart.
func (api *API) keepOneCopy(response http.ResponseWriter, request *http.Request) {
	recordingID, err := uuid.Parse(chi.URLParam(request, "recordingID"))
	if err != nil {
		api.problem(response, http.StatusNotFound, "recording not found", nil)
		return
	}
	if api.remover == nil {
		api.problem(response, http.StatusServiceUnavailable, "deleting files is unavailable",
			[]string{"This installation cannot remove files from the library."})
		return
	}
	var input struct {
		FileID   string   `json:"fileId"`
		Deleting []string `json:"deleting"`
	}
	if err := decodeJSON(response, request, &input); err != nil {
		api.problem(response, http.StatusBadRequest, "invalid request body", []string{err.Error()})
		return
	}
	keeperID, err := uuid.Parse(input.FileID)
	if err != nil {
		api.problem(response, http.StatusNotFound, "library file not found", nil)
		return
	}
	deleting := make([]uuid.UUID, 0, len(input.Deleting))
	for _, named := range input.Deleting {
		fileID, err := uuid.Parse(named)
		if err != nil {
			api.problem(response, http.StatusNotFound, "library file not found", nil)
			return
		}
		deleting = append(deleting, fileID)
	}

	kept, err := api.remover.KeepOne(
		request.Context(), recordingID, keeperID, deleting, library.DuplicateScreenRemovalActor,
	)
	// KeepOne names whatever it deleted even when it also returns an error: a
	// failure partway through the doomed files stops it, but cannot take back
	// the copies already gone. Those are reported as what they are — deleted —
	// rather than folded into a refusal that would tell the person nothing was
	// touched when something was.
	if len(kept.RemovedPaths) == 0 {
		switch {
		case errors.Is(err, library.ErrNotHeldTwice):
			api.problem(response, http.StatusNotFound,
				"the library does not hold that recording more than once", nil)
			return
		case errors.Is(err, library.ErrNotACopy):
			api.problem(response, http.StatusConflict, "that file is not a copy of this recording",
				[]string{"The list has changed since it was read. Reload the page."})
			return
		case errors.Is(err, library.ErrCopiesChanged):
			api.problem(response, http.StatusConflict, "the copies changed",
				[]string{"Nothing was deleted. Reload the page and choose again."})
			return
		case errors.Is(err, library.ErrOnlyOtherMusicLeft):
			api.problem(response, http.StatusConflict, "every other copy is also other music",
				[]string{"Nothing was deleted. Those copies stay whichever one you keep."})
			return
		case errors.Is(err, library.ErrKeeperMissing):
			api.problem(response, http.StatusConflict, "the copy to keep is not on disc",
				[]string{"Nothing was deleted. Scan the library to catch up."})
			return
		case err != nil:
			api.internalError(response, request, err)
			return
		}
	}
	logEntry := api.logger.Info().
		Str("musicbrainz_recording_id", recordingID.String()).
		Str("kept_file_id", kept.Kept.String()).
		Int("removed", len(kept.RemovedPaths)).
		Int("still_on_disc", len(kept.StillOnDisc))
	stopped := ""
	if err != nil {
		// Something was deleted before this stopped the rest, so it is logged
		// as a warning about a press that partly worked rather than a plain
		// failure.
		logEntry = api.logger.Warn().Err(err).
			Str("musicbrainz_recording_id", recordingID.String()).
			Str("kept_file_id", kept.Kept.String()).
			Int("removed", len(kept.RemovedPaths)).
			Int("still_on_disc", len(kept.StillOnDisc))
		stopped = "Not every copy could be deleted. Reload the page and try the rest again."
	}
	logEntry.Msg("one copy of a recording was kept and the rest deleted on request")
	api.events.Publish(events.TopicLibrary)
	// The survivor has just been matched again, so what the review screens say
	// about it is stale too.
	api.decisions.Publish()
	body := map[string]any{
		"keptFileId":   kept.Kept,
		"keptPath":     kept.KeptPath,
		"removedPaths": kept.RemovedPaths,
		"stillOnDisc":  kept.StillOnDisc,
	}
	if stopped != "" {
		body["stopped"] = stopped
	}
	api.writeJSON(response, http.StatusOK, body)
}

// retryRemovals tries again on the files the library has let go of and could
// not delete. It exists because the alternative is a screen that names a
// problem and offers nothing: the mount was read-only, somebody fixed it, and
// this is the press that finishes the job.
func (api *API) retryRemovals(response http.ResponseWriter, request *http.Request) {
	if api.remover == nil {
		api.problem(response, http.StatusServiceUnavailable, "deleting files is unavailable",
			[]string{"This installation cannot remove files from the library."})
		return
	}
	deleted := api.remover.RetryUnlinks(request.Context())
	api.logger.Info().Int("deleted", deleted).
		Msg("tried again on the files the library let go of and could not delete")
	api.events.Publish(events.TopicLibrary)
	api.writeJSON(response, http.StatusOK, map[string]any{"deleted": deleted})
}

func (api *API) reconcileLibraryFile(response http.ResponseWriter, request *http.Request) {
	fileID, err := uuid.Parse(chi.URLParam(request, "fileID"))
	if err != nil || api.matches == nil {
		api.problem(response, http.StatusNotFound, "library file not found", nil)
		return
	}
	if err := api.matches.ReconcileFile(request.Context(), fileID); err != nil {
		api.internalError(response, request, err)
		return
	}
	// Unlike a scan or a resolution, this one is finished by the time it
	// answers: the file's match state is already whatever the fresh evaluation
	// made it.
	api.decisions.Publish()
	response.WriteHeader(http.StatusNoContent)
}

func (api *API) queueLibraryScan(response http.ResponseWriter, request *http.Request) {
	if !api.libraryAvailable(response) {
		return
	}
	job, err := api.library.QueueLibraryScan(request.Context())
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	// Queued is a state the library view shows, and the worker may be minutes
	// away from claiming this job. Without this, an interface that is not the
	// one that asked for the scan would show nothing until it started.
	api.events.Publish(events.TopicLibrary)
	api.writeJSON(response, http.StatusAccepted, refreshResponse{
		JobID: job.ID, Status: job.Status, CreatedAt: job.CreatedAt,
	})
}

func (api *API) libraryAvailable(response http.ResponseWriter) bool {
	if api.library == nil {
		api.problem(response, http.StatusServiceUnavailable, "library is unavailable", nil)
		return false
	}
	return true
}

// artistResponse describes an artist and, just as importantly, why Schall holds
// them. Followed is a statement of intent: keep this artist's releases
// complete. An artist with no FollowedAt is in the catalogue because the
// library contains their music, and CatalogueSummary says so in one sentence.
type artistResponse struct {
	ID               uuid.UUID  `json:"id"`
	MusicBrainzID    *uuid.UUID `json:"musicbrainzId"`
	Name             string     `json:"name"`
	SortName         string     `json:"sortName"`
	Followed         bool       `json:"followed"`
	FollowedAt       *time.Time `json:"followedAt"`
	CatalogueSummary *string    `json:"catalogueSummary,omitempty"`
	// Source is the service the artist is an account on, where they are one.
	// A SoundCloud uploader has no discography and nothing to refresh, so the
	// page says their completeness does not apply rather than badging them.
	Source          string     `json:"source,omitempty"`
	LastRefreshedAt *time.Time `json:"lastRefreshedAt"`
	RefreshStatus   string     `json:"refreshStatus"`
	RefreshError    *string    `json:"refreshError,omitempty"`
}

// artistListItemResponse is an artist as the index shows them: how much of their
// catalogue the library holds, and whether anything about them wants a person.
// The counts live here rather than on artistResponse because the endpoints that
// return a single artist — following, unfollowing, refreshing — are answering
// what happened to that artist, not describing the collection around them.
type artistListItemResponse struct {
	artistResponse
	ReleaseCount      int64 `json:"releaseCount"`
	OwnedReleaseCount int64 `json:"ownedReleaseCount"`
	TrackCount        int64 `json:"trackCount"`
	OwnedTrackCount   int64 `json:"ownedTrackCount"`
	// InFlightCount is transfers moving now, and ReviewCount is wants whose copy
	// nobody could identify and which are held for the user to listen to.
	InFlightCount  int64 `json:"inFlightCount"`
	ReviewCount    int64 `json:"reviewCount"`
	NeedsAttention bool  `json:"needsAttention"`
	// HasImage says whether a picture of the artist is cached, so the index
	// asks for the pictures that exist and not for every card once an hour.
	HasImage bool `json:"hasImage"`
}

// listArtists returns the catalogue's artists. An explicit limit keeps API
// callers able to page the list, while an omitted limit returns every artist
// for the browser's unpaged landing page.
//
// Scope narrows to one of the two reasons an artist is listed. It is absent by
// default, meaning everyone: unlike the releases browser, whose catalogue holds
// releases nobody has asked for, every artist here is either followed or held
// for music the user owns, so there is nothing a default scope would be right
// to hide. Which scope opens the page is the interface's decision, not this
// handler's.
//
// completeness and sort narrow and order by how much of an artist is owned.
// Both are answered here rather than in the browser so the full filtered list
// is ordered before it arrives.
func (api *API) listArtists(response http.ResponseWriter, request *http.Request) {
	limit := int32(0)
	if raw := strings.TrimSpace(request.URL.Query().Get("limit")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 200 {
			api.problem(response, http.StatusUnprocessableEntity, "validation failed", []string{"limit must be between 1 and 200"})
			return
		}
		limit = int32(value)
	}
	offset := int32(0)
	if raw := strings.TrimSpace(request.URL.Query().Get("offset")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 0 {
			api.problem(response, http.StatusUnprocessableEntity, "validation failed", []string{"offset must be zero or greater"})
			return
		}
		offset = int32(value)
	}
	scope := strings.TrimSpace(request.URL.Query().Get("scope"))
	if scope != "" && scope != "followed" && scope != "held" {
		api.problem(response, http.StatusUnprocessableEntity, "validation failed", []string{"scope must be followed or held"})
		return
	}
	query := strings.TrimSpace(request.URL.Query().Get("q"))
	completeness := strings.TrimSpace(request.URL.Query().Get("completeness"))
	switch completeness {
	case "", "incomplete", "complete", "attention":
	default:
		api.problem(response, http.StatusUnprocessableEntity, "validation failed",
			[]string{"completeness must be incomplete, complete or attention"})
		return
	}
	sort := strings.TrimSpace(request.URL.Query().Get("sort"))
	switch sort {
	case "", "name", "least-complete", "most-missing":
	default:
		api.problem(response, http.StatusUnprocessableEntity, "validation failed",
			[]string{"sort must be name, least-complete or most-missing"})
		return
	}

	// A genre is picked from the list the catalogue holds, so it is matched
	// whole. A genre nobody is filed under simply finds nobody.
	genre := strings.TrimSpace(request.URL.Query().Get("genre"))

	totals, err := api.store.ArtistListTotals(request.Context(), db.ArtistListTotalsParams{
		Query: query, Scope: scope, Genre: genre,
	})
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	artists, err := api.store.ListArtists(request.Context(), db.ListArtistsParams{
		Scope: scope, Query: query, Completeness: completeness, Sort: sort,
		Genre: genre, Limit: limit, Offset: offset,
	})
	if err != nil {
		api.internalError(response, request, err)
		return
	}

	result := make([]artistListItemResponse, 0, len(artists))
	for _, artist := range artists {
		result = append(result, artistListItemResponse{
			artistResponse: artistResponse{
				ID:               artist.ID,
				MusicBrainzID:    nullableUUID(artist.MusicbrainzID),
				Name:             artist.Name,
				SortName:         artist.SortName,
				Followed:         artist.FollowedAt.Valid,
				FollowedAt:       nullableTime(artist.FollowedAt.Valid, artist.FollowedAt.Time),
				CatalogueSummary: presentText(artist.CatalogueSummary),
				Source:           artist.Source,
				LastRefreshedAt:  nullableTime(artist.LastRefreshedAt.Valid, artist.LastRefreshedAt.Time),
				RefreshStatus:    artist.RefreshStatus,
				RefreshError:     textPointer(artist.RefreshError),
			},
			ReleaseCount:      artist.ReleaseCount,
			OwnedReleaseCount: artist.OwnedReleaseCount,
			TrackCount:        artist.TrackCount,
			OwnedTrackCount:   artist.OwnedTrackCount,
			InFlightCount:     artist.InFlightCount,
			ReviewCount:       artist.ReviewCount,
			NeedsAttention:    artist.NeedsAttention,
			HasImage:          artist.HasImage,
		})
	}
	// Both halves are reported whatever the scope is, so the page can always
	// say how large the list it is not showing is. total counts what the page is
	// a page of, which is the scope narrowed by completeness.
	total := totals.AllCount
	switch completeness {
	case "incomplete":
		total = totals.IncompleteCount
	case "complete":
		total = totals.CompleteCount
	case "attention":
		total = totals.AttentionCount
	}
	api.writeJSON(response, http.StatusOK, map[string]any{
		"items": result, "total": total, "limit": limit, "offset": offset,
		"followedCount": totals.FollowedCount, "heldCount": totals.HeldCount,
		"refreshingCount": totals.RefreshingCount,
		"allCount":        totals.AllCount, "incompleteCount": totals.IncompleteCount,
		"completeCount": totals.CompleteCount, "attentionCount": totals.AttentionCount,
		"releaseTotal": totals.ReleaseTotal, "ownedReleaseTotal": totals.OwnedReleaseTotal,
	})
}

// listCatalogueGenres answers with every genre the catalogue holds an artist
// under, in alphabetical order. It is what the artists browser offers to filter
// by, so it names only genres that would find somebody.
func (api *API) listCatalogueGenres(response http.ResponseWriter, request *http.Request) {
	genres, err := api.store.CatalogueGenres(request.Context())
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	api.writeJSON(response, http.StatusOK, map[string]any{"items": stringList(genres)})
}

// presentText returns a pointer to a string only when there is something to
// say, so an empty summary is absent rather than blank.
func presentText(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return &value
}

// stringList turns a column that may be NULL into a list the browser can read.
// A page draws no chips for an empty list, which is what both "nobody voted"
// and "nobody has asked yet" should look like.
func stringList(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

// biographyLink withholds the article address when there are no words to
// attribute. The link exists to credit Wikipedia for the text; on its own it is
// an attribution line under nothing.
func biographyLink(biography, sourceURL string) string {
	if strings.TrimSpace(biography) == "" {
		return ""
	}
	return sourceURL
}

type artistDetailResponse struct {
	artistResponse
	AlbumCount int64 `json:"albumCount"`
	// MonitorLevel says what counts as missing for this artist and what "want
	// everything missing" wants: everything, main, albums_eps, or owned. It
	// filters counting and auto-wanting, never what is shown.
	MonitorLevel string `json:"monitorLevel"`
	// WantMissing is the standing "want what is missing": while it is on, every
	// missing track of every monitored release is wanted, including the releases
	// whose tracklist has not arrived yet.
	WantMissing bool `json:"wantMissing"`
	// Discography is how much of this artist the library holds, over everything
	// they have. It is answered here because the artist's page asks for their
	// releases one page at a time, and a fraction of a page is not a fraction of
	// a discography.
	Discography artistDiscographyResponse `json:"discography"`
	// Genres is what MusicBrainz's community voted this artist is, most voted
	// first. Display, and empty when nobody voted or nobody has asked yet.
	Genres []string `json:"genres"`
	// Biography is a few lines from Wikipedia about who this is, and
	// BiographySourceUrl is the article they came from. The text is CC BY-SA, so
	// whatever shows it has to name Wikipedia and link the article — the two
	// fields arrive together or not at all.
	Biography          string `json:"biography,omitempty"`
	BiographySourceURL string `json:"biographySourceUrl,omitempty"`
}

// artistDiscographyResponse is the completeness the artist's page states.
// Owned over Counted is the bar; Missing is what "want everything missing"
// would want, which is not Counted less Owned because a release still
// refreshing is neither yet.
type artistDiscographyResponse struct {
	Releases  int64 `json:"releases"`
	Owned     int64 `json:"owned"`
	Missing   int64 `json:"missing"`
	Dismissed int64 `json:"dismissed"`
	Counted   int64 `json:"counted"`
	// Loading is monitored releases whose tracklist has not arrived. They are
	// neither owned nor missing yet, so the page says how many are still on the
	// way rather than letting them look like nothing at all.
	Loading int64 `json:"loading"`
}

func (api *API) getArtist(response http.ResponseWriter, request *http.Request) {
	artistID, err := uuid.Parse(chi.URLParam(request, "artistID"))
	if err != nil {
		api.problem(response, http.StatusNotFound, "artist not found", nil)
		return
	}

	artist, err := api.store.GetArtist(request.Context(), artistID)
	if errors.Is(err, pgx.ErrNoRows) {
		api.problem(response, http.StatusNotFound, "artist not found", nil)
		return
	}
	if err != nil {
		api.internalError(response, request, err)
		return
	}

	discography, err := api.store.ArtistDiscography(request.Context(), artistID)
	if err != nil {
		api.internalError(response, request, err)
		return
	}

	api.writeJSON(response, http.StatusOK, artistDetailResponse{
		artistResponse: artistResponse{
			ID:               artist.ID,
			MusicBrainzID:    nullableUUID(artist.MusicbrainzID),
			Name:             artist.Name,
			SortName:         artist.SortName,
			Followed:         artist.FollowedAt.Valid,
			FollowedAt:       nullableTime(artist.FollowedAt.Valid, artist.FollowedAt.Time),
			CatalogueSummary: presentText(artist.CatalogueSummary),
			Source:           artist.Source,
			LastRefreshedAt:  nullableTime(artist.LastRefreshedAt.Valid, artist.LastRefreshedAt.Time),
			RefreshStatus:    artist.RefreshStatus,
			RefreshError:     textPointer(artist.RefreshError),
		},
		AlbumCount:   artist.AlbumCount,
		MonitorLevel: artist.MonitorLevel,
		WantMissing:  artist.WantMissingSince.Valid,
		Genres:       stringList(artist.Genres),
		Biography:    artist.Biography,
		// The link is withheld when there is nothing to attribute, so the page
		// cannot show an attribution line under no words.
		BiographySourceURL: biographyLink(artist.Biography, artist.BiographySourceUrl),
		Discography: artistDiscographyResponse{
			Releases:  discography.Releases,
			Owned:     discography.Owned,
			Missing:   discography.Missing,
			Dismissed: discography.Dismissed,
			Counted:   discography.Counted,
			Loading:   discography.Loading,
		},
	})
}

// artistMonitorLevels is the closed set the schema enforces, named here so the
// handler can answer with a sentence rather than a constraint violation.
var artistMonitorLevels = map[string]bool{
	"everything": true,
	"main":       true,
	"albums_eps": true,
	"owned":      true,
}

// setArtistMonitorLevel records what counts as missing for this artist. It is
// a filter on counting and auto-wanting, never on data: nothing is deleted,
// existing wants stay, and per-track decisions stay permanent underneath it.
func (api *API) setArtistMonitorLevel(response http.ResponseWriter, request *http.Request) {
	artistID, err := uuid.Parse(chi.URLParam(request, "artistID"))
	if err != nil {
		api.problem(response, http.StatusNotFound, "artist not found", nil)
		return
	}
	var body struct {
		Level string `json:"level"`
	}
	if err := decodeJSON(response, request, &body); err != nil {
		api.problem(response, http.StatusBadRequest, "invalid request body", []string{err.Error()})
		return
	}
	if !artistMonitorLevels[body.Level] {
		api.problem(response, http.StatusUnprocessableEntity, "validation failed",
			[]string{"level must be one of everything, main, albums_eps, or owned"})
		return
	}
	result, err := api.store.SetArtistMonitorLevel(request.Context(), db.SetArtistMonitorLevelParams{
		ArtistID: artistID, MonitorLevel: body.Level,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		api.problem(response, http.StatusNotFound, "artist not found", nil)
		return
	}
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	api.logger.Info().
		Str("artist_id", artistID.String()).
		Str("monitor_level", result.MonitorLevel).
		Msg("artist monitor level set")
	api.writeJSON(response, http.StatusOK, map[string]any{
		"artistId": result.ID, "monitorLevel": result.MonitorLevel,
	})
}

// followArtist promotes an artist Schall already holds. The artist is in the
// catalogue because the library contains their music; following them is the
// separate decision to keep their releases complete, and it queues the full
// refresh that following an artist has always queued.
func (api *API) followArtist(response http.ResponseWriter, request *http.Request) {
	artistID, err := uuid.Parse(chi.URLParam(request, "artistID"))
	if err != nil {
		api.problem(response, http.StatusNotFound, "artist not found", nil)
		return
	}

	artist, err := api.store.FollowArtistByID(request.Context(), artistID)
	if errors.Is(err, pgx.ErrNoRows) {
		// No row means the artist is gone or was already followed. Either way
		// there is nothing to promote, and the caller is told which.
		if _, lookupErr := api.store.GetArtist(request.Context(), artistID); lookupErr == nil {
			api.problem(response, http.StatusConflict, "artist is already followed", nil)
			return
		}
		api.problem(response, http.StatusNotFound, "artist not found", nil)
		return
	}
	if err != nil {
		api.internalError(response, request, err)
		return
	}

	api.writeJSON(response, http.StatusOK, artistResponse{
		ID:              artist.ID,
		MusicBrainzID:   nullableUUID(artist.MusicbrainzID),
		Name:            artist.Name,
		SortName:        artist.SortName,
		Followed:        artist.FollowedAt.Valid,
		FollowedAt:      nullableTime(artist.FollowedAt.Valid, artist.FollowedAt.Time),
		LastRefreshedAt: nullableTime(artist.LastRefreshedAt.Valid, artist.LastRefreshedAt.Time),
		RefreshStatus:   "queued",
	})
}

// unfollowResponse reports which of the two things unfollowing can mean
// actually happened, because the difference matters to the user. "held" means
// the artist is still in the catalogue and the page they came from will still
// list them; "released" means the artist is gone, and gone is only ever chosen
// when the library reached nothing that deleting them could take.
type unfollowResponse struct {
	ID               uuid.UUID  `json:"id"`
	MusicBrainzID    *uuid.UUID `json:"musicbrainzId"`
	Name             string     `json:"name"`
	SortName         string     `json:"sortName"`
	Followed         bool       `json:"followed"`
	Outcome          string     `json:"outcome"`
	CatalogueSummary *string    `json:"catalogueSummary,omitempty"`
	MappedTrackCount int64      `json:"mappedTrackCount"`
}

// unfollowArtist withdraws the intent to keep an artist's releases complete.
//
// It is deliberately not a delete. An artist whose music the library still
// reaches is demoted to held and keeps every release; only an artist the
// library reaches in no way at all is removed, which is safe precisely because
// there is then nothing for the cascade to take. UnfollowArtist decides which,
// in one statement, so the reach cannot change between the check and the act.
func (api *API) unfollowArtist(response http.ResponseWriter, request *http.Request) {
	artistID, err := uuid.Parse(chi.URLParam(request, "artistID"))
	if err != nil {
		api.problem(response, http.StatusNotFound, "artist not found", nil)
		return
	}

	artist, err := api.store.UnfollowArtist(request.Context(), artistID)
	if errors.Is(err, pgx.ErrNoRows) {
		// No row means the artist is gone or was never followed. An artist
		// Schall merely holds has no intent to withdraw, and saying so is more
		// useful than reporting success for a change nobody made.
		if _, lookupErr := api.store.GetArtist(request.Context(), artistID); lookupErr == nil {
			api.problem(response, http.StatusConflict, "artist is not followed", nil)
			return
		}
		api.problem(response, http.StatusNotFound, "artist not found", nil)
		return
	}
	if err != nil {
		api.internalError(response, request, err)
		return
	}

	outcome := "released"
	if artist.Held.Bool {
		outcome = "held"
	}
	api.writeJSON(response, http.StatusOK, unfollowResponse{
		ID:               artist.ID,
		MusicBrainzID:    nullableUUID(artist.MusicbrainzID),
		Name:             artist.Name,
		SortName:         artist.SortName,
		Followed:         false,
		Outcome:          outcome,
		CatalogueSummary: presentText(artist.CatalogueSummary),
		MappedTrackCount: artist.MappedTrackCount,
	})
}

type refreshResponse struct {
	JobID     uuid.UUID `json:"jobId"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"createdAt"`
}

func (api *API) refreshArtist(response http.ResponseWriter, request *http.Request) {
	artistID, err := uuid.Parse(chi.URLParam(request, "artistID"))
	if err != nil {
		api.problem(response, http.StatusNotFound, "artist not found", nil)
		return
	}

	job, err := api.store.QueueArtistRefresh(request.Context(), artistID)
	if errors.Is(err, pgx.ErrNoRows) {
		api.problem(response, http.StatusNotFound, "artist not found", nil)
		return
	}
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	// See queueLibraryScan: the artists list shows a refresh as queued, and the
	// worker announces only once it has claimed the job.
	api.events.Publish(events.TopicCatalogue)

	api.writeJSON(response, http.StatusAccepted, refreshResponse{
		JobID:     job.ID,
		Status:    job.Status,
		CreatedAt: job.CreatedAt.Time,
	})
}

type albumResponse struct {
	ID         uuid.UUID `json:"id"`
	ArtistID   uuid.UUID `json:"artistId"`
	ArtistName string    `json:"artistName"`
	// ArtistFollowed says whether this release belongs to an artist the user
	// asked to keep complete. A release held only because the library contains
	// one of its tracks is not missing the rest of them.
	ArtistFollowed            bool       `json:"artistFollowed"`
	MusicBrainzReleaseGroupID *uuid.UUID `json:"musicbrainzReleaseGroupId"`
	Title                     string     `json:"title"`
	FirstReleaseDate          string     `json:"firstReleaseDate,omitempty"`
	AlbumType                 string     `json:"albumType"`
	TrackCount                int64      `json:"trackCount"`
	OwnedTrackCount           int64      `json:"ownedTrackCount"`
	// DismissedTrackCount is the unowned tracks whose recordings the user
	// decided not to want; completion counts leave them out of the denominator.
	DismissedTrackCount int64 `json:"dismissedTrackCount"`
	// Monitored says whether the artist's monitor level counts this release as
	// part of "complete". Unmonitored releases stay browsable and wantable by
	// hand; they just stop being missing.
	Monitored bool `json:"monitored"`
	// ArtistMonitorLevel is carried per row so pages compute the same
	// denominator the queries do; at 'owned', countable tracks are the owned
	// ones.
	ArtistMonitorLevel     string     `json:"artistMonitorLevel"`
	MusicBrainzReleaseID   *uuid.UUID `json:"musicbrainzReleaseId"`
	EditionSelectionReason string     `json:"editionSelectionReason,omitempty"`
	TrackRefreshStatus     string     `json:"trackRefreshStatus"`
	// HasCover says whether a picture is cached for this release, so a list
	// asks for covers that exist and not for every row once an hour.
	HasCover bool `json:"hasCover"`
}

// The shapes a release can be in, as the browser names them, and the orders it
// can ask for. Both are closed sets: what is not here is a 422 rather than a
// filter quietly ignored, and an ordering in particular has to be one the
// database is willing to walk — see db.albumOrderings, which "owned" rejoined
// once a release started carrying how much of it is held.
// albumCompleteness is the narrowing an artist's page asks for. It is a
// separate vocabulary from the statuses on purpose: these two are the halves of
// the fraction that page states, counted by the monitor level the way the
// fraction is, so a chip and the header can never disagree about a release.
var (
	albumStatuses     = []string{"owned", "partial", "missing", "untracked", "failed"}
	albumCompleteness = []string{"owned", "missing"}
	albumOrderings    = []string{"artist", "title", "year", "owned"}
)

// listAlbums returns the catalogue's releases, one page of them when a limit is
// given.
//
// Paging is asked for rather than imposed, because two other views still need
// the whole list to answer a question about it — the missing view totals every
// absent track a followed artist has, and an artist's own page shows their
// complete discography. Neither is a page of anything. The browser that made
// this list unbounded is the one that now asks for a page.
//
// Scope, search, status and ordering all belong here rather than in the client
// for the same reason: filtering after the fact would filter one page, which
// would be a different list than the one the user asked for.
func (api *API) listAlbums(response http.ResponseWriter, request *http.Request) {
	params := db.ListAlbumsParams{}
	if rawArtistID := strings.TrimSpace(request.URL.Query().Get("artistId")); rawArtistID != "" {
		parsedArtistID, err := uuid.Parse(rawArtistID)
		if err != nil {
			api.problem(response, http.StatusUnprocessableEntity, "validation failed", []string{"artistId must be a UUID"})
			return
		}
		params.ArtistID = uuid.NullUUID{UUID: parsedArtistID, Valid: true}
	}
	params.Scope = strings.TrimSpace(request.URL.Query().Get("scope"))
	if params.Scope != "" && params.Scope != "followed" && params.Scope != "library" {
		api.problem(response, http.StatusUnprocessableEntity, "validation failed", []string{"scope must be followed or library"})
		return
	}
	params.Query = strings.TrimSpace(request.URL.Query().Get("q"))
	params.Status = strings.TrimSpace(request.URL.Query().Get("status"))
	if params.Status != "" && !slices.Contains(albumStatuses, params.Status) {
		api.problem(response, http.StatusUnprocessableEntity, "validation failed",
			[]string{"status must be one of " + strings.Join(albumStatuses, ", ")})
		return
	}
	params.Completeness = strings.TrimSpace(request.URL.Query().Get("completeness"))
	if params.Completeness != "" && !slices.Contains(albumCompleteness, params.Completeness) {
		api.problem(response, http.StatusUnprocessableEntity, "validation failed",
			[]string{"completeness must be one of " + strings.Join(albumCompleteness, ", ")})
		return
	}
	params.Sort = strings.TrimSpace(request.URL.Query().Get("sort"))
	if params.Sort != "" && !slices.Contains(albumOrderings, params.Sort) {
		api.problem(response, http.StatusUnprocessableEntity, "validation failed",
			[]string{"sort must be one of " + strings.Join(albumOrderings, ", ")})
		return
	}
	switch direction := strings.TrimSpace(request.URL.Query().Get("direction")); direction {
	case "", "asc":
	case "desc":
		params.Descending = true
	default:
		api.problem(response, http.StatusUnprocessableEntity, "validation failed", []string{"direction must be asc or desc"})
		return
	}
	// No limit means the whole list, which is what it has always meant here.
	if raw := strings.TrimSpace(request.URL.Query().Get("limit")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 200 {
			api.problem(response, http.StatusUnprocessableEntity, "validation failed", []string{"limit must be between 1 and 200"})
			return
		}
		params.Limit = int32(value)
	}
	if raw := strings.TrimSpace(request.URL.Query().Get("offset")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 0 {
			api.problem(response, http.StatusUnprocessableEntity, "validation failed", []string{"offset must be zero or greater"})
			return
		}
		params.Offset = int32(value)
	}

	albums, err := api.store.ListAlbums(request.Context(), params)
	if err != nil {
		api.internalError(response, request, err)
		return
	}

	result := make([]albumResponse, 0, len(albums.Items))
	for _, album := range albums.Items {
		firstReleaseDate := album.FirstReleaseDate
		if firstReleaseDate == "" && album.ReleaseDate.Valid {
			firstReleaseDate = album.ReleaseDate.Time.Format("2006-01-02")
		}
		result = append(result, albumResponse{
			ID:                        album.ID,
			ArtistID:                  album.ArtistID,
			ArtistName:                album.ArtistName,
			ArtistFollowed:            album.ArtistFollowed,
			MusicBrainzReleaseGroupID: nullableUUID(album.MusicbrainzReleaseGroupID),
			Title:                     album.Title,
			FirstReleaseDate:          firstReleaseDate,
			AlbumType:                 album.AlbumType,
			TrackCount:                album.TrackCount,
			OwnedTrackCount:           album.OwnedTrackCount,
			DismissedTrackCount:       album.DismissedTrackCount,
			Monitored:                 album.Monitored,
			ArtistMonitorLevel:        album.MonitorLevel,
			MusicBrainzReleaseID:      nullableUUID(album.MusicbrainzReleaseID),
			EditionSelectionReason:    album.EditionSelectionReason,
			TrackRefreshStatus:        album.TrackRefreshStatus,
			HasCover:                  album.HasCover,
		})
	}
	body := map[string]any{
		"items":  result,
		"total":  albums.Total,
		"limit":  params.Limit,
		"offset": params.Offset,
		// What the status filter is holding back, so the page can say so rather
		// than looking like the scope itself is empty.
		"scopeTotal": albums.ScopeTotal,
	}
	// How many releases each shape holds, counted in the same pass as the scope.
	// The browser's chips say so; before the counts were columns, asking six
	// times was six passes over the catalogue. A request that asked for the
	// whole list rather than a page was never counted, and leaves them out
	// rather than answering nought, which is a number and would be read as one.
	if albums.StatusTotals != nil {
		body["ownedCount"] = albums.StatusTotals["owned"]
		body["partialCount"] = albums.StatusTotals["partial"]
		body["missingCount"] = albums.StatusTotals["missing"]
		body["untrackedCount"] = albums.StatusTotals["untracked"]
		body["failedCount"] = albums.StatusTotals["failed"]
	}
	api.writeJSON(response, http.StatusOK, body)
}

type albumDetailResponse struct {
	ID                        uuid.UUID  `json:"id"`
	ArtistID                  uuid.UUID  `json:"artistId"`
	ArtistName                string     `json:"artistName"`
	ArtistFollowed            bool       `json:"artistFollowed"`
	MusicBrainzReleaseGroupID *uuid.UUID `json:"musicbrainzReleaseGroupId"`
	Title                     string     `json:"title"`
	FirstReleaseDate          string     `json:"firstReleaseDate,omitempty"`
	AlbumType                 string     `json:"albumType"`
	MusicBrainzReleaseID      *uuid.UUID `json:"musicbrainzReleaseId"`
	EditionStatus             *string    `json:"editionStatus,omitempty"`
	EditionCountry            *string    `json:"editionCountry,omitempty"`
	Barcode                   *string    `json:"barcode,omitempty"`
	EditionReleaseDate        *string    `json:"editionReleaseDate,omitempty"`
	MediaCount                int32      `json:"mediaCount"`
	TrackCount                int32      `json:"trackCount"`
	SelectedAutomatically     bool       `json:"selectedAutomatically"`
	SelectionReason           string     `json:"selectionReason,omitempty"`
	// CoverSource is which archive the picture came from, empty until one is
	// cached. It is reported because a cover found by name is a guess and an
	// interface showing one has to be able to say so.
	CoverSource string `json:"coverSource,omitempty"`
	// TrackRefreshStatus says whether the tracks of this release are still on
	// their way from MusicBrainz, in the same five words a row in the releases
	// list uses. A release with no tracks is an ordinary permanent state for
	// music somebody holds locally, so a page that has none cannot tell from the
	// tracks alone whether waiting would ever end. This says: 'queued' or
	// 'running' means work is under way, and anything else means nobody is
	// fetching them and nothing will arrive.
	TrackRefreshStatus string `json:"trackRefreshStatus"`
	// Genres is what MusicBrainz's community voted this release group is, most
	// voted first. Display, and empty both when nobody voted and when nobody has
	// asked yet.
	Genres []string `json:"genres"`
}

func (api *API) getAlbum(response http.ResponseWriter, request *http.Request) {
	albumID, ok := api.albumID(response, request)
	if !ok {
		return
	}
	album, err := api.store.GetAlbum(request.Context(), albumID)
	if errors.Is(err, pgx.ErrNoRows) {
		api.problem(response, http.StatusNotFound, "release not found", nil)
		return
	}
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	firstReleaseDate := album.FirstReleaseDate
	if firstReleaseDate == "" && album.ReleaseDate.Valid {
		firstReleaseDate = album.ReleaseDate.Time.Format("2006-01-02")
	}
	api.writeJSON(response, http.StatusOK, albumDetailResponse{
		ID: album.ID, ArtistID: album.ArtistID, ArtistName: album.ArtistName,
		ArtistFollowed:            album.ArtistFollowed,
		MusicBrainzReleaseGroupID: nullableUUID(album.MusicbrainzReleaseGroupID),
		Title:                     album.Title, FirstReleaseDate: firstReleaseDate, AlbumType: album.AlbumType,
		MusicBrainzReleaseID: nullableUUID(album.MusicbrainzReleaseID),
		EditionStatus:        textPointer(album.EditionStatus), EditionCountry: textPointer(album.EditionCountry),
		CoverSource: album.CoverSource,
		Barcode:     textPointer(album.Barcode), EditionReleaseDate: textPointer(album.EditionReleaseDate),
		MediaCount: album.MediaCount, TrackCount: album.TrackCount,
		SelectedAutomatically: album.SelectedAutomatically, SelectionReason: album.SelectionReason,
		TrackRefreshStatus: album.TrackRefreshStatus,
		Genres:             stringList(album.Genres),
	})
}

type trackResponse struct {
	ID                     uuid.UUID  `json:"id"`
	MusicBrainzRecordingID *uuid.UUID `json:"musicbrainzRecordingId"`
	Title                  string     `json:"title"`
	DiscNumber             int32      `json:"discNumber"`
	TrackNumber            *int32     `json:"trackNumber"`
	DurationMS             *int32     `json:"durationMs"`
	ISRC                   *string    `json:"isrc,omitempty"`
	Owned                  bool       `json:"owned"`
	// HeldElsewhere is the library holding this recording on a file that maps
	// onto some other release. The music is here and the page says so; only the
	// match details below belong to the release that has the mapping.
	HeldElsewhere   bool       `json:"heldElsewhere"`
	MatchMethod     *string    `json:"matchMethod,omitempty"`
	MatchConfidence *float32   `json:"matchConfidence,omitempty"`
	MatchManual     bool       `json:"matchManual"`
	LibraryFileID   *uuid.UUID `json:"libraryFileId,omitempty"`
	// The want that governs this track's recording, when one exists: its id so
	// the page can act on it, and its status so the page can say which action
	// fits. Absent for a track nothing was ever asked for.
	WantID     *uuid.UUID `json:"wantId,omitempty"`
	WantStatus *string    `json:"wantStatus,omitempty"`
}

func (api *API) listAlbumTracks(response http.ResponseWriter, request *http.Request) {
	albumID, ok := api.albumID(response, request)
	if !ok {
		return
	}
	tracks, err := api.store.ListAlbumTracks(request.Context(), albumID)
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	result := make([]trackResponse, 0, len(tracks))
	for _, track := range tracks {
		result = append(result, trackResponse{
			ID: track.ID, MusicBrainzRecordingID: nullableUUID(track.MusicbrainzRecordingID),
			Title: track.Title, DiscNumber: track.DiscNumber,
			TrackNumber: int32Pointer(track.TrackNumber), DurationMS: int32Pointer(track.DurationMs),
			ISRC: textPointer(track.Isrc), Owned: track.Owned,
			HeldElsewhere:   track.HeldElsewhere,
			MatchMethod:     textPointer(track.MatchMethod),
			MatchConfidence: float32Pointer(track.MatchConfidence),
			MatchManual:     track.MatchManual, LibraryFileID: nullableUUID(track.LibraryFileID),
			WantID:     nullableUUID(track.WantID),
			WantStatus: textPointer(track.WantStatus),
		})
	}
	api.writeJSON(response, http.StatusOK, map[string]any{"items": result})
}

type editionResponse struct {
	MusicBrainzReleaseID uuid.UUID `json:"musicbrainzReleaseId"`
	Title                string    `json:"title"`
	Status               string    `json:"status,omitempty"`
	Country              string    `json:"country,omitempty"`
	ReleaseDate          string    `json:"releaseDate,omitempty"`
}

func (api *API) listAlbumEditions(response http.ResponseWriter, request *http.Request) {
	albumID, ok := api.albumID(response, request)
	if !ok {
		return
	}
	if api.editions == nil {
		api.problem(response, http.StatusServiceUnavailable, "edition lookup is unavailable", nil)
		return
	}
	album, err := api.store.GetAlbum(request.Context(), albumID)
	if errors.Is(err, pgx.ErrNoRows) || !album.MusicbrainzReleaseGroupID.Valid {
		api.problem(response, http.StatusNotFound, "release not found", nil)
		return
	}
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	items, err := api.editions.ListReleaseEditions(request.Context(), album.MusicbrainzReleaseGroupID.UUID)
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	result := make([]editionResponse, 0, len(items))
	for _, item := range items {
		result = append(result, editionResponse{
			MusicBrainzReleaseID: item.ID, Title: item.Title, Status: item.Status,
			Country: item.Country, ReleaseDate: item.Date,
		})
	}
	api.writeJSON(response, http.StatusOK, map[string]any{"items": result})
}

type selectEditionRequest struct {
	MusicBrainzReleaseID uuid.UUID `json:"musicbrainzReleaseId"`
}

func (api *API) selectAlbumEdition(response http.ResponseWriter, request *http.Request) {
	albumID, ok := api.albumID(response, request)
	if !ok {
		return
	}
	if api.editionStore == nil {
		api.problem(response, http.StatusServiceUnavailable, "edition selection is unavailable", nil)
		return
	}
	var input selectEditionRequest
	if err := decodeJSON(response, request, &input); err != nil {
		api.problem(response, http.StatusBadRequest, "invalid request body", []string{err.Error()})
		return
	}
	if input.MusicBrainzReleaseID == uuid.Nil {
		api.problem(response, http.StatusUnprocessableEntity, "validation failed", []string{"musicbrainzReleaseId is required"})
		return
	}
	if api.editions == nil {
		api.problem(response, http.StatusServiceUnavailable, "edition lookup is unavailable", nil)
		return
	}
	album, err := api.store.GetAlbum(request.Context(), albumID)
	if errors.Is(err, pgx.ErrNoRows) || !album.MusicbrainzReleaseGroupID.Valid {
		api.problem(response, http.StatusNotFound, "release not found", nil)
		return
	}
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	editions, err := api.editions.ListReleaseEditions(request.Context(), album.MusicbrainzReleaseGroupID.UUID)
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	valid := false
	for _, edition := range editions {
		if edition.ID == input.MusicBrainzReleaseID {
			valid = true
			break
		}
	}
	if !valid {
		api.problem(response, http.StatusUnprocessableEntity, "validation failed", []string{"musicbrainzReleaseId is not an edition of this release group"})
		return
	}
	job, err := api.editionStore.SelectAlbumEdition(request.Context(), albumID, input.MusicBrainzReleaseID)
	if errors.Is(err, pgx.ErrNoRows) {
		api.problem(response, http.StatusNotFound, "release not found", nil)
		return
	}
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	// See queueLibraryScan: the chosen edition is already recorded, but the
	// tracks it brings arrive only once the worker claims the refresh this
	// queued, and the release is shown in more places than the page that asked.
	api.events.Publish(events.TopicCatalogue)
	api.writeJSON(response, http.StatusAccepted, refreshResponse{
		JobID: job.ID, Status: job.Status, CreatedAt: job.CreatedAt,
	})
}

// albumCover serves the picture of a release, and is the only thing that ever
// fetches one.
//
// The browser asks Schall and Schall asks the archive at most once per release,
// ever: a page of a hundred releases is a hundred requests to this installation
// and nothing to anybody else. That is the point of caching the bytes rather than
// handing out an archive URL — a self-hosted tool making a hundred third-party
// requests on the user's behalf should be a decision, not an accident.
//
// A release the archive has no picture for is remembered as having none, so the
// absence is answered from the cache like everything else. A fetch that failed
// for any other reason is not remembered, because an outage is not an absence and
// caching it would make a bad afternoon permanent.
//
// None of this is evidence. The cover is keyed by a MusicBrainz release the
// catalogue already resolved, no grader reads it, and a file's identity is
// untouched by which picture sits beside it.
func (api *API) albumCover(response http.ResponseWriter, request *http.Request) {
	albumID, ok := api.albumID(response, request)
	if !ok {
		return
	}
	if api.covers == nil {
		api.problem(response, http.StatusNotFound, "no cover art", nil)
		return
	}

	cached, err := api.covers.ReleaseCoverArt(request.Context(), albumID)
	switch {
	case err == nil:
		api.writeCover(response, request, cached.Image, cached.ContentType)
		return
	case !errors.Is(err, pgx.ErrNoRows):
		api.internalError(response, request, err)
		return
	}

	// A list of releases asks for what is already here and never sets an archive
	// working: a page of a hundred covers is a hundred requests to this
	// installation and none to anybody else. Filling the cache is the sweep's job.
	if api.artwork == nil {
		api.problem(response, http.StatusNotFound, "no cover art", nil)
		return
	}
	if request.URL.Query().Get("cached") == "1" {
		api.cachedCoverMiss(response, "no cover art")
		return
	}
	album, err := api.store.GetAlbum(request.Context(), albumID)
	if errors.Is(err, pgx.ErrNoRows) {
		api.problem(response, http.StatusNotFound, "release not found", nil)
		return
	}
	if err != nil {
		api.internalError(response, request, err)
		return
	}

	artwork, err := api.artwork.Front(request.Context(), coverart.Release{
		AlbumID:                   albumID,
		MusicBrainzReleaseID:      album.MusicbrainzReleaseID,
		MusicBrainzReleaseGroupID: album.MusicbrainzReleaseGroupID,
		Artist:                    album.ArtistName,
		Title:                     album.Title,
	})
	if err != nil && !errors.Is(err, coverart.ErrNoArtwork) {
		api.logger.Warn().Err(err).Str("album_id", albumID.String()).Msg("fetch cover art")
		api.problem(response, http.StatusNotFound, "no cover art", nil)
		return
	}
	// What is remembered is the picture, or the fact that there is none — and
	// never an archive's name with no picture under it. A release recorded that
	// way is looked for again by nothing and has nothing to show, which leaves it
	// blank for good.
	stored := coverart.Saved(artwork)
	if saveErr := api.covers.SaveReleaseCoverArt(request.Context(), db.SaveReleaseCoverArtParams{
		AlbumID: albumID, Image: stored.Image, ContentType: stored.ContentType,
		Source: stored.Source,
	}); saveErr != nil {
		// The picture is still servable; only the remembering failed.
		api.logger.Warn().Err(saveErr).Str("album_id", albumID.String()).Msg("cache cover art")
	}
	api.writeCover(response, request, artwork.Image, artwork.ContentType)
}

// coverUploadBytes is the most a request setting a cover may carry: the bound on
// the picture itself, and a megabyte for the form around it. A request past this
// is refused as it arrives rather than after it has all been read.
const coverUploadBytes = coverart.MaxImageBytes + (1 << 20)

// coverResponse is what a release's cover became. The browser reads the source
// to know a cover is one somebody set, which is the only cover it offers to take
// back, and asks for the picture itself at the address it always does.
type coverResponse struct {
	Source string `json:"source"`
	// ContentType is what the bytes turned out to be, read from the bytes and
	// not from the name of the file.
	ContentType string `json:"contentType,omitempty"`
	SizeBytes   int    `json:"sizeBytes,omitempty"`
}

// setAlbumCover keeps the picture a person chose for a release.
//
// A release Schall cannot picture is an ordinary thing: the Cover Art Archive
// answers about the release it was asked about and nothing else, and there are
// records nobody has ever uploaded a sleeve for anywhere. The sweep records that
// as "asked, and there is none", and the release stays blank for good. This is
// the way out of that — the person has the picture, and hands it over.
//
// It arrives one of two ways, and the two meet immediately. A multipart request
// carries the bytes of a file they chose; a JSON request carries an address, and
// Schall fetches it under the same size bound and the same reading of what came
// back that the archive gets. Either way what is kept is checked to be a picture
// first, so a page saved as .jpg becomes a refusal rather than a cover nobody
// can open.
//
// What is written is a cover like any other, under the source 'user'. That name
// is the whole of its permanence: the sweep passes over it, and nothing asks an
// archive about this release again until the person takes it back.
//
// None of this is evidence, exactly as no other cover is. A picture cannot say
// what a file is, and a picture the user supplied says no more than one an
// archive sent.
func (api *API) setAlbumCover(response http.ResponseWriter, request *http.Request) {
	albumID, ok := api.albumID(response, request)
	if !ok {
		return
	}
	if api.covers == nil {
		api.problem(response, http.StatusServiceUnavailable, "covers are not available",
			[]string{"This installation cannot store covers. Nothing was changed."})
		return
	}
	if _, err := api.store.GetAlbum(request.Context(), albumID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			api.problem(response, http.StatusNotFound, "release not found", nil)
			return
		}
		api.internalError(response, request, err)
		return
	}

	artwork, ok := api.suppliedCover(response, request)
	if !ok {
		return
	}
	if err := api.covers.SaveReleaseCoverArt(request.Context(), db.SaveReleaseCoverArtParams{
		AlbumID: albumID, Image: artwork.Image, ContentType: artwork.ContentType,
		Source: artwork.Source,
	}); err != nil {
		api.internalError(response, request, err)
		return
	}
	// The same cover is drawn on the library grid and on the artist's page, not
	// only on the page that set it.
	api.events.Publish(events.TopicCatalogue)
	api.writeJSON(response, http.StatusOK, coverResponse{
		Source: artwork.Source, ContentType: artwork.ContentType, SizeBytes: len(artwork.Image),
	})
}

// suppliedCover reads the picture out of the request, whichever way it was sent,
// and answers the person itself when it cannot.
//
// Nothing is written down on any path that fails. A cover that could not be read
// leaves the release exactly as it was — which is what lets somebody try the
// address, find it is a page rather than a picture, and then choose a file
// instead, without the release having changed in between.
func (api *API) suppliedCover(
	response http.ResponseWriter, request *http.Request,
) (coverart.Artwork, bool) {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err == nil && strings.HasPrefix(mediaType, "multipart/") {
		return api.uploadedCover(response, request)
	}
	return api.fetchedCover(response, request)
}

// uploadedCover reads the file the browser sent.
func (api *API) uploadedCover(
	response http.ResponseWriter, request *http.Request,
) (coverart.Artwork, bool) {
	request.Body = http.MaxBytesReader(response, request.Body, coverUploadBytes)
	file, _, err := request.FormFile("image")
	if err != nil {
		if tooLarge(err) {
			api.coverTooLarge(response)
			return coverart.Artwork{}, false
		}
		api.problem(response, http.StatusBadRequest, "no image was sent",
			[]string{"The request carried no image. Choose a file and try again."})
		return coverart.Artwork{}, false
	}
	defer func() { _ = file.Close() }()

	// One byte past the bound is read on purpose: a file exactly at the bound is
	// kept, and the first byte over it is what says the picture is too big
	// rather than truncating it into one that decodes.
	image, err := io.ReadAll(io.LimitReader(file, coverart.MaxImageBytes+1))
	if err != nil {
		if tooLarge(err) {
			api.coverTooLarge(response)
			return coverart.Artwork{}, false
		}
		api.internalError(response, request, err)
		return coverart.Artwork{}, false
	}
	if len(image) > coverart.MaxImageBytes {
		api.coverTooLarge(response)
		return coverart.Artwork{}, false
	}
	artwork, err := coverart.Supplied(image)
	if err != nil {
		api.notAPicture(response)
		return coverart.Artwork{}, false
	}
	return artwork, true
}

// fetchedCover fetches the picture at the address the person gave.
func (api *API) fetchedCover(
	response http.ResponseWriter, request *http.Request,
) (coverart.Artwork, bool) {
	var input struct {
		URL string `json:"url"`
	}
	if err := decodeJSON(response, request, &input); err != nil {
		api.problem(response, http.StatusBadRequest, "no image was sent",
			[]string{"The request carried no image. Choose a file or paste an address."})
		return coverart.Artwork{}, false
	}
	if strings.TrimSpace(input.URL) == "" {
		api.problem(response, http.StatusBadRequest, "no image was sent",
			[]string{"The request carried no image. Choose a file or paste an address."})
		return coverart.Artwork{}, false
	}
	if api.coverPictures == nil {
		api.problem(response, http.StatusServiceUnavailable, "addresses are not available",
			[]string{"This installation cannot fetch an address. Choose a file instead."})
		return coverart.Artwork{}, false
	}

	artwork, err := api.coverPictures.FromURL(request.Context(), input.URL)
	switch {
	case err == nil:
		return artwork, true
	case errors.Is(err, coverart.ErrNotAnAddress):
		api.problem(response, http.StatusBadRequest, "that is not a web address",
			[]string{"Schall needs an address that starts with http:// or https://."})
	case errors.Is(err, coverart.ErrPictureTooLarge):
		api.coverTooLarge(response)
	// An address Schall refuses to reach is answered exactly as an address with
	// no picture at it. Two different answers would tell whoever is asking which
	// machines and ports exist on the network Schall is running inside, one
	// address at a time.
	case errors.Is(err, coverart.ErrNotAPicture),
		errors.Is(err, coverart.ErrNoArtwork),
		errors.Is(err, coverart.ErrAddressRefused):
		api.problem(response, http.StatusBadRequest, "that address has no image",
			[]string{"Nothing at that address is an image Schall can read. Check the address, or choose a file."})
	default:
		api.logger.Warn().Err(err).Msg("fetch a cover from an address")
		api.problem(response, http.StatusBadGateway, "that address did not answer",
			[]string{"Schall could not reach that address. Try again, or choose a file."})
	}
	return coverart.Artwork{}, false
}

func (api *API) coverTooLarge(response http.ResponseWriter) {
	api.problem(response, http.StatusRequestEntityTooLarge, "that image is too large",
		[]string{"A cover may be up to 8 MB. Choose a smaller image."})
}

func (api *API) notAPicture(response http.ResponseWriter) {
	api.problem(response, http.StatusBadRequest, "that file is not an image",
		[]string{"Schall could not read that file as an image. Choose a JPEG, PNG, GIF or WebP."})
}

// tooLarge reports whether a read stopped because the request was over the
// bound, which net/http reports through an error of its own.
func tooLarge(err error) bool {
	var bound *http.MaxBytesError
	return errors.As(err, &bound)
}

// removeAlbumCover takes back a cover somebody set by hand.
//
// Only theirs: an archive's answer is a cache and stays. Once it is gone the
// release is one nobody has answered about, so the next sweep asks the archives
// again — which is what "remove" means here. A release with no cover of its own
// to remove is not an error, because the outcome asked for is the outcome.
//
// The copy written beside the music is left alone. Deleting a file out of
// somebody's library is a heavier act than changing a row, and the folder keeps
// the picture until something else replaces it.
func (api *API) removeAlbumCover(response http.ResponseWriter, request *http.Request) {
	albumID, ok := api.albumID(response, request)
	if !ok {
		return
	}
	if api.covers == nil {
		api.problem(response, http.StatusServiceUnavailable, "covers are not available",
			[]string{"This installation cannot store covers. Nothing was changed."})
		return
	}
	removed, err := api.covers.DeleteUserReleaseCoverArt(request.Context(), albumID)
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	if removed > 0 {
		api.events.Publish(events.TopicCatalogue)
	}
	response.WriteHeader(http.StatusNoContent)
}

// artistImage serves the picture of an artist, from the cache and only from the
// cache. Unlike a release, nothing here ever fetches: an artist is pictured by
// the sweep, which is the one place that talks to anybody else and the one place
// the pace is set.
func (api *API) artistImage(response http.ResponseWriter, request *http.Request) {
	artistID, err := uuid.Parse(chi.URLParam(request, "artistID"))
	if err != nil || api.covers == nil {
		api.problem(response, http.StatusNotFound, "no artist picture", nil)
		return
	}
	cached, err := api.covers.ArtistImage(request.Context(), artistID)
	if errors.Is(err, pgx.ErrNoRows) {
		api.cachedCoverMiss(response, "no artist picture")
		return
	}
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	api.writeCover(response, request, cached.Image, cached.ContentType)
}

// writeCover answers with the picture, or says there is none. A cover changes
// only when somebody chooses a different edition, so it is cached by the
// browser for a day; the ETag is the hash of the bytes, so a phone already
// holding this cover confirms it with a 304 instead of downloading it again.
// ServeContent also answers a Range request, which nothing here needs today
// but costs nothing to have. Private because the route sits behind Schall's
// own auth, not a cache anything else should share.
func (api *API) writeCover(response http.ResponseWriter, request *http.Request, image []byte, contentType string) {
	if len(image) == 0 {
		api.cachedCoverMiss(response, "no cover art")
		return
	}
	sum := sha256.Sum256(image)
	response.Header().Set("Content-Type", contentType)
	response.Header().Set("ETag", `"`+hex.EncodeToString(sum[:])+`"`)
	response.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeContent(response, request, "", time.Time{}, bytes.NewReader(image))
}

// cachedCoverMiss answers a known absence. The sweep and the import fill
// covers later, so the browser holds the miss for an hour, not the day a
// picture gets.
func (api *API) cachedCoverMiss(response http.ResponseWriter, message string) {
	response.Header().Set("Cache-Control", "public, max-age=3600")
	api.problem(response, http.StatusNotFound, message, nil)
}

func (api *API) albumID(response http.ResponseWriter, request *http.Request) (uuid.UUID, bool) {
	albumID, err := uuid.Parse(chi.URLParam(request, "albumID"))
	if err != nil {
		api.problem(response, http.StatusNotFound, "release not found", nil)
		return uuid.Nil, false
	}
	return albumID, true
}

type createArtistRequest struct {
	MusicBrainzID uuid.UUID `json:"musicbrainzId"`
	Name          string    `json:"name"`
	SortName      string    `json:"sortName"`
}

func (api *API) createArtist(response http.ResponseWriter, request *http.Request) {
	var input createArtistRequest
	if err := decodeJSON(response, request, &input); err != nil {
		api.problem(response, http.StatusBadRequest, "invalid request body", []string{err.Error()})
		return
	}

	input.Name = strings.TrimSpace(input.Name)
	input.SortName = strings.TrimSpace(input.SortName)
	if input.MusicBrainzID == uuid.Nil {
		api.problem(response, http.StatusUnprocessableEntity, "validation failed", []string{"musicbrainzId is required"})
		return
	}
	if input.Name == "" {
		api.problem(response, http.StatusUnprocessableEntity, "validation failed", []string{"name is required"})
		return
	}
	if len(input.Name) > 300 {
		api.problem(response, http.StatusUnprocessableEntity, "validation failed", []string{"name must be 300 characters or fewer"})
		return
	}
	if input.SortName == "" {
		api.problem(response, http.StatusUnprocessableEntity, "validation failed", []string{"sortName is required"})
		return
	}
	if len(input.SortName) > 300 {
		api.problem(response, http.StatusUnprocessableEntity, "validation failed", []string{"sortName must be 300 characters or fewer"})
		return
	}

	artist, err := api.store.CreateArtist(request.Context(), db.CreateArtistParams{
		MusicbrainzID: uuid.NullUUID{UUID: input.MusicBrainzID, Valid: true},
		Name:          input.Name,
		SortName:      input.SortName,
	})
	// An artist Schall already holds is promoted rather than rejected, and the
	// query returns no row only when the artist was genuinely already followed.
	if errors.Is(err, pgx.ErrNoRows) {
		api.problem(response, http.StatusConflict, "artist is already followed", nil)
		return
	}
	if err != nil {
		var pgError *pgconn.PgError
		if errors.As(err, &pgError) && pgError.Code == "23505" {
			api.problem(response, http.StatusConflict, "artist is already followed", nil)
			return
		}
		api.internalError(response, request, err)
		return
	}

	api.writeJSON(response, http.StatusCreated, artistResponse{
		ID:              artist.ID,
		MusicBrainzID:   nullableUUID(artist.MusicbrainzID),
		Name:            artist.Name,
		SortName:        artist.SortName,
		Followed:        artist.FollowedAt.Valid,
		FollowedAt:      nullableTime(artist.FollowedAt.Valid, artist.FollowedAt.Time),
		LastRefreshedAt: nullableTime(artist.LastRefreshedAt.Valid, artist.LastRefreshedAt.Time),
		RefreshStatus:   "queued",
	})
}

type artistSearchResponse struct {
	MusicBrainzID  uuid.UUID `json:"musicbrainzId"`
	Name           string    `json:"name"`
	SortName       string    `json:"sortName"`
	Type           string    `json:"type,omitempty"`
	Country        string    `json:"country,omitempty"`
	Area           string    `json:"area,omitempty"`
	Disambiguation string    `json:"disambiguation,omitempty"`
	Score          int       `json:"score"`
}

func (api *API) searchArtists(response http.ResponseWriter, request *http.Request) {
	query := strings.TrimSpace(request.URL.Query().Get("q"))
	if len([]rune(query)) < 2 {
		api.problem(response, http.StatusUnprocessableEntity, "validation failed", []string{"q must be at least 2 characters"})
		return
	}
	if len([]rune(query)) > 200 {
		api.problem(response, http.StatusUnprocessableEntity, "validation failed", []string{"q must be 200 characters or fewer"})
		return
	}

	artists, err := api.artists.SearchArtists(request.Context(), query, 10)
	if err != nil {
		var providerError *musicbrainz.HTTPError
		if errors.As(err, &providerError) && providerError.StatusCode == http.StatusServiceUnavailable {
			api.problem(response, http.StatusServiceUnavailable, "MusicBrainz is temporarily unavailable", nil)
			return
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		api.logger.Error().Err(err).Msg("MusicBrainz artist search failed")
		api.problem(response, http.StatusBadGateway, "artist search provider failed", nil)
		return
	}

	result := make([]artistSearchResponse, 0, len(artists))
	for _, artist := range artists {
		result = append(result, artistSearchResponse{
			MusicBrainzID:  artist.ID,
			Name:           artist.Name,
			SortName:       artist.SortName,
			Type:           artist.Type,
			Country:        artist.Country,
			Area:           artist.Area,
			Disambiguation: artist.Disambiguation,
			Score:          artist.Score,
		})
	}
	api.writeJSON(response, http.StatusOK, map[string]any{"items": result})
}

func nullableUUID(value uuid.NullUUID) *uuid.UUID {
	if !value.Valid {
		return nil
	}
	result := value.UUID
	return &result
}

func nullableTime(valid bool, value time.Time) *time.Time {
	if !valid {
		return nil
	}
	return &value
}

func textPointer(value pgtype.Text) *string {
	if !value.Valid {
		return nil
	}
	result := value.String
	return &result
}

func uuidPointer(value uuid.NullUUID) *uuid.UUID {
	if !value.Valid {
		return nil
	}
	result := value.UUID
	return &result
}

func boolPointer(value pgtype.Bool) *bool {
	if !value.Valid {
		return nil
	}
	result := value.Bool
	return &result
}

func int32Pointer(value pgtype.Int4) *int32 {
	if !value.Valid {
		return nil
	}
	result := value.Int32
	return &result
}

func int64Pointer(value pgtype.Int8) *int64 {
	if !value.Valid {
		return nil
	}
	result := value.Int64
	return &result
}

func float32Pointer(value pgtype.Float4) *float32 {
	if !value.Valid {
		return nil
	}
	result := value.Float32
	return &result
}

func isUniqueViolation(err error) bool {
	var pgError *pgconn.PgError
	return errors.As(err, &pgError) && pgError.Code == "23505"
}

func decodeJSON(response http.ResponseWriter, request *http.Request, destination any) error {
	request.Body = http.MaxBytesReader(response, request.Body, maxRequestBody)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain one JSON object")
	}
	return nil
}

func (api *API) writeJSON(response http.ResponseWriter, status int, payload any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	if err := json.NewEncoder(response).Encode(payload); err != nil {
		api.logger.Error().Err(err).Msg("encode response")
	}
}

func (api *API) problem(response http.ResponseWriter, status int, title string, details []string) {
	api.writeJSON(response, status, map[string]any{
		"title":   title,
		"status":  status,
		"details": details,
	})
}

func (api *API) internalError(response http.ResponseWriter, request *http.Request, err error) {
	api.logger.Error().
		Err(err).
		Str("request_id", middleware.GetReqID(request.Context())).
		Msg("request failed")
	api.problem(response, http.StatusInternalServerError, "internal server error", nil)
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (recorder *statusRecorder) WriteHeader(status int) {
	recorder.status = status
	recorder.ResponseWriter.WriteHeader(status)
}

// Unwrap lets http.NewResponseController reach the real writer through this
// one. Without it a handler cannot flush or lift its write deadline, which the
// event stream needs to do both of.
func (recorder *statusRecorder) Unwrap() http.ResponseWriter { return recorder.ResponseWriter }

func (api *API) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		start := time.Now()
		recorder := &statusRecorder{ResponseWriter: response, status: http.StatusOK}
		// The actor is found by the middleware inside this one, so it is read
		// through a holder put on the request here rather than out of the
		// context, which only carries values inwards.
		request, actor := carryActor(request)
		next.ServeHTTP(recorder, request)
		api.logger.Info().
			Str("method", request.Method).
			Str("path", request.URL.Path).
			Int("status", recorder.status).
			Dur("duration", time.Since(start)).
			Str("request_id", middleware.GetReqID(request.Context())).
			Str("actor", actor.Name).
			Msg("request")
	})
}

// keepOneOfEachCopy settles every recording the library holds more than once,
// keeping one copy of each and deleting the rest.
//
// It is the duplicates screen's own press made once per row, and it is licensed
// exactly the same way — a row saying what went and what stayed, committed
// before the audio is unlinked. What it will not do is choose where the copies
// cannot be shown to be the same audio; those recordings are named in the
// answer and left as they were, because a person saying "any of them is fine"
// said it about copies.
func (api *API) keepOneOfEachCopy(response http.ResponseWriter, request *http.Request) {
	if api.remover == nil {
		api.problem(response, http.StatusServiceUnavailable, "deleting files is unavailable",
			[]string{"This installation cannot remove files from the library."})
		return
	}
	swept, err := api.remover.KeepOneOfEach(request.Context(), library.DuplicateScreenRemovalActor)
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	api.logger.Info().
		Int("resolved", swept.Resolved).
		Int("removed", len(swept.RemovedPaths)).
		Int("left_alone", swept.LeftAlone).
		Msg("one copy was kept of every recording held more than once")
	api.events.Publish(events.TopicLibrary)
	api.writeJSON(response, http.StatusOK, swept)
}
