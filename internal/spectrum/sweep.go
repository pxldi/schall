package spectrum

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/pxldi/schall/internal/db"
	"github.com/rs/zerolog"
)

// sweepBatch is how many files one pass decodes.
//
// Decoding is the most expensive thing Schall does to a file it already holds,
// and nobody is waiting on any of it, so a pass is short and there are many of
// them rather than one long pass holding a worker while a person waits for a
// scan or an import behind it.
const sweepBatch = 25

// Store is what the sweep reads and writes. It is the whole of this package's
// reach into the database.
type Store interface {
	FilesToMeasure(ctx context.Context, limit int32) ([]db.FileToMeasureRow, error)
	RecordFileSpectrum(ctx context.Context, params db.FileSpectrumParams) error
}

// Analyser looks at one file's audio.
type Analyser interface {
	Available() bool
	// Binary names the program the measurement runs, for the one log line said
	// when it cannot be found.
	Binary() string
	ReadWithBitRate(
		ctx context.Context, path string, sampleRateHz, durationMS, bitRateKbps int,
	) (Reading, error)
}

// Service measures the library's audio, a batch at a time.
type Service struct {
	store  Store
	reader Analyser
	logger zerolog.Logger
	// toldNobodyCanMeasure keeps the pass from saying the same thing every few
	// minutes. ffmpeg is optional, an installation without it will never
	// measure anything, and the pass goes on being scheduled — so the fact is
	// worth one line in the log and not one line per pass.
	toldNobodyCanMeasure atomic.Bool
}

func NewService(store Store, reader Analyser, logger zerolog.Logger) *Service {
	return &Service{store: store, reader: reader, logger: logger}
}

// Sweep measures the files that have never been looked at and reports whether
// there are more waiting.
//
// A pass never fails for one file. A file that will not decode is a fact about
// that file, written down as a look that concluded nothing, and the pass carries
// on — only a database that will not answer ends a pass early.
//
// A machine with no decoder at all is not a failure either. Nothing here can be
// measured on it, and retrying a job that reliably cannot run would fail it
// three times, log an error each time, and land it on the six-hour retry
// forever — for a fact about the machine that will not be different in six
// hours. It is said once in the log instead, and the pass completes with
// nothing done, exactly as it would if the library held nothing left to look
// at.
func (service *Service) Sweep(ctx context.Context) (bool, error) {
	if !service.reader.Available() {
		if service.toldNobodyCanMeasure.CompareAndSwap(false, true) {
			service.logger.Info().Str("binary", service.reader.Binary()).
				Msg("audio will not be measured: ffmpeg could not be found")
		}
		return false, nil
	}
	files, err := service.store.FilesToMeasure(ctx, sweepBatch)
	if err != nil {
		return false, fmt.Errorf("list the files still to be measured: %w", err)
	}
	for _, file := range files {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		if err := service.measure(ctx, file); err != nil {
			// The decoder can also vanish between the check above and this
			// file — a package removed, a path unmounted. It is the same fact
			// as failing the check up front and is met the same way: said once,
			// and the pass ends clean rather than failing for a machine problem
			// no retry fixes.
			if errors.Is(err, ErrUnavailable) {
				if service.toldNobodyCanMeasure.CompareAndSwap(false, true) {
					service.logger.Info().Str("binary", service.reader.Binary()).
						Msg("audio will not be measured: ffmpeg could not be found")
				}
				return false, nil
			}
			return false, err
		}
	}
	return len(files) == sweepBatch, nil
}

// measure looks at one file and writes down what it found. The error it returns
// is a failure to write, or a decoder that is not there; what the audio turned
// out to be is never an error.
func (service *Service) measure(ctx context.Context, file db.FileToMeasureRow) error {
	reading, err := service.reader.ReadWithBitRate(
		ctx, file.Path, file.SampleRateHz, file.DurationMS, file.BitRateKbps)
	if err != nil {
		return fmt.Errorf("measure the audio in %s: %w", file.Path, err)
	}
	if reading.TranscodeSuspected {
		service.logger.Info().
			Str("library_file_id", file.ID.String()).
			Int("cutoff_hz", reading.CutoffHz).
			Msg("a copy's audio stops where a lossy encoder would have stopped it")
	}
	return service.store.RecordFileSpectrum(ctx, db.FileSpectrumParams{
		FileID:             file.ID,
		Judged:             reading.Judged,
		CutoffHz:           reading.CutoffHz,
		Encoder:            reading.Encoder,
		TranscodeSuspected: reading.TranscodeSuspected,
	})
}
