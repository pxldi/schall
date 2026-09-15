package db

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pxldi/schall/internal/dbtest"
)

// An installation that has never opened the screen has no row at all. That is
// not an error and not a question: it is the answer every installation starts
// with, and it means "rank as Schall has always ranked".
func TestFormatPreferencesNobodyHasSetReadAsEmpty(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	row, err := New(pool).SourcePreferences(ctx)
	if err != nil {
		t.Fatalf("SourcePreferences() error = %v", err)
	}
	if len(row.Preferred) != 0 || len(row.Unacceptable) != 0 || row.MinimumBitRate != 0 {
		t.Fatalf("preferences = %#v, want nothing said", row)
	}
}

func TestFormatPreferencesComeBackAsTheyWereSaved(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	if _, err := queries.SaveSourcePreferences(ctx, SourcePreferencesRow{
		Preferred: []string{"mp3", "flac"}, Unacceptable: []string{"dsf"},
		MinimumBitRate: 320, FormatMinimumBitRate: map[string]int32{"mp3": 320, "opus": 160},
	}); err != nil {
		t.Fatalf("SaveSourcePreferences() error = %v", err)
	}

	row, err := queries.SourcePreferences(ctx)
	if err != nil {
		t.Fatalf("SourcePreferences() error = %v", err)
	}
	if len(row.Preferred) != 2 || row.Preferred[0] != "mp3" || row.Preferred[1] != "flac" {
		t.Fatalf("preferred = %v, want the order it was given in", row.Preferred)
	}
	if len(row.Unacceptable) != 1 || row.Unacceptable[0] != "dsf" {
		t.Fatalf("unacceptable = %v", row.Unacceptable)
	}
	if row.MinimumBitRate != 320 {
		t.Fatalf("minimum bitrate = %d, want 320", row.MinimumBitRate)
	}
	if row.FormatMinimumBitRate["mp3"] != 320 || row.FormatMinimumBitRate["opus"] != 160 {
		t.Fatalf("per-format floors = %v, want the two that were saved",
			row.FormatMinimumBitRate)
	}
}

// There is one row, so saving again replaces rather than adds.
func TestSavingFormatPreferencesTwiceKeepsOneAnswer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	if _, err := queries.SaveSourcePreferences(ctx, SourcePreferencesRow{
		Preferred: []string{"mp3"}, MinimumBitRate: 320,
		FormatMinimumBitRate: map[string]int32{"mp3": 320},
	}); err != nil {
		t.Fatalf("SaveSourcePreferences() error = %v", err)
	}
	if _, err := queries.SaveSourcePreferences(ctx, SourcePreferencesRow{
		Preferred: []string{"flac"},
	}); err != nil {
		t.Fatalf("SaveSourcePreferences() error = %v", err)
	}

	var rows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM source_preferences`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("%d rows, want one", rows)
	}
	row, err := queries.SourcePreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(row.Preferred) != 1 || row.Preferred[0] != "flac" || row.MinimumBitRate != 0 {
		t.Fatalf("preferences = %#v, want the second answer", row)
	}
	if len(row.FormatMinimumBitRate) != 0 {
		t.Fatalf("per-format floors = %v, want the second answer's none",
			row.FormatMinimumBitRate)
	}
}

// A format that is both preferred and refused is a setting that cannot do what
// it says. The table refuses it, so no code path can store one.
func TestAFormatCannotBeBothPreferredAndRefused(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	_, err := New(pool).SaveSourcePreferences(ctx, SourcePreferencesRow{
		Preferred: []string{"mp3"}, Unacceptable: []string{"mp3"},
	})
	if err == nil {
		t.Fatal("a format was stored as both preferred and refused")
	}
}

func TestLoweringBitrateFloorWakesParkedWantsButRaisingItDoesNot(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	if _, err := queries.SaveSourcePreferences(ctx, SourcePreferencesRow{
		MinimumBitRate: 320,
	}); err != nil {
		t.Fatalf("save initial floor: %v", err)
	}
	targetID, _, err := queries.CreateAcquisitionTarget(ctx, wantParams("Parked", uuid.New()))
	if err != nil {
		t.Fatalf("CreateAcquisitionTarget() error = %v", err)
	}
	future := time.Now().Add(24 * time.Hour)
	if _, err := pool.Exec(ctx, "UPDATE acquisition_targets SET below_floor_streak = 3, below_floor_since = now(), next_attempt_at = $2 WHERE id = $1", targetID, future); err != nil {
		t.Fatalf("park target: %v", err)
	}

	if _, err := queries.SaveSourcePreferences(ctx, SourcePreferencesRow{
		MinimumBitRate: 256,
	}); err != nil {
		t.Fatalf("lower floor: %v", err)
	}
	var next time.Time
	var streak int16
	var since pgtype.Timestamptz
	if err := pool.QueryRow(ctx, "SELECT next_attempt_at, below_floor_streak, below_floor_since FROM acquisition_targets WHERE id = $1", targetID).Scan(&next, &streak, &since); err != nil {
		t.Fatal(err)
	}
	if next.After(time.Now().Add(time.Minute)) {
		t.Errorf("lowered floor left next attempt at %v, want it awake", next)
	}
	if streak != 0 {
		t.Errorf("lowered floor streak = %d, want zero", streak)
	}
	if since.Valid {
		t.Errorf("lowered floor since = %v, want cleared", since.Time)
	}

	future = time.Now().Add(24 * time.Hour)
	if _, err := pool.Exec(ctx, "UPDATE acquisition_targets SET below_floor_streak = 3, below_floor_since = now(), next_attempt_at = $2 WHERE id = $1", targetID, future); err != nil {
		t.Fatalf("re-park target: %v", err)
	}
	if _, err := queries.SaveSourcePreferences(ctx, SourcePreferencesRow{
		MinimumBitRate: 320,
	}); err != nil {
		t.Fatalf("raise floor: %v", err)
	}
	var still time.Time
	if err := pool.QueryRow(ctx, "SELECT next_attempt_at FROM acquisition_targets WHERE id = $1", targetID).Scan(&still); err != nil {
		t.Fatal(err)
	}
	if still.Before(time.Now().Add(23 * time.Hour)) {
		t.Errorf("raised floor moved next attempt to %v, want it parked", still)
	}
}
