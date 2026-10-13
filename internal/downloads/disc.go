package downloads

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	"github.com/pxldi/schall/internal/accuraterip"
	"github.com/pxldi/schall/internal/db"
)

// DiscChecker looks a copy up in AccurateRip under the CDs MusicBrainz lists
// its recording on. See accuraterip.Checker.
type DiscChecker interface {
	Available() bool
	Check(ctx context.Context, recordingID uuid.UUID, path string) (accuraterip.Answer, error)
}

// WithDiscChecker registers what looks lossless copies up in AccurateRip.
// Without one no copy is looked up, and a copy that was not is judged exactly
// as it was before.
func (importer *Importer) WithDiscChecker(checker DiscChecker) *Importer {
	importer.discs = checker
	return importer
}

// cdLossless is the containers a CD rip arrives in. A lossy file never holds
// a CD's samples, so decoding one to sum it would only cost time.
var cdLossless = map[string]bool{
	".flac": true, ".wav": true, ".aif": true, ".aiff": true, ".wv": true, ".ape": true,
}

const notCDAudio = "Not 44.1 kHz stereo, so not a CD rip"

// measureAgainstDisc looks the copy up in AccurateRip and writes down what
// came of it.
//
// Only a lossless copy at a CD's 44.1 kHz in stereo is looked up. Everything
// that finds nothing is silence: a copy of a CD nobody filed in MusicBrainz, a
// download that was never on a CD, a rip with its drive's offset left
// uncorrected. Those are written down so the person reading the copy can see
// the check ran.
func (importer *Importer) measureAgainstDisc(
	ctx context.Context, row db.TargetImportRow, source string,
	evidence *db.AcquiredFileEvidence,
) {
	if importer.discs == nil || !importer.discs.Available() ||
		row.MusicBrainzRecordingID == uuid.Nil || witnessAdmits(evidence) ||
		!cdLossless[strings.ToLower(filepath.Ext(source))] {
		return
	}
	// A lossless copy that cannot be a CD track is written down as looked at,
	// so judging copies again does not offer it for this check a second time.
	properties, err := importer.properties(source)
	if err != nil || properties.SampleRateHz != 44100 || properties.Channels != 2 {
		evidence.Disc = &db.ImportDisc{Silence: notCDAudio}
		return
	}
	answer, err := importer.discs.Check(ctx, row.MusicBrainzRecordingID, source)
	if err != nil {
		evidence.Disc = &db.ImportDisc{Unavailable: "AccurateRip or MusicBrainz didn't answer"}
		importer.logger.Warn().Err(err).Str("file", evidence.Name).
			Str("musicbrainz_recording_id", row.MusicBrainzRecordingID.String()).
			Msg("could not look a copy up in accuraterip")
		return
	}
	if answer.Match == nil {
		evidence.Disc = &db.ImportDisc{Silence: answer.Silence}
		return
	}
	evidence.Disc = &db.ImportDisc{
		Matched: true, ReleaseID: answer.Match.ReleaseID.String(),
		Medium: answer.Match.Medium, Track: answer.Match.Track,
		DiscID: answer.Match.DiscID, Confidence: answer.Match.Confidence,
	}
}
