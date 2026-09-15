// Package downloads turns a recorded download request into transfers at a
// provider, and follows them until they settle. It is the only place in Schall
// that starts a transfer.
package downloads

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/events"
	"github.com/pxldi/schall/internal/sources"
	"github.com/rs/zerolog"
)

// pollInterval is how long the poller waits before looking at the provider
// again while transfers are still moving. Soulseek transfers take minutes, so
// asking more often would only add load without telling anyone anything new.
const pollInterval = 5 * time.Second

// startTimeout, pollTimeout and recheckTimeout bound the provider calls, so a
// wedged slskd cannot hold a request open or stall the worker.
const (
	startTimeout   = 30 * time.Second
	pollTimeout    = 20 * time.Second
	recheckTimeout = 45 * time.Second
)

// peerBusySummary is what a want says while the peer holding its copy is
// already sending as much as it will take at once. It says nothing about the
// music and asks nothing of anybody: the loop comes back on its own, which is
// the whole point of not calling this a failure.
const peerBusySummary = "A peer has a copy and is busy. It will be asked again shortly."

// failedTransferSummary is what a want says when the copy it was following
// failed to arrive at all. It is looked for again straight away rather than
// waiting out the rest it gets while a copy is still in flight.
const failedTransferSummary = "The copy being fetched did not arrive. It will be looked for again."

// ErrNotSupported reports a configured provider that can search but not
// transfer. Search-only providers are legitimate, so this is not a failure of
// configuration.
var ErrNotSupported = errors.New("the configured download source cannot start transfers")

// ErrSourceChanged reports a peer that is still offering the recorded folder,
// but not offering what was recorded for it. Starting anyway is a decision for
// the user rather than for Schall, so it is refused until someone answers.
var ErrSourceChanged = errors.New("the peer no longer offers this folder as it was recorded")

// SourceOffer is what re-searching for a recorded folder found at the peer.
//
// A request is provenance recorded at a moment in time, and a peer's shared
// folder is not a promise. Between choosing a source and starting it the peer
// may have renamed files, re-ripped the release, or stopped sharing it, and
// every one of those becomes a transfer that fails on a name nobody can serve.
type SourceOffer struct {
	// Found is whether the folder appeared in the search at all. This is the
	// distinction the whole check turns on: a peer that is merely offline
	// returns nothing, and nothing is not evidence that the folder is gone.
	// slskd queues a transfer for a peer that comes back, so an offline peer
	// must not stop a start.
	Found bool
	// Missing are the recorded files the peer no longer offers under the name
	// it advertised, and Changed are the ones it offers at a different size.
	// Both are readable names rather than remote paths, because this is shown
	// to a person.
	Missing []string
	Changed []string
}

// Stale reports a folder that is still on offer but no longer holds what was
// recorded against it.
func (offer SourceOffer) Stale() bool {
	return offer.Found && (len(offer.Missing) > 0 || len(offer.Changed) > 0)
}

// SourceChangedError is ErrSourceChanged carrying the evidence behind it, so a
// refusal can show what is gone rather than only that something is. It matches
// how duplicate protection refuses: the answer it asks for is only as good as
// what the person answering was shown.
type SourceChangedError struct {
	Offer SourceOffer
}

func (err SourceChangedError) Error() string {
	return ErrSourceChanged.Error() + ": " + err.Offer.summary()
}

func (err SourceChangedError) Unwrap() error { return ErrSourceChanged }

type Store interface {
	DownloadRequest(context.Context, uuid.UUID) (db.DownloadRequestRow, error)
	StartDownloadRequest(context.Context, uuid.UUID) (db.DownloadRequestRow, error)
	RevertDownloadRequestStart(context.Context, uuid.UUID, string) (db.DownloadRequestRow, error)
	RetryDownloadRequestFiles(context.Context, uuid.UUID, []string) (db.DownloadRequestRow, []db.DownloadRequestFile, error)
	RevertDownloadRequestRetry(context.Context, uuid.UUID, []string, string) (db.DownloadRequestRow, error)
	RecordTransferProgress(context.Context, uuid.UUID, []db.TransferProgress) (db.DownloadRequestRow, error)
	StartedDownloadRequests(context.Context) ([]db.DownloadRequestRow, error)
	RequestedTransferIDs(context.Context, uuid.UUID) ([]string, error)
	CancelDownloadRequest(context.Context, uuid.UUID) (db.DownloadRequestRow, error)
	QueueDownloadPoll(context.Context, time.Time) error
	QueuePeerChallenges(context.Context, time.Time) error
	QueueDownloadImport(context.Context, uuid.UUID) error
	DeferAcquiredFetch(ctx context.Context, requestID uuid.UUID, targetSummary string) error
	RescheduleFailedTransfer(ctx context.Context, targetID uuid.UUID, summary string) error
	SlskdSettings(context.Context) (db.SlskdSettingsRow, error)
}

type Service struct {
	store     Store
	providers sources.Builder
	logger    zerolog.Logger
	imports   bool
	// notices tells connected interfaces that a request changed. It is
	// optional: without it the views go back to asking on a timer.
	notices *events.Hub
}

func NewService(store Store, providers sources.Builder, logger zerolog.Logger) *Service {
	return &Service{store: store, providers: providers, logger: logger}
}

func (service *Service) WithImportQueue() *Service {
	service.imports = true
	return service
}

// WithEvents registers the hub that tells the interface a request changed, so
// starting, settling, and cancelling reach it without waiting for a timer.
func (service *Service) WithEvents(hub *events.Hub) *Service {
	service.notices = hub
	return service
}

// announce says the downloads view is stale. It carries no state: the view
// refetches through the endpoints it already uses.
func (service *Service) announce() { service.notices.Publish(events.TopicDownloads) }

// Start enqueues every file of a recorded request at the provider and begins
// following it. The request is marked started before the provider is asked, so
// a transfer can never be running without a record of it; if the provider
// refuses, the request goes back to being merely recorded, with the reason.
func (service *Service) Start(
	ctx context.Context, requestID uuid.UUID, acknowledgeSourceChange bool,
) (db.DownloadRequestRow, error) {
	downloader, err := service.downloader(ctx)
	if err != nil {
		return db.DownloadRequestRow{}, err
	}

	// The offer is checked before the request is claimed, so a refusal leaves it
	// exactly as it was rather than started and immediately reverted.
	if !acknowledgeSourceChange {
		recorded, recordedErr := service.store.DownloadRequest(ctx, requestID)
		if recordedErr != nil {
			return db.DownloadRequestRow{}, recordedErr
		}
		offer, offerErr := service.offer(ctx, downloader, recorded)
		if offerErr != nil {
			// A search that failed says nothing about the folder. Refusing on it
			// would make an unreachable provider look like a peer who withdrew
			// the music, and starting is what the user asked for.
			service.logger.Warn().Err(offerErr).Str("request_id", requestID.String()).
				Msg("could not re-check whether the source is still on offer")
		} else if offer.Stale() {
			return recorded, SourceChangedError{Offer: offer}
		}
	}

	row, err := service.store.StartDownloadRequest(ctx, requestID)
	if err != nil {
		return db.DownloadRequestRow{}, err
	}

	files := make([]sources.File, 0, len(row.Files))
	for _, file := range row.Files {
		files = append(files, sources.File{Path: file.Path, SizeBytes: file.SizeBytes})
	}

	startCtx, cancel := context.WithTimeout(ctx, startTimeout)
	defer cancel()
	if startErr := downloader.StartDownloads(startCtx, row.SourceUsername, files); startErr != nil {
		// Reverted with the original context: the request must not be left
		// claiming to be running because the caller gave up mid-flight.
		reverted, revertErr := service.store.RevertDownloadRequestStart(
			context.WithoutCancel(ctx), requestID, startErr.Error())
		if revertErr != nil {
			service.logger.Error().Err(revertErr).Str("request_id", requestID.String()).
				Msg("could not revert a download request that failed to start")
			return db.DownloadRequestRow{}, startErr
		}
		service.logger.Warn().Err(startErr).Str("request_id", requestID.String()).
			Msg("provider refused to start the requested transfers")
		return reverted, startErr
	}

	if err := service.store.QueueDownloadPoll(ctx, time.Now()); err != nil {
		// The transfers are running; failing to queue the poller only means
		// progress is not followed yet, and the next start or restart queues it.
		service.logger.Error().Err(err).Msg("queue download poller")
	}
	service.readPeerMessages(ctx)
	service.logger.Info().
		Str("request_id", requestID.String()).
		Str("username", row.SourceUsername).
		Int("files", len(files)).
		Msg("download transfers started")
	service.announce()
	return row, nil
}

// ErrNotOnePeer reports a batch of requests that are not all addressed to the
// same person. One provider call goes to one peer, so a batch that spans two of
// them is a caller's mistake rather than something to split up quietly.
var ErrNotOnePeer = errors.New("one enqueue goes to one peer, and these requests name several")

// StartTogether enqueues several requests at one peer in a single provider
// call.
//
// A Soulseek client counts what one stranger has queued with it and refuses
// everything past its limit, so asking one person for ten files ten times over
// is the surest way to be told "too many files" ten times. Asked once, the same
// ten files are one queue entry and arrive together, which is also the better
// outcome: several wants satisfied by one folder in one go.
//
// What is shared is the provider call and the peer's view of us, and nothing
// else. Every request keeps its own row, its own transfers and its own
// bookkeeping — one open request per want, exactly as before — because the thing
// being batched is the asking, not the wanting.
//
// The source is not re-checked. These paths came out of a search a moment ago,
// which is the case the re-check in Start deliberately does not exist for.
func (service *Service) StartTogether(ctx context.Context, requestIDs []uuid.UUID) error {
	if len(requestIDs) == 0 {
		return nil
	}
	downloader, err := service.downloader(ctx)
	if err != nil {
		return err
	}

	// Every row is claimed before the provider is asked, for the reason Start
	// claims first: a transfer running that Schall has no record of is invisible.
	// A claim that fails un-claims the ones already taken, so a batch that cannot
	// go out whole leaves nothing half-started behind it.
	username := ""
	started := make([]uuid.UUID, 0, len(requestIDs))
	files := make([]sources.File, 0, len(requestIDs))
	for _, requestID := range requestIDs {
		row, startErr := service.store.StartDownloadRequest(ctx, requestID)
		if startErr != nil {
			service.revertAll(ctx, started, startErr)
			return startErr
		}
		// Claimed first and only then read, because a row that was claimed has to
		// be put back whatever is wrong with it. Deciding this before adding it to
		// the batch left the offending request started with nothing on its way.
		started = append(started, requestID)
		if username != "" && row.SourceUsername != username {
			service.revertAll(ctx, started, ErrNotOnePeer)
			return ErrNotOnePeer
		}
		username = row.SourceUsername
		for _, file := range row.Files {
			files = append(files, sources.File{Path: file.Path, SizeBytes: file.SizeBytes})
		}
	}

	startCtx, cancel := context.WithTimeout(ctx, startTimeout)
	defer cancel()
	if startErr := downloader.StartDownloads(startCtx, username, files); startErr != nil {
		service.revertAll(ctx, started, startErr)
		service.logger.Warn().Err(startErr).Str("username", username).
			Int("requests", len(started)).
			Msg("provider refused to start the requested transfers")
		return startErr
	}

	if err := service.store.QueueDownloadPoll(ctx, time.Now()); err != nil {
		service.logger.Error().Err(err).Msg("queue download poller")
	}
	service.readPeerMessages(ctx)
	service.logger.Info().
		Str("username", username).
		Int("requests", len(started)).
		Int("files", len(files)).
		Msg("download transfers started")
	service.announce()
	return nil
}

// revertAll returns every request of a batch that could not go out to the state
// it was in before, with the reason against it. The original context is used
// deliberately: a caller giving up must not leave rows claiming to be running.
func (service *Service) revertAll(ctx context.Context, requestIDs []uuid.UUID, cause error) {
	for _, requestID := range requestIDs {
		if _, err := service.store.RevertDownloadRequestStart(
			context.WithoutCancel(ctx), requestID, cause.Error()); err != nil {
			service.logger.Error().Err(err).Str("request_id", requestID.String()).
				Msg("could not revert a download request that failed to start")
		}
	}
}

// Retry asks the provider for the files of a settled request that did not
// arrive, and for nothing else. Naming no path retries every failed file.
//
// A retry is not a second request: the same row, the same provenance, and the
// same folder, with only the transfers that failed queued again. The files that
// did arrive are left exactly as they are, which is the whole point — asking a
// peer for eleven files again because one of them failed wastes the peer's
// upload and the user's time.
func (service *Service) Retry(
	ctx context.Context, requestID uuid.UUID, paths []string,
) (db.DownloadRequestRow, error) {
	downloader, err := service.downloader(ctx)
	if err != nil {
		return db.DownloadRequestRow{}, err
	}

	// The rows move first, for the same reason they do in Start: a transfer
	// running at the provider that Schall has no record of is invisible.
	row, retried, err := service.store.RetryDownloadRequestFiles(ctx, requestID, paths)
	if err != nil {
		return db.DownloadRequestRow{}, err
	}

	files := make([]sources.File, 0, len(retried))
	queued := make([]string, 0, len(retried))
	for _, file := range retried {
		files = append(files, sources.File{Path: file.Path, SizeBytes: file.SizeBytes})
		queued = append(queued, file.Path)
	}

	startCtx, cancel := context.WithTimeout(ctx, startTimeout)
	defer cancel()
	if startErr := downloader.StartDownloads(startCtx, row.SourceUsername, files); startErr != nil {
		reverted, revertErr := service.store.RevertDownloadRequestRetry(
			context.WithoutCancel(ctx), requestID, queued, startErr.Error())
		if revertErr != nil {
			service.logger.Error().Err(revertErr).Str("request_id", requestID.String()).
				Msg("could not revert a download retry that failed to start")
			return db.DownloadRequestRow{}, startErr
		}
		service.logger.Warn().Err(startErr).Str("request_id", requestID.String()).
			Msg("provider refused to retry the failed transfers")
		return reverted, startErr
	}

	if err := service.store.QueueDownloadPoll(ctx, time.Now()); err != nil {
		service.logger.Error().Err(err).Msg("queue download poller")
	}
	service.logger.Info().
		Str("request_id", requestID.String()).
		Str("username", row.SourceUsername).
		Int("files", len(files)).
		Msg("failed download transfers retried")
	service.announce()
	return row, nil
}

// CheckSource re-searches for a recorded folder and reports what the peer is
// offering now, without starting anything. It is what the interface asks before
// it offers a start button it knows will fail.
func (service *Service) CheckSource(ctx context.Context, requestID uuid.UUID) (SourceOffer, error) {
	row, err := service.store.DownloadRequest(ctx, requestID)
	if err != nil {
		return SourceOffer{}, err
	}
	provider, err := service.downloader(ctx)
	if err != nil {
		return SourceOffer{}, err
	}
	return service.offer(ctx, provider, row)
}

// offer re-runs the search that produced this candidate and compares what the
// peer offers now against what was recorded.
//
// It deliberately re-searches rather than browsing the peer's shares directly.
// Search is the one response shape that has been confirmed against a real
// slskd; a browse endpoint would be a new assumption written on both sides of
// a stub, which is exactly the class of mistake the connection check already
// made once.
func (service *Service) offer(
	ctx context.Context, provider sources.Provider, row db.DownloadRequestRow,
) (SourceOffer, error) {
	searchCtx, cancel := context.WithTimeout(ctx, recheckTimeout)
	defer cancel()

	// The same conservative phrase sequence the original search used, so a
	// release whose title carries an EP or Single label is still findable. A
	// request serving a want has no release, and the words it was asked for in are
	// what found the copy in the first place.
	artist, title := row.ArtistName, row.AlbumTitle
	if strings.TrimSpace(title) == "" {
		artist, title = row.EntryArtist, row.EntryTitle
	}
	var candidates []sources.Candidate
	for _, text := range sources.CatalogueQueryTexts(artist, title) {
		if text == "" {
			continue
		}
		found, err := provider.Search(searchCtx, sources.Query{
			Text: text, ExpectedTrackCount: int(row.ExpectedTrackCount),
		})
		if err != nil {
			return SourceOffer{}, err
		}
		if len(found) > 0 {
			candidates = found
			break
		}
	}

	offered := make(map[string]int64)
	for _, candidate := range candidates {
		if candidate.Username != row.SourceUsername || candidate.Directory != row.SourceDirectory {
			continue
		}
		for _, file := range candidate.Files {
			offered[file.Path] = file.SizeBytes
		}
	}
	// Nothing from this peer says nothing about the folder. An offline peer
	// returns no results, and slskd will queue the transfer until it is back.
	if len(offered) == 0 {
		return SourceOffer{}, nil
	}

	result := SourceOffer{Found: true}
	for _, file := range row.Files {
		size, still := offered[file.Path]
		switch {
		case !still:
			result.Missing = append(result.Missing, file.Name)
		// A size of zero on either side is silence rather than disagreement:
		// not every peer reports one, and an absent size proves nothing.
		case file.SizeBytes > 0 && size > 0 && size != file.SizeBytes:
			result.Changed = append(result.Changed, file.Name)
		}
	}
	return result, nil
}

// summary says in one sentence what the peer no longer offers.
func (offer SourceOffer) summary() string {
	parts := make([]string, 0, 2)
	if count := len(offer.Missing); count > 0 {
		parts = append(parts, fmt.Sprintf("%d no longer offered (%s)",
			count, strings.Join(offer.Missing, ", ")))
	}
	if count := len(offer.Changed); count > 0 {
		parts = append(parts, fmt.Sprintf("%d offered at a different size (%s)",
			count, strings.Join(offer.Changed, ", ")))
	}
	return strings.Join(parts, "; ")
}

// Cancel withdraws a request. Transfers that were already running are stopped
// at the provider first, so a cancelled request cannot leave slskd downloading
// music nobody asked for any more.
func (service *Service) Cancel(ctx context.Context, requestID uuid.UUID) (db.DownloadRequestRow, error) {
	row, err := service.store.DownloadRequest(ctx, requestID)
	if err != nil {
		return db.DownloadRequestRow{}, err
	}
	if row.Status == "started" {
		if err := service.stopTransfers(ctx, row); err != nil {
			return db.DownloadRequestRow{}, err
		}
	}
	withdrawn, err := service.store.CancelDownloadRequest(ctx, requestID)
	if err != nil {
		return db.DownloadRequestRow{}, err
	}
	service.announce()
	return withdrawn, nil
}

func (service *Service) stopTransfers(ctx context.Context, row db.DownloadRequestRow) error {
	transferIDs, err := service.store.RequestedTransferIDs(ctx, row.ID)
	if err != nil {
		return err
	}
	if len(transferIDs) == 0 {
		return nil
	}
	downloader, err := service.downloader(ctx)
	if errors.Is(err, ErrNotSupported) || errors.Is(err, sources.ErrNotConfigured) {
		// Nothing can be stopped through a provider that is gone. Withdrawing
		// the request is still the right outcome, and is what the caller does.
		service.logger.Warn().Str("request_id", row.ID.String()).
			Msg("cancelling a started request without a provider to stop it at")
		return nil
	}
	if err != nil {
		return err
	}

	stopCtx, cancel := context.WithTimeout(ctx, startTimeout)
	defer cancel()
	for _, transferID := range transferIDs {
		if err := downloader.CancelDownload(stopCtx, row.SourceUsername, transferID); err != nil {
			return fmt.Errorf("stop transfer %s: %w", transferID, err)
		}
	}
	return nil
}

// Poll asks the provider what it is doing with every started request and
// records it. It reports whether anything is still moving, so the worker knows
// whether to look again; once nothing is, polling stops entirely.
func (service *Service) Poll(ctx context.Context) (bool, error) {
	requests, err := service.store.StartedDownloadRequests(ctx)
	if err != nil {
		return false, err
	}
	if len(requests) == 0 {
		return false, nil
	}
	downloader, err := service.downloader(ctx)
	if err != nil {
		return false, err
	}

	// One provider call per peer, however many requests that peer covers.
	byPeer := make(map[string][]db.DownloadRequestRow, len(requests))
	for _, request := range requests {
		byPeer[request.SourceUsername] = append(byPeer[request.SourceUsername], request)
	}

	active, held := false, false
	for username, peerRequests := range byPeer {
		pollCtx, cancel := context.WithTimeout(ctx, pollTimeout)
		transfers, err := downloader.Transfers(pollCtx, username)
		cancel()
		if err != nil {
			// One unreachable peer must not stop the others from being followed.
			service.logger.Warn().Err(err).Str("username", username).
				Msg("could not read transfer progress")
			active = true
			continue
		}

		byPath := make(map[string]sources.Transfer, len(transfers))
		for _, transfer := range transfers {
			byPath[transfer.Path] = transfer
		}
		for _, request := range peerRequests {
			updated, err := service.record(ctx, request, byPath)
			if err != nil {
				return active, err
			}
			if updated.Status == "started" {
				active = true
			}
			if updated.Status == "started" || updated.Status == "failed" {
				// Either the peer is holding the files or it refused them, and
				// both are what a human check looks like from here.
				held = true
			}
		}
	}
	if held {
		service.readPeerMessages(ctx)
	}
	// One announcement for the whole pass, not one per request. Every recorded
	// pass is announced rather than only the ones that changed status — bytes
	// moving is what the progress bar is showing — but announcing per request
	// meant six open downloads invalidated the same view six times every five
	// seconds, and the view fetched once for each.
	service.announce()
	return active, nil
}

// record stores the progress of one request. A requested file the provider does
// not mention is left alone rather than assumed lost: slskd forgets a transfer
// some time after it finishes, and a forgotten transfer is not a failed one.
func (service *Service) record(ctx context.Context, request db.DownloadRequestRow, byPath map[string]sources.Transfer) (db.DownloadRequestRow, error) {
	progress := make([]db.TransferProgress, 0, len(request.Files))
	deferred, refused := 0, 0
	for _, file := range request.Files {
		transfer, ok := byPath[file.Path]
		if !ok {
			continue
		}
		if transfer.State == sources.TransferFailed {
			if transfer.Deferred {
				deferred++
			} else {
				refused++
			}
		}
		progress = append(progress, db.TransferProgress{
			ProviderID: transfer.ID, Path: transfer.Path, State: transfer.State,
			Detail: transfer.Detail, SizeBytes: transfer.SizeBytes,
			TransferredBytes: transfer.TransferredBytes,
		})
	}
	// A request that failed only because the peer would not take it now is not a
	// want's copy going wrong. It is recorded as the backpressure it is, so the
	// want comes back to this peer in minutes rather than writing the copy off
	// for a day and reporting that nobody is sharing the music.
	//
	// It is written before the request settles, and that order is the whole
	// safety of it. Interrupted here the request is still 'started', so the next
	// poll sees the same rejection and says the same thing again — the write only
	// touches a copy still marked as being fetched, so saying it twice costs
	// nothing. The other order loses the distinction for good: the request would
	// be failed, and the next sweep would release the copy as an ordinary one
	// that did not arrive before anybody could say why.
	//
	// A want is pursued one file at a time, which is what lets one deferred file
	// stand for the whole request. A release chosen by hand has no want behind it
	// and is not read this way at all.
	if request.AcquisitionTargetID.Valid && deferred > 0 && refused == 0 {
		service.logger.Info().
			Str("request_id", request.ID.String()).
			Str("username", request.SourceUsername).
			Int("files", deferred).
			Msg("a peer would not take a copy of a wanted recording just now")
		if err := service.store.DeferAcquiredFetch(ctx, request.ID, peerBusySummary); err != nil {
			// The copy settles either way; failing here costs the want the shorter
			// rest and nothing else, so it must not stop the pass.
			service.logger.Error().Err(err).Str("request_id", request.ID.String()).
				Msg("record that a peer would not take a copy just now")
		}
	}
	updated, err := service.store.RecordTransferProgress(ctx, request.ID, progress)
	if err != nil {
		return db.DownloadRequestRow{}, err
	}
	if updated.Status != request.Status {
		service.logger.Info().
			Str("request_id", request.ID.String()).
			Str("status", updated.Status).
			Int32("completed", updated.CompletedCount).
			Int32("failed", updated.FailedCount).
			Msg("download request settled")
		// A transfer that fails outright is done for, the same as a copy that
		// is refused or proven: there is nothing left to wait inFlightDelay
		// for. Without this the want would sit out the rest meant for a copy
		// that might still arrive.
		if updated.Status == "failed" && updated.AcquisitionTargetID.Valid {
			if err := service.store.RescheduleFailedTransfer(
				ctx, updated.AcquisitionTargetID.UUID, failedTransferSummary,
			); err != nil {
				service.logger.Error().Err(err).Str("request_id", request.ID.String()).
					Msg("reschedule a want after a failed transfer")
			}
		}
	}
	if service.imports && updated.Status == "completed" {
		if err := service.store.QueueDownloadImport(ctx, updated.ID); err != nil {
			return db.DownloadRequestRow{}, fmt.Errorf("queue download import: %w", err)
		}
	}
	return updated, nil
}

// PollInterval is how long the worker should wait before polling again while
// transfers are still moving.
func (service *Service) PollInterval() time.Duration { return pollInterval }

// peerChallengeDelay is how long the private chat of a peer is left before it
// is read. A peer that challenges sends its message with the refusal, so there
// is nothing to read the moment a request goes out, and the delay is what keeps
// a poll every few seconds from becoming a read every few seconds: the pass
// holds one live row, so a delay on the insert is a floor under how often it
// runs.
const peerChallengeDelay = time.Minute

// readPeerMessages asks for the private chat of the peers holding transfers to
// be read. Failing to queue it means a challenge is answered a minute later, by
// the next start or the next settled transfer, so it is logged rather than
// returned.
func (service *Service) readPeerMessages(ctx context.Context) {
	if err := service.store.QueuePeerChallenges(ctx, time.Now().Add(peerChallengeDelay)); err != nil {
		service.logger.Error().Err(err).Msg("queue the peer challenge reader")
	}
}

// downloader builds the configured provider and reports whether it can transfer
// at all.
func (service *Service) downloader(ctx context.Context) (sources.Downloader, error) {
	if service.providers == nil {
		return nil, sources.ErrNotConfigured
	}
	row, err := service.store.SlskdSettings(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, sources.ErrNotConfigured
	}
	if err != nil {
		return nil, err
	}
	if !row.Enabled {
		return nil, sources.ErrNotConfigured
	}
	provider, err := service.providers.Build(sources.Settings{
		BaseURL:       row.BaseURL,
		APIKey:        row.APIKey,
		SearchTimeout: time.Duration(row.SearchTimeoutSeconds) * time.Second,
	})
	if err != nil {
		return nil, err
	}
	downloader, ok := provider.(sources.Downloader)
	if !ok {
		return nil, ErrNotSupported
	}
	return downloader, nil
}
