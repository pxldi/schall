package library

import (
	"context"
	"fmt"
	"strings"

	"github.com/pxldi/schall/internal/chromaprint"
	"github.com/pxldi/schall/internal/identity"
)

// WithFingerprinter lets ReplaceIfBetter compare a damaged file's audio with
// the copy fetched to replace it. Without one, a file whose identity is not
// settled is never replaced.
func (remover *Remover) WithFingerprinter(prints *chromaprint.Fingerprinter) *Remover {
	remover.prints = prints
	return remover
}

// audioAgrees asks whether a file whose identity is not settled holds the same
// audio as the proven copy fetched to replace it. It reads the two files on
// the rule a library witness admits a copy on (identity.WitnessAgrees): from
// the start, over both whole files, with both lengths known and agreeing.
//
// Every failure to measure is "no". why says what stopped it, for the log.
func (remover *Remover) audioAgrees(
	ctx context.Context, old, fresh upgradeCandidate,
) (bool, string) {
	if remover.prints == nil {
		return false, "no fingerprinter is set up"
	}
	oldFrames, oldSeconds, err := remover.oldFrames(ctx, old)
	if err != nil {
		return false, fmt.Sprintf("the damaged file could not be fingerprinted: %v", err)
	}
	freshFrames, freshSeconds, err := remover.prints.PlayableFrames(
		ctx, fresh.path, chromaprint.CandidateLengthSeconds)
	if err != nil {
		return false, fmt.Sprintf("the new copy could not be fingerprinted: %v", err)
	}
	comparison, err := chromaprint.CompareFromStart(oldFrames, freshFrames)
	if err != nil {
		return false, fmt.Sprintf("the two files could not be compared: %v", err)
	}
	// The comparison reads only as far as the shorter fingerprint, and the
	// damaged file's length may come from a tag. Both decoded lengths must
	// agree, so a longer file that opens with the same audio is not deleted.
	if diff := oldSeconds - freshSeconds; diff*1000 > identity.DurationToleranceMS ||
		-diff*1000 > identity.DurationToleranceMS {
		return false, fmt.Sprintf("the damaged file decodes to %ds and the new copy to %ds",
			oldSeconds, freshSeconds)
	}
	freshDurationMS := fresh.durationMS
	if freshDurationMS <= 0 {
		// The new copy decoded cleanly at import, so its frames are its length.
		freshDurationMS = freshSeconds * 1000
	}
	witness := identity.WitnessComparison{
		Rate:                     comparison.GlobalBER,
		Comparison:               &comparison,
		FileDurationMS:           old.durationMS,
		CandidateDurationMS:      freshDurationMS,
		ReferenceCoverageSeconds: oldSeconds,
		CandidateCoverageSeconds: freshSeconds,
	}
	why := fmt.Sprintf("rate %.3f, worst window %.3f, damaged file %ds of %dms, new copy %ds of %dms",
		comparison.GlobalBER, comparison.WorstWindowBER,
		oldSeconds, old.durationMS, freshSeconds, freshDurationMS)
	return identity.WitnessAgrees(&witness), why
}

// oldFrames reads the damaged file's fingerprint. A kept witness fingerprint
// is used when it covers the whole file. fpcalc stops at a damaged frame, so
// a kept one often covers only the opening; the file is then read again
// through ffmpeg, which decodes past the damage.
func (remover *Remover) oldFrames(
	ctx context.Context, old upgradeCandidate,
) ([]uint32, int, error) {
	if kept := old.witnessFingerprint; kept != "" {
		if _, value, found := strings.Cut(kept, ":"); found && value != "" {
			frames, err := chromaprint.Decode(value)
			seconds := chromaprint.CoveredSeconds(len(frames))
			if err == nil && old.durationMS > 0 &&
				seconds*1000+identity.DurationToleranceMS >= old.durationMS {
				return frames, seconds, nil
			}
		}
	}
	return remover.prints.PlayableFrames(ctx, old.path, chromaprint.CandidateLengthSeconds)
}

// keepBoth leaves a damaged file and the copy fetched for it side by side
// when nothing proved the two hold the same audio. Both carry the same
// recording, so the pair is listed on the duplicates screen for a person to
// compare. The old file stands back from the sweep so the same copy is not
// fetched again on the next pass.
func (remover *Remover) keepBoth(ctx context.Context, old, fresh upgradeCandidate, why string) {
	if _, err := remover.pool.Exec(ctx,
		`UPDATE library_files SET upgrade_refused_at = now() WHERE id = $1`, old.id,
	); err != nil {
		remover.logger.Warn().Err(err).Str("library_file_id", old.id.String()).
			Msg("record that a damaged file's replacement did not match its audio")
	}
	remover.logger.Warn().
		Str("library_file_id", old.id.String()).
		Str("path", old.path).
		Str("new_library_file_id", fresh.id.String()).
		Str("why", why).
		Msg("a damaged file was kept beside its replacement because their audio did not agree")
}
