package acquisition

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/dbtest"
)

// fingerprintedCopy holds a copy of a want with the given audio and bit rate.
func fingerprintedCopy(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool, targetID uuid.UUID,
	name, fingerprint string, bitRate int,
) uuid.UUID {
	t.Helper()
	if err := db.New(pool).RecordAcquiredFile(ctx, db.RecordAcquiredFileParams{
		AcquisitionTargetID: targetID, Provider: "slskd", SourceUsername: "peer",
		RemotePath: `Music\Portishead\Dummy\` + name, FileName: name,
		SizeBytes: 25000000, Verdict: db.AcquiredFileHeld,
		Summary:  "Nothing could identify the audio.",
		Evidence: &db.AcquiredFileEvidence{Name: name, BitRate: bitRate},
	}); err != nil {
		t.Fatalf("hold the copy: %v", err)
	}
	var id uuid.UUID
	if err := pool.QueryRow(ctx, `
		UPDATE acquisition_target_files SET audio_fingerprint = $3
		WHERE acquisition_target_id = $1 AND file_name = $2
		RETURNING id
	`, targetID, name, fingerprint).Scan(&id); err != nil {
		t.Fatalf("fingerprint the copy: %v", err)
	}
	return id
}

func verdictsOf(ctx context.Context, t *testing.T, pool *pgxpool.Pool, targetID uuid.UUID) map[uuid.UUID]string {
	t.Helper()
	files, err := db.New(pool).AcquisitionTargetFiles(ctx, targetID)
	if err != nil {
		t.Fatal(err)
	}
	verdicts := make(map[uuid.UUID]string, len(files))
	for _, file := range files {
		verdicts[file.ID] = file.Verdict
	}
	return verdicts
}

// Three peers sent one master and a fourth sent another. One copy of each
// master is left to ask about, the lossless one of the first.
func TestAWantKeepsOneHeldCopyOfEachPieceOfAudio(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	service := hunting(pool, &fakeHunter{}, &fakeFetcher{}, true)
	target := createWantWithDuration(ctx, t, service, uuid.New(), 301_000)

	lossy := fingerprintedCopy(ctx, t, pool, target.ID, "a.mp3", audioA, 320)
	lossless := fingerprintedCopy(ctx, t, pool, target.ID, "b.flac", audioA, 0)
	again := fingerprintedCopy(ctx, t, pool, target.ID, "c.mp3", audioA, 128)
	other := fingerprintedCopy(ctx, t, pool, target.ID, "d.mp3", audioB, 128)

	marked, err := service.SetAsideDuplicates(ctx)
	if err != nil {
		t.Fatalf("SetAsideDuplicates() error = %v", err)
	}

	if marked != 2 {
		t.Errorf("set aside %d copies, want 2", marked)
	}
	verdicts := verdictsOf(ctx, t, pool, target.ID)
	want := map[uuid.UUID]string{
		lossy: db.AcquiredFileDuplicate, lossless: db.AcquiredFileHeld,
		again: db.AcquiredFileDuplicate, other: db.AcquiredFileHeld,
	}
	for id, verdict := range want {
		if verdicts[id] != verdict {
			t.Errorf("copy %v verdict = %q, want %q", id, verdicts[id], verdict)
		}
	}
}

// A person's answer about a copy stays the person's.
func TestACopyAPersonDecidedIsNeverSetAside(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	service := hunting(pool, &fakeHunter{}, &fakeFetcher{}, true)
	target := createWantWithDuration(ctx, t, service, uuid.New(), 301_000)

	kept := fingerprintedCopy(ctx, t, pool, target.ID, "a.flac", audioA, 0)
	answered := fingerprintedCopy(ctx, t, pool, target.ID, "b.mp3", audioA, 320)
	if _, err := pool.Exec(ctx, `
		UPDATE acquisition_target_files SET decided_by = 'user' WHERE id = $1
	`, answered); err != nil {
		t.Fatal(err)
	}

	if _, err := service.SetAsideDuplicates(ctx); err != nil {
		t.Fatalf("SetAsideDuplicates() error = %v", err)
	}

	verdicts := verdictsOf(ctx, t, pool, target.ID)
	if verdicts[kept] != db.AcquiredFileHeld || verdicts[answered] != db.AcquiredFileHeld {
		t.Errorf("verdicts = %v, want both copies still held", verdicts)
	}
}
