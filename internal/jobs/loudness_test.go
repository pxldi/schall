package jobs

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// fakeLoudness is a loudness pass that answers however the test needs it to.
type fakeLoudness struct {
	more bool
	err  error
	runs int
}

func (loud *fakeLoudness) Sweep(context.Context) (bool, error) {
	loud.runs++
	return loud.more, loud.err
}

func TestALoudnessPassWithMoreToMeasureComesStraightBack(t *testing.T) {
	jobID := uuid.New()
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithLoudnessSweeper(&fakeLoudness{more: true})
	now := time.Date(2026, 8, 27, 9, 0, 0, 0, time.UTC)
	worker.now = func() time.Time { return now }

	worker.process(context.Background(), Job{
		ID: jobID, Kind: SweepLoudness, Attempts: 1, MaxAttempts: 3,
	})

	if queue.completed != jobID || !queue.loudnessSweepQueuedFor.Equal(now) {
		t.Fatalf("completed = %s, next pass = %s, want one due now",
			queue.completed, queue.loudnessSweepQueuedFor)
	}
}

func TestALoudnessPassWithNothingLeftWaits(t *testing.T) {
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithLoudnessSweeper(&fakeLoudness{})
	now := time.Date(2026, 8, 27, 9, 0, 0, 0, time.UTC)
	worker.now = func() time.Time { return now }

	worker.process(context.Background(), Job{
		ID: uuid.New(), Kind: SweepLoudness, Attempts: 1, MaxAttempts: 3,
	})

	if !queue.loudnessSweepQueuedFor.Equal(now.Add(loudnessIdle)) {
		t.Fatalf("next pass = %s, want it %s later", queue.loudnessSweepQueuedFor, loudnessIdle)
	}
}

func TestALoudnessPassOnItsLastAttemptStillSchedulesAnother(t *testing.T) {
	// A pass that ended badly must not be the last one. Nothing else queues
	// this job, so a run that failed out of its attempts with no successor
	// would leave the library unmeasured until the next restart.
	jobID := uuid.New()
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithLoudnessSweeper(&fakeLoudness{err: errors.New("the database went away")})
	now := time.Date(2026, 8, 27, 9, 0, 0, 0, time.UTC)
	worker.now = func() time.Time { return now }

	worker.process(context.Background(), Job{
		ID: jobID, Kind: SweepLoudness, Attempts: 3, MaxAttempts: 3,
	})

	if queue.failed != jobID {
		t.Fatalf("failed = %s, want the pass recorded as failed", queue.failed)
	}
	if !queue.loudnessSweepQueuedFor.Equal(now.Add(loudnessIdle)) {
		t.Fatalf("next pass = %s, want one %s later", queue.loudnessSweepQueuedFor, loudnessIdle)
	}
}

func TestALoudnessPassWithNothingToRunItFails(t *testing.T) {
	jobID := uuid.New()
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop())

	worker.process(context.Background(), Job{
		ID: jobID, Kind: SweepLoudness, Attempts: 1, MaxAttempts: 3,
	})

	if queue.failed != jobID {
		t.Fatalf("failed = %s, want the pass refused", queue.failed)
	}
}
