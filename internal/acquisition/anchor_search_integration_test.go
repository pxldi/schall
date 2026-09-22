package acquisition

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/dbtest"
	"github.com/pxldi/schall/internal/sources"
)

// A want MusicBrainz has no recording for is searched on its anchor when that
// anchor can admit a copy, on a schedule of its own (ADR 0037 §1, §2).

// unresolvedAnchoredBy creates a want no resolver will answer and records an
// anchor from source on it.
func unresolvedAnchoredBy(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool, service *Service, source string,
) db.AcquisitionTargetRow {
	t.Helper()
	duration := int32(301_000)
	target, _, err := service.Create(ctx, Entry{
		Origin: "playlist", Artist: "Portishead", Title: "Glory Box", DurationMS: &duration,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := db.New(pool).RecordAnchor(ctx, db.AnchorParams{
		TargetID: target.ID, Fingerprint: "AQAAfingerprint", Source: source,
		Reference: "2178654", Seconds: 30,
	}); err != nil {
		t.Fatalf("RecordAnchor() error = %v", err)
	}
	anchored, err := service.Target(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	return anchored
}

func TestASweepSearchesAWantWithNoRecordingOnItsAnchor(t *testing.T) {
	for _, source := range []string{"deezer", "youtube-topic"} {
		t.Run(source, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			pool := dbtest.Setup(t)
			hunter := &fakeHunter{candidates: offering(sources.File{
				Path: `Music\Portishead\Dummy\05 Glory Box.flac`, Name: "05 Glory Box.flac",
				Extension: "flac", SizeBytes: 25_000_000, DurationSeconds: 301,
			})}
			fetcher := &fakeFetcher{}
			service := hunting(pool, hunter, fetcher, true)
			target := unresolvedAnchoredBy(ctx, t, pool, service, source)

			if err := service.Sweep(ctx); err != nil {
				t.Fatalf("Sweep() error = %v", err)
			}

			if len(hunter.texts) != 1 || hunter.texts[0][0] != "Portishead Glory Box" {
				t.Fatalf("searched %v, want the entry's own words", hunter.texts)
			}
			if len(fetcher.started) != 1 {
				t.Fatalf("started %d transfers, want the copy fetched", len(fetcher.started))
			}
			after, err := service.Target(ctx, target.ID)
			if err != nil {
				t.Fatal(err)
			}
			if after.Status != "unresolved" || !after.NextSearchAt.Valid ||
				!after.NextSearchAt.Time.After(time.Now().Add(20*time.Minute)) {
				t.Errorf("want = %s search %v, want it unresolved and waiting on the copy",
					after.Status, after.NextSearchAt)
			}
			if !after.NextAttemptAt.Time.Equal(target.NextAttemptAt.Time) {
				t.Errorf("next_attempt_at = %v, want resolution's schedule left at %v",
					after.NextAttemptAt.Time, target.NextAttemptAt.Time)
			}
		})
	}
}

// A search that finds nothing waits out the unfound ladder: a day first.
func TestASearchOnAnAnchorThatFindsNothingWaitsADay(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	hunter := &fakeHunter{}
	service := hunting(pool, hunter, &fakeFetcher{}, true)
	target := unresolvedAnchoredBy(ctx, t, pool, service, "deezer")

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	after, err := service.Target(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	wait := time.Until(after.NextSearchAt.Time)
	if after.SearchAttempts != 1 || wait < 23*time.Hour || wait > 25*time.Hour {
		t.Errorf("search %d, next in %v, want one search counted and a day's wait",
			after.SearchAttempts, wait)
	}
	due, err := service.NextDue(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if due.IsZero() || due.After(after.NextSearchAt.Time) {
		t.Errorf("NextDue() = %v, want the sweeper woken by %v at the latest",
			due, after.NextSearchAt.Time)
	}
}

// A name-found upload cannot admit (ADR 0034), so searching on it would only
// fill the review queue.
func TestAWantAnchoredByANameFoundUploadIsNeverSearched(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	hunter := &fakeHunter{}
	service := hunting(pool, hunter, &fakeFetcher{}, true)
	target := unresolvedAnchoredBy(ctx, t, pool, service, "youtube")

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	if looked, err := service.attempt(ctx, target, nil); err != nil || looked {
		t.Fatalf("attempt() = %v, %v, want nothing looked for", looked, err)
	}

	if len(hunter.texts) != 0 {
		t.Fatalf("searched %v, want nothing asked", hunter.texts)
	}
	if target.NextSearchAt.Valid {
		t.Errorf("next_search_at = %v, want no search scheduled", target.NextSearchAt)
	}
}
