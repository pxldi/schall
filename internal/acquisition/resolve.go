package acquisition

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/identity"
	"github.com/pxldi/schall/internal/musicbrainz"
	"github.com/pxldi/schall/internal/tagmatch"
)

// resolveBatch bounds how many entries one sweep asks the provider about.
//
// It is much smaller than sweepBatch because the two cost different things.
// Looking for an owned copy is a query; resolving an entry is one or two
// rate-limited MusicBrainz requests, and the limiter is shared with the
// interactive artist search. Five entries is a few seconds of throttle, and
// whatever is left over stays due now, so the sweeper carries straight on rather
// than capping how much can be wanted at once.
const resolveBatch = 5

// resolveTimeout bounds one entry's resolution. Two throttled requests and their
// round trips fit inside it with room to spare; past that the provider is not
// answering, which is a failure to record rather than something to wait out.
const resolveTimeout = 30 * time.Second

// Resolver answers which MusicBrainz recording a piece of evidence names.
//
// It is the same resolver that decides what a library file is, deliberately: an
// entry and a file are the same question asked at different times, and one set of
// rules answers both so that neither can be loosened alone.
type Resolver interface {
	Resolve(
		ctx context.Context, evidence identity.Evidence, excluded ...uuid.UUID,
	) (identity.Outcome, error)
	// OneRegistration reports what the recording a want names and the recording
	// the library filed that want's copy under came to: rows MusicBrainz has
	// merged, one registered track it holds twice with the copy proven against
	// the want's row (ADR 0025, 0031), the explicit edition of a clean
	// row the want names (ADR 0032), or two recordings only a person
	// can separate. It is asked at one moment: a want about to stop for ever with
	// its own music on the disc. The evidence is what the want's own record says
	// about the copy on its disc.
	OneRegistration(
		ctx context.Context, wanted, filed uuid.UUID, evidence identity.FilingEvidence,
	) (identity.FilingVerdict, error)
}

// WithResolver registers what turns an entry into a recording. Without it,
// targets carrying a recording somebody supplied still work, and entries wait
// unresolved rather than being guessed at.
func (service *Service) WithResolver(resolver Resolver) *Service {
	service.resolver = resolver
	return service
}

// resolveDue asks the provider about the entries whose turn it is.
//
// A provider that cannot be reached is recorded on the target and the sweep moves
// on to the next one. Failing the whole sweep would be worse than useless: the
// sweeper is its own scheduler, so one unreachable provider would stop every
// want in the installation from being looked at again.
func (service *Service) resolveDue(ctx context.Context) error {
	if service.resolver == nil {
		return nil
	}
	targets, err := service.store.DueUnresolvedTargets(ctx, service.now(), resolveBatch)
	if err != nil {
		return fmt.Errorf("list unresolved acquisition targets: %w", err)
	}
	// A refusal for load is about the provider's patience rather than any one
	// entry, so the rest of the pass hears it once rather than finding it out one
	// entry at a time. Five entries each putting their question three times to a
	// provider that has just said stop is the shape of asking that got the refusal
	// in the first place.
	busy := false
	for _, target := range targets {
		if busy {
			if err := service.deferBusy(ctx, target); err != nil {
				return err
			}
			continue
		}
		throttled, err := service.resolve(ctx, target)
		if err != nil {
			return err
		}
		busy = throttled
	}
	if len(targets) > 0 {
		service.announce()
	}
	return nil
}

// resolve turns one entry into a recording, or into a question. It reports
// whether the provider refused to be asked at all, which is the one outcome the
// rest of the pass needs to know about.
func (service *Service) resolve(
	ctx context.Context, target db.AcquisitionTargetRow,
) (throttled bool, err error) {
	// The stored credit is verbatim — "A, B, C" as the source wrote it — but
	// the resolver is offered the primary artist alone. A comma-joined credit
	// does not say how its artists relate, and the grader's guest rule already
	// forgives a recording credited to the primary together with others; the
	// full string would demand the joins match word for word.
	evidence := identity.Evidence{
		Subject:    identity.SubjectEntry,
		ISRC:       target.EntryISRC.String,
		Artist:     tagmatch.PrimaryCredit(target.EntryArtist),
		Album:      target.EntryAlbum,
		Title:      target.EntryTitle,
		DurationMS: int(target.EntryDurationMS.Int32),
		// What the source said about the edition, and only where it said it. It
		// excludes a clean row and admits nothing (ADR 0032).
		Explicit: target.EntryExplicit.Valid && target.EntryExplicit.Bool,
	}

	// What somebody has already said this entry is not. Asking again without it
	// would offer them the answer they rejected, which is the whole reason the
	// wrong-target outcome exists.
	excluded, err := service.store.RejectedTargetResolutions(ctx, target.ID)
	if err != nil {
		return false, fmt.Errorf("read rejected resolutions for acquisition target %s: %w", target.ID, err)
	}

	askCtx, cancel := context.WithTimeout(ctx, resolveTimeout)
	outcome, err := service.resolver.Resolve(askCtx, evidence, excluded...)
	cancel()
	if err != nil {
		return errors.Is(err, musicbrainz.ErrThrottled), service.recordResolutionFailure(ctx, target, err)
	}

	switch outcome.Status {
	case identity.StatusResolved:
		return false, service.takeAnswer(ctx, target, outcome)
	case identity.StatusNeedsReview, identity.StatusConflict:
		return false, service.askTheUser(ctx, target, outcome)
	}
	// Nothing MusicBrainz knows of matches the entry. That is not a question a
	// person can answer — there is nothing to choose between — and it is not
	// permanent either: MusicBrainz gains recordings, so the entry is asked about
	// again on the slower ladder, which is paced to how often a catalogue gains a
	// recording rather than to how often a peer network changes.
	return false, service.settleResolution(ctx, target, service.store.RequeueUnresolvedTarget(
		ctx, target.ID, "none", unfoundSummary, outcome.Summary, "",
		service.nextUnfoundAttempt(target.Attempts),
	))
}

// takeAnswer writes the recording the entry resolved to.
func (service *Service) takeAnswer(
	ctx context.Context, target db.AcquisitionTargetRow, outcome identity.Outcome,
) error {
	resolved, err := service.store.ResolveAcquisitionTarget(ctx, db.ResolveAcquisitionTargetParams{
		ID:                        target.ID,
		MusicBrainzRecordingID:    outcome.Identity.RecordingID,
		MusicBrainzReleaseGroupID: nullableUUID(outcome.Identity.ReleaseGroupID),
		ResolutionMethod:          outcome.Identity.Method,
		Summary:                   outcome.Summary + " " + wantedSummary,
		SupersededSummary:         supersededSummary,
		SupersededNotWanted:       supersededNotWantedSummary,
		Detail:                    outcome.Summary,
		NextAttemptAt:             service.now(),
	})
	if err == nil {
		service.logger.Info().
			Str("acquisition_target_id", target.ID.String()).
			Str("musicbrainz_recording_id", outcome.Identity.RecordingID.String()).
			Str("method", outcome.Identity.Method).
			Str("status", resolved.Status).
			Msg("acquisition target resolved")
	}
	return service.settleResolution(ctx, target, err)
}

// askTheUser stores the plausible answers and stops.
//
// There is no next attempt: the question is now the user's, and re-asking the
// provider on a timer would replace the candidates they are looking at with a
// fresh copy of the same ones. Item by item, this is what keeps the guarantee —
// nothing here picks between candidates, ever.
func (service *Service) askTheUser(
	ctx context.Context, target db.AcquisitionTargetRow, outcome identity.Outcome,
) error {
	candidates := make([]db.AcquisitionTargetCandidateRow, 0, len(outcome.Candidates))
	for _, candidate := range outcome.Candidates {
		candidates = append(candidates, db.AcquisitionTargetCandidateRow{
			MusicBrainzRecordingID:    candidate.RecordingID,
			MusicBrainzReleaseGroupID: nullableRecordingID(candidate.ReleaseGroupID),
			ArtistName:                candidate.ArtistName,
			ReleaseTitle:              optionalText(candidate.ReleaseTitle),
			TrackTitle:                candidate.TrackTitle,
			DurationMS:                optionalDuration(candidate.DurationMS),
			ISRC:                      optionalText(candidate.ISRC),
			Rank:                      int32(candidate.Rank),
			// An empty list is what the provider said; a missing one would be a
			// null the column refuses.
			Agrees:  orEmpty(candidate.Agrees),
			Differs: orEmpty(candidate.Differs),
			Summary: candidate.Summary,
		})
	}
	_, err := service.store.SendAcquisitionTargetToReview(ctx, db.ReviewAcquisitionTargetParams{
		ID:         target.ID,
		Summary:    outcome.Summary,
		Detail:     outcome.Summary,
		Candidates: candidates,
	})
	if err == nil {
		service.logger.Info().
			Str("acquisition_target_id", target.ID.String()).
			Int("candidates", len(candidates)).
			Msg("acquisition target needs a decision")
	}
	return service.settleResolution(ctx, target, err)
}

// recordResolutionFailure writes down that the provider could not be asked. It
// is about Schall and never about the music, so it goes to last_error, the target
// stays unresolved, and it is tried again rather than shown to anybody as a
// question they cannot answer.
//
// Being refused for load is that with one difference: MusicBrainz said "not now",
// not "not reachable", and it costs the entry nothing. No attempt is spent and
// the ladder does not move, only the time the entry comes back at. This is what a
// want already gets when the download source refuses to be asked, and it matters
// more here than there — an entry refused before it was ever looked up would
// otherwise climb to the day-long rungs having never once been asked about, which
// is exactly what a playlist imported into a busy provider would do to itself.
func (service *Service) recordResolutionFailure(
	ctx context.Context, target db.AcquisitionTargetRow, cause error,
) error {
	if errors.Is(cause, musicbrainz.ErrThrottled) {
		service.logger.Info().Err(cause).
			Str("acquisition_target_id", target.ID.String()).
			Dur("deferred_for", throttleDelay).
			Msg("MusicBrainz would not be asked what an entry is")
		return service.store.DeferUnresolvedTarget(
			ctx, target.ID, catalogueBusySummary, cause.Error(),
			service.now().Add(throttleDelay),
		)
	}
	service.logger.Warn().Err(cause).
		Str("acquisition_target_id", target.ID.String()).
		Msg("resolve acquisition target")
	return service.settleResolution(ctx, target, service.store.RequeueUnresolvedTarget(
		ctx, target.ID, "failed", unaskedSummary, cause.Error(), cause.Error(),
		service.nextAttempt(target.Attempts),
	))
}

// deferBusy stands an entry back without asking, because another entry in this
// same pass was just refused for load. The refusal is about the provider rather
// than about any one entry, so passing it on costs nothing: no attempt is spent,
// and the entry comes back with the others once the refusal has expired.
func (service *Service) deferBusy(ctx context.Context, target db.AcquisitionTargetRow) error {
	service.logger.Info().
		Str("acquisition_target_id", target.ID.String()).
		Dur("deferred_for", throttleDelay).
		Msg("an entry stood back with the rest of a refused pass")
	return service.store.DeferUnresolvedTarget(
		ctx, target.ID, catalogueBusySummary, sweepBusyDetail,
		service.now().Add(throttleDelay),
	)
}

// settleResolution absorbs the one failure that is not a failure: the target
// stopped being unresolved between being read and being written, which means
// somebody decided something about it while this pass was running. Their decision
// stands, and the attempt is dropped rather than applied over it.
func (service *Service) settleResolution(
	ctx context.Context, target db.AcquisitionTargetRow, err error,
) error {
	if errors.Is(err, pgx.ErrNoRows) {
		service.logger.Debug().
			Str("acquisition_target_id", target.ID.String()).
			Msg("acquisition target decided elsewhere during resolution")
		return nil
	}
	if err != nil {
		return fmt.Errorf("record resolution of acquisition target %s: %w", target.ID, err)
	}
	return nil
}

// SourceRechecker asks again what a file with an automatic source identity is
// (ADR 0037 §6). It is the identity service, which owns what a file is.
type SourceRechecker interface {
	RecheckSource(
		ctx context.Context, fileID uuid.UUID, want *identity.SourceWant,
	) (identity.SourceRecheck, error)
}

// WithSourceRechecker registers what asks again about files with an automatic
// source identity.
func (service *Service) WithSourceRechecker(sources SourceRechecker) *Service {
	service.sources = sources
	return service
}

// recheckSources asks again about the files with an automatic source identity
// whose turn it is, on the unfound ladder: a day, three days, a week, then
// every thirty days (ADR 0037 §6). A file that moves onto a recording loses
// its schedule with its source identity. Every other answer is recorded, with
// whatever it found against the identity, and the file climbs a rung.
//
// It is bounded like resolution, because it asks the same rate-limited
// provider, and a refusal for load stands the rest of the pass down without
// spending a rung.
func (service *Service) recheckSources(ctx context.Context) error {
	if service.sources == nil {
		return nil
	}
	due, err := service.store.DueSourceRechecks(ctx, service.now(), resolveBatch)
	if err != nil {
		return fmt.Errorf("list the source files due another look: %w", err)
	}
	busy := false
	for _, file := range due {
		if busy {
			if err := service.store.RescheduleSourceRecheck(
				ctx, file.LibraryFileID, service.now().Add(throttleDelay), false, "",
			); err != nil {
				return err
			}
			continue
		}
		want := sourceWant(file)
		if want != nil {
			if want.Excluded, err = service.store.RejectedTargetResolutions(ctx, file.TargetID.UUID); err != nil {
				return fmt.Errorf("read rejected resolutions for acquisition target %s: %w",
					file.TargetID.UUID, err)
			}
		}
		result, err := service.sources.RecheckSource(ctx, file.LibraryFileID, want)
		if errors.Is(err, musicbrainz.ErrThrottled) {
			busy = true
			if err := service.store.RescheduleSourceRecheck(
				ctx, file.LibraryFileID, service.now().Add(throttleDelay), false, "",
			); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			// A provider nobody could reach says nothing about the file, so the
			// rung and what was last found stay as they were.
			service.logger.Warn().Err(err).
				Str("library_file_id", file.LibraryFileID.String()).
				Msg("ask again what a source file is")
			if err := service.store.RescheduleSourceRecheck(
				ctx, file.LibraryFileID, service.nextUnfoundAttempt(file.Attempts), false, "",
			); err != nil {
				return err
			}
			continue
		}
		if result.Moved {
			service.logger.Info().
				Str("library_file_id", file.LibraryFileID.String()).
				Str("musicbrainz_recording_id", result.RecordingID.String()).
				Msg("a source file moved onto the recording MusicBrainz proved")
			continue
		}
		if err := service.store.RescheduleSourceRecheck(
			ctx, file.LibraryFileID, service.nextUnfoundAttempt(file.Attempts+1),
			true, result.Contradiction,
		); err != nil {
			return err
		}
	}
	return nil
}

// sourceWant is the want a source file's copy was fetched for, in the words
// resolution reads, or nil where there is none.
func sourceWant(file db.SourceRecheckRow) *identity.SourceWant {
	if !file.TargetID.Valid {
		return nil
	}
	return &identity.SourceWant{Entry: identity.Evidence{
		Subject:    identity.SubjectEntry,
		ISRC:       file.EntryISRC,
		Artist:     tagmatch.PrimaryCredit(file.EntryArtist),
		Album:      file.EntryAlbum,
		Title:      file.EntryTitle,
		DurationMS: file.EntryDurationMS,
		Explicit:   file.EntryExplicit,
	}}
}

func nullableUUID(value uuid.UUID) uuid.NullUUID {
	if value == uuid.Nil {
		return uuid.NullUUID{}
	}
	return uuid.NullUUID{UUID: value, Valid: true}
}

func nullableRecordingID(value uuid.UUID) *uuid.UUID {
	if value == uuid.Nil {
		return nil
	}
	return &value
}

func optionalText(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func optionalDuration(value *int) *int32 {
	if value == nil {
		return nil
	}
	milliseconds := int32(*value)
	return &milliseconds
}

func orEmpty(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}
