package jobs

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/db"
	"github.com/rs/zerolog"
)

// A pass that put a batch of refreshes on the queue waits for them to run
// before asking again. Coming straight back would find the same artists, whose
// refreshes have not run yet, and queue nothing over and over.
func TestAGenrePassWithMoreWaitingWaitsForTheRefreshesToRun(t *testing.T) {
	jobID := uuid.New()
	queue := &fakeQueue{genreBackfill: db.BackfillGenresRow{WaitingCount: 25, QueuedCount: 25}}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop())
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	worker.now = func() time.Time { return now }

	worker.process(context.Background(), Job{
		ID: jobID, Kind: SweepGenres, Attempts: 1, MaxAttempts: 3,
	})

	if queue.completed != jobID {
		t.Fatalf("completed = %s, want the pass finished", queue.completed)
	}
	if !queue.genreSweepQueuedFor.Equal(now.Add(genresPace)) {
		t.Fatalf("next pass = %s; want one in %s", queue.genreSweepQueuedFor, genresPace)
	}
}

// A pass with nobody left to ask about comes back in half a day, for whatever
// entered the catalogue since.
func TestAGenrePassWithNobodyLeftWaits(t *testing.T) {
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop())
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	worker.now = func() time.Time { return now }

	worker.process(context.Background(), Job{
		ID: uuid.New(), Kind: SweepGenres, Attempts: 1, MaxAttempts: 3,
	})

	if !queue.genreSweepQueuedFor.Equal(now.Add(genresIdle)) {
		t.Fatalf("next pass = %s; want one in %s", queue.genreSweepQueuedFor, genresIdle)
	}
}

// A pass that spent its last attempt still puts its replacement on the books,
// so a database in difficulty does not cost the backfill for good.
func TestAGenrePassThatSpentItsAttemptsStillQueuesTheNextOne(t *testing.T) {
	queue := &fakeQueue{backfillGenresErr: errors.New("the database is not answering")}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop())
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	worker.now = func() time.Time { return now }

	worker.process(context.Background(), Job{
		ID: uuid.New(), Kind: SweepGenres, Attempts: 3, MaxAttempts: 3,
	})

	if !queue.genreSweepQueuedFor.Equal(now.Add(genresIdle)) {
		t.Fatalf("next pass = %s; want one in %s", queue.genreSweepQueuedFor, genresIdle)
	}
}

// A pass with attempts left is retried by the queue rather than rescheduled by
// hand, so it does not queue a second pass beside the retry.
func TestAGenrePassWithAttemptsLeftIsRetriedRatherThanRescheduled(t *testing.T) {
	queue := &fakeQueue{backfillGenresErr: errors.New("the database is not answering")}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop())

	worker.process(context.Background(), Job{
		ID: uuid.New(), Kind: SweepGenres, Attempts: 1, MaxAttempts: 3,
	})

	if !queue.genreSweepQueuedFor.IsZero() {
		t.Fatalf("next pass = %s; want the retry to be the next pass",
			queue.genreSweepQueuedFor)
	}
}
