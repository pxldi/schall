// Package acquisition holds what somebody wants: one target per wanted
// recording, and the lifecycle that carries it from being asked for to being a
// file in the library.
//
// A target is the want, not the answer. The entry evidence it was created from
// is kept verbatim and the MusicBrainz recording is written into it, so a
// resolution that turns out to be wrong can be replaced without losing the fact
// that the music was asked for. Nothing here decides what a file is: that is
// matching's job, and a target only ever reads decisions matching has already
// made.
package acquisition

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pxldi/schall/internal/anchor"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/events"
	"github.com/pxldi/schall/internal/identity"
	"github.com/rs/zerolog"
)

// ErrEntryNotIdentifiable reports an entry with nothing to work from. What a
// target wants is never guessed at from the rest of the row.
var ErrEntryNotIdentifiable = errors.New("a target needs a title or an ISRC to be resolved from")

// ErrUnknownOrigin reports a target nobody can account for. Provenance is not
// decoration: a want nobody remembers asking for has to say where it came from.
var ErrUnknownOrigin = errors.New("that is not a way a target can come to exist")

// sweepBatch bounds how many targets one pass attempts. Every attempt will
// eventually be a bounded provider search, and source-search jobs are claimed
// one at a time in creation order, so a large sweep would sit in front of every
// refresh and interactive search behind it. What is left over is picked up by
// the next pass, immediately: a full batch leaves something due now.
//
// Forty rather than twenty so that a pass is five rounds of huntWorkers rather
// than two and a half, and the workers are not idle at the end of one waiting
// for the next pass to be queued.
const sweepBatch = 40

// huntWorkers is how many of one pass's wants are attempted at once. A want the
// library already holds costs a query; one that has to be searched for costs a
// bounded provider search that is almost entirely waiting for peers to answer, so
// several at a time is close to free in wall-clock terms.
//
// It replaces a cap on how many wants per pass went looking at all. That cap
// existed to stop searching from starving the transfer poller and the importer,
// which acquisition's own lane already settles; what is left to protect is slskd,
// and that bound lives in its client where the 429 is understood. This number is
// only how many attempts are in flight, and it is deliberately no larger than what
// the source gate will admit — queueing here rather than there would be the same
// wait with less to show for it.
//
// It follows defaultSearchConcurrency in internal/slskd, which went to eight on
// the measurement written down there. It also doubles the worst case peerFileBudget
// describes, to forty files at one peer, which needs all eight wants to converge
// on the same folder in the same pass. A peer that refuses defers the want fifteen
// minutes and spends no attempt, so that case costs a delay and never a copy.
const huntWorkers = 8

// attemptDelays is how long a target waits before each further attempt. This is
// the retry cadence PRODUCT.md states, and it lives here as
// a handful of numbers in one place rather than as a column default or a
// constraint — retuning it is a one-line change and never a migration.
//
// It starts in minutes because an empty answer is usually about the network
// at that moment, not about the music: seven wants whose ladders had come back
// empty five times each were found by hand minutes later, by the ladder's own
// first phrase, with 50–240 files on offer (issue #121). It still climbs,
// because a copy truly absent this morning is often absent tonight, and it
// stops at a day because a want does not expire — a target checked daily
// forever costs a few searches and eventually succeeds.
var attemptDelays = []time.Duration{
	15 * time.Minute,
	time.Hour,
	3 * time.Hour,
	6 * time.Hour,
	12 * time.Hour,
	24 * time.Hour,
}

// unfoundDelays is the same ladder for the other kind of empty answer: the entry
// MusicBrainz holds no recording for at all.
//
// The two are not the same wait. A want with a recording and no copy is asking a
// question that changes hour by hour, because who is sharing what on a peer
// network changes hour by hour. A want with no recording is asking whether
// MusicBrainz has gained one, and a catalogue gains a recording when a person
// sits down and writes it in. That happens over weeks, and never at all for most
// entries: one imported playlist of 314 entries left 253 of them here, and the
// daily ladder meant 253 lookups a day, every day, for recordings that in all
// likelihood nobody will ever add.
//
// So it starts where the other one stops and climbs to a month. The want does not
// expire and is never abandoned — it is asked about roughly as often as the
// answer could plausibly have changed, and no more.
var unfoundDelays = []time.Duration{
	24 * time.Hour,
	72 * time.Hour,
	7 * 24 * time.Hour,
	30 * 24 * time.Hour,
}

// Store is the persistence acquisition needs, and nothing else.
type Store interface {
	CreateAcquisitionTarget(context.Context, db.CreateAcquisitionTargetParams) (uuid.UUID, bool, error)
	// ScheduleAnchor puts a want in line for the distributor's excerpt of the
	// recording it names, which a copy a stranger sends can then be compared
	// against (ADR 0024). It is a note that something is owed, not an
	// answer about the want, and it does nothing to a want with no ISRC.
	ScheduleAnchor(ctx context.Context, id uuid.UUID, at time.Time) error
	AcquisitionTarget(context.Context, uuid.UUID) (db.AcquisitionTargetRow, error)
	ListAcquisitionTargets(context.Context, db.ListAcquisitionTargetsParams) (db.AcquisitionTargetPage, error)
	StopPursuingAcquisitionTarget(context.Context, uuid.UUID, string) (db.AcquisitionTargetRow, error)
	DueUnresolvedTargets(context.Context, time.Time, int32) ([]db.AcquisitionTargetRow, error)
	ResolveAcquisitionTarget(context.Context, db.ResolveAcquisitionTargetParams) (db.AcquisitionTargetRow, error)
	ChooseAcquisitionTargetResolution(
		context.Context, db.ResolveAcquisitionTargetParams,
	) (db.AcquisitionTargetRow, error)
	SendAcquisitionTargetToReview(context.Context, db.ReviewAcquisitionTargetParams) (db.AcquisitionTargetRow, error)
	RequeueUnresolvedTarget(ctx context.Context, id uuid.UUID, outcome, summary, detail, lastError string, nextAttemptAt time.Time) error
	DeferUnresolvedTarget(
		ctx context.Context, id uuid.UUID, summary, reason string, nextAttemptAt time.Time,
	) error
	AcquisitionTargetCandidates(context.Context, uuid.UUID) ([]db.AcquisitionTargetCandidateRow, error)
	RejectedTargetResolutions(context.Context, uuid.UUID) ([]uuid.UUID, error)
	RejectAcquisitionTargetResolution(
		ctx context.Context, id uuid.UUID, summary, detail string, nextAttemptAt time.Time,
	) (db.AcquisitionTargetRow, error)
	AcquisitionReviewQueue(ctx context.Context, limit, offset int32) (db.AcquisitionReviewPage, error)
	AcquiredCopy(context.Context, uuid.UUID) (db.AcquisitionTargetFileRow, error)
	AcceptHeldCopy(ctx context.Context, copyID uuid.UUID, summary string) (uuid.UUID, error)
	RefuseHeldCopies(
		ctx context.Context, targetID uuid.UUID, summary, targetSummary string,
		nextAttemptAt time.Time,
	) error
	// Taking one of those four answers back, for as long as the thing it asked
	// for has not happened (ADR 0019).
	AnswerIsReversible(ctx context.Context, answer db.Answer, id uuid.UUID) (bool, error)
	TakeBackAcceptedCopy(ctx context.Context, copyID uuid.UUID, heldSummary, targetSummary string) error
	TakeBackRefusedCopies(ctx context.Context, targetID uuid.UUID, heldSummary, targetSummary string) error
	TakeBackNotWanted(
		ctx context.Context, id uuid.UUID, unresolvedSummary, wantedSummary string,
		nextAttemptAt time.Time,
	) (db.AcquisitionTargetRow, error)
	TakeBackWrongRecording(
		ctx context.Context, id uuid.UUID, summary string, nextAttemptAt time.Time,
	) (db.AcquisitionTargetRow, error)
	PursueAcquisitionTargetAgain(ctx context.Context, id uuid.UUID, unresolvedSummary, wantedSummary string, nextAttemptAt time.Time) (db.AcquisitionTargetRow, error)
	TakeBestAvailableAcquisitionTarget(ctx context.Context, id uuid.UUID) (db.AcquisitionTargetRow, error)
	KeepAcquisitionTargetFloor(ctx context.Context, id uuid.UUID) (db.AcquisitionTargetRow, error)
	RequeueBelowFloor(ctx context.Context, id uuid.UUID, summary, parkedSummary, detail string,
		nextAttemptAt, parkedUntil time.Time) error
	RequeueParkedAcquisitionTarget(ctx context.Context, id uuid.UUID, outcome, summary, detail, lastError string,
		nextAttemptAt time.Time) error
	DueAcquisitionTargets(context.Context, time.Time, int32) ([]db.AcquisitionTargetRow, error)
	// DueSearchTargets and NextSearchTargetDue are the unresolved wants searched
	// on their anchor (ADR 0037).
	DueSearchTargets(context.Context, time.Time, int32) ([]db.AcquisitionTargetRow, error)
	NextAcquisitionTargetDue(context.Context) (pgtype.Timestamptz, error)
	NextUnresolvedTargetDue(context.Context) (pgtype.Timestamptz, error)
	NextSearchTargetDue(context.Context) (pgtype.Timestamptz, error)
	// DueSourceRechecks, NextSourceRecheckDue and RescheduleSourceRecheck are
	// the files with an automatic source identity asked about again on the
	// unfound ladder (ADR 0037 §6).
	DueSourceRechecks(context.Context, time.Time, int32) ([]db.SourceRecheckRow, error)
	NextSourceRecheckDue(context.Context) (pgtype.Timestamptz, error)
	RescheduleSourceRecheck(
		ctx context.Context, fileID uuid.UUID, next time.Time, counted bool, contradiction string,
	) error
	OwnedFileForRecording(context.Context, uuid.UUID) (uuid.NullUUID, error)
	SettleAcquiredTarget(ctx context.Context, id, libraryFileID uuid.UUID, summary, detail string) error
	RequeueAcquisitionTarget(
		ctx context.Context, id uuid.UUID, outcome, summary, detail, lastError string,
		nextAttemptAt time.Time,
	) error
	QueueAcquisitionSweep(context.Context, time.Time) error
	// The rest is looking for a copy: what has already been tried, what is in
	// flight, what to fetch next, and completing a copy the scan has since made a
	// library file of.
	AcquisitionTargetFiles(context.Context, uuid.UUID) ([]db.AcquisitionTargetFileRow, error)
	OpenTargetRequest(context.Context, uuid.UUID) (uuid.NullUUID, error)
	OpenTargetTransferQueuedPast(context.Context, uuid.UUID, time.Time) (bool, error)
	OpenFilesByPeer(context.Context) (map[db.Peer]int, error)
	WantsSharingAReleaseWith(context.Context, uuid.UUID) ([]db.AcquisitionTargetRow, error)
	FetchAcquiredFile(context.Context, db.FetchAcquiredFileParams) (uuid.UUID, error)
	RecordAcquiredFile(context.Context, db.RecordAcquiredFileParams) error
	CancelDownloadRequest(context.Context, uuid.UUID) (db.DownloadRequestRow, error)
	RescheduleAcquisitionTarget(
		ctx context.Context, id uuid.UUID, summary string, nextAttemptAt time.Time,
	) error
	DeferAcquisitionTarget(
		ctx context.Context, id uuid.UUID, summary, reason string, nextAttemptAt time.Time,
	) error
	// MarkSourceLoggedOut and ClearSourceLoggedOut track the one fact the
	// dashboard has to be able to say that Test in Settings otherwise keeps to
	// itself: whether the download source is refusing searches because it is
	// not logged in to Soulseek, and since when (#495). MarkSourceLoggedOut
	// reports the moment the outage started, which may be earlier than the
	// instant passed to it.
	MarkSourceLoggedOut(ctx context.Context, at time.Time) (time.Time, error)
	// ClearSourceLoggedOut reports whether this call is the one that found the
	// source signed back in, so a caller can wake every want the outage stood
	// back rather than waiting for each of their own half-hour clocks.
	ClearSourceLoggedOut(ctx context.Context) (bool, error)
	ReleaseStaleFetches(context.Context, uuid.UUID) error
	CompleteAcquiredFile(
		ctx context.Context, id uuid.UUID, summary, detail string,
	) (db.AcquiredFileOutcome, error)
	AcquisitionTargetHasAcceptedCopy(context.Context, uuid.UUID) (bool, error)
	StopLookingForAcquisitionTarget(ctx context.Context, id uuid.UUID, summary string) error
	StopAfterCreditOnlyRefusals(ctx context.Context, id uuid.UUID, summary string) (bool, error)
	StopWhileACopyWaits(ctx context.Context, id uuid.UUID, summary string) (bool, error)
	SettledWitnesses(ctx context.Context, recordingID uuid.UUID, limit int32) ([]db.LibraryWitnessRow, error)
	AcquisitionTargetHasAnchor(ctx context.Context, targetID uuid.UUID) (bool, error)
	// What the library says about the file a want's accepted copy became, and
	// the wants that have already stopped because it said something else.
	AcquiredCopyFiling(context.Context, uuid.UUID) (db.AcquiredCopyFiling, error)
	RecordCopyWaveform(ctx context.Context, copyID uuid.UUID, peaks []byte) error
	StoppedTargetsHoldingAFiledCopy(context.Context, int32) ([]db.AcquisitionTargetRow, error)
	// QueueFileResolution asks the library what one of its files is. A want
	// whose copy nobody has asked about yet waits for the answer rather than
	// stopping on the silence.
	QueueFileResolution(context.Context, uuid.UUID) (db.LibraryScanJobRow, error)
	// Keying a want to an address and setting its floor (ADR 0038).
	KeyAcquisitionTargetToSource(context.Context, db.KeySourceParams) (db.AcquisitionTargetRow, error)
	SetAcquisitionTargetMinimumBitrate(ctx context.Context, id uuid.UUID, kbps int) (db.AcquisitionTargetRow, error)
}

// Entry is what somebody asked for, in their own words. Everything except the
// origin is optional on its own; together they have to identify something.
type Entry struct {
	Origin         string
	OriginAlbumID  uuid.NullUUID
	Artist         string
	Title          string
	Album          string
	DurationMS     *int32
	ISRC           string
	RecordingID    uuid.NullUUID
	ReleaseGroupID uuid.NullUUID
}

type Service struct {
	store Store
	// resolver turns an entry into a recording. Optional: without it an entry
	// waits to be resolved rather than being guessed at.
	resolver Resolver
	// hunter looks for copies, fetcher starts their transfers, and listener says
	// whether a copy that arrives could be verified at all. All three are
	// optional, and without any of them a want waits rather than being satisfied
	// by something nobody checked.
	hunter   Hunter
	fetcher  Fetcher
	listener Listener
	// matcher offers an accounted-for file to the catalogue. Optional: without
	// it the file stays unmatched until matching next happens to look at it,
	// which for a file with junk peer tags is no particular day at all.
	matcher Matcher
	// upgrader carries out a proven upgrade want: comparing the new copy
	// against the old file and replacing it when the new one is better.
	// Optional: without it an upgrade want still settles, and the two files
	// are left for the duplicates screen.
	upgrader Upgrader
	// filer writes down that a library file is the recording a want's copy was
	// proven to be. Optional: without it a want whose copy the library files
	// under a second row of the same registration stops for a person.
	filer Filer
	// sources asks again what a file with an automatic source identity is, and
	// moves it onto a recording once one is proven (ADR 0037 §6). Optional:
	// without it such a file keeps its source identity.
	sources SourceRechecker
	// tracks reads an address a person keys a want to, and prints fingerprints
	// its excerpt (ADR 0038). Optional: without them no want can be keyed.
	tracks TrackSources
	prints anchor.Fingerprinter
	// namer names a file admitted for a keyed want. Optional: without it the
	// file keeps the tags it arrived with.
	namer SourceNamer
	// notices tells connected interfaces that the wants changed. Optional:
	// without it the views go back to asking on a timer.
	notices *events.Hub
	logger  zerolog.Logger
	now     func() time.Time
	// reviewed guards the one pass over the wants that stopped before the rules
	// that stop them now. It runs once per process, on the first sweep.
	reviewed sync.Once
}

// Matcher maps a library file to the catalogue tracks its identity names. It is
// the same matcher the scanner and the identity service call; acquisition only
// adds the missing occasion — the moment the proven identity is written.
type Matcher interface {
	ReconcileFile(ctx context.Context, fileID uuid.UUID) error
}

// Upgrader carries out what an upgrade want exists for once its copy is
// proven and imported: comparing the new file against the old one it names,
// and — only when the new one is genuinely better — replacing the old file
// with it under a recorded deletion licence (ADR 0021). It reports
// whether it replaced the file. When the new copy is not better it leaves both
// files exactly where they are and reports false; nothing here ever refuses a
// proof or admits one that was not already proven.
type Upgrader interface {
	ReplaceIfBetter(ctx context.Context, oldFileID, newFileID uuid.UUID) (bool, error)
}

func NewService(store Store, logger zerolog.Logger) *Service {
	return &Service{store: store, logger: logger, now: time.Now}
}

func (service *Service) WithEvents(hub *events.Hub) *Service {
	service.notices = hub
	return service
}

// WithMatcher registers what maps an imported copy to the catalogue.
func (service *Service) WithMatcher(matcher Matcher) *Service {
	service.matcher = matcher
	return service
}

// WithUpgrader registers what carries out a proven upgrade. Without it an
// upgrade want still settles once its copy is proven — the two files simply
// sit side by side, exactly as two copies obtained any other way would, until
// somebody clears one from the duplicates screen.
func (service *Service) WithUpgrader(upgrader Upgrader) *Service {
	service.upgrader = upgrader
	return service
}

// Create records a want. It reports whether the target is a new one: asking
// twice for the same recording is one want, and the second answer is the target
// that already exists rather than a second acquisition of the same music.
func (service *Service) Create(ctx context.Context, entry Entry) (db.AcquisitionTargetRow, bool, error) {
	entry.Artist = strings.TrimSpace(entry.Artist)
	entry.Title = strings.TrimSpace(entry.Title)
	entry.Album = strings.TrimSpace(entry.Album)
	entry.ISRC = strings.ToUpper(strings.TrimSpace(entry.ISRC))

	switch entry.Origin {
	case "manual", "playlist", "follow_feed", "recommendation", "label_feed":
	default:
		return db.AcquisitionTargetRow{}, false, ErrUnknownOrigin
	}
	if entry.Title == "" && entry.ISRC == "" {
		return db.AcquisitionTargetRow{}, false, ErrEntryNotIdentifiable
	}

	params := db.CreateAcquisitionTargetParams{
		Origin:                    entry.Origin,
		OriginAlbumID:             entry.OriginAlbumID,
		EntryArtist:               entry.Artist,
		EntryTitle:                entry.Title,
		EntryAlbum:                entry.Album,
		EntryISRC:                 text(entry.ISRC),
		MusicBrainzRecordingID:    entry.RecordingID,
		MusicBrainzReleaseGroupID: entry.ReleaseGroupID,
		Summary:                   UnresolvedSummary,
	}
	if entry.DurationMS != nil {
		params.EntryDurationMS = pgtype.Int4{Int32: *entry.DurationMS, Valid: true}
	}
	// A target somebody handed a recording has already been resolved, by them.
	// Recording that as the method keeps every resolved target able to say who
	// or what concluded it.
	if entry.RecordingID.Valid {
		params.ResolutionMethod = text("manual")
		params.Summary = wantedSummary
	}

	id, created, err := service.store.CreateAcquisitionTarget(ctx, params)
	if err != nil {
		return db.AcquisitionTargetRow{}, false, fmt.Errorf("create acquisition target: %w", err)
	}
	target, err := service.store.AcquisitionTarget(ctx, id)
	if err != nil {
		return db.AcquisitionTargetRow{}, false, fmt.Errorf("read acquisition target %s: %w", id, err)
	}
	// Every new want goes in line for an audio anchor: the distributor's preview
	// when the entry has an ISRC, a YouTube upload found by name when it has not
	// (ADR 0029). Failing to note that is not a reason to fail the
	// creation: the want exists, and an anchor is evidence it may gain later
	// rather than anything it depends on.
	if created {
		if err := service.store.ScheduleAnchor(ctx, id, time.Now()); err != nil {
			service.logger.Warn().Err(err).Str("acquisition_target_id", id.String()).
				Msg("could not put a new want in line for its preview")
		}
	}
	if created && target.Status == "pending" {
		service.kickSweeper(ctx)
	}
	if created {
		service.announce()
	}
	return target, created, nil
}

// Target returns one target, or pgx.ErrNoRows when it does not exist.
func (service *Service) Target(ctx context.Context, id uuid.UUID) (db.AcquisitionTargetRow, error) {
	return service.store.AcquisitionTarget(ctx, id)
}

// List returns a page of targets, most recently asked for first.
func (service *Service) List(
	ctx context.Context, params db.ListAcquisitionTargetsParams,
) (db.AcquisitionTargetPage, error) {
	return service.store.ListAcquisitionTargets(ctx, params)
}

// StopPursuing records that the user does not want this recording. It is scoped
// to the one recording: the release it belongs to and the artist behind it are
// untouched, and a file already acquired stays in the library, because this
// says stop looking rather than throw away.
//
// pgx.ErrNoRows means there was nothing left to stop.
func (service *Service) StopPursuing(ctx context.Context, id uuid.UUID) (db.AcquisitionTargetRow, error) {
	target, err := service.store.StopPursuingAcquisitionTarget(ctx, id, notWantedSummary)
	if err != nil {
		return db.AcquisitionTargetRow{}, err
	}
	service.announce()
	return target, nil
}

// PursueAgain withdraws a decision to stop. Nothing automatic may overturn a
// decision the user made, but they may take it back themselves, exactly as a
// manual file identity can be withdrawn.
//
// pgx.ErrNoRows means this target was not one somebody had stopped pursuing.
func (service *Service) PursueAgain(ctx context.Context, id uuid.UUID) (db.AcquisitionTargetRow, error) {
	target, err := service.store.PursueAcquisitionTargetAgain(
		ctx, id, UnresolvedSummary, wantedSummary, service.now(),
	)
	if err != nil {
		return db.AcquisitionTargetRow{}, err
	}
	// Either way it is due now, so the sweeper is asked for now.
	//
	// Only 'pending' woke it before, which left a want that had never been
	// resolved sitting at "due" until some unrelated target's turn came round —
	// due, and nothing looking. Wanting something again is the same instruction
	// whether what happens next is a lookup or a search.
	if target.Status == "pending" || target.Status == "unresolved" {
		service.kickSweeper(ctx)
	}
	service.announce()
	return target, nil
}

// TakeBestAvailable records the user's decision to accept a below-floor copy
// for this want only. The source search remains subject to format refusals.
func (service *Service) TakeBestAvailable(ctx context.Context, id uuid.UUID) (db.AcquisitionTargetRow, error) {
	target, err := service.store.TakeBestAvailableAcquisitionTarget(ctx, id)
	if err != nil {
		return db.AcquisitionTargetRow{}, err
	}
	service.kickSweeper(ctx)
	service.announce()
	return target, nil
}

// KeepFloor withdraws the user's bitrate-floor waiver and checks the want again
// with the stored floor.
func (service *Service) KeepFloor(ctx context.Context, id uuid.UUID) (db.AcquisitionTargetRow, error) {
	target, err := service.store.KeepAcquisitionTargetFloor(ctx, id)
	if err != nil {
		return db.AcquisitionTargetRow{}, err
	}
	service.kickSweeper(ctx)
	service.announce()
	return target, nil
}

// Sweep attempts every target whose turn it is.
//
// Resolution runs first, because a target it resolves becomes due immediately and
// the pass that comes straight after picks it up: an entry asked for now can end
// up as a want being looked for within two sweeps rather than two ladder rungs.
func (service *Service) Sweep(ctx context.Context) error {
	// The wants that stopped are not due and never will be, so the first sweep of
	// a process is the only thing that can look at them again. It logs its own
	// failures: a want nobody could re-read must not stop every want that is due.
	service.reviewed.Do(func() { service.reviewStoppedFilings(ctx) })
	if err := service.resolveDue(ctx); err != nil {
		return err
	}
	if err := service.recheckSources(ctx); err != nil {
		return err
	}
	targets, err := service.store.DueAcquisitionTargets(ctx, service.now(), sweepBatch)
	if err != nil {
		return fmt.Errorf("list due acquisition targets: %w", err)
	}
	// Read after resolution ran, so a want resolved this pass is looked for on
	// its recording and not on its anchor. The two share one batch: a search on
	// an anchor is a Soulseek search like any other.
	if spare := sweepBatch - len(targets); spare > 0 {
		anchored, err := service.store.DueSearchTargets(ctx, service.now(), int32(spare))
		if err != nil {
			return fmt.Errorf("list the wants due a search on their anchor: %w", err)
		}
		targets = append(targets, anchored...)
	}
	// Attempted a few at a time. Every want in the pass is independent — its own
	// row, its own request, its own copy — and almost all of the time an attempt
	// takes is spent waiting for peers to answer a search, so waiting for several
	// at once is what the pass is for. A want that fails does not take the others
	// down with it: the first failure is what the pass reports, once the rest have
	// finished, and everything still due is picked up by the pass after.
	var attempts sync.WaitGroup
	var mutex sync.Mutex
	var failure error
	// A refusal about the source stands the whole pass down. The provider saying
	// "no more searches" to one want is saying it to all of them, and a pass
	// that asks anyway burns its whole round learning the same fact once per
	// want — seventeen of nineteen, one evening. A provider that refused only
	// one want's own words has said nothing about the others, and is not
	// recorded here at all (see refusalFor).
	var refused passRefusal
	slots := make(chan struct{}, huntWorkers)
	for _, target := range targets {
		attempts.Add(1)
		go func() {
			defer attempts.Done()
			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-slots }()

			if _, err := service.attempt(ctx, target, &refused); err != nil {
				mutex.Lock()
				defer mutex.Unlock()
				if failure == nil {
					failure = err
				}
			}
		}()
	}
	attempts.Wait()

	if len(targets) > 0 {
		service.announce()
	}
	return failure
}

// NextDue reports when the next target is due — the earlier of the next entry to
// resolve and the next want to look for. A zero time means nothing is scheduled,
// which is what lets the sweeper stop instead of waking up to find nothing to do.
//
// Entries waiting to be resolved count only when something can resolve them. An
// installation with no resolver leaves them alone, and a schedule for work that
// will not happen is a sweeper waking forever rather than a schedule.
//
// It is asked separately from Sweep, and deliberately: the answer is only
// trustworthy once the sweep's job is no longer live. While one is, a want
// created alongside it cannot queue a sweep of its own — the index allows one —
// so a schedule read before that point can miss it and nothing would be left to
// pick it up.
func (service *Service) NextDue(ctx context.Context) (time.Time, error) {
	due, err := service.store.NextAcquisitionTargetDue(ctx)
	if err != nil {
		return time.Time{}, fmt.Errorf("read the next acquisition attempt: %w", err)
	}
	next := time.Time{}
	if due.Valid {
		next = due.Time
	}
	searched, err := service.store.NextSearchTargetDue(ctx)
	if err != nil {
		return time.Time{}, fmt.Errorf("read the next search on an anchor: %w", err)
	}
	if searched.Valid && (next.IsZero() || searched.Time.Before(next)) {
		next = searched.Time
	}
	if service.sources != nil {
		recheck, err := service.store.NextSourceRecheckDue(ctx)
		if err != nil {
			return time.Time{}, fmt.Errorf("read the next look at a source file: %w", err)
		}
		if recheck.Valid && (next.IsZero() || recheck.Time.Before(next)) {
			next = recheck.Time
		}
	}
	if service.resolver == nil {
		return next, nil
	}
	unresolved, err := service.store.NextUnresolvedTargetDue(ctx)
	if err != nil {
		return time.Time{}, fmt.Errorf("read the next entry to resolve: %w", err)
	}
	if unresolved.Valid && (next.IsZero() || unresolved.Time.Before(next)) {
		next = unresolved.Time
	}
	return next, nil
}

// attempt is one try at satisfying one want.
//
// The library is asked first, and that is a real answer rather than a shortcut: a
// file imported by hand, or acquired for another release entirely, satisfies the
// want the moment its identity is decided, and nobody has to be told. Only then
// does anything go looking, because acquiring a second copy of music already
// owned is the one outcome nobody wants.
//
// A copy that was proven and imported is finished here too. The library row it
// became does not exist until the scan the import queued has run, so the pass
// after that is what gives the file its identity and settles the want — which
// also means a restart in between costs a delay rather than the want.
// It reports whether this want went looking, which is what the pass counts: a
// want the library answers costs a query, and one that has to be searched for
// costs a provider the worker is sharing with everything else.
func (service *Service) attempt(
	ctx context.Context, target db.AcquisitionTargetRow, pass *passRefusal,
) (bool, error) {
	// A want with no recording is looked for only when it is unresolved and
	// searched on its anchor, which is the only way DueSearchTargets returns one
	// (ADR 0037).
	onAnchor := !target.MusicBrainzRecordingID.Valid
	if onAnchor && (target.Status != "unresolved" || !anchorAdmits(target.AnchorSource)) {
		return false, nil
	}
	if done, err := service.completeAcquired(ctx, target); err != nil || done {
		return false, err
	}
	// An upgrade want's whole premise is that the library already owns the
	// recording — in the very file this want exists to replace. Asking the
	// library here would settle the want against that file on its first turn
	// and nothing would ever be looked for. Skipping this one question admits
	// nothing: the proof a copy is judged by is unchanged, and a copy that
	// arrives still has to pass it.
	// The library holds files by recording, and this want has none to ask about.
	if target.Origin != "upgrade" && !onAnchor {
		held, err := service.heldAlready(ctx, target)
		if err != nil || held {
			return false, err
		}
	}
	// One want, one copy. A want that already has an accepted copy is not looked
	// for again, whatever became of that copy afterwards, and this is the one
	// place every route to a search passes through.
	answered, err := service.answeredAlready(ctx, target)
	if err != nil || answered {
		return false, err
	}
	// A refusal already heard this pass is heard for everyone: the want
	// stands back for the same short while instead of asking a provider
	// that just said no, and no attempt is spent on it.
	if heard, refused := pass.refused(); refused {
		return false, service.deferRefused(ctx, target, heard)
	}
	return true, service.hunt(ctx, target, pass)
}

// heldAlready asks the library whether it already has the want's recording, and
// finishes the want against the file that has it when it does. It reports
// whether the want is done with, which is the only thing its callers need: a
// want the library answers has nothing left to look for.
//
// It lives here, rather than inline in the one place that used to ask, because
// the question belongs to every path that is about to go looking. A want
// reached on its own turn and a want reached as a sibling of somebody else's
// folder are the same want, and asking on only one of those routes is how a
// recording sitting on the disc gets fetched a second time.
//
// What counts as having it is exactly what the library has decided:
// OwnedFileForRecording reads a resolved identity or a mapping onto a catalogue
// track carrying the recording, and never the identifier a file merely carries
// in its own tags. Both callers hold a want that names a recording; there is
// nothing to ask about one that does not.
func (service *Service) heldAlready(
	ctx context.Context, target db.AcquisitionTargetRow,
) (bool, error) {
	fileID, err := service.store.OwnedFileForRecording(ctx, target.MusicBrainzRecordingID.UUID)
	if err != nil {
		return false, fmt.Errorf("look for an owned copy of target %s: %w", target.ID, err)
	}
	if !fileID.Valid {
		return false, nil
	}
	err = service.store.SettleAcquiredTarget(
		ctx, target.ID, fileID.UUID, acquiredSummary, acquiredDetail)
	// The target stopped being pending between the read and the write, which
	// means somebody decided something about it while this pass was running.
	// Their decision stands; the attempt is dropped rather than applied over it.
	if errors.Is(err, pgx.ErrNoRows) {
		service.logger.Debug().
			Str("acquisition_target_id", target.ID.String()).
			Msg("acquisition target settled elsewhere during a sweep")
		return true, nil
	}
	if err != nil {
		// Not settled, so not done with: a caller reading this as finished would
		// leave a want the library holds sitting acquired without ever having been.
		return false, fmt.Errorf("settle acquisition target %s: %w", target.ID, err)
	}
	service.logger.Info().
		Str("acquisition_target_id", target.ID.String()).
		Str("library_file_id", fileID.UUID.String()).
		Msg("a wanted recording was already in the library")
	return true, nil
}

// answeredAlready asks whether this want already has a copy somebody or
// something accepted, and stops it looking when it does. It reports whether the
// want is done with, which is all its callers need.
//
// It is the second half of the question heldAlready asks, and it is asked
// second. The library answers a want it can answer; this catches the want the
// library cannot answer because the file it holds is filed under another
// recording — the want has its music, nothing automatic may say so, and every
// further look would fetch the same song again. One want fetched twelve copies
// of it in an hour before this existed (#368).
//
// It is asked third, after completing an accepted copy and after the library,
// and the order is the whole of its safety. Completing runs first so a copy the
// scan has just made a file of is settled, or recorded as the disagreement it
// is, before anything reads it; the library runs next so a want somebody has
// answered by hand is settled rather than stopped. Reaching here means the copy
// is on the disc and the library does not call it this recording, which is three
// different situations that resolveFiling tells apart. Only one of them is the
// library naming other music, and only that one stops.
//
// The other route to a search — a want reached as a sibling of somebody else's
// open folder — does not ask this, because the list of siblings it comes from
// has already left out every want with a copy in flight, being validated or
// imported. Asking again there would be the same question twice, and the answer
// written on a want the folder can no longer reach.
func (service *Service) answeredAlready(
	ctx context.Context, target db.AcquisitionTargetRow,
) (bool, error) {
	accepted, err := service.store.AcquisitionTargetHasAcceptedCopy(ctx, target.ID)
	if err != nil {
		return false, fmt.Errorf("ask what want %s already holds: %w", target.ID, err)
	}
	if !accepted {
		return false, nil
	}
	// A want with no recording whose accepted copy is a library file and still
	// not settled is one completing stopped: the file already carried another
	// identity. It stays stopped. Searching would fetch the music again.
	if !target.MusicBrainzRecordingID.Valid {
		return true, service.store.StopLookingForAcquisitionTarget(ctx, target.ID, disagreedSummary)
	}
	return true, service.resolveFiling(ctx, target)
}

// completeAcquired finishes a copy that was proven and imported, once the scan has
// created the library row for it. It reports whether this pass is done with the
// want either way: a copy whose identity the library already says is something
// else is left in the queue with that as its reason rather than settled on a
// disagreement, and nothing anybody decided is overwritten.
func (service *Service) completeAcquired(
	ctx context.Context, target db.AcquisitionTargetRow,
) (bool, error) {
	outcome, err := service.store.CompleteAcquiredFile(
		ctx, target.ID, disagreedSummary, acquiredCopyDetail)
	if err != nil {
		return false, fmt.Errorf("complete the acquired copy of target %s: %w", target.ID, err)
	}
	if !outcome.LibraryFileID.Valid {
		// A copy is imported and the scan has not made a library row of it yet.
		// The want has its music; it simply has nothing to be settled against
		// for another moment. Going looking now would fetch a second copy of
		// exactly the music that just arrived, so the want stands back instead
		// and the scan finishing is what brings it round again.
		if target.Summary == strandedSummary {
			// Already reported as stranded. The copy is checked again on the
			// same cadence as one still inside its patience, without a second
			// warning or attempt row.
			return true, service.store.RescheduleAcquisitionTarget(
				ctx, target.ID, strandedSummary, service.now().Add(importedDelay))
		}
		if outcome.Awaiting {
			if service.now().Sub(outcome.AwaitingSince) >= importedPatience {
				return true, service.abandonTheWait(ctx, target)
			}
			return true, service.store.RescheduleAcquisitionTarget(
				ctx, target.ID, importedSummary, service.now().Add(importedDelay))
		}
		return false, nil
	}
	event := service.logger.Info()
	if outcome.Disagreed {
		event = service.logger.Warn()
	}
	event.
		Str("acquisition_target_id", target.ID.String()).
		Str("library_file_id", outcome.LibraryFileID.UUID.String()).
		Bool("settled", outcome.Settled).
		Msg("an imported copy of a wanted recording was accounted for")
	// The identity that admitted the copy was written a moment ago, after the
	// scanner had already reconciled the file against a library that did not
	// know it yet. For a file whose peer tags say something else entirely, that
	// identity is the only thing that can map it, so matching is asked now. A
	// failure costs immediacy and nothing else: the identity is recorded, and
	// matching reads it whenever it next looks at the file.
	if service.matcher != nil && outcome.Settled {
		if err := service.matcher.ReconcileFile(ctx, outcome.LibraryFileID.UUID); err != nil {
			service.logger.Error().Err(err).
				Str("library_file_id", outcome.LibraryFileID.UUID.String()).
				Msg("offer an acquired file to the catalogue")
		}
	}
	if outcome.Settled {
		service.nameKeyedFile(ctx, target)
	}
	// An upgrade want settling is the moment this feature exists for: a copy
	// was proven — by exactly the same rules as any other want, and the floor
	// already refused any candidate under it — and now stands beside the file
	// it may replace. Disagreed is excluded on purpose: a copy the library
	// says is a different recording from what this want named is never
	// compared against the old file at all.
	if service.upgrader != nil && outcome.Settled && !outcome.Disagreed &&
		target.Origin == "upgrade" && target.UpgradeOfLibraryFileID.Valid {
		replaced, err := service.upgrader.ReplaceIfBetter(
			ctx, target.UpgradeOfLibraryFileID.UUID, outcome.LibraryFileID.UUID)
		if err != nil {
			service.logger.Error().Err(err).
				Str("acquisition_target_id", target.ID.String()).
				Str("old_library_file_id", target.UpgradeOfLibraryFileID.UUID.String()).
				Msg("replace the file an upgrade want was raised for")
		} else if replaced {
			service.logger.Info().
				Str("acquisition_target_id", target.ID.String()).
				Str("replaced_library_file_id", target.UpgradeOfLibraryFileID.UUID.String()).
				Str("library_file_id", outcome.LibraryFileID.UUID.String()).
				Msg("a better copy replaced a file below the quality floor")
		}
	}
	// The library did not call the file this want's recording, and completing has
	// already written the blunt version of that: stopped, with the general
	// sentence. Whether it is really a disagreement is a question about two
	// MusicBrainz rows and a measurement, which no transaction can ask, so it is
	// asked here and the want is corrected where the answer is no.
	if outcome.Disagreed {
		return true, service.resolveFiling(ctx, target)
	}
	return true, nil
}

// abandonTheWait stops a want claiming its copy is on the way in when it plainly
// is not.
//
// A copy that was imported and never became a library file has waited out
// everything a scan could reasonably take, so what is wrong is not slowness.
// Schall does not know what — the file may have been moved, or removed, or be of
// a type no scan indexes, which is how this was found — so it says the one true
// thing and stops repeating a sentence that reads like progress. The want keeps
// its place: the copy stays accepted and is never offered again, and if the row
// does turn up later the next pass settles the want against it exactly as it
// would have.
//
// Nothing goes looking for another copy. One is already on the disc, and fetching
// a second of the same music because the bookkeeping stalled would be the loop
// papering over a fault instead of reporting it.
func (service *Service) abandonTheWait(
	ctx context.Context, target db.AcquisitionTargetRow,
) error {
	service.logger.Warn().
		Str("acquisition_target_id", target.ID.String()).
		Dur("waited", importedPatience).
		Msg("an imported copy never became a library file")
	return service.settleAttempt(ctx, target, "failed", strandedSummary, strandedDetail)
}

// nextAttempt reads the ladder. attempts is how many have already happened, so
// a target nobody has tried yet waits the first delay.
func (service *Service) nextAttempt(attempts int32) time.Time {
	return service.now().Add(rungOf(attemptDelays, attempts))
}

// nextUnfoundAttempt reads the slower ladder, for a want MusicBrainz has no
// recording for.
func (service *Service) nextUnfoundAttempt(attempts int32) time.Time {
	return service.now().Add(rungOf(unfoundDelays, attempts))
}

// anchorAdmits reports an anchor source that can admit a copy on its own: a
// Deezer preview, the artist's Topic upload (ADR 0024, 0034) or the excerpt
// from the address a person keyed the want to (ADR 0038). A want with no
// recording is searched only on one of these (ADR 0037 §1).
func anchorAdmits(source string) bool {
	return source == identity.AnchorSourceDeezer || source == identity.AnchorByNameTopic ||
		source == identity.AnchorSourceKeyed
}

// nextLook is when a search that found nothing comes back. A want with no
// recording climbs the unfound ladder on its own search count, and never stops
// (ADR 0037 §2): a track can appear years later.
func (service *Service) nextLook(target db.AcquisitionTargetRow) time.Time {
	if target.Status == "unresolved" {
		return service.nextUnfoundAttempt(target.SearchAttempts)
	}
	return service.nextAttempt(target.Attempts)
}

// rungOf picks the delay for an attempt, holding at the last one once the ladder
// runs out. attempts is how many have already happened, so a target nobody has
// tried yet waits the first delay.
func rungOf(ladder []time.Duration, attempts int32) time.Duration {
	index := int(attempts)
	if index < 0 {
		index = 0
	}
	if index >= len(ladder) {
		index = len(ladder) - 1
	}
	return ladder[index]
}

// kickSweeper asks for a sweep now. A failure is logged rather than returned:
// the want is recorded, and a sweep that was not queued costs a delay, never
// the target.
func (service *Service) kickSweeper(ctx context.Context) {
	if err := service.store.QueueAcquisitionSweep(ctx, service.now()); err != nil {
		service.logger.Error().Err(err).Msg("queue acquisition sweep")
	}
}

func (service *Service) announce() {
	service.notices.Publish(events.TopicAcquisitions)
}

// The sentences a target explains itself with. Every state has one, because a
// status nobody can explain is a status nobody trusts.
const (
	UnresolvedSummary = "Waiting to be resolved to a MusicBrainz recording."
	unfoundSummary    = "No recording MusicBrainz knows of matches this entry yet. It will be asked about again."
	unaskedSummary    = "MusicBrainz could not be reached, so this entry has not been resolved yet."
	// A refusal for load reads differently to an outage on purpose: nothing is
	// wrong, and the entry has lost nothing by being told to come back.
	catalogueBusySummary = "MusicBrainz is busy. This entry will be asked about again shortly."
	sweepBusyDetail      = "MusicBrainz is refusing requests for now: it refused another entry in the same pass"
	// The two ways one recording can already be wanted. Which sentence a merged
	// target gets is chosen where the survivor is known, so it cannot describe a
	// decision the survivor did not make.
	supersededSummary          = "Already wanted. This entry names the same recording as another target, which carries it now."
	supersededNotWantedSummary = "This entry resolved to a recording you said you did not want. It has not been added again."
	wantedSummary              = "Wanted. Waiting for a copy to be found."
	waitingSummary             = "Not in your library yet."
	throttledSummary           = "The download source is busy. It will be asked again shortly."
	// The other refusal: not too many questions, but nowhere to put one. It
	// avoids saying busy, because a source that is idle for the wrong reason
	// is exactly what this is.
	unavailableSummary = "The download source cannot search right now. It will be asked again shortly."
	// The sentence a want wears between its copy being imported and the scan
	// making a library file of it. Nothing is pending about the music any more,
	// only the bookkeeping.
	importedSummary = "A copy arrived and was proven. It is being taken into your library."
	// What that sentence turns into when the library row never appears. It says
	// what is known and not what it means, because Schall cannot tell from here
	// whether the file was moved, removed, or is of a type no scan reads.
	strandedSummary  = "A copy was imported, but it never appeared in your library. Nothing is being fetched for this wishlist entry while that copy sits there."
	strandedDetail   = "An accepted copy has been waiting to be scanned for longer than any scan takes."
	acquiredSummary  = "In your library."
	notWantedSummary = "No longer wanted. This recording only; the release and the artist are untouched."
	rejectedSummary  = "The recording this entry was resolved to was the wrong one. Being looked up again, without it."
	rejectedDetail   = "A person said this entry does not name that recording."
	// What a chosen answer says. It never claims to have been proven: the
	// evidence is exactly what could not choose, and a person did.
	chosenSummary = "You chose which recording this entry names. " + wantedSummary
	chosenDetail  = "A person chose this recording from the ones the entry could have named."
	// The two decisions a person takes about copies rather than about the entry.
	// Neither borrows the word proven: nothing proved these, somebody listened.
	acceptedByHandSummary = "You listened to this copy and said it is the wanted recording."
	refusedByHandSummary  = "You listened and said this copy is not the wanted recording. It will not be offered for this wishlist entry again."
	// What a decision that has been taken back says. None of these claims
	// anything was learned: a copy whose answer was withdrawn is a question
	// again, exactly as it was before anybody answered it.
	takenBackCopySummary = "The decision about this copy was taken back. It is waiting for one again."
	takenBackWantSummary = "A copy is waiting for your decision again."
	restoredWantSummary  = "You took back saying this entry names the wrong recording. " + wantedSummary

	// What a want says once its copy is on the disc and the library files that
	// file under another recording. It stops rather than looking again: the music
	// is here, nothing automatic may say the two recordings are one, and looking
	// again fetched the same song twelve times in an hour. Both ways on belong to
	// a person, so both are named.
	disagreedSummary = "A copy arrived and was proven, but your library calls that file a different recording. " +
		"Nothing more is being fetched — match the file to this track, or stop looking."
	// What the same want says when the library's answer is that its copy is owned
	// music no provider names. It is a different fact from naming another
	// recording, and reads as one.
	localOnlyFilingSummary = "A copy arrived and was proven, but your library records that file as owned music with no catalogue recording. " +
		"Nothing more is being fetched — match the file to this track, or stop looking."
	// What the same want says when the library has no identity for its copy at
	// all and the copy's row carries no proof of this recording to write back.
	unprovenFilingSummary = "A copy arrived, but your library has no identity for that file and nothing on record proves the copy is this recording. " +
		"Nothing more is being fetched — match the file to this track, or stop looking."
	creditOnlyRefusalsSummary = "Every copy found so far carries a different artist credit. Nothing more is being fetched until you decide whether these copies are the wanted recording."
	// What a want says while it holds a copy nobody has answered and has no
	// sample of its own to answer it with. See db.StopWhileACopyWaits.
	copyWaitingSummary = "A copy is waiting for your answer. Nothing more is fetched until you decide."

	acquiredDetail     = "A file the library already holds is proven to be this recording."
	acquiredCopyDetail = "A copy was fetched, proven by its audio, and imported."
	// What an attempt records when the want named the clean edition and the file
	// on the disc holds the explicit one. The two are different recordings, so
	// the detail says which one the library has (ADR 0032).
	explicitEditionDetail = "A copy was fetched, proven by its audio, and imported. " +
		"Your library files it as the explicit edition of this track, which is the one Schall keeps."
	waitingDetail = "No file in the library is proven to be this recording."
)

func text(value string) pgtype.Text {
	if value == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: value, Valid: true}
}

// Copies reads back what has been tried for one want: every offer that was
// fetched or judged, newest first. It is what makes the loop observable — how
// often a copy is proven, refused, or turns out to be a question is the one thing
// nobody can guess at from the outside.
func (service *Service) Copies(
	ctx context.Context, id uuid.UUID,
) ([]db.AcquisitionTargetFileRow, error) {
	return service.store.AcquisitionTargetFiles(ctx, id)
}

// RejectResolution records that an entry was resolved to the wrong recording and
// sends it back to be looked up again without it.
//
// This is the one review outcome that says nothing about any file. The copies
// fetched so far were fetched for a recording nobody wanted, so they are not
// evidence about anything and they are not re-offered; what they were judged
// against no longer exists as this want's answer.
//
// db.ErrNothingToReject means there was no resolution to reject.
func (service *Service) RejectResolution(
	ctx context.Context, id uuid.UUID,
) (db.AcquisitionTargetRow, error) {
	target, err := service.store.RejectAcquisitionTargetResolution(
		ctx, id, rejectedSummary, rejectedDetail, service.now(),
	)
	if err != nil {
		return db.AcquisitionTargetRow{}, err
	}
	service.logger.Info().
		Str("acquisition_target_id", target.ID.String()).
		Msg("acquisition target resolution rejected")
	service.kickSweeper(ctx)
	service.announce()
	return target, nil
}

// ChooseResolution records which of the recordings an entry could name is the one
// it does.
//
// This is the answer to the question askTheUser stored: several recordings fit
// the entry and the evidence does not choose between them, so nothing automatic
// ever will. A person can, and what they say is written as a manual resolution —
// permanent, in the same way a manual mapping is, and never revisited by the
// resolver, which only ever looks at entries that have no recording at all.
//
// db.ErrNotOfferedForReview means the recording was not one of the answers
// offered; pgx.ErrNoRows means the want stopped waiting for an answer meanwhile.
func (service *Service) ChooseResolution(
	ctx context.Context, id, recordingID uuid.UUID,
) (db.AcquisitionTargetRow, error) {
	target, err := service.store.ChooseAcquisitionTargetResolution(
		ctx, db.ResolveAcquisitionTargetParams{
			ID:                     id,
			MusicBrainzRecordingID: recordingID,
			Summary:                chosenSummary,
			SupersededSummary:      supersededSummary,
			SupersededNotWanted:    supersededNotWantedSummary,
			Detail:                 chosenDetail,
			NextAttemptAt:          service.now(),
		})
	if err != nil {
		return db.AcquisitionTargetRow{}, err
	}
	service.logger.Info().
		Str("acquisition_target_id", target.ID.String()).
		Str("musicbrainz_recording_id", recordingID.String()).
		Str("status", target.Status).
		Msg("a person chose which recording an entry names")
	// A want that has just been given its recording is due now. Superseded is the
	// other outcome — the recording is already wanted by another target — and
	// there is nothing to look for on this one.
	if target.Status == "pending" {
		service.kickSweeper(ctx)
	}
	service.announce()
	return target, nil
}

// AcceptCopy records that a person says one held copy is the wanted recording.
// The file is brought in by the import that follows, not here.
//
// db.ErrNotWaitingForADecision means the copy was already answered.
func (service *Service) AcceptCopy(ctx context.Context, copyID uuid.UUID) error {
	requestID, err := service.store.AcceptHeldCopy(ctx, copyID, acceptedByHandSummary)
	if err != nil {
		return err
	}
	service.logger.Info().
		Str("acquisition_target_file_id", copyID.String()).
		Str("request_id", requestID.String()).
		Msg("a held copy was accepted by hand")
	service.announce()
	return nil
}

// RefuseCopies records that none of the copies held for a want are the recording,
// and sends the want back to look for another.
//
// db.ErrNotWaitingForADecision means there was nothing held to refuse.
func (service *Service) RefuseCopies(ctx context.Context, targetID uuid.UUID) error {
	if err := service.store.RefuseHeldCopies(
		ctx, targetID, refusedByHandSummary, waitingSummary+" The next copy will be tried.",
		service.now(),
	); err != nil {
		return err
	}
	service.logger.Info().
		Str("acquisition_target_id", targetID.String()).
		Msg("the copies held for a want were refused by hand")
	service.kickSweeper(ctx)
	service.announce()
	return nil
}

// Reversible reports whether one recorded answer can still be taken back, so
// the queue offers the control only while it would work. Nothing writes on the
// strength of this: every condition it asks about is asked again, inside the
// statement that performs the reversal.
func (service *Service) Reversible(
	ctx context.Context, answer db.Answer, id uuid.UUID,
) (bool, error) {
	return service.store.AnswerIsReversible(ctx, answer, id)
}

// TakeBack withdraws one recorded answer.
//
// Four of the five review outcomes are written down, and each may be taken back
// for as long as the thing it asked for has not happened (ADR 0019): the copy is
// not in the library yet, the want has taken no other answer, nothing else has
// claimed the recording, the entry has not been resolved again. Which of those
// applies is decided by the statement that performs the reversal, so a refusal
// here is a fact rather than a race.
//
// Nothing Schall decided for itself is reversible by any of them. The loop's own
// refusals are its reading of the audio, and a person who disagrees answers the
// question the loop raised instead of editing its record.
func (service *Service) TakeBack(ctx context.Context, answer db.Answer, id uuid.UUID) error {
	switch answer {
	case db.AnswerAccept:
		if err := service.store.TakeBackAcceptedCopy(
			ctx, id, takenBackCopySummary, takenBackWantSummary,
		); err != nil {
			return err
		}
	case db.AnswerNoneOfThese:
		if err := service.store.TakeBackRefusedCopies(
			ctx, id, takenBackCopySummary, takenBackWantSummary,
		); err != nil {
			return err
		}
	case db.AnswerNotWanted:
		target, err := service.store.TakeBackNotWanted(
			ctx, id, UnresolvedSummary, wantedSummary, service.now(),
		)
		if err != nil {
			return err
		}
		// Either way it is due now, and for the reason PursueAgain wakes the
		// sweeper for both states: wanting something again is the same
		// instruction whether what happens next is a lookup or a search.
		if target.Status == "pending" || target.Status == "unresolved" {
			service.kickSweeper(ctx)
		}
	case db.AnswerWrongRecording:
		if _, err := service.store.TakeBackWrongRecording(
			ctx, id, restoredWantSummary, service.now(),
		); err != nil {
			return err
		}
		service.kickSweeper(ctx)
	default:
		return fmt.Errorf("%q is not an answer that is written down", answer)
	}
	service.logger.Info().
		Str("answer", string(answer)).
		Str("subject_id", id.String()).
		Msg("a recorded answer was taken back")
	service.announce()
	return nil
}

// Copy reads one offer by name. pgx.ErrNoRows means no such copy.
func (service *Service) Copy(
	ctx context.Context, id uuid.UUID,
) (db.AcquisitionTargetFileRow, error) {
	return service.store.AcquiredCopy(ctx, id)
}

// RecordCopyWaveform keeps the shape of one copy's audio, for the copies that
// were held before anything drew one. It writes a picture and touches no verdict.
func (service *Service) RecordCopyWaveform(
	ctx context.Context, copyID uuid.UUID, peaks []byte,
) error {
	return service.store.RecordCopyWaveform(ctx, copyID, peaks)
}

// ReviewQueue reads the wants waiting on a person: the ones holding a copy
// nobody could decide about, and the ones that fit several recordings equally
// well. Both are questions with an answer somebody can give.
//
// A want MusicBrainz holds no recording for is not one of them and is not read
// here. Nothing about it is uncertain and there is nothing to choose between; the
// way out is to write the release into MusicBrainz, which the playlist page
// offers on the entry itself.
func (service *Service) ReviewQueue(
	ctx context.Context, limit, offset int32,
) (db.AcquisitionReviewPage, error) {
	return service.store.AcquisitionReviewQueue(ctx, limit, offset)
}

// Candidates reads back the recordings one entry could name, so the question can
// be shown together with the evidence behind it.
func (service *Service) Candidates(
	ctx context.Context, id uuid.UUID,
) ([]db.AcquisitionTargetCandidateRow, error) {
	return service.store.AcquisitionTargetCandidates(ctx, id)
}
