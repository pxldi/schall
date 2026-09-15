package downloads

import (
	"context"
	"errors"
	"sync/atomic"

	"github.com/pxldi/schall/internal/loudness"
	"github.com/pxldi/schall/internal/tagging"
)

// An import ends by writing what Schall proved the files to be. How loud they
// are is written at the same moment and by the same write.
//
// It is measured here rather than left to the pass that walks the library
// because this is the one moment the whole record is in hand at once. Album
// gain is the loudness of the record played end to end, and that can only be
// worked out where every track of it is present and measured — which is exactly
// what an import of a release is. The library pass measures a file at a time
// and reaches the same answer later; this reaches it immediately.
//
// Nothing here decides anything. A loudness is not evidence of what a file is:
// two unrelated recordings mastered by the same engineer measure the same, and
// nothing in matching, identity or import validation reads these numbers. They
// are written for the music server, beside the release identifiers, and they
// are written only into files the import already committed.

// WithLoudness registers what measures how loud a file is. Without it an import
// writes no replay gain and the file keeps whatever gain the peer shipped,
// which is what every import did until now.
func (importer *Importer) WithLoudness(measurer *loudness.Measurer) *Importer {
	importer.loud = measurer
	return importer
}

// measureRelease measures every imported file of one release and answers with
// what to write into each of them, by path.
//
// tracks is how many tracks the release has. The album's own gain is written
// only where that many files were imported and every one of them measured: a
// record levelled from nine of its twelve tracks is the loudness of a record
// that does not exist, and a player following it would level the whole album by
// it. Where the record is not all here, each file carries its own gain and no
// album gain, which is what a player falls back to.
func (importer *Importer) measureRelease(
	ctx context.Context, paths []string, tracks int,
) map[string]*tagging.ReplayGain {
	if importer.loud == nil || len(paths) == 0 {
		return nil
	}

	measured := make(map[string]loudness.TrackOf, len(paths))
	for _, path := range paths {
		reading, err := importer.loud.Measure(ctx, path)
		if err != nil {
			importer.noteUnmeasured(path, err)
			continue
		}
		// The length is read off the stream by the same library that reads the
		// tags, which is a parse of the headers rather than a second decode.
		// The album is weighted by it: a two-minute track is less of a record
		// than a ten-minute one.
		properties, _ := tagging.ReadProperties(path)
		measured[path] = loudness.TrackOf{Measured: reading, DurationMS: properties.DurationMS}
	}

	gains := make(map[string]*tagging.ReplayGain, len(measured))
	for path, track := range measured {
		gains[path] = &tagging.ReplayGain{TrackGainDB: track.GainDB, TrackPeak: track.Peak}
	}
	if tracks <= 0 || len(paths) != tracks || len(measured) != tracks {
		return gains
	}

	whole := make([]loudness.TrackOf, 0, len(measured))
	for _, track := range measured {
		whole = append(whole, track)
	}
	album, ok := loudness.AlbumOf(whole)
	if !ok {
		return gains
	}
	for _, gain := range gains {
		gain.Album = &tagging.AlbumGain{GainDB: album.GainDB, Peak: album.Peak}
	}
	return gains
}

// toldNobodyCanMeasure keeps an installation with no ffmpeg from saying so on
// every import. It is a fact about the installation, not about the file, and it
// is worth one line in the log for as long as the process runs.
var toldNobodyCanMeasure atomic.Bool

// noteUnmeasured records that a file has no loudness, and carries on. The
// import is committed and the bytes are in the library by the time this runs: a
// file nobody could measure is a file with no replay gain, which is every file
// in every library that has never been measured.
func (importer *Importer) noteUnmeasured(path string, err error) {
	if errors.Is(err, loudness.ErrUnavailable) {
		if toldNobodyCanMeasure.CompareAndSwap(false, true) {
			importer.logger.Info().Str("binary", importer.loud.Binary()).
				Msg("loudness will not be measured: ffmpeg could not be found")
		}
		return
	}
	importer.logger.Debug().Err(err).Str("path", path).
		Msg("measure how loud an imported file is")
}
