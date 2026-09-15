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

// release is one thing to seed: an artist somebody follows, a release of theirs,
// and one track on it. Every field a test cares about is named, because what the
// feed does turns entirely on the relation between the follow and the release.
type release struct {
	followedAt     time.Time
	lastRefreshed  *time.Time
	monitorLevel   string
	releaseDate    *time.Time
	albumType      string
	secondaryTypes []string
}

// TestTheFeedWantsARecordingReleasedAfterTheFollow is the whole promise of a
// follow: music that did not exist when it was pressed arrives as a want with
// nobody pressing anything again.
func TestTheFeedWantsARecordingReleasedAfterTheFollow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	feed := newTestFeed(pool)

	followed := time.Now().AddDate(0, -1, 0)
	published := time.Now().AddDate(0, 0, -1)
	recordingID := seedFollowedRelease(ctx, t, pool, release{
		followedAt: followed, releaseDate: &published,
	})

	if _, err := feed.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	target := wantFor(ctx, t, pool, recordingID)
	if target.Origin != "follow_feed" {
		t.Fatalf("origin = %s, want follow_feed", target.Origin)
	}
	if target.Status != "pending" {
		t.Fatalf("status = %s, want a want the loop will pick up", target.Status)
	}
}

// Following an artist says "keep them complete from here". Wanting what they
// released years ago is a separate press, on purpose — a follow that quietly
// ordered a back catalogue would be a decision nobody made.
func TestTheFeedLeavesARecordingReleasedBeforeTheFollow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	feed := newTestFeed(pool)

	published := time.Now().AddDate(-3, 0, 0)
	recordingID := seedFollowedRelease(ctx, t, pool, release{
		followedAt: time.Now().AddDate(0, -1, 0), releaseDate: &published,
	})

	if _, err := feed.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	assertNoWant(ctx, t, pool, recordingID)
}

// An absent release date is silence, never "new". Reading it as new would make
// following an artist drag in every undated thing MusicBrainz holds against
// them, which is the same guess this project refuses everywhere else.
func TestTheFeedLeavesAReleaseWithNoDate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	feed := newTestFeed(pool)

	recordingID := seedFollowedRelease(ctx, t, pool, release{
		followedAt: time.Now().AddDate(0, -1, 0),
	})

	if _, err := feed.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	assertNoWant(ctx, t, pool, recordingID)
}

// The monitor level says up front what counts, and it filters the feed exactly
// as it filters the discography press. A live album by an artist monitored for
// albums and EPs is browsable, and wantable by hand, and not fetched on its own.
func TestTheFeedLeavesAReleaseTheMonitorLevelDoesNotCount(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	feed := newTestFeed(pool)

	published := time.Now().AddDate(0, 0, -1)
	recordingID := seedFollowedRelease(ctx, t, pool, release{
		followedAt:     time.Now().AddDate(0, -1, 0),
		releaseDate:    &published,
		monitorLevel:   "albums_eps",
		secondaryTypes: []string{"live"},
	})

	if _, err := feed.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	assertNoWant(ctx, t, pool, recordingID)
}

// A recording somebody stopped pursuing stays stopped. The feed finds the
// not_wanted target holding that recording and passes over it, rather than
// re-opening a decision every six hours for as long as the artist is followed.
func TestTheFeedDoesNotReopenARecordingSomebodyStoppedPursuing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	service := newTestService(pool)
	feed := NewFeed(db.New(pool), service, zerolog.Nop())

	published := time.Now().AddDate(0, 0, -1)
	recordingID := seedFollowedRelease(ctx, t, pool, release{
		followedAt: time.Now().AddDate(0, -1, 0), releaseDate: &published,
	})
	refused := createWant(ctx, t, service, recordingID)
	if _, err := service.StopPursuing(ctx, refused.ID); err != nil {
		t.Fatalf("StopPursuing() error = %v", err)
	}

	if _, err := feed.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	target := wantFor(ctx, t, pool, recordingID)
	if target.ID != refused.ID || target.Status != "not_wanted" {
		t.Fatalf("target = (%s, %s), want the refusal left standing", target.ID, target.Status)
	}
}

// The feed runs every few hours forever, so making the same want twice is not a
// theoretical failure. One live target per recording is what stops it, and this
// is the test that the feed reaches that rule rather than working around it.
func TestASecondPassOverTheSameNewReleaseCreatesNoSecondWant(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	feed := newTestFeed(pool)

	published := time.Now().AddDate(0, 0, -1)
	recordingID := seedFollowedRelease(ctx, t, pool, release{
		followedAt: time.Now().AddDate(0, -1, 0), releaseDate: &published,
	})

	for pass := 0; pass < 2; pass++ {
		if _, err := feed.Sweep(ctx); err != nil {
			t.Fatalf("Sweep() error = %v", err)
		}
	}

	var wants int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM acquisition_targets WHERE musicbrainz_recording_id = $1
	`, recordingID).Scan(&wants); err != nil {
		t.Fatalf("count wants: %v", err)
	}
	if wants != 1 {
		t.Fatalf("wants = %d, want the one the first pass made", wants)
	}
}

// Music already on the disc is not asked for from strangers. The library holds
// it through a file proven to be that recording, which is what the loop would
// settle the want against a minute later anyway.
func TestTheFeedLeavesATrackTheLibraryAlreadyHolds(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	feed := newTestFeed(pool)

	published := time.Now().AddDate(0, 0, -1)
	recordingID := seedFollowedRelease(ctx, t, pool, release{
		followedAt: time.Now().AddDate(0, -1, 0), releaseDate: &published,
	})
	seedIdentifiedFile(ctx, t, pool, "/music/already-here.flac", recordingID)

	if _, err := feed.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	assertNoWant(ctx, t, pool, recordingID)
}

// A release only becomes a want once the catalogue knows about it, and the
// catalogue only learns by being asked. This is the half of the feed that makes
// the other half possible.
func TestTheFeedAsksAboutAFollowedArtistNobodyHasRefreshedToday(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	feed := newTestFeed(pool)

	stale := time.Now().AddDate(0, 0, -3)
	seedFollowedRelease(ctx, t, pool, release{
		followedAt: time.Now().AddDate(0, -1, 0), lastRefreshed: &stale,
	})

	if _, err := feed.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	if queued := refreshesQueued(ctx, t, pool); queued != 1 {
		t.Fatalf("queued refreshes = %d, want the one this artist is owed", queued)
	}
}

// And an artist asked about an hour ago is left alone. MusicBrainz answers one
// request a second on a limiter shared with the interactive search, so a feed
// that re-asked on every pass would spend that budget learning nothing.
func TestTheFeedLeavesAnArtistItAskedAboutRecently(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	feed := newTestFeed(pool)

	fresh := time.Now().Add(-time.Hour)
	seedFollowedRelease(ctx, t, pool, release{
		followedAt: time.Now().AddDate(0, -1, 0), lastRefreshed: &fresh,
	})

	if _, err := feed.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	if queued := refreshesQueued(ctx, t, pool); queued != 0 {
		t.Fatalf("queued refreshes = %d, want the limiter left alone", queued)
	}
}

// An artist in the catalogue because the library holds their music has not been
// followed, and holding is a statement of fact rather than a request to keep
// anybody complete.
func TestTheFeedWantsNothingForAnArtistNobodyFollowed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	feed := newTestFeed(pool)

	published := time.Now().AddDate(0, 0, -1)
	recordingID := seedFollowedRelease(ctx, t, pool, release{releaseDate: &published})

	if _, err := feed.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	assertNoWant(ctx, t, pool, recordingID)
	if queued := refreshesQueued(ctx, t, pool); queued != 0 {
		t.Fatalf("queued refreshes = %d, want a held artist left alone", queued)
	}
}

// A single and the album it lands on carry the same recording, and both are new.
// The query cannot exclude a recording this same pass is about to want, so the
// pass has to remember — otherwise one piece of music spends two of the batch's
// places.
func TestTheFeedWantsOneRecordingHoweverManyNewReleasesCarryIt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	feed := newTestFeed(pool)

	published := time.Now().AddDate(0, 0, -1)
	recordingID := seedFollowedRelease(ctx, t, pool, release{
		followedAt: time.Now().AddDate(0, -1, 0), releaseDate: &published,
	})
	seedSecondReleaseCarrying(ctx, t, pool, recordingID, published)

	if _, err := feed.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	var wants int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM acquisition_targets WHERE musicbrainz_recording_id = $1
	`, recordingID).Scan(&wants); err != nil {
		t.Fatalf("count wants: %v", err)
	}
	if wants != 1 {
		t.Fatalf("wants = %d, want one however many releases carry the recording", wants)
	}
}

// seedSecondReleaseCarrying puts the same recording on a second new release by
// the artist the first one belongs to.
func seedSecondReleaseCarrying(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool,
	recordingID uuid.UUID, published time.Time,
) {
	t.Helper()

	var artistID uuid.UUID
	if err := pool.QueryRow(ctx, `
		SELECT albums.artist_id FROM albums
		JOIN tracks ON tracks.album_id = albums.id
		WHERE tracks.musicbrainz_recording_id = $1
	`, recordingID).Scan(&artistID); err != nil {
		t.Fatalf("read the artist: %v", err)
	}
	var albumID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO albums (
			artist_id, musicbrainz_release_group_id, title, release_date, album_type,
			secondary_types
		)
		VALUES ($1, $2, 'Aviolet Dusk', $3, 'single', '{}')
		RETURNING id
	`, artistID, uuid.New(), published).Scan(&albumID); err != nil {
		t.Fatalf("seed the single: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO tracks (album_id, musicbrainz_recording_id, title, disc_number, track_number, duration_ms)
		VALUES ($1, $2, 'Aviolet Dusk', 1, 1, 245000)
	`, albumID, recordingID); err != nil {
		t.Fatalf("seed the single's track: %v", err)
	}
}

func newTestFeed(pool *pgxpool.Pool) *Feed {
	return NewFeed(db.New(pool), newTestService(pool), zerolog.Nop())
}

// seedFollowedRelease writes one artist, one release and one track, and answers
// with the track's recording. A zero followedAt is the catalogue-only artist:
// held because the library has their music, never followed.
func seedFollowedRelease(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool, spec release,
) uuid.UUID {
	t.Helper()

	if spec.monitorLevel == "" {
		spec.monitorLevel = "everything"
	}
	if spec.albumType == "" {
		spec.albumType = "album"
	}
	if spec.secondaryTypes == nil {
		spec.secondaryTypes = []string{}
	}
	var followedAt any
	summary := "held because the library has their music"
	if !spec.followedAt.IsZero() {
		followedAt = spec.followedAt
		summary = ""
	}

	var artistID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO artists (
			musicbrainz_id, name, sort_name, followed_at, last_refreshed_at,
			monitor_level, catalogue_summary
		)
		VALUES ($1, 'Sewerslvt', 'Sewerslvt', $2, $3, $4, nullif($5, ''))
		RETURNING id
	`, uuid.New(), followedAt, spec.lastRefreshed, spec.monitorLevel, summary).Scan(&artistID); err != nil {
		t.Fatalf("seed artist: %v", err)
	}

	var albumID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO albums (
			artist_id, musicbrainz_release_group_id, title, release_date, album_type,
			secondary_types
		)
		VALUES ($1, $2, 'We Had Good Times Together', $3, $4, $5)
		RETURNING id
	`, artistID, uuid.New(), spec.releaseDate, spec.albumType, spec.secondaryTypes).Scan(&albumID); err != nil {
		t.Fatalf("seed release: %v", err)
	}

	recordingID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO tracks (album_id, musicbrainz_recording_id, title, disc_number, track_number, duration_ms)
		VALUES ($1, $2, 'Aviolet Dusk', 1, 1, 245000)
	`, albumID, recordingID); err != nil {
		t.Fatalf("seed track: %v", err)
	}
	return recordingID
}

func wantFor(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool, recordingID uuid.UUID,
) db.AcquisitionTargetRow {
	t.Helper()

	var target db.AcquisitionTargetRow
	if err := pool.QueryRow(ctx, `
		SELECT id, origin, status FROM acquisition_targets
		WHERE musicbrainz_recording_id = $1
	`, recordingID).Scan(&target.ID, &target.Origin, &target.Status); err != nil {
		t.Fatalf("read want for %s: %v", recordingID, err)
	}
	return target
}

func assertNoWant(ctx context.Context, t *testing.T, pool *pgxpool.Pool, recordingID uuid.UUID) {
	t.Helper()

	var wants int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM acquisition_targets WHERE musicbrainz_recording_id = $1
	`, recordingID).Scan(&wants); err != nil {
		t.Fatalf("count wants: %v", err)
	}
	if wants != 0 {
		t.Fatalf("wants = %d, want none asked for", wants)
	}
}

func refreshesQueued(ctx context.Context, t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()

	var queued int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM jobs WHERE kind = 'refresh_artist_metadata'
	`).Scan(&queued); err != nil {
		t.Fatalf("count refreshes: %v", err)
	}
	return queued
}
