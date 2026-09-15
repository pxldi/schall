package db

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pxldi/schall/internal/dbtest"
)

// anchorable records a want that carries an ISRC, which is the only kind a
// preview can be fetched for.
func anchorable(
	ctx context.Context, t *testing.T, queries *Queries, title, isrc string,
) uuid.UUID {
	t.Helper()

	params := wantParams(title, uuid.New())
	params.EntryISRC = pgtype.Text{String: isrc, Valid: true}
	id, created, err := queries.CreateAcquisitionTarget(ctx, params)
	if err != nil || !created {
		t.Fatalf("create a want: %v, %v", created, err)
	}
	return id
}

func anchorFields(
	ctx context.Context, t *testing.T, queries *Queries, id uuid.UUID,
) (fingerprint, unavailable string, next *time.Time, attempts int32) {
	t.Helper()

	var (
		print  *string
		reason *string
		due    *time.Time
	)
	if err := queries.db.QueryRow(ctx, `
		SELECT anchor_fingerprint, anchor_unavailable, anchor_next_attempt_at, anchor_attempts
		FROM acquisition_targets WHERE id = $1
	`, id).Scan(&print, &reason, &due, &attempts); err != nil {
		t.Fatalf("read the anchor of %s: %v", id, err)
	}
	if print != nil {
		fingerprint = *print
	}
	if reason != nil {
		unavailable = *reason
	}
	return fingerprint, unavailable, due, attempts
}

func TestAWantWithAnISRCCanBeScheduledForAPreview(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	queries := New(dbtest.Setup(t))

	id := anchorable(ctx, t, queries, "Glory Box", "GBAAA9900123")
	at := time.Now().Add(-time.Minute)
	if err := queries.ScheduleAnchor(ctx, id, at); err != nil {
		t.Fatalf("schedule: %v", err)
	}

	due, err := queries.DueAnchors(ctx, time.Now(), 10)
	if err != nil {
		t.Fatalf("read the wants due an anchor: %v", err)
	}
	if len(due) != 1 || due[0].ID != id {
		t.Fatalf("due = %v, wanted the one want just scheduled", due)
	}
	if due[0].ISRC != "GBAAA9900123" {
		t.Errorf("the fetch was handed %q to ask by", due[0].ISRC)
	}
}

// A preview is only ever fetched by ISRC. An entry that carries none has no
// exact key to ask by, and a text search returns the wrong recording often
// enough to be refused outright, so it is never put in line at all.
// A want without an ISRC is in line too: the distributor has nothing for it,
// and the upload search of docs/decisions/0029 is what answers then.
func TestAWantWithNoISRCIsScheduledForAnAnchor(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	queries := New(dbtest.Setup(t))

	id, created, err := queries.CreateAcquisitionTarget(ctx, wantParams("Roads", uuid.New()))
	if err != nil || !created {
		t.Fatalf("create a want: %v, %v", created, err)
	}
	if err := queries.ScheduleAnchor(ctx, id, time.Now()); err != nil {
		t.Fatalf("schedule: %v", err)
	}

	due, err := queries.DueAnchors(ctx, time.Now(), 10)
	if err != nil {
		t.Fatalf("read the wants due an anchor: %v", err)
	}
	if len(due) != 1 || due[0].ID != id || due[0].ISRC != "" {
		t.Fatalf("a want with no ISRC was not put in line for an anchor: %v", due)
	}
}

func TestARecordedAnchorTakesTheWantOffTheSchedule(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	queries := New(dbtest.Setup(t))

	id := anchorable(ctx, t, queries, "Glory Box", "GBAAA9900123")
	if err := queries.ScheduleAnchor(ctx, id, time.Now().Add(-time.Minute)); err != nil {
		t.Fatalf("schedule: %v", err)
	}
	if err := queries.RecordAnchor(ctx, AnchorParams{
		TargetID: id, Fingerprint: "AQAB", Source: "deezer", Reference: "4166953962", Seconds: 30,
	}); err != nil {
		t.Fatalf("record: %v", err)
	}

	fingerprint, unavailable, next, _ := anchorFields(ctx, t, queries, id)
	if fingerprint != "AQAB" {
		t.Errorf("kept %q", fingerprint)
	}
	if unavailable != "" {
		t.Errorf("a want with an anchor also says %q about not having one", unavailable)
	}
	if next != nil {
		t.Error("a want with an anchor is still waiting for one")
	}

	due, err := queries.DueAnchors(ctx, time.Now(), 10)
	if err != nil {
		t.Fatalf("read the wants due an anchor: %v", err)
	}
	if len(due) != 0 {
		t.Fatal("a want that has its anchor was offered for fetching again")
	}
}

// The distinction the two calls exist for. "There is no preview" stops the
// asking; "the fetch could not run" does not, because it says nothing about
// whether a preview exists.
func TestAnAbsentPreviewStopsTheAskingAndAFailedFetchDoesNot(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	queries := New(dbtest.Setup(t))

	absent := anchorable(ctx, t, queries, "Glory Box", "GBAAA9900123")
	failed := anchorable(ctx, t, queries, "Roads", "GBAAA9900124")
	for _, id := range []uuid.UUID{absent, failed} {
		if err := queries.ScheduleAnchor(ctx, id, time.Now().Add(-time.Minute)); err != nil {
			t.Fatalf("schedule: %v", err)
		}
	}

	if err := queries.RecordAnchorAbsent(ctx, absent, "Deezer publishes no preview."); err != nil {
		t.Fatalf("record absent: %v", err)
	}
	later := time.Now().Add(30 * time.Minute)
	if err := queries.DeferAnchor(ctx, failed, later, "Deezer could not be reached."); err != nil {
		t.Fatalf("defer: %v", err)
	}

	if _, _, next, attempts := anchorFields(ctx, t, queries, absent); next != nil || attempts != 1 {
		t.Errorf("the want with no preview is due again at %v after %d attempts", next, attempts)
	}
	if _, _, next, attempts := anchorFields(ctx, t, queries, failed); next == nil || attempts != 1 {
		t.Errorf("the want whose fetch failed is due at %v after %d attempts", next, attempts)
	}

	due, err := queries.DueAnchors(ctx, later.Add(time.Minute), 10)
	if err != nil {
		t.Fatalf("read the wants due an anchor: %v", err)
	}
	if len(due) != 1 || due[0].ID != failed {
		t.Fatalf("due = %v, wanted only the want whose fetch failed", due)
	}
}

// Scheduling is a note that something is owed, so it must never overwrite an
// answer already on record.
func TestSchedulingDoesNotDisturbAWantThatHasBeenAnswered(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	queries := New(dbtest.Setup(t))

	anchored := anchorable(ctx, t, queries, "Glory Box", "GBAAA9900123")
	none := anchorable(ctx, t, queries, "Roads", "GBAAA9900124")
	if err := queries.RecordAnchor(ctx, AnchorParams{
		TargetID: anchored, Fingerprint: "AQAB", Source: "deezer", Reference: "1", Seconds: 30,
	}); err != nil {
		t.Fatalf("record: %v", err)
	}
	if err := queries.RecordAnchorAbsent(ctx, none, "Deezer publishes no preview."); err != nil {
		t.Fatalf("record absent: %v", err)
	}

	for _, id := range []uuid.UUID{anchored, none} {
		if err := queries.ScheduleAnchor(ctx, id, time.Now()); err != nil {
			t.Fatalf("schedule: %v", err)
		}
	}

	due, err := queries.DueAnchors(ctx, time.Now(), 10)
	if err != nil {
		t.Fatalf("read the wants due an anchor: %v", err)
	}
	if len(due) != 0 {
		t.Fatalf("scheduling asked again about wants already answered: %v", due)
	}
}
