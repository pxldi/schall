package acquisition

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/dbtest"
	"github.com/pxldi/schall/internal/tracksource"
)

// When a search round finds nothing to fetch, a keyed want takes the track from
// its own address, once, and the import judges it like a peer's copy
// (ADR 0038 §6).

// fakeSourceFetcher writes a file where yt-dlp would, or fails as it is told.
type fakeSourceFetcher struct {
	err     error
	fetched []tracksource.Track
}

func (fetcher *fakeSourceFetcher) Fetch(
	_ context.Context, track tracksource.Track, directory string,
) (tracksource.Fetched, error) {
	fetcher.fetched = append(fetcher.fetched, track)
	if fetcher.err != nil {
		return tracksource.Fetched{}, fetcher.err
	}
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return tracksource.Fetched{}, err
	}
	name := track.ID + ".mp3"
	if err := os.WriteFile(filepath.Join(directory, name), []byte("mp3 audio"), 0o644); err != nil {
		return tracksource.Fetched{}, err
	}
	return tracksource.Fetched{
		Path: filepath.Join(directory, name), Name: name, Extension: "mp3", SizeBytes: 9,
	}, nil
}

// keyedWithNothingOnOffer keys a want to addressedTrack on a service whose
// Soulseek search finds nothing, and which fetches from addresses into root.
func keyedWithNothingOnOffer(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool, fetcher *fakeSourceFetcher, root string,
) (*Service, *fakeHunter, db.AcquisitionTargetRow) {
	t.Helper()
	hunter := &fakeHunter{}
	service := hunting(pool, hunter, &fakeFetcher{}, true).
		WithTrackSources(&fakeTrackSources{track: addressedTrack}, fakePrints{value: excerptFingerprint}).
		WithSourceFetches(fetcher, root)
	target := createEntry(ctx, t, service)
	keyed, err := service.KeyToSource(ctx, target.ID, addressedTrack.URL, addressedTrack.ExternalID)
	if err != nil {
		t.Fatalf("KeyToSource() error = %v", err)
	}
	return service, hunter, keyed
}

// searchDueNow makes a want's next search due, as settling one of its copies
// does.
func searchDueNow(ctx context.Context, t *testing.T, pool *pgxpool.Pool, id uuid.UUID) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		UPDATE acquisition_targets SET next_search_at = now() WHERE id = $1
	`, id); err != nil {
		t.Fatal(err)
	}
}

// waitingADay fails unless the want is still keyed and searched for, and waits
// out the unfound ladder's first rung.
func waitingADay(ctx context.Context, t *testing.T, service *Service, id uuid.UUID) {
	t.Helper()
	after, err := service.Target(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	wait := time.Until(after.NextSearchAt.Time)
	if after.Status != "unresolved" || after.SearchAttempts != 1 || wait < 23*time.Hour || wait > 25*time.Hour {
		t.Fatalf("want = %s, search %d, next in %v, want it unresolved on the ladder a day out",
			after.Status, after.SearchAttempts, wait)
	}
}

func TestAKeyedWantWhoseSearchFindsNothingIsFetchedFromItsAddressOnce(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	fetcher := &fakeSourceFetcher{}
	root := t.TempDir()
	service, hunter, target := keyedWithNothingOnOffer(ctx, t, pool, fetcher, root)

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	if len(hunter.texts) != 1 || len(fetcher.fetched) != 1 {
		t.Fatalf("searched %d times and fetched %d times, want one of each",
			len(hunter.texts), len(fetcher.fetched))
	}
	if got := fetcher.fetched[0]; got.URL != addressedTrack.URL || got.ID != addressedTrack.ID {
		t.Fatalf("fetched %+v, want the address the want is keyed to", got)
	}
	copies := offers(ctx, t, pool, target.ID)
	if len(copies) != 1 || copies[0].Provider != addressedTrack.Source ||
		copies[0].Verdict != db.AcquiredFileFetching {
		t.Fatalf("copies = %+v, want the fetched track recorded as a SoundCloud copy on its way", copies)
	}

	// The import is queued and claims the copy as it claims a peer's, from the
	// fetch folder.
	requestID := copiesRequest(t, copies[0])
	var queued int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM jobs WHERE kind = 'import_download' AND payload->>'requestId' = $1
	`, requestID.String()).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 1 {
		t.Fatalf("import jobs = %d, want the fetched track queued for import", queued)
	}
	claimed, err := db.New(pool).ClaimTargetImport(ctx, requestID)
	if err != nil {
		t.Fatalf("ClaimTargetImport() error = %v", err)
	}
	if claimed.Provider != addressedTrack.Source || claimed.SourceDirectory != target.ID.String() ||
		claimed.File.Name != "293.mp3" {
		t.Fatalf("claimed %s %q %q, want the fetched file", claimed.Provider,
			claimed.SourceDirectory, claimed.File.Name)
	}
	if _, err := os.Stat(filepath.Join(root, claimed.SourceDirectory, claimed.File.Name)); err != nil {
		t.Fatalf("the fetched file is not where the import looks: %v", err)
	}
	// SoundCloud serves 128 kbit/s, so the import holds it below the floor.
	if err := db.New(pool).SettleAcquiredFile(ctx, db.SettleAcquiredFileParams{
		RecordAcquiredFileParams: db.RecordAcquiredFileParams{
			AcquisitionTargetID: target.ID, Provider: claimed.Provider,
			SourceUsername: claimed.SourceUsername, RemotePath: claimed.File.Path,
			FileName: claimed.File.Name, SizeBytes: claimed.File.SizeBytes,
			Verdict: db.AcquiredFileHeld, Summary: "below the floor",
			DownloadRequestID: uuid.NullUUID{UUID: claimed.RequestID, Valid: true},
		},
		ImportStatus: "needs_review", ImportError: "below the floor",
		TargetSummary: "held", NextAttemptAt: time.Now(), Outcome: "inconclusive",
	}); err != nil {
		t.Fatalf("SettleAcquiredFile() error = %v", err)
	}
	searchDueNow(ctx, t, pool, target.ID)

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	if len(hunter.texts) != 2 {
		t.Fatalf("searched %d times, want the held copy to leave the want searching", len(hunter.texts))
	}
	if len(fetcher.fetched) != 1 {
		t.Fatalf("fetched %d times, want the address fetched once per want", len(fetcher.fetched))
	}
	waitingADay(ctx, t, service, target.ID)
}

// copiesRequest is the request a copy was fetched through.
func copiesRequest(t *testing.T, copied db.AcquisitionTargetFileRow) uuid.UUID {
	t.Helper()
	if !copied.DownloadRequestID.Valid {
		t.Fatalf("copy %s names no request", copied.ID)
	}
	return copied.DownloadRequestID.UUID
}

// A fetch that fails says nothing about the track. Nothing is recorded, and the
// next round asks again.
func TestAFetchFromAnAddressThatFailsLeavesNoCopyAndIsAskedAgain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	fetcher := &fakeSourceFetcher{err: tracksource.ErrRateLimited}
	service, _, target := keyedWithNothingOnOffer(ctx, t, pool, fetcher, t.TempDir())

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	if copies := offers(ctx, t, pool, target.ID); len(copies) != 0 {
		t.Fatalf("copies = %+v, want a failed fetch to leave no verdict", copies)
	}
	var requests int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM download_requests WHERE acquisition_target_id = $1
	`, target.ID).Scan(&requests); err != nil {
		t.Fatal(err)
	}
	if requests != 0 {
		t.Fatalf("requests = %d, want a failed fetch to record nothing", requests)
	}
	waitingADay(ctx, t, service, target.ID)

	searchDueNow(ctx, t, pool, target.ID)
	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	if len(fetcher.fetched) != 2 {
		t.Fatalf("fetched %d times, want the next round to ask again", len(fetcher.fetched))
	}
}
