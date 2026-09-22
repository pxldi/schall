package downloads

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/chromaprint"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/identity"
	"github.com/pxldi/schall/internal/tagmatch"
)

// What a copy whose bytes have gone is told after it was measured against the
// want's new anchor. None of the three moves the verdict: there is no file to
// import, so the copy stays the question it was, and these say which question.
const (
	storedProvenSummary = "This copy is the wanted recording. Its file has gone from the " +
		"download folder, so there is nothing left to import and the want is still looking."
	storedDiffersSummary = "This copy is not the wanted recording, and its file has gone " +
		"from the download folder."
	storedUndecidedSummary = "This copy was measured against the wanted recording's sample " +
		"and it settled nothing. Its file has gone from the download folder."
)

// JudgedAgain is what one pass over the copies already decided about came to.
//
// Missing is counted rather than acted on, and it is the number that says
// whether this is worth running again later: a copy whose bytes have gone from
// the inbox cannot be judged by anything, and its recorded verdict is left
// exactly as it is.
type JudgedAgain struct {
	Offered  int
	Judged   int
	Missing  int
	Answered int
}

// JudgeAgain puts the copies a peer already sent through validation a second
// time.
//
// A copy is one file Schall fetched for a want. Validation decides whether it is
// the recording that want names, and what it decides is normally permanent. This
// exists because the rules changed underneath copies that were already decided:
// wants gained an audio anchor, and an identification naming another MusicBrainz
// row of the same registered track stopped being read as a refusal
// (ADR 0024 and 0025). The files are still in the inbox, so they can
// be answered rather than argued about.
//
// Nothing here decides anything. Each copy goes through ImportOne, which is the
// same call a copy gets the moment it arrives, so there is one set of rules and
// this pass cannot be looser than the live path. It only chooses what to offer.
//
// It runs once and stops. The list is read at the start and worked through, so
// the pass ends whatever it finds, and a want whose copies all stay questions is
// not tried again until somebody asks for another pass.
func (importer *Importer) JudgeAgain(ctx context.Context) error {
	_, err := importer.judgeAgain(ctx)
	return err
}

// JudgeCreditRefusalsAgain puts the copies refused on the artist tag alone
// through validation a second time.
//
// It is the same pass over a different set. A credit is now compared as the set
// of artists it names, and a credit that disagrees no longer voids a pair the
// audio already identified (ADR 0030), so the copies those two rules
// refused can be answered rather than left standing.
func (importer *Importer) JudgeCreditRefusalsAgain(ctx context.Context) error {
	_, err := importer.judgeCreditRefusalsAgain(ctx)
	return err
}

// judgeAgain is JudgeAgain with the tally the caller does not need. The pass
// reports itself to the log, which is where somebody who asked for it looks;
// the numbers are returned so a test can read what one pass did.
func (importer *Importer) judgeAgain(ctx context.Context) (JudgedAgain, error) {
	copies, err := importer.store.CopiesToJudgeAgain(ctx)
	if err != nil {
		return JudgedAgain{}, err
	}
	return importer.judgeCopies(ctx, copies)
}

// judgeCreditRefusalsAgain is JudgeCreditRefusalsAgain with the same tally.
func (importer *Importer) judgeCreditRefusalsAgain(ctx context.Context) (JudgedAgain, error) {
	copies, err := importer.store.CopiesRefusedOnCreditAlone(ctx)
	if err != nil {
		return JudgedAgain{}, err
	}
	return importer.judgeCopies(ctx, copies)
}

// judgeCopies works through one list of copies. Which copies are worth offering
// is the caller's question; what happens to one of them is the same whatever
// rule changed under it, because every copy goes back through ImportOne.
func (importer *Importer) judgeCopies(
	ctx context.Context, copies []db.JudgeAgainRow,
) (JudgedAgain, error) {
	var result JudgedAgain
	result.Offered = len(copies)

	for _, copied := range copies {
		// Asked per copy rather than once per want, because a copy judged a moment
		// ago may have been the answer. Validation imports what it proves without
		// asking whether the want was answered since, so this is what stops a want
		// with fifteen copies taking fifteen of them.
		answered, err := importer.store.WantHasAcceptedCopy(ctx, copied.AcquisitionTargetID)
		if err != nil {
			return result, err
		}
		if answered {
			result.Answered++
			continue
		}
		if !importer.candidatePresent(copied) {
			result.Missing++
			continue
		}
		if err := importer.store.ReopenCopyForJudging(ctx, copied.RequestID); err != nil {
			if errors.Is(err, db.ErrCopyNotReopened) {
				importer.logger.Debug().
					Str("acquisition_target_id", copied.AcquisitionTargetID.String()).
					Str("request_id", copied.RequestID.String()).
					Msg("a copy was taken by something else before it could be judged again")
				continue
			}
			return result, err
		}
		if err := importer.ImportOne(ctx, copied.RequestID); err != nil {
			// One copy that could not be judged is not a reason to stop judging the
			// rest. Nothing retries this pass for the copy, so the claim the pass
			// took is given back: a request left at 'validating' would hide the copy
			// from the queue and hold the want's one open request forever.
			importer.logger.Warn().Err(err).
				Str("acquisition_target_id", copied.AcquisitionTargetID.String()).
				Str("request_id", copied.RequestID.String()).
				Msg("judge a copy again")
			importer.releaseFromJudging(ctx, copied)
			continue
		}
		result.Judged++
	}
	importer.logger.Info().
		Int("offered", result.Offered).Int("judged", result.Judged).
		Int("missing", result.Missing).Int("already_answered", result.Answered).
		Msg("copies already decided about were judged again")
	return result, nil
}

// candidatePresent reports whether the copy's bytes are still where validation
// would look for them.
//
// It is asked before the copy is put back into validation, and the reason is a
// question that would otherwise be destroyed. A copy whose file has gone is read
// by validation as bytes that never arrived, and it would be recorded as such —
// so a question somebody was going to answer would be overwritten with a wrong
// account of why nobody can. A copy nothing can find is left saying exactly what
// it says now.
//
// The path is derived the way inspectCandidate derives it and not some other
// way. A second derivation that disagreed by one component would pass here and
// fail there, which is the same lost question by a longer route.
func (importer *Importer) candidatePresent(copied db.JudgeAgainRow) bool {
	_, present := importer.deliveredFile(copied.Provider, copied.SourceDirectory, copied.FileName)
	return present
}

// releaseFromJudging gives back the claim a judging pass took on a copy it could
// not decide. A failure here is logged and not returned: the pass carries on,
// and the request is no worse off than before this call.
func (importer *Importer) releaseFromJudging(ctx context.Context, copied db.JudgeAgainRow) {
	if err := importer.store.ReleaseCopyFromJudging(ctx, copied.RequestID); err != nil {
		importer.logger.Error().Err(err).
			Str("acquisition_target_id", copied.AcquisitionTargetID.String()).
			Str("request_id", copied.RequestID.String()).
			Msg("give a copy back after a judging pass failed on it")
	}
}

// JudgeWantCopies measures one want's held copies against the anchor that has
// just landed on it.
//
// Nobody asks for this. An anchor arriving is a witness the copies never had
// when they were judged, and ADR 0029 §5 says they are measured
// against it without anybody pressing anything. The bounds are JudgeAgain's, so
// a pass nothing asked for cannot reach further than the pass a person asks for.
//
// A copy whose bytes are still in the inbox goes back through ImportOne, which
// is the same call it got when it arrived. A copy whose bytes have gone is
// measured from the fingerprint stored on it, and only the measurement is
// written: there is no file to import, so there is no admission to record.
func (importer *Importer) JudgeWantCopies(ctx context.Context, targetID uuid.UUID) error {
	copies, err := importer.store.CopiesToJudgeAgainForWant(ctx, targetID)
	if err != nil {
		return err
	}
	var result JudgedAgain
	result.Offered = len(copies)

	for _, copied := range copies {
		answered, err := importer.store.WantHasAcceptedCopy(ctx, copied.AcquisitionTargetID)
		if err != nil {
			return err
		}
		if answered {
			result.Answered++
			continue
		}
		if !importer.candidatePresent(copied) {
			result.Missing++
			if err := importer.readStoredCopy(ctx, copied); err != nil {
				return err
			}
			continue
		}
		if err := importer.store.ReopenCopyForJudging(ctx, copied.RequestID); err != nil {
			if errors.Is(err, db.ErrCopyNotReopened) {
				continue
			}
			return err
		}
		if err := importer.ImportOne(ctx, copied.RequestID); err != nil {
			// One copy that could not be judged is not a reason to stop judging the
			// rest, exactly as in JudgeAgain, and its claim is given back the same way.
			importer.logger.Warn().Err(err).
				Str("acquisition_target_id", copied.AcquisitionTargetID.String()).
				Str("request_id", copied.RequestID.String()).
				Msg("judge a copy against a new anchor")
			importer.releaseFromJudging(ctx, copied)
			continue
		}
		result.Judged++
	}
	importer.logger.Info().
		Str("acquisition_target_id", targetID.String()).
		Int("offered", result.Offered).Int("judged", result.Judged).
		Int("without_bytes", result.Missing).Int("already_answered", result.Answered).
		Msg("a newly anchored want's copies were judged")
	// The want may have stopped fetching because it held a copy nobody had
	// answered and had nothing to answer it with. It has something now, and the
	// copies have been put to it, so it looks again. A want a copy here refused
	// or admitted already has a time of its own and is left alone.
	woken, err := importer.store.RearmWantNowAnchored(ctx, targetID, db.RearmedByAnAnchorSummary)
	if err != nil {
		return err
	}
	if woken {
		importer.logger.Info().
			Str("acquisition_target_id", targetID.String()).
			Msg("a want that stopped for an answer looks again now it has an anchor")
	}
	return nil
}

// readStoredCopy measures a copy whose bytes have gone against the want's
// anchor, from the fingerprint kept on the copy when it was judged.
//
// The verdict is not touched, and that is the point. Importing needs the file:
// admit copies the bytes into the library, and there are none, so a copy proven
// here cannot be let in and must not be recorded as if it had been. What it can
// have is an account of itself — the measurement and a sentence saying the file
// has gone — so the person reading the queue knows the copy was the right
// recording and that the want has to be fetched again.
//
// Everything that can go wrong is silence. No anchor, no stored fingerprint, a
// fingerprint that will not decode, a copy shorter than the excerpt: none of them
// is evidence about the music, and none of them is written down as one.
func (importer *Importer) readStoredCopy(ctx context.Context, copied db.JudgeAgainRow) error {
	if copied.AnchorFingerprint == "" || copied.AudioFingerprint == "" {
		return nil
	}
	reference, err := chromaprint.Decode(copied.AnchorFingerprint)
	if err != nil {
		importer.logger.Error().Err(err).
			Str("acquisition_target_id", copied.AcquisitionTargetID.String()).
			Msg("a want's stored anchor is not a fingerprint")
		return nil
	}
	frames, err := chromaprint.Decode(copied.AudioFingerprint)
	if err != nil {
		importer.logger.Error().Err(err).Str("copy_id", copied.CopyID.String()).
			Msg("a copy's stored fingerprint could not be read")
		return nil
	}
	rate, err := chromaprint.BitErrorRate(reference, frames)
	if err != nil {
		return nil
	}

	anchor := db.ImportAnchor{
		Source: copied.AnchorSource, Reference: copied.AnchorReference,
		Seconds: copied.AnchorSeconds, Label: copied.AnchorLabel, Views: copied.AnchorViews,
		Rate: rate, Measured: true,
	}
	summary := storedUndecidedSummary
	switch identity.ReadAnchor(copied.AnchorSource, rate) {
	case tagmatch.Agrees:
		agrees := true
		anchor.Agrees = &agrees
		summary = storedProvenSummary
	case tagmatch.Differs:
		agrees := false
		anchor.Agrees = &agrees
		summary = storedDiffersSummary
	case tagmatch.Unknown:
		// An upload found by name proves nothing, but the person reading the
		// row is still told the audio matched it: that is the fact they weigh.
		if copied.AnchorSource == identity.AnchorByName &&
			chromaprint.Read(rate) == chromaprint.SameAudio {
			agrees := true
			anchor.Agrees = &agrees
		}
	}
	return importer.store.RecordCopyAnchorReading(ctx, copied.CopyID, anchor, summary)
}
