package acquisition

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/dbtest"
	"github.com/pxldi/schall/internal/sources"
)

// oneCopyOnOffer is a peer sharing a copy of what the want is looking for. It is
// a different file from the one heldCopy already recorded, so the want passing
// it over would be this rule and not the ordinary refusal to fetch a copy it has
// already judged.
func oneCopyOnOffer() *fakeHunter {
	return &fakeHunter{candidates: offering(sources.File{
		Path: `Music\Portishead\Dummy\09 Glory Box.flac`, Name: "09 Glory Box.flac",
		Extension: "flac", SizeBytes: 25_000_000, DurationSeconds: 301,
	})}
}

// dueNow brings a want forward, the way settling a copy for it does.
func dueNow(ctx context.Context, t *testing.T, pool *pgxpool.Pool, targetID uuid.UUID) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		UPDATE acquisition_targets SET next_attempt_at = now() WHERE id = $1
	`, targetID); err != nil {
		t.Fatalf("bring the want forward: %v", err)
	}
}

// A want holding a copy nobody has answered, with no anchor to answer it with,
// is not searched for again. On 2026-09-08 the queue held 1,941 copies over 727
// wants and 1,278 of them were the second or later copy of a want that was
// already asking a question. Nothing but AcoustID could admit any of them, and
// AcoustID had already failed on the first.
func TestASweepDoesNotFetchForAWantHoldingACopyNobodyHasAnswered(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	hunter, fetcher := oneCopyOnOffer(), &fakeFetcher{}
	service := hunting(pool, hunter, fetcher, true)

	target := createWantWithDuration(ctx, t, service, uuid.New(), 301_000)
	heldCopy(ctx, t, pool, target.ID)
	dueNow(ctx, t, pool, target.ID)

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	if len(hunter.texts) != 0 {
		t.Errorf("searched %v, want a want already asking a question left alone", hunter.texts)
	}
	if len(fetcher.started) != 0 {
		t.Errorf("started %d transfers, want none", len(fetcher.started))
	}
	stopped, err := service.Target(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stopped.NextAttemptAt.Valid {
		t.Errorf("next attempt = %v, want no time at all rather than a later one",
			stopped.NextAttemptAt.Time)
	}
	if stopped.Summary != copyWaitingSummary {
		t.Errorf("summary = %q, want %q", stopped.Summary, copyWaitingSummary)
	}
	// It is still a question somebody can answer. Stopping the fetching must not
	// take the want out of the queue that asks about it.
	page, err := db.New(pool).AcquisitionReviewQueue(ctx, 10, 0)
	if err != nil {
		t.Fatalf("AcquisitionReviewQueue() error = %v", err)
	}
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].Target.ID != target.ID {
		t.Errorf("queue = %+v, want the stopped want still asking", page)
	}
}

// A want with an anchor keeps fetching. The anchor can refuse or admit each copy
// that arrives, so a further copy can settle it without anybody listening.
func TestASweepFetchesForAnAnchoredWantHoldingACopy(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	hunter, fetcher := oneCopyOnOffer(), &fakeFetcher{}
	service := hunting(pool, hunter, fetcher, true)

	target := createWantWithDuration(ctx, t, service, uuid.New(), 301_000)
	heldCopy(ctx, t, pool, target.ID)
	if err := db.New(pool).RecordAnchor(ctx, db.AnchorParams{
		TargetID: target.ID, Fingerprint: "AQABz0mUSEkSHV-OH0d_HG9wJXo0Hz-uI9-C_sfx4XmC",
		Source: "deezer", Reference: "2178654", Seconds: 30,
	}); err != nil {
		t.Fatalf("record the anchor: %v", err)
	}
	dueNow(ctx, t, pool, target.ID)

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	if len(hunter.texts) != 1 {
		t.Fatalf("searched %v, want an anchored want looking for another copy", hunter.texts)
	}
	if len(fetcher.started) != 1 {
		t.Errorf("started %d transfers, want the next copy fetched", len(fetcher.started))
	}
}

// The way back. A person saying none of these refuses the copies, and the want
// goes for the next one on the next pass.
func TestRefusingTheCopyLetsAStoppedWantFetchAgain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	hunter, fetcher := oneCopyOnOffer(), &fakeFetcher{}
	service := hunting(pool, hunter, fetcher, true)

	target := createWantWithDuration(ctx, t, service, uuid.New(), 301_000)
	heldCopy(ctx, t, pool, target.ID)
	dueNow(ctx, t, pool, target.ID)
	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	if err := service.RefuseCopies(ctx, target.ID); err != nil {
		t.Fatalf("RefuseCopies() error = %v", err)
	}
	looking, err := service.Target(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !looking.NextAttemptAt.Valid || looking.NextAttemptAt.Time.After(time.Now()) {
		t.Fatalf("next attempt = %v, want the refused want due again", looking.NextAttemptAt)
	}
	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("second Sweep() error = %v", err)
	}
	if len(hunter.texts) != 1 || len(fetcher.started) != 1 {
		t.Errorf("searched %v and started %d transfers, want the next copy fetched",
			hunter.texts, len(fetcher.started))
	}
}

// moreHeldCopies records four further held copies of the want, each a rip of
// its own size, so it holds five.
func moreHeldCopies(ctx context.Context, t *testing.T, pool *pgxpool.Pool, targetID uuid.UUID) {
	t.Helper()
	for i := range 4 {
		if _, err := pool.Exec(ctx, `
			INSERT INTO acquisition_target_files (
				acquisition_target_id, provider, source_username, remote_path,
				file_name, size_bytes, verdict, decided_by, summary, evidence
			)
			VALUES ($1, 'slskd', 'peer', $2, '05 Glory Box.flac', $3, 'held', 'schall',
			        'Nothing could identify the audio.', '{}'::jsonb)
		`, targetID, fmt.Sprintf(`Music\Portishead\Copy %d\05 Glory Box.flac`, i),
			26_000_000+i); err != nil {
			t.Fatalf("hold copy %d: %v", i, err)
		}
	}
}

// A want whose anchor was found by name and already holds five unanswered
// copies is not searched for again. The anchor can never prove or refuse a
// copy, so the sixth would be one more question. Before, only a want with no
// anchor at all stopped, and one want fetched 153 copies.
func TestASweepDoesNotFetchForAWantAnchoredByNameHoldingFiveCopies(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	hunter, fetcher := oneCopyOnOffer(), &fakeFetcher{}
	service := hunting(pool, hunter, fetcher, true)

	target := createWantWithDuration(ctx, t, service, uuid.New(), 301_000)
	heldCopy(ctx, t, pool, target.ID)
	moreHeldCopies(ctx, t, pool, target.ID)
	if err := db.New(pool).RecordAnchor(ctx, db.AnchorParams{
		TargetID: target.ID, Fingerprint: "AQABz0mUSEkSHV-OH0d_HG9wJXo0Hz-uI9-C_sfx4XmC",
		Source: "youtube", Reference: "dQw4w9WgXcQ", Seconds: 30,
	}); err != nil {
		t.Fatalf("record the anchor: %v", err)
	}
	dueNow(ctx, t, pool, target.ID)

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	if len(hunter.texts) != 0 {
		t.Errorf("searched %v, want a want holding five questions left alone", hunter.texts)
	}
	if len(fetcher.started) != 0 {
		t.Errorf("started %d transfers, want none", len(fetcher.started))
	}
	stopped, err := service.Target(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stopped.NextAttemptAt.Valid {
		t.Errorf("next attempt = %v, want none", stopped.NextAttemptAt.Time)
	}
	if stopped.Summary != copyWaitingSummary {
		t.Errorf("summary = %q, want %q", stopped.Summary, copyWaitingSummary)
	}
}

// The same five copies under a Deezer anchor keep the want searching. That
// anchor can admit or refuse the next copy without anybody listening.
func TestASweepFetchesForADeezerAnchoredWantHoldingFiveCopies(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	hunter, fetcher := oneCopyOnOffer(), &fakeFetcher{}
	service := hunting(pool, hunter, fetcher, true)

	target := createWantWithDuration(ctx, t, service, uuid.New(), 301_000)
	heldCopy(ctx, t, pool, target.ID)
	moreHeldCopies(ctx, t, pool, target.ID)
	if err := db.New(pool).RecordAnchor(ctx, db.AnchorParams{
		TargetID: target.ID, Fingerprint: "AQABz0mUSEkSHV-OH0d_HG9wJXo0Hz-uI9-C_sfx4XmC",
		Source: "deezer", Reference: "2178654", Seconds: 30,
	}); err != nil {
		t.Fatalf("record the anchor: %v", err)
	}
	dueNow(ctx, t, pool, target.ID)

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	if len(hunter.texts) != 1 {
		t.Fatalf("searched %v, want a Deezer-anchored want looking for another copy", hunter.texts)
	}
	if len(fetcher.started) != 1 {
		t.Errorf("started %d transfers, want the next copy fetched", len(fetcher.started))
	}
}
