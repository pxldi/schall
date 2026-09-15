package db

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/dbtest"
)

// MusicBrainz merges recordings: two rows that turn out to describe one
// performance become one, and the retired identifier keeps resolving through a
// redirect. A file tagged before a merge carries the retired one for ever.
func TestARetiredIdentifierAnswersWithTheSurvivingOne(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	retired, surviving := uuid.New(), uuid.New()
	if err := queries.RecordRecordingAlias(ctx, retired, surviving); err != nil {
		t.Fatalf("RecordRecordingAlias() error = %v", err)
	}

	canonical, err := queries.CanonicalRecordingIDs(ctx, []uuid.UUID{retired})
	if err != nil {
		t.Fatalf("CanonicalRecordingIDs() error = %v", err)
	}
	if canonical[retired] != surviving {
		t.Fatalf("%s answers %s, want %s", retired, canonical[retired], surviving)
	}
}

// An identifier nothing was merged into is its own answer. Every input comes
// back, so no caller has to check whether it did.
func TestAnIdentifierNothingWasMergedIntoIsItsOwnAnswer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	untouched := uuid.New()
	canonical, err := New(pool).CanonicalRecordingIDs(ctx, []uuid.UUID{untouched})
	if err != nil {
		t.Fatalf("CanonicalRecordingIDs() error = %v", err)
	}
	if canonical[untouched] != untouched {
		t.Fatalf("%s answers %s, want itself", untouched, canonical[untouched])
	}
}

// The comparisons read the set of identifiers that answer for one recording,
// rather than canonicalising both sides, so that the index on the track's
// identifier survives. The set has to hold the survivor and every identifier
// merged into it, whichever end it is asked from.
func TestEveryIdentifierThatAnswersForARecordingIsFoundFromEitherEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	first, second, surviving := uuid.New(), uuid.New(), uuid.New()
	for _, retired := range []uuid.UUID{first, second} {
		if err := queries.RecordRecordingAlias(ctx, retired, surviving); err != nil {
			t.Fatalf("RecordRecordingAlias() error = %v", err)
		}
	}

	for name, asked := range map[string]uuid.UUID{
		"asked with the surviving identifier": surviving,
		"asked with a retired one":            first,
	} {
		t.Run(name, func(t *testing.T) {
			rows, err := pool.Query(ctx, `SELECT recording_id_aliases($1)`, asked)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			found := make(map[uuid.UUID]bool)
			for rows.Next() {
				var id uuid.UUID
				if err := rows.Scan(&id); err != nil {
					t.Fatal(err)
				}
				found[id] = true
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			if !found[surviving] || !found[first] || !found[second] || len(found) != 3 {
				t.Fatalf("answers = %v, want all three identifiers of the one recording", found)
			}
		})
	}
}

// A recording nothing was merged into answers to itself and to nothing else.
// The whole safety of this rests on it: an identifier with no row here is
// compared exactly as it was before, so a genuinely different recording still
// contradicts.
func TestARecordingNothingWasMergedIntoAnswersToItselfAlone(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	alone := uuid.New()
	var answers int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM recording_id_aliases($1)`, alone).Scan(&answers); err != nil {
		t.Fatal(err)
	}
	if answers != 1 {
		t.Fatalf("%d answers, want the identifier itself and nothing else", answers)
	}
}

// The row is a copy of the provider's answer. Asking again cannot make an
// answer more true, so the first one stands and the timestamp keeps meaning
// when Schall learned it.
func TestRememberingAMergeTwiceKeepsTheFirstAnswer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	retired, surviving := uuid.New(), uuid.New()
	if err := queries.RecordRecordingAlias(ctx, retired, surviving); err != nil {
		t.Fatal(err)
	}
	var first time.Time
	if err := pool.QueryRow(ctx,
		`SELECT observed_at FROM recording_aliases WHERE retired_id = $1`, retired).Scan(&first); err != nil {
		t.Fatal(err)
	}
	if err := queries.RecordRecordingAlias(ctx, retired, surviving); err != nil {
		t.Fatal(err)
	}

	var again time.Time
	var rows int
	if err := pool.QueryRow(ctx, `
		SELECT count(*), max(observed_at) FROM recording_aliases WHERE retired_id = $1
	`, retired).Scan(&rows, &again); err != nil {
		t.Fatal(err)
	}
	if rows != 1 || !again.Equal(first) {
		t.Fatalf("%d rows, observed at %s, want one row still observed at %s", rows, again, first)
	}
}

// A recording is never its own alias. It would say nothing and it would make
// the canonical lookup a loop.
func TestARecordingIsNeverItsOwnAlias(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	same := uuid.New()
	if err := New(pool).RecordRecordingAlias(ctx, same, same); err != nil {
		t.Fatalf("RecordRecordingAlias() error = %v, want it refused quietly", err)
	}
	var rows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM recording_aliases`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("%d rows, want none", rows)
	}
}
