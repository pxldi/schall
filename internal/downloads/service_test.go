package downloads

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/events"
	"github.com/pxldi/schall/internal/sources"
	"github.com/rs/zerolog"
)

type fakeStore struct {
	request      db.DownloadRequestRow
	startErr     error
	started      bool
	revertedWith string
	recorded     []db.TransferProgress
	cancelled    bool
	transferIDs  []string
	pollQueued   bool
	// challengesQueuedFor is when the pass that reads peers' private chat was
	// asked for, and zero when it was not asked for at all.
	challengesQueuedFor time.Time
	importQueued        bool
	settings            db.SlskdSettingsRow
	unconfigured        bool
	statusAfterAll      string
	// retryFiles is what the store hands back as the failed files of a request.
	// Which files those are is the store's decision and is proven against a real
	// database; here it is the input the service has to act on.
	retryFiles    []db.DownloadRequestFile
	retryErr      error
	retryAsked    bool
	retryReverted string
	// byID stands for a store holding more than one request, which is what a
	// batch addressed to one peer needs. Left nil, every lookup answers with the
	// single request above, exactly as it did before batching existed.
	byID        map[uuid.UUID]db.DownloadRequestRow
	startedIDs  []uuid.UUID
	revertedIDs []uuid.UUID
	// What the poller recorded about a peer that would not take a copy just now.
	deferredID      uuid.NullUUID
	deferredSummary string
	deferErr        error
	// What the poller recorded when a transfer failed outright.
	rescheduledTargetID   uuid.NullUUID
	rescheduledSummary    string
	rescheduleTransferErr error
	// What the database refuses. Each stands for the store being unreachable at
	// exactly that call, which is what decides whether a request is left as it
	// was or claiming to be running.
	requestErr      error
	revertErr       error
	retryRevertErr  error
	pollQueueErr    error
	importQueueErr  error
	startedErr      error
	progressErr     error
	transferIDsErr  error
	cancelErr       error
	settingsReadErr error
}

func (store *fakeStore) DownloadRequest(context.Context, uuid.UUID) (db.DownloadRequestRow, error) {
	return store.request, store.requestErr
}

func (store *fakeStore) StartDownloadRequest(_ context.Context, id uuid.UUID) (db.DownloadRequestRow, error) {
	if store.startErr != nil {
		return db.DownloadRequestRow{}, store.startErr
	}
	store.startedIDs = append(store.startedIDs, id)
	if row, known := store.byID[id]; known {
		row.Status = "started"
		store.byID[id] = row
		return row, nil
	}
	store.started = true
	store.request.Status = "started"
	return store.request, nil
}

func (store *fakeStore) RevertDownloadRequestStart(_ context.Context, id uuid.UUID, message string) (db.DownloadRequestRow, error) {
	if store.revertErr != nil {
		return db.DownloadRequestRow{}, store.revertErr
	}
	store.revertedIDs = append(store.revertedIDs, id)
	store.revertedWith = message
	if row, known := store.byID[id]; known {
		row.Status = "requested"
		store.byID[id] = row
		return row, nil
	}
	store.request.Status = "requested"
	return store.request, nil
}

func (store *fakeStore) DeferAcquiredFetch(_ context.Context, id uuid.UUID, summary string) error {
	if store.deferErr != nil {
		return store.deferErr
	}
	store.deferredID = uuid.NullUUID{UUID: id, Valid: true}
	store.deferredSummary = summary
	return nil
}

func (store *fakeStore) RescheduleFailedTransfer(_ context.Context, id uuid.UUID, summary string) error {
	if store.rescheduleTransferErr != nil {
		return store.rescheduleTransferErr
	}
	store.rescheduledTargetID = uuid.NullUUID{UUID: id, Valid: true}
	store.rescheduledSummary = summary
	return nil
}

func (store *fakeStore) RetryDownloadRequestFiles(_ context.Context, _ uuid.UUID, _ []string) (db.DownloadRequestRow, []db.DownloadRequestFile, error) {
	if store.retryErr != nil {
		return db.DownloadRequestRow{}, nil, store.retryErr
	}
	store.retryAsked = true
	store.request.Status = "started"
	return store.request, store.retryFiles, nil
}

func (store *fakeStore) RevertDownloadRequestRetry(_ context.Context, _ uuid.UUID, _ []string, message string) (db.DownloadRequestRow, error) {
	if store.retryRevertErr != nil {
		return db.DownloadRequestRow{}, store.retryRevertErr
	}
	store.retryReverted = message
	store.request.Status = "failed"
	return store.request, nil
}

func (store *fakeStore) RecordTransferProgress(_ context.Context, _ uuid.UUID, progress []db.TransferProgress) (db.DownloadRequestRow, error) {
	if store.progressErr != nil {
		return db.DownloadRequestRow{}, store.progressErr
	}
	store.recorded = append(store.recorded, progress...)
	updated := store.request
	if store.statusAfterAll != "" {
		updated.Status = store.statusAfterAll
	}
	return updated, nil
}

func (store *fakeStore) StartedDownloadRequests(context.Context) ([]db.DownloadRequestRow, error) {
	if store.startedErr != nil {
		return nil, store.startedErr
	}
	if store.request.Status != "started" {
		return nil, nil
	}
	return []db.DownloadRequestRow{store.request}, nil
}

func (store *fakeStore) RequestedTransferIDs(context.Context, uuid.UUID) ([]string, error) {
	return store.transferIDs, store.transferIDsErr
}

func (store *fakeStore) CancelDownloadRequest(context.Context, uuid.UUID) (db.DownloadRequestRow, error) {
	if store.cancelErr != nil {
		return db.DownloadRequestRow{}, store.cancelErr
	}
	store.cancelled = true
	store.request.Status = "cancelled"
	return store.request, nil
}

func (store *fakeStore) QueueDownloadPoll(context.Context, time.Time) error {
	if store.pollQueueErr != nil {
		return store.pollQueueErr
	}
	store.pollQueued = true
	return nil
}

func (store *fakeStore) QueuePeerChallenges(_ context.Context, runAfter time.Time) error {
	store.challengesQueuedFor = runAfter
	return nil
}

func (store *fakeStore) QueueDownloadImport(context.Context, uuid.UUID) error {
	if store.importQueueErr != nil {
		return store.importQueueErr
	}
	store.importQueued = true
	return nil
}

func (store *fakeStore) SlskdSettings(context.Context) (db.SlskdSettingsRow, error) {
	if store.settingsReadErr != nil {
		return db.SlskdSettingsRow{}, store.settingsReadErr
	}
	if store.unconfigured {
		return db.SlskdSettingsRow{}, pgx.ErrNoRows
	}
	return store.settings, nil
}

type fakeDownloader struct {
	startedFor  string
	startedWith []sources.File
	// starts is how many times the provider was asked, which is the whole
	// question a peer's queue limit turns on: ten files asked once, or once each.
	starts      int
	startErr    error
	transfers   []sources.Transfer
	transferErr error
	cancelled   []string
	searchOnly  bool
	// candidates is what a search reports, which is how the source re-check is
	// told what the peer is offering now.
	candidates []sources.Candidate
	searchErr  error
	searched   []string
	// query is the last query the provider was asked, so a test can check what
	// a candidate was to be corroborated against and not only what came back.
	query sources.Query
	// results, when set, is what successive searches return. It is how a test
	// shows a search finding nothing and the identical search finding sources
	// moments later, which is what a real Soulseek search does.
	results [][]sources.Candidate
	// retrySearchErr fails every search after the first, which is how a test
	// shows the repeat of an empty search failing where the first one did not.
	retrySearchErr error
	// rungBudget is how long one fake Search is allowed to spend. Zero keeps the
	// production-shaped default for tests that are not about time.
	rungBudget time.Duration
	// buildErr stands for settings a provider cannot be made from at all.
	buildErr error
	// cancelDownloadErr is a provider that will not stop a running transfer.
	cancelDownloadErr error
}

func (downloader *fakeDownloader) Build(sources.Settings) (sources.Provider, error) {
	if downloader.buildErr != nil {
		return nil, downloader.buildErr
	}
	if downloader.searchOnly {
		return searchOnlyProvider{}, nil
	}
	return downloader, nil
}

func (downloader *fakeDownloader) Name() string { return "fake" }

func (downloader *fakeDownloader) CheckConnection(context.Context) (sources.Status, error) {
	return sources.Status{}, nil
}

func (downloader *fakeDownloader) SearchBudget() time.Duration {
	if downloader.rungBudget > 0 {
		return downloader.rungBudget
	}
	return 30 * time.Second
}

func (downloader *fakeDownloader) Search(_ context.Context, query sources.Query) ([]sources.Candidate, error) {
	downloader.searched = append(downloader.searched, query.Text)
	downloader.query = query
	if downloader.retrySearchErr != nil && len(downloader.searched) > 1 {
		return nil, downloader.retrySearchErr
	}
	if len(downloader.results) > 0 {
		index := min(len(downloader.searched)-1, len(downloader.results)-1)
		return downloader.results[index], downloader.searchErr
	}
	return downloader.candidates, downloader.searchErr
}

func (downloader *fakeDownloader) StartDownloads(_ context.Context, username string, files []sources.File) error {
	downloader.startedFor, downloader.startedWith = username, files
	downloader.starts++
	return downloader.startErr
}

func (downloader *fakeDownloader) Transfers(context.Context, string) ([]sources.Transfer, error) {
	return downloader.transfers, downloader.transferErr
}

func (downloader *fakeDownloader) CancelDownload(_ context.Context, _, transferID string) error {
	if downloader.cancelDownloadErr != nil {
		return downloader.cancelDownloadErr
	}
	downloader.cancelled = append(downloader.cancelled, transferID)
	return nil
}

type searchOnlyProvider struct{}

func (searchOnlyProvider) Name() string { return "search-only" }
func (searchOnlyProvider) CheckConnection(context.Context) (sources.Status, error) {
	return sources.Status{}, nil
}
func (searchOnlyProvider) SearchBudget() time.Duration { return 30 * time.Second }
func (searchOnlyProvider) Search(context.Context, sources.Query) ([]sources.Candidate, error) {
	return nil, nil
}

func requestRow() db.DownloadRequestRow {
	return db.DownloadRequestRow{
		ID: uuid.New(), Status: "requested", SourceUsername: "peer",
		Files: []db.DownloadRequestFile{
			{Path: `@@peer\Music\01.flac`, Name: "01.flac", SizeBytes: 100},
			{Path: `@@peer\Music\02.flac`, Name: "02.flac", SizeBytes: 200},
		},
	}
}

func enabledSettings() db.SlskdSettingsRow {
	return db.SlskdSettingsRow{BaseURL: "http://slskd:5030", APIKey: "k", Enabled: true, SearchTimeoutSeconds: 20}
}

// recheckableRow is a request the source re-check can actually search for: it
// needs the catalogue names the query is built from and the folder to match.
func recheckableRow() db.DownloadRequestRow {
	row := requestRow()
	row.ArtistName, row.AlbumTitle = "Boards of Canada", "Geogaddi"
	row.SourceDirectory = `@@peer\Music`
	row.ExpectedTrackCount = 2
	return row
}

func offeredCandidate(files ...sources.File) sources.Candidate {
	return sources.Candidate{
		Provider: "fake", Username: "peer", Directory: `@@peer\Music`, Files: files,
	}
}

func TestStartRefusesAFolderThePeerNoLongerOffersAsRecorded(t *testing.T) {
	store := &fakeStore{request: recheckableRow(), settings: enabledSettings()}
	downloader := &fakeDownloader{candidates: []sources.Candidate{offeredCandidate(
		sources.File{Path: `@@peer\Music\01.flac`, SizeBytes: 100},
	)}}
	service := NewService(store, downloader, zerolog.Nop())

	_, err := service.Start(context.Background(), store.request.ID, false)
	if !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("error = %v, want ErrSourceChanged", err)
	}
	if !strings.Contains(err.Error(), "02.flac") {
		t.Fatalf("error = %q, want it to name the file that is gone", err)
	}
	// A refusal must leave the request exactly as it was, so it can be started
	// again once someone answers for it.
	if store.started {
		t.Fatal("a stale source was started anyway")
	}
}

// The distinction the whole check turns on. slskd queues a transfer for a peer
// that is not around, so silence must not be read as a withdrawn folder.
func TestStartProceedsWhenThePeerSaysNothingAtAll(t *testing.T) {
	store := &fakeStore{request: recheckableRow(), settings: enabledSettings()}
	downloader := &fakeDownloader{}
	service := NewService(store, downloader, zerolog.Nop())

	if _, err := service.Start(context.Background(), store.request.ID, false); err != nil {
		t.Fatalf("Start() error = %v, want an offline peer not to stop a start", err)
	}
	if !store.started {
		t.Fatal("an offline peer stopped the request from starting")
	}
}

// A search that failed is a statement about the provider, not about the folder.
func TestStartProceedsWhenTheSourceCannotBeChecked(t *testing.T) {
	store := &fakeStore{request: recheckableRow(), settings: enabledSettings()}
	downloader := &fakeDownloader{searchErr: errors.New("slskd is unreachable")}
	service := NewService(store, downloader, zerolog.Nop())

	if _, err := service.Start(context.Background(), store.request.ID, false); err != nil {
		t.Fatalf("Start() error = %v, want a failed check not to stop a start", err)
	}
	if !store.started {
		t.Fatal("an unreadable provider stopped the request from starting")
	}
}

func TestStartAcceptsAnAcknowledgedSourceChange(t *testing.T) {
	store := &fakeStore{request: recheckableRow(), settings: enabledSettings()}
	downloader := &fakeDownloader{candidates: []sources.Candidate{offeredCandidate(
		sources.File{Path: `@@peer\Music\01.flac`, SizeBytes: 100},
	)}}
	service := NewService(store, downloader, zerolog.Nop())

	if _, err := service.Start(context.Background(), store.request.ID, true); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if !store.started {
		t.Fatal("an acknowledged source change did not start")
	}
	// Acknowledged means the question was already answered, so it is not asked
	// again on the way past.
	if len(downloader.searched) != 0 {
		t.Fatalf("searched %v while starting an acknowledged request", downloader.searched)
	}
}

// A file offered at a different size is a different file, whatever it is
// called. The importer would refuse it later; refusing now costs the peer
// nothing.
func TestCheckSourceReportsAFileOfferedAtADifferentSize(t *testing.T) {
	store := &fakeStore{request: recheckableRow(), settings: enabledSettings()}
	downloader := &fakeDownloader{candidates: []sources.Candidate{offeredCandidate(
		sources.File{Path: `@@peer\Music\01.flac`, SizeBytes: 100},
		sources.File{Path: `@@peer\Music\02.flac`, SizeBytes: 999},
	)}}
	service := NewService(store, downloader, zerolog.Nop())

	offer, err := service.CheckSource(context.Background(), store.request.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !offer.Found || !offer.Stale() {
		t.Fatalf("offer = %#v, want a folder that is there but changed", offer)
	}
	if len(offer.Changed) != 1 || offer.Changed[0] != "02.flac" {
		t.Fatalf("changed = %v, want the resized file", offer.Changed)
	}
	if len(offer.Missing) != 0 {
		t.Fatalf("missing = %v, want nothing reported as gone", offer.Missing)
	}
}

// A peer with no size to report is silent about it rather than disagreeing.
func TestCheckSourceTreatsAnAbsentSizeAsSilence(t *testing.T) {
	store := &fakeStore{request: recheckableRow(), settings: enabledSettings()}
	downloader := &fakeDownloader{candidates: []sources.Candidate{offeredCandidate(
		sources.File{Path: `@@peer\Music\01.flac`},
		sources.File{Path: `@@peer\Music\02.flac`},
	)}}
	service := NewService(store, downloader, zerolog.Nop())

	offer, err := service.CheckSource(context.Background(), store.request.ID)
	if err != nil {
		t.Fatal(err)
	}
	if offer.Stale() {
		t.Fatalf("offer = %#v, want an unreported size to prove nothing", offer)
	}
}

// Another peer's folder is not this one, however well it matches the release.
func TestCheckSourceIgnoresAnotherPeersFolder(t *testing.T) {
	store := &fakeStore{request: recheckableRow(), settings: enabledSettings()}
	elsewhere := offeredCandidate(sources.File{Path: `@@other\Music\01.flac`, SizeBytes: 100})
	elsewhere.Username = "another-peer"
	downloader := &fakeDownloader{candidates: []sources.Candidate{elsewhere}}
	service := NewService(store, downloader, zerolog.Nop())

	offer, err := service.CheckSource(context.Background(), store.request.ID)
	if err != nil {
		t.Fatal(err)
	}
	if offer.Found {
		t.Fatalf("offer = %#v, want another peer's folder to say nothing about this one", offer)
	}
}

func TestStartEnqueuesEveryFileAndFollowsProgress(t *testing.T) {
	store := &fakeStore{request: requestRow(), settings: enabledSettings()}
	downloader := &fakeDownloader{}
	service := NewService(store, downloader, zerolog.Nop())

	row, err := service.Start(context.Background(), store.request.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != "started" || !store.started {
		t.Fatalf("row = %#v", row)
	}
	if downloader.startedFor != "peer" || len(downloader.startedWith) != 2 {
		t.Fatalf("enqueued %q with %#v", downloader.startedFor, downloader.startedWith)
	}
	// The remote path is what a peer serves, so that is what gets sent.
	if downloader.startedWith[0].Path != `@@peer\Music\01.flac` {
		t.Fatalf("sent path = %q", downloader.startedWith[0].Path)
	}
	if !store.pollQueued {
		t.Fatal("progress is not being followed")
	}
}

// A few peers hold every transfer until a word is typed in their private chat,
// and they send that message the moment they are asked for a file. Starting is
// therefore what schedules the read of it, dated forward so a burst of starts
// is one read rather than one each.
func TestStartAsksForThePeersMessagesToBeRead(t *testing.T) {
	store := &fakeStore{request: requestRow(), settings: enabledSettings()}
	service := NewService(store, &fakeDownloader{}, zerolog.Nop())

	if _, err := service.Start(context.Background(), store.request.ID, true); err != nil {
		t.Fatal(err)
	}
	if store.challengesQueuedFor.IsZero() {
		t.Fatal("nothing will read what the peer said")
	}
	if !store.challengesQueuedFor.After(time.Now()) {
		t.Fatalf("next read = %v, want a future time", store.challengesQueuedFor)
	}
}

// The point of a retry is that the peer is asked only for what did not arrive.
func TestRetryAsksTheProviderOnlyForTheFailedFiles(t *testing.T) {
	row := requestRow()
	row.Status = "failed"
	store := &fakeStore{
		request: row, settings: enabledSettings(),
		retryFiles: []db.DownloadRequestFile{row.Files[1]},
	}
	downloader := &fakeDownloader{}
	service := NewService(store, downloader, zerolog.Nop())

	updated, err := service.Retry(context.Background(), row.ID, []string{row.Files[1].Path})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != "started" {
		t.Fatalf("status = %q, want the request following its retry", updated.Status)
	}
	if len(downloader.startedWith) != 1 || downloader.startedWith[0].Path != row.Files[1].Path {
		t.Fatalf("enqueued %#v, want only the file that failed", downloader.startedWith)
	}
}

func TestRetryFollowsTheProgressOfTheFilesItQueued(t *testing.T) {
	row := requestRow()
	row.Status = "failed"
	store := &fakeStore{
		request: row, settings: enabledSettings(),
		retryFiles: []db.DownloadRequestFile{row.Files[0]},
	}
	service := NewService(store, &fakeDownloader{}, zerolog.Nop())

	if _, err := service.Retry(context.Background(), row.ID, nil); err != nil {
		t.Fatal(err)
	}
	if !store.pollQueued {
		t.Fatal("a retried transfer is not being followed")
	}
}

// A refused retry must leave the request failed with the provider's reason,
// rather than stranded as started with transfers nobody is running.
func TestRetryRevertsToFailedWhenTheProviderRefuses(t *testing.T) {
	row := requestRow()
	row.Status = "failed"
	store := &fakeStore{
		request: row, settings: enabledSettings(),
		retryFiles: []db.DownloadRequestFile{row.Files[0]},
	}
	downloader := &fakeDownloader{startErr: errors.New("peer is offline")}
	service := NewService(store, downloader, zerolog.Nop())

	updated, err := service.Retry(context.Background(), row.ID, nil)
	if err == nil {
		t.Fatal("expected the provider failure to be reported")
	}
	if updated.Status != "failed" {
		t.Fatalf("status = %q, want the request left failed", updated.Status)
	}
	if store.retryReverted != "peer is offline" {
		t.Fatalf("reverted with %q", store.retryReverted)
	}
	if store.pollQueued {
		t.Fatal("nothing is running, so nothing should be polled")
	}
}

// Nothing may be requeued when there is no provider to serve it: rows claiming
// to be queued at a provider that cannot transfer would never settle.
func TestRetryRequeuesNothingWithoutAProviderThatCanTransfer(t *testing.T) {
	row := requestRow()
	row.Status = "failed"
	store := &fakeStore{request: row, settings: enabledSettings()}
	service := NewService(store, &fakeDownloader{searchOnly: true}, zerolog.Nop())

	if _, err := service.Retry(context.Background(), row.ID, nil); !errors.Is(err, ErrNotSupported) {
		t.Fatalf("error = %v, want ErrNotSupported", err)
	}
	if store.retryAsked {
		t.Fatal("transfers were requeued without a provider to run them")
	}
}

// A provider that refuses must leave the request recorded rather than claiming
// to be running, and must say why.
func TestStartRevertsWhenTheProviderRefuses(t *testing.T) {
	store := &fakeStore{request: requestRow(), settings: enabledSettings()}
	downloader := &fakeDownloader{startErr: errors.New("slskd rejected the API key")}
	service := NewService(store, downloader, zerolog.Nop())

	row, err := service.Start(context.Background(), store.request.ID, true)
	if err == nil {
		t.Fatal("expected the provider failure to be reported")
	}
	if row.Status != "requested" {
		t.Fatalf("status = %q, want the request left recorded", row.Status)
	}
	if store.revertedWith != "slskd rejected the API key" {
		t.Fatalf("reverted with %q", store.revertedWith)
	}
	if store.pollQueued {
		t.Fatal("nothing is running, so nothing should be polled")
	}
}

func TestStartRefusesWithoutAProviderThatCanTransfer(t *testing.T) {
	store := &fakeStore{request: requestRow(), settings: enabledSettings()}
	service := NewService(store, &fakeDownloader{searchOnly: true}, zerolog.Nop())
	if _, err := service.Start(context.Background(), store.request.ID, true); !errors.Is(err, ErrNotSupported) {
		t.Fatalf("error = %v, want ErrNotSupported", err)
	}

	unconfigured := &fakeStore{request: requestRow(), unconfigured: true}
	service = NewService(unconfigured, &fakeDownloader{}, zerolog.Nop())
	if _, err := service.Start(context.Background(), unconfigured.request.ID, true); !errors.Is(err, sources.ErrNotConfigured) {
		t.Fatalf("error = %v, want ErrNotConfigured", err)
	}
	if unconfigured.started {
		t.Fatal("a request was started without a provider to start it at")
	}

	disabled := &fakeStore{request: requestRow(), settings: enabledSettings()}
	disabled.settings.Enabled = false
	service = NewService(disabled, &fakeDownloader{}, zerolog.Nop())
	if _, err := service.Start(context.Background(), disabled.request.ID, true); !errors.Is(err, sources.ErrNotConfigured) {
		t.Fatalf("disabled provider error = %v, want ErrNotConfigured", err)
	}
}

func TestPollRecordsWhatTheProviderReports(t *testing.T) {
	store := &fakeStore{request: requestRow(), settings: enabledSettings()}
	store.request.Status = "started"
	downloader := &fakeDownloader{transfers: []sources.Transfer{
		{ID: "t1", Path: `@@peer\Music\01.flac`, State: sources.TransferCompleted, SizeBytes: 100, TransferredBytes: 100},
		{ID: "t2", Path: `@@peer\Music\02.flac`, State: sources.TransferDownloading, SizeBytes: 200, TransferredBytes: 50},
		// A transfer for something this request never asked for is ignored.
		{ID: "t3", Path: `@@peer\Other\09.flac`, State: sources.TransferCompleted},
	}}
	service := NewService(store, downloader, zerolog.Nop())

	active, err := service.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !active {
		t.Fatal("a transfer is still downloading, so polling should continue")
	}
	if len(store.recorded) != 2 {
		t.Fatalf("recorded = %#v", store.recorded)
	}
	if store.recorded[0].ProviderID != "t1" || store.recorded[0].TransferredBytes != 100 {
		t.Fatalf("first progress = %#v", store.recorded[0])
	}
}

// A file the provider no longer mentions is left alone: slskd forgets finished
// transfers, and a forgotten transfer is not a lost one.
func TestPollLeavesForgottenTransfersAlone(t *testing.T) {
	store := &fakeStore{request: requestRow(), settings: enabledSettings(), statusAfterAll: "completed"}
	store.request.Status = "started"
	service := NewService(store, &fakeDownloader{}, zerolog.Nop()).WithImportQueue()

	active, err := service.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(store.recorded) != 0 {
		t.Fatalf("recorded %#v for transfers the provider never mentioned", store.recorded)
	}
	if active {
		t.Fatal("a settled request should stop the polling")
	}
	if !store.importQueued {
		t.Fatal("a completed transfer must queue validation and import")
	}
}

func TestPollStopsWhenNothingIsStarted(t *testing.T) {
	store := &fakeStore{request: requestRow(), settings: enabledSettings()}
	service := NewService(store, &fakeDownloader{}, zerolog.Nop())

	active, err := service.Poll(context.Background())
	if err != nil || active {
		t.Fatalf("active = %v, err = %v; want no polling with nothing started", active, err)
	}
}

func TestCancelStopsRunningTransfersFirst(t *testing.T) {
	store := &fakeStore{request: requestRow(), settings: enabledSettings(), transferIDs: []string{"t1", "t2"}}
	store.request.Status = "started"
	downloader := &fakeDownloader{}
	service := NewService(store, downloader, zerolog.Nop())

	row, err := service.Cancel(context.Background(), store.request.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(downloader.cancelled) != 2 {
		t.Fatalf("stopped %#v, want both transfers stopped at the provider", downloader.cancelled)
	}
	if !store.cancelled || row.Status != "cancelled" {
		t.Fatalf("row = %#v", row)
	}
}

// Withdrawing a request that never started needs no provider at all.
func TestCancelDoesNotNeedAProviderForARecordedRequest(t *testing.T) {
	store := &fakeStore{request: requestRow(), unconfigured: true}
	service := NewService(store, &fakeDownloader{}, zerolog.Nop())

	row, err := service.Cancel(context.Background(), store.request.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !store.cancelled || row.Status != "cancelled" {
		t.Fatalf("row = %#v", row)
	}
}

// A started request whose provider is gone can still be withdrawn: leaving it
// stuck would be worse than withdrawing it without stopping anything.
func TestCancelWithdrawsAStartedRequestWithoutAProvider(t *testing.T) {
	store := &fakeStore{request: requestRow(), unconfigured: true, transferIDs: []string{"t1"}}
	store.request.Status = "started"
	service := NewService(store, &fakeDownloader{}, zerolog.Nop())

	row, err := service.Cancel(context.Background(), store.request.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !store.cancelled || row.Status != "cancelled" {
		t.Fatalf("row = %#v", row)
	}
}

// The refusal shows what is gone rather than only that something is: the answer
// it asks for is only as good as what the person answering was shown.
func TestARefusedStartNamesTheFileOfferedAtADifferentSize(t *testing.T) {
	store := &fakeStore{request: recheckableRow(), settings: enabledSettings()}
	downloader := &fakeDownloader{candidates: []sources.Candidate{offeredCandidate(
		sources.File{Path: `@@peer\Music\01.flac`, SizeBytes: 100},
		sources.File{Path: `@@peer\Music\02.flac`, SizeBytes: 999},
	)}}
	service := NewService(store, downloader, zerolog.Nop())

	_, err := service.Start(context.Background(), store.request.ID, false)
	if !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("error = %v, want ErrSourceChanged", err)
	}
	if !strings.Contains(err.Error(), "different size") || !strings.Contains(err.Error(), "02.flac") {
		t.Fatalf("error = %q, want it to name the resized file", err)
	}
}

// A request serving a want has no release, so the words it was asked for in are
// the words the folder is looked for by.
func TestASourceCheckLooksForAWantByTheWordsItWasAskedFor(t *testing.T) {
	row := requestRow()
	row.SourceDirectory = `@@peer\Music`
	row.EntryArtist, row.EntryTitle = "Anetha", "Candy from Strangers"
	store := &fakeStore{request: row, settings: enabledSettings()}
	downloader := &fakeDownloader{}
	service := NewService(store, downloader, zerolog.Nop())

	if _, err := service.CheckSource(context.Background(), row.ID); err != nil {
		t.Fatal(err)
	}
	if len(downloader.searched) == 0 ||
		!strings.Contains(downloader.searched[0], "Candy from Strangers") {
		t.Fatalf("searched %v, want the want's own words", downloader.searched)
	}
}

// A request with nothing to search for asks the provider nothing, rather than
// broadcasting an empty phrase and reading whatever comes back.
func TestASourceCheckAsksNothingWhenThereIsNothingToSearchFor(t *testing.T) {
	row := requestRow()
	row.SourceDirectory = `@@peer\Music`
	store := &fakeStore{request: row, settings: enabledSettings()}
	downloader := &fakeDownloader{}
	service := NewService(store, downloader, zerolog.Nop())

	offer, err := service.CheckSource(context.Background(), row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(downloader.searched) != 0 {
		t.Fatalf("searched %v, want nothing asked", downloader.searched)
	}
	if offer.Found {
		t.Fatalf("offer = %#v, want a search nobody made to prove nothing", offer)
	}
}

func TestCheckSourceReportsARequestItCouldNotRead(t *testing.T) {
	store := &fakeStore{request: recheckableRow(), settings: enabledSettings()}
	store.requestErr = errors.New("the requests table is unreachable")
	service := NewService(store, &fakeDownloader{}, zerolog.Nop())

	if _, err := service.CheckSource(context.Background(), store.request.ID); !errors.Is(err, store.requestErr) {
		t.Fatalf("error = %v, want the read failure reported", err)
	}
}

// Without a provider there is nobody to ask what is on offer, and saying
// "nothing" would read as a peer who withdrew the folder.
func TestCheckSourceRefusesWithoutAProviderThatCanTransfer(t *testing.T) {
	store := &fakeStore{request: recheckableRow(), unconfigured: true}
	service := NewService(store, &fakeDownloader{}, zerolog.Nop())

	if _, err := service.CheckSource(context.Background(), store.request.ID); !errors.Is(
		err, sources.ErrNotConfigured,
	) {
		t.Fatalf("error = %v, want %v", err, sources.ErrNotConfigured)
	}
}

func TestStartReportsARequestItCouldNotRead(t *testing.T) {
	store := &fakeStore{request: recheckableRow(), settings: enabledSettings()}
	store.requestErr = errors.New("the requests table is unreachable")
	service := NewService(store, &fakeDownloader{}, zerolog.Nop())

	if _, err := service.Start(context.Background(), store.request.ID, false); !errors.Is(
		err, store.requestErr,
	) {
		t.Fatalf("error = %v, want the read failure reported", err)
	}
	if store.started {
		t.Fatal("a request nobody could read was started anyway")
	}
}

// A request that could not be claimed is one another worker may already be
// running, so nothing is enqueued at the provider.
func TestNothingIsEnqueuedForARequestThatCannotBeClaimed(t *testing.T) {
	store := &fakeStore{request: requestRow(), settings: enabledSettings()}
	store.startErr = errors.New("the requests table is unreachable")
	downloader := &fakeDownloader{}
	service := NewService(store, downloader, zerolog.Nop())

	if _, err := service.Start(context.Background(), store.request.ID, true); !errors.Is(
		err, store.startErr,
	) {
		t.Fatalf("error = %v, want the claim failure reported", err)
	}
	if downloader.startedFor != "" {
		t.Fatalf("enqueued at %q for a request that was never claimed", downloader.startedFor)
	}
}

// When the provider refuses and the request cannot be put back, the provider's
// refusal is what the caller is told: it is the failure that actually happened.
func TestAProviderRefusalIsReportedEvenWhenTheRequestCannotBeReverted(t *testing.T) {
	store := &fakeStore{request: requestRow(), settings: enabledSettings()}
	store.revertErr = errors.New("the requests table is unreachable")
	downloader := &fakeDownloader{startErr: errors.New("slskd rejected the API key")}
	service := NewService(store, downloader, zerolog.Nop())

	_, err := service.Start(context.Background(), store.request.ID, true)
	if err == nil || !strings.Contains(err.Error(), "rejected the API key") {
		t.Fatalf("error = %v, want the provider's refusal reported", err)
	}
}

// The transfers are running by then. Failing to queue the poller only means
// progress is not followed yet, and the next start queues it.
func TestAStartedRequestStandsWhenThePollerCannotBeQueued(t *testing.T) {
	store := &fakeStore{request: requestRow(), settings: enabledSettings()}
	store.pollQueueErr = errors.New("the jobs table is unreachable")
	service := NewService(store, &fakeDownloader{}, zerolog.Nop())

	row, err := service.Start(context.Background(), store.request.ID, true)
	if err != nil {
		t.Fatalf("an unqueued poller failed a start that had already happened: %v", err)
	}
	if row.Status != "started" {
		t.Fatalf("status = %q, want the request following its transfers", row.Status)
	}
}

func TestARetriedRequestStandsWhenThePollerCannotBeQueued(t *testing.T) {
	row := requestRow()
	row.Status = "failed"
	store := &fakeStore{
		request: row, settings: enabledSettings(),
		retryFiles: []db.DownloadRequestFile{row.Files[0]},
	}
	store.pollQueueErr = errors.New("the jobs table is unreachable")
	service := NewService(store, &fakeDownloader{}, zerolog.Nop())

	if _, err := service.Retry(context.Background(), row.ID, nil); err != nil {
		t.Fatalf("an unqueued poller failed a retry that had already happened: %v", err)
	}
}

func TestNothingIsRequeuedForAFailedRequestThatCannotBeClaimed(t *testing.T) {
	row := requestRow()
	row.Status = "failed"
	store := &fakeStore{
		request: row, settings: enabledSettings(),
		retryErr: errors.New("the requests table is unreachable"),
	}
	downloader := &fakeDownloader{}
	service := NewService(store, downloader, zerolog.Nop())

	if _, err := service.Retry(context.Background(), row.ID, nil); !errors.Is(err, store.retryErr) {
		t.Fatalf("error = %v, want the claim failure reported", err)
	}
	if downloader.startedFor != "" {
		t.Fatalf("requeued at %q for a request that was never claimed", downloader.startedFor)
	}
}

func TestAProviderRefusalIsReportedEvenWhenARetryCannotBeReverted(t *testing.T) {
	row := requestRow()
	row.Status = "failed"
	store := &fakeStore{
		request: row, settings: enabledSettings(),
		retryFiles:     []db.DownloadRequestFile{row.Files[0]},
		retryRevertErr: errors.New("the requests table is unreachable"),
	}
	downloader := &fakeDownloader{startErr: errors.New("peer is offline")}
	service := NewService(store, downloader, zerolog.Nop())

	_, err := service.Retry(context.Background(), row.ID, nil)
	if err == nil || !strings.Contains(err.Error(), "peer is offline") {
		t.Fatalf("error = %v, want the provider's refusal reported", err)
	}
}

func TestCancelReportsARequestItCouldNotRead(t *testing.T) {
	store := &fakeStore{request: requestRow(), settings: enabledSettings()}
	store.requestErr = errors.New("the requests table is unreachable")
	service := NewService(store, &fakeDownloader{}, zerolog.Nop())

	if _, err := service.Cancel(context.Background(), store.request.ID); !errors.Is(
		err, store.requestErr,
	) {
		t.Fatalf("error = %v, want the read failure reported", err)
	}
	if store.cancelled {
		t.Fatal("a request nobody could read was withdrawn")
	}
}

// A request whose transfers are still running at the provider must not be
// withdrawn while they are: slskd would go on downloading music nobody asked
// for, with no record of it.
func TestARequestIsNotWithdrawnWhileItsTransfersCannotBeStopped(t *testing.T) {
	store := &fakeStore{request: requestRow(), settings: enabledSettings(), transferIDs: []string{"t1"}}
	store.request.Status = "started"
	downloader := &fakeDownloader{cancelDownloadErr: errors.New("slskd is unreachable")}
	service := NewService(store, downloader, zerolog.Nop())

	_, err := service.Cancel(context.Background(), store.request.ID)
	if err == nil || !strings.Contains(err.Error(), "stop transfer t1") {
		t.Fatalf("error = %v, want the transfer that would not stop named", err)
	}
	if store.cancelled {
		t.Fatal("a request was withdrawn while its transfers were still running")
	}
}

func TestCancelReportsTransferIDsItCouldNotRead(t *testing.T) {
	store := &fakeStore{request: requestRow(), settings: enabledSettings()}
	store.request.Status = "started"
	store.transferIDsErr = errors.New("the downloads table is unreachable")
	service := NewService(store, &fakeDownloader{}, zerolog.Nop())

	if _, err := service.Cancel(context.Background(), store.request.ID); !errors.Is(
		err, store.transferIDsErr,
	) {
		t.Fatalf("error = %v, want the read failure reported", err)
	}
	if store.cancelled {
		t.Fatal("a request was withdrawn without knowing what was running for it")
	}
}

// A started request with no transfers recorded has nothing to stop, so the
// provider is not asked at all.
func TestAStartedRequestWithNoTransfersIsWithdrawnWithoutAskingTheProvider(t *testing.T) {
	store := &fakeStore{request: requestRow(), settings: enabledSettings()}
	store.request.Status = "started"
	downloader := &fakeDownloader{}
	service := NewService(store, downloader, zerolog.Nop())

	if _, err := service.Cancel(context.Background(), store.request.ID); err != nil {
		t.Fatal(err)
	}
	if len(downloader.cancelled) != 0 {
		t.Fatalf("stopped %v, want nothing asked of the provider", downloader.cancelled)
	}
	if !store.cancelled {
		t.Fatal("the request was not withdrawn")
	}
}

// A provider that could not be built is not the same as one that is gone: the
// first is a failure to report, the second a reason to withdraw anyway.
func TestARequestIsNotWithdrawnWhenTheProviderCannotBeBuilt(t *testing.T) {
	store := &fakeStore{request: requestRow(), settings: enabledSettings(), transferIDs: []string{"t1"}}
	store.request.Status = "started"
	store.settingsReadErr = errors.New("the settings table is unreachable")
	service := NewService(store, &fakeDownloader{}, zerolog.Nop())

	if _, err := service.Cancel(context.Background(), store.request.ID); !errors.Is(
		err, store.settingsReadErr,
	) {
		t.Fatalf("error = %v, want the settings failure reported", err)
	}
	if store.cancelled {
		t.Fatal("a request was withdrawn without knowing whether its transfers could be stopped")
	}
}

func TestCancelReportsAWithdrawalItCouldNotRecord(t *testing.T) {
	store := &fakeStore{request: requestRow(), settings: enabledSettings()}
	store.cancelErr = errors.New("the requests table is unreachable")
	service := NewService(store, &fakeDownloader{}, zerolog.Nop())

	if _, err := service.Cancel(context.Background(), store.request.ID); !errors.Is(
		err, store.cancelErr,
	) {
		t.Fatalf("error = %v, want the write failure reported", err)
	}
}

func TestPollReportsARequestListItCouldNotRead(t *testing.T) {
	store := &fakeStore{request: requestRow(), settings: enabledSettings()}
	store.startedErr = errors.New("the requests table is unreachable")
	service := NewService(store, &fakeDownloader{}, zerolog.Nop())

	if _, err := service.Poll(context.Background()); !errors.Is(err, store.startedErr) {
		t.Fatalf("error = %v, want the read failure reported", err)
	}
}

// Transfers are running and there is nothing to ask about them. That is a
// configuration failure to report rather than a set of requests to settle.
func TestPollRefusesWithoutAProviderWhileTransfersAreRunning(t *testing.T) {
	store := &fakeStore{request: requestRow(), unconfigured: true}
	store.request.Status = "started"
	service := NewService(store, &fakeDownloader{}, zerolog.Nop())

	if _, err := service.Poll(context.Background()); !errors.Is(err, sources.ErrNotConfigured) {
		t.Fatalf("error = %v, want %v", err, sources.ErrNotConfigured)
	}
}

// A peer nobody can reach says nothing about its transfers, so they go on being
// followed rather than being read as finished.
func TestAPeerThatCannotBeReachedLeavesItsTransfersRunning(t *testing.T) {
	store := &fakeStore{request: requestRow(), settings: enabledSettings()}
	store.request.Status = "started"
	downloader := &fakeDownloader{transferErr: errors.New("slskd is unreachable")}
	service := NewService(store, downloader, zerolog.Nop())

	active, err := service.Poll(context.Background())
	if err != nil {
		t.Fatalf("one unreachable peer failed the whole poll: %v", err)
	}
	if !active {
		t.Fatal("a peer nobody could reach was read as having finished")
	}
	if len(store.recorded) != 0 {
		t.Fatalf("recorded %#v about a peer that said nothing", store.recorded)
	}
}

func TestPollReportsProgressItCouldNotRecord(t *testing.T) {
	store := &fakeStore{request: requestRow(), settings: enabledSettings()}
	store.request.Status = "started"
	store.progressErr = errors.New("the downloads table is unreachable")
	downloader := &fakeDownloader{transfers: []sources.Transfer{
		{ID: "t1", Path: `@@peer\Music\01.flac`, State: sources.TransferCompleted},
	}}
	service := NewService(store, downloader, zerolog.Nop())

	if _, err := service.Poll(context.Background()); !errors.Is(err, store.progressErr) {
		t.Fatalf("error = %v, want the write failure reported", err)
	}
}

// A completed request that could not be queued for validation would sit in the
// inbox forever, so the poll fails and is run again.
func TestPollFailsWhenACompletedRequestCannotBeQueuedForImport(t *testing.T) {
	store := &fakeStore{request: requestRow(), settings: enabledSettings(), statusAfterAll: "completed"}
	store.request.Status = "started"
	store.importQueueErr = errors.New("the jobs table is unreachable")
	service := NewService(store, &fakeDownloader{}, zerolog.Nop()).WithImportQueue()

	_, err := service.Poll(context.Background())
	if err == nil || !strings.Contains(err.Error(), "queue download import") {
		t.Fatalf("error = %v, want the failure to queue validation reported", err)
	}
}

// The worker sleeps for this between polls. A zero interval would turn
// following a transfer into a spin.
func TestThePollIntervalIsSomethingToWaitFor(t *testing.T) {
	service := NewService(&fakeStore{}, &fakeDownloader{}, zerolog.Nop())

	if service.PollInterval() <= 0 {
		t.Fatalf("poll interval = %v, want a wait", service.PollInterval())
	}
}

// Nothing may be started through a Schall that was wired without any provider
// at all, and the reason has to read as configuration rather than as a failure.
func TestNothingIsStartedWithoutAConfiguredProvider(t *testing.T) {
	store := &fakeStore{request: requestRow(), settings: enabledSettings()}
	service := NewService(store, nil, zerolog.Nop())

	if _, err := service.Start(context.Background(), store.request.ID, true); !errors.Is(
		err, sources.ErrNotConfigured,
	) {
		t.Fatalf("error = %v, want %v", err, sources.ErrNotConfigured)
	}
	if store.started {
		t.Fatal("a request was started with no provider wired at all")
	}
}

// Settings nobody could read are not the same as settings nobody has entered.
// One is a failure worth retrying; the other is a question for the user.
func TestNothingIsStartedWhenTheProviderSettingsCannotBeRead(t *testing.T) {
	store := &fakeStore{request: requestRow()}
	store.settingsReadErr = errors.New("the settings table is unreachable")
	service := NewService(store, &fakeDownloader{}, zerolog.Nop())

	_, err := service.Start(context.Background(), store.request.ID, true)
	if !errors.Is(err, store.settingsReadErr) {
		t.Fatalf("error = %v, want the read failure reported", err)
	}
	if errors.Is(err, sources.ErrNotConfigured) {
		t.Fatal("a database failure was reported as a provider nobody configured")
	}
}

func TestNothingIsStartedWhenTheProviderCannotBeBuilt(t *testing.T) {
	store := &fakeStore{request: requestRow(), settings: enabledSettings()}
	downloader := &fakeDownloader{buildErr: errors.New("the base URL is not a URL")}
	service := NewService(store, downloader, zerolog.Nop())

	if _, err := service.Start(context.Background(), store.request.ID, true); !errors.Is(
		err, downloader.buildErr,
	) {
		t.Fatalf("error = %v, want the build failure reported", err)
	}
	if store.started {
		t.Fatal("a request was started at a provider that could not be built")
	}
}

// Starting is what a person is watching for, so the view is told at once rather
// than on its next timer.
func TestStartingARequestTellsTheInterface(t *testing.T) {
	store := &fakeStore{request: requestRow(), settings: enabledSettings()}
	hub := events.NewHub()
	notices, unsubscribe := hub.Subscribe()
	defer unsubscribe()
	service := NewService(store, &fakeDownloader{}, zerolog.Nop()).WithEvents(hub)

	if _, err := service.Start(context.Background(), store.request.ID, true); err != nil {
		t.Fatal(err)
	}

	select {
	case topic := <-notices:
		if topic != events.TopicDownloads {
			t.Fatalf("notice = %q, want the downloads topic", topic)
		}
	default:
		t.Fatal("a started request published nothing")
	}
}

// wantRequests are two copies chosen for two wants from one person's folder.
// They are separate requests, because a want is followed one copy at a time,
// and they are one question, because the peer counts what one stranger has
// queued with it and refuses everything past its limit.
func wantRequests() (map[uuid.UUID]db.DownloadRequestRow, []uuid.UUID) {
	rows := make(map[uuid.UUID]db.DownloadRequestRow, 2)
	ids := make([]uuid.UUID, 0, 2)
	for _, name := range []string{"01.flac", "02.flac"} {
		row := db.DownloadRequestRow{
			ID: uuid.New(), Status: "requested", SourceUsername: "peer",
			AcquisitionTargetID: uuid.NullUUID{UUID: uuid.New(), Valid: true},
			Files: []db.DownloadRequestFile{
				{Path: `@@peer\Music\` + name, Name: name, SizeBytes: 100},
			},
		}
		rows[row.ID] = row
		ids = append(ids, row.ID)
	}
	return rows, ids
}

func TestSeveralRequestsForOnePeerAreEnqueuedInOneCall(t *testing.T) {
	rows, ids := wantRequests()
	store := &fakeStore{byID: rows, settings: enabledSettings()}
	downloader := &fakeDownloader{}
	service := NewService(store, downloader, zerolog.Nop())

	if err := service.StartTogether(context.Background(), ids); err != nil {
		t.Fatal(err)
	}

	if downloader.starts != 1 {
		t.Fatalf("asked the peer %d times, want one question for both copies", downloader.starts)
	}
	if len(downloader.startedWith) != 2 {
		t.Fatalf("enqueued %#v, want both files in the one request", downloader.startedWith)
	}
}

// The batch is how the peer is asked and never what a want is: every request is
// claimed in its own right, so each keeps its own transfers and its own place in
// the one-open-request-per-want rule.
func TestEveryRequestOfABatchIsStartedInItsOwnRight(t *testing.T) {
	rows, ids := wantRequests()
	store := &fakeStore{byID: rows, settings: enabledSettings()}
	service := NewService(store, &fakeDownloader{}, zerolog.Nop())

	if err := service.StartTogether(context.Background(), ids); err != nil {
		t.Fatal(err)
	}

	if len(store.startedIDs) != 2 {
		t.Fatalf("claimed %v, want each request started in its own right", store.startedIDs)
	}
	for _, id := range ids {
		if row := store.byID[id]; row.Status != "started" {
			t.Errorf("request %s = %q, want it started", id, row.Status)
		}
	}
}

// One enqueue goes to one person. A batch that names two of them is a caller's
// mistake, and splitting it quietly would hide the mistake rather than the
// files.
func TestABatchAddressedToTwoPeersIsRefused(t *testing.T) {
	rows, ids := wantRequests()
	second := rows[ids[1]]
	second.SourceUsername = "somebody else"
	rows[ids[1]] = second
	store := &fakeStore{byID: rows, settings: enabledSettings()}
	downloader := &fakeDownloader{}
	service := NewService(store, downloader, zerolog.Nop())

	if err := service.StartTogether(context.Background(), ids); !errors.Is(err, ErrNotOnePeer) {
		t.Fatalf("error = %v, want a batch spanning two peers refused", err)
	}
	if downloader.starts != 0 {
		t.Fatal("nothing may be enqueued for a batch that could not go out whole")
	}
	// Including the request the mismatch was found on. A row left claiming to be
	// started with nothing on its way holds its want's one open request forever.
	for _, id := range ids {
		if row := store.byID[id]; row.Status != "requested" {
			t.Errorf("request %s = %q, want it back as merely recorded", id, row.Status)
		}
	}
}

// A batch the provider would not take leaves nothing claiming to be running.
// Half a batch started is worse than none: the rows the want reads would say a
// copy is on its way that nobody is sending.
func TestABatchTheProviderRefusedLeavesNothingStarted(t *testing.T) {
	rows, ids := wantRequests()
	store := &fakeStore{byID: rows, settings: enabledSettings()}
	downloader := &fakeDownloader{startErr: errors.New("peer is offline")}
	service := NewService(store, downloader, zerolog.Nop())

	if err := service.StartTogether(context.Background(), ids); err == nil {
		t.Fatal("want the provider's refusal reported")
	}

	if len(store.revertedIDs) != 2 {
		t.Fatalf("reverted %v, want every request of the batch put back", store.revertedIDs)
	}
	for _, id := range ids {
		if row := store.byID[id]; row.Status != "requested" {
			t.Errorf("request %s = %q, want it back as merely recorded", id, row.Status)
		}
	}
}

// "Too many files" is the peer counting what we already have queued with it. It
// is not the copy failing, so it is recorded as backpressure and the want comes
// back to this peer shortly instead of striking the copy off for a day.
func TestAPeerTooBusyToSendACopyIsNotACopyThatFailed(t *testing.T) {
	store := &fakeStore{request: requestRow(), settings: enabledSettings(), statusAfterAll: "failed"}
	store.request.Status = "started"
	store.request.AcquisitionTargetID = uuid.NullUUID{UUID: uuid.New(), Valid: true}
	downloader := &fakeDownloader{transfers: []sources.Transfer{
		{ID: "t1", Path: `@@peer\Music\01.flac`, State: sources.TransferFailed,
			Detail: "Completed, Rejected: Transfer rejected: Too many files", Deferred: true},
	}}
	service := NewService(store, downloader, zerolog.Nop())

	if _, err := service.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}

	if !store.deferredID.Valid || store.deferredID.UUID != store.request.ID {
		t.Fatalf("deferred = %v, want the want's copy recorded as merely put off", store.deferredID)
	}
}

// The distinction only means anything if it holds the other way. A peer that
// stopped sharing the file has answered about the file, and asking again in a
// quarter of an hour would be told the same thing.
func TestATransferThePeerRefusedOutrightIsNotDeferred(t *testing.T) {
	store := &fakeStore{request: requestRow(), settings: enabledSettings(), statusAfterAll: "failed"}
	store.request.Status = "started"
	store.request.AcquisitionTargetID = uuid.NullUUID{UUID: uuid.New(), Valid: true}
	downloader := &fakeDownloader{transfers: []sources.Transfer{
		{ID: "t1", Path: `@@peer\Music\01.flac`, State: sources.TransferFailed,
			Detail: "Completed, Rejected: Transfer rejected: File not shared."},
	}}
	service := NewService(store, downloader, zerolog.Nop())

	if _, err := service.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}

	if store.deferredID.Valid {
		t.Fatal("a peer refusing outright was read as backpressure")
	}
}

// A transfer that fails outright is done for the same as a copy that is
// refused or proven: the want must not sit out the rest meant for a copy that
// might still arrive, so it is brought back to now and a sweep is asked for.
func TestAFailedTransferWakesTheWantItWasFollowing(t *testing.T) {
	store := &fakeStore{request: requestRow(), settings: enabledSettings(), statusAfterAll: "failed"}
	store.request.Status = "started"
	store.request.AcquisitionTargetID = uuid.NullUUID{UUID: uuid.New(), Valid: true}
	downloader := &fakeDownloader{transfers: []sources.Transfer{
		{ID: "t1", Path: `@@peer\Music\01.flac`, State: sources.TransferFailed,
			Detail: "Completed, Rejected: Transfer rejected: File not shared."},
	}}
	service := NewService(store, downloader, zerolog.Nop())

	if _, err := service.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}

	if !store.rescheduledTargetID.Valid || store.rescheduledTargetID.UUID != store.request.AcquisitionTargetID.UUID {
		t.Fatalf("rescheduled = %v, want the want brought back to now", store.rescheduledTargetID)
	}
}

// A release somebody chose by hand has no want behind it, so a failed transfer
// against it has nothing to reschedule.
func TestAFailedTransferOnAReleaseNobodyIsPursuingReschedulesNothing(t *testing.T) {
	store := &fakeStore{request: requestRow(), settings: enabledSettings(), statusAfterAll: "failed"}
	store.request.Status = "started"
	downloader := &fakeDownloader{transfers: []sources.Transfer{
		{ID: "t1", Path: `@@peer\Music\01.flac`, State: sources.TransferFailed,
			Detail: "Completed, Rejected: Transfer rejected: File not shared."},
	}}
	service := NewService(store, downloader, zerolog.Nop())

	if _, err := service.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}

	if store.rescheduledTargetID.Valid {
		t.Fatal("a release with no want behind it was rescheduled as if it had one")
	}
}

// A request that failed for two reasons at once has genuinely failed. Reading
// the whole of it as backpressure because part of it was would keep asking a
// peer for a file it has already said it does not have.
func TestAMixedFailureIsNotDeferred(t *testing.T) {
	store := &fakeStore{request: requestRow(), settings: enabledSettings(), statusAfterAll: "failed"}
	store.request.Status = "started"
	store.request.AcquisitionTargetID = uuid.NullUUID{UUID: uuid.New(), Valid: true}
	downloader := &fakeDownloader{transfers: []sources.Transfer{
		{ID: "t1", Path: `@@peer\Music\01.flac`, State: sources.TransferFailed, Deferred: true},
		{ID: "t2", Path: `@@peer\Music\02.flac`, State: sources.TransferFailed},
	}}
	service := NewService(store, downloader, zerolog.Nop())

	if _, err := service.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}

	if store.deferredID.Valid {
		t.Fatal("a request with a real failure in it was read as backpressure")
	}
}

// A release somebody chose by hand has no loop behind it, so nothing here may
// quietly reschedule one on its behalf.
func TestAPeerTooBusyOnAReleaseNobodyIsPursuingIsLeftAlone(t *testing.T) {
	store := &fakeStore{request: requestRow(), settings: enabledSettings(), statusAfterAll: "failed"}
	store.request.Status = "started"
	downloader := &fakeDownloader{transfers: []sources.Transfer{
		{ID: "t1", Path: `@@peer\Music\01.flac`, State: sources.TransferFailed, Deferred: true},
	}}
	service := NewService(store, downloader, zerolog.Nop())

	if _, err := service.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}

	if store.deferredID.Valid {
		t.Fatal("a release download was recorded against a want that does not exist")
	}
}

// The deferral is written before the request settles, so an interruption
// between the two leaves the request still started and the next poll says the
// same thing again. The other order loses the distinction for good: the sweep
// would release the copy as an ordinary one that did not arrive.
func TestAPeerBeingBusyIsRecordedBeforeTheRequestSettles(t *testing.T) {
	store := &fakeStore{request: requestRow(), settings: enabledSettings(), statusAfterAll: "failed"}
	store.request.Status = "started"
	store.request.AcquisitionTargetID = uuid.NullUUID{UUID: uuid.New(), Valid: true}
	store.progressErr = errors.New("the downloads table is unreachable")
	downloader := &fakeDownloader{transfers: []sources.Transfer{
		{ID: "t1", Path: `@@peer\Music\01.flac`, State: sources.TransferFailed, Deferred: true},
	}}
	service := NewService(store, downloader, zerolog.Nop())

	if _, err := service.Poll(context.Background()); !errors.Is(err, store.progressErr) {
		t.Fatalf("error = %v, want the write failure reported", err)
	}

	if !store.deferredID.Valid {
		t.Fatal("the peer's reason was lost with the write that could not settle the request")
	}
}
