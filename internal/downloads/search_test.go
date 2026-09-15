package downloads

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/events"
	"github.com/pxldi/schall/internal/slskd"
	"github.com/pxldi/schall/internal/sources"
	"github.com/rs/zerolog"
)

type fakeSearchStore struct {
	// What the user said to prefer among the copies on offer. The zero value is
	// an installation that has said nothing, which is what most tests are.
	preferences db.SourcePreferencesRow
	album       db.SourceSearchAlbum
	claimErr    error
	settings    db.SlskdSettingsRow
	recorded    []db.RecordSourceSearchParams
	// duplicates is what the library already holds against the release, which
	// is the one thing a corroborated candidate must still not decide.
	duplicates    db.DuplicateEvidence
	requested     []db.CreateDownloadRequestParams
	autoRequested []uuid.UUID
	// What the database refuses, each at exactly one call.
	duplicatesErr error
	requestErr    error
	markErr       error
	recordErr     error
	settingsErr   error
}

func (store *fakeSearchStore) AlbumDuplicateEvidence(
	context.Context, uuid.UUID,
) (db.DuplicateEvidence, error) {
	return store.duplicates, store.duplicatesErr
}

func (store *fakeSearchStore) CreateDownloadRequest(
	_ context.Context, params db.CreateDownloadRequestParams,
) (uuid.UUID, bool, error) {
	if store.requestErr != nil {
		return uuid.Nil, false, store.requestErr
	}
	store.requested = append(store.requested, params)
	return uuid.New(), true, nil
}

func (store *fakeSearchStore) MarkSourceSearchAutoRequested(
	_ context.Context, _, albumID uuid.UUID, _ time.Time,
) error {
	if store.markErr != nil {
		return store.markErr
	}
	store.autoRequested = append(store.autoRequested, albumID)
	return nil
}

func (store *fakeSearchStore) ClaimSourceSearch(
	context.Context, uuid.UUID, uuid.UUID,
) (db.SourceSearchAlbum, error) {
	return store.album, store.claimErr
}

func (store *fakeSearchStore) RecordSourceSearch(
	_ context.Context, params db.RecordSourceSearchParams,
) error {
	if store.recordErr != nil {
		return store.recordErr
	}
	store.recorded = append(store.recorded, params)
	return nil
}

func (store *fakeSearchStore) SlskdSettings(context.Context) (db.SlskdSettingsRow, error) {
	return store.settings, store.settingsErr
}

// testSearcher drops the pause before an empty result is asked again. The pause
// is there for a real network, and waiting it out would buy a test nothing.
func testSearcher(store SearchStore, providers sources.Builder) *Searcher {
	searcher := NewSearcher(store, providers, zerolog.Nop())
	searcher.emptyRetryDelay = 0
	return searcher
}

// rungs is a ladder of phrases and nothing else, for the tests that care only
// what a search asked. A ladder that turns on what a rung was made of is built
// by sources.TrackQueryRungs instead, from the catalogue text that made it.
func rungs(texts ...string) []sources.Rung {
	ladder := make([]sources.Rung, 0, len(texts))
	for _, text := range texts {
		ladder = append(ladder, sources.Rung{Text: text})
	}
	return ladder
}

func searchableAlbum() db.SourceSearchAlbum {
	return db.SourceSearchAlbum{
		AlbumID: uuid.New(), AlbumTitle: "Geogaddi",
		ArtistName: "Boards of Canada", TrackCount: 23,
	}
}

func TestSearchRecordsTheCandidatesTheProviderOffered(t *testing.T) {
	store := &fakeSearchStore{album: searchableAlbum(), settings: enabledSettings()}
	downloader := &fakeDownloader{candidates: []sources.Candidate{{
		Provider: "fake", Username: "peer", Directory: `@@peer\Geogaddi`,
		Format: "flac", Score: 0.9, Reasons: []string{"Lossless FLAC"},
		Files: []sources.File{{Path: `@@peer\Geogaddi\01.flac`, Name: "01.flac", SizeBytes: 100}},
	}}}
	searcher := testSearcher(store, downloader)

	if err := searcher.Search(context.Background(), uuid.New(), store.album.AlbumID); err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(store.recorded) != 1 {
		t.Fatalf("recorded %d results, want 1", len(store.recorded))
	}
	recorded := store.recorded[0]
	if recorded.Status != "found" || recorded.Query == "" {
		t.Fatalf("recorded = %#v", recorded)
	}
	if len(recorded.Candidates) != 1 || recorded.Candidates[0].Username != "peer" {
		t.Fatalf("recorded candidates = %#v", recorded.Candidates)
	}
	// The remote path is what a transfer has to ask for, so it has to survive
	// being stored; a candidate without it could never be acted on.
	if len(recorded.Candidates[0].Files) != 1 ||
		recorded.Candidates[0].Files[0].Path != `@@peer\Geogaddi\01.flac` {
		t.Fatalf("recorded files = %#v", recorded.Candidates[0].Files)
	}
}

// The catalogue is what a candidate is corroborated against, so it has to reach
// the provider. Without it the review screen can only show how good a copy is.
func TestSearchSendsTheCatalogueTracksToTheProvider(t *testing.T) {
	album := searchableAlbum()
	album.Tracks = []db.SourceSearchTrack{
		{Title: "Ready Lets Go", DurationSeconds: 59},
		{Title: "Music Is Math", DurationSeconds: 322},
	}
	store := &fakeSearchStore{album: album, settings: enabledSettings()}
	downloader := &fakeDownloader{}
	searcher := testSearcher(store, downloader)

	if err := searcher.Search(context.Background(), uuid.New(), album.AlbumID); err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(downloader.query.Tracks) != 2 {
		t.Fatalf("provider saw %d tracks, want both catalogue tracks", len(downloader.query.Tracks))
	}
	if downloader.query.Tracks[1].Title != "Music Is Math" ||
		downloader.query.Tracks[1].DurationSeconds != 322 {
		t.Fatalf("track = %#v", downloader.query.Tracks[1])
	}
}

// The match is the reason a decision was offered the way it was, so it is
// stored rather than recomputed: the catalogue can change afterwards.
func TestSearchStoresWhatCorroboratedEachCandidate(t *testing.T) {
	store := &fakeSearchStore{album: searchableAlbum(), settings: enabledSettings()}
	downloader := &fakeDownloader{candidates: []sources.Candidate{{
		Provider: "fake", Username: "peer", Directory: `@@peer\Geogaddi`,
		Format: "flac", Score: 0.9,
		Files: []sources.File{{Path: `@@peer\Geogaddi\01.flac`, Name: "01.flac", SizeBytes: 100}},
		Match: sources.Match{Expected: 23, Confirmed: 21, Partial: 1, Absent: 1},
	}}}
	searcher := testSearcher(store, downloader)

	if err := searcher.Search(context.Background(), uuid.New(), store.album.AlbumID); err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	stored := store.recorded[0].Candidates[0].Match
	if stored.Expected != 23 || stored.Confirmed != 21 || stored.Partial != 1 || stored.Absent != 1 {
		t.Fatalf("stored match = %#v, want the corroboration the ranking found", stored)
	}
}

// No peer sharing the release is an answer. Recording it is what keeps a
// release from being asked about forever.
func TestSearchRecordsAnEmptyResultAsAnAnswer(t *testing.T) {
	store := &fakeSearchStore{album: searchableAlbum(), settings: enabledSettings()}
	searcher := testSearcher(store, &fakeDownloader{})

	if err := searcher.Search(context.Background(), uuid.New(), store.album.AlbumID); err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(store.recorded) != 1 || store.recorded[0].Status != "none" {
		t.Fatalf("recorded = %#v", store.recorded)
	}
	if store.recorded[0].Error != "" {
		t.Errorf("an empty result recorded a failure: %q", store.recorded[0].Error)
	}
}

// Soulseek search is a broadcast that collects replies for a bounded window, so
// an empty result means nobody answered in time. Replaying the identical query
// has returned a full set of candidates on a real network, and a release
// recorded as unavailable is one nobody ever goes back to.
func TestSearchAsksAgainWhenTheFirstAttemptFindsNothing(t *testing.T) {
	store := &fakeSearchStore{album: searchableAlbum(), settings: enabledSettings()}
	downloader := &fakeDownloader{results: [][]sources.Candidate{
		nil,
		{{
			Provider: "fake", Username: "slow-peer", Directory: `@@slow\Geogaddi`,
			Format: "flac",
			Files:  []sources.File{{Path: `@@slow\Geogaddi\01.flac`, Name: "01.flac", SizeBytes: 100}},
		}},
	}}
	searcher := testSearcher(store, downloader)

	if err := searcher.Search(context.Background(), uuid.New(), store.album.AlbumID); err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(store.recorded) != 1 || store.recorded[0].Status != "found" {
		t.Fatalf("recorded = %#v, want the second attempt's candidates", store.recorded)
	}
	if len(store.recorded[0].Candidates) != 1 ||
		store.recorded[0].Candidates[0].Username != "slow-peer" {
		t.Fatalf("recorded candidates = %#v", store.recorded[0].Candidates)
	}
}

// Rate limiting is backpressure, not an answer. It must reach the worker intact
// so the job can back off, and it must never settle the search.
func TestSearchSurfacesRateLimitingWithoutRecordingAnything(t *testing.T) {
	store := &fakeSearchStore{album: searchableAlbum(), settings: enabledSettings()}
	downloader := &fakeDownloader{
		searchErr: fmt.Errorf("%w: slskd is refusing further searches", sources.ErrRateLimited),
	}
	searcher := testSearcher(store, downloader)

	err := searcher.Search(context.Background(), uuid.New(), store.album.AlbumID)
	if !errors.Is(err, sources.ErrRateLimited) {
		t.Fatalf("Search() error = %v, want it to report rate limiting", err)
	}
	if len(store.recorded) != 0 {
		t.Fatalf("recorded %#v, want a rate-limited search to settle nothing", store.recorded)
	}
}

// corroborated is a candidate whose files account for every catalogue track,
// which is the only thing a run is allowed to act on without asking.
func corroborated(tracks int) sources.Candidate {
	return sources.Candidate{
		Provider: "fake", Username: "peer", Directory: `@@peer\Geogaddi`,
		Format: "flac", Score: 0.9,
		Files: []sources.File{{Path: `@@peer\Geogaddi\01.flac`, Name: "01.flac", SizeBytes: 100}},
		Match: sources.Match{Expected: tracks, Confirmed: tracks},
	}
}

func autoRequestingAlbum() db.SourceSearchAlbum {
	album := searchableAlbum()
	album.AutoRequest = true
	return album
}

func TestSearchRecordsTheDownloadWhenEveryTrackIsConfirmed(t *testing.T) {
	store := &fakeSearchStore{album: autoRequestingAlbum(), settings: enabledSettings()}
	downloader := &fakeDownloader{candidates: []sources.Candidate{corroborated(23)}}
	searcher := testSearcher(store, downloader)

	if err := searcher.Search(context.Background(), uuid.New(), store.album.AlbumID); err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(store.requested) != 1 {
		t.Fatalf("recorded %d downloads, want the corroborated candidate taken", len(store.requested))
	}
	if store.requested[0].SourceUsername != "peer" ||
		store.requested[0].SourceDirectory != `@@peer\Geogaddi` {
		t.Fatalf("requested = %#v, want the folder that was corroborated", store.requested[0])
	}
	if len(store.autoRequested) != 1 {
		t.Fatal("the search did not record that it had acted on its own")
	}
}

// Permission is given per run. A run started without it records nothing, no
// matter how well corroborated the candidate is.
func TestSearchRecordsNoDownloadWithoutPermission(t *testing.T) {
	store := &fakeSearchStore{album: searchableAlbum(), settings: enabledSettings()}
	downloader := &fakeDownloader{candidates: []sources.Candidate{corroborated(23)}}
	searcher := testSearcher(store, downloader)

	if err := searcher.Search(context.Background(), uuid.New(), store.album.AlbumID); err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(store.requested) != 0 {
		t.Fatalf("recorded %#v, want nothing without permission", store.requested)
	}
}

// The score measures how good a copy is and says nothing about which release it
// holds. A candidate that scores well but accounts for only part of the
// catalogue is exactly the case the review screen exists for.
func TestSearchLeavesAnUncorroboratedCandidateForReview(t *testing.T) {
	store := &fakeSearchStore{album: autoRequestingAlbum(), settings: enabledSettings()}
	best := corroborated(23)
	best.Score = 1
	best.Match = sources.Match{Expected: 23, Confirmed: 22, Absent: 1}
	downloader := &fakeDownloader{candidates: []sources.Candidate{best}}
	searcher := testSearcher(store, downloader)

	if err := searcher.Search(context.Background(), uuid.New(), store.album.AlbumID); err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(store.requested) != 0 {
		t.Fatalf("recorded %#v, want one missing track to be enough to ask", store.requested)
	}
}

// Whether to acquire music the library already holds is a decision about the
// user's own files. Corroboration answers a different question and must not be
// mistaken for an answer to this one.
func TestSearchLeavesADuplicateForReviewHoweverWellCorroborated(t *testing.T) {
	store := &fakeSearchStore{
		album: autoRequestingAlbum(), settings: enabledSettings(),
		duplicates: db.DuplicateEvidence{
			OwnedTrackCount: 4,
			Files: []db.DuplicateFile{
				{ID: uuid.New(), Path: "/music/Boards of Canada/Geogaddi/01.flac", State: "owned"},
			},
		},
	}
	downloader := &fakeDownloader{candidates: []sources.Candidate{corroborated(23)}}
	searcher := testSearcher(store, downloader)

	if err := searcher.Search(context.Background(), uuid.New(), store.album.AlbumID); err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(store.requested) != 0 {
		t.Fatalf("recorded %#v, want the duplicate question left for the user", store.requested)
	}
	if len(store.autoRequested) != 0 {
		t.Fatal("the search marked itself as having acted when it had not")
	}
}

// APE ranks as lossless (sources.losslessExtensions) but a scan never indexes
// it (library.supportedExtensions), so fetching it automatically would leave a
// file the library can prove but never hold, waiting on a row no scan would
// ever create — the AIFF failure library/scanner.go describes, on the one
// path that used not to ask the question.
func TestSearchLeavesAFormatTheLibraryCannotHoldForReview(t *testing.T) {
	store := &fakeSearchStore{album: autoRequestingAlbum(), settings: enabledSettings()}
	best := corroborated(23)
	best.Format = "ape"
	best.Files = []sources.File{
		{Path: `@@peer\Geogaddi\01.ape`, Name: "01.ape", Extension: "ape", SizeBytes: 100},
	}
	downloader := &fakeDownloader{candidates: []sources.Candidate{best}}
	searcher := testSearcher(store, downloader)

	if err := searcher.Search(context.Background(), uuid.New(), store.album.AlbumID); err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(store.requested) != 0 {
		t.Fatalf("recorded %#v, want a format no scan indexes left for review", store.requested)
	}
	if len(store.autoRequested) != 0 {
		t.Fatal("the search marked itself as having acted when it had not")
	}
}

// A query with no catalogue tracks corroborates nothing, so Match is empty. An
// empty match must read as "not checked", never as "nothing to confirm".
func TestSearchRecordsNoDownloadWhenThereWasNothingToCorroborateAgainst(t *testing.T) {
	store := &fakeSearchStore{album: autoRequestingAlbum(), settings: enabledSettings()}
	downloader := &fakeDownloader{candidates: []sources.Candidate{corroborated(0)}}
	searcher := testSearcher(store, downloader)

	if err := searcher.Search(context.Background(), uuid.New(), store.album.AlbumID); err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(store.requested) != 0 {
		t.Fatalf("recorded %#v, want an unchecked candidate to be left alone", store.requested)
	}
}

// A provider that could not be searched has said nothing about the release, so
// nothing is recorded and the job is left to try again.
func TestSearchRecordsNothingWhenTheProviderCannotBeSearched(t *testing.T) {
	store := &fakeSearchStore{album: searchableAlbum(), settings: enabledSettings()}
	downloader := &fakeDownloader{searchErr: errors.New("slskd is unreachable")}
	searcher := testSearcher(store, downloader)

	err := searcher.Search(context.Background(), uuid.New(), store.album.AlbumID)
	if err == nil {
		t.Fatal("Search() error = nil, want the provider failure to surface")
	}
	if len(store.recorded) != 0 {
		t.Fatalf("recorded %#v, want nothing", store.recorded)
	}
}

func TestSearchRefusesWithoutAnEnabledProvider(t *testing.T) {
	store := &fakeSearchStore{album: searchableAlbum()}
	searcher := testSearcher(store, &fakeDownloader{})

	if err := searcher.Search(
		context.Background(), uuid.New(), store.album.AlbumID,
	); !errors.Is(err, sources.ErrNotConfigured) {
		t.Fatalf("Search() error = %v, want %v", err, sources.ErrNotConfigured)
	}
}

// A release whose name normalises to nothing cannot be looked for. It is an
// answer rather than a failure, and it says which it is.
func TestSearchRecordsAReleaseThereIsNothingToSearchFor(t *testing.T) {
	album := searchableAlbum()
	album.ArtistName, album.AlbumTitle = "", ""
	store := &fakeSearchStore{album: album, settings: enabledSettings()}
	downloader := &fakeDownloader{}
	searcher := testSearcher(store, downloader)

	if err := searcher.Search(context.Background(), uuid.New(), album.AlbumID); err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(store.recorded) != 1 || store.recorded[0].Status != "none" {
		t.Fatalf("recorded = %#v", store.recorded)
	}
	if !strings.Contains(store.recorded[0].Error, "nothing that could be searched for") {
		t.Fatalf("recorded error = %q", store.recorded[0].Error)
	}
	if len(downloader.searched) != 0 {
		t.Fatalf("searched %v for a release with no name", downloader.searched)
	}
}

// A title of nothing but symbols leaves the artist's name standing on its own,
// which is a question about their whole discography rather than about this
// release. There is no phrase to run, and the run says so rather than asking it.
func TestSearchRecordsAReleaseWhoseTitleLeavesOnlyTheArtist(t *testing.T) {
	album := searchableAlbum()
	album.ArtistName, album.AlbumTitle = "Cynthoni", "｡ﾟ･ (>﹏<) ･ﾟ｡"
	store := &fakeSearchStore{album: album, settings: enabledSettings()}
	downloader := &fakeDownloader{}
	searcher := testSearcher(store, downloader)

	if err := searcher.Search(context.Background(), uuid.New(), album.AlbumID); err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(downloader.searched) != 0 {
		t.Fatalf("searched %q, want the artist's name never put to the network",
			downloader.searched)
	}
}

// A release nobody could claim is one another worker may already be searching
// for, so nothing is recorded against it.
func TestSearchRecordsNothingForAReleaseItCouldNotClaim(t *testing.T) {
	store := &fakeSearchStore{
		album: searchableAlbum(), settings: enabledSettings(),
		claimErr: errors.New("the runs table is unreachable"),
	}
	searcher := testSearcher(store, &fakeDownloader{})

	err := searcher.Search(context.Background(), uuid.New(), store.album.AlbumID)
	if !errors.Is(err, store.claimErr) {
		t.Fatalf("error = %v, want the claim failure reported", err)
	}
	if len(store.recorded) != 0 {
		t.Fatalf("recorded %#v about a release nobody claimed", store.recorded)
	}
}

// Settings nobody could read are a failure to retry, not a provider nobody
// configured.
func TestSearchRecordsNothingWhenTheProviderSettingsCannotBeRead(t *testing.T) {
	store := &fakeSearchStore{
		album: searchableAlbum(), settingsErr: errors.New("the settings table is unreachable"),
	}
	searcher := testSearcher(store, &fakeDownloader{})

	if err := searcher.Search(
		context.Background(), uuid.New(), store.album.AlbumID,
	); !errors.Is(err, store.settingsErr) {
		t.Fatalf("error = %v, want the read failure reported", err)
	}
}

// A run that is being shut down does not sit out the pause before asking again.
func TestSearchStopsWhenTheRunIsCancelledBeforeAskingAgain(t *testing.T) {
	store := &fakeSearchStore{album: searchableAlbum(), settings: enabledSettings()}
	downloader := &fakeDownloader{}
	searcher := testSearcher(store, downloader)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := searcher.Search(ctx, uuid.New(), store.album.AlbumID); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want the cancellation reported", err)
	}
	if len(store.recorded) != 0 {
		t.Fatalf("recorded %#v for a run that was stopped", store.recorded)
	}
}

// The second attempt is the one that decides an empty result, so a provider
// that failed on it has said nothing either.
func TestSearchRecordsNothingWhenTheSecondAttemptFails(t *testing.T) {
	store := &fakeSearchStore{album: searchableAlbum(), settings: enabledSettings()}
	downloader := &fakeDownloader{retrySearchErr: errors.New("slskd is unreachable")}
	searcher := testSearcher(store, downloader)

	err := searcher.Search(context.Background(), uuid.New(), store.album.AlbumID)
	if !errors.Is(err, downloader.retrySearchErr) {
		t.Fatalf("error = %v, want the provider failure reported", err)
	}
	if len(store.recorded) != 0 {
		t.Fatalf("recorded %#v, want nothing", store.recorded)
	}
}

// A result that could not be stored leaves the release still waiting, so the
// job fails and is tried again rather than reporting an answer nobody kept.
func TestSearchFailsWhenTheResultCannotBeRecorded(t *testing.T) {
	store := &fakeSearchStore{
		album: searchableAlbum(), settings: enabledSettings(),
		recordErr: errors.New("the searches table is unreachable"),
	}
	searcher := testSearcher(store, &fakeDownloader{candidates: []sources.Candidate{corroborated(23)}})

	if err := searcher.Search(
		context.Background(), uuid.New(), store.album.AlbumID,
	); !errors.Is(err, store.recordErr) {
		t.Fatalf("error = %v, want the write failure reported", err)
	}
}

// Nothing found is nothing to act on, however freely the run was permitted to
// act.
func TestSearchRecordsNoDownloadWhenNothingWasFound(t *testing.T) {
	store := &fakeSearchStore{album: autoRequestingAlbum(), settings: enabledSettings()}
	searcher := testSearcher(store, &fakeDownloader{})

	if err := searcher.Search(context.Background(), uuid.New(), store.album.AlbumID); err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(store.requested) != 0 {
		t.Fatalf("recorded %#v with nothing found", store.requested)
	}
}

// Whether the library already holds the release is a question that has to be
// answered before acting, so an answer nobody could read stops the acting.
func TestSearchRecordsNoDownloadWhenTheDuplicateCheckCannotBeRead(t *testing.T) {
	store := &fakeSearchStore{
		album: autoRequestingAlbum(), settings: enabledSettings(),
		duplicatesErr: errors.New("the library tables are unreachable"),
	}
	searcher := testSearcher(store, &fakeDownloader{candidates: []sources.Candidate{corroborated(23)}})

	if err := searcher.Search(context.Background(), uuid.New(), store.album.AlbumID); err != nil {
		t.Fatalf("a duplicate check that failed also failed the search: %v", err)
	}
	if len(store.requested) != 0 {
		t.Fatalf("recorded %#v without knowing what the library holds", store.requested)
	}
	if len(store.recorded) != 1 || store.recorded[0].Status != "found" {
		t.Fatalf("recorded = %#v, want the search itself still answered", store.recorded)
	}
}

// The search has been answered and stored by the time a download is recorded,
// so a request that cannot be recorded leaves the release in the review screen
// rather than undoing an answer that was correct.
func TestSearchStandsWhenTheDownloadCannotBeRecorded(t *testing.T) {
	store := &fakeSearchStore{
		album: autoRequestingAlbum(), settings: enabledSettings(),
		requestErr: errors.New("the requests table is unreachable"),
	}
	searcher := testSearcher(store, &fakeDownloader{candidates: []sources.Candidate{corroborated(23)}})

	if err := searcher.Search(context.Background(), uuid.New(), store.album.AlbumID); err != nil {
		t.Fatalf("an unrecordable download failed the search: %v", err)
	}
	if len(store.recorded) != 1 || store.recorded[0].Status != "found" {
		t.Fatalf("recorded = %#v, want the answer kept", store.recorded)
	}
	if len(store.autoRequested) != 0 {
		t.Fatal("the search marked itself as having acted when it had not")
	}
}

// The download is recorded and the mark is bookkeeping, so a mark that failed
// must not undo it.
func TestARecordedDownloadStandsWhenTheSearchCannotBeMarked(t *testing.T) {
	store := &fakeSearchStore{
		album: autoRequestingAlbum(), settings: enabledSettings(),
		markErr: errors.New("the searches table is unreachable"),
	}
	searcher := testSearcher(store, &fakeDownloader{candidates: []sources.Candidate{corroborated(23)}})

	if err := searcher.Search(context.Background(), uuid.New(), store.album.AlbumID); err != nil {
		t.Fatalf("an unmarked search failed after recording a download: %v", err)
	}
	if len(store.requested) != 1 {
		t.Fatalf("recorded %d downloads, want the one that was decided", len(store.requested))
	}
}

// The bit rate is part of what the decision was made on, so it is stored with
// the rest of the evidence rather than recomputed later from the files.
func TestARecordedDownloadKeepsTheBitRateItWasDecidedOn(t *testing.T) {
	store := &fakeSearchStore{album: autoRequestingAlbum(), settings: enabledSettings()}
	best := corroborated(23)
	best.AverageBitRate = 987
	searcher := testSearcher(store, &fakeDownloader{candidates: []sources.Candidate{best}})

	if err := searcher.Search(context.Background(), uuid.New(), store.album.AlbumID); err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(store.requested) != 1 {
		t.Fatalf("recorded %d downloads, want one", len(store.requested))
	}
	rate := store.requested[0].AverageBitRate
	if !rate.Valid || rate.Int32 != 987 {
		t.Fatalf("bit rate = %+v, want the one the candidate was chosen on", rate)
	}
}

// A run fills a review screen for minutes on end, so each release it answers is
// announced rather than waiting for the interface's next timer.
func TestASearchedReleaseTellsTheInterface(t *testing.T) {
	store := &fakeSearchStore{album: searchableAlbum(), settings: enabledSettings()}
	hub := events.NewHub()
	notices, unsubscribe := hub.Subscribe()
	defer unsubscribe()
	searcher := testSearcher(store, &fakeDownloader{}).WithEvents(hub)

	if err := searcher.Search(context.Background(), uuid.New(), store.album.AlbumID); err != nil {
		t.Fatalf("Search() error = %v", err)
	}

	select {
	case topic := <-notices:
		if topic != events.TopicSourceSearches {
			t.Fatalf("notice = %q, want the source-searches topic", topic)
		}
	default:
		t.Fatal("a searched release published nothing")
	}
}

// SearchFor is the same search with no run and no release behind it: a want has
// neither, and it reports which phrase found what.
func TestSearchBudgetFitsTheWholeLadderWithHeadroom(t *testing.T) {
	const perRung = 30 * time.Second
	ladder := sources.TrackQueryRungs("Cynthoni",
		"Lvstlove (Night Drive At 177mph Mix) - EP",
		"Draining Love Story (In the Eyes of Cynthoni)")
	if len(ladder) == 0 {
		t.Fatal("the full track ladder has no rungs")
	}

	searcher := NewSearcher(&fakeSearchStore{}, &fakeDownloader{}, zerolog.Nop())
	budget := searcher.searchBudget(len(ladder), perRung)
	want := time.Duration(len(ladder)+1)*perRung + emptyRetryDelay + searchHeadroom
	if budget != want {
		t.Fatalf("searchBudget() = %s, want %s", budget, want)
	}
	if budget-time.Duration(len(ladder))*perRung <= 0 {
		t.Fatalf("searchBudget() = %s, want headroom beyond every rung", budget)
	}
}

// A rung that finds nothing spends its whole reply window, so a want nobody
// answers reaches the second look having spent the ladder exactly. What is left
// at that point has to pay for the pause and one more reply window, or the
// second look is refused in the one case it was written for (issue #301).
func TestSearchBudgetPaysForTheSecondLookAfterAFruitlessLadder(t *testing.T) {
	const perRung = 30 * time.Second
	ladder := sources.TrackQueryRungs("Anetha", "Lvstlove (Night Drive At 177mph Mix)", "Lvstlove")
	if len(ladder) == 0 {
		t.Fatal("the full track ladder has no rungs")
	}

	searcher := NewSearcher(&fakeSearchStore{}, &fakeDownloader{}, zerolog.Nop())
	budget := searcher.searchBudget(len(ladder), perRung)

	left := budget - time.Duration(len(ladder))*perRung
	if want := emptyRetryDelay + perRung; left < want {
		t.Fatalf("a fruitless ladder leaves %s, want %s for the pause and the rung after it",
			left, want)
	}
}

func TestSearchForSkipsARungTheRemainingBudgetCannotFinish(t *testing.T) {
	store := &fakeSearchStore{settings: enabledSettings()}
	downloader := &fakeDownloader{rungBudget: 100 * time.Millisecond}
	searcher := testSearcher(store, downloader)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, _, _, exhausted, err := searcher.askWithinBudget(
		ctx, downloader, sources.Query{}, []string{"Anetha"})
	if err != nil {
		t.Fatalf("error = %v, want budget exhaustion", err)
	}
	if !exhausted {
		t.Fatal("the rung was not reported exhausted")
	}
	if len(downloader.searched) != 0 {
		t.Fatalf("searched %q, want the underfunded rung skipped", downloader.searched)
	}
}

func TestSearchForReportsBudgetExhaustionAsAnEmptyAnswer(t *testing.T) {
	store := &fakeSearchStore{settings: enabledSettings()}
	downloader := &fakeDownloader{rungBudget: 100 * time.Millisecond}
	searcher := testSearcher(store, downloader)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	candidates, _, _, _, err := searcher.SearchFor(
		ctx, sources.Query{}, rungs("Anetha"))
	if err != nil {
		t.Fatalf("error = %v, want exhaustion rather than provider failure", err)
	}
	if len(candidates) != 0 {
		t.Fatalf("candidates = %#v, want an exhausted empty search", candidates)
	}
	if len(downloader.searched) != 0 {
		t.Fatalf("searched %q, want the underfunded rung skipped", downloader.searched)
	}
}

func TestSearchForReportsThePhraseThatFoundTheCandidates(t *testing.T) {
	store := &fakeSearchStore{settings: enabledSettings()}
	downloader := &fakeDownloader{candidates: []sources.Candidate{corroborated(1)}}
	searcher := testSearcher(store, downloader)

	candidates, _, _, text, err := searcher.SearchFor(
		context.Background(), sources.Query{}, rungs("Anetha Candy from Strangers"))
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates = %#v, want what the peer offered", candidates)
	}
	if text != "Anetha Candy from Strangers" {
		t.Fatalf("phrase = %q, want the one that found them", text)
	}
}

// Soulseek search collects replies for a bounded window, so nothing found means
// nobody answered in time rather than nobody has it.
func TestSearchForAsksAgainWhenTheFirstAttemptFindsNothing(t *testing.T) {
	store := &fakeSearchStore{settings: enabledSettings()}
	downloader := &fakeDownloader{results: [][]sources.Candidate{
		nil,
		{corroborated(1)},
	}}
	searcher := testSearcher(store, downloader)

	candidates, _, _, _, err := searcher.SearchFor(
		context.Background(), sources.Query{}, rungs("Anetha Candy from Strangers"))
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates = %#v, want the second attempt's answer", candidates)
	}
}

// Two walks never fitted a want's budget, and re-asking a phrase Soulseek has
// just heard is what earns this instance its bans. So the repeat asks one rung:
// the one with the fewest tokens to match, which is the phrase that reaches the
// most peers, wherever on the ladder it sits.
func TestSearchForRepeatsOnlyTheRungWithTheFewestTokens(t *testing.T) {
	store := &fakeSearchStore{settings: enabledSettings()}
	downloader := &fakeDownloader{}
	searcher := testSearcher(store, downloader)
	ladder := rungs(
		"Anetha Candy from Strangers Club Mix", "Anetha Candy",
		"Candy from Strangers Club Mix",
	)

	if _, _, _, _, err := searcher.SearchFor(context.Background(), sources.Query{}, ladder); err != nil {
		t.Fatal(err)
	}

	want := append(sources.QueryTexts(ladder), "Anetha Candy")
	if !slices.Equal(downloader.searched, want) {
		t.Fatalf("searched %q, want the walk and then its broadest rung alone",
			downloader.searched)
	}
}

// The ladder a want really carries is not ordered by width. TrackQueryTexts ends
// with the title on its own, qualifier and all, which for a titled remix is more
// tokens than the plain title one rung above it -- and every token is one more
// thing a peer's path has to say. Repeating the last rung would repeat one of the
// narrowest phrases the want has.
func TestSearchForRepeatsThePlainTitleOfARemix(t *testing.T) {
	store := &fakeSearchStore{settings: enabledSettings()}
	downloader := &fakeDownloader{}
	searcher := testSearcher(store, downloader)
	ladder := sources.TrackQueryRungs(
		"Anetha", "Lvstlove (Night Drive At 177mph Mix)", "Lvstlove")

	if _, _, _, _, err := searcher.SearchFor(context.Background(), sources.Query{}, ladder); err != nil {
		t.Fatal(err)
	}

	if repeated := downloader.searched[len(downloader.searched)-1]; repeated != "Anetha Lvstlove" {
		t.Fatalf("repeated %q, want the fewest tokens a peer's path has to match",
			repeated)
	}
}

// Where width cannot separate two rungs, what they were made of can. Both of
// these are three tokens with neither inside the other, and one still carries the
// release-kind label the catalogue ladder spends a whole rung shedding, peer
// folders being named without it. The repeat asks the other.
//
// The ladder here is built by hand rather than through TrackQueryRungs: since
// plainTitle sheds a bare trailing label itself, the ladder no longer hands out
// this exact tie for any title, but broadestRung still has to break it correctly
// when a Rung carries the label and its equal-width neighbour does not.
func TestSearchForRepeatsTheRungNotCarryingAReleaseKind(t *testing.T) {
	store := &fakeSearchStore{settings: enabledSettings()}
	downloader := &fakeDownloader{}
	searcher := testSearcher(store, downloader)
	ladder := []sources.Rung{
		{Text: "Cynthoni Femcels Forever", CarriesReleaseKind: false},
		{Text: "Femcels Forever EP", CarriesReleaseKind: true},
	}

	if _, _, _, _, err := searcher.SearchFor(context.Background(), sources.Query{}, ladder); err != nil {
		t.Fatal(err)
	}

	repeated := downloader.searched[len(downloader.searched)-1]
	if repeated != "Cynthoni Femcels Forever" {
		t.Fatalf("repeated %q, want the three-token rung with no label on it", repeated)
	}
}

// Two rungs of the same width, neither carrying a label, are separated by the
// ladder's own order, which appends each rung as a broadening of what stands
// above it. It matters for the want this ladder's last rung exists for: Soulseek
// blacklists some artists' names, and a phrase carrying one comes back empty
// however long it is given.
func TestSearchForRepeatsTheLaterOfTwoRungsOfEqualWidth(t *testing.T) {
	store := &fakeSearchStore{settings: enabledSettings()}
	downloader := &fakeDownloader{}
	searcher := testSearcher(store, downloader)
	ladder := sources.TrackQueryRungs("Gorillaz", "Feel Good Inc", "Demon Days")

	if _, _, _, _, err := searcher.SearchFor(context.Background(), sources.Query{}, ladder); err != nil {
		t.Fatal(err)
	}

	if repeated := downloader.searched[len(downloader.searched)-1]; repeated != "Feel Good Inc" {
		t.Fatalf("repeated %q, want the later of the two three-token rungs", repeated)
	}
}

// The repeat is asked in its own words, and a want told which phrase answered has
// to be told the phrase that was actually asked.
func TestSearchForReportsTheBroadestRungWhenTheRepeatFindsCandidates(t *testing.T) {
	store := &fakeSearchStore{settings: enabledSettings()}
	downloader := &fakeDownloader{results: [][]sources.Candidate{
		nil, nil, {corroborated(1)},
	}}
	searcher := testSearcher(store, downloader)

	_, _, _, text, err := searcher.SearchFor(context.Background(), sources.Query{},
		rungs("Anetha Candy from Strangers", "Candy from Strangers"))
	if err != nil {
		t.Fatal(err)
	}
	if text != "Candy from Strangers" {
		t.Fatalf("phrase = %q, want the broadest rung the repeat asked", text)
	}
}

// A rung can normalize away to nothing -- a title written entirely in symbols
// leaves an empty phrase behind -- and no tokens at all is not the fewest tokens.
// An empty phrase asks every peer for everything, which is the one search worth
// less than not repeating at all.
func TestSearchForNeverRepeatsAPhraseThatNormalizedAway(t *testing.T) {
	store := &fakeSearchStore{settings: enabledSettings()}
	downloader := &fakeDownloader{}
	searcher := testSearcher(store, downloader)

	if _, _, _, _, err := searcher.SearchFor(context.Background(), sources.Query{},
		rungs("", "Draining Love Story")); err != nil {
		t.Fatal(err)
	}

	if repeated := downloader.searched[len(downloader.searched)-1]; repeated != "Draining Love Story" {
		t.Fatalf("repeated %q, want the only rung there was anything to ask", repeated)
	}
}

// A want with no phrase to search for is answered rather than crashed into:
// there is no rung to walk, and so none to repeat either. The provider is holding
// a candidate it is never asked for.
func TestSearchForAnswersEmptyForAWantWithNoPhrase(t *testing.T) {
	store := &fakeSearchStore{settings: enabledSettings()}
	searcher := testSearcher(store,
		&fakeDownloader{candidates: []sources.Candidate{corroborated(1)}})

	candidates, _, _, _, err := searcher.SearchFor(context.Background(), sources.Query{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 0 {
		t.Fatalf("candidates = %#v, want nothing found for nothing asked", candidates)
	}
}

func TestSearchForRefusesWithoutAnEnabledProvider(t *testing.T) {
	store := &fakeSearchStore{}
	searcher := testSearcher(store, &fakeDownloader{})

	if _, _, _, _, err := searcher.SearchFor(
		context.Background(), sources.Query{}, rungs("Anetha"),
	); !errors.Is(err, sources.ErrNotConfigured) {
		t.Fatalf("error = %v, want %v", err, sources.ErrNotConfigured)
	}
}

// A provider that could not be searched has said nothing, and a want must never
// read that as nobody sharing the music.
func TestSearchForSurfacesAProviderFailure(t *testing.T) {
	store := &fakeSearchStore{settings: enabledSettings()}
	downloader := &fakeDownloader{searchErr: errors.New("slskd is unreachable")}
	searcher := testSearcher(store, downloader)

	if _, _, _, _, err := searcher.SearchFor(
		context.Background(), sources.Query{}, rungs("Anetha"),
	); !errors.Is(err, downloader.searchErr) {
		t.Fatalf("error = %v, want the provider failure reported", err)
	}
}

func TestSearchForSurfacesAFailureOnTheSecondAttempt(t *testing.T) {
	store := &fakeSearchStore{settings: enabledSettings()}
	downloader := &fakeDownloader{retrySearchErr: errors.New("slskd is unreachable")}
	searcher := testSearcher(store, downloader)

	if _, _, _, _, err := searcher.SearchFor(
		context.Background(), sources.Query{}, rungs("Anetha"),
	); !errors.Is(err, downloader.retrySearchErr) {
		t.Fatalf("error = %v, want the provider failure reported", err)
	}
}

func TestSearchForStopsWhenTheCallerGivesUpBeforeAskingAgain(t *testing.T) {
	store := &fakeSearchStore{settings: enabledSettings()}
	searcher := testSearcher(store, &fakeDownloader{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, _, _, _, err := searcher.SearchFor(
		ctx, sources.Query{}, rungs("Anetha"),
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want the cancellation reported", err)
	}
}

// expiringProvider answers walk phrases at once. What every phrase after that
// does is the test's to choose: spend waits the caller's budget out first, which
// is what a rung started with nothing left does, and refusal is the provider
// objecting instead of reporting its own clock.
type expiringProvider struct {
	walk      int
	spend     bool
	refusal   error
	asked     int
	budget    time.Duration
	walkDelay time.Duration
}

func (provider *expiringProvider) Build(sources.Settings) (sources.Provider, error) {
	return provider, nil
}

func (provider *expiringProvider) Name() string { return "fake" }

func (provider *expiringProvider) CheckConnection(context.Context) (sources.Status, error) {
	return sources.Status{}, nil
}

func (provider *expiringProvider) SearchBudget() time.Duration {
	if provider.budget > 0 {
		return provider.budget
	}
	return 20 * time.Millisecond
}

func (provider *expiringProvider) Search(
	ctx context.Context, _ sources.Query,
) ([]sources.Candidate, error) {
	provider.asked++
	if provider.asked <= provider.walk {
		if provider.walkDelay > 0 {
			time.Sleep(provider.walkDelay)
		}
		return nil, nil
	}
	if provider.spend {
		<-ctx.Done()
	}
	if provider.refusal != nil {
		return nil, provider.refusal
	}
	return nil, ctx.Err()
}

// A repeat skipped before its first rung has no phrase of its own. The phrase
// returned is still the last one the completed walk actually asked, because
// that is the empty answer acquisition records for the want.
func TestSearchForKeepsTheLastPhraseWhenTheRepeatCannotFit(t *testing.T) {
	store := &fakeSearchStore{settings: enabledSettings()}
	provider := &expiringProvider{
		walk: 1, budget: 100 * time.Millisecond, walkDelay: 180 * time.Millisecond,
	}
	searcher := testSearcher(store, provider)
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	_, _, _, text, err := searcher.SearchFor(
		ctx, sources.Query{}, rungs("Anetha Lvstlove"))
	if err != nil {
		t.Fatalf("error = %v, want the completed walk's empty answer", err)
	}
	if text != "Anetha Lvstlove" {
		t.Fatalf("phrase = %q, want the last phrase actually searched", text)
	}
	if provider.asked != 1 {
		t.Fatalf("asked %d times, want the underfunded repeat skipped", provider.asked)
	}
}

func TestSearchForTreatsProviderAdmissionExhaustionAsAnEmptyAnswer(t *testing.T) {
	store := &fakeSearchStore{settings: enabledSettings()}
	searcher := testSearcher(store, &expiringProvider{
		refusal: sources.ErrSearchBudgetExhausted,
	})

	candidates, _, _, _, err := searcher.SearchFor(
		context.Background(), sources.Query{}, rungs("Anetha"))
	if err != nil {
		t.Fatalf("error = %v, want admission exhaustion rather than provider failure", err)
	}
	if len(candidates) != 0 {
		t.Fatalf("candidates = %#v, want none", candidates)
	}
}

// The budget pays for the repeat, but the caller can hold a shorter deadline of
// its own, and then the repeat is cut short anyway. The first walk asked every
// phrase and heard nothing, which is an answer, and it must survive a
// confirmation there was no time left for.
func TestSearchForKeepsTheEmptyAnswerWhenTheRepeatRanOutOfBudget(t *testing.T) {
	store := &fakeSearchStore{settings: enabledSettings()}
	searcher := testSearcher(store, &expiringProvider{walk: 2, spend: true})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	candidates, _, _, _, err := searcher.SearchFor(
		ctx, sources.Query{}, rungs("Anetha Candy from Strangers", "Anetha"))
	if err != nil {
		t.Fatalf("error = %v, want the empty answer the first walk produced", err)
	}
	if len(candidates) != 0 {
		t.Fatalf("candidates = %#v, want none", candidates)
	}
}

// The budget can just as easily run out in the pause, before the repeat has
// asked anything at all, and the answer is the same one.
func TestSearchForKeepsTheEmptyAnswerWhenTheBudgetRanOutInThePause(t *testing.T) {
	store := &fakeSearchStore{settings: enabledSettings()}
	searcher := testSearcher(store, &expiringProvider{walk: 1, spend: true})
	searcher.emptyRetryDelay = time.Minute
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	candidates, _, _, _, err := searcher.SearchFor(ctx, sources.Query{}, rungs("Anetha"))
	if err != nil {
		t.Fatalf("error = %v, want the empty answer the first walk produced", err)
	}
	if len(candidates) != 0 {
		t.Fatalf("candidates = %#v, want none", candidates)
	}
}

// silentNetwork is a peer network nobody answers on. Every search spends its
// whole reply window and reports nothing, which is what a Soulseek search does
// when no peer replies in time, and it records the window each search closed so
// a test can see how many there were and how far apart.
type silentNetwork struct {
	window  time.Duration
	windows []replyWindow
}

type replyWindow struct {
	text   string
	opened time.Time
	closed time.Time
}

func (network *silentNetwork) Build(sources.Settings) (sources.Provider, error) {
	return network, nil
}

func (network *silentNetwork) Name() string { return "fake" }

func (network *silentNetwork) CheckConnection(context.Context) (sources.Status, error) {
	return sources.Status{}, nil
}

func (network *silentNetwork) SearchBudget() time.Duration { return network.window }

func (network *silentNetwork) Search(
	ctx context.Context, query sources.Query,
) ([]sources.Candidate, error) {
	opened := time.Now()
	if !wait(ctx, network.window) {
		return nil, ctx.Err()
	}
	network.windows = append(network.windows, replyWindow{
		text: query.Text, opened: opened, closed: time.Now(),
	})
	return nil, nil
}

// A want nobody answers is the whole reason the repeat exists: a Soulseek search
// is a broadcast, so an empty reply window means nobody answered in time rather
// than nobody has the music. The want must therefore close two reply windows on
// its broadest phrase, a pause apart, and not one.
//
// The reply window, the pause and the headroom are all shrunk here. Their real
// values are seconds, and the arithmetic between them is what
// TestSearchBudgetPaysForTheSecondLookAfterAFruitlessLadder holds to the real
// ones; this test watches the searches themselves happen.
func TestSearchForClosesASecondReplyWindowWhenNobodyAnswers(t *testing.T) {
	const window = 100 * time.Millisecond
	const pause = 60 * time.Millisecond
	store := &fakeSearchStore{settings: enabledSettings()}
	network := &silentNetwork{window: window}
	searcher := NewSearcher(store, network, zerolog.Nop())
	searcher.emptyRetryDelay = pause
	// Headroom stands for the work around the searches. It is generous next to
	// this reply window so a loaded machine cannot lose the second look to
	// scheduling, and still far short of the pause plus a window, which is what
	// the second look would have to borrow from it if the budget did not pay.
	searcher.headroom = 120 * time.Millisecond

	candidates, _, _, _, err := searcher.SearchFor(context.Background(), sources.Query{},
		rungs("Anetha Lvstlove (Night Drive At 177mph Mix)", "Anetha Lvstlove"))
	if err != nil {
		t.Fatalf("SearchFor() error = %v", err)
	}
	if len(candidates) != 0 {
		t.Fatalf("candidates = %#v, want none from a network nobody answers on", candidates)
	}

	if len(network.windows) != 3 {
		t.Fatalf("closed %d reply windows, want both rungs and the second look at the broadest",
			len(network.windows))
	}
	first, second := network.windows[1], network.windows[2]
	if second.text != first.text {
		t.Fatalf("the second look asked %q, want the broadest phrase %q again",
			second.text, first.text)
	}
	// The pause is what keeps two asks of one phrase apart, and asking again too
	// soon is what earns this instance its Soulseek bans. Budgeting the second
	// look must lengthen the want, never shorten that gap.
	if gap := second.opened.Sub(first.closed); gap < pause || gap > pause+window {
		t.Fatalf("the two looks were %s apart, want about the %s pause", gap, pause)
	}
}

// A caller that gave up is not a search that ran out of time. Nothing was
// established about the music and nobody is waiting for the answer, so the
// cancellation is reported rather than dressed up as nobody sharing it.
func TestSearchForSurfacesACancellationDuringTheRepeat(t *testing.T) {
	store := &fakeSearchStore{settings: enabledSettings()}
	searcher := testSearcher(store, &expiringProvider{walk: 1, spend: true})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	time.AfterFunc(20*time.Millisecond, cancel)

	if _, _, _, _, err := searcher.SearchFor(
		ctx, sources.Query{}, rungs("Anetha"),
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want the cancellation reported", err)
	}
}

// The Soulseek ban is seen as slskd abandoning the search, and it surfaces on
// whichever rung was running when it landed -- which, on a repeat, is a rung the
// budget has usually already run out under. Reading that refusal as nobody
// sharing the music is issue #193 reached from the far end of the walk.
func TestSearchForSurfacesARefusalThatArrivedAfterTheBudgetRanOut(t *testing.T) {
	store := &fakeSearchStore{settings: enabledSettings()}
	provider := &expiringProvider{
		walk: 1, spend: true, refusal: sources.ErrProviderUnavailable,
	}
	searcher := testSearcher(store, provider)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	if _, _, _, _, err := searcher.SearchFor(
		ctx, sources.Query{}, rungs("Anetha"),
	); !errors.Is(err, sources.ErrProviderUnavailable) {
		t.Fatalf("error = %v, want the refusal reported", err)
	}
}

// One rung's own reply window expiring while the caller still has budget is a
// provider that stopped answering, and there is time left in which to say so.
func TestSearchForSurfacesARungThatTimedOutWithBudgetLeft(t *testing.T) {
	store := &fakeSearchStore{settings: enabledSettings()}
	provider := &expiringProvider{walk: 1, refusal: context.DeadlineExceeded}
	searcher := testSearcher(store, provider)

	if _, _, _, _, err := searcher.SearchFor(
		context.Background(), sources.Query{}, rungs("Anetha"),
	); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want the rung's own timeout reported", err)
	}
}

// The pause before an empty search is asked again exists so the second attempt
// reaches a slightly different set of peers rather than repeating the first
// one's timing.
func TestWaitingSleepsBeforeTheSecondAttempt(t *testing.T) {
	start := time.Now()

	if !wait(context.Background(), 20*time.Millisecond) {
		t.Fatal("wait() reported that it had not slept")
	}
	if elapsed := time.Since(start); elapsed < 20*time.Millisecond {
		t.Fatalf("waited %s, want the pause spent", elapsed)
	}
}

// A caller that has given up does not spend the pause first.
func TestWaitingStopsWhenTheCallerGivesUp(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	if wait(ctx, time.Minute) {
		t.Fatal("wait() slept for a caller that had given up")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("waited %s after being cancelled", elapsed)
	}
}

// Fail is what the worker calls once retrying has stopped, and it must leave a
// result that reads as attempted rather than as pending.
func TestFailRecordsWhyTheSearchNeverRan(t *testing.T) {
	store := &fakeSearchStore{}
	searcher := testSearcher(store, &fakeDownloader{})

	if err := searcher.Fail(
		context.Background(), uuid.New(), uuid.New(), "slskd is unreachable",
	); err != nil {
		t.Fatalf("Fail() error = %v", err)
	}
	if len(store.recorded) != 1 || store.recorded[0].Status != "failed" {
		t.Fatalf("recorded = %#v", store.recorded)
	}
	if store.recorded[0].Error != "slskd is unreachable" {
		t.Errorf("recorded error = %q", store.recorded[0].Error)
	}
}

func (store *fakeSearchStore) SourcePreferences(context.Context) (db.SourcePreferencesRow, error) {
	return store.preferences, nil
}

// The floor is stored per format, and a floor nothing reads is a setting that
// does nothing. This is the whole path: the row the settings screen writes,
// through the search, to the copies it will not fetch.
func TestAStoredPerFormatFloorRefusesTheCopiesUnderIt(t *testing.T) {
	store := &fakeSearchStore{
		settings: enabledSettings(),
		preferences: db.SourcePreferencesRow{
			FormatMinimumBitRate: map[string]int32{"mp3": 320},
		},
	}
	downloader := &fakeDownloader{candidates: []sources.Candidate{
		{
			Provider: "fake", Username: "thin", Directory: `Music\Dummy [MP3]`,
			Format: "mp3", AverageBitRate: 192,
			Files: []sources.File{{Path: `Music\Dummy [MP3]\05 Glory Box.mp3`,
				Name: "05 Glory Box.mp3", Extension: "mp3", BitRate: 192}},
		},
		{
			Provider: "fake", Username: "full", Directory: `Music\Dummy [320]`,
			Format: "mp3", AverageBitRate: 320,
			Files: []sources.File{{Path: `Music\Dummy [320]\05 Glory Box.mp3`,
				Name: "05 Glory Box.mp3", Extension: "mp3", BitRate: 320}},
		},
	}}
	searcher := testSearcher(store, downloader)

	candidates, refused, preferences, _, err := searcher.SearchFor(
		context.Background(), sources.Query{}, rungs("Portishead Dummy"))
	if err != nil {
		t.Fatalf("SearchFor() error = %v", err)
	}
	if len(candidates) != 1 || candidates[0].Username != "full" {
		t.Fatalf("candidates = %+v, want only the copy that meets the floor", candidates)
	}
	if len(refused) != 1 || refused[0].Kind != sources.RefusedBitRate {
		t.Fatalf("refused = %+v, want the thin copy reported with its rule", refused)
	}
	if preferences.FormatMinimumBitRate["mp3"] != 320 {
		t.Fatalf("preferences = %+v, want the stored floor handed back to the caller",
			preferences)
	}
}

// The settings page lets somebody wait two minutes for peers, which is the
// widest "Search timeout" takes (5..120 seconds, validated in
// internal/server/sources.go against the CHECK in 00005_slskd_settings.sql).
//
// A want's whole search used to be capped at a number of its own, and at two
// minutes one rung already cost more than that cap, so the setting was inviting
// a choice the loop could not honour and rungs were skipped for want of budget.
// The cap is derived from the setting now. Nothing here is typed by hand: the
// per-rung cost is asked of a real slskd client built at the top of the range,
// and the assertion runs through SearchFor, which is the caller that turns that
// cost into the want's deadline.
func TestTheWantsCeilingHonoursTheWidestReplyWindowTheSettingAllows(t *testing.T) {
	// The widest the stored setting may be. A client built with it reports what
	// one rung costs after admission — the reply window it waits out, and the
	// HTTP allowance for opening and reading the search on top of it.
	const widestSearchTimeout = 120 * time.Second
	client, err := slskd.NewClient(slskd.Options{
		BaseURL: "http://slskd.test", APIKey: "key", SearchTimeout: widestSearchTimeout,
	})
	if err != nil {
		t.Fatal(err)
	}
	perRung := client.SearchBudget()
	if perRung <= widestSearchTimeout {
		t.Fatalf("SearchBudget() = %s, want more than the %s window it waits out",
			perRung, widestSearchTimeout)
	}

	ladder := sources.TrackQueryRungs("Anetha", "Lvstlove (Night Drive At 177mph Mix)", "Lvstlove")
	if len(ladder) < 2 {
		t.Fatalf("the full track ladder has %d rungs, want a ladder to walk", len(ladder))
	}

	// What SearchFor gives the whole want, worked out from that per-rung cost
	// and the ladder in front of it. Nobody sharing the music is the case it has
	// to survive: every rung spends its whole reply window, and the broadest
	// rung is asked once more after the pause. A cap of its own is exactly what
	// fails here — at two minutes a single rung already costs more than the old
	// 150 seconds, so the last rungs would be unaffordable and canFinishSearch
	// would skip them rather than ask.
	searcher := testSearcher(&fakeSearchStore{settings: enabledSettings()}, &fakeDownloader{})
	budget := searcher.searchBudget(len(ladder), perRung)
	if want := time.Duration(len(ladder)+1) * perRung; budget < want {
		t.Fatalf("a want gets %s for a ladder of %d, want at least the %s every rung and the repeat cost",
			budget, len(ladder), want)
	}

	// And the caller wires that budget to the provider it came from: with the
	// deadline derived rather than capped, every phrase is actually asked.
	downloader := &fakeDownloader{rungBudget: perRung}
	searcher = testSearcher(&fakeSearchStore{settings: enabledSettings()}, downloader)

	candidates, _, _, _, err := searcher.SearchFor(context.Background(), sources.Query{}, ladder)
	if err != nil {
		t.Fatalf("SearchFor() error = %v", err)
	}
	if len(candidates) != 0 {
		t.Fatalf("candidates = %d, want the empty answer this fixture offers", len(candidates))
	}

	// Every rung asked, and the repeat after them. A skipped rung is a want told
	// nobody is sharing music nobody was asked about.
	if want := len(ladder) + 1; len(downloader.searched) != want {
		t.Fatalf("asked %d phrases (%q), want all %d rungs and the repeat",
			len(downloader.searched), downloader.searched, want)
	}
}
