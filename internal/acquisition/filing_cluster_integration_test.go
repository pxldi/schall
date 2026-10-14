package acquisition

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/dbtest"
	"github.com/pxldi/schall/internal/musicbrainz"
)

// pissyPamper is the live case: one leak MusicBrainz holds twice, once as
// "Pissy Pamper" by Young Nudy feat. Playboi Carti and once as "Pissy Pamper /
// Kid Cudi" by Playboi Carti feat. Young Nudy. The titles and credits differ,
// so the name-and-length test cannot join them, and AcoustID names both rows
// in one cluster.
func pissyPamper(wanted, filed uuid.UUID) *catalogueProvider {
	return &catalogueProvider{recordings: map[uuid.UUID]musicbrainz.Recording{
		wanted: credited(recording(wanted, "Pissy Pamper", "Young Nudy feat. Playboi Carti", 201000),
			"Young Nudy", "Playboi Carti"),
		filed: credited(recording(filed, "Pissy Pamper / Kid Cudi", "Playboi Carti feat. Young Nudy", 201000),
			"Playboi Carti", "Young Nudy"),
	}}
}

// clusterOf writes the AcoustID answer that admitted the copy: one leading
// cluster naming these recordings.
func clusterOf(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool, targetID uuid.UUID, ids ...uuid.UUID,
) {
	t.Helper()
	list := "["
	for index, id := range ids {
		if index > 0 {
			list += ","
		}
		list += fmt.Sprintf("%q", id.String())
	}
	list += "]"
	acoustic := fmt.Sprintf(
		`{"score":0.99,"agrees":true,"recordingIds":%s,"clusters":[{"score":0.99,"recordingIds":%s}]}`,
		list, list)
	if _, err := pool.Exec(ctx, `
		UPDATE acquisition_target_files
		SET evidence = jsonb_set(evidence, '{acoustic}', $2::jsonb)
		WHERE acquisition_target_id = $1
	`, targetID, acoustic); err != nil {
		t.Fatalf("seed the AcoustID answer for want %s: %v", targetID, err)
	}
}

// AcoustID admitted the copy at a cluster that names the wanted row and the row
// the library filed the file under. The want has its music, so it settles
// against the file, and the file keeps the library's answer. Before this the
// want stopped and asked a person "Same audio. Match the file, or remove the
// song."
func TestAWantSettlesWhenItsCopysClusterNamesTheFiledRecordingToo(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	wanted, filed := uuid.New(), uuid.New()
	service := filingService(pool, pissyPamper(wanted, filed))
	target := createWant(ctx, t, service, wanted)
	fileID := acceptedCopy(ctx, t, pool, target.ID, wanted)
	clusterOf(ctx, t, pool, target.ID, filed, wanted)
	filedAs(ctx, t, pool, fileID, filed, false)

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	settled := targetRow(ctx, t, service, target.ID)
	if settled.Status != "acquired" {
		t.Fatalf("status = %s summary = %q, want acquired", settled.Status, settled.Summary)
	}
	if !settled.AcquiredLibraryFileID.Valid || settled.AcquiredLibraryFileID.UUID != fileID {
		t.Errorf("acquired file = %v, want %s", settled.AcquiredLibraryFileID, fileID)
	}
	stored, _, _ := identityOfFile(ctx, t, pool, fileID)
	if stored != filed {
		t.Errorf("identity = %s, want the library's own answer %s untouched", stored, filed)
	}
	if owned := ownerOf(ctx, t, pool, wanted); owned != fileID {
		t.Errorf("file owning the wanted row = %s, want %s", owned, fileID)
	}
}

// A cluster naming the filed row speaks only for a copy AcoustID admitted. A
// copy admitted any other way says nothing about the second row, so the want
// still stops for a person.
func TestAClusterDoesNotSettleAWantWhoseCopyItDidNotAdmit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	wanted, filed := uuid.New(), uuid.New()
	service := filingService(pool, pissyPamper(wanted, filed))
	target := createWant(ctx, t, service, wanted)
	fileID := acceptedCopy(ctx, t, pool, target.ID, wanted)
	clusterOf(ctx, t, pool, target.ID, filed, wanted)
	provenBy(ctx, t, pool, target.ID, "published-sample")
	filedAs(ctx, t, pool, fileID, filed, false)

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	stopped := targetRow(ctx, t, service, target.ID)
	if stopped.Status != "pending" || stopped.NextAttemptAt.Valid {
		t.Fatalf("status = %s next = %v, want a pending want with no next look",
			stopped.Status, stopped.NextAttemptAt)
	}
}

// A cluster naming only the wanted row says nothing about the row the library
// chose, so the want still stops.
func TestAClusterNamingOnlyTheWantedRowDoesNotSettleTheWant(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	wanted, filed := uuid.New(), uuid.New()
	service := filingService(pool, pissyPamper(wanted, filed))
	target := createWant(ctx, t, service, wanted)
	fileID := acceptedCopy(ctx, t, pool, target.ID, wanted)
	clusterOf(ctx, t, pool, target.ID, wanted)
	filedAs(ctx, t, pool, fileID, filed, false)

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	stopped := targetRow(ctx, t, service, target.ID)
	if stopped.Status != "pending" || stopped.NextAttemptAt.Valid {
		t.Fatalf("status = %s next = %v, want a pending want with no next look",
			stopped.Status, stopped.NextAttemptAt)
	}
}
