package weekly

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/rs/zerolog"

	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/library"
	"github.com/pxldi/schall/internal/recommendations"
)

// A note on what these tests are for.
//
// This package is the only thing in Schall that deletes music without somebody
// pressing a button. Every test below fixes one rule about when it may and when
// it may not, and the rules that keep music are the ones worth the most: a
// signal Schall could not read must never end in a deletion.

var (
	fixedNow  = time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	fileID    = uuid.MustParse("11111111-1111-1111-1111-111111111111")
	targetID  = uuid.MustParse("22222222-2222-2222-2222-222222222221")
	recording = uuid.MustParse("33333333-3333-3333-3333-333333333331")
	leaseID   = uuid.MustParse("66666666-6666-6666-6666-666666666661")
)

func stampAt(at time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: at, Valid: true}
}

// heldLease is a song whose week is up and which nothing has judged yet.
func heldLease() db.LibraryFileLeaseRow {
	return db.LibraryFileLeaseRow{
		ID:                     leaseID,
		LibraryFileID:          uuid.NullUUID{UUID: fileID, Valid: true},
		LibraryPath:            "/music/Weekly/Portishead - Glory Box.flac",
		MusicBrainzRecordingID: recording,
		AcquisitionTargetID:    targetID,
		State:                  "held",
		GrantedAt:              stampAt(fixedNow.Add(-8 * 24 * time.Hour)),
		ExpiresAt:              stampAt(fixedNow.Add(-time.Hour)),
		EntryArtist:            "Portishead",
		EntryTitle:             "Glory Box",
	}
}

// leavingLease is a song that was announced for removal a week ago and whose
// date has now arrived.
func leavingLease() db.LibraryFileLeaseRow {
	lease := heldLease()
	lease.State = "leaving"
	lease.RemovesAt = stampAt(fixedNow.Add(-time.Minute))
	lease.LeavingRunID = uuid.NullUUID{UUID: uuid.New(), Valid: true}
	return lease
}

type keptCall struct {
	leaseID uuid.UUID
	keptBy  string
	runID   uuid.NullUUID
}

type announceCall struct {
	leaseID   uuid.UUID
	removesAt time.Time
}

type extendCall struct {
	leaseID   uuid.UUID
	expiresAt time.Time
	reason    string
}

type unkeptCall struct {
	recordingID     uuid.UUID
	suppressedUntil time.Time
}

type fakeStore struct {
	settings    db.WeeklyPlaylistSettingsRow
	settingsErr error
	leases      []db.LibraryFileLeaseRow
	pressed     map[uuid.UUID]struct{}
	arrivals    []db.WeeklyPlaylistArrivalRow
	openWants   int
	staleWants  int
	leaseByID   map[uuid.UUID]db.LibraryFileLeaseRow

	entryArtists    []string
	libraryPool     []db.WeeklyLibraryCandidateRow
	libraryPicks    []uuid.UUID
	ownedEntries    []uuid.UUID
	droppedLibrary  int
	staleWantsAfter []time.Time

	started        []db.StartWeeklyPlaylistRunParams
	startedRuns    []string
	finished       []db.FinishWeeklyPlaylistRunParams
	kept           []keptCall
	announced      []announceCall
	extended       []extendCall
	removed        []uuid.UUID
	abandoned      []uuid.UUID
	reads          []db.RecordLibraryFileKeepReadParams
	unkept         []unkeptCall
	settledTargets []uuid.UUID
	granted        []db.GrantLibraryFileLeaseParams
	appended       []db.AppendWeeklyPlaylistEntryParams
	createdTargets []db.CreateAcquisitionTargetParams
	dropped        int
	sweepsQueued   int
	announceErr    error
	refreshQueued  []time.Time
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		settings:  db.WeeklyPlaylistSettingsRow{Enabled: true, SongsPerWeek: 20, Mode: ModeRemove},
		pressed:   map[uuid.UUID]struct{}{},
		leaseByID: map[uuid.UUID]db.LibraryFileLeaseRow{},
	}
}

func (store *fakeStore) WeeklyPlaylistSettings(context.Context) (db.WeeklyPlaylistSettingsRow, error) {
	return store.settings, store.settingsErr
}

func (store *fakeStore) SaveWeeklyPlaylistSettings(
	_ context.Context, params db.SaveWeeklyPlaylistSettingsParams,
) (db.WeeklyPlaylistSettingsRow, error) {
	store.settings = db.WeeklyPlaylistSettingsRow{
		Enabled: params.Enabled, SongsPerWeek: params.SongsPerWeek, Mode: params.Mode,
	}
	return store.settings, nil
}

func (store *fakeStore) EnsureWeeklyPlaylist(context.Context, string) (db.PlaylistRow, error) {
	return db.PlaylistRow{ID: uuid.MustParse("44444444-4444-4444-4444-444444444441"), Source: "weekly"}, nil
}

func (store *fakeStore) WeeklyPlaylist(context.Context) (db.PlaylistRow, error) {
	return store.EnsureWeeklyPlaylist(context.Background(), "")
}

func (store *fakeStore) StartWeeklyPlaylistRun(
	_ context.Context, params db.StartWeeklyPlaylistRunParams,
) (db.WeeklyPlaylistRunRow, error) {
	store.startedRuns = append(store.startedRuns, params.Mode)
	store.started = append(store.started, params)
	return db.WeeklyPlaylistRunRow{
		ID:         uuid.MustParse("55555555-5555-5555-5555-555555555551"),
		PlaylistID: params.PlaylistID, Mode: params.Mode, Status: "running",
		LibraryShare: params.LibraryShare, OnePerArtist: params.OnePerArtist,
	}, nil
}

func (store *fakeStore) FinishWeeklyPlaylistRun(
	_ context.Context, params db.FinishWeeklyPlaylistRunParams,
) (db.WeeklyPlaylistRunRow, error) {
	store.finished = append(store.finished, params)
	return db.WeeklyPlaylistRunRow{ID: params.ID, Status: params.Status, Detail: params.Detail}, nil
}

func (store *fakeStore) AbandonRunningWeeklyPlaylistRuns(context.Context, string) (int64, error) {
	return 0, nil
}

func (store *fakeStore) WeeklyPlaylistRuns(context.Context, int32) ([]db.WeeklyPlaylistRunRow, error) {
	return nil, nil
}

func (store *fakeStore) LiveLibraryFileLeases(context.Context) ([]db.LibraryFileLeaseRow, error) {
	return store.leases, nil
}

func (store *fakeStore) LiveLibraryFileLeaseForFile(_ context.Context, id uuid.UUID) (db.LibraryFileLeaseRow, error) {
	for _, lease := range store.leases {
		if lease.LibraryFileID.Valid && lease.LibraryFileID.UUID == id {
			return lease, nil
		}
	}
	return db.LibraryFileLeaseRow{}, pgx.ErrNoRows
}

func (store *fakeStore) LibraryFileLease(_ context.Context, id uuid.UUID) (db.LibraryFileLeaseRow, error) {
	if lease, ok := store.leaseByID[id]; ok {
		return lease, nil
	}
	for _, lease := range store.leases {
		if lease.ID == id {
			return lease, nil
		}
	}
	return db.LibraryFileLeaseRow{}, pgx.ErrNoRows
}

func (store *fakeStore) GrantLibraryFileLease(
	_ context.Context, params db.GrantLibraryFileLeaseParams,
) (db.LibraryFileLeaseRow, error) {
	store.granted = append(store.granted, params)
	return db.LibraryFileLeaseRow{ID: uuid.New()}, nil
}

func (store *fakeStore) KeepLibraryFileLease(
	_ context.Context, id uuid.UUID, keptBy string, runID uuid.NullUUID,
) (bool, error) {
	store.kept = append(store.kept, keptCall{leaseID: id, keptBy: keptBy, runID: runID})
	return true, nil
}

func (store *fakeStore) AnnounceLibraryFileLeaseDeparture(
	_ context.Context, id, _ uuid.UUID, removesAt time.Time,
) error {
	if store.announceErr != nil {
		return store.announceErr
	}
	store.announced = append(store.announced, announceCall{leaseID: id, removesAt: removesAt})
	return nil
}

func (store *fakeStore) ExtendLibraryFileLease(
	_ context.Context, id uuid.UUID, expiresAt time.Time, reason string,
) error {
	store.extended = append(store.extended, extendCall{leaseID: id, expiresAt: expiresAt, reason: reason})
	return nil
}

func (store *fakeStore) RemoveLibraryFileLease(_ context.Context, id, _ uuid.UUID) error {
	store.removed = append(store.removed, id)
	return nil
}

func (store *fakeStore) AbandonLibraryFileLease(_ context.Context, id, _ uuid.UUID) error {
	store.abandoned = append(store.abandoned, id)
	return nil
}

func (store *fakeStore) RecordLibraryFileKeepRead(
	_ context.Context, params db.RecordLibraryFileKeepReadParams,
) error {
	store.reads = append(store.reads, params)
	return nil
}

func (store *fakeStore) LibraryFileKeepReads(context.Context, uuid.UUID) ([]db.LibraryFileKeepReadRow, error) {
	return nil, nil
}

func (store *fakeStore) PressedKeeps(context.Context) (map[uuid.UUID]struct{}, error) {
	return store.pressed, nil
}

func (store *fakeStore) RecordRecommendationUnkept(
	_ context.Context, recordingID uuid.UUID, _ uuid.NullUUID, suppressedUntil time.Time,
) error {
	store.unkept = append(store.unkept, unkeptCall{recordingID: recordingID, suppressedUntil: suppressedUntil})
	return nil
}

func (store *fakeStore) SettleWeeklyTargetNotWanted(_ context.Context, id uuid.UUID, _ string) error {
	store.settledTargets = append(store.settledTargets, id)
	return nil
}

func (store *fakeStore) WeeklyPlaylistArrivals(context.Context, uuid.UUID) ([]db.WeeklyPlaylistArrivalRow, error) {
	return store.arrivals, nil
}

func (store *fakeStore) WeeklyPlaylistOpenWants(context.Context, uuid.UUID) (int, error) {
	return store.openWants, nil
}

func (store *fakeStore) DropSettledWeeklyPlaylistEntries(context.Context, uuid.UUID) (int64, error) {
	store.dropped++
	return 0, nil
}

func (store *fakeStore) AppendWeeklyPlaylistEntry(
	_ context.Context, params db.AppendWeeklyPlaylistEntryParams,
) (db.PlaylistEntryRow, error) {
	store.appended = append(store.appended, params)
	return db.PlaylistEntryRow{ID: uuid.New()}, nil
}

func (store *fakeStore) WeeklyPlaylistStaleWants(
	_ context.Context, _ uuid.UUID, before time.Time,
) (int, error) {
	store.staleWantsAfter = append(store.staleWantsAfter, before)
	return store.staleWants, nil
}

func (store *fakeStore) WeeklyPlaylistEntryArtists(context.Context, uuid.UUID) ([]string, error) {
	return store.entryArtists, nil
}

func (store *fakeStore) WeeklyLibraryPoolCandidates(
	_ context.Context, _ uuid.UUID, _ time.Time, limit int,
) ([]db.WeeklyLibraryCandidateRow, error) {
	if len(store.libraryPool) > limit {
		return store.libraryPool[:limit], nil
	}
	return store.libraryPool, nil
}

func (store *fakeStore) RecordWeeklyLibraryPick(
	_ context.Context, recordingID, _ uuid.UUID, _ uuid.NullUUID,
) error {
	store.libraryPicks = append(store.libraryPicks, recordingID)
	return nil
}

func (store *fakeStore) DropWeeklyLibraryEntries(context.Context, uuid.UUID) (int64, error) {
	store.droppedLibrary++
	return 0, nil
}

func (store *fakeStore) MarkPlaylistEntryOwned(_ context.Context, entryID uuid.UUID, _ uuid.NullUUID) error {
	store.ownedEntries = append(store.ownedEntries, entryID)
	return nil
}

func (store *fakeStore) CreatePlaylistEntryTarget(
	_ context.Context, _ uuid.UUID, params db.CreateAcquisitionTargetParams,
) (uuid.UUID, error) {
	store.createdTargets = append(store.createdTargets, params)
	return uuid.New(), nil
}

func (store *fakeStore) QueueWeeklyPlaylistRefresh(_ context.Context, at time.Time) error {
	store.refreshQueued = append(store.refreshQueued, at)
	return nil
}

func (store *fakeStore) QueueAcquisitionSweep(context.Context, time.Time) error {
	store.sweepsQueued++
	return nil
}

// fakeStars is a player that answers whatever the test says it answers.
type fakeStars struct {
	answers map[string]SignalRead
	err     error
	asked   [][]Song
}

func (stars *fakeStars) Stars(_ context.Context, songs []Song) (map[string]SignalRead, error) {
	stars.asked = append(stars.asked, songs)
	if stars.err != nil {
		return nil, stars.err
	}
	return stars.answers, nil
}

type fakeRemover struct {
	removed []uuid.UUID
	err     error
}

func (remover *fakeRemover) Remove(_ context.Context, id uuid.UUID) (string, error) {
	if remover.err != nil {
		return "", remover.err
	}
	remover.removed = append(remover.removed, id)
	return "/music/gone.flac", nil
}

type fakeRecommender struct {
	candidates []recommendations.Candidate
	err        error
}

func (recommender *fakeRecommender) List(context.Context, string) ([]recommendations.Candidate, error) {
	return recommender.candidates, recommender.err
}

func newService(store Store) *Service {
	return NewService(store, zerolog.New(io.Discard)).
		WithClock(func() time.Time { return fixedNow })
}

func TestARefreshDoesNothingAtAllWhileTheWeeklyPlaylistIsSwitchedOff(t *testing.T) {
	store := newFakeStore()
	store.settings.Enabled = false
	store.leases = []db.LibraryFileLeaseRow{heldLease()}

	result, err := newService(store).Refresh(context.Background())
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if !result.Skipped {
		t.Fatal("a refresh ran while the feature was switched off")
	}
	if len(store.startedRuns) != 0 {
		t.Fatalf("a run was opened while the feature was switched off: %v", store.startedRuns)
	}
}

func TestASecondRefreshStandsAsideWhileOneIsAlreadyRunning(t *testing.T) {
	store := &blockedStore{fakeStore: newFakeStore()}

	result, err := newService(store).Refresh(context.Background())
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if !result.Skipped {
		t.Fatal("a second refresh ran beside the first")
	}
}

type blockedStore struct{ *fakeStore }

func (store *blockedStore) StartWeeklyPlaylistRun(
	context.Context, db.StartWeeklyPlaylistRunParams,
) (db.WeeklyPlaylistRunRow, error) {
	return db.WeeklyPlaylistRunRow{}, db.ErrWeeklyRunInFlight
}

func TestASongTheUserStarredIsKept(t *testing.T) {
	store := newFakeStore()
	lease := heldLease()
	store.leases = []db.LibraryFileLeaseRow{lease}
	stars := &fakeStars{answers: map[string]SignalRead{lease.LibraryPath: {Reading: Present}}}

	service := newService(store).WithStars(stars).WithRemover(&fakeRemover{})
	if _, err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	if len(store.kept) != 1 || store.kept[0].keptBy != signalStar {
		t.Fatalf("the starred song was not kept: %+v", store.kept)
	}
	if len(store.announced) != 0 {
		t.Fatalf("a starred song was announced for removal: %+v", store.announced)
	}
	if len(store.reads) != 1 || store.reads[0].Outcome != outcomeKept {
		t.Fatalf("the star was not recorded as a keep: %+v", store.reads)
	}
}

func TestASongTheUserPressedKeepOnIsKeptWithoutAskingThePlayer(t *testing.T) {
	store := newFakeStore()
	lease := heldLease()
	store.leases = []db.LibraryFileLeaseRow{lease}
	store.pressed = map[uuid.UUID]struct{}{lease.ID: {}}
	stars := &fakeStars{answers: map[string]SignalRead{lease.LibraryPath: {Reading: Absent}}}

	service := newService(store).WithStars(stars).WithRemover(&fakeRemover{})
	if _, err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	if len(store.kept) != 1 || store.kept[0].keptBy != signalKeep {
		t.Fatalf("a pressed Keep did not keep the song: %+v", store.kept)
	}
	if len(store.announced) != 0 {
		t.Fatalf("a song somebody kept was announced for removal: %+v", store.announced)
	}
}

func TestASongNobodyKeptIsAnnouncedRatherThanRemoved(t *testing.T) {
	store := newFakeStore()
	lease := heldLease()
	store.leases = []db.LibraryFileLeaseRow{lease}
	stars := &fakeStars{answers: map[string]SignalRead{lease.LibraryPath: {Reading: Absent}}}
	remover := &fakeRemover{}

	service := newService(store).WithStars(stars).WithRemover(remover)
	if _, err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	if len(store.announced) != 1 {
		t.Fatalf("the song was not announced: %+v", store.announced)
	}
	if want := fixedNow.Add(graceWeek); !store.announced[0].removesAt.Equal(want) {
		t.Fatalf("the removal was dated %s, want %s", store.announced[0].removesAt, want)
	}
	if len(remover.removed) != 0 {
		t.Fatalf("a song was deleted in the same refresh that announced it: %v", remover.removed)
	}
}

func TestAnAnnouncedSongIsRemovedOnlyWhenItsDateHasArrived(t *testing.T) {
	store := newFakeStore()
	lease := leavingLease()
	store.leases = []db.LibraryFileLeaseRow{lease}
	stars := &fakeStars{answers: map[string]SignalRead{lease.LibraryPath: {Reading: Absent}}}
	remover := &fakeRemover{}

	service := newService(store).WithStars(stars).WithRemover(remover)
	if _, err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	if len(remover.removed) != 1 || remover.removed[0] != fileID {
		t.Fatalf("the announced song was not deleted: %v", remover.removed)
	}
	if len(store.removed) != 1 {
		t.Fatalf("the lease was not settled as removed: %v", store.removed)
	}
	if len(store.settledTargets) != 1 || store.settledTargets[0] != targetID {
		t.Fatalf("the want was not settled: %v", store.settledTargets)
	}
	if len(store.unkept) != 1 || store.unkept[0].recordingID != recording {
		t.Fatalf("the recording was not suppressed: %+v", store.unkept)
	}
	if want := fixedNow.Add(unkeptWindow); !store.unkept[0].suppressedUntil.Equal(want) {
		t.Fatalf("the suppression ends %s, want %s", store.unkept[0].suppressedUntil, want)
	}
}

// The lease can change after the due list was read. A changed lease no longer
// licenses this removal.
func TestAnAnnouncedSongWhoseLeaseChangedIsNotRemoved(t *testing.T) {
	store := newFakeStore()
	lease := leavingLease()
	store.leases = []db.LibraryFileLeaseRow{lease}
	changed := lease
	changed.State = "kept"
	store.leaseByID[lease.ID] = changed
	remover := &fakeRemover{}

	service := newService(store).
		WithStars(&fakeStars{answers: map[string]SignalRead{lease.LibraryPath: {Reading: Absent}}}).
		WithRemover(remover)
	if _, err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	if len(remover.removed) != 0 {
		t.Fatalf("a file with a changed lease was deleted: %v", remover.removed)
	}
	if len(store.removed) != 0 {
		t.Fatalf("a changed lease was settled as removed: %v", store.removed)
	}
}

func TestAnAnnouncedSongWhoseDateHasNotArrivedIsLeftAlone(t *testing.T) {
	store := newFakeStore()
	lease := leavingLease()
	lease.RemovesAt = stampAt(fixedNow.Add(3 * 24 * time.Hour))
	store.leases = []db.LibraryFileLeaseRow{lease}
	remover := &fakeRemover{}

	service := newService(store).
		WithStars(&fakeStars{answers: map[string]SignalRead{lease.LibraryPath: {Reading: Absent}}}).
		WithRemover(remover)
	if _, err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	if len(remover.removed) != 0 {
		t.Fatalf("a song was deleted before the date it was given: %v", remover.removed)
	}
	if len(store.reads) != 0 {
		t.Fatalf("a song that is not due was judged: %+v", store.reads)
	}
}

func TestAStarThatCouldNotBeReadKeepsTheSongRatherThanAnnouncingIt(t *testing.T) {
	store := newFakeStore()
	lease := heldLease()
	store.leases = []db.LibraryFileLeaseRow{lease}
	stars := &fakeStars{answers: map[string]SignalRead{
		lease.LibraryPath: {Reading: Unreadable, Detail: "navidrome does not have a song at that path"},
	}}
	remover := &fakeRemover{}

	service := newService(store).WithStars(stars).WithRemover(remover)
	if _, err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	if len(store.announced) != 0 || len(remover.removed) != 0 {
		t.Fatal("a song nothing could be read about was put on its way out")
	}
	if len(store.extended) != 1 {
		t.Fatalf("the song was not given more time: %+v", store.extended)
	}
	if store.extended[0].reason != "navidrome does not have a song at that path" {
		t.Fatalf("the extension does not say why: %q", store.extended[0].reason)
	}
	if len(store.reads) != 1 || store.reads[0].Outcome != outcomeUnreadable {
		t.Fatalf("the failed read was not recorded as unreadable: %+v", store.reads)
	}
}

func TestAPlayerThatDidNotAnswerAtAllKeepsEverySongDue(t *testing.T) {
	store := newFakeStore()
	lease := heldLease()
	store.leases = []db.LibraryFileLeaseRow{lease}
	stars := &fakeStars{err: errors.New("navidrome did not answer")}
	remover := &fakeRemover{}

	service := newService(store).WithStars(stars).WithRemover(remover)
	if _, err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	if len(store.announced) != 0 || len(remover.removed) != 0 {
		t.Fatal("a player being unreachable put music on its way out")
	}
	if len(store.extended) != 1 || store.extended[0].reason != "navidrome did not answer" {
		t.Fatalf("the extension does not name the outage: %+v", store.extended)
	}
}

func TestASecondReadThatFailedWithdrawsAnAnnouncedRemoval(t *testing.T) {
	store := newFakeStore()
	lease := leavingLease()
	store.leases = []db.LibraryFileLeaseRow{lease}
	stars := &fakeStars{answers: map[string]SignalRead{
		lease.LibraryPath: {Reading: Unreadable, Detail: "navidrome did not answer"},
	}}
	remover := &fakeRemover{}

	service := newService(store).WithStars(stars).WithRemover(remover)
	if _, err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	if len(remover.removed) != 0 {
		t.Fatalf("an announced song was deleted on a read that failed: %v", remover.removed)
	}
	if len(store.extended) != 1 || store.extended[0].leaseID != lease.ID {
		t.Fatalf("the announced removal was not withdrawn: %+v", store.extended)
	}
}

func TestAKeepPressedDuringTheWeekOfNoticeStopsTheRemoval(t *testing.T) {
	store := newFakeStore()
	lease := leavingLease()
	store.leases = []db.LibraryFileLeaseRow{lease}
	store.pressed = map[uuid.UUID]struct{}{lease.ID: {}}
	remover := &fakeRemover{}

	service := newService(store).
		WithStars(&fakeStars{answers: map[string]SignalRead{lease.LibraryPath: {Reading: Absent}}}).
		WithRemover(remover)
	if _, err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	if len(remover.removed) != 0 {
		t.Fatalf("a song somebody kept was deleted: %v", remover.removed)
	}
	if len(store.kept) != 1 || store.kept[0].keptBy != signalKeep {
		t.Fatalf("the pressed Keep did not settle the lease: %+v", store.kept)
	}
}

func TestNothingIsRemovedWhileNoPlayerIsConnected(t *testing.T) {
	store := newFakeStore()
	lease := heldLease()
	store.leases = []db.LibraryFileLeaseRow{lease}
	remover := &fakeRemover{}

	// No star reader at all: the only signal left is the Keep button, and a
	// week's deletions must not rest on a control nobody has found.
	service := newService(store).WithRemover(remover)
	if _, err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	if len(store.announced) != 0 || len(remover.removed) != 0 {
		t.Fatal("music was put on its way out with no player to say it was kept")
	}
	if len(store.extended) != 1 {
		t.Fatalf("the song was not given more time: %+v", store.extended)
	}
}

func TestAReportingRefreshSaysWhatItWouldRemoveAndRemovesNothing(t *testing.T) {
	store := newFakeStore()
	store.settings.Mode = ModeReport
	lease := heldLease()
	store.leases = []db.LibraryFileLeaseRow{lease}
	remover := &fakeRemover{}

	service := newService(store).
		WithStars(&fakeStars{answers: map[string]SignalRead{lease.LibraryPath: {Reading: Absent}}}).
		WithRemover(remover)
	if _, err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	if len(store.announced) != 0 || len(remover.removed) != 0 {
		t.Fatal("a reporting refresh acted on what it found")
	}
	if len(store.finished) != 1 || store.finished[0].LeavingCount != 1 {
		t.Fatalf("the run did not count what it would have removed: %+v", store.finished)
	}
}

func TestASongDeletedByHandClosesItsLeaseWithoutSettlingTheWant(t *testing.T) {
	store := newFakeStore()
	lease := heldLease()
	lease.LibraryFileID = uuid.NullUUID{}
	store.leases = []db.LibraryFileLeaseRow{lease}
	remover := &fakeRemover{}

	service := newService(store).
		WithStars(&fakeStars{answers: map[string]SignalRead{}}).
		WithRemover(remover)
	if _, err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	if len(store.abandoned) != 1 || store.abandoned[0] != lease.ID {
		t.Fatalf("the lease was not closed: %v", store.abandoned)
	}
	if len(store.settledTargets) != 0 {
		t.Fatalf("a want was settled for a file nobody here deleted: %v", store.settledTargets)
	}
	if len(store.unkept) != 0 {
		t.Fatalf("a recording was suppressed for a file nobody here deleted: %+v", store.unkept)
	}
}

func TestASongThatArrivedIsPutOnTrialByTheRunThatFoundIt(t *testing.T) {
	store := newFakeStore()
	store.arrivals = []db.WeeklyPlaylistArrivalRow{{
		EntryID: uuid.New(), TargetID: targetID, LibraryFileID: fileID,
		LibraryPath:            "/music/Weekly/Portishead - Glory Box.flac",
		MusicBrainzRecordingID: recording,
	}}

	service := newService(store)
	if _, err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	if len(store.granted) != 1 {
		t.Fatalf("the song that arrived was not put on trial: %+v", store.granted)
	}
	granted := store.granted[0]
	if granted.AcquisitionTargetID != targetID {
		t.Fatalf("the lease does not name the want it filled: %s", granted.AcquisitionTargetID)
	}
	if want := fixedNow.Add(trialWeek); !granted.ExpiresAt.Equal(want) {
		t.Fatalf("the song was given until %s, want %s", granted.ExpiresAt, want)
	}
}

func TestARefreshAsksForTheWeeksSongsAndCountsTheOnesAlreadyBeingLookedFor(t *testing.T) {
	store := newFakeStore()
	store.settings.SongsPerWeek = 5
	store.openWants = 3
	store.arrivals = nil
	suggestions := make([]recommendations.Candidate, 0, 10)
	for i := 0; i < 10; i++ {
		suggestions = append(suggestions, recommendations.Candidate{
			MusicBrainzRecordingID:    uuid.New(),
			MusicBrainzReleaseGroupID: uuid.New(),
			RecordingTitle:            "Song",
			ArtistName:                "Artist",
		})
	}

	service := newService(store).WithRecommender(&fakeRecommender{candidates: suggestions})
	if _, err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	if len(store.createdTargets) != 2 {
		t.Fatalf("the refresh asked for %d songs, want 2", len(store.createdTargets))
	}
	if store.createdTargets[0].Origin != "recommendation" {
		t.Fatalf("the want does not say where it came from: %q", store.createdTargets[0].Origin)
	}
	if store.sweepsQueued != 1 {
		t.Fatalf("the acquisition sweep was woken %d times, want 1", store.sweepsQueued)
	}
}

func TestARefreshAsksForNothingWhileTheWeeksSongsAreStillBeingLookedFor(t *testing.T) {
	store := newFakeStore()
	store.settings.SongsPerWeek = 5
	store.openWants = 5

	service := newService(store).WithRecommender(&fakeRecommender{
		candidates: []recommendations.Candidate{{
			MusicBrainzRecordingID: uuid.New(), RecordingTitle: "Song", ArtistName: "Artist",
		}},
	})
	if _, err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	if len(store.createdTargets) != 0 {
		t.Fatalf("the refresh asked for more songs while a week's worth was still open: %d",
			len(store.createdTargets))
	}
}

func TestKeepingASongOutsideARefreshNamesNoRun(t *testing.T) {
	store := newFakeStore()
	lease := heldLease()
	store.leases = []db.LibraryFileLeaseRow{lease}
	store.leaseByID[lease.ID] = lease

	if _, err := newService(store).Keep(context.Background(), fileID); err != nil {
		t.Fatalf("keep: %v", err)
	}

	if len(store.reads) != 1 || store.reads[0].Signal != signalKeep || store.reads[0].RunID.Valid {
		t.Fatalf("the pressed Keep was recorded as a read by a run: %+v", store.reads)
	}
	if len(store.kept) != 1 || store.kept[0].runID.Valid {
		t.Fatalf("the lease named a run that did not settle it: %+v", store.kept)
	}
}

func TestKeepingASongThatIsNotOnTheWeeklyPlaylistSaysSo(t *testing.T) {
	store := newFakeStore()

	_, err := newService(store).Keep(context.Background(), fileID)
	if !errors.Is(err, ErrNotLeased) {
		t.Fatalf("keeping a song that is not on trial answered %v", err)
	}
}

func TestASettingsScreenCannotChooseAModeNobodyDefined(t *testing.T) {
	store := newFakeStore()

	_, err := newService(store).SaveSettings(context.Background(), db.SaveWeeklyPlaylistSettingsParams{
		Enabled: true, SongsPerWeek: 20, Mode: "delete-everything",
	})
	if err == nil {
		t.Fatal("a mode nobody defined was accepted")
	}
}

func TestAWeekThatCouldNotFinishEverythingSaysWhat(t *testing.T) {
	store := newFakeStore()
	service := newService(store).WithRecommender(&fakeRecommender{err: errors.New("listenbrainz did not answer")})

	if _, err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	if len(store.finished) != 1 {
		t.Fatalf("the run was not closed: %+v", store.finished)
	}
	if store.finished[0].Status != "partial" {
		t.Fatalf("the run ended %q, want partial", store.finished[0].Status)
	}
	if store.finished[0].Detail == "" {
		t.Fatal("a partial run did not say what it could not do")
	}
}

func TestThePlayerIsAskedWithTheTagsSchallHoldsAndNotOnlyThePath(t *testing.T) {
	store := newFakeStore()
	lease := heldLease()
	store.leases = []db.LibraryFileLeaseRow{lease}
	stars := &fakeStars{answers: map[string]SignalRead{lease.LibraryPath: {Reading: Absent}}}

	service := newService(store).WithStars(stars).WithRemover(&fakeRemover{})
	if _, err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	if len(stars.asked) != 1 || len(stars.asked[0]) != 1 {
		t.Fatalf("the player was asked %v", stars.asked)
	}
	asked := stars.asked[0][0]
	if asked.Artist != "Portishead" || asked.Title != "Glory Box" {
		t.Fatalf("the player was asked without the tags: %+v", asked)
	}
}

// The library stopped holding the file between the lease being read and the
// delete being asked for. Nothing was deleted, so nothing is settled and nothing
// is suppressed — the reader may want this recording again tomorrow.
func TestAFileTheLibraryNoLongerHoldsClosesItsLeaseWithoutSettlingTheWant(t *testing.T) {
	store := newFakeStore()
	lease := leavingLease()
	store.leases = []db.LibraryFileLeaseRow{lease}
	remover := &fakeRemover{err: library.ErrFileGone}

	service := newService(store).
		WithStars(&fakeStars{answers: map[string]SignalRead{lease.LibraryPath: {Reading: Absent}}}).
		WithRemover(remover)
	if _, err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	if len(store.abandoned) != 1 || store.abandoned[0] != lease.ID {
		t.Fatalf("the lease was not closed as gone: %v", store.abandoned)
	}
	if len(store.removed) != 0 {
		t.Fatalf("a lease was settled as removed with nothing deleted: %v", store.removed)
	}
	if len(store.settledTargets) != 0 || len(store.unkept) != 0 {
		t.Fatal("a want was settled and a recording suppressed for a file nobody here deleted")
	}
	if len(store.finished) != 1 || store.finished[0].Status != "complete" {
		t.Fatalf("the run ended %+v, want complete", store.finished)
	}
}

// One song nothing could be done about must not cost every other song its
// judgement. The songs after it include the ones somebody starred.
func TestASongThatCouldNotBeJudgedDoesNotStopTheOthersBeingJudged(t *testing.T) {
	store := newFakeStore()
	awkward := heldLease()
	awkward.ID = uuid.MustParse("66666666-6666-6666-6666-666666666662")
	awkward.LibraryPath = "/music/Weekly/Unreachable.flac"
	starred := heldLease()
	store.leases = []db.LibraryFileLeaseRow{awkward, starred}

	service := newService(store).
		WithStars(&fakeStars{answers: map[string]SignalRead{
			awkward.LibraryPath: {Reading: Absent},
			starred.LibraryPath: {Reading: Present},
		}}).
		WithRemover(&fakeRemover{})

	// The first song is announced, and announcing it fails.
	store.announceErr = errors.New("the database refused the announcement")

	if _, err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	if len(store.kept) != 1 || store.kept[0].leaseID != starred.ID {
		t.Fatalf("the starred song was not kept after an earlier song failed: %+v", store.kept)
	}
	if len(store.finished) != 1 || store.finished[0].Status != "partial" {
		t.Fatalf("the run ended %+v, want partial", store.finished)
	}
	if !strings.Contains(store.finished[0].Detail, "Unreachable.flac") {
		t.Fatalf("the run does not name the song it could not judge: %q", store.finished[0].Detail)
	}
}

// libraryPool builds n songs the collection already holds, one artist each.
func libraryPool(n int) []db.WeeklyLibraryCandidateRow {
	pool := make([]db.WeeklyLibraryCandidateRow, 0, n)
	for i := 0; i < n; i++ {
		pool = append(pool, db.WeeklyLibraryCandidateRow{
			LibraryFileID:          uuid.New(),
			MusicBrainzRecordingID: uuid.New(),
			Artist:                 fmt.Sprintf("Artist %d", i),
			Title:                  fmt.Sprintf("Song %d", i),
			Album:                  "Album",
		})
	}
	return pool
}

// suggestionsBy builds n suggestions, all credited to one artist unless spread.
func suggestionsBy(n int, artist string, spread bool) []recommendations.Candidate {
	candidates := make([]recommendations.Candidate, 0, n)
	for i := 0; i < n; i++ {
		name := artist
		if spread {
			name = fmt.Sprintf("%s %d", artist, i)
		}
		candidates = append(candidates, recommendations.Candidate{
			MusicBrainzRecordingID:    uuid.New(),
			MusicBrainzReleaseGroupID: uuid.New(),
			RecordingTitle:            fmt.Sprintf("Suggestion %d", i),
			ArtistName:                name,
		})
	}
	return candidates
}

func TestALibrarySongJoinsTheListWithoutAWant(t *testing.T) {
	store := newFakeStore()
	store.settings.SongsPerWeek = 10
	store.settings.LibraryShare = 50
	store.libraryPool = libraryPool(8)

	service := newService(store).WithRecommender(&fakeRecommender{})
	if _, err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	if len(store.appended) != 5 {
		t.Fatalf("the library share put %d songs on the list, want 5", len(store.appended))
	}
	if len(store.createdTargets) != 0 {
		t.Fatalf("a library song asked for %d downloads, want 0", len(store.createdTargets))
	}
}

func TestALibrarySongIsNeverPutOnTrial(t *testing.T) {
	store := newFakeStore()
	store.settings.SongsPerWeek = 4
	store.settings.LibraryShare = 100
	store.libraryPool = libraryPool(4)

	service := newService(store).WithRecommender(&fakeRecommender{})
	if _, err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	// The lease is the only licence to delete. A library song that never gets
	// one cannot be removed by any later refresh, whatever the mode says.
	if len(store.granted) != 0 {
		t.Fatalf("a library song was put on trial %d times, want 0", len(store.granted))
	}
	if len(store.removed) != 0 {
		t.Fatalf("the refresh removed %d files, want 0", len(store.removed))
	}
}

func TestTheWeekRotatesLastWeeksLibrarySongsOffTheList(t *testing.T) {
	store := newFakeStore()
	store.settings.LibraryShare = 50

	if _, err := newService(store).WithRecommender(&fakeRecommender{}).
		Refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	if store.droppedLibrary != 1 {
		t.Fatalf("the refresh rotated the library songs %d times, want 1", store.droppedLibrary)
	}
}

func TestNoArtistIsAskedForTwiceInOneWeek(t *testing.T) {
	store := newFakeStore()
	store.settings.SongsPerWeek = 5
	store.settings.OnePerArtist = true

	service := newService(store).
		WithRecommender(&fakeRecommender{candidates: suggestionsBy(5, "One Artist", false)})
	if _, err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	if len(store.createdTargets) != 1 {
		t.Fatalf("the refresh asked for %d songs by one artist, want 1", len(store.createdTargets))
	}
}

func TestAnArtistAlreadyOnTheListIsNotAskedForAgain(t *testing.T) {
	store := newFakeStore()
	store.settings.SongsPerWeek = 5
	store.settings.OnePerArtist = true
	store.entryArtists = []string{"One Artist"}

	service := newService(store).
		WithRecommender(&fakeRecommender{candidates: suggestionsBy(5, "One Artist", false)})
	if _, err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	if len(store.createdTargets) != 0 {
		t.Fatalf("the refresh asked for %d songs by an artist already on the list, want 0",
			len(store.createdTargets))
	}
}

func TestAnArtistTwiceIsAllowedWhileTheRuleIsOff(t *testing.T) {
	store := newFakeStore()
	store.settings.SongsPerWeek = 3

	service := newService(store).
		WithRecommender(&fakeRecommender{candidates: suggestionsBy(3, "One Artist", false)})
	if _, err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	if len(store.createdTargets) != 3 {
		t.Fatalf("the refresh asked for %d songs, want 3", len(store.createdTargets))
	}
}

func TestAWantThatHadItsWeekStopsHoldingASlot(t *testing.T) {
	store := newFakeStore()
	store.settings.SongsPerWeek = 5
	store.openWants = 5
	store.staleWants = 3

	service := newService(store).
		WithRecommender(&fakeRecommender{candidates: suggestionsBy(10, "Artist", true)})
	if _, err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	if len(store.createdTargets) != 3 {
		t.Fatalf("the refresh replaced %d unproven picks, want 3", len(store.createdTargets))
	}
	if store.finished[0].ReplacedCount != 3 {
		t.Fatalf("the run reported %d replacements, want 3", store.finished[0].ReplacedCount)
	}
}

func TestAWantThatIsStillYoungKeepsItsSlot(t *testing.T) {
	store := newFakeStore()
	store.settings.SongsPerWeek = 5
	store.openWants = 5
	store.staleWants = 0

	service := newService(store).
		WithRecommender(&fakeRecommender{candidates: suggestionsBy(10, "Artist", true)})
	if _, err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	if len(store.createdTargets) != 0 {
		t.Fatalf("the refresh asked for %d songs, want 0", len(store.createdTargets))
	}
}

func TestAWantIsGivenAWholeTrialWeekBeforeItsSlotIsTakenBack(t *testing.T) {
	store := newFakeStore()
	store.openWants = 1

	if _, err := newService(store).WithRecommender(&fakeRecommender{}).
		Refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	if len(store.staleWantsAfter) != 1 {
		t.Fatalf("the refresh asked about stale wants %d times, want 1", len(store.staleWantsAfter))
	}
	if want := fixedNow.Add(-trialWeek); !store.staleWantsAfter[0].Equal(want) {
		t.Fatalf("a want was called stale from %s, want %s", store.staleWantsAfter[0], want)
	}
}

func TestARefreshAsksForNothingWhileTooMuchIsStillBeingLookedFor(t *testing.T) {
	store := newFakeStore()
	store.settings.SongsPerWeek = 5
	store.openWants = 15
	store.staleWants = 15

	service := newService(store).
		WithRecommender(&fakeRecommender{candidates: suggestionsBy(10, "Artist", true)})
	if _, err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	if len(store.createdTargets) != 0 {
		t.Fatalf("the refresh asked for %d songs past the ceiling, want 0", len(store.createdTargets))
	}
}

func TestAnInstallationThatSetsNoMixTakesNothingFromTheLibrary(t *testing.T) {
	store := newFakeStore()
	store.settings.SongsPerWeek = 5
	store.libraryPool = libraryPool(5)

	service := newService(store).
		WithRecommender(&fakeRecommender{candidates: suggestionsBy(5, "Artist", true)})
	if _, err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	if store.finished[0].LibraryCount != 0 {
		t.Fatalf("the week took %d songs from the library, want 0", store.finished[0].LibraryCount)
	}
	if len(store.createdTargets) != 5 {
		t.Fatalf("the refresh asked for %d songs, want 5", len(store.createdTargets))
	}
}

func TestTheRunSaysWhatTheMixWasThatWeek(t *testing.T) {
	store := newFakeStore()
	store.settings.LibraryShare = 40
	store.settings.OnePerArtist = true

	if _, err := newService(store).WithRecommender(&fakeRecommender{}).
		Refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	if store.started[0].LibraryShare != 40 || !store.started[0].OnePerArtist {
		t.Fatalf("the run recorded share %d and per-artist %v, want 40 and true",
			store.started[0].LibraryShare, store.started[0].OnePerArtist)
	}
}
