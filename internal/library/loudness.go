package library

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	"github.com/pxldi/schall/internal/loudness"
)

// The library was indexed long before there was anywhere to record how loud a
// file is, so almost every file in it has no measurement. A file arriving today
// is measured as it is imported; this is the pass that catches up with
// everything that arrived before.
//
// It is deliberately a pass of its own rather than something the scanner does
// on the way past. Every other thing the scanner reads off a file is read out
// of its headers in a few microseconds; this one decodes the whole stream,
// which for one file is a second or two and for a library is hours. A scan that
// did it would be a scan nobody could run.

// loudnessBatch is how many files one pass measures before it goes back to the
// queue. It is small because each file is a whole decode, and because the
// worker running it is one goroutine working every other kind of job in turn: a
// batch of a hundred would park everything else for minutes. A batch that finds
// more waiting queues its own replacement immediately, so a backlog is still
// worked through at full speed and the worker gets a turn between batches.
const loudnessBatch = 10

// Loudness measures how loud the files already in the library are.
type Loudness struct {
	pool     *pgxpool.Pool
	measurer *loudness.Measurer
	logger   zerolog.Logger
	// toldNobodyCanMeasure keeps the pass from saying the same thing every few
	// minutes. ffmpeg is optional, an installation without it will never
	// measure anything, and the pass goes on being scheduled — so the fact is
	// worth one line in the log and not one line per pass.
	toldNobodyCanMeasure atomic.Bool
}

func NewLoudness(pool *pgxpool.Pool, measurer *loudness.Measurer, logger zerolog.Logger) *Loudness {
	return &Loudness{pool: pool, measurer: measurer, logger: logger}
}

// unmeasured is one file the pass has not listened to yet.
type unmeasured struct {
	fileID uuid.UUID
	path   string
}

// Sweep measures one batch and reports whether there is more of the library
// left to measure.
//
// A file that gives no answer is recorded as measured and left empty, so the
// pass asks once rather than on every turn. That covers both silences: audio
// too short or too quiet to measure, and a file that could not be read at all.
//
// The whole pass stops the moment ffmpeg turns out to be missing. Nothing can
// be measured on that installation, and walking the rest of the library to fail
// on every file of it is work with no answer at the end.
func (sweep *Loudness) Sweep(ctx context.Context) (bool, error) {
	files, err := sweep.next(ctx)
	if err != nil {
		return false, err
	}
	if len(files) == 0 {
		return false, nil
	}

	measured := 0
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			// The process is stopping. Nothing is written down for a file the
			// measurement was cut off part way through: this pass simply ends,
			// and the file is still waiting when the next one starts.
			return false, nil
		}
		reading, err := sweep.measurer.Measure(ctx, file.path)
		switch {
		case errors.Is(err, loudness.ErrUnavailable):
			if sweep.toldNobodyCanMeasure.CompareAndSwap(false, true) {
				sweep.logger.Info().Str("binary", sweep.measurer.Binary()).
					Msg("loudness will not be measured: ffmpeg could not be found")
			}
			return false, nil
		case err != nil:
			// One file that gave no answer is not a failed pass. The library
			// holds files the operator put there by hand and files a scan has
			// not caught up with, and a pass that stopped at the first of them
			// would never reach the rest of the library.
			//
			// It is written down as asked all the same, and that is what keeps
			// the pass moving. This query takes the files nothing has measured,
			// so a file left unrecorded is a file the very next batch reads
			// again — and ten unreadable files at the head of the library would
			// be a pass that measures them, learns nothing, asks for the next
			// batch immediately, and gets the same ten forever. A scan that
			// finds the file changed clears the record and it is measured
			// again.
			sweep.logger.Debug().Err(err).Str("path", file.path).
				Msg("measure how loud a file is")
			if err := sweep.recordNothing(ctx, file.fileID); err != nil {
				return false, err
			}
		default:
			if err := sweep.record(ctx, file.fileID, reading); err != nil {
				return false, err
			}
			measured++
		}
	}
	if measured > 0 {
		sweep.logger.Info().Int("measured", measured).Int("looked", len(files)).
			Msg("loudness measured")
	}
	return len(files) == loudnessBatch, nil
}

// next reads the files nothing has listened to.
//
// Order is by identifier, which nothing changes, so a pass killed halfway does
// not start again from the top. There is no cursor because there is no need for
// one: a measured file stops matching this query, so the next pass begins where
// this one left off by definition.
func (sweep *Loudness) next(ctx context.Context) ([]unmeasured, error) {
	rows, err := sweep.pool.Query(ctx, `
		SELECT id, path
		FROM library_files
		WHERE missing_at IS NULL
		  AND loudness_measured_at IS NULL
		ORDER BY id
		LIMIT $1
	`, loudnessBatch)
	if err != nil {
		return nil, fmt.Errorf("read the files nothing has measured: %w", err)
	}
	defer rows.Close()

	var files []unmeasured
	for rows.Next() {
		var file unmeasured
		if err := rows.Scan(&file.fileID, &file.path); err != nil {
			return nil, err
		}
		files = append(files, file)
	}
	return files, rows.Err()
}

func (sweep *Loudness) record(
	ctx context.Context, fileID uuid.UUID, reading loudness.Measured,
) error {
	_, err := sweep.pool.Exec(ctx, `
		UPDATE library_files
		SET replaygain_track_gain = $2,
		    replaygain_track_peak = nullif($3::double precision, 0),
		    loudness_measured_at = now(),
		    updated_at = now()
		WHERE id = $1
	`, fileID, reading.GainDB, reading.Peak)
	if err != nil {
		return fmt.Errorf("record how loud %s is: %w", fileID, err)
	}
	return nil
}

// recordNothing writes down that the file was listened to and had nothing to
// say. The two gain columns stay null, which is what they already were, and the
// tagger goes on writing no gain into that file.
func (sweep *Loudness) recordNothing(ctx context.Context, fileID uuid.UUID) error {
	_, err := sweep.pool.Exec(ctx, `
		UPDATE library_files
		SET loudness_measured_at = now(), updated_at = now()
		WHERE id = $1
	`, fileID)
	if err != nil {
		return fmt.Errorf("record that %s was measured: %w", fileID, err)
	}
	return nil
}
