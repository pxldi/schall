// Package dedupe runs the duplicates screen's press on a schedule.
//
// It sits between the two rather than inside either: internal/library holds
// what a deletion is and internal/jobs holds when work runs, and jobs already
// reads library, so the thing that joins them has to live outside both.
package dedupe

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/jobs"
	"github.com/pxldi/schall/internal/library"
)

const duplicateSweepRemovalActor = "the duplicate-resolution sweep"

// DuplicateSweep keeps one copy of every recording the library holds more than
// once, on a schedule, where it can prove the copies are the same audio.
type DuplicateSweep struct {
	remover *library.Remover
	pool    *pgxpool.Pool
}

// NewDuplicateSweep builds it over the same remover the duplicates screen uses,
// so both paths record a removal licence before they unlink audio.
func NewDuplicateSweep(remover *library.Remover, pool *pgxpool.Pool) *DuplicateSweep {
	return &DuplicateSweep{remover: remover, pool: pool}
}

// SweepDuplicates makes one pass, or none.
//
// The setting is read here rather than by the worker, so that "switched off" is
// an answer this pass gives rather than a reason the job was never made. The
// pass still reschedules itself either way: turning the setting on has to be
// enough, without also having to find the button that queues the first pass.
func (sweep *DuplicateSweep) SweepDuplicates(ctx context.Context) (jobs.SweptDuplicates, error) {
	settings, err := db.New(sweep.pool).DuplicateResolutionSettings(ctx)
	if err != nil {
		// No row is no answer, and no answer is off — the same reading the
		// column default gives a fresh installation.
		return jobs.SweptDuplicates{Skipped: true}, nil
	}
	if !settings.Enabled {
		return jobs.SweptDuplicates{Skipped: true}, nil
	}

	swept, err := sweep.remover.KeepOneOfEach(ctx, duplicateSweepRemovalActor)
	if err != nil {
		return jobs.SweptDuplicates{}, fmt.Errorf("keep one copy of each recording: %w", err)
	}
	return jobs.SweptDuplicates{
		Resolved:  swept.Resolved,
		Removed:   len(swept.RemovedPaths),
		LeftAlone: swept.LeftAlone,
	}, nil
}

// QueueDuplicateSweep asks for a pass at a moment.
func (sweep *DuplicateSweep) QueueDuplicateSweep(ctx context.Context, runAfter time.Time) error {
	return db.New(sweep.pool).QueueDuplicateSweep(ctx, runAfter)
}
