package acquisition

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/dbtest"
	"github.com/pxldi/schall/internal/musicbrainz"
)

// An entry for a censored track finds two MusicBrainz rows that fit it equally
// well: the explicit master and the clean edition. That used to stop 11 wants on
// the deployed instance with a question nobody wanted asked. The owner takes the
// explicit edition, so the clean row stops being an answer (ADR 0032).

// The two rows of "Timeless" by The Weeknd, as the provider returns them, with
// the editors' comment on the clean one.
func timelessRows() (clean, explicit musicbrainz.Recording) {
	clean = credited(
		recording(uuid.New(), "Timeless", "The Weeknd", 256000, "USUG12406537"), "The Weeknd")
	clean.Disambiguation = "clean"
	explicit = credited(
		recording(uuid.New(), "Timeless", "The Weeknd", 256000, "USUG12406536"), "The Weeknd")
	return clean, explicit
}

func createTimelessEntry(
	ctx context.Context, t *testing.T, service *Service,
) db.AcquisitionTargetRow {
	t.Helper()

	duration := int32(256000)
	target, _, err := service.Create(ctx, Entry{
		Origin: "playlist", Artist: "The Weeknd", Title: "Timeless", DurationMS: &duration,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	return target
}

// The entry resolves to the explicit row, and the method says which rule left
// one answer standing.
func TestAnEntryResolvesToTheExplicitEdition(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	clean, explicit := timelessRows()
	service := withProvider(newTestService(pool), &stubProvider{
		results: []musicbrainz.Recording{clean, explicit},
	})
	target := createTimelessEntry(ctx, t, service)

	if err := service.resolveDue(ctx); err != nil {
		t.Fatalf("resolveDue() error = %v", err)
	}

	after, err := service.Target(ctx, target.ID)
	if err != nil {
		t.Fatalf("Target() error = %v", err)
	}
	if after.Status != "pending" {
		t.Fatalf("status = %s, want pending: %s", after.Status, after.Summary)
	}
	if after.MusicBrainzRecordingID.UUID != explicit.ID {
		t.Fatalf("recording = %v, want the explicit row %s",
			after.MusicBrainzRecordingID, explicit.ID)
	}
	if !strings.HasSuffix(after.ResolutionMethod.String, "-and-explicit-edition") {
		t.Fatalf("method = %q, want it to name the rule that chose", after.ResolutionMethod.String)
	}
}

// The same two rows with nothing marking either. Two recordings fit and the
// evidence does not choose between them, so a person is asked.
func TestTwoUnmarkedRowsStillAskThePerson(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	clean, explicit := timelessRows()
	clean.Disambiguation = ""
	service := withProvider(newTestService(pool), &stubProvider{
		results: []musicbrainz.Recording{clean, explicit},
	})
	target := createTimelessEntry(ctx, t, service)

	if err := service.resolveDue(ctx); err != nil {
		t.Fatalf("resolveDue() error = %v", err)
	}

	after, err := service.Target(ctx, target.ID)
	if err != nil {
		t.Fatalf("Target() error = %v", err)
	}
	if after.Status != "awaiting_review" {
		t.Fatalf("status = %s, want the question to stand: %s", after.Status, after.Summary)
	}
	if after.MusicBrainzRecordingID.Valid {
		t.Fatalf("the want took %v with nothing to choose by", after.MusicBrainzRecordingID)
	}
}

// The flag Spotify sends is kept on the want, and null where no source said.
func TestTheExplicitFlagIsStoredOnTheWant(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	store := db.New(pool)

	flagged, _, err := store.CreateAcquisitionTarget(ctx, db.CreateAcquisitionTargetParams{
		Origin: "playlist", EntryArtist: "The Weeknd", EntryTitle: "Timeless",
		EntryExplicit: pgtype.Bool{Bool: true, Valid: true},
		Summary:       UnresolvedSummary,
	})
	if err != nil {
		t.Fatalf("CreateAcquisitionTarget() error = %v", err)
	}
	silent, _, err := store.CreateAcquisitionTarget(ctx, db.CreateAcquisitionTargetParams{
		Origin: "manual", EntryArtist: "The Weeknd", EntryTitle: "Blinding Lights",
		Summary: UnresolvedSummary,
	})
	if err != nil {
		t.Fatalf("CreateAcquisitionTarget() error = %v", err)
	}

	target, err := store.AcquisitionTarget(ctx, flagged)
	if err != nil {
		t.Fatalf("AcquisitionTarget() error = %v", err)
	}
	if !target.EntryExplicit.Valid || !target.EntryExplicit.Bool {
		t.Errorf("entry_explicit = %v, want what the source said", target.EntryExplicit)
	}
	unsaid, err := store.AcquisitionTarget(ctx, silent)
	if err != nil {
		t.Fatalf("AcquisitionTarget() error = %v", err)
	}
	if unsaid.EntryExplicit.Valid {
		t.Errorf("entry_explicit = %v, want silence where no source said", unsaid.EntryExplicit)
	}
}
