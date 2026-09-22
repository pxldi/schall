package acquisition

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/identity"
)

// stoppedFilingReview bounds how many stopped wants one start looks at again.
// Each costs up to two rate-limited MusicBrainz requests, from the same limiter
// the interactive artist search uses, and a want that waits for the next start
// has been waiting days already.
const stoppedFilingReview = 50

// Filer records what the library says one of its files is.
//
// It is the identity service, which owns that answer and everything that hangs
// off it. Optional: without one, a want whose copy the library files under
// another recording stops for a person exactly as it did before.
type Filer interface {
	// FileAsAcquired files a library file under the recording a want's copy was
	// proven to be. The proof is why, and it is written onto the identity as its
	// evidence. It refuses a file somebody decided about by hand.
	FileAsAcquired(
		ctx context.Context, fileID, recordingID uuid.UUID, proof identity.FilingProof,
	) error
	// AcceptAsWanted writes down a person's decision that a library file is the
	// recording a want asked for. Nothing proved it; they read both credits and
	// said so, and their answer is permanent.
	AcceptAsWanted(ctx context.Context, fileID, recordingID uuid.UUID) error
}

// ErrWantIsNotStopped reports a want that is not waiting on the question this
// answers: it either has a time to be looked at again, or holds no accepted copy
// the library made a file of.
var ErrWantIsNotStopped = errors.New("this wishlist entry is not waiting on its library file")

// ErrNoFiler reports an installation with no identity service, so nothing can
// write down what a library file is.
var ErrNoFiler = errors.New("recording what a library file is is unavailable")

// AcceptFiledCopy records that a person, looking at the two credits, says the
// library file a want's copy became is the wanted recording.
//
// It admits nothing on its own. The want stopped because two MusicBrainz rows
// disagreed and no witness could separate them, and this is a person separating
// them. The decision is written onto the file as a manual identity, which rearms
// the want in the same transaction; the sweep that follows settles the want
// against the file, through the same check every other settled want goes through
// (OwnedFileForRecording, then SettleAcquiredTarget).
//
// ErrWantIsNotStopped means the queue is no longer asking this.
func (service *Service) AcceptFiledCopy(ctx context.Context, targetID uuid.UUID) error {
	if service.filer == nil {
		return ErrNoFiler
	}
	target, err := service.store.AcquisitionTarget(ctx, targetID)
	if err != nil {
		return err
	}
	// The same three facts the stopped question is read from, asked again here:
	// a want with a time is being looked at, and one with no recording has
	// nothing to be accepted as.
	if target.Status != "pending" || target.NextAttemptAt.Valid ||
		!target.MusicBrainzRecordingID.Valid {
		return ErrWantIsNotStopped
	}
	filing, err := service.store.AcquiredCopyFiling(ctx, targetID)
	if err != nil {
		return err
	}
	if !filing.Found {
		return ErrWantIsNotStopped
	}
	if err := service.filer.AcceptAsWanted(
		ctx, filing.LibraryFileID, target.MusicBrainzRecordingID.UUID,
	); err != nil {
		return fmt.Errorf("record by hand that %s is recording %s: %w",
			filing.LibraryFileID, target.MusicBrainzRecordingID.UUID, err)
	}
	service.logger.Info().
		Str("acquisition_target_id", targetID.String()).
		Str("library_file_id", filing.LibraryFileID.String()).
		Str("musicbrainz_recording_id", target.MusicBrainzRecordingID.UUID.String()).
		Msg("a library file was accepted by hand as the recording a want asked for")
	service.kickSweeper(ctx)
	service.announce()
	return nil
}

// WithFiler registers what writes down that a library file is the recording a
// want's copy was proven to be.
func (service *Service) WithFiler(filer Filer) *Service {
	service.filer = filer
	return service
}

// resolveFiling decides what to do with a want whose copy is on the disc and
// whose library file the library does not call the want's recording.
//
// Three things can be true here and they are not the same thing at all:
//
//   - The library has not answered what the file is. That is silence, and a want
//     is never stopped on silence. Either nobody has asked yet, and the file is
//     asked about; or the library has finished with the file and written nothing
//     down, which is a hole where the proof that admitted the copy used to be.
//   - The library answered with a second MusicBrainz row that names the same
//     recording, because MusicBrainz merged the two, or with a second row of the
//     same registered track for a copy something else already proved to be the
//     want's music. One performance entered twice is not other music
//     (ADR 0025, 0031), so the want has exactly what it asked for.
//   - The library answered with a different recording that happens to share this
//     audio. Only a person can separate those, so the want stops and says which
//     two they are.
//
// Every route into a search passes through here, and so does the moment an
// imported copy is accounted for. The rules are one set in one place, so no
// route can be loosened on its own.
func (service *Service) resolveFiling(
	ctx context.Context, target db.AcquisitionTargetRow,
) error {
	if !target.MusicBrainzRecordingID.Valid {
		return nil
	}
	wanted := target.MusicBrainzRecordingID.UUID
	filing, err := service.store.AcquiredCopyFiling(ctx, target.ID)
	if err != nil {
		return err
	}
	if !filing.Found {
		return service.stopOnFiling(ctx, target, disagreedSummary)
	}
	if !filing.Identified {
		return service.fileNobodyAnswered(ctx, target, filing)
	}
	if filing.FiledRecordingID.Valid && filing.FiledRecordingID.UUID == wanted {
		return service.settleFiling(ctx, target, filing, acquiredCopyDetail)
	}
	if !filing.FiledRecordingID.Valid {
		// The library's answer is that the file holds owned music no provider
		// names. That is an answer, and taking it back belongs to whoever gave it.
		return service.stopOnFiling(ctx, target, localOnlyFilingSummary)
	}
	if service.resolver == nil {
		return service.stopOnFiling(ctx, target, filedElsewhereSummary(filing))
	}
	// The excerpt this copy was measured against when it was admitted, with who
	// published it. The read leaves it out unless it was fetched for the
	// recording the want names now. Only the registration-code test reads it,
	// and only a Deezer excerpt passes there.
	var anchor *identity.AnchorComparison
	if filing.AnchorMeasured {
		anchor = &identity.AnchorComparison{
			RecordingID: wanted, Rate: filing.AnchorRate, Source: filing.AnchorSource,
		}
	}
	verdict, err := service.resolver.OneRegistration(
		ctx, wanted, filing.FiledRecordingID.UUID, identity.FilingEvidence{
			Anchor:         anchor,
			CopyDurationMS: filing.CopyDurationMS,
			Method:         filing.ProvenMethod,
		})
	if err != nil {
		service.logger.Error().Err(err).
			Str("acquisition_target_id", target.ID.String()).
			Str("wanted_recording_id", wanted.String()).
			Str("filed_recording_id", filing.FiledRecordingID.UUID.String()).
			Msg("ask whether a want and its copy are one registration")
		return nil
	}
	if verdict == identity.FilingOtherRecording {
		return service.stopOnFiling(ctx, target, filedElsewhereSummary(filing))
	}
	// The want names the clean edition of the track on the disc, and the owner
	// keeps the explicit one (ADR 0032). The want settles against the
	// file as the library has it: the two rows are two recordings, so re-filing
	// the file under the want's row would write down that this audio is a
	// recording it is not.
	if verdict == identity.FilingExplicitEdition {
		service.logger.Info().
			Str("acquisition_target_id", target.ID.String()).
			Str("library_file_id", filing.LibraryFileID.String()).
			Str("wanted_recording_id", wanted.String()).
			Str("filed_recording_id", filing.FiledRecordingID.UUID.String()).
			Msg("a want for a clean edition holds the explicit one")
		return service.settleFiling(ctx, target, filing, explicitEditionDetail)
	}
	// What the want settles on. Usually it is what admitted the copy, read off the
	// copy's own row. The registration-code branch is the exception: it is carried
	// by the excerpt this recording's distributor published (ADR 0025),
	// and reaching here without a proof on the row means that branch answered.
	provenBy := filing.ProvenMethod
	if verdict == identity.FilingOneRegistration && !identity.ProvenForItsWant(provenBy) {
		provenBy = "published-sample"
	}
	detail := sameRegistrationDetail(verdict, provenBy)
	// A person's answer stands whatever else is true, and here it also satisfies
	// the want as it is: the two rows are one piece of music, so the file they
	// decided about is the music that was asked for.
	if filing.FiledIsManual {
		return service.settleFiling(ctx, target, filing, detail)
	}
	if service.filer == nil {
		return service.stopOnFiling(ctx, target, filedElsewhereSummary(filing))
	}
	proof := identity.FilingProof{
		Merged:    verdict == identity.FilingMerged,
		SecondRow: true,
		Method:    provenBy,
	}
	if err := service.filer.FileAsAcquired(
		ctx, filing.LibraryFileID, wanted, proof,
	); err != nil {
		// Somebody decided about the file between the read and the write. Their
		// answer is the same music, so the want settles against it untouched.
		if errors.Is(err, identity.ErrDecidedByHand) {
			return service.settleFiling(ctx, target, filing, detail)
		}
		return fmt.Errorf("file %s under the recording want %s names: %w",
			filing.LibraryFileID, target.ID, err)
	}
	service.logger.Info().
		Str("acquisition_target_id", target.ID.String()).
		Str("library_file_id", filing.LibraryFileID.String()).
		Str("wanted_recording_id", wanted.String()).
		Str("filed_recording_id", filing.FiledRecordingID.UUID.String()).
		Str("proven_by", provenBy).
		Bool("merged_by_musicbrainz", verdict == identity.FilingMerged).
		Msg("a want's copy was filed under a second row of the same registration")
	return service.settleFiling(ctx, target, filing, detail)
}

// sameRegistrationDetail says what let a want settle against a file its library
// calls another recording. Two things can, and they are different facts, so each
// says which it was: MusicBrainz merged the two rows, or something proved the
// copy is the want's music and the second row turned out to be the same
// registered track.
//
// The proof is named rather than assumed. Whoever reads the attempt back is
// reading why a want settled against a file that does not carry its recording,
// and "the two rows look alike" is not that reason.
func sameRegistrationDetail(verdict identity.FilingVerdict, method string) string {
	if verdict == identity.FilingMerged {
		return "A copy was fetched and imported. MusicBrainz has since merged " +
			"the recording your library filed it under with this one."
	}
	return "A copy was fetched and imported. " + identity.MethodSentence(method) +
		" The recording your library filed it under is the same registered track."
}

// fileNobodyAnswered handles a want whose copy became a library file the library
// has no identity for at all.
//
// An absent row is silence and never a disagreement. What it means depends on
// whether the library has finished with the file: a file nobody has asked about
// is waiting its turn, and a file the library has finished with and written
// nothing down for has lost something it had. The copy was admitted because its
// audio proved it, and that proof was written onto the file at import; a file
// that has none now is a file something deleted it from.
func (service *Service) fileNobodyAnswered(
	ctx context.Context, target db.AcquisitionTargetRow, filing db.AcquiredCopyFiling,
) error {
	if filing.ResolutionStatus == "pending" || filing.ResolutionStatus == "failed" {
		if _, err := service.store.QueueFileResolution(ctx, filing.LibraryFileID); err != nil {
			return fmt.Errorf("ask what file %s is: %w", filing.LibraryFileID, err)
		}
		// The want stands back rather than stopping or searching. It has its
		// music; what is missing is the library saying so, and the answer is on
		// its way.
		return service.store.RescheduleAcquisitionTarget(
			ctx, target.ID, importedSummary, service.now().Add(importedDelay))
	}
	service.logger.Error().
		Str("acquisition_target_id", target.ID.String()).
		Str("library_file_id", filing.LibraryFileID.String()).
		Str("wanted_recording_id", target.MusicBrainzRecordingID.UUID.String()).
		Str("resolution_status", filing.ResolutionStatus).
		Msg("a library file the library has finished with carries no identity")
	// The copy's own proof is the only thing that can put the file under this
	// recording. A row with no method, or one judged against a recording the want
	// no longer names, has nothing to write down, so the want stops and asks.
	if !identity.ProvenForItsWant(filing.ProvenMethod) {
		return service.stopOnFiling(ctx, target, unprovenFilingSummary)
	}
	if service.filer == nil {
		return service.stopOnFiling(ctx, target, disagreedSummary)
	}
	// No second row is involved: the library has no answer at all. What is written
	// down is the copy's own proof and nothing beside it.
	err := service.filer.FileAsAcquired(
		ctx, filing.LibraryFileID, target.MusicBrainzRecordingID.UUID,
		identity.FilingProof{Method: filing.ProvenMethod})
	if err != nil && !errors.Is(err, identity.ErrDecidedByHand) {
		return fmt.Errorf("file %s under the recording want %s names: %w",
			filing.LibraryFileID, target.ID, err)
	}
	return service.settleFiling(ctx, target, filing, acquiredCopyDetail)
}

// settleFiling finishes the want against the file it holds.
func (service *Service) settleFiling(
	ctx context.Context, target db.AcquisitionTargetRow,
	filing db.AcquiredCopyFiling, detail string,
) error {
	err := service.store.SettleAcquiredTarget(
		ctx, target.ID, filing.LibraryFileID, acquiredSummary, detail)
	// The want stopped being pending between the read and the write, which means
	// somebody decided something about it while this was running. Theirs stands.
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("settle want %s against the copy it holds: %w", target.ID, err)
	}
	// The identity may have changed a moment ago, and for a file carrying a
	// peer's tags that identity is the only thing that can map it. A failure
	// costs immediacy and nothing else.
	if service.matcher != nil {
		if err := service.matcher.ReconcileFile(ctx, filing.LibraryFileID); err != nil {
			service.logger.Error().Err(err).
				Str("library_file_id", filing.LibraryFileID.String()).
				Msg("offer a settled file to the catalogue")
		}
	}
	return nil
}

// stopOnFiling takes the want's next look away and says why. It is the state
// this whole path used to reach unconditionally.
func (service *Service) stopOnFiling(
	ctx context.Context, target db.AcquisitionTargetRow, summary string,
) error {
	service.logger.Warn().
		Str("acquisition_target_id", target.ID.String()).
		Msg("a want holds a copy the library files under another recording, so nothing was looked for")
	return service.store.StopLookingForAcquisitionTarget(ctx, target.ID, summary)
}

// reviewStoppedFilings looks again at the wants that stopped because the library
// files their copy under another recording.
//
// A stopped want has no next attempt, so no sweep reaches it and nothing
// automatic looks at it again. That is what stopping is for, and it is right for
// a want waiting on a person. It is wrong for a want stopped by a rule that has
// since been corrected, and such a want has no other way back. So the list is
// read once per start and every want on it is judged by exactly the rules a want
// stopping today is judged by. Nothing is admitted that a want reaching this
// today would not be admitted by.
func (service *Service) reviewStoppedFilings(ctx context.Context) {
	targets, err := service.store.StoppedTargetsHoldingAFiledCopy(ctx, stoppedFilingReview)
	if err != nil {
		service.logger.Error().Err(err).Msg("list the wants stopped on a filed copy")
		return
	}
	if len(targets) == 0 {
		return
	}
	service.logger.Info().Int("wants", len(targets)).
		Msg("looking again at the wants stopped on a filed copy")
	for _, target := range targets {
		if ctx.Err() != nil {
			return
		}
		if err := service.resolveFiling(ctx, target); err != nil {
			service.logger.Error().Err(err).
				Str("acquisition_target_id", target.ID.String()).
				Msg("look again at a want stopped on a filed copy")
		}
	}
}

// filedElsewhereSummary names the two recordings and says the audio cannot
// separate them.
//
// Both facts are what somebody needs to decide. The names are the difference;
// that the two share their audio is why nothing automatic will ever choose
// between them, and without it the row reads as a fault rather than a question.
// A recording missing its title falls back to the general sentence: half a name
// in a sentence about which of two recordings this is helps nobody.
func filedElsewhereSummary(filing db.AcquiredCopyFiling) string {
	filed := recordingNamed(filing.FiledTitle, filing.FiledArtist)
	wanted := recordingNamed(filing.WantedTitle, filing.WantedArtist)
	if filed == "" || wanted == "" {
		return disagreedSummary
	}
	return fmt.Sprintf(
		"Your library files that copy as %s. This entry wants %s, and the audio cannot "+
			"tell the two apart. Match the file to this track, or stop looking.",
		filed, wanted)
}

func recordingNamed(title, artist string) string {
	title, artist = strings.TrimSpace(title), strings.TrimSpace(artist)
	switch {
	case title == "":
		return ""
	case artist == "":
		return fmt.Sprintf("%q", title)
	}
	return fmt.Sprintf("%q by %s", title, artist)
}
