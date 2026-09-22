package downloads

import (
	"context"
	"fmt"

	"github.com/pxldi/schall/internal/chromaprint"
	"github.com/pxldi/schall/internal/db"
)

// How many copies one pass reads. Each one is a whole file decoded, which is
// seconds of processor time, so a pass is short and there are many of them
// rather than one long pass holding the single worker while nothing else runs.
const fingerprintBatch = 20

// FingerprintedCopies is what one backfill pass came to.
//
// Missing is counted rather than acted on. A copy whose bytes have gone from the
// download inbox cannot be measured by anything, and nothing about it is written
// down: it keeps saying exactly what it says now.
type FingerprintedCopies struct {
	Offered     int
	Measured    int
	Missing     int
	Unreadable  int
	MoreWaiting bool
}

// FingerprintHeldCopies measures the audio of the held copies that have none on
// record, a batch at a time, and reports whether more are waiting.
//
// A held copy is one file a peer sent for a want that nothing could decide
// about, kept as a question for a person. Every copy fetched from now on is
// fingerprinted while it is judged; these are the ones judged before there was
// anywhere to keep the number, and their files are still in the inbox.
//
// Nothing here decides anything, and there is nowhere for a decision to be
// added: the pass writes two columns and never a verdict, a summary or a piece
// of evidence. What the number is for is the review queue, which shows the
// copies that are one piece of audio as one row instead of eleven.
func (importer *Importer) FingerprintHeldCopies(ctx context.Context) (bool, error) {
	result, err := importer.fingerprintHeldCopies(ctx)
	return result.MoreWaiting, err
}

// fingerprintHeldCopies is FingerprintHeldCopies with the tally the caller does
// not need. The pass reports itself to the log; the numbers are returned so a
// test can read what one pass did.
func (importer *Importer) fingerprintHeldCopies(
	ctx context.Context,
) (FingerprintedCopies, error) {
	var result FingerprintedCopies
	waiting, err := importer.store.CopiesMissingFingerprint(ctx, fingerprintBatch)
	if err != nil {
		return result, err
	}
	result.Offered = len(waiting)
	result.MoreWaiting = len(waiting) == fingerprintBatch
	if len(waiting) == 0 {
		// Asked before the program is: an installation with nothing waiting is
		// not missing anything, whether or not it could fingerprint audio.
		return result, nil
	}
	if importer.prints == nil || !importer.prints.Available() {
		// Said once for the pass rather than once per copy. Without the program
		// that computes fingerprints there is nothing this pass can do, and it is
		// not a fault of any copy.
		return result, fmt.Errorf("%w: no copy can be fingerprinted", chromaprint.ErrNoFpcalc)
	}

	for _, copied := range waiting {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		source, ok := importer.copySource(copied)
		if !ok {
			result.Missing++
			continue
		}
		value, seconds, err := importer.prints.Compute(
			ctx, source, chromaprint.CandidateLengthSeconds)
		if err != nil || value == "" || seconds <= 0 {
			// Audio nothing could read is a fact about this copy and never a
			// reason to stop the pass. Nothing is written: no measurement is
			// silence, and silence is what the absent column already says.
			result.Unreadable++
			importer.logger.Warn().Err(err).Str("copy_id", copied.CopyID.String()).
				Msg("could not fingerprint a held copy")
			continue
		}
		if err := importer.store.RecordCopyFingerprint(
			ctx, copied.CopyID, value, seconds,
		); err != nil {
			return result, err
		}
		result.Measured++
	}
	importer.logger.Info().
		Int("offered", result.Offered).Int("measured", result.Measured).
		Int("missing", result.Missing).Int("unreadable", result.Unreadable).
		Msg("held copies were fingerprinted")
	return result, nil
}

// copySource is where this copy's bytes are, and whether they are still there.
//
// The path is derived the way inspectCandidate derives it and not some other
// way. A second derivation that disagreed by one component would look in a
// folder that is not there and count every copy as gone.
func (importer *Importer) copySource(copied db.UnfingerprintedCopyRow) (string, bool) {
	return importer.deliveredFile(copied.Provider, copied.SourceDirectory, copied.FileName)
}
