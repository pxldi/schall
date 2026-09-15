package newreleases

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/rs/zerolog"

	"github.com/pxldi/schall/internal/db"
)

var (
	fixedNow    = time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	playlistID  = uuid.MustParse("11111111-1111-1111-1111-111111111111")
	oldFileID   = uuid.MustParse("22222222-2222-2222-2222-222222222222")
	newFileID   = uuid.MustParse("33333333-3333-3333-3333-333333333333")
	heldFileID  = uuid.MustParse("44444444-4444-4444-4444-444444444444")
	takenFileID = uuid.MustParse("55555555-5555-5555-5555-555555555555")
)

func toDate(t time.Time) pgtype.Date {
	return pgtype.Date{Time: t, Valid: true}
}

// fakeStore is a hand-rolled double for the Store interface. It records what
// was asked of it and answers from the fields a test sets up first.
type fakeStore struct {
	refreshesAsked int
	settings       db.NewReleasesPlaylistSettingsRow
	settingsErr    error
	saved          db.SaveNewReleasesPlaylistSettingsParams
	saveErr        error

	playlist    db.PlaylistRow
	playlistErr error
	ensured     bool

	candidates    []db.NewReleaseCandidateRow
	candidatesArg time.Time

	members []db.NewReleasePlaylistMemberRow

	withdrawn    int64
	withdrawnErr error

	added   []db.AddNewReleasePlaylistMemberParams
	addErr  error
	dropped []uuid.UUID
	dropErr error

	forgotten    int64
	forgottenErr error

	ordered    []uuid.UUID
	orderErr   error
	published  []uuid.UUID
	publishErr error
}

func (fake *fakeStore) NewReleasesPlaylistSettings(context.Context) (db.NewReleasesPlaylistSettingsRow, error) {
	if fake.settingsErr != nil {
		return db.NewReleasesPlaylistSettingsRow{}, fake.settingsErr
	}
	return fake.settings, nil
}

func (fake *fakeStore) SaveNewReleasesPlaylistSettings(
	_ context.Context, params db.SaveNewReleasesPlaylistSettingsParams,
) (db.NewReleasesPlaylistSettingsRow, error) {
	fake.saved = params
	if fake.saveErr != nil {
		return db.NewReleasesPlaylistSettingsRow{}, fake.saveErr
	}
	return db.NewReleasesPlaylistSettingsRow{Enabled: params.Enabled, WindowDays: params.WindowDays}, nil
}

func (fake *fakeStore) EnsureNewReleasesPlaylist(_ context.Context, name string) (db.PlaylistRow, error) {
	fake.ensured = true
	fake.playlist = db.PlaylistRow{ID: playlistID, Name: name}
	fake.playlistErr = nil
	return fake.playlist, nil
}

func (fake *fakeStore) NewReleasesPlaylist(context.Context) (db.PlaylistRow, error) {
	if fake.playlistErr != nil {
		return db.PlaylistRow{}, fake.playlistErr
	}
	return fake.playlist, nil
}

func (fake *fakeStore) NewReleaseCandidates(
	_ context.Context, releasedOnOrAfter time.Time,
) ([]db.NewReleaseCandidateRow, error) {
	fake.candidatesArg = releasedOnOrAfter
	return fake.candidates, nil
}

func (fake *fakeStore) NewReleasePlaylistMembers(context.Context) ([]db.NewReleasePlaylistMemberRow, error) {
	return fake.members, nil
}

func (fake *fakeStore) WithdrawRemovedNewReleaseMembers(context.Context) (int64, error) {
	return fake.withdrawn, fake.withdrawnErr
}

func (fake *fakeStore) AddNewReleasePlaylistMember(
	_ context.Context, params db.AddNewReleasePlaylistMemberParams,
) error {
	if fake.addErr != nil {
		return fake.addErr
	}
	fake.added = append(fake.added, params)
	return nil
}

func (fake *fakeStore) DropNewReleasePlaylistMember(_ context.Context, libraryFileID uuid.UUID) error {
	if fake.dropErr != nil {
		return fake.dropErr
	}
	fake.dropped = append(fake.dropped, libraryFileID)
	return nil
}

func (fake *fakeStore) ForgetNewReleasePlaylistMembers(context.Context) (int64, error) {
	return fake.forgotten, fake.forgottenErr
}

func (fake *fakeStore) OrderNewReleasePlaylistEntries(_ context.Context, playlistID uuid.UUID) error {
	if fake.orderErr != nil {
		return fake.orderErr
	}
	fake.ordered = append(fake.ordered, playlistID)
	return nil
}

func (fake *fakeStore) QueueNewReleasesPlaylistRefresh(_ context.Context, _ time.Time) error {
	fake.refreshesAsked++
	return nil
}

func (fake *fakeStore) QueueNavidromePlaylistSync(_ context.Context, playlistID uuid.UUID) error {
	if fake.publishErr != nil {
		return fake.publishErr
	}
	fake.published = append(fake.published, playlistID)
	return nil
}

func newTestService(store *fakeStore) *Service {
	logger := zerolog.New(io.Discard)
	return NewService(store, logger).WithClock(func() time.Time { return fixedNow })
}

func TestSettingsAreTheDefaultsWhenNobodyHasSavedAny(t *testing.T) {
	store := &fakeStore{settingsErr: pgx.ErrNoRows}
	service := newTestService(store)

	settings, err := service.Settings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !settings.Enabled || settings.WindowDays != defaultWindowDays {
		t.Fatalf("expected the switched-on default window, got %+v", settings)
	}
}

func TestSaveSettingsRefusesAWindowBelowOneDay(t *testing.T) {
	store := &fakeStore{}
	service := newTestService(store)

	_, err := service.SaveSettings(context.Background(), db.SaveNewReleasesPlaylistSettingsParams{
		Enabled: true, WindowDays: 0,
	})
	if !errors.Is(err, ErrWindowOutOfRange) {
		t.Fatalf("expected ErrWindowOutOfRange, got %v", err)
	}
}

func TestSaveSettingsRefusesAWindowOverAYear(t *testing.T) {
	store := &fakeStore{}
	service := newTestService(store)

	_, err := service.SaveSettings(context.Background(), db.SaveNewReleasesPlaylistSettingsParams{
		Enabled: true, WindowDays: 366,
	})
	if !errors.Is(err, ErrWindowOutOfRange) {
		t.Fatalf("expected ErrWindowOutOfRange, got %v", err)
	}
}

func TestRefreshMakesThePlaylistTheFirstTime(t *testing.T) {
	store := &fakeStore{
		settingsErr: pgx.ErrNoRows,
		playlistErr: pgx.ErrNoRows,
	}
	service := newTestService(store)

	result, err := service.Refresh(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !store.ensured {
		t.Fatal("the playlist was never made")
	}
	if !result.PlaylistID.Valid || result.PlaylistID.UUID != playlistID {
		t.Fatalf("the result does not name the playlist it wrote: %+v", result)
	}
}

func TestRefreshAddsAnOwnedTrackFromANewlyFoundRelease(t *testing.T) {
	store := &fakeStore{
		settings: db.NewReleasesPlaylistSettingsRow{Enabled: true, WindowDays: 60},
		playlist: db.PlaylistRow{ID: playlistID},
		candidates: []db.NewReleaseCandidateRow{
			{LibraryFileID: newFileID, ReleasedOn: toDate(fixedNow.AddDate(0, 0, -1)), Title: "New Track"},
		},
	}
	service := newTestService(store)

	result, err := service.Refresh(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Added != 1 || result.Total != 1 {
		t.Fatalf("expected one track added, got %+v", result)
	}
	if len(store.added) != 1 || store.added[0].LibraryFileID != newFileID {
		t.Fatalf("the wrong file was added: %+v", store.added)
	}
	if len(store.ordered) != 1 {
		t.Fatal("the list was never reordered")
	}
	if len(store.published) != 1 {
		t.Fatal("the sync was never queued")
	}
}

func TestRefreshDoesNotReaddATrackAlreadyOnTheList(t *testing.T) {
	store := &fakeStore{
		settings: db.NewReleasesPlaylistSettingsRow{Enabled: true, WindowDays: 60},
		playlist: db.PlaylistRow{ID: playlistID},
		candidates: []db.NewReleaseCandidateRow{
			{LibraryFileID: heldFileID, ReleasedOn: toDate(fixedNow.AddDate(0, 0, -1)), Title: "Held"},
		},
		members: []db.NewReleasePlaylistMemberRow{
			{LibraryFileID: heldFileID, EntryID: uuid.NullUUID{UUID: uuid.New(), Valid: true}},
		},
	}
	service := newTestService(store)

	result, err := service.Refresh(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Added != 0 || result.Total != 1 {
		t.Fatalf("a track already on the list was added again: %+v", result)
	}
	if len(store.added) != 0 {
		t.Fatalf("AddNewReleasePlaylistMember was called for a track already held: %+v", store.added)
	}
}

func TestRefreshDropsATrackWhoseReleaseHasAgedOutOfTheWindow(t *testing.T) {
	store := &fakeStore{
		settings: db.NewReleasesPlaylistSettingsRow{Enabled: true, WindowDays: 60},
		playlist: db.PlaylistRow{ID: playlistID},
		members: []db.NewReleasePlaylistMemberRow{
			{LibraryFileID: oldFileID, EntryID: uuid.NullUUID{UUID: uuid.New(), Valid: true}},
		},
	}
	service := newTestService(store)

	result, err := service.Refresh(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Dropped != 1 || result.Total != 0 {
		t.Fatalf("the aged-out track was not dropped: %+v", result)
	}
	if len(store.dropped) != 1 || store.dropped[0] != oldFileID {
		t.Fatalf("the wrong file was dropped: %+v", store.dropped)
	}
}

// TestRefreshLeavesAWithdrawnTrackOutForGood is the rule that gives the
// playlist its manual-decisions-win behaviour: a person took a track out in
// their player, and even though its release is still inside the window on this
// pass, the refresh must not put it back.
func TestRefreshLeavesAWithdrawnTrackOutForGood(t *testing.T) {
	store := &fakeStore{
		settings: db.NewReleasesPlaylistSettingsRow{Enabled: true, WindowDays: 60},
		playlist: db.PlaylistRow{ID: playlistID},
		candidates: []db.NewReleaseCandidateRow{
			{LibraryFileID: takenFileID, ReleasedOn: toDate(fixedNow.AddDate(0, 0, -1)), Title: "Taken"},
		},
		members: []db.NewReleasePlaylistMemberRow{
			{LibraryFileID: takenFileID, WithdrawnAt: pgtype.Timestamptz{Time: fixedNow.Add(-time.Hour), Valid: true}},
		},
	}
	service := newTestService(store)

	result, err := service.Refresh(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Added != 0 || result.Total != 0 {
		t.Fatalf("a withdrawn track was put back on the list: %+v", result)
	}
	if len(store.added) != 0 {
		t.Fatalf("AddNewReleasePlaylistMember was called for a withdrawn track: %+v", store.added)
	}
	if len(store.dropped) != 0 {
		t.Fatalf("a withdrawn row was dropped, which is not what withdrawal means: %+v", store.dropped)
	}
}

func TestRefreshRecordsWhatSomebodyTookOutInThePlayer(t *testing.T) {
	store := &fakeStore{
		settings:  db.NewReleasesPlaylistSettingsRow{Enabled: true, WindowDays: 60},
		playlist:  db.PlaylistRow{ID: playlistID},
		withdrawn: 2,
	}
	service := newTestService(store)

	result, err := service.Refresh(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Withdrawn != 2 {
		t.Fatalf("expected the withdrawal count to be reported, got %+v", result)
	}
}

func TestRefreshSkipsWhenSwitchedOffAndThereIsNoPlaylistYet(t *testing.T) {
	store := &fakeStore{
		settings:    db.NewReleasesPlaylistSettingsRow{Enabled: false},
		playlistErr: pgx.ErrNoRows,
	}
	service := newTestService(store)

	result, err := service.Refresh(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Skipped {
		t.Fatalf("expected a skipped pass, got %+v", result)
	}
	if store.ensured {
		t.Fatal("a playlist was made for a feature that has never been turned on")
	}
}

func TestRefreshEmptiesTheListWhenSwitchedOff(t *testing.T) {
	store := &fakeStore{
		settings: db.NewReleasesPlaylistSettingsRow{Enabled: false},
		playlist: db.PlaylistRow{ID: playlistID},
		members: []db.NewReleasePlaylistMemberRow{
			{LibraryFileID: oldFileID, EntryID: uuid.NullUUID{UUID: uuid.New(), Valid: true}},
			{LibraryFileID: takenFileID, WithdrawnAt: pgtype.Timestamptz{Time: fixedNow, Valid: true}},
		},
	}
	service := newTestService(store)

	result, err := service.Refresh(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Dropped != 1 {
		t.Fatalf("expected the one held member to be dropped, got %+v", result)
	}
	if len(store.dropped) != 1 || store.dropped[0] != oldFileID {
		t.Fatalf("the wrong member was emptied out: %+v", store.dropped)
	}
	// The library file itself is never touched by any of this: emptying the
	// list is a playlist operation and nothing else this test could see.
	if len(store.published) != 1 {
		t.Fatal("the sync was never told the list changed")
	}
}

func TestRefreshForgetsMembersWhenThePlaylistWasDeleted(t *testing.T) {
	store := &fakeStore{
		settings:    db.NewReleasesPlaylistSettingsRow{Enabled: true, WindowDays: 60},
		playlistErr: pgx.ErrNoRows,
		forgotten:   3,
	}
	service := newTestService(store)

	if _, err := service.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !store.ensured {
		t.Fatal("a deleted playlist should be remade once the pass finds no list")
	}
}

func TestOverviewReadsSettingsWithNoPlaylistYet(t *testing.T) {
	store := &fakeStore{
		settingsErr: pgx.ErrNoRows,
		playlistErr: pgx.ErrNoRows,
	}
	service := newTestService(store)

	overview, err := service.Overview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if overview.Exists {
		t.Fatal("a playlist that has never been made should not be reported as existing")
	}
	if !overview.Settings.Enabled {
		t.Fatal("the default settings are switched on")
	}
}

// The settings are the list. Turning the feature on, or widening the window
// from two months to six, changes what belongs on the playlist, and a person
// who pressed Save meant that to happen. Before this the press did nothing
// visible until the follow feed next ran or a scan brought a file in — days, on
// a library that has settled — and the only thing to do about it was wait.
func TestSavingTheSettingsAsksForTheListToBeWrittenAgain(t *testing.T) {
	store := &fakeStore{}
	service := NewService(store, zerolog.Nop())

	if _, err := service.SaveSettings(context.Background(),
		db.SaveNewReleasesPlaylistSettingsParams{Enabled: true, WindowDays: 180}); err != nil {
		t.Fatalf("SaveSettings() error = %v", err)
	}

	if store.refreshesAsked != 1 {
		t.Fatalf("refreshes asked for = %d, want one", store.refreshesAsked)
	}
}

// A window nobody can have is refused before anything is written, so nothing is
// saved and no refresh is asked for.
func TestARefusedWindowAsksForNothing(t *testing.T) {
	store := &fakeStore{}
	service := NewService(store, zerolog.Nop())

	if _, err := service.SaveSettings(context.Background(),
		db.SaveNewReleasesPlaylistSettingsParams{Enabled: true, WindowDays: 4000}); !errors.Is(err, ErrWindowOutOfRange) {
		t.Fatalf("SaveSettings() error = %v, want ErrWindowOutOfRange", err)
	}
	if store.refreshesAsked != 0 {
		t.Fatalf("refreshes asked for = %d, want none", store.refreshesAsked)
	}
}
