package acquisition

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/dbtest"
	"github.com/rs/zerolog"
)

// standing is one release to seed under an artist who may or may not have the
// standing want set.
type standing struct {
	wantMissing    bool
	monitorLevel   string
	albumType      string
	secondaryTypes []string
	// owned maps the track onto a library file the collection still has, which
	// is the plain reason not to want it.
	owned bool
}

// The whole point of the standing want. Following an artist saves their release
// groups and queues one release refresh each, and those land one at a time
// behind MusicBrainz's rate limit — so the press on the artist's page reaches
// only the releases whose tracks had already arrived, and the rest have to be
// wanted when they land.
func TestAStandingWantWantsATracklistThatArrivesLater(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	albumID, recordingID := seedStandingRelease(ctx, t, pool, standing{wantMissing: true})

	wanted, err := newTestStanding(pool).WantAlbumMissing(ctx, albumID)
	if err != nil {
		t.Fatalf("WantAlbumMissing() error = %v", err)
	}
	if wanted != 1 {
		t.Fatalf("wanted = %d, want the one missing track asked for", wanted)
	}
	target := wantFor(ctx, t, pool, recordingID)
	if target.Origin != "manual" {
		t.Fatalf("origin = %s, want the press that set the standing want", target.Origin)
	}
}

// A release the artist's monitor level does not count is browsable and wantable
// by hand, and the standing want never reaches it. The rule is the one the
// discography counts by, so the header, the chips and the standing want cannot
// disagree about which releases count.
func TestAStandingWantLeavesAReleaseOutsideTheMonitorLevel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	albumID, recordingID := seedStandingRelease(ctx, t, pool, standing{
		wantMissing:    true,
		monitorLevel:   "main",
		secondaryTypes: []string{"live"},
	})

	wanted, err := newTestStanding(pool).WantAlbumMissing(ctx, albumID)
	if err != nil {
		t.Fatalf("WantAlbumMissing() error = %v", err)
	}
	if wanted != 0 {
		t.Fatalf("wanted = %d, want a live album left alone at 'main'", wanted)
	}
	assertNoWant(ctx, t, pool, recordingID)
}

// An artist nobody asked to keep complete is left alone. A tracklist arriving
// is metadata, and metadata arriving must not order music by itself.
func TestATracklistWithoutAStandingWantAsksForNothing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	albumID, recordingID := seedStandingRelease(ctx, t, pool, standing{})

	wanted, err := newTestStanding(pool).WantAlbumMissing(ctx, albumID)
	if err != nil {
		t.Fatalf("WantAlbumMissing() error = %v", err)
	}
	if wanted != 0 {
		t.Fatalf("wanted = %d, want nothing asked for", wanted)
	}
	assertNoWant(ctx, t, pool, recordingID)
}

// A release refresh runs again whenever MusicBrainz is asked about the release
// group again, and the second run must not ask for the same music a second
// time. Creation is idempotent on the recording ID, so the second pass finds
// the want that exists.
func TestASecondRefreshOfTheSameReleaseMakesNoSecondWant(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	albumID, recordingID := seedStandingRelease(ctx, t, pool, standing{wantMissing: true})
	service := newTestStanding(pool)

	if _, err := service.WantAlbumMissing(ctx, albumID); err != nil {
		t.Fatalf("first WantAlbumMissing() error = %v", err)
	}
	wanted, err := service.WantAlbumMissing(ctx, albumID)
	if err != nil {
		t.Fatalf("second WantAlbumMissing() error = %v", err)
	}
	if wanted != 0 {
		t.Fatalf("wanted = %d, want the second pass to find the want that exists", wanted)
	}

	var targets int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM acquisition_targets WHERE musicbrainz_recording_id = $1
	`, recordingID).Scan(&targets); err != nil {
		t.Fatalf("count wants: %v", err)
	}
	if targets != 1 {
		t.Fatalf("wants = %d, want one want for one recording", targets)
	}
}

// A track the library already holds is not asked for, whatever the standing
// want says: the loop would settle that want against the file it already has a
// minute later.
func TestAStandingWantLeavesATrackTheLibraryHolds(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	albumID, recordingID := seedStandingRelease(ctx, t, pool, standing{
		wantMissing: true, owned: true,
	})

	wanted, err := newTestStanding(pool).WantAlbumMissing(ctx, albumID)
	if err != nil {
		t.Fatalf("WantAlbumMissing() error = %v", err)
	}
	if wanted != 0 {
		t.Fatalf("wanted = %d, want the owned track left alone", wanted)
	}
	assertNoWant(ctx, t, pool, recordingID)
}

// A newly followed artist watches main releases. 'everything' counted every
// live album, compilation and remix MusicBrainz credits to them, which is not
// what following somebody asks for.
func TestANewlyFollowedArtistWatchesMainReleases(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	created, err := db.New(pool).CreateArtist(ctx, db.CreateArtistParams{
		MusicbrainzID: uuid.NullUUID{UUID: uuid.New(), Valid: true},
		Name:          "Yaeji",
		SortName:      "Yaeji",
	})
	if err != nil {
		t.Fatalf("CreateArtist() error = %v", err)
	}

	var level string
	if err := pool.QueryRow(ctx, `
		SELECT monitor_level FROM artists WHERE id = $1
	`, created.ID).Scan(&level); err != nil {
		t.Fatalf("read monitor level: %v", err)
	}
	if level != "main" {
		t.Fatalf("monitor_level = %s, want main", level)
	}
}

func newTestStanding(pool *pgxpool.Pool) *Standing {
	return NewStanding(db.New(pool), newTestService(pool), zerolog.Nop())
}

// seedStandingRelease writes one followed artist, one release and one track, and
// answers with the release and the track's recording.
func seedStandingRelease(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool, spec standing,
) (uuid.UUID, uuid.UUID) {
	t.Helper()

	if spec.monitorLevel == "" {
		spec.monitorLevel = "main"
	}
	if spec.albumType == "" {
		spec.albumType = "album"
	}
	if spec.secondaryTypes == nil {
		spec.secondaryTypes = []string{}
	}
	var wantMissingSince any
	if spec.wantMissing {
		wantMissingSince = time.Now().Add(-time.Hour)
	}

	var artistID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO artists (
			musicbrainz_id, name, sort_name, followed_at, monitor_level,
			want_missing_since
		)
		VALUES ($1, 'Yaeji', 'Yaeji', now() - interval '1 day', $2, $3)
		RETURNING id
	`, uuid.New(), spec.monitorLevel, wantMissingSince).Scan(&artistID); err != nil {
		t.Fatalf("seed artist: %v", err)
	}

	var albumID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO albums (
			artist_id, musicbrainz_release_group_id, title, album_type, secondary_types
		)
		VALUES ($1, $2, 'With A Hammer', $3, $4)
		RETURNING id
	`, artistID, uuid.New(), spec.albumType, spec.secondaryTypes).Scan(&albumID); err != nil {
		t.Fatalf("seed release: %v", err)
	}

	recordingID := uuid.New()
	var trackID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO tracks (album_id, musicbrainz_recording_id, title, disc_number, track_number, duration_ms)
		VALUES ($1, $2, 'For Granted', 1, 1, 198000)
		RETURNING id
	`, albumID, recordingID).Scan(&trackID); err != nil {
		t.Fatalf("seed track: %v", err)
	}
	if spec.owned {
		seedMappedFile(ctx, t, pool, trackID)
	}
	return albumID, recordingID
}

// seedMappedFile puts a present library file under one track, which is the
// plainest way the library can already hold it.
func seedMappedFile(ctx context.Context, t *testing.T, pool *pgxpool.Pool, trackID uuid.UUID) {
	t.Helper()

	var fileID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO library_files (path, size_bytes, modified_at)
		VALUES ('/music/yaeji/for-granted.flac', 40000000, now())
		RETURNING id
	`).Scan(&fileID); err != nil {
		t.Fatalf("seed library file: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO track_mappings (track_id, library_file_id, method, is_manual)
		VALUES ($1, $2, 'manual', true)
	`, trackID, fileID); err != nil {
		t.Fatalf("seed track mapping: %v", err)
	}
}
