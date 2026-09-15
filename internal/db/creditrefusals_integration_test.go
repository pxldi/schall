package db

import (
	"context"
	"testing"
	"time"

	"github.com/pxldi/schall/internal/dbtest"
)

// A credit is compared as the set of artists it names, and a credit that
// disagrees no longer voids a pair the audio identified (docs/decisions/0030).
// Both bear on the copies refused on the artist tag alone, and what that pass is
// allowed to offer is the whole of its safety. These tests are that list.

// creditOnly is the evidence of a copy discarded because the artist tag was the
// one thing that disagreed. It is the shape StopAfterCreditOnlyRefusals reads.
const creditOnly = `{"differs": ["artist"], "agrees": ["title", "duration"]}`

// A copy refused on the artist tag alone is offered again.
func TestACopyRefusedOnTheCreditAloneIsOfferedAgain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
	settledCopy(ctx, t, pool, targetID, requestID,
		AcquiredFileDiscardedTags, creditOnly)

	offered, err := queries.CopiesRefusedOnCreditAlone(ctx)
	if err != nil {
		t.Fatalf("CopiesRefusedOnCreditAlone() error = %v", err)
	}
	if len(offered) != 1 || offered[0].RequestID != requestID {
		t.Fatalf("offered = %+v, want the copy back", offered)
	}
	if offered[0].SourceDirectory != "Music/Anetha" || offered[0].FileName != "03.flac" {
		t.Errorf("offered = %+v, want where its bytes are", offered[0])
	}
}

// A copy something else disagreed about is left alone. The credit was not the
// reason it was refused, so neither rule that changed reaches it.
func TestACopyRefusedOnMoreThanTheCreditIsNotOffered(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
	settledCopy(ctx, t, pool, targetID, requestID, AcquiredFileDiscardedTags,
		`{"differs": ["artist", "duration (9 seconds out)"]}`)

	offered, err := queries.CopiesRefusedOnCreditAlone(ctx)
	if err != nil {
		t.Fatalf("CopiesRefusedOnCreditAlone() error = %v", err)
	}
	if len(offered) != 0 {
		t.Fatalf("offered = %+v, want the other disagreement left standing", offered)
	}
}

// A copy whose bytes have gone is left alone. Nothing can import it, and
// re-opening it would overwrite what it says with a wrong account of why nobody
// can decide.
func TestACopyWhoseFileIsGoneIsNotOffered(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
	settledCopy(ctx, t, pool, targetID, requestID,
		AcquiredFileDiscardedTags, creditOnly)
	if _, err := pool.Exec(ctx, `
		UPDATE acquisition_target_files SET file_removed_at = now()
		WHERE acquisition_target_id = $1
	`, targetID); err != nil {
		t.Fatalf("record the file as gone: %v", err)
	}

	offered, err := queries.CopiesRefusedOnCreditAlone(ctx)
	if err != nil {
		t.Fatalf("CopiesRefusedOnCreditAlone() error = %v", err)
	}
	if len(offered) != 0 {
		t.Fatalf("offered = %+v, want a copy nothing can import left alone", offered)
	}
}

// A want stopped and waiting on a person is offered too. Its copies are what
// somebody was going to be asked about, and answering them without asking is
// what this pass is for.
func TestACopyOfAWantAwaitingReviewIsOffered(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
	settledCopy(ctx, t, pool, targetID, requestID,
		AcquiredFileDiscardedTags, creditOnly)
	if _, err := pool.Exec(ctx, `
		UPDATE acquisition_targets
		SET status = 'awaiting_review', review_reason = 'resolution',
		    next_attempt_at = NULL
		WHERE id = $1
	`, targetID); err != nil {
		t.Fatalf("stop the want: %v", err)
	}

	offered, err := queries.CopiesRefusedOnCreditAlone(ctx)
	if err != nil {
		t.Fatalf("CopiesRefusedOnCreditAlone() error = %v", err)
	}
	if len(offered) != 1 {
		t.Fatalf("offered = %+v, want the stopped want's copy back", offered)
	}
}

// A copy somebody decided about by hand is never offered. A manual decision is
// permanent, whatever rule changed afterwards.
func TestACopyDecidedByHandIsNotOfferedForItsCredit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
	settledCopy(ctx, t, pool, targetID, requestID,
		AcquiredFileDiscardedTags, creditOnly)
	if _, err := pool.Exec(ctx, `
		UPDATE acquisition_target_files SET decided_by = 'user'
		WHERE acquisition_target_id = $1
	`, targetID); err != nil {
		t.Fatalf("record the decision as a person's: %v", err)
	}

	offered, err := queries.CopiesRefusedOnCreditAlone(ctx)
	if err != nil {
		t.Fatalf("CopiesRefusedOnCreditAlone() error = %v", err)
	}
	if len(offered) != 0 {
		t.Fatalf("offered = %+v, want a person's decision left standing", offered)
	}
}

// A want that already has its music offers nothing. Validation imports what it
// proves without asking whether the want was answered since.
func TestAWantWithItsMusicOffersNoCreditRefusals(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
	settledCopy(ctx, t, pool, targetID, requestID,
		AcquiredFileDiscardedTags, creditOnly)
	if _, err := pool.Exec(ctx, `
		INSERT INTO acquisition_target_files (
			acquisition_target_id, provider, source_username, remote_path,
			file_name, size_bytes, verdict, decided_by, summary, evidence
		)
		VALUES ($1, 'slskd', 'peer', $2, '04.flac', 9000000, 'accepted', 'schall',
		        'proven by its audio', '{}'::jsonb)
	`, targetID, `Music\Anetha\04.flac`); err != nil {
		t.Fatalf("record the accepted copy: %v", err)
	}

	offered, err := queries.CopiesRefusedOnCreditAlone(ctx)
	if err != nil {
		t.Fatalf("CopiesRefusedOnCreditAlone() error = %v", err)
	}
	if len(offered) != 0 {
		t.Fatalf("offered = %+v, want nothing for a want that has its music", offered)
	}
}
