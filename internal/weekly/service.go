// Package weekly is the weekly playlist: the part of the recommendation engine
// that acts on its own.
//
// Schall recommends recordings and stops there — a suggestion becomes a want
// only when a person presses "Want it". Once a week this package picks a few
// suggestions, obtains them the ordinary way through the acquisition loop, and
// puts them on a playlist the user's player can see. A song nobody kept is
// removed again, so the collection does not grow by twenty songs a week forever.
//
// Deleting music is the one thing Schall does that cannot be undone, so the
// authority to do it is written down rather than inferred (ADR 0021). A file may
// be deleted here only while a lease row says so, and a lease is granted only by
// a run, for a want that run created. A file that arrived any other way — a
// scan, an import, a want the user made — is unreachable from this package.
//
// The governing rule is the inverse of the matching rule. Everywhere else in
// Schall an absent signal never admits anything. Here absent evidence never
// deletes anything: a signal Schall could not read keeps the song.
package weekly

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/rs/zerolog"

	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/events"
	"github.com/pxldi/schall/internal/library"
	"github.com/pxldi/schall/internal/recommendations"
)

const (
	// PlaylistName is what the list is called in Schall and in the player.
	PlaylistName = "Schall Weekly"

	// trialWeek is how long a song sits on the playlist before it is judged.
	trialWeek = 7 * 24 * time.Hour

	// graceWeek is the notice. A refresh that finds no keep announces a
	// departure and deletes nothing; the refresh whose date has arrived reads
	// the signals again and carries it out. Nothing is deleted that was not
	// announced a week earlier and shown on the screen in between.
	graceWeek = 7 * 24 * time.Hour

	// unkeptWindow is how long a recording that was obtained and not kept stays
	// off the recommendation list. It sits between the 90 days an ignored
	// impression buys and a dismissal, which never expires.
	unkeptWindow = 180 * 24 * time.Hour

	// source is the recommendation list the discover share picks from.
	source = "listenbrainz"

	// libraryWindow is how long the library pool waits before offering a
	// recording again. Without it a collection of a few hundred recordings would
	// show the same songs every week.
	libraryWindow = 180 * 24 * time.Hour

	// wantPatience is how long a want holds its slot on the week's list. A want
	// that has had a whole trial week and is still being searched for stops
	// counting, so the next refresh asks for its full number and the week ships
	// full. The want is left standing and the acquisition loop keeps trying.
	wantPatience = trialWeek

	// openWantCeiling bounds the backlog that patience allows. Slots that free
	// themselves every week would otherwise let unfindable music pile up
	// forever, and every open want is a Soulseek search. Soulseek bans a client
	// that searches quickly. Past this multiple of the week's number, a refresh
	// asks for nothing new and logs why.
	openWantCeiling = 3

	signalStar = "navidrome_star"
	signalKeep = "schall_keep"

	outcomeKept       = "kept"
	outcomeAbsent     = "absent"
	outcomeUnreadable = "unreadable"

	// ModeReport records what a refresh would have removed and removes nothing.
	ModeReport = "report"
	// ModeRemove is the ordinary mode: announce, then carry out.
	ModeRemove = "remove"
)

// ErrNotLeased is returned when somebody presses Keep on a file that is not on
// trial. It is not a failure — there is nothing to keep it from — but the caller
// is owed the difference.
var ErrNotLeased = errors.New("this file is not on the weekly playlist")

// Reading is what one look at one signal returned.
//
// Three values, never two. "The user did not star it" and "Schall could not ask"
// arrive from the player as different answers and must stay different here: the
// first may end in a deletion and the second never may. Collapsing them is the
// one bug this feature must not have, because it turns an hour of Navidrome
// being down into a week's music deleted.
type Reading int

const (
	// Unreadable — the question could not be asked or answered.
	Unreadable Reading = iota
	// Absent — asked and answered: the signal is not there.
	Absent
	// Present — asked and answered: the signal is there.
	Present
)

// SignalRead is one answer about one song, with the reason when there is none.
type SignalRead struct {
	Reading Reading
	Detail  string
}

// Song is one file to ask the player about: where Schall wrote it, and what it
// was asked for. Both are needed, because a player indexes tags and not paths:
// asking by file name alone finds a small share of a real library, and a song
// the player could not find is an answer that keeps rather than one that
// deletes, so a thin question would quietly stop the feature working.
type Song struct {
	Path   string
	Artist string
	Title  string
}

// StarReader reads the star the user presses in their player. The answers are
// keyed by the path they were asked about.
//
// The error is returned only when Schall could not ask at all — the player is
// not configured, or it did not answer. A song the player could not answer about
// is one Unreadable entry in the map.
type StarReader interface {
	Stars(ctx context.Context, songs []Song) (map[string]SignalRead, error)
}

// Recommender is the suggestion list this feature picks from. It has already
// applied every suppression rule, including the one this package writes.
type Recommender interface {
	List(ctx context.Context, source string) ([]recommendations.Candidate, error)
}

// Remover deletes one library file: the audio from disc and the row from the
// database, in one transaction.
type Remover interface {
	Remove(ctx context.Context, fileID uuid.UUID) (string, error)
}

// Publisher pushes the playlist to the player, so the songs of the week appear
// where the user listens.
type Publisher interface {
	SyncPlaylist(ctx context.Context, playlistID uuid.UUID) error
}

// Store is everything this package reads and writes.
type Store interface {
	WeeklyPlaylistSettings(ctx context.Context) (db.WeeklyPlaylistSettingsRow, error)
	SaveWeeklyPlaylistSettings(ctx context.Context, params db.SaveWeeklyPlaylistSettingsParams) (db.WeeklyPlaylistSettingsRow, error)

	EnsureWeeklyPlaylist(ctx context.Context, name string) (db.PlaylistRow, error)
	WeeklyPlaylist(ctx context.Context) (db.PlaylistRow, error)

	StartWeeklyPlaylistRun(ctx context.Context, params db.StartWeeklyPlaylistRunParams) (db.WeeklyPlaylistRunRow, error)
	FinishWeeklyPlaylistRun(ctx context.Context, params db.FinishWeeklyPlaylistRunParams) (db.WeeklyPlaylistRunRow, error)
	AbandonRunningWeeklyPlaylistRuns(ctx context.Context, detail string) (int64, error)
	WeeklyPlaylistRuns(ctx context.Context, limit int32) ([]db.WeeklyPlaylistRunRow, error)

	LiveLibraryFileLeases(ctx context.Context) ([]db.LibraryFileLeaseRow, error)
	LiveLibraryFileLeaseForFile(ctx context.Context, fileID uuid.UUID) (db.LibraryFileLeaseRow, error)
	LibraryFileLease(ctx context.Context, id uuid.UUID) (db.LibraryFileLeaseRow, error)
	GrantLibraryFileLease(ctx context.Context, params db.GrantLibraryFileLeaseParams) (db.LibraryFileLeaseRow, error)
	KeepLibraryFileLease(ctx context.Context, id uuid.UUID, keptBy string, runID uuid.NullUUID) (bool, error)
	AnnounceLibraryFileLeaseDeparture(ctx context.Context, id, runID uuid.UUID, removesAt time.Time) error
	ExtendLibraryFileLease(ctx context.Context, id uuid.UUID, expiresAt time.Time, reason string) error
	RemoveLibraryFileLease(ctx context.Context, id, runID uuid.UUID) error
	AbandonLibraryFileLease(ctx context.Context, id, runID uuid.UUID) error

	RecordLibraryFileKeepRead(ctx context.Context, params db.RecordLibraryFileKeepReadParams) error
	LibraryFileKeepReads(ctx context.Context, leaseID uuid.UUID) ([]db.LibraryFileKeepReadRow, error)
	PressedKeeps(ctx context.Context) (map[uuid.UUID]struct{}, error)

	RecordRecommendationUnkept(ctx context.Context, recordingID uuid.UUID, runID uuid.NullUUID, suppressedUntil time.Time) error
	SettleWeeklyTargetNotWanted(ctx context.Context, targetID uuid.UUID, summary string) error

	WeeklyPlaylistArrivals(ctx context.Context, playlistID uuid.UUID) ([]db.WeeklyPlaylistArrivalRow, error)
	WeeklyPlaylistOpenWants(ctx context.Context, playlistID uuid.UUID) (int, error)
	WeeklyPlaylistStaleWants(ctx context.Context, playlistID uuid.UUID, before time.Time) (int, error)
	WeeklyPlaylistEntryArtists(ctx context.Context, playlistID uuid.UUID) ([]string, error)
	WeeklyLibraryPoolCandidates(ctx context.Context, playlistID uuid.UUID, pickedAfter time.Time, limit int) ([]db.WeeklyLibraryCandidateRow, error)
	RecordWeeklyLibraryPick(ctx context.Context, recordingID, fileID uuid.UUID, runID uuid.NullUUID) error
	DropWeeklyLibraryEntries(ctx context.Context, playlistID uuid.UUID) (int64, error)
	DropSettledWeeklyPlaylistEntries(ctx context.Context, playlistID uuid.UUID) (int64, error)
	AppendWeeklyPlaylistEntry(ctx context.Context, params db.AppendWeeklyPlaylistEntryParams) (db.PlaylistEntryRow, error)
	MarkPlaylistEntryOwned(ctx context.Context, entryID uuid.UUID, fileID uuid.NullUUID) error
	CreatePlaylistEntryTarget(ctx context.Context, entryID uuid.UUID, params db.CreateAcquisitionTargetParams) (uuid.UUID, error)

	QueueWeeklyPlaylistRefresh(ctx context.Context, runAfter time.Time) error
	QueueAcquisitionSweep(ctx context.Context, runAfter time.Time) error
}

// Service is the weekly refresh and the decisions around it.
type Service struct {
	store  Store
	logger zerolog.Logger

	// stars reads the player. Optional: without it the only keep signal is the
	// Keep button in Schall, and a refresh in that state removes nothing.
	stars StarReader
	// suggestions is where the week's songs come from. Optional: without it a
	// refresh judges the leases it has and adds nothing.
	suggestions Recommender
	// remover deletes a file. Optional: without it a refresh announces and never
	// carries out, which is the same as report mode.
	remover Remover
	// publisher pushes the list to the player. Optional and best effort: a list
	// that did not reach the player is a partial run, not a failed one.
	publisher Publisher
	// notices tells connected interfaces the playlists changed.
	notices *events.Hub

	now func() time.Time
}

// NewService builds the weekly service. Every collaborator is optional and
// registered separately, because what a refresh may do depends on what is
// there: no remover means no removal can happen at all.
func NewService(store Store, logger zerolog.Logger) *Service {
	return &Service{store: store, logger: logger, now: time.Now}
}

func (service *Service) WithStars(stars StarReader) *Service {
	service.stars = stars
	return service
}

func (service *Service) WithRecommender(suggestions Recommender) *Service {
	service.suggestions = suggestions
	return service
}

func (service *Service) WithRemover(remover Remover) *Service {
	service.remover = remover
	return service
}

func (service *Service) WithPublisher(publisher Publisher) *Service {
	service.publisher = publisher
	return service
}

func (service *Service) WithEvents(hub *events.Hub) *Service {
	service.notices = hub
	return service
}

// WithClock replaces the clock. Tests use it; nothing else does.
func (service *Service) WithClock(now func() time.Time) *Service {
	service.now = now
	return service
}

func (service *Service) announce() {
	if service.notices != nil {
		service.notices.Publish(events.TopicPlaylists)
	}
}

// Settings returns how the weekly playlist is configured. A person who has never
// saved the settings gets the defaults, switched off: a feature that deletes
// music starts when somebody turns it on and not when a container restarts.
func (service *Service) Settings(ctx context.Context) (db.WeeklyPlaylistSettingsRow, error) {
	settings, err := service.store.WeeklyPlaylistSettings(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.WeeklyPlaylistSettingsRow{Enabled: false, SongsPerWeek: 20, Mode: ModeRemove}, nil
	}
	if err != nil {
		return db.WeeklyPlaylistSettingsRow{}, fmt.Errorf("read weekly playlist settings: %w", err)
	}
	return settings, nil
}

// SaveSettings writes the settings and schedules the next refresh when the
// feature has just been switched on, so that turning it on does something
// visible rather than waiting a week.
func (service *Service) SaveSettings(
	ctx context.Context, params db.SaveWeeklyPlaylistSettingsParams,
) (db.WeeklyPlaylistSettingsRow, error) {
	params.Mode = strings.TrimSpace(params.Mode)
	if params.Mode != ModeReport && params.Mode != ModeRemove {
		return db.WeeklyPlaylistSettingsRow{}, fmt.Errorf("weekly playlist mode %q is not report or remove", params.Mode)
	}
	saved, err := service.store.SaveWeeklyPlaylistSettings(ctx, params)
	if err != nil {
		return db.WeeklyPlaylistSettingsRow{}, fmt.Errorf("save weekly playlist settings: %w", err)
	}
	if saved.Enabled {
		if err := service.store.QueueWeeklyPlaylistRefresh(ctx, service.now()); err != nil {
			service.logger.Warn().Err(err).Msg("queue weekly playlist refresh after settings change")
		}
	}
	service.announce()
	return saved, nil
}

// Keep is the Keep button: this song stays.
//
// It is the one signal that can never be unreadable, because nothing outside
// Schall has to answer for it, and it is the answer for somebody who never stars
// anything. It works on a song already announced for removal, which is the whole
// point of announcing a week early.
func (service *Service) Keep(ctx context.Context, fileID uuid.UUID) (db.LibraryFileLeaseRow, error) {
	lease, err := service.store.LiveLibraryFileLeaseForFile(ctx, fileID)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.LibraryFileLeaseRow{}, ErrNotLeased
	}
	if err != nil {
		return db.LibraryFileLeaseRow{}, fmt.Errorf("read lease for file %s: %w", fileID, err)
	}

	// The read is written before the lease is settled. If the process stops
	// between the two, the next refresh finds a pressed Keep and keeps the song;
	// the other order would lose the press and delete it.
	err = service.store.RecordLibraryFileKeepRead(ctx, db.RecordLibraryFileKeepReadParams{
		LeaseID: lease.ID, Signal: signalKeep, Outcome: outcomeKept,
	})
	if err != nil {
		return db.LibraryFileLeaseRow{}, err
	}
	if _, err := service.store.KeepLibraryFileLease(ctx, lease.ID, signalKeep, uuid.NullUUID{}); err != nil {
		return db.LibraryFileLeaseRow{}, err
	}
	service.announce()

	kept, err := service.store.LibraryFileLease(ctx, lease.ID)
	if err != nil {
		return db.LibraryFileLeaseRow{}, err
	}
	return kept, nil
}

// Overview is everything the weekly playlist screen shows: how it is set up,
// what is on trial now, and what the recent refreshes did.
type Overview struct {
	Settings db.WeeklyPlaylistSettingsRow
	// Playlist is absent until the first refresh has made one.
	Playlist *db.PlaylistRow
	Leases   []db.LibraryFileLeaseRow
	Runs     []db.WeeklyPlaylistRunRow
}

// Overview reads the screen in one call.
func (service *Service) Overview(ctx context.Context) (Overview, error) {
	settings, err := service.Settings(ctx)
	if err != nil {
		return Overview{}, err
	}

	overview := Overview{Settings: settings}
	playlist, err := service.store.WeeklyPlaylist(ctx)
	switch {
	case err == nil:
		overview.Playlist = &playlist
	case errors.Is(err, pgx.ErrNoRows):
	default:
		return Overview{}, fmt.Errorf("read the weekly playlist: %w", err)
	}

	if overview.Leases, err = service.store.LiveLibraryFileLeases(ctx); err != nil {
		return Overview{}, fmt.Errorf("read the files on trial: %w", err)
	}
	if overview.Runs, err = service.store.WeeklyPlaylistRuns(ctx, recentRuns); err != nil {
		return Overview{}, fmt.Errorf("read the recent refreshes: %w", err)
	}
	return overview, nil
}

// LeaseEvidence returns what was read about one song, so a person can see why it
// is leaving before it goes.
func (service *Service) LeaseEvidence(ctx context.Context, leaseID uuid.UUID) ([]db.LibraryFileKeepReadRow, error) {
	return service.store.LibraryFileKeepReads(ctx, leaseID)
}

// recentRuns is how many refreshes the screen shows. Enough to see a pattern,
// not so many that the page becomes a log.
const recentRuns = 8

// counts is what one run did, in the order the run does it.
type counts struct {
	chosen   int32
	wanted   int32
	arrived  int32
	kept     int32
	extended int32
	leaving  int32
	removed  int32
	library  int32
	replaced int32
}

// RunResult is what one refresh did, for the caller that asked for it.
type RunResult struct {
	Run     db.WeeklyPlaylistRunRow
	Skipped bool
	Reason  string
}

// Refresh is one weekly pass: judge what is on trial, take in what arrived,
// drop what is settled, choose the next songs, and push the list to the player.
//
// It returns without a run when the feature is switched off or when a refresh is
// already going. Neither is a failure and neither is worth a row.
func (service *Service) Refresh(ctx context.Context) (RunResult, error) {
	settings, err := service.Settings(ctx)
	if err != nil {
		return RunResult{}, err
	}
	if !settings.Enabled {
		return RunResult{Skipped: true, Reason: "the weekly playlist is switched off"}, nil
	}

	playlist, err := service.store.EnsureWeeklyPlaylist(ctx, PlaylistName)
	if err != nil {
		return RunResult{}, fmt.Errorf("open the weekly playlist: %w", err)
	}

	run, err := service.store.StartWeeklyPlaylistRun(ctx, db.StartWeeklyPlaylistRunParams{
		PlaylistID:   uuid.NullUUID{UUID: playlist.ID, Valid: true},
		Mode:         settings.Mode,
		LibraryShare: settings.LibraryShare,
		OnePerArtist: settings.OnePerArtist,
	})
	if errors.Is(err, db.ErrWeeklyRunInFlight) {
		return RunResult{Skipped: true, Reason: "a refresh is already running"}, nil
	}
	if err != nil {
		return RunResult{}, fmt.Errorf("start weekly playlist run: %w", err)
	}

	tally := &counts{}
	troubles := service.pass(ctx, run, settings, playlist, tally)

	status, detail := "complete", ""
	if len(troubles) > 0 {
		status, detail = "partial", strings.Join(troubles, "; ")
	}
	finished, err := service.store.FinishWeeklyPlaylistRun(ctx, db.FinishWeeklyPlaylistRunParams{
		ID: run.ID, Status: status, Detail: detail,
		ChosenCount: tally.chosen, WantedCount: tally.wanted, ArrivedCount: tally.arrived,
		KeptCount: tally.kept, ExtendedCount: tally.extended,
		LeavingCount: tally.leaving, RemovedCount: tally.removed,
		LibraryCount: tally.library, ReplacedCount: tally.replaced,
	})
	if err != nil {
		return RunResult{}, fmt.Errorf("finish weekly playlist run %s: %w", run.ID, err)
	}

	service.announce()
	service.logger.Info().
		Str("run_id", run.ID.String()).
		Str("mode", settings.Mode).
		Str("status", status).
		Int32("chosen", tally.chosen).
		Int32("library", tally.library).
		Int32("replaced", tally.replaced).
		Int32("arrived", tally.arrived).
		Int32("kept", tally.kept).
		Int32("leaving", tally.leaving).
		Int32("removed", tally.removed).
		Msg("weekly playlist refresh")
	return RunResult{Run: finished}, nil
}

// pass does the work of one run and collects what went wrong along the way.
//
// Trouble is collected rather than returned. A week when Soulseek was slow, or
// when the player did not answer, is an ordinary week: the run says what it
// could not do and ends 'partial'. Only a failure that makes the account itself
// untrustworthy stops the pass.
func (service *Service) pass(
	ctx context.Context,
	run db.WeeklyPlaylistRunRow,
	settings db.WeeklyPlaylistSettingsRow,
	playlist db.PlaylistRow,
	tally *counts,
) []string {
	var troubles []string

	if err := service.judge(ctx, run, settings, tally); err != nil {
		troubles = append(troubles, err.Error())
	}
	if err := service.takeIn(ctx, run, playlist, tally); err != nil {
		troubles = append(troubles, err.Error())
	}
	if _, err := service.store.DropSettledWeeklyPlaylistEntries(ctx, playlist.ID); err != nil {
		troubles = append(troubles, err.Error())
	}
	// Last week's library songs come off before this week's are chosen. They
	// were never leased, so this is a playlist row and nothing on disc.
	if _, err := service.store.DropWeeklyLibraryEntries(ctx, playlist.ID); err != nil {
		troubles = append(troubles, err.Error())
	}
	if err := service.choose(ctx, run, settings, playlist, tally); err != nil {
		troubles = append(troubles, err.Error())
	}
	if err := service.publish(ctx, playlist); err != nil {
		troubles = append(troubles, err.Error())
	}
	return troubles
}

// judge reads the keep signals for every lease that is due and acts on what they
// said. It is the only place in Schall that decides music may be deleted.
func (service *Service) judge(
	ctx context.Context, run db.WeeklyPlaylistRunRow, settings db.WeeklyPlaylistSettingsRow, tally *counts,
) error {
	leases, err := service.store.LiveLibraryFileLeases(ctx)
	if err != nil {
		return fmt.Errorf("read the files on trial: %w", err)
	}
	now := service.now()
	due := make([]db.LibraryFileLeaseRow, 0, len(leases))
	for _, lease := range leases {
		if isDue(lease, now) {
			due = append(due, lease)
		}
	}
	if len(due) == 0 {
		return nil
	}

	pressed, err := service.store.PressedKeeps(ctx)
	if err != nil {
		return fmt.Errorf("read the songs somebody kept: %w", err)
	}
	stars := service.readStars(ctx, due)

	// One song that could not be judged does not stop the others being judged.
	// Stopping at the first would be the expensive kind of safe: the songs after
	// it include the ones somebody starred, and leaving those unsettled means a
	// person who did press the star sees nothing happen. A song this refresh
	// could not judge simply keeps its lease, which is the outcome that keeps it.
	var refused []string
	for _, lease := range due {
		if err := service.settle(ctx, run, settings, lease, pressed, stars, tally); err != nil {
			service.logger.Error().Err(err).
				Str("lease_id", lease.ID.String()).
				Str("library_path", lease.LibraryPath).
				Msg("settle weekly lease")
			refused = append(refused, fmt.Sprintf("%s: %v", lease.LibraryPath, err))
		}
	}
	if len(refused) > 0 {
		return fmt.Errorf("could not judge %d of %d songs: %s",
			len(refused), len(due), strings.Join(refused, "; "))
	}
	return nil
}

// isDue is whether this refresh has to decide anything about a lease. A song is
// judged when its week is up, and an announced departure when its date arrives.
func isDue(lease db.LibraryFileLeaseRow, now time.Time) bool {
	switch lease.State {
	case "held":
		return lease.ExpiresAt.Valid && !lease.ExpiresAt.Time.After(now)
	case "leaving":
		return lease.RemovesAt.Valid && !lease.RemovesAt.Time.After(now)
	default:
		return false
	}
}

// readStars asks the player about every song that is due at once.
//
// A player nobody configured, or one that did not answer, gives every song the
// same Unreadable answer with the same reason. That is the "silence keeps" rule
// arriving as data rather than as a special case further down.
func (service *Service) readStars(ctx context.Context, due []db.LibraryFileLeaseRow) map[string]SignalRead {
	if service.stars == nil {
		return unreadableAll(due, "no player is connected, so nothing can say you kept a song")
	}
	songs := make([]Song, 0, len(due))
	for _, lease := range due {
		songs = append(songs, Song{
			Path: lease.LibraryPath, Artist: lease.EntryArtist, Title: lease.EntryTitle,
		})
	}
	answers, err := service.stars.Stars(ctx, songs)
	if err != nil {
		service.logger.Warn().Err(err).Int("songs", len(songs)).Msg("read stars from the player")
		return unreadableAll(due, err.Error())
	}
	return answers
}

func unreadableAll(due []db.LibraryFileLeaseRow, detail string) map[string]SignalRead {
	answers := make(map[string]SignalRead, len(due))
	for _, lease := range due {
		answers[lease.LibraryPath] = SignalRead{Reading: Unreadable, Detail: detail}
	}
	return answers
}

// settle decides one lease.
func (service *Service) settle(
	ctx context.Context,
	run db.WeeklyPlaylistRunRow,
	settings db.WeeklyPlaylistSettingsRow,
	lease db.LibraryFileLeaseRow,
	pressed map[uuid.UUID]struct{},
	stars map[string]SignalRead,
	tally *counts,
) error {
	// The file left by another hand: the user deleted it from the duplicates
	// screen, or a scan found it gone. Nothing to delete, nothing decided, and
	// no want settled — they may want this recording again tomorrow.
	if !lease.LibraryFileID.Valid || lease.FileMissingAt.Valid {
		return service.store.AbandonLibraryFileLease(ctx, lease.ID, run.ID)
	}

	if _, ok := pressed[lease.ID]; ok {
		if _, err := service.store.KeepLibraryFileLease(
			ctx, lease.ID, signalKeep, uuid.NullUUID{UUID: run.ID, Valid: true},
		); err != nil {
			return err
		}
		tally.kept++
		return nil
	}

	star, answered := stars[lease.LibraryPath]
	if !answered {
		star = SignalRead{Reading: Unreadable, Detail: "the player did not answer about this song"}
	}
	if err := service.store.RecordLibraryFileKeepRead(ctx, db.RecordLibraryFileKeepReadParams{
		LeaseID: lease.ID,
		RunID:   uuid.NullUUID{UUID: run.ID, Valid: true},
		Signal:  signalStar,
		Outcome: outcomeFor(star.Reading),
		Detail:  star.Detail,
	}); err != nil {
		return err
	}

	switch star.Reading {
	case Present:
		if _, err := service.store.KeepLibraryFileLease(
			ctx, lease.ID, signalStar, uuid.NullUUID{UUID: run.ID, Valid: true},
		); err != nil {
			return err
		}
		tally.kept++
		return nil

	case Unreadable:
		// Silence keeps. The song gets another week, an announced departure is
		// withdrawn, and the reason is carried so the screen can say which week
		// could not be read.
		if err := service.store.ExtendLibraryFileLease(
			ctx, lease.ID, service.now().Add(trialWeek), star.Detail,
		); err != nil {
			return err
		}
		tally.extended++
		return nil
	}

	// Asked, answered, and nobody kept it.
	return service.depart(ctx, run, settings, lease, tally)
}

func outcomeFor(reading Reading) string {
	switch reading {
	case Present:
		return outcomeKept
	case Absent:
		return outcomeAbsent
	default:
		return outcomeUnreadable
	}
}

// depart is what happens to a song nobody kept: it is announced this week and
// removed the next.
func (service *Service) depart(
	ctx context.Context,
	run db.WeeklyPlaylistRunRow,
	settings db.WeeklyPlaylistSettingsRow,
	lease db.LibraryFileLeaseRow,
	tally *counts,
) error {
	if settings.Mode == ModeReport || service.remover == nil {
		reason := "nobody kept this song, and removals are switched off"
		if service.remover == nil {
			reason = "nobody kept this song, and nothing here can remove a file"
		}
		if err := service.store.ExtendLibraryFileLease(
			ctx, lease.ID, service.now().Add(trialWeek), reason,
		); err != nil {
			return err
		}
		tally.leaving++
		return nil
	}

	if lease.State == "held" {
		if err := service.store.AnnounceLibraryFileLeaseDeparture(
			ctx, lease.ID, run.ID, service.now().Add(graceWeek),
		); err != nil {
			return err
		}
		tally.leaving++
		return nil
	}

	// The departure was announced a week ago, the signals were read again just
	// now, and they still say nobody kept it.
	return service.remove(ctx, run, lease, tally)
}

// remove deletes the file and settles everything that pointed at it.
//
// The order matters. The audio goes first, because a lease that says 'removed'
// beside a file still on disc is a lie the next run would act on. The want is
// settled next so the acquisition sweep does not fetch the same song again, and
// the recording is suppressed last so the recommender does not offer it back
// next week. A failure in either of the last two leaves the file deleted and
// says so: the run ends partial and the row is written next week.
func (service *Service) remove(
	ctx context.Context, run db.WeeklyPlaylistRunRow, lease db.LibraryFileLeaseRow, tally *counts,
) error {
	fileID := lease.LibraryFileID.UUID
	current, err := service.store.LibraryFileLease(ctx, lease.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		service.logger.Warn().
			Str("lease_id", lease.ID.String()).
			Str("file_id", fileID.String()).
			Msg("abandon weekly lease removal")
		return nil
	}
	if err != nil {
		return fmt.Errorf("revalidate lease %s before removal: %w", lease.ID, err)
	}
	if current.State != "leaving" ||
		!current.LibraryFileID.Valid || current.LibraryFileID.UUID != fileID {
		service.logger.Warn().
			Str("lease_id", lease.ID.String()).
			Str("file_id", fileID.String()).
			Msg("abandon weekly lease removal")
		return nil
	}

	_, err = service.remover.Remove(ctx, fileID)
	switch {
	case err == nil:
	case errors.Is(err, library.ErrFileGone), errors.Is(err, pgx.ErrNoRows):
		// The library stopped holding the file between reading the lease and
		// deleting it. Nothing was deleted here, so the lease is closed as gone
		// rather than as removed, and the want is left alone.
		return service.store.AbandonLibraryFileLease(ctx, lease.ID, run.ID)
	default:
		return fmt.Errorf("delete %s: %w", lease.LibraryPath, err)
	}

	if err := service.store.RemoveLibraryFileLease(ctx, lease.ID, run.ID); err != nil {
		return err
	}
	tally.removed++

	summary := fmt.Sprintf("obtained for the weekly playlist and removed on %s, unkept",
		service.now().UTC().Format("2 January 2006"))
	if err := service.store.SettleWeeklyTargetNotWanted(ctx, lease.AcquisitionTargetID, summary); err != nil &&
		!errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	return service.store.RecordRecommendationUnkept(
		ctx, lease.MusicBrainzRecordingID,
		uuid.NullUUID{UUID: run.ID, Valid: true},
		service.now().Add(unkeptWindow),
	)
}

// takeIn puts the songs that arrived since the last refresh on trial.
//
// The lease is granted here, by a run, and not at the moment the download was
// imported. The import path knows nothing about this feature and keeps knowing
// nothing: a file becomes deletable because a weekly run put it on trial, never
// because of the way it arrived.
func (service *Service) takeIn(
	ctx context.Context, run db.WeeklyPlaylistRunRow, playlist db.PlaylistRow, tally *counts,
) error {
	arrivals, err := service.store.WeeklyPlaylistArrivals(ctx, playlist.ID)
	if err != nil {
		return fmt.Errorf("read what arrived for the weekly playlist: %w", err)
	}
	expires := service.now().Add(trialWeek)
	for _, arrival := range arrivals {
		_, err := service.store.GrantLibraryFileLease(ctx, db.GrantLibraryFileLeaseParams{
			LibraryFileID:          arrival.LibraryFileID,
			LibraryPath:            arrival.LibraryPath,
			MusicBrainzRecordingID: arrival.MusicBrainzRecordingID,
			RunID:                  run.ID,
			AcquisitionTargetID:    arrival.TargetID,
			ExpiresAt:              expires,
		})
		if err != nil {
			return fmt.Errorf("put %s on trial: %w", arrival.LibraryPath, err)
		}
		if err := service.store.MarkPlaylistEntryOwned(
			ctx, arrival.EntryID, uuid.NullUUID{UUID: arrival.LibraryFileID, Valid: true},
		); err != nil {
			return err
		}
		tally.arrived++
	}
	return nil
}

// choose picks the week's songs.
//
// The number is counted against the wants still being looked for, not against
// the songs already here. A slow week on Soulseek must not become a queue of two
// hundred searches, and Soulseek bans a client that searches quickly. A want
// that has had a whole trial week no longer counts, so the week ships full
// rather than short; the ceiling is what keeps that from becoming the queue of
// two hundred.
//
// The week is split between two pools. The discover share asks for music the
// library does not hold. The library share puts music the library already holds
// on the list, with no want and no lease, which is why nothing in this function
// can lead to a library song being deleted.
func (service *Service) choose(
	ctx context.Context,
	run db.WeeklyPlaylistRunRow,
	settings db.WeeklyPlaylistSettingsRow,
	playlist db.PlaylistRow,
	tally *counts,
) error {
	room, replaced, err := service.room(ctx, settings, playlist)
	if err != nil {
		return err
	}
	tally.replaced = int32(replaced)
	if room <= 0 {
		return nil
	}

	taken, err := service.artistsOnTheList(ctx, settings, playlist)
	if err != nil {
		return err
	}

	// The library share is rounded down, so a share nobody set takes nothing and
	// a share of 100 still leaves the discover pool whatever the library could
	// not fill.
	fromLibrary := room * int(settings.LibraryShare) / 100
	if err := service.chooseFromLibrary(ctx, run, playlist, fromLibrary, taken, tally); err != nil {
		return err
	}

	if err := service.chooseFromSuggestions(ctx, playlist, room-int(tally.library), taken, tally); err != nil {
		return err
	}

	if tally.wanted > 0 {
		if err := service.store.QueueAcquisitionSweep(ctx, service.now()); err != nil {
			service.logger.Warn().Err(err).Msg("wake the acquisition sweep for the weekly playlist")
		}
	}
	return nil
}

// room is how many songs this refresh may add, and how many slots were freed by
// wants that had their week and are still being looked for.
func (service *Service) room(
	ctx context.Context, settings db.WeeklyPlaylistSettingsRow, playlist db.PlaylistRow,
) (int, int, error) {
	open, err := service.store.WeeklyPlaylistOpenWants(ctx, playlist.ID)
	if err != nil {
		return 0, 0, err
	}
	stale, err := service.store.WeeklyPlaylistStaleWants(
		ctx, playlist.ID, service.now().Add(-wantPatience),
	)
	if err != nil {
		return 0, 0, err
	}
	if open >= openWantCeiling*int(settings.SongsPerWeek) {
		service.logger.Warn().
			Int("open_wants", open).
			Int32("songs_per_week", settings.SongsPerWeek).
			Msg("the weekly playlist asks for nothing this week: too much is still being looked for")
		return 0, stale, nil
	}
	return int(settings.SongsPerWeek) - (open - stale), stale, nil
}

// artistsOnTheList is the per-artist rule's memory, and nil when the rule is
// off, so the caller does not have to ask twice.
//
// The songs already on the list are in it: an arrival on trial, a want still
// being looked for. Without them a week would stack an artist across two
// refreshes while each one looked correct on its own.
func (service *Service) artistsOnTheList(
	ctx context.Context, settings db.WeeklyPlaylistSettingsRow, playlist db.PlaylistRow,
) (map[string]struct{}, error) {
	if !settings.OnePerArtist {
		return nil, nil
	}
	taken := make(map[string]struct{})
	artists, err := service.store.WeeklyPlaylistEntryArtists(ctx, playlist.ID)
	if err != nil {
		return nil, err
	}
	for _, artist := range artists {
		taken[artistKey(artist)] = struct{}{}
	}
	return taken, nil
}

// artistKey is what the per-artist rule compares. The playlist stores artist
// text and not MusicBrainz artist IDs, so this is a name match, and it is only
// ever used to spread a list rather than to decide what a file is.
func artistKey(artist string) string {
	return strings.ToLower(strings.TrimSpace(artist))
}

// take reports whether the rule allows this artist, and records it when it does.
// A nil map means the rule is off and everything is allowed.
func take(taken map[string]struct{}, artist string) bool {
	if taken == nil {
		return true
	}
	key := artistKey(artist)
	if key == "" {
		return true
	}
	if _, already := taken[key]; already {
		return false
	}
	taken[key] = struct{}{}
	return true
}

// chooseFromLibrary puts songs the collection already holds on the list.
//
// No want and no lease. The lease is the only licence to delete
// (ADR 0021), so a song that arrives this way cannot be removed by
// any later refresh whatever the mode says, and no code has to know that rule.
func (service *Service) chooseFromLibrary(
	ctx context.Context,
	run db.WeeklyPlaylistRunRow,
	playlist db.PlaylistRow,
	want int,
	taken map[string]struct{},
	tally *counts,
) error {
	if want <= 0 {
		return nil
	}
	// Ask for more than the share needs: the per-artist rule turns some of them
	// down, and one query a week is cheaper than a second round trip.
	candidates, err := service.store.WeeklyLibraryPoolCandidates(
		ctx, playlist.ID, service.now().Add(-libraryWindow), want*4,
	)
	if err != nil {
		return err
	}

	for _, candidate := range candidates {
		if int(tally.library) >= want {
			break
		}
		if !take(taken, candidate.Artist) {
			continue
		}
		entry, err := service.store.AppendWeeklyPlaylistEntry(ctx, db.AppendWeeklyPlaylistEntryParams{
			PlaylistID:             playlist.ID,
			MusicBrainzRecordingID: candidate.MusicBrainzRecordingID,
			EntryArtist:            candidate.Artist,
			EntryTitle:             candidate.Title,
			EntryAlbum:             candidate.Album,
		})
		if err != nil {
			return fmt.Errorf("add %s to the weekly playlist: %w", candidate.Title, err)
		}
		if err := service.store.MarkPlaylistEntryOwned(
			ctx, entry.ID, uuid.NullUUID{UUID: candidate.LibraryFileID, Valid: true},
		); err != nil {
			return fmt.Errorf("mark %s owned on the weekly playlist: %w", candidate.Title, err)
		}
		if err := service.store.RecordWeeklyLibraryPick(
			ctx, candidate.MusicBrainzRecordingID, candidate.LibraryFileID,
			uuid.NullUUID{UUID: run.ID, Valid: true},
		); err != nil {
			return err
		}
		tally.chosen++
		tally.library++
	}
	return nil
}

// chooseFromSuggestions asks for music the library does not hold. The candidates
// have already been through every suppression rule, including the one this
// package writes.
func (service *Service) chooseFromSuggestions(
	ctx context.Context,
	playlist db.PlaylistRow,
	want int,
	taken map[string]struct{},
	tally *counts,
) error {
	if service.suggestions == nil || want <= 0 {
		return nil
	}
	candidates, err := service.suggestions.List(ctx, source)
	if err != nil {
		return fmt.Errorf("read the suggestions: %w", err)
	}

	asked := 0
	for _, candidate := range candidates {
		if asked >= want {
			break
		}
		if !take(taken, candidate.ArtistName) {
			continue
		}
		entry, err := service.store.AppendWeeklyPlaylistEntry(ctx, db.AppendWeeklyPlaylistEntryParams{
			PlaylistID:             playlist.ID,
			MusicBrainzRecordingID: candidate.MusicBrainzRecordingID,
			EntryArtist:            candidate.ArtistName,
			EntryTitle:             candidate.RecordingTitle,
			EntryAlbum:             candidate.ReleaseTitle,
		})
		if err != nil {
			return fmt.Errorf("add %s to the weekly playlist: %w", candidate.RecordingTitle, err)
		}
		tally.chosen++

		_, err = service.store.CreatePlaylistEntryTarget(ctx, entry.ID, db.CreateAcquisitionTargetParams{
			Origin:                    "recommendation",
			EntryArtist:               candidate.ArtistName,
			EntryTitle:                candidate.RecordingTitle,
			EntryAlbum:                candidate.ReleaseTitle,
			MusicBrainzRecordingID:    uuid.NullUUID{UUID: candidate.MusicBrainzRecordingID, Valid: true},
			MusicBrainzReleaseGroupID: uuid.NullUUID{UUID: candidate.MusicBrainzReleaseGroupID, Valid: true},
			ResolutionMethod:          pgtype.Text{String: "manual", Valid: true},
			Summary:                   "wanted for the weekly playlist",
		})
		if err != nil {
			return fmt.Errorf("ask for %s: %w", candidate.RecordingTitle, err)
		}
		tally.wanted++
		asked++
	}
	return nil
}

// publish pushes the list to the player. It is best effort: a list that did not
// reach the player is a partial run, never a reason to stop.
func (service *Service) publish(ctx context.Context, playlist db.PlaylistRow) error {
	if service.publisher == nil {
		return nil
	}
	if err := service.publisher.SyncPlaylist(ctx, playlist.ID); err != nil {
		return fmt.Errorf("send the weekly playlist to the player: %w", err)
	}
	return nil
}

// CloseAbandonedRuns closes a run left open by a restart, so that the next
// refresh is not blocked forever by a row nobody will ever finish.
func (service *Service) CloseAbandonedRuns(ctx context.Context) error {
	closed, err := service.store.AbandonRunningWeeklyPlaylistRuns(
		ctx, "Schall stopped in the middle of this refresh",
	)
	if err != nil {
		return err
	}
	if closed > 0 {
		service.logger.Warn().Int64("runs", closed).Msg("close weekly playlist runs left open by a restart")
	}
	return nil
}
