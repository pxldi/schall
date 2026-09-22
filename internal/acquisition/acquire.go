package acquisition

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/library"
	"github.com/pxldi/schall/internal/sources"
	"github.com/pxldi/schall/internal/tagmatch"
)

// inFlightDelay is how long a want waits while a copy is on its way to it.
// Nothing is expected to happen at the end of it: the copy settling is what moves
// the want, and this is only how long the want is prepared to hear nothing before
// looking again — a transfer the provider silently forgot must not leave a want
// waiting forever.
const inFlightDelay = 30 * time.Minute

// throttleDelay is how long a want stands back when the provider refused to be
// asked at all. Short, because nothing about the music was learned and the
// refusal expires on its own; not immediate, because coming straight back is how
// a queue of wants turns one rate limiter into a busy loop.
const throttleDelay = 5 * time.Minute

// bannedRest is how long a want stands back when the search that should have
// gone out for it never left the machine.
//
// It is six times the wait above, and it has to be. The Soulseek server stops
// distributing this account's searches for thirty minutes at a time when it
// decides they are arriving too fast, and coming back every five minutes to ask
// again is the offence itself rather than a way out of it. Waiting it out costs
// the want nothing that matters: no attempt was spent, no absence was recorded,
// and it comes back exactly where it was.
const bannedRest = 30 * time.Minute

// importedDelay is how long a want waits for the scan to make a library file of
// the copy it has already been given. It is a backstop and not the mechanism:
// the scan finishing wakes the sweeper, and this is only how long the want is
// prepared to wait if that never happens.
const importedDelay = time.Minute

// importedPatience is how long that backstop is allowed to keep firing before
// the want stops calling it progress.
//
// A scan of a large library takes minutes, never hours, so anything past this is
// a copy whose library row is not coming rather than one that is late. It was an
// hour of AIFF, imported and unscannable, repeating "it is being taken into your
// library" once a minute all night that made the difference worth drawing.
const importedPatience = 2 * time.Hour

// undeliveredRest is how long a copy that failed to arrive rests before being
// asked for again. A failed transfer says nothing about the music, so the copy
// is never struck off — but retrying it ahead of everything else meant one
// broken copy was fetched twenty-five times in a day while untried copies sat
// below it, starved by rank. Every copy nobody has tried goes first; a copy
// that would not arrive today is asked about again tomorrow.
const undeliveredRest = 24 * time.Hour

// deferredRest is how long a copy the peer declined for load rests instead.
//
// A peer that answered "too many files" is sharing the music and would send it
// happily; what it objected to is how much of it we had queued at once. Resting
// that for a day would be reading backpressure as an absence, so it rests
// minutes — long enough for a queue to drain, and short enough that the want
// comes back to it well inside the half hour it waits after a copy settles.
//
// Every copy nobody has tried still goes first, so this is when the peer is
// worth asking again rather than when it is asked.
const deferredRest = 15 * time.Minute

// peerFileBudget is how many files Schall will have open at one peer at a time.
//
// It is a guess, and it has to be: a Soulseek client's per-user queue limit is
// never disclosed, and the two clients most people run — SoulseekQt and
// Nicotine+ — let their owner set it to anything. What is known is that the
// production failure had ten files of one folder open at one person and was
// refused, so the limit in the wild can be under ten. Five is chosen to sit well
// under that while still being worth batching for; it is a number to revise from
// what peers actually say, not a fact.
//
// The bound is read from what is open before a pass fetches, and the sweep's
// workers read it without coordinating, so it is a bound and not a guarantee: in
// the worst case every one of huntWorkers wants converges on the same peer's
// folder in the same pass and fills its own budget, which is huntWorkers times
// this number. That is a narrower condition than the failure it replaces — that
// one needed no convergence at all, only ten wants and one popular peer — but it
// is not "a few over", and a peer seen refusing after this lands is evidence for
// counting what is being asked for rather than what has already been asked.
const peerFileBudget = 5

// peerBusyDelay is how long a want stands back when every peer offering it is
// already being asked for as much as it will take. Nothing was learned about the
// music, so no attempt is spent; it is the same short wait a want takes when the
// provider itself refuses to be asked.
const peerBusyDelay = 5 * time.Minute

// queuePatience is how long a transfer may sit in a peer's queue, reported and
// never started, before the want gives up on it and looks elsewhere.
//
// Measured on the live instance 2026-08-29: 21 downloads sat in slskd's
// "Queued, Remotely" state across 12 peers, seven of them for 87.7 hours, every
// one with placeInQueue null — the peer took the request and never reported a
// position or sent a byte. Two hours matches importedPatience: a peer that has
// not begun sending in that long is not going to, and the owner's report that
// some peers gate a download behind a chat whitelist challenge is one way that
// silence happens.
const queuePatience = 2 * time.Hour

// Hunter looks for copies of one wanted recording. It is the same search a
// release run makes, asked for something that has no release.
// It also reports the preferences it applied. The search refuses whole folders;
// picking one file out of a folder happens here, and the bitrate floor has to be
// put to that file as well as to the folder's average.
type Hunter interface {
	SearchFor(
		ctx context.Context, query sources.Query, rungs []sources.Rung,
	) ([]sources.Candidate, []sources.Refused, sources.Preferences, string, error)
}

// WaivableHunter searches a want with its bitrate floors removed. Format
// refusals remain part of the returned answer.
type WaivableHunter interface {
	SearchForWithFloorWaived(
		ctx context.Context, query sources.Query, rungs []sources.Rung,
	) ([]sources.Candidate, []sources.Refused, sources.Preferences, string, error)
	StoredPreferences(context.Context) sources.Preferences
}

// Fetcher starts the transfer of a chosen copy. It is the path a release chosen
// by hand already takes: the same rows written before the provider is asked, the
// same re-check that the peer still offers what was recorded, and the same poller
// following it afterwards.
// StartTogether asks one peer for the copies of several wants in one call. A
// peer counts what one stranger has queued with it, so wants that all chose the
// same folder have to arrive as one question rather than as one each.
type Fetcher interface {
	StartTogether(ctx context.Context, requestIDs []uuid.UUID) error
	// Cancel withdraws a request, stopping its transfer at the provider first —
	// the same cancel a person pressing the button on the downloads screen gets —
	// so a want that gives up on a peer does not leave slskd still holding it.
	Cancel(ctx context.Context, requestID uuid.UUID) (db.DownloadRequestRow, error)
}

// Listener reports whether the audio of a fetched copy could be identified at
// all. Nothing else can admit a copy a stranger sent (ADR 0002), so an
// installation that cannot listen does not fetch: it would spend bandwidth and a
// peer's upload slot on files it could never accept.
type Listener interface {
	Configured(ctx context.Context) bool
}

// WithHunter registers what looks for copies. Without it a want waits: there is
// nothing to guess with, and a want that is never looked for is better than one
// satisfied by something nobody checked.
func (service *Service) WithHunter(hunter Hunter) *Service {
	service.hunter = hunter
	return service
}

// WithFetcher registers what starts the transfer of a chosen copy.
func (service *Service) WithFetcher(fetcher Fetcher) *Service {
	service.fetcher = fetcher
	return service
}

// WithListener registers what says whether audio can be identified at all.
func (service *Service) WithListener(listener Listener) *Service {
	service.listener = listener
	return service
}

// hunt is one look for one copy of one wanted recording.
//
// It fetches at most one copy per pass, and the next pass is scheduled by
// whatever that copy came to: refused, so try the next one now; proven, so settle
// the want; nothing on offer, so wait out the ladder. That is what keeps the loop
// a queue of bounded work rather than a job that downloads a folder's worth of
// wrong files in one go.
func (service *Service) hunt(
	ctx context.Context, target db.AcquisitionTargetRow, pass *passRefusal,
) error {
	stopped, err := service.store.StopAfterCreditOnlyRefusals(
		ctx, target.ID, creditOnlyRefusalsSummary,
	)
	if err != nil {
		return err
	}
	if stopped {
		service.logger.Info().
			Str("acquisition_target_id", target.ID.String()).
			Msg("stopped a want after every found copy disagreed only on artist credit")
		return nil
	}
	// An installation with nothing to look with leaves the want exactly as it was
	// before anything could look: wanted, and not in the library yet.
	if service.hunter == nil || service.fetcher == nil {
		return service.settleAttempt(ctx, target, "none", waitingSummary, waitingDetail)
	}
	// A listener, settled library witness, or recorded anchor can verify a copy.
	// Without all three, fetching would buy a file that could only ever be a
	// question.
	canVerify := service.listener != nil && service.listener.Configured(ctx)
	if !canVerify && target.MusicBrainzRecordingID.Valid {
		witnesses, err := service.store.SettledWitnesses(ctx, target.MusicBrainzRecordingID.UUID, 1)
		if err != nil {
			return err
		}
		canVerify = len(witnesses) > 0
	}
	if !canVerify {
		var err error
		canVerify, err = service.store.AcquisitionTargetHasAnchor(ctx, target.ID)
		if err != nil {
			return err
		}
	}
	if !canVerify {
		return service.settleAttempt(ctx, target, "failed", unlistenableSummary, unlistenableDetail)
	}

	// A copy the want started fetching and never heard the end of is freed first,
	// so a transfer that failed leaves the offer available rather than reserved
	// for a fetch that is not happening.
	if err := service.store.ReleaseStaleFetches(ctx, target.ID); err != nil {
		return err
	}

	// A copy already in flight is this want's turn taken. The request will settle
	// it, and starting a second transfer of the same music is what the one open
	// request per want exists to prevent. The want is still given a time to come
	// back at: a want that stayed permanently due would have the sweeper queueing
	// itself over and over, and the single worker would never reach the poller
	// that settles the transfer.
	open, err := service.store.OpenTargetRequest(ctx, target.ID)
	if err != nil {
		return err
	}
	if open.Valid {
		// A transfer that has sat in a peer's queue past queuePatience without
		// moving is not a copy on its way. The check reads when slskd was asked
		// to enqueue it, not when the want chose it, because choosing and
		// enqueuing can be minutes apart and it is the peer's silence since being
		// asked that is being measured.
		stalled, err := service.store.OpenTargetTransferQueuedPast(
			ctx, open.UUID, service.now().Add(-queuePatience))
		if err != nil {
			return err
		}
		if stalled {
			return service.abandonQueuedFetch(ctx, target, open.UUID)
		}
		return service.waitOut(ctx, target, fetchingSummary)
	}

	// A want holding a copy nobody has answered, with no anchor to answer it
	// with, fetches nothing more. It is asked after the copy in flight rather
	// than before, so a transfer already under way is left to settle; the want
	// comes straight back here when it does.
	//
	// Nothing is admitted or refused by this: the copies keep their verdicts and
	// the want keeps its place in the review queue. What it loses is the next
	// search. Answering the copies gives it back, and so does an anchor landing.
	waiting, err := service.store.StopWhileACopyWaits(ctx, target.ID, copyWaitingSummary)
	if err != nil {
		return err
	}
	if waiting {
		service.logger.Info().
			Str("acquisition_target_id", target.ID.String()).
			Msg("stopped a want holding a copy nobody has answered, with no anchor to answer it")
		return nil
	}

	wanted := sources.QueryTrack{Title: target.EntryTitle}
	if target.EntryDurationMS.Valid {
		wanted.DurationSeconds = int(target.EntryDurationMS.Int32) / 1000
	}
	// The words somebody asked for it in, which is all a want has: the recording
	// is what proves a copy afterwards, and Soulseek has never heard of it.
	rungs := sources.TrackQueryRungs(
		tagmatch.PrimaryCredit(target.EntryArtist), target.EntryTitle, target.EntryAlbum)
	if len(rungs) == 0 || rungs[0].Text == "" {
		return service.store.RequeueAcquisitionTarget(
			ctx, target.ID, "none", unsearchableSummary, unsearchableDetail, "",
			service.nextLook(target),
		)
	}

	// SearchFor derives the want's budget from this ladder and the provider's
	// complete per-rung allowance. It also refuses to begin a rung that cannot
	// finish inside what remains, so exhaustion stays "nothing on offer" rather
	// than becoming a provider timeout.
	search := service.hunter.SearchFor
	if target.FloorWaivedAt.Valid {
		if waived, ok := service.hunter.(WaivableHunter); ok {
			search = waived.SearchForWithFloorWaived
		}
	}
	candidates, refused, preferences, text, err := search(
		ctx, sources.Query{Tracks: []sources.QueryTrack{wanted}}, rungs)
	if err != nil {
		return service.recordSearchFailure(ctx, target, err, pass)
	}
	// A search that ran at all only happens logged in, so it is the one place
	// that gets to say the outage is over. Advisory: a settings-table write
	// that failed must not stop a copy this pass just found from being fetched.
	switch backOnline, err := service.store.ClearSourceLoggedOut(ctx); {
	case err != nil:
		service.logger.Warn().Err(err).
			Msg("could not clear when the download source last signed out")
	case backOnline:
		// Every want the outage stood back is sitting on a bannedRest clock of
		// its own, some of them close to thirty minutes from now. Waking the
		// sweeper here means they are looked at again as soon as the source can
		// answer, rather than one by one as each clock happens to run out.
		service.kickSweeper(ctx)
	}

	// Peers already being asked for as much as they will take are set aside
	// before anything is chosen. It is not a judgement about their copies — they
	// go back in the moment their queue drains — and a want left with nothing but
	// those has been told something quite different from nobody sharing it.
	openAtPeers, err := service.store.OpenFilesByPeer(ctx)
	if err != nil {
		return fmt.Errorf("count the files open at each peer: %w", err)
	}
	harvestPreferences := preferences
	if target.FloorWaivedAt.Valid {
		if stored, ok := service.hunter.(WaivableHunter); ok {
			harvestPreferences = stored.StoredPreferences(ctx)
		}
	}
	offer, found, heldBack, belowFloor := service.choose(
		ctx, target, candidates, wanted, openAtPeers, preferences)
	if !found {
		// A copy passed over only because its peer is busy is not an absence, so
		// the two are answered differently: the want stands back without spending
		// an attempt rather than recording that nobody is sharing the music.
		if heldBack {
			return service.deferForPeers(ctx, target)
		}
		// Nothing a peer shares can be fetched this round, so a keyed want takes
		// the track from its own address, once (ADR 0038 §6). The copy is then
		// on its way like any other.
		fetched, err := service.fetchFromSource(ctx, target)
		if err != nil {
			return err
		}
		if fetched {
			return service.waitOut(ctx, target, sourceFetchedSummary)
		}
		// Copies were on offer and the user's own format preference is what
		// turned every one of them away. Saying nobody is sharing it would be
		// untrue, and it would point at the network instead of at the one
		// setting that can change the answer.
		// The floor is answered before the format, and separately, because they
		// send the reader to different settings. A file under the floor was
		// turned away by a number the reader can lower; a format was turned away
		// by a list.
		if turnedAway, why := belowTheFloor(refused, belowFloor); turnedAway {
			return service.settleBelowFloor(ctx, target, preferences,
				fmt.Sprintf("%s Every copy offered for %q was under it: %s.",
					belowBitRateFloorDetail, text, why), append(refused, belowFloor...))
		}
		if len(candidates) == 0 && len(refused) > 0 {
			return service.settleAttempt(ctx, target, "none",
				refusedByPreferenceSummary,
				fmt.Sprintf("%s Every copy offered for %q was in a format your preferences refuse: %s.",
					refusedByPreferenceDetail, text, refused[0].Reason))
		}
		if target.BelowFloorSince.Valid &&
			len(disclosedOffers(sources.OffersFor(candidates, wanted))) == 0 && len(refused) == 0 {
			return service.requeueParked(ctx, target, nothingOfferedSummary,
				fmt.Sprintf("Nothing on offer for %q.", text))
		}
		return service.settleAttempt(ctx, target, "none",
			nothingOfferedSummary, fmt.Sprintf("Nothing on offer for %q.", text))
	}
	// Everything taken from this one folder goes out as one question. The want
	// that prompted the search leads it, so a batch nobody could start reports its
	// failure against the want whose turn this was, and what is left of the peer's
	// budget after this copy is what the rest of the folder may fill.
	spare := peerFileBudget - openAtPeers[peerOf(offer)] - 1
	return service.fetchTogether(ctx, append(
		[]chosen{{target: target, offer: offer}},
		service.harvest(ctx, target, offer, candidates, spare, harvestPreferences)...))
}

// chosen is one want and the copy picked for it, before anything is recorded.
type chosen struct {
	target db.AcquisitionTargetRow
	offer  sources.Offer
}

func peerOf(offer sources.Offer) db.Peer {
	return db.Peer{Provider: offer.Provider, Username: offer.Username}
}

// deferForPeers stands a want back because everyone offering it is busy. No
// attempt is spent: the music is plainly out there, and being asked to wait
// one's turn is not a search that came to nothing.
func (service *Service) deferForPeers(
	ctx context.Context, target db.AcquisitionTargetRow,
) error {
	service.logger.Info().
		Str("acquisition_target_id", target.ID.String()).
		Dur("deferred_for", peerBusyDelay).
		Msg("every peer offering a wanted recording is already being asked for enough")
	return service.store.DeferAcquisitionTarget(
		ctx, target.ID, peerBusySummary, peerBusyDetail,
		service.now().Add(peerBusyDelay),
	)
}

// fetchTogether records every chosen copy and asks their one peer for all of
// them in a single call.
//
// The records are written first and separately, one request per want, because
// that is what a want is: its own row, its own copy, its own answer. Only the
// asking is shared. A want whose record cannot be written is dropped from the
// batch rather than taking the others down — except the first, which is the want
// whose turn this is and whose failure is this attempt's failure.
//
// A want that stopped waiting for a copy while this pass was choosing one is
// dropped whichever place it holds in the batch, the first included. Somebody
// settled it, withdrew it or gave it a copy of its own during a search that
// takes minutes, and their decision is the current one: there is nothing here to
// report as a failure, and nothing left to fetch for.
func (service *Service) fetchTogether(ctx context.Context, wants []chosen) error {
	if len(wants) == 0 {
		return nil
	}
	// Whose turn this is. Which want that is has to be read from which one it is
	// rather than from where it stands in the batch, because the leading want can
	// be dropped out of it and a sibling standing first afterwards is still only
	// a sibling — whose failure is logged rather than made this attempt's.
	leading := wants[0].target.ID
	requestIDs := make([]uuid.UUID, 0, len(wants))
	recorded := make([]chosen, 0, len(wants))
	for _, want := range wants {
		requestID, err := service.record(ctx, want)
		if err != nil {
			if errors.Is(err, db.ErrWantHasMovedOn) || errors.Is(err, db.ErrWantHasAnotherCopy) {
				service.logger.Info().
					Str("acquisition_target_id", want.target.ID.String()).
					Msg("a wanted recording was answered while a copy was being chosen for it")
				continue
			}
			if want.target.ID == leading {
				return err
			}
			service.logger.Error().Err(err).
				Str("acquisition_target_id", want.target.ID.String()).
				Msg("record a sibling's copy from a folder already found")
			continue
		}
		requestIDs = append(requestIDs, requestID)
		recorded = append(recorded, want)
	}
	// Every want in the batch was answered elsewhere, so there is nobody to ask.
	if len(requestIDs) == 0 {
		return nil
	}

	if err := service.fetcher.StartTogether(ctx, requestIDs); err != nil {
		// Every want in the batch was asked in the same breath, so a refusal is
		// every one of their refusals. Each withdraws its own copy and carries on
		// with the next one, exactly as it would have alone.
		var first error
		for index, want := range recorded {
			if failed := service.abandonFetch(
				ctx, want.target, want.offer, requestIDs[index], err); failed != nil && first == nil {
				first = failed
			}
		}
		return first
	}

	for index, want := range recorded {
		service.logger.Info().
			Str("acquisition_target_id", want.target.ID.String()).
			Str("download_request_id", requestIDs[index].String()).
			Str("username", want.offer.Username).
			Str("file", want.offer.File.Name).
			Bool("named_and_timed", want.offer.Confirmed).
			Msg("fetching a copy of a wanted recording")
		if err := service.waitOut(ctx, want.target, fetchingSummary); err != nil {
			if want.target.ID == leading {
				return err
			}
			service.logger.Error().Err(err).
				Str("acquisition_target_id", want.target.ID.String()).
				Msg("give a sibling a time to come back at")
		}
	}
	return nil
}

// harvest reads the folder this want's copy came from for the wants that would
// have found the same folder.
//
// A peer shares a folder, not a track. Every copy fetched for a want on an EP
// arrived in a folder holding the rest of that EP, and every sibling want then
// went looking for it alone and was told nobody was sharing it -- the same
// network, minutes apart, answering the same question differently. Four EPs gave
// up exactly one track each that way while the other eleven sat in folders
// Schall had already been shown.
//
// It stays inside what the loop is: one file per want, never a folder downloaded
// wholesale. A sibling is only fetched for when the peer's own filename carries
// its title, which is the one thing disclosed about a track rather than about
// the folder around it. A file that fits nothing is left where it is.
//
// None of this decides anything. The filename is not evidence and never has
// been: each copy is still proven by its audio against the recording its own
// want names, and one that is something else is refused exactly as it would have
// been had that want fetched it itself. What changes is only which folders get
// looked in.
//
// A failure here is logged and swallowed. The want that prompted it has its copy
// on the way, and a sibling that misses its turn is asked again on its own.
//
// A sibling that already has its music is not here to be asked about. The list
// this reads leaves out any want with a copy in flight, being validated, or
// imported — the same test the want's own turn applies before it searches, and
// the same one the review queue applies before it asks about copies. So the
// invariant "one want, one copy" is enforced once for this path, in the query
// that chooses who is in the folder's reach at all, rather than twice.
//
// Every sibling is asked about in the same order a want's own turn asks: the
// library first, the folder only after. A sibling the library already holds is
// settled against the copy it holds and taken no further, because a folder open
// in front of the loop reaches wants that were never going to search today, and
// the second copy of a recording already on the disc is the one outcome nobody
// wants.
//
// What it collects is asked for in one call together with the copy that prompted
// it, which is the other half of why reading the folder is worth doing: one
// person is asked one question for the whole EP instead of being asked ten
// times and refusing on the ninth. That is also why the batch is bounded — the
// peer's limit is theirs to set and Schall cannot see it, so a folder holding
// forty tracks is asked about a few at a time and the rest come round again.
func (service *Service) harvest(
	ctx context.Context,
	target db.AcquisitionTargetRow,
	taken sources.Offer,
	candidates []sources.Candidate,
	spare int,
	preferences sources.Preferences,
) []chosen {
	if !target.MusicBrainzReleaseGroupID.Valid || spare <= 0 {
		return nil
	}
	siblings, err := service.store.WantsSharingAReleaseWith(ctx, target.ID)
	if err != nil {
		service.logger.Error().Err(err).
			Str("acquisition_target_id", target.ID.String()).
			Msg("read the wants sharing a release")
		return nil
	}
	taking := make([]chosen, 0, len(siblings))
	for _, sibling := range siblings {
		if len(taking) >= spare {
			break
		}
		// The library is asked about a sibling before anything is picked for it,
		// exactly as it is asked about the want whose turn this is. An open folder
		// is where a second copy of music already on the disc is easiest to take by
		// accident, and the sibling's own turn asking later asks too late. A want
		// answered this way is finished here rather than merely passed over: what
		// the library holds is what the queue should say.
		held, err := service.heldAlready(ctx, sibling)
		if err != nil {
			// Not knowing whether the library has it is not a reason to fetch it.
			service.logger.Error().Err(err).
				Str("acquisition_target_id", sibling.ID.String()).
				Msg("ask the library about a want sharing a release")
			continue
		}
		if held {
			continue
		}
		wanted := sources.QueryTrack{Title: sibling.EntryTitle}
		if sibling.EntryDurationMS.Valid {
			wanted.DurationSeconds = int(sibling.EntryDurationMS.Int32) / 1000
		}
		offer, found := service.chooseFrom(ctx, sibling, candidates, wanted, taken, preferences)
		if !found {
			continue
		}
		taking = append(taking, chosen{target: sibling, offer: offer})
	}
	return taking
}

// chooseFrom picks a sibling's copy out of the folder another want's copy came
// from, and only out of that folder: a candidate somewhere else was not the thing
// this search found, and reaching for it would be answering a question nobody
// asked on a turn that belongs to somebody else.
func (service *Service) chooseFrom(
	ctx context.Context,
	sibling db.AcquisitionTargetRow,
	candidates []sources.Candidate,
	wanted sources.QueryTrack,
	taken sources.Offer,
	preferences sources.Preferences,
) (sources.Offer, bool) {
	verdicts, ok := service.verdictsFor(ctx, sibling.ID)
	if !ok {
		return sources.Offer{}, false
	}
	// A file taken along for a neighbouring want is held to the same floor as
	// the one that prompted the search. Nothing is fetched under it, whoever it
	// is for.
	offers, _ := acceptableOffers(sources.OffersFor(candidates, wanted), preferences)
	return verdicts.pick(service.now(), disclosedOffers(offers),
		func(offer sources.Offer) bool {
			return offer.Named &&
				offer.Provider == taken.Provider && offer.Username == taken.Username &&
				offer.Directory == taken.Directory && offer.File.Path != taken.File.Path
		})
}

// choose picks the copy worth fetching first among the ones nothing has been
// decided about yet.
//
// The order is reading order and never a claim: what a peer discloses is a
// filename and a length, and neither identifies anything. What rules a copy out
// is what has already been decided about it — the audio was something else, its
// own identifiers contradicted, or somebody looked and said no — and a copy that
// merely failed to arrive is not ruled out, because that says nothing about
// whether this peer has the right music.
// A peer already being asked for as many files as it will take is passed over
// too, and that is not a judgement either: past their limit a Soulseek client
// refuses everything, and a refusal costs both sides the transfer and tells
// nobody anything. It is reported separately from finding nothing, because a
// want whose only copies are behind a queue has been told something quite
// different from a want nobody is sharing.
// A file under the user's bitrate floor is passed over too, and that is a
// judgement — about the copy's quality, never about what music it holds. The
// folder was already held to the floor by its average bitrate, and an average is
// not the file: a folder averaging 320 kbps can hold one track at 128. The want
// asks for one file, so the file it asks for is the one measured. What the floor
// turned away is reported, because "nobody is sharing this" and "nobody is
// sharing this above the bitrate you set" are different answers and only the
// second one has a setting behind it.
func (service *Service) choose(
	ctx context.Context,
	target db.AcquisitionTargetRow,
	candidates []sources.Candidate,
	wanted sources.QueryTrack,
	open map[db.Peer]int,
	preferences sources.Preferences,
) (sources.Offer, bool, bool, []sources.Refused) {
	verdicts, ok := service.verdictsFor(ctx, target.ID)
	if !ok {
		return sources.Offer{}, false, false, nil
	}
	// The floor is held only against files this pass would otherwise fetch.
	// A file nothing disclosed fits is not a copy the floor turned away, and
	// counting it would tell the reader to lower a number that would change
	// nothing.
	offers, belowFloor := acceptableOffers(
		disclosedOffers(sources.OffersFor(candidates, wanted)), preferences)
	if offer, found := verdicts.pick(service.now(), offers, func(offer sources.Offer) bool {
		return open[peerOf(offer)] < peerFileBudget
	}); found {
		return offer, true, false, belowFloor
	}
	// Nothing was worth fetching. Whether a busy peer is why is the difference
	// between waiting one's turn and nobody having the music, so it is asked
	// rather than assumed: the same pick again, with the budget set aside.
	_, elsewhere := verdicts.pick(service.now(), offers,
		func(sources.Offer) bool { return true })
	return sources.Offer{}, false, elsewhere, belowFloor
}

// acceptableOffers takes the files under the user's bitrate floor out of the
// reading order, and reports them.
func acceptableOffers(
	offers []sources.Offer, preferences sources.Preferences,
) ([]sources.Offer, []sources.Refused) {
	if !preferences.Stated() {
		return offers, nil
	}
	kept := make([]sources.Offer, 0, len(offers))
	var refused []sources.Refused
	for _, offer := range offers {
		reason, kind := preferences.RefuseFile(offer.File.Extension, offer.File.BitRate)
		if reason == "" {
			kept = append(kept, offer)
			continue
		}
		refused = append(refused, sources.Refused{
			Username: offer.Username, Directory: offer.Directory,
			Format: offer.File.Extension, Reason: reason, Kind: kind,
		})
	}
	return kept, refused
}

// disclosedOffers takes out the offers whose peer disclosed nothing that fits
// the want: no title token in the filename, no duration within tolerance, or a
// disclosed duration that contradicts the title.
// Fetching blind on that tier produced zero imports in 3,024 of 18,200
// requests audited on 2026-09-04, so this pass never spends a peer's upload
// slot there. `OffersFor` still returns the offer, last, so nothing here
// changes what a person browsing candidates by hand would see — only the
// automatic pick drops it. If every candidate sits in this tier, the want
// ends the pass the same way it does when there were no candidates at all.
// This changes what gets fetched first, never what gets admitted: the audio
// proof after download is unchanged.
func disclosedOffers(offers []sources.Offer) []sources.Offer {
	kept := make([]sources.Offer, 0, len(offers))
	for _, offer := range offers {
		if offer.Confirmed || offer.Partial {
			kept = append(kept, offer)
		}
	}
	return kept
}

// belowTheFloor reports whether the bitrate floor is the whole reason this want
// came away with nothing, and names one copy it turned away.
//
// It has to be the whole reason. A want that was also offered a copy in a format
// the user refuses is told about the format list instead, because two settings
// stand between it and a copy and naming one of them would send the reader to
// change a number that would not be enough on its own.
func belowTheFloor(searchRefused, fileRefused []sources.Refused) (bool, string) {
	all := append(append([]sources.Refused{}, searchRefused...), fileRefused...)
	if len(all) == 0 {
		return false, ""
	}
	for _, one := range all {
		if one.Kind != sources.RefusedBitRate {
			return false, ""
		}
	}
	return true, all[0].Reason
}

// offerVerdicts is what has already been decided about a want's offers: struck
// off for good, or tried and never delivered.
//
// A copy that did not arrive is held by when it is worth asking about again
// rather than by when it was tried, because how long that is depends on what
// happened. A transfer that went wrong says the copy may be bad; a peer that
// said "not now" says only that it was busy, and the two must not rest alike.
//
// restedPeers holds the coarser fact a queue expiry is actually about: a peer
// that took a request into its queue and went quiet may serve the same
// recording under a second path — a different folder, a compilation, an
// alternate rip — and offering that path is not a different answer. Every
// other undelivered verdict stays offer-scoped, because it says something
// about one file, not about the peer.
type offerVerdicts struct {
	settled     map[string]bool
	rested      map[string]time.Time
	restedPeers map[db.Peer]time.Time
}

func (service *Service) verdictsFor(
	ctx context.Context, targetID uuid.UUID,
) (offerVerdicts, bool) {
	judged, err := service.store.AcquisitionTargetFiles(ctx, targetID)
	if err != nil {
		service.logger.Error().Err(err).
			Str("acquisition_target_id", targetID.String()).
			Msg("read what has already been tried for a want")
		return offerVerdicts{}, false
	}
	verdicts := offerVerdicts{
		settled:     make(map[string]bool, len(judged)),
		rested:      make(map[string]time.Time, len(judged)),
		restedPeers: make(map[db.Peer]time.Time),
	}
	for _, file := range judged {
		key := offerKey(file.Provider, file.SourceUsername, file.RemotePath)
		if file.Verdict == db.AcquiredFileUndelivered {
			ready := file.DecidedAt.Add(restFor(file))
			if ready.After(verdicts.rested[key]) {
				verdicts.rested[key] = ready
			}
			if file.Summary == queueExpiredFileSummary {
				peer := db.Peer{Provider: file.Provider, Username: file.SourceUsername}
				if ready.After(verdicts.restedPeers[peer]) {
					verdicts.restedPeers[peer] = ready
				}
			}
			continue
		}
		verdicts.settled[key] = true
	}
	return verdicts, true
}

// restFor is how long one copy that did not arrive waits before it is worth
// asking about again.
//
// The sentence is what tells the two apart, and it is the one the transfer layer
// wrote when the peer declined for load rather than refused. Reading a summary
// is doing real work here rather than displaying it, which is why the sentence
// itself is a constant in one place: a copy nobody sent because the peer was
// busy is not a copy that failed to arrive.
func restFor(file db.AcquisitionTargetFileRow) time.Duration {
	if file.Summary == db.DeferredByPeerSummary {
		return deferredRest
	}
	return undeliveredRest
}

// pick takes the first offer worth fetching, in disclosed order among the
// copies nobody has tried, and only then a copy that failed to arrive and has
// rested. keep is the caller's own bounds — the harvest's one-folder rule —
// and never a judgement about a copy.
//
// A file the library could not hold is passed over before any of that. It is not
// a judgement about the copy either: the audio may well be the wanted recording,
// and this says only that a scan would never index it, so fetching it would spend
// a peer's upload slot on music that would sit in the library directory unseen —
// which is exactly what happened to an AIFF that was proven and imported and then
// waited all night for a library row nothing would create.
func (verdicts offerVerdicts) pick(
	now time.Time, offers []sources.Offer, keep func(sources.Offer) bool,
) (sources.Offer, bool) {
	for _, offer := range offers {
		key := offerKey(offer.Provider, offer.Username, offer.File.Path)
		if !holdable(offer) || !keep(offer) || verdicts.settled[key] || verdicts.peerResting(now, offer) {
			continue
		}
		if _, tried := verdicts.rested[key]; tried {
			continue
		}
		return offer, true
	}
	for _, offer := range offers {
		key := offerKey(offer.Provider, offer.Username, offer.File.Path)
		if !holdable(offer) || !keep(offer) || verdicts.settled[key] || verdicts.peerResting(now, offer) {
			continue
		}
		if ready, tried := verdicts.rested[key]; tried && !now.Before(ready) {
			return offer, true
		}
	}
	return sources.Offer{}, false
}

// peerResting reports whether this offer's peer is still inside the rest a
// queue expiry earned it — asked again elsewhere, and asked again by nothing
// this want does until the rest is up.
func (verdicts offerVerdicts) peerResting(now time.Time, offer sources.Offer) bool {
	ready, resting := verdicts.restedPeers[db.Peer{Provider: offer.Provider, Username: offer.Username}]
	return resting && now.Before(ready)
}

func offerKey(provider, username, path string) string {
	return strings.Join([]string{provider, username, path}, "\x00")
}

// holdable reports whether the library could hold what a peer is offering. The
// name a peer advertised is all there is to go on, and it is the same question a
// scan asks of a path on disk, so it is asked of the same list.
func holdable(offer sources.Offer) bool {
	name := offer.File.Name
	if name == "" {
		name = offer.File.Path
	}
	return library.CanHold(name)
}

// record writes down the copy one want has chosen and the request that will
// fetch it, and answers with that request.
//
// It is the want's own row either way: one open request per want, whether that
// request is asked for on its own or in the same breath as a neighbour's. The
// offer is not re-checked before it goes out, because it was searched for a
// moment ago and a start acts on one file rather than on a folder somebody chose
// weeks earlier — the case the re-check exists for. A peer that has withdrawn it
// since fails the transfer, and that is recorded as the transport fact it is.
func (service *Service) record(
	ctx context.Context, want chosen,
) (uuid.UUID, error) {
	return service.store.FetchAcquiredFile(ctx, db.FetchAcquiredFileParams{
		AcquisitionTargetID: want.target.ID,
		Provider:            want.offer.Provider,
		SourceUsername:      want.offer.Username,
		SourceDirectory:     want.offer.Directory,
		RemotePath:          want.offer.File.Path,
		FileName:            want.offer.File.Name,
		SizeBytes:           want.offer.File.SizeBytes,
		BitRate:             want.offer.File.BitRate,
		DurationSeconds:     want.offer.File.DurationSeconds,
		Extension:           want.offer.File.Extension,
		Score:               want.offer.Score,
		Reasons:             []string{want.offer.Summary()},
		Summary:             "Being fetched. " + want.offer.Summary() + ".",
	})
}

// waitOut gives a want whose turn is taken a time to be looked at again. It
// counts no attempt and records none: nothing was attempted, a copy is simply on
// its way, and whatever that copy comes to is what will move the want next.
//
// The wait only matters when nothing does come of it — a copy that is refused or
// proven reschedules the want itself, immediately.
func (service *Service) waitOut(
	ctx context.Context, target db.AcquisitionTargetRow, summary string,
) error {
	err := service.store.RescheduleAcquisitionTarget(
		ctx, target.ID, summary, service.now().Add(inFlightDelay))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	return err
}

// abandonFetch withdraws a copy the provider would not start. Nothing was
// learned about the music, so the offer stays available another day and the want
// carries on with the next one.
func (service *Service) abandonFetch(
	ctx context.Context,
	target db.AcquisitionTargetRow,
	offer sources.Offer,
	requestID uuid.UUID,
	cause error,
) error {
	service.logger.Warn().Err(cause).
		Str("acquisition_target_id", target.ID.String()).
		Str("download_request_id", requestID.String()).
		Msg("a chosen copy could not be started")
	if _, err := service.store.CancelDownloadRequest(ctx, requestID); err != nil {
		service.logger.Error().Err(err).Str("download_request_id", requestID.String()).
			Msg("withdraw a copy that could not be started")
	}
	if err := service.store.RecordAcquiredFile(ctx, db.RecordAcquiredFileParams{
		AcquisitionTargetID: target.ID,
		Provider:            offer.Provider,
		SourceUsername:      offer.Username,
		RemotePath:          offer.File.Path,
		FileName:            offer.File.Name,
		SizeBytes:           offer.File.SizeBytes,
		Verdict:             db.AcquiredFileUndelivered,
		Summary:             "The transfer could not be started: " + cause.Error(),
	}); err != nil {
		return err
	}
	// On the ladder rather than straight away. The copy is not struck off — a
	// start nobody could make says nothing about the music — so trying again now
	// would pick the same offer and fail on it again, forever.
	return service.settleAttempt(ctx, target, "failed", waitingSummary, cause.Error())
}

// abandonQueuedFetch withdraws a copy a peer accepted into its queue and then
// said nothing about for longer than queuePatience. Cancel stops the transfer
// at the provider before the request is marked withdrawn, exactly as a person
// cancelling one from the downloads screen does.
//
// The offer is recorded undelivered rather than struck off — the peer may be
// slow, or gating the transfer behind something Schall cannot see, such as a
// chat whitelist challenge — so it rests and is tried again another day
// instead of being ruled out for good. It counts as one attempt: unlike a
// provider that refused to be asked at all, this peer answered the question,
// and going quiet after accepting is a fact about it worth spending the
// want's turn on.
func (service *Service) abandonQueuedFetch(
	ctx context.Context, target db.AcquisitionTargetRow, requestID uuid.UUID,
) error {
	withdrawn, err := service.fetcher.Cancel(ctx, requestID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Settled or moved on while patience was being checked — there is
			// nothing left here to give up on.
			return nil
		}
		return err
	}
	for _, file := range withdrawn.Files {
		if err := service.store.RecordAcquiredFile(ctx, db.RecordAcquiredFileParams{
			AcquisitionTargetID: target.ID,
			Provider:            withdrawn.Provider,
			SourceUsername:      withdrawn.SourceUsername,
			RemotePath:          file.Path,
			FileName:            file.Name,
			SizeBytes:           file.SizeBytes,
			Verdict:             db.AcquiredFileUndelivered,
			Summary:             queueExpiredFileSummary,
		}); err != nil {
			return err
		}
	}
	service.logger.Info().
		Str("acquisition_target_id", target.ID.String()).
		Str("download_request_id", requestID.String()).
		Str("username", withdrawn.SourceUsername).
		Dur("queued_for", queuePatience).
		Msg("a copy queued at a peer past patience was withdrawn")
	return service.settleAttempt(ctx, target, "failed", queueExpiredSummary, queueExpiredDetail)
}

// refusal is what a provider that would not search said, in the words the want
// that heard it — and often the rest of the pass — will wear.
//
// Five things produce one: being asked too often, a provider that cannot ask
// anybody anything at the moment, a provider that was signed out of Soulseek
// while it searched, a search that was made and never went out, and one phrase
// the provider would not repeat this soon. What separates them is how long has
// to pass before asking again works, and whether the refusal was about the
// source or about the one want that asked, so a refusal carries both.
type refusal struct {
	summary string
	// detail is the sentence a want that only stood back wears. A refusal about
	// one want's own words has none, because it is never passed on.
	detail string
	// delay is how long a want wearing this refusal stands back for.
	delay time.Duration
	// aboutOnePhrase says the provider refused these words rather than refused
	// to search. Every other want in the pass asks in words of its own and the
	// source is still answering, so nothing else stands back for it.
	aboutOnePhrase bool
}

// passRefusal is the one refusal a pass remembers.
//
// A provider that will not search is not refusing this want, it is refusing the
// question, so one want hears it and every want behind it stands back on that
// sentence rather than queueing up to be told the same thing.
//
// Which one is kept is decided by how long it asks for, longest first. The
// refusals do not all mean the same wait: being rate limited is five minutes,
// and a search that never went out is half an hour, because the thing that
// stops a search going out is a thirty-minute ban. A pass that heard both and
// kept the first would come back in five minutes and ask straight into the ban
// it had just been shown, which is the offence itself. Keeping the longest is
// the only reading that cannot do that, and it costs the shorter refusal
// nothing but a delay it was never harmed by.
//
// Ties go to the first, which is the one that was actually heard from the
// provider; a later want's copy of the same refusal would say the same thing
// with less claim to it.
type passRefusal struct {
	heard atomic.Pointer[refusal]
}

// record keeps this refusal if the pass has heard none, or if this one asks the
// wants to stand back longer than what it has. The loop is a loop because two
// wants can be refused at the same moment on different workers, and the reader
// must never see a shorter wait than the longest that was heard.
func (pass *passRefusal) record(heard refusal) {
	if pass == nil {
		return
	}
	for {
		standing := pass.heard.Load()
		if standing != nil && standing.delay >= heard.delay {
			return
		}
		if pass.heard.CompareAndSwap(standing, &heard) {
			return
		}
	}
}

// refused reports the refusal this pass has already heard, if it heard one. A
// nil pass has heard nothing, which is what lets one want be looked for on its
// own without inventing a pass to hold it.
func (pass *passRefusal) refused() (refusal, bool) {
	if pass == nil {
		return refusal{}, false
	}
	if heard := pass.heard.Load(); heard != nil {
		return *heard, true
	}
	return refusal{}, false
}

// recordSearchFailure writes down that the provider could not be asked.
//
// A refusal is backpressure rather than an answer, so it costs the want
// nothing: no attempt is spent and the ladder does not move, only the time it
// comes back at. Spending an attempt on it would mean a press big enough to be
// refused punished itself for being made — the wants refused first would climb
// fastest towards the last rung, having never once been looked for. It is
// logged, because a want that keeps being deferred is a provider that is being
// asked for more than it will give, and that is worth seeing.
//
// An unreachable provider is different: nobody said no, the question went
// nowhere, and there is nothing to come straight back for. That stays on the
// ladder.
func (service *Service) recordSearchFailure(
	ctx context.Context, target db.AcquisitionTargetRow, cause error, pass *passRefusal,
) error {
	if heard, refused := refusalFor(cause); refused {
		// A refusal about the source is heard by the rest of the pass too, so a
		// queue of wants learns it once rather than once per want. A refusal
		// about this want's own words is not: the source is still searching,
		// every other want in the pass asks in different words, and standing
		// them all back was costing ten wants of one release five passes and
		// twenty-five minutes before any of them was asked.
		if !heard.aboutOnePhrase {
			pass.record(heard)
		}
		event := service.logger.Info().Err(cause).
			Str("acquisition_target_id", target.ID.String()).
			Dur("deferred_for", heard.delay).
			Bool("about_one_phrase", heard.aboutOnePhrase)
		// Being signed out of Soulseek is the one refusal worth a running
		// total: it is the fact that sat for two hours with four hundred
		// identical lines and nothing that said how long it had been going on
		// (#495). logged_out_for turns that into one greppable field instead —
		// "wants deferred for two hours" is a fact about the field, not a count
		// of lines.
		if errors.Is(cause, sources.ErrNotLoggedIn) || errors.Is(cause, sources.ErrSignedOut) {
			switch since, err := service.store.MarkSourceLoggedOut(ctx, service.now()); {
			case errors.Is(err, pgx.ErrNoRows):
				// slskd was never configured, so there is nothing to record a
				// refusal against — the refusal is still deferred below.
			case err != nil:
				service.logger.Warn().Err(err).
					Msg("could not record when the download source signed out")
			default:
				event = event.Dur("logged_out_for", service.now().Sub(since))
			}
		}
		event.Msg("the download source would not be asked about a wanted recording")
		return service.store.DeferAcquisitionTarget(
			ctx, target.ID, heard.summary, cause.Error(),
			service.now().Add(heard.delay),
		)
	}
	service.logger.Warn().Err(cause).Str("acquisition_target_id", target.ID.String()).
		Msg("look for a copy of a wanted recording")
	return service.settleAttempt(ctx, target, "failed", waitingSummary, cause.Error())
}

// refusalFor names the six refusals, and reports anything else as a failure.
// The sentences are the pass's, not the provider's: what this one provider said
// is kept verbatim as the detail of the want that heard it, and the wants that
// only stood back never claim to have been told anything themselves.
//
// The narrow refusals are read before the wide ones they are told apart from.
// ErrNobodyWasAsked, ErrPhraseHeldBack, ErrNotLoggedIn, ErrSignedOut and
// ErrSearchSilenced all wrap ErrProviderUnavailable, so reading the general
// case first would give every one of them the same five-minute wait shared by
// the whole pass, which is what they exist to stop.
func refusalFor(cause error) (refusal, bool) {
	switch {
	case errors.Is(cause, sources.ErrNobodyWasAsked):
		// slskd's own queue held this one search back; every other want in the
		// pass asks in words of its own and may well be reaching Soulseek fine, so
		// this is never recorded on the pass.
		return refusal{
			summary: nobodyAskedSummary, delay: bannedRest, aboutOnePhrase: true,
		}, true
	case errors.Is(cause, sources.ErrRepliesUnread):
		// Peers answered this search and slskd had not stored what they said, so
		// the search is worth making again in a few minutes and no attempt may
		// be spent on it. It says nothing about the source's ability to search,
		// so the rest of the pass is asked as usual.
		return refusal{
			summary: repliesUnreadSummary, delay: throttleDelay, aboutOnePhrase: true,
		}, true
	case errors.Is(cause, sources.ErrPhraseHeldBack):
		// No pass sentence, because this refusal is never passed on.
		return refusal{
			summary: phraseHeldSummary, delay: throttleDelay, aboutOnePhrase: true,
		}, true
	case errors.Is(cause, sources.ErrNotLoggedIn), errors.Is(cause, sources.ErrSignedOut):
		// Being signed out of Soulseek is an account-wide fact, not a fact about
		// one search, so it is recorded on the pass with the same half-hour wait a
		// ban gets: nothing else in the pass can reach Soulseek either. ErrSignedOut
		// is the same fact caught earlier, by the cheaper check that runs before a
		// search is even made (see its own comment); it belongs in this case, not
		// the general one below, or which wait a want gets would depend on which of
		// the two checks got there first.
		return refusal{
			summary: loggedOutSummary, detail: sweepLoggedOutDetail, delay: bannedRest,
		}, true
	case errors.Is(cause, sources.ErrSearchSilenced):
		// Soulseek has stopped distributing this account's searches while
		// leaving the connection up, which is account-wide in the way being
		// signed out is: no want in the pass can be asked about either. It gets
		// the same half hour, because that is how long the ban behind it lasts.
		return refusal{
			summary: silencedSummary, detail: sweepSilencedDetail, delay: bannedRest,
		}, true
	case errors.Is(cause, sources.ErrRateLimited):
		return refusal{
			summary: throttledSummary, detail: sweepThrottledDetail, delay: throttleDelay,
		}, true
	case errors.Is(cause, sources.ErrProviderUnavailable):
		return refusal{
			summary: unavailableSummary, detail: sweepUnavailableDetail, delay: throttleDelay,
		}, true
	default:
		return refusal{}, false
	}
}

// deferRefused stands a want back without asking, because another want in this
// same pass was just refused. The refusal is about the provider rather than any
// one want, so passing it on costs nothing: no attempt is spent, and the want
// comes back with the others once whatever caused it has passed.
func (service *Service) deferRefused(
	ctx context.Context, target db.AcquisitionTargetRow, heard refusal,
) error {
	service.logger.Info().
		Str("acquisition_target_id", target.ID.String()).
		Dur("deferred_for", heard.delay).
		Msg("a want stood back with the rest of a refused pass")
	return service.store.DeferAcquisitionTarget(
		ctx, target.ID, heard.summary, heard.detail,
		service.now().Add(heard.delay),
	)
}

// settleAttempt requeues a want on the ladder with what this attempt came to.
// This is where attempts is spent: Schall went looking, and looking came to
// nothing worth fetching.
//
// 'none' and 'failed' stay apart for the reason a source search keeps them
// apart: nobody sharing it and nobody having managed to look are different
// answers, and only one of them says anything about the music.

func (service *Service) settleBelowFloor(
	ctx context.Context, target db.AcquisitionTargetRow, preferences sources.Preferences,
	detail string, refused []sources.Refused,
) error {
	floor := 0
	for _, one := range refused {
		if one.Kind != sources.RefusedBitRate {
			continue
		}
		if value, ok := preferences.BitRateFloor(one.Format); ok {
			floor = value
			break
		}
	}
	parkedSummary := fmt.Sprintf(
		"Only copies below your %d kbps minimum were found. Looked at again weekly.", floor)
	err := service.store.RequeueBelowFloor(ctx, target.ID, belowBitRateFloorSummary,
		parkedSummary, detail, service.nextLook(target),
		service.now().Add(7*24*time.Hour))
	return service.settleError(target, err)
}

func (service *Service) requeueParked(
	ctx context.Context, target db.AcquisitionTargetRow, summary, detail string,
) error {
	err := service.store.RequeueParkedAcquisitionTarget(ctx, target.ID, "none", summary, detail, "",
		service.now().Add(7*24*time.Hour))
	return service.settleError(target, err)
}

func (service *Service) settleError(target db.AcquisitionTargetRow, err error) error {
	// The want stopped being pending while this pass was running, which means
	// somebody decided something about it. Their decision stands.
	if errors.Is(err, pgx.ErrNoRows) {
		service.logger.Debug().Str("acquisition_target_id", target.ID.String()).
			Msg("acquisition target decided elsewhere while it was being looked for")
		return nil
	}
	return err
}

func (service *Service) settleAttempt(
	ctx context.Context, target db.AcquisitionTargetRow, outcome, summary, detail string,
) error {
	lastError := ""
	if outcome == "failed" {
		lastError = detail
	}
	err := service.store.RequeueAcquisitionTarget(
		ctx, target.ID, outcome, summary, detail, lastError,
		service.nextLook(target),
	)
	return service.settleError(target, err)
}

// The sentences a want being looked for explains itself with.
const (
	nothingOfferedSummary = "No peer is sharing a copy yet. It will be looked for again."
	// A want stuck on this is stuck for as long as the installation is, so the
	// sentence names the control rather than only the trouble: without it the
	// row says "waiting" for ever and there is nothing on any screen to act on
	// (issue #381). The key and the switch are named together because either one
	// missing stops the same thing, and the want cannot tell which it is.
	unlistenableSummary  = "Add an AcoustID key in Settings — nothing here can prove a copy is this recording."
	unlistenableDetail   = "Identifying files by their audio is switched off, and nothing else may admit a file a stranger sent."
	unsearchableSummary  = "There is nothing in this entry that could be searched for."
	unsearchableDetail   = "Every word of the entry was punctuation a source search cannot use."
	fetchingSummary      = "A copy is on its way. What it turns out to be decides what happens next."
	sweepThrottledDetail = "the download source is rate limiting requests: it refused another wishlist entry in the same pass"
	// Said of a want that was never asked about, so it claims only what the
	// pass knows: another want heard this, and nothing was learned about any
	// of them.
	sweepUnavailableDetail = "the download source cannot search at the moment: it refused another wishlist entry in the same pass"
	// Said of a want whose search was made and then never went out. It names the
	// half hour, because a want told "shortly" that comes back in thirty minutes
	// reads as something broken. It is never passed to another want: the search
	// queue that held this one back says nothing about anybody else's.
	nobodyAskedSummary = "The download source never sent this search out. It will be looked for again in half an hour."
	// Said of a want standing back because the download source was signed out of
	// Soulseek while another want in the pass searched. Unlike nobodyAskedSummary
	// this is passed to every want in the pass, because being signed out is a fact
	// about the source, not about one search.
	loggedOutSummary     = "The download source is not logged in to Soulseek. It will be looked for again in half an hour."
	sweepLoggedOutDetail = "the download source is not logged in to Soulseek: it refused another wishlist entry in the same pass"
	// Said of a want standing back because Soulseek has stopped answering this
	// account's searches at all. It names the half hour for nobodyAskedSummary's
	// reason, and it is passed to every want in the pass: the network answering
	// nobody is a fact about the account, not about one search.
	silencedSummary     = "Soulseek has stopped answering this account's searches. It will be looked for again in half an hour."
	sweepSilencedDetail = "the download source's searches are going unanswered: it refused another wishlist entry in the same pass"

	// Said of a want whose search peers answered, where the download source had
	// not stored their replies before the wait for it ran out. It reads nothing
	// like "nobody is sharing this", because peers did answer.
	repliesUnreadSummary = "Peers answered, but the download source had not stored their replies. It will be looked for again shortly."
	// Said of a want asked in words the download source had just been asked and
	// will not repeat this soon. It claims nothing about the source, which is
	// searching perfectly well for every other want in the pass.
	phraseHeldSummary = "This search was made moments ago. It will be made again shortly."
	// Said of a want everybody offering it is too busy to send to. It reads
	// nothing like "nobody is sharing this", because the two are opposites: the
	// music is plainly there, and the queue in front of it is the only thing
	// between the want and a copy.
	// Said of a want peers are sharing copies of, every one of which the user's
	// stored format preference refuses. It names the preference because the
	// preference is the only thing that can change the answer, and because
	// "nobody is sharing this" would send somebody looking at the network.
	refusedByPreferenceSummary = "Copies are on offer, but none in a format you accept. Change your format preferences to take one."
	refusedByPreferenceDetail  = "no copy on offer met the stored format preferences."
	// Said of a want peers are sharing copies of, every one of which is under
	// the bitrate the user set as their lowest. It names the floor for the same
	// reason: the floor is the one thing that can change the answer, and the
	// want keeps waiting on the ladder rather than lowering it by itself.
	belowBitRateFloorSummary = "Copies are on offer, but all below your minimum bit rate. Lower it in Settings to take one."
	belowBitRateFloorDetail  = "every copy on offer was below the minimum bit rate you set."
	peerBusySummary          = "Every peer sharing a copy is already sending as much as it will take at once. They will be asked again shortly."
	peerBusyDetail           = "every peer offering this recording already has as many files open with Schall as it will accept"
	// Said of a want whose copy sat in a peer's queue past queuePatience without
	// a position or a byte ever arriving. It names what happened rather than
	// leaving "a copy is on its way" standing for days over a request nothing
	// is coming of.
	queueExpiredSummary = "The peer that accepted this download never sent it. Schall will look elsewhere."
	queueExpiredDetail  = "the peer had accepted the download and reported nothing for over two hours: no queue position, no bytes"
	// The sentence the offer itself is recorded with, so a look back at what was
	// tried says why this one is resting rather than repeating "being fetched".
	queueExpiredFileSummary = "The peer accepted this download and never sent it."
)
