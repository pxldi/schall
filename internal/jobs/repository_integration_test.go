package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/dbtest"
	"github.com/pxldi/schall/internal/downloads"
	"github.com/pxldi/schall/internal/musicbrainz"
	"github.com/rs/zerolog"
)

func TestRepositoryRefreshLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	artistID, musicBrainzID, releaseGroupID := uuid.New(), uuid.New(), uuid.New()
	payload, _ := json.Marshal(map[string]uuid.UUID{"artistId": artistID})
	if _, err := pool.Exec(ctx, `
		INSERT INTO artists (id, musicbrainz_id, name, sort_name)
		VALUES ($1, $2, 'Old name', 'Old name')
	`, artistID, musicBrainzID); err != nil {
		t.Fatalf("seed artist: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO jobs (kind, payload) VALUES ($1, $2)
	`, RefreshArtistMetadata, payload); err != nil {
		t.Fatalf("seed refresh job: %v", err)
	}

	repository := NewRepository(pool)
	job, err := repository.Claim(ctx, Lane{})
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if job.Attempts != 1 || job.Kind != RefreshArtistMetadata {
		t.Fatalf("claimed job = %#v", job)
	}
	gotMusicBrainzID, err := repository.ArtistMusicBrainzID(ctx, artistID)
	if err != nil || gotMusicBrainzID != musicBrainzID {
		t.Fatalf("ArtistMusicBrainzID() = (%s, %v)", gotMusicBrainzID, err)
	}

	catalogue := musicbrainz.ArtistCatalogue{
		Artist:   musicbrainz.Artist{ID: musicBrainzID, Name: "Canonical name", SortName: "Name, Canonical"},
		Metadata: json.RawMessage(`{"id":"` + musicBrainzID.String() + `"}`),
		ReleaseGroups: []musicbrainz.ReleaseGroup{{
			ID:               releaseGroupID,
			Title:            "First album",
			FirstReleaseDate: "1994-08-22",
			PrimaryType:      "Album",
			Metadata:         json.RawMessage(`{"id":"` + releaseGroupID.String() + `"}`),
		}},
	}
	if err := repository.SaveArtistCatalogue(ctx, artistID, catalogue); err != nil {
		t.Fatalf("SaveArtistCatalogue() error = %v", err)
	}
	if err := repository.Complete(ctx, job); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}

	var artistName, jobStatus string
	var albumCount, sourceCount int
	var refreshedAt *time.Time
	err = pool.QueryRow(ctx, `
		SELECT
			(SELECT name FROM artists WHERE id = $1),
			(SELECT last_refreshed_at FROM artists WHERE id = $1),
			(SELECT count(*) FROM albums WHERE artist_id = $1),
			(SELECT count(*) FROM metadata_sources),
			(SELECT status FROM jobs WHERE id = $2)
	`, artistID, job.ID).Scan(&artistName, &refreshedAt, &albumCount, &sourceCount, &jobStatus)
	if err != nil {
		t.Fatalf("inspect refresh result: %v", err)
	}
	if artistName != "Canonical name" || refreshedAt == nil || albumCount != 1 || sourceCount != 2 || jobStatus != "completed" {
		t.Errorf(
			"refresh result = name %q, refreshed %v, albums %d, sources %d, status %q",
			artistName, refreshedAt, albumCount, sourceCount, jobStatus,
		)
	}

	var albumID uuid.UUID
	if err := pool.QueryRow(ctx, `
		SELECT id FROM albums WHERE musicbrainz_release_group_id = $1
	`, releaseGroupID).Scan(&albumID); err != nil {
		t.Fatalf("load queued album: %v", err)
	}
	albumJob, err := repository.Claim(ctx, Lane{})
	if err != nil {
		t.Fatalf("claim album refresh: %v", err)
	}
	if albumJob.Kind != RefreshAlbumMetadata {
		t.Fatalf("album job = %#v", albumJob)
	}
	recordingID := uuid.New()
	release := musicbrainz.ReleaseCatalogue{
		Edition: musicbrainz.ReleaseEdition{
			ID: uuid.New(), Title: "First album", Status: "Official", Country: "GB",
			Date: "1994-08-22", MediaCount: 1, TrackCount: 1,
			SelectionReason: "Earliest official edition",
		},
		Tracks: []musicbrainz.Track{{
			RecordingID: recordingID, Title: "First track", DiscNumber: 1,
			TrackNumber: 1, DurationMS: intPointer(240000), ISRC: "GBABC9400001",
		}},
		Metadata: json.RawMessage(`{"id":"edition"}`),
	}
	if err := repository.SaveReleaseCatalogue(ctx, albumID, release); err != nil {
		t.Fatalf("SaveReleaseCatalogue() error = %v", err)
	}
	if err := repository.SaveReleaseCatalogue(ctx, albumID, release); err != nil {
		t.Fatalf("idempotent SaveReleaseCatalogue() error = %v", err)
	}

	var editionCount, trackCount int
	var savedRecordingID uuid.UUID
	err = pool.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM release_editions WHERE album_id = $1),
			(SELECT count(*) FROM tracks WHERE album_id = $1),
			(SELECT musicbrainz_recording_id FROM tracks WHERE album_id = $1)
	`, albumID).Scan(&editionCount, &trackCount, &savedRecordingID)
	if err != nil {
		t.Fatalf("inspect release ingestion: %v", err)
	}
	if editionCount != 1 || trackCount != 1 || savedRecordingID != recordingID {
		t.Errorf("release ingestion = editions %d, tracks %d, recording %s", editionCount, trackCount, savedRecordingID)
	}
}

func intPointer(value int) *int {
	return &value
}

// The release behind a proven file identity reaches the catalogue under an
// artist Schall holds rather than follows. Nobody asked for that artist's
// releases to be kept complete, and ingesting one album is not that request.
func TestIngestingAReleaseGroupHoldsItsArtistWithoutFollowing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	repository := NewRepository(pool)

	artistMBID, releaseGroupID := uuid.New(), uuid.New()
	albumID, err := repository.SaveCatalogueReleaseGroup(ctx, musicbrainz.ReleaseGroupDetail{
		ReleaseGroup: musicbrainz.ReleaseGroup{
			ID: releaseGroupID, Title: "Dummy", FirstReleaseDate: "1994-08-22",
			PrimaryType: "Album", Metadata: json.RawMessage(`{"first-release-date":"1994-08-22"}`),
		},
		Artist: musicbrainz.Artist{ID: artistMBID, Name: "Portishead", SortName: "Portishead"},
		Credit: "Portishead",
	})
	if err != nil {
		t.Fatalf("SaveCatalogueReleaseGroup() error = %v", err)
	}

	var followed *time.Time
	var summary string
	var title string
	if err := pool.QueryRow(ctx, `
		SELECT artists.followed_at, coalesce(artists.catalogue_summary, ''), albums.title
		FROM albums
		JOIN artists ON artists.id = albums.artist_id
		WHERE albums.id = $1
	`, albumID).Scan(&followed, &summary, &title); err != nil {
		t.Fatal(err)
	}
	if followed != nil {
		t.Errorf("followed_at = %v, want an artist held rather than followed", followed)
	}
	if summary == "" {
		t.Error("catalogue_summary is empty; an artist nobody chose must explain itself")
	}
	if title != "Dummy" {
		t.Errorf("album title = %q", title)
	}

	// The tracks are what the file needs, and they come from the representative
	// edition the existing refresh already knows how to choose.
	var queued int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM jobs
		WHERE kind = 'refresh_album_metadata' AND payload->>'albumId' = $1::text
	`, albumID).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 1 {
		t.Errorf("album refreshes queued = %d, want 1", queued)
	}
}

// A proven file is a reason to hold an artist, never a reason to stop tracking
// one. Ingesting a release for an artist the user already follows must leave
// that decision alone.
func TestIngestingDoesNotUnfollowAnArtist(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	repository := NewRepository(pool)

	artistMBID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO artists (musicbrainz_id, name, sort_name, followed_at)
		VALUES ($1, 'Portishead', 'Portishead', now())
	`, artistMBID); err != nil {
		t.Fatal(err)
	}

	if _, err := repository.SaveCatalogueReleaseGroup(ctx, musicbrainz.ReleaseGroupDetail{
		ReleaseGroup: musicbrainz.ReleaseGroup{ID: uuid.New(), Title: "Third"},
		Artist:       musicbrainz.Artist{ID: artistMBID, Name: "Portishead", SortName: "Portishead"},
		Credit:       "Portishead",
	}); err != nil {
		t.Fatalf("SaveCatalogueReleaseGroup() error = %v", err)
	}

	var followed *time.Time
	if err := pool.QueryRow(ctx, `
		SELECT followed_at FROM artists WHERE musicbrainz_id = $1
	`, artistMBID).Scan(&followed); err != nil {
		t.Fatal(err)
	}
	if followed == nil {
		t.Error("followed_at = NULL, want the follow decision left alone")
	}
}

// Missing music is scoped to followed artists. A release held to explain one
// owned file is not a completion target, and reporting its other tracks as
// missing would answer a question nobody asked.
func TestHeldReleasesAreNotCountedAsMissing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	repository := NewRepository(pool)

	if _, err := repository.SaveCatalogueReleaseGroup(ctx, musicbrainz.ReleaseGroupDetail{
		ReleaseGroup: musicbrainz.ReleaseGroup{ID: uuid.New(), Title: "Dummy"},
		Artist: musicbrainz.Artist{
			ID: uuid.New(), Name: "Portishead", SortName: "Portishead",
		},
		Credit: "Portishead",
	}); err != nil {
		t.Fatalf("SaveCatalogueReleaseGroup() error = %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO tracks (album_id, title, disc_number, track_number)
		SELECT id, 'Roads', 1, 5 FROM albums
	`); err != nil {
		t.Fatal(err)
	}

	var missing, catalogueArtists int64
	if err := pool.QueryRow(ctx, `
		SELECT
		    (SELECT count(*) FROM tracks t
		        JOIN albums al ON al.id = t.album_id
		        JOIN artists ar ON ar.id = al.artist_id
		        WHERE ar.followed_at IS NOT NULL
		          AND NOT EXISTS (
		            SELECT 1 FROM track_mappings tm
		            JOIN library_files lf ON lf.id = tm.library_file_id
		            WHERE tm.track_id = t.id AND lf.missing_at IS NULL))::bigint,
		    (SELECT count(*) FROM artists WHERE followed_at IS NULL)::bigint
	`).Scan(&missing, &catalogueArtists); err != nil {
		t.Fatal(err)
	}
	if missing != 0 {
		t.Errorf("missing tracks = %d, want none under an artist nobody follows", missing)
	}
	if catalogueArtists != 1 {
		t.Errorf("held artists = %d, want 1", catalogueArtists)
	}
}

// An artist refresh may bring a release group in between the ingest being
// queued and it running. Holding an artist for a release somebody else already
// filed would leave that artist holding nothing, explaining itself with an
// album it does not own.
func TestIngestingAReleaseSomebodyElseFiledHoldsNobody(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	repository := NewRepository(pool)

	releaseGroupID, followedMBID := uuid.New(), uuid.New()
	var existingAlbumID uuid.UUID
	if err := pool.QueryRow(ctx, `
		WITH followed AS (
		    INSERT INTO artists (musicbrainz_id, name, sort_name, followed_at)
		    VALUES ($1, 'Portishead', 'Portishead', now())
		    RETURNING id
		)
		INSERT INTO albums (artist_id, musicbrainz_release_group_id, title)
		SELECT id, $2, 'Dummy' FROM followed
		RETURNING id
	`, followedMBID, releaseGroupID).Scan(&existingAlbumID); err != nil {
		t.Fatal(err)
	}

	albumID, err := repository.SaveCatalogueReleaseGroup(ctx, musicbrainz.ReleaseGroupDetail{
		ReleaseGroup: musicbrainz.ReleaseGroup{ID: releaseGroupID, Title: "Dummy"},
		Artist: musicbrainz.Artist{
			ID: uuid.New(), Name: "Various Artists", SortName: "Various Artists",
		},
		Credit: "Various Artists",
	})
	if err != nil {
		t.Fatalf("SaveCatalogueReleaseGroup() error = %v", err)
	}
	if albumID != existingAlbumID {
		t.Errorf("album = %s, want the one already filed (%s)", albumID, existingAlbumID)
	}

	var artists int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM artists`).Scan(&artists); err != nil {
		t.Fatal(err)
	}
	if artists != 1 {
		t.Errorf("artists = %d, want no second artist holding nothing", artists)
	}
}

// A release ingested because the library holds it is asked about at once. Left
// to the idle cycle it would be a grey rectangle for up to six hours: the sweep
// is the only thing that fills release_cover_art, and the pass it was waiting
// for had just gone by.
func TestAnIngestedReleaseIsAskedAboutBeforeTheIdleSweep(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	repository := NewRepository(pool)

	idle := seedIdleCoverArtSweep(ctx, t, pool)

	albumID, err := repository.SaveCatalogueReleaseGroup(ctx, musicbrainz.ReleaseGroupDetail{
		ReleaseGroup: musicbrainz.ReleaseGroup{ID: uuid.New(), Title: "Dummy"},
		Artist: musicbrainz.Artist{
			ID: uuid.New(), Name: "Portishead", SortName: "Portishead",
		},
		Credit: "Portishead",
	})
	if err != nil {
		t.Fatalf("SaveCatalogueReleaseGroup() error = %v", err)
	}

	assertCoverArtSweepMovedTo(ctx, t, pool, idle)

	// And the pass that has just been asked for would picture this release: the
	// sweep is woken to no purpose if the release it was woken for is not one of
	// the ones it goes looking at.
	waiting, err := db.New(pool).ReleasesMissingCoverArt(ctx, 25)
	if err != nil {
		t.Fatalf("ReleasesMissingCoverArt() error = %v", err)
	}
	if len(waiting) != 1 || waiting[0].ID != albumID {
		t.Errorf("releases the next pass would picture = %v, want the one just ingested (%s)",
			waiting, albumID)
	}
}

// An artist refresh is the other way a release enters the catalogue, and a
// followed artist's new album arrives through it. It wakes the sweep for the
// same reason.
func TestAReleaseFromAnArtistRefreshIsAskedAboutBeforeTheIdleSweep(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	repository := NewRepository(pool)

	artistID, musicBrainzID, releaseGroupID := uuid.New(), uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO artists (id, musicbrainz_id, name, sort_name)
		VALUES ($1, $2, 'Portishead', 'Portishead')
	`, artistID, musicBrainzID); err != nil {
		t.Fatalf("seed artist: %v", err)
	}
	idle := seedIdleCoverArtSweep(ctx, t, pool)

	if err := repository.SaveArtistCatalogue(ctx, artistID, musicbrainz.ArtistCatalogue{
		Artist:   musicbrainz.Artist{ID: musicBrainzID, Name: "Portishead", SortName: "Portishead"},
		Metadata: json.RawMessage(`{"id":"` + musicBrainzID.String() + `"}`),
		ReleaseGroups: []musicbrainz.ReleaseGroup{{
			ID: releaseGroupID, Title: "Third", FirstReleaseDate: "2008-04-27",
			PrimaryType: "Album", Metadata: json.RawMessage(`{"id":"` + releaseGroupID.String() + `"}`),
		}},
	}); err != nil {
		t.Fatalf("SaveArtistCatalogue() error = %v", err)
	}

	assertCoverArtSweepMovedTo(ctx, t, pool, idle)
}

// seedIdleCoverArtSweep leaves the sweep where a pass that found nothing to do
// leaves it: one queued job, six hours out. It returns that time, which is the
// wait an ingest has to shorten.
func seedIdleCoverArtSweep(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool,
) time.Time {
	t.Helper()
	idle := time.Now().Add(6 * time.Hour)
	if _, err := pool.Exec(ctx, `
		INSERT INTO jobs (kind, payload, max_attempts, run_after)
		VALUES ($1, '{}'::jsonb, 3, $2)
	`, SweepCoverArt, idle); err != nil {
		t.Fatalf("seed the idle cover art sweep: %v", err)
	}
	return idle
}

// assertCoverArtSweepMovedTo reads back the one sweep that may be queued and
// checks it is now due rather than at the idle time it was sitting on. One job,
// moved: a second sweep is what the partial unique index exists to refuse, and
// asserting the count is how a fix that queued another one would be caught.
func assertCoverArtSweepMovedTo(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool, idle time.Time,
) {
	t.Helper()
	var queued int
	var runAfter *time.Time
	if err := pool.QueryRow(ctx, `
		SELECT count(*), min(run_after)
		FROM jobs
		WHERE kind = $1 AND status = 'queued'
	`, SweepCoverArt).Scan(&queued, &runAfter); err != nil {
		t.Fatalf("read the cover art sweep: %v", err)
	}
	if queued != 1 || runAfter == nil {
		t.Fatalf("cover art sweeps queued = %d, want the one that was already there", queued)
	}
	if !runAfter.Before(idle) {
		t.Errorf("sweep due at %s, want it moved forward from the idle %s", *runAfter, idle)
	}
	if runAfter.After(time.Now()) {
		t.Errorf("sweep due at %s, want it due now", *runAfter)
	}
}

// The acquisition lane claims the searching and nothing else, so a twenty-second
// search window cannot hold up importing a transfer that has already arrived.
func TestTheAcquisitionLaneClaimsOnlyTheSearching(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	seedJobs(ctx, t, pool, ImportDownload, SweepAcquisitionTargets)

	job, err := NewRepository(pool).Claim(ctx, Acquisition())
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if job.Kind != SweepAcquisitionTargets {
		t.Fatalf("kind = %q, want the sweep even though the import was queued first", job.Kind)
	}
}

// The anchors lane claims only the preview sweep.
func TestTheAnchorLaneClaimsOnlyTheSweep(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	seedJobs(ctx, t, pool, JudgeWantCopies, SweepPreviewAnchors)

	repository := NewRepository(pool)
	job, err := repository.Claim(ctx, Anchors())
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if job.Kind != SweepPreviewAnchors {
		t.Fatalf("kind = %q, want the preview-anchor sweep", job.Kind)
	}
	if _, err := repository.Claim(ctx, Anchors()); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("second claim error = %v, want no other kind", err)
	}
}

// A pressed re-judge goes ahead of the per-want judges the anchor sweep queues.
func TestTheJudgingLaneClaimsOnlyJudgesAndPressesGoFirst(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	seedJobs(ctx, t, pool,
		JudgeWantCopies, JudgeCreditRefusals, JudgeCopiesAgain, SweepPreviewAnchors)

	repository := NewRepository(pool)
	for _, want := range []string{JudgeCopiesAgain, JudgeWantCopies, JudgeCreditRefusals} {
		job, err := repository.Claim(ctx, Judging())
		if err != nil {
			t.Fatalf("Claim() error = %v", err)
		}
		if job.Kind != want {
			t.Fatalf("kind = %q, want %q", job.Kind, want)
		}
	}
	if _, err := repository.Claim(ctx, Judging()); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("fourth claim error = %v, want no non-judge kind", err)
	}
}

// The general lane claims everything the other four do not, by exclusion rather
// than by listing. A job kind added later has to run somewhere.
func TestTheGeneralLaneClaimsEverythingTheOtherLanesDoNot(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	seedJobs(ctx, t, pool,
		SearchAlbumSources, PollDownloads, SweepPreviewAnchors,
		JudgeWantCopies, JudgeCopiesAgain, JudgeCreditRefusals,
		"a_kind_invented_after_the_lanes_were")

	repository := NewRepository(pool)
	job, err := repository.Claim(ctx, General())
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if job.Kind != "a_kind_invented_after_the_lanes_were" {
		t.Fatalf("kind = %q, want the kind no lane names", job.Kind)
	}
	if _, err := repository.Claim(ctx, General()); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("second claim error = %v, want all named lanes excluded", err)
	}
}

// A finished transfer has to be imported while its files are still where slskd
// left them, and the general queue routinely holds hundreds of catalogue rows
// queued before it. Its own lane is what stops the import waiting behind them.
func TestATransferIsFollowedWhileTheCatalogueBacklogWaits(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	seedJobs(ctx, t, pool,
		RefreshAlbumMetadata, RefreshAlbumMetadata, RefreshAlbumMetadata,
		ImportDownload, PollDownloads)

	repository := NewRepository(pool)
	job, err := repository.Claim(ctx, Transfers())
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if job.Kind != ImportDownload {
		t.Fatalf("kind = %q, want the import ahead of the metadata queued before it", job.Kind)
	}

	// And the general lane leaves the transfer kinds alone, so the import is
	// never claimed twice or held behind the same backlog after all.
	general, err := repository.Claim(ctx, General())
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if general.Kind != RefreshAlbumMetadata {
		t.Fatalf("kind = %q, want the general lane to take its own backlog", general.Kind)
	}
}

// A person pressing a button is waiting at a screen, and the queue regularly
// holds hundreds of rows of background work. The pressed job is claimed first;
// everything else keeps the order it was created in.
func TestWorkAPersonPressedIsClaimedBeforeTheBacklog(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	seedJobs(ctx, t, pool,
		RefreshAlbumMetadata, RefreshAlbumMetadata, SweepTranscode, RefreshAlbumMetadata)

	repository := NewRepository(pool)
	job, err := repository.Claim(ctx, General())
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if job.Kind != SweepTranscode {
		t.Fatalf("kind = %q, want the pressed sweep ahead of the backlog", job.Kind)
	}

	// And the backlog behind it is still in the order it was made.
	next, err := repository.Claim(ctx, General())
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if next.Kind != RefreshAlbumMetadata {
		t.Fatalf("kind = %q, want the backlog to follow in its own order", next.Kind)
	}
}

// One job is claimed once however many lanes are asking: a kind on both lanes'
// side of the line would otherwise be run twice at once.
func TestAJobIsClaimedByOneLaneOnly(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	seedJobs(ctx, t, pool, SweepAcquisitionTargets)
	repository := NewRepository(pool)

	if _, err := repository.Claim(ctx, Acquisition()); err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if _, err := repository.Claim(ctx, Lane{}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("second claim error = %v, want the job to be taken already", err)
	}
}

func seedJobs(ctx context.Context, t *testing.T, pool *pgxpool.Pool, kinds ...string) {
	t.Helper()
	for _, kind := range kinds {
		if _, err := pool.Exec(ctx, `
			INSERT INTO jobs (kind, payload) VALUES ($1, '{}'::jsonb)
		`, kind); err != nil {
			t.Fatalf("seed %s job: %v", kind, err)
		}
	}
}

// jobStatus reads back what became of one job.
func jobStatus(ctx context.Context, t *testing.T, pool *pgxpool.Pool, jobID uuid.UUID) (string, string) {
	t.Helper()
	var status string
	var message *string
	if err := pool.QueryRow(ctx, `
		SELECT status, error_message FROM jobs WHERE id = $1
	`, jobID).Scan(&status, &message); err != nil {
		t.Fatalf("read job %s: %v", jobID, err)
	}
	if message == nil {
		return status, ""
	}
	return status, *message
}

// A process that stopped mid-job leaves rows marked running that nobody is
// running. They go back on the queue at startup, saying why, so the work is
// picked up rather than sitting there forever looking busy.
func TestJobsAShutdownInterruptedGoBackOnTheQueue(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	seedJobs(ctx, t, pool, ScanLibrary)
	repository := NewRepository(pool)

	job, err := repository.Claim(ctx, Lane{})
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if err := repository.RecoverInterrupted(ctx); err != nil {
		t.Fatalf("RecoverInterrupted() error = %v", err)
	}

	status, message := jobStatus(ctx, t, pool, job.ID)
	if status != "queued" {
		t.Fatalf("status = %q, want the job back on the queue", status)
	}
	if message == "" {
		t.Error("error_message is empty; a job put back must say why it was")
	}
}

// A job that finished while it was not running is one somebody else already
// settled, and overwriting that would lose whichever answer came first.
func TestOnlyARunningJobIsCompleted(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	seedJobs(ctx, t, pool, ScanLibrary)

	var jobID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM jobs`).Scan(&jobID); err != nil {
		t.Fatal(err)
	}
	if err := NewRepository(pool).Complete(ctx, Job{ID: jobID}); err == nil {
		t.Fatal("Complete() error = nil, want a queued job refused")
	}
}

// A scan can wake the witness coordinator after it has been claimed. The
// running row records that wake, and completion queues a due follow-up.
func TestARunningLibraryWitnessSweepKeepsAWake(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	repository := NewRepository(pool)

	if err := repository.QueueLibraryWitnessFingerprintSweep(ctx, time.Now()); err != nil {
		t.Fatalf("QueueLibraryWitnessFingerprintSweep() error = %v", err)
	}
	job, err := repository.Claim(ctx, Lane{Only: []string{SweepLibraryWitnesses}})
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if job.Kind != SweepLibraryWitnesses {
		t.Fatalf("claimed job kind = %q, want %q", job.Kind, SweepLibraryWitnesses)
	}
	if err := repository.QueueLibraryWitnessFingerprintSweep(ctx, time.Now()); err != nil {
		t.Fatalf("wake the running sweep: %v", err)
	}
	woken, err := repository.CompleteLibraryWitnessSweep(ctx, job)
	if err != nil {
		t.Fatalf("CompleteLibraryWitnessSweep() error = %v", err)
	}
	if !woken {
		t.Fatal("CompleteLibraryWitnessSweep() woken = false, want true")
	}
	var due int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM jobs
		WHERE kind = $q$sweep_library_witnesses$q$
		  AND status = $q$queued$q$ AND run_after <= now()
	`).Scan(&due); err != nil {
		t.Fatal(err)
	}
	if due != 1 {
		t.Fatalf("due follow-up sweeps = %d, want 1", due)
	}
}

// A sweep already running cannot be moved — there is nothing to move, it is
// live — so before this a wake asking for one while a pass ran was a silent
// no-op against the partial index over ('queued', 'running'). Now
// QueueAcquisitionSweep marks the running row instead, and
// CompleteAcquisitionSweep reads that mark in the same statement that retires
// the row and queues the follow-up sweep itself.
func TestCompletingAnAcquisitionSweepQueuesAnotherIfSomethingWokeItWhileRunning(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	repository := NewRepository(pool)

	if err := repository.QueueAcquisitionSweep(ctx, time.Now()); err != nil {
		t.Fatalf("QueueAcquisitionSweep() error = %v", err)
	}
	job, err := repository.Claim(ctx, Lane{})
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if job.Kind != SweepAcquisitionTargets {
		t.Fatalf("claimed = %s, want %s", job.Kind, SweepAcquisitionTargets)
	}

	// This used to do nothing at all: the row is running, so the conflict's
	// WHERE clause matched nothing.
	if err := repository.QueueAcquisitionSweep(ctx, time.Now()); err != nil {
		t.Fatalf("QueueAcquisitionSweep() while running, error = %v", err)
	}
	var liveSweeps int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM jobs
		WHERE kind = 'sweep_acquisition_targets' AND status IN ('queued', 'running')
	`).Scan(&liveSweeps); err != nil {
		t.Fatal(err)
	}
	if liveSweeps != 1 {
		t.Fatalf("live sweeps = %d, want the wake to mark the running row rather than queue a second one",
			liveSweeps)
	}

	woken, err := repository.CompleteAcquisitionSweep(ctx, job)
	if err != nil {
		t.Fatalf("CompleteAcquisitionSweep() error = %v", err)
	}
	if !woken {
		t.Fatal("CompleteAcquisitionSweep() woken = false, want true: a wake landed while this pass ran")
	}

	var queued int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM jobs
		WHERE kind = 'sweep_acquisition_targets' AND status = 'queued'
	`).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 1 {
		t.Fatalf("queued sweeps after finishing = %d, want one queued for the wake this pass received",
			queued)
	}
}

// A pass nothing woke during it finishes clean: nothing queues a follow-up of
// its own, which is what leaves rescheduleAcquisitionSweep free to decide
// whether one is needed.
func TestCompletingAnAcquisitionSweepQueuesNothingWhenNothingWokeIt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	repository := NewRepository(pool)

	if err := repository.QueueAcquisitionSweep(ctx, time.Now()); err != nil {
		t.Fatalf("QueueAcquisitionSweep() error = %v", err)
	}
	job, err := repository.Claim(ctx, Lane{})
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}

	woken, err := repository.CompleteAcquisitionSweep(ctx, job)
	if err != nil {
		t.Fatalf("CompleteAcquisitionSweep() error = %v", err)
	}
	if woken {
		t.Fatal("CompleteAcquisitionSweep() woken = true, want false: nothing asked for a sweep")
	}

	var queued int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM jobs
		WHERE kind = 'sweep_acquisition_targets' AND status = 'queued'
	`).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 0 {
		t.Fatalf("queued sweeps = %d, want none: nothing woke this pass", queued)
	}
}

// The claim may legitimately outlive any fixed runtime threshold. A heartbeat
// proves that its worker is still present, so the reaper leaves the row alone.
func TestAHeartbeatKeepsALongRunningJobOwned(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	seedJobs(ctx, t, pool, ScanLibrary)
	repository := NewRepository(pool)

	job, err := repository.Claim(ctx, Lane{})
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if err := pool.QueryRow(ctx, `
		UPDATE jobs SET started_at = now() - interval '6 hours', updated_at = now() - interval '6 hours'
		WHERE id = $1
		RETURNING started_at
	`, job.ID).Scan(&job.StartedAt); err != nil {
		t.Fatal(err)
	}
	owned, err := repository.Heartbeat(ctx, job)
	if err != nil || !owned {
		t.Fatalf("Heartbeat() = (%t, %v), want the claim renewed", owned, err)
	}
	if reaped, err := repository.ReapStalled(ctx, time.Hour); err != nil || reaped != 0 {
		t.Fatalf("ReapStalled() = (%d, %v), want the live job left alone", reaped, err)
	}
	if status, _ := jobStatus(ctx, t, pool, job.ID); status != "running" {
		t.Fatalf("status = %q, want running", status)
	}
}

// The attempt and start time identify one claim. Once a stale claim has been
// reaped and the row claimed again, its old worker can neither renew nor settle
// the new run.
func TestAWorkerFromAnOldClaimCannotSettleTheNewOne(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	seedJobs(ctx, t, pool, ScanLibrary)
	repository := NewRepository(pool)

	oldClaim, err := repository.Claim(ctx, Lane{})
	if err != nil {
		t.Fatalf("first Claim() error = %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE jobs SET updated_at = now() - interval '2 hours' WHERE id = $1`, oldClaim.ID); err != nil {
		t.Fatal(err)
	}
	if reaped, err := repository.ReapStalled(ctx, time.Hour); err != nil || reaped != 1 {
		t.Fatalf("ReapStalled() = (%d, %v), want the abandoned claim requeued", reaped, err)
	}
	// An operator retry resets the attempt ladder. Reproduce that reset here so
	// both claims have attempt 1 and the claim timestamp has to distinguish them.
	if _, err := pool.Exec(ctx, `UPDATE jobs SET attempts = 0 WHERE id = $1`, oldClaim.ID); err != nil {
		t.Fatal(err)
	}
	newClaim, err := repository.Claim(ctx, Lane{})
	if err != nil {
		t.Fatalf("second Claim() error = %v", err)
	}
	if oldClaim.Attempts != newClaim.Attempts {
		t.Fatalf("attempts = old %d new %d, want the reset to make them collide", oldClaim.Attempts, newClaim.Attempts)
	}
	if owned, err := repository.Heartbeat(ctx, oldClaim); err != nil || owned {
		t.Fatalf("old Heartbeat() = (%t, %v), want the later claim protected", owned, err)
	}
	if err := repository.Complete(ctx, oldClaim); err == nil {
		t.Fatal("old Complete() error = nil, want the later claim protected")
	}
	if status, _ := jobStatus(ctx, t, pool, oldClaim.ID); status != "running" {
		t.Fatalf("status = %q, want the new claim still running", status)
	}
	if err := repository.Complete(ctx, newClaim); err != nil {
		t.Fatalf("new Complete() error = %v", err)
	}
}

// A retry puts the job back with the reason and the moment to try again, so the
// backoff the worker chose is the backoff the queue honours.
func TestARetriedJobIsQueuedAgainForTheMomentItWasGiven(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	seedJobs(ctx, t, pool, ScanLibrary)
	repository := NewRepository(pool)

	job, err := repository.Claim(ctx, Lane{})
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	runAfter := time.Now().Add(time.Hour).UTC().Truncate(time.Millisecond)
	if err := repository.Retry(ctx, job, "musicbrainz did not respond", runAfter); err != nil {
		t.Fatalf("Retry() error = %v", err)
	}

	var status string
	var storedRunAfter time.Time
	if err := pool.QueryRow(ctx, `
		SELECT status, run_after FROM jobs WHERE id = $1
	`, job.ID).Scan(&status, &storedRunAfter); err != nil {
		t.Fatal(err)
	}
	if status != "queued" || !storedRunAfter.Equal(runAfter) {
		t.Fatalf("status = %q run_after = %s, want it queued for %s", status, storedRunAfter, runAfter)
	}
}

// Retrying a job nobody is running would resurrect one that has already been
// answered, so it is refused rather than applied.
func TestOnlyARunningJobIsRetried(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	seedJobs(ctx, t, pool, ScanLibrary)

	var jobID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM jobs`).Scan(&jobID); err != nil {
		t.Fatal(err)
	}
	if err := NewRepository(pool).Retry(ctx, Job{ID: jobID}, "a reason", time.Now()); err == nil {
		t.Fatal("Retry() error = nil, want a queued job refused")
	}
}

// A failed job records why it failed, because that reason is the whole of what
// the interface has to show for the work that did not happen.
func TestAFailedJobKeepsTheReasonItFailed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	seedJobs(ctx, t, pool, ScanLibrary)
	repository := NewRepository(pool)

	job, err := repository.Claim(ctx, Lane{})
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if err := repository.Fail(ctx, job, "no enabled music folders are configured"); err != nil {
		t.Fatalf("Fail() error = %v", err)
	}

	status, message := jobStatus(ctx, t, pool, job.ID)
	if status != "failed" || message != "no enabled music folders are configured" {
		t.Fatalf("status = %q message = %q", status, message)
	}
}

// A cancelled job was stopped rather than tried, and the two have to read
// differently: a release whose run was called off was never found wanting.
func TestACancelledJobIsStoppedRatherThanFailed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	seedJobs(ctx, t, pool, SearchAlbumSources)
	repository := NewRepository(pool)

	job, err := repository.Claim(ctx, Lane{})
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if err := repository.Cancel(ctx, job, "the run was stopped"); err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}

	if status, _ := jobStatus(ctx, t, pool, job.ID); status != "cancelled" {
		t.Fatalf("status = %q, want cancelled", status)
	}
}

// A run is cancelled when the jobs that carry it are, and a search that fails
// afterwards has to be able to tell.
func TestASearchCanTellItsRunWasCancelled(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	repository := NewRepository(pool)

	runID := uuid.New()
	payload, _ := json.Marshal(map[string]uuid.UUID{"runId": runID, "albumId": uuid.New()})
	if _, err := pool.Exec(ctx, `
		INSERT INTO jobs (kind, payload, status) VALUES ($1, $2, 'cancelled')
	`, SearchAlbumSources, payload); err != nil {
		t.Fatal(err)
	}

	cancelled, err := repository.SourceSearchRunCancelled(ctx, runID)
	if err != nil {
		t.Fatalf("SourceSearchRunCancelled() error = %v", err)
	}
	if !cancelled {
		t.Fatal("the run reads as live although its jobs were cancelled")
	}
}

// A run nobody stopped reads as live, or every failed search would be treated
// as one whose run had been called off.
func TestARunNobodyStoppedReadsAsLive(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	cancelled, err := NewRepository(pool).SourceSearchRunCancelled(ctx, uuid.New())
	if err != nil {
		t.Fatalf("SourceSearchRunCancelled() error = %v", err)
	}
	if cancelled {
		t.Fatal("a run with no cancelled jobs reads as cancelled")
	}
}

// Each self-scheduling job asks for its own successor, and the schedule holds
// one of each: a second copy would double the rate every pass.
func TestTheSelfSchedulingJobsQueueOneSuccessorEach(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	repository := NewRepository(pool)

	runAfter := time.Now().Add(time.Hour)
	for _, queueOne := range []func(context.Context, time.Time) error{
		repository.QueueDownloadPoll,
		repository.QueueAcquisitionSweep,
		repository.QueueCoverArtSweep,
		repository.QueueFollowFeedSweep,
		repository.QueueRecommendationSweep,
	} {
		if err := queueOne(ctx, runAfter); err != nil {
			t.Fatalf("queueing a successor: %v", err)
		}
		if err := queueOne(ctx, runAfter); err != nil {
			t.Fatalf("queueing a second successor: %v", err)
		}
	}

	var queued int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM jobs WHERE kind = ANY($1::text[])
	`, []string{
		PollDownloads, SweepAcquisitionTargets, SweepCoverArt,
		SweepFollowFeed, SweepRecommendations,
	}).
		Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 5 {
		t.Fatalf("jobs queued = %d, want one of each of the five", queued)
	}
}

// The whole pass over the failed list, against the real queue.
//
// Every fifteen minutes Schall reads the jobs that failed for good, keeps the
// ones an outage stopped, and puts those back for one more attempt. The
// recommendation sweep is a job that schedules its own successor, and the
// database allows one copy of it to be queued or running at a time. So a sweep
// that failed sits beside a live successor and cannot go back — and while the
// pass was one statement for the whole batch, that one refusal aborted it, and
// the transfer imports a MusicBrainz timeout had stopped were left failed with
// it.
func TestTheFailedSweepBesideItsSuccessorDoesNotHoldTheOtherJobsBack(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	if err := NewRepository(pool).QueueRecommendationSweep(ctx, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("queue the live sweep: %v", err)
	}
	sweep := seedSpentJob(ctx, t, pool, SweepRecommendations,
		"sweep recommendations: Get \"https://listenbrainz.org/1/cf/recommendation\": "+
			"context deadline exceeded (Client.Timeout exceeded while awaiting headers)")
	transfer := seedSpentJob(ctx, t, pool, ImportDownload,
		"import download: verify \"/music/01 track.flac\" against recording "+
			"e4d6ec0e-4a4c-4d24-9b2c-2f0dc9f2f0aa: look up MusicBrainz recording: "+
			"Get \"https://musicbrainz.org/ws/2/recording/e4d6ec0e-4a4c-4d24-9b2c-2f0dc9f2f0aa\": "+
			"context deadline exceeded (Client.Timeout exceeded while awaiting headers)")

	NewWorker(NewRepository(pool), fakeProvider{}, zerolog.Nop()).reviveSpent(ctx)

	if status, _ := jobStatus(ctx, t, pool, transfer); status != "queued" {
		t.Fatalf("the transfer import is %q, want it back on the queue", status)
	}
	if status, _ := jobStatus(ctx, t, pool, sweep); status != "failed" {
		t.Fatalf("the failed sweep is %q, want it left beside its live successor", status)
	}
}

// seedSpentJob writes a job that failed as often as it was allowed to, long
// enough ago for the pass over the failed list to look at it.
func seedSpentJob(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool, kind, message string,
) uuid.UUID {
	t.Helper()
	var jobID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO jobs (
		    kind, status, attempts, max_attempts, started_at, completed_at, error_message
		)
		VALUES ($1, 'failed', 3, 3, now() - interval '2 hours', now() - interval '2 hours', $2)
		RETURNING id
	`, kind, message).Scan(&jobID); err != nil {
		t.Fatalf("seed spent %s job: %v", kind, err)
	}
	return jobID
}

// A scan discovers music; it does not find out what that music is. The files
// nobody has asked the providers about are queued afterwards, one job each.
func TestTheFilesNobodyHasAskedAboutAreQueuedForResolution(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	if _, err := pool.Exec(ctx, `
		INSERT INTO library_files (path, size_bytes, modified_at)
		VALUES ('/music/portishead/dummy/01.flac', 1024, now())
	`); err != nil {
		t.Fatal(err)
	}

	queued, err := NewRepository(pool).QueueFileResolutions(ctx, resolutionBatch)
	if err != nil {
		t.Fatalf("QueueFileResolutions() error = %v", err)
	}
	if queued != 1 {
		t.Fatalf("queued = %d, want the one unasked file", queued)
	}
}

// The refresh needs both the release group and whichever edition was chosen, so
// the fetch asks about the release that is actually represented.
func TestAnAlbumNamesItsReleaseGroupAndItsChosenEdition(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	releaseGroupID, releaseID := uuid.New(), uuid.New()
	var albumID uuid.UUID
	if err := pool.QueryRow(ctx, `
		WITH artist AS (
		    INSERT INTO artists (musicbrainz_id, name, sort_name)
		    VALUES ($1, 'Portishead', 'Portishead')
		    RETURNING id
		), album AS (
		    INSERT INTO albums (artist_id, musicbrainz_release_group_id, title)
		    SELECT id, $2, 'Dummy' FROM artist
		    RETURNING id
		)
		INSERT INTO release_editions (album_id, musicbrainz_release_id, title, selection_reason)
		SELECT id, $3, 'Dummy', 'Earliest official edition' FROM album
		RETURNING album_id
	`, uuid.New(), releaseGroupID, releaseID).Scan(&albumID); err != nil {
		t.Fatal(err)
	}

	gotGroup, gotRelease, err := NewRepository(pool).AlbumMusicBrainzIDs(ctx, albumID)
	if err != nil {
		t.Fatalf("AlbumMusicBrainzIDs() error = %v", err)
	}
	if gotGroup != releaseGroupID || gotRelease != releaseID {
		t.Fatalf("ids = (%s, %s), want (%s, %s)", gotGroup, gotRelease, releaseGroupID, releaseID)
	}
}

// An album nobody has refreshed yet has no edition, and the refresh is exactly
// what chooses one — so no edition is an answer rather than a failure.
func TestAnAlbumWithNoEditionYetNamesNoRelease(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	releaseGroupID := uuid.New()
	var albumID uuid.UUID
	if err := pool.QueryRow(ctx, `
		WITH artist AS (
		    INSERT INTO artists (musicbrainz_id, name, sort_name)
		    VALUES ($1, 'Portishead', 'Portishead')
		    RETURNING id
		)
		INSERT INTO albums (artist_id, musicbrainz_release_group_id, title)
		SELECT id, $2, 'Dummy' FROM artist
		RETURNING id
	`, uuid.New(), releaseGroupID).Scan(&albumID); err != nil {
		t.Fatal(err)
	}

	gotGroup, gotRelease, err := NewRepository(pool).AlbumMusicBrainzIDs(ctx, albumID)
	if err != nil {
		t.Fatalf("AlbumMusicBrainzIDs() error = %v", err)
	}
	if gotGroup != releaseGroupID || gotRelease != uuid.Nil {
		t.Fatalf("ids = (%s, %s), want the group alone", gotGroup, gotRelease)
	}
}

// An album with no MusicBrainz identity cannot be refreshed against MusicBrainz,
// and saying so is better than fetching whatever the nil UUID names.
func TestAnAlbumWithNoMusicBrainzIdentityCannotBeRefreshed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	var albumID uuid.UUID
	if err := pool.QueryRow(ctx, `
		WITH artist AS (
		    INSERT INTO artists (musicbrainz_id, name, sort_name)
		    VALUES ($1, 'Portishead', 'Portishead')
		    RETURNING id
		)
		INSERT INTO albums (artist_id, title) SELECT id, 'Dummy' FROM artist
		RETURNING id
	`, uuid.New()).Scan(&albumID); err != nil {
		t.Fatal(err)
	}

	if _, _, err := NewRepository(pool).AlbumMusicBrainzIDs(ctx, albumID); err == nil {
		t.Fatal("AlbumMusicBrainzIDs() error = nil, want an album with no identity refused")
	}
}

// The same for an artist: no identity is not a MusicBrainz lookup waiting to be
// made, it is one that cannot be.
func TestAnArtistWithNoMusicBrainzIdentityCannotBeRefreshed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	var artistID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO artists (name, sort_name) VALUES ('Portishead', 'Portishead')
		RETURNING id
	`).Scan(&artistID); err != nil {
		t.Fatal(err)
	}

	if _, err := NewRepository(pool).ArtistMusicBrainzID(ctx, artistID); err == nil {
		t.Fatal("ArtistMusicBrainzID() error = nil, want an artist with no identity refused")
	}
}

// An artist row that went away between the job being queued and it running is
// not an artist to refresh.
func TestAnArtistThatIsNoLongerThereCannotBeRefreshed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	_, err := NewRepository(pool).ArtistMusicBrainzID(ctx, uuid.New())
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("ArtistMusicBrainzID() error = %v, want no rows", err)
	}
}

// The identity in the catalogue is what the refresh was queued against. An
// artist whose identity changed underneath it is a different artist, and writing
// this catalogue over them would file one artist's releases under another.
func TestACatalogueIsRefusedWhenTheArtistIdentityChangedUnderIt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	var artistID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO artists (musicbrainz_id, name, sort_name)
		VALUES ($1, 'Portishead', 'Portishead')
		RETURNING id
	`, uuid.New()).Scan(&artistID); err != nil {
		t.Fatal(err)
	}

	err := NewRepository(pool).SaveArtistCatalogue(ctx, artistID, musicbrainz.ArtistCatalogue{
		Artist: musicbrainz.Artist{ID: uuid.New(), Name: "Somebody else", SortName: "Somebody else"},
	})
	if err == nil {
		t.Fatal("SaveArtistCatalogue() error = nil, want a changed identity refused")
	}
}

// MusicBrainz writes its secondary types in its own casing. They are stored the
// way the primary type is, so "Live" and "DJ-mix" compare equal to the lowercase
// sets the monitor level filters by, and a blank one is not a type at all.
func TestSecondaryTypesAreStoredTheWayThePrimaryTypeIs(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	artistMBID, releaseGroupID := uuid.New(), uuid.New()
	var artistID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO artists (musicbrainz_id, name, sort_name)
		VALUES ($1, 'Portishead', 'Portishead')
		RETURNING id
	`, artistMBID).Scan(&artistID); err != nil {
		t.Fatal(err)
	}

	if err := NewRepository(pool).SaveArtistCatalogue(ctx, artistID, musicbrainz.ArtistCatalogue{
		Artist: musicbrainz.Artist{ID: artistMBID, Name: "Portishead", SortName: "Portishead"},
		ReleaseGroups: []musicbrainz.ReleaseGroup{{
			ID: releaseGroupID, Title: "Roseland NYC Live", PrimaryType: "Album",
			SecondaryTypes: []string{"Live", "  ", "DJ-mix"},
		}},
	}); err != nil {
		t.Fatalf("SaveArtistCatalogue() error = %v", err)
	}

	var stored []string
	if err := pool.QueryRow(ctx, `
		SELECT secondary_types FROM albums WHERE musicbrainz_release_group_id = $1
	`, releaseGroupID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if len(stored) != 2 || stored[0] != "live" || stored[1] != "dj-mix" {
		t.Fatalf("secondary types = %v, want the two lowercased", stored)
	}
}

// An edition somebody chose by hand says so, rather than repeating the reason
// the automatic choice would have given.
func TestAnEditionChosenByHandSaysItWasChosenByHand(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	releaseID := uuid.New()
	var albumID uuid.UUID
	if err := pool.QueryRow(ctx, `
		WITH artist AS (
		    INSERT INTO artists (musicbrainz_id, name, sort_name)
		    VALUES ($1, 'Portishead', 'Portishead')
		    RETURNING id
		)
		INSERT INTO albums (
		    artist_id, musicbrainz_release_group_id, title, preferred_musicbrainz_release_id
		)
		SELECT id, $2, 'Dummy', $3 FROM artist
		RETURNING id
	`, uuid.New(), uuid.New(), releaseID).Scan(&albumID); err != nil {
		t.Fatal(err)
	}

	if err := NewRepository(pool).SaveReleaseCatalogue(ctx, albumID, musicbrainz.ReleaseCatalogue{
		Edition: musicbrainz.ReleaseEdition{
			ID: releaseID, Title: "Dummy", SelectionReason: "Earliest official edition",
		},
	}); err != nil {
		t.Fatalf("SaveReleaseCatalogue() error = %v", err)
	}

	var reason string
	var automatic bool
	if err := pool.QueryRow(ctx, `
		SELECT selection_reason, selected_automatically FROM release_editions WHERE album_id = $1
	`, albumID).Scan(&reason, &automatic); err != nil {
		t.Fatal(err)
	}
	if automatic || reason != "Selected manually" {
		t.Fatalf("automatic = %t reason = %q, want the manual choice recorded", automatic, reason)
	}
}

// A refresh in flight when somebody chose a different edition would write the
// old one over the new choice. Manual decisions win, so the refresh gives way.
func TestACatalogueIsRefusedWhenTheChosenEditionChangedUnderIt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	var albumID uuid.UUID
	if err := pool.QueryRow(ctx, `
		WITH artist AS (
		    INSERT INTO artists (musicbrainz_id, name, sort_name)
		    VALUES ($1, 'Portishead', 'Portishead')
		    RETURNING id
		)
		INSERT INTO albums (
		    artist_id, musicbrainz_release_group_id, title, preferred_musicbrainz_release_id
		)
		SELECT id, $2, 'Dummy', $3 FROM artist
		RETURNING id
	`, uuid.New(), uuid.New(), uuid.New()).Scan(&albumID); err != nil {
		t.Fatal(err)
	}

	err := NewRepository(pool).SaveReleaseCatalogue(ctx, albumID, musicbrainz.ReleaseCatalogue{
		Edition: musicbrainz.ReleaseEdition{ID: uuid.New(), Title: "A different edition"},
	})
	if err == nil {
		t.Fatal("SaveReleaseCatalogue() error = nil, want the manual choice left alone")
	}
}

// A release refresh changes which recordings the catalogue holds, so whatever
// the library already answers for has to be looked at again.
func TestARefreshedReleaseIsMatchedAgainstTheLibrary(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	var albumID uuid.UUID
	if err := pool.QueryRow(ctx, `
		WITH artist AS (
		    INSERT INTO artists (musicbrainz_id, name, sort_name)
		    VALUES ($1, 'Portishead', 'Portishead')
		    RETURNING id
		)
		INSERT INTO albums (artist_id, musicbrainz_release_group_id, title)
		SELECT id, $2, 'Dummy' FROM artist
		RETURNING id
	`, uuid.New(), uuid.New()).Scan(&albumID); err != nil {
		t.Fatal(err)
	}

	matcher := &recordingMatcher{}
	repository := NewRepository(pool, matcher)
	if err := repository.SaveReleaseCatalogue(ctx, albumID, musicbrainz.ReleaseCatalogue{
		Edition: musicbrainz.ReleaseEdition{
			ID: uuid.New(), Title: "Dummy", SelectionReason: "Earliest official edition",
		},
	}); err != nil {
		t.Fatalf("SaveReleaseCatalogue() error = %v", err)
	}

	if matcher.reconciled != albumID {
		t.Fatalf("reconciled = %s, want %s", matcher.reconciled, albumID)
	}
}

// A rematch that could not be run leaves the library disagreeing with the
// catalogue it was just refreshed against, so the refresh reports the failure
// rather than reading as done.
func TestARefreshIsNotDoneUntilTheLibraryHasBeenMatchedAgain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	var albumID uuid.UUID
	if err := pool.QueryRow(ctx, `
		WITH artist AS (
		    INSERT INTO artists (musicbrainz_id, name, sort_name)
		    VALUES ($1, 'Portishead', 'Portishead')
		    RETURNING id
		)
		INSERT INTO albums (artist_id, musicbrainz_release_group_id, title)
		SELECT id, $2, 'Dummy' FROM artist
		RETURNING id
	`, uuid.New(), uuid.New()).Scan(&albumID); err != nil {
		t.Fatal(err)
	}

	matcher := &recordingMatcher{err: errors.New("the database is unreachable")}
	err := NewRepository(pool, matcher).SaveReleaseCatalogue(ctx, albumID, musicbrainz.ReleaseCatalogue{
		Edition: musicbrainz.ReleaseEdition{
			ID: uuid.New(), Title: "Dummy", SelectionReason: "Earliest official edition",
		},
	})
	if err == nil {
		t.Fatal("SaveReleaseCatalogue() error = nil, want the failed rematch reported")
	}
}

// An album deleted between its refresh being queued and it running is not an
// album to fetch.
func TestAnAlbumThatIsNoLongerThereCannotBeRefreshed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	_, _, err := NewRepository(pool).AlbumMusicBrainzIDs(ctx, uuid.New())
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("AlbumMusicBrainzIDs() error = %v, want no rows", err)
	}
}

// recordingMatcher records which album was matched against the library.
type recordingMatcher struct {
	reconciled uuid.UUID
	err        error
}

func (matcher *recordingMatcher) ReconcileAlbum(_ context.Context, albumID uuid.UUID) error {
	matcher.reconciled = albumID
	return matcher.err
}

// MusicBrainz writes a collaboration as one credit. Naming the release after
// whichever artist happens to be first would explain a held artist with an album
// they only half made; an empty credit falls back to the artist's own name.
func TestAHeldReleaseWithoutACreditIsExplainedByItsArtist(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	albumID, err := NewRepository(pool).SaveCatalogueReleaseGroup(ctx, musicbrainz.ReleaseGroupDetail{
		ReleaseGroup: musicbrainz.ReleaseGroup{ID: uuid.New(), Title: "Dummy"},
		Artist:       musicbrainz.Artist{ID: uuid.New(), Name: "Portishead", SortName: "Portishead"},
	})
	if err != nil {
		t.Fatalf("SaveCatalogueReleaseGroup() error = %v", err)
	}

	var summary string
	if err := pool.QueryRow(ctx, `
		SELECT coalesce(artists.catalogue_summary, '')
		FROM albums JOIN artists ON artists.id = albums.artist_id
		WHERE albums.id = $1
	`, albumID).Scan(&summary); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(summary, "Portishead") {
		t.Fatalf("summary = %q, want the artist named", summary)
	}
}

// MusicBrainz holds a credit as a list, and the catalogue now holds it as one
// too: the names in the order they were printed, each against the artist the
// provider says it is, with the phrase printed between them kept verbatim.
func TestARefreshHoldsTheAlbumCreditAsAList(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	albumID := seedCreditedAlbum(ctx, t, pool)
	first, second := uuid.New(), uuid.New()

	if err := NewRepository(pool).SaveReleaseCatalogue(ctx, albumID, creditedCatalogue(
		[]musicbrainz.Credit{
			{Name: "Porter Robinson", ArtistID: first, JoinPhrase: " & "},
			{Name: "Ninajirachi", ArtistID: second},
		}, nil,
	)); err != nil {
		t.Fatalf("SaveReleaseCatalogue() error = %v", err)
	}

	credits := storedCredits(ctx, t, pool, albumCreditQuery, albumID)
	if len(credits) != 2 {
		t.Fatalf("album credits = %#v, want 2", credits)
	}
	if credits[0] != (storedCredit{1, "Porter Robinson", first, " & "}) {
		t.Errorf("first credit = %#v", credits[0])
	}
	if credits[1] != (storedCredit{2, "Ninajirachi", second, ""}) {
		t.Errorf("second credit = %#v", credits[1])
	}
}

// A credit MusicBrainz has no artist entity for keeps its place with no
// identifier. Dropping it would leave the credit reading as one the release
// never printed, with an artist missing from the middle of it.
func TestARefreshKeepsThePlaceOfACreditWithNoArtist(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	albumID := seedCreditedAlbum(ctx, t, pool)
	known := uuid.New()

	if err := NewRepository(pool).SaveReleaseCatalogue(ctx, albumID, creditedCatalogue(nil,
		[]musicbrainz.Credit{
			{Name: "A choir nobody catalogued", JoinPhrase: " feat. "},
			{Name: "Ninajirachi", ArtistID: known},
		},
	)); err != nil {
		t.Fatalf("SaveReleaseCatalogue() error = %v", err)
	}

	var trackID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM tracks WHERE album_id = $1`, albumID).Scan(&trackID); err != nil {
		t.Fatal(err)
	}
	credits := storedCredits(ctx, t, pool, trackCreditQuery, trackID)
	if len(credits) != 2 {
		t.Fatalf("track credits = %#v, want 2", credits)
	}
	if credits[0] != (storedCredit{1, "A choir nobody catalogued", uuid.Nil, " feat. "}) {
		t.Errorf("first credit = %#v", credits[0])
	}
	if credits[1] != (storedCredit{2, "Ninajirachi", known, ""}) {
		t.Errorf("second credit = %#v", credits[1])
	}
}

// A refresh writes the credit it was given over the one held before. An artist
// MusicBrainz has since dropped from the credit would otherwise stay in the
// list forever, standing at a position that is now somebody else's.
func TestARefreshReplacesTheCreditItHeldBefore(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	albumID := seedCreditedAlbum(ctx, t, pool)
	repository := NewRepository(pool)

	if err := repository.SaveReleaseCatalogue(ctx, albumID, creditedCatalogue(
		[]musicbrainz.Credit{
			{Name: "Porter Robinson", ArtistID: uuid.New(), JoinPhrase: ", "},
			{Name: "A guest since withdrawn", ArtistID: uuid.New(), JoinPhrase: " & "},
			{Name: "Ninajirachi", ArtistID: uuid.New()},
		}, nil,
	)); err != nil {
		t.Fatalf("SaveReleaseCatalogue() error = %v", err)
	}
	if err := repository.SaveReleaseCatalogue(ctx, albumID, creditedCatalogue(
		[]musicbrainz.Credit{
			{Name: "Porter Robinson", ArtistID: uuid.New(), JoinPhrase: " & "},
			{Name: "Ninajirachi", ArtistID: uuid.New()},
		}, nil,
	)); err != nil {
		t.Fatalf("second SaveReleaseCatalogue() error = %v", err)
	}

	credits := storedCredits(ctx, t, pool, albumCreditQuery, albumID)
	if len(credits) != 2 {
		t.Fatalf("album credits = %#v, want the list replaced rather than added to", credits)
	}
	if credits[0].name != "Porter Robinson" || credits[1].name != "Ninajirachi" {
		t.Errorf("album credits = %#v", credits)
	}
}

// A response carrying no credit array is the provider not having said, which is
// silence — and silence is no reason to throw away the credit on record.
func TestARefreshWithoutACreditKeepsTheOneOnRecord(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	albumID := seedCreditedAlbum(ctx, t, pool)
	repository := NewRepository(pool)

	credit := []musicbrainz.Credit{{Name: "Porter Robinson", ArtistID: uuid.New()}}
	if err := repository.SaveReleaseCatalogue(ctx, albumID, creditedCatalogue(credit, credit)); err != nil {
		t.Fatalf("SaveReleaseCatalogue() error = %v", err)
	}
	if err := repository.SaveReleaseCatalogue(ctx, albumID, creditedCatalogue(nil, nil)); err != nil {
		t.Fatalf("uncredited SaveReleaseCatalogue() error = %v", err)
	}

	var albumCredits, trackCredits int
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM album_artist_credits WHERE album_id = $1),
			(SELECT count(*) FROM track_artist_credits
			    JOIN tracks ON tracks.id = track_artist_credits.track_id
			    WHERE tracks.album_id = $1)
	`, albumID).Scan(&albumCredits, &trackCredits); err != nil {
		t.Fatal(err)
	}
	if albumCredits != 1 || trackCredits != 1 {
		t.Errorf("credits held = album %d, track %d, want both kept", albumCredits, trackCredits)
	}
}

const (
	albumCreditQuery = `
		SELECT position, credited_name, musicbrainz_artist_id, join_phrase
		FROM album_artist_credits WHERE album_id = $1 ORDER BY position
	`
	trackCreditQuery = `
		SELECT position, credited_name, musicbrainz_artist_id, join_phrase
		FROM track_artist_credits WHERE track_id = $1 ORDER BY position
	`
)

// storedCredit is one credit row as the catalogue holds it. A credit naming
// nobody MusicBrainz holds reads back as uuid.Nil, the way the client hands one
// over.
type storedCredit struct {
	position   int
	name       string
	artistID   uuid.UUID
	joinPhrase string
}

func storedCredits(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool, query string, entityID uuid.UUID,
) []storedCredit {
	t.Helper()
	rows, err := pool.Query(ctx, query, entityID)
	if err != nil {
		t.Fatalf("read credits: %v", err)
	}
	defer rows.Close()
	credits := make([]storedCredit, 0)
	for rows.Next() {
		var credit storedCredit
		var artistID *uuid.UUID
		if err := rows.Scan(&credit.position, &credit.name, &artistID, &credit.joinPhrase); err != nil {
			t.Fatalf("scan credit: %v", err)
		}
		if artistID != nil {
			credit.artistID = *artistID
		}
		credits = append(credits, credit)
	}
	if rows.Err() != nil {
		t.Fatalf("read credits: %v", rows.Err())
	}
	return credits
}

// seedCreditedAlbum puts one album in the catalogue for a credit to hang off.
func seedCreditedAlbum(ctx context.Context, t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	var albumID uuid.UUID
	if err := pool.QueryRow(ctx, `
		WITH artist AS (
		    INSERT INTO artists (musicbrainz_id, name, sort_name)
		    VALUES (gen_random_uuid(), 'Porter Robinson', 'Robinson, Porter')
		    RETURNING id
		)
		INSERT INTO albums (artist_id, musicbrainz_release_group_id, title)
		SELECT id, gen_random_uuid(), 'Nurture' FROM artist
		RETURNING id
	`).Scan(&albumID); err != nil {
		t.Fatalf("seed album: %v", err)
	}
	return albumID
}

// creditedCatalogue is one release of one track, credited as the caller says.
func creditedCatalogue(edition, track []musicbrainz.Credit) musicbrainz.ReleaseCatalogue {
	return musicbrainz.ReleaseCatalogue{
		Edition: musicbrainz.ReleaseEdition{
			ID: creditedReleaseID, Title: "Nurture", Status: "Official", Country: "US",
			Date: "2021-04-23", MediaCount: 1, TrackCount: 1,
			SelectionReason: "Earliest official edition", Credits: edition,
		},
		Tracks: []musicbrainz.Track{{
			RecordingID: creditedRecordingID, Title: "Musician", DiscNumber: 1,
			TrackNumber: 1, DurationMS: intPointer(202000), Credits: track,
		}},
		Metadata: json.RawMessage(`{"id":"edition"}`),
	}
}

var (
	creditedReleaseID   = uuid.New()
	creditedRecordingID = uuid.New()
)

// import_download is the job that takes one finished Soulseek transfer into the
// library. It runs by itself, and it starts by claiming the download request it
// names: the row that records what was fetched, from whom, and how far the
// import of it has got.
//
// An import that stopped on a real fault spends its attempts, records the fault
// on the request, and pauses for review. A person then presses Retry, which
// gives the job its attempts back and runs it again — and by then there is
// nothing left to claim, because the request has already been decided about.
//
// The job must finish quietly in that case. Failing again would spend six more
// attempts on a request whose reason is already written, and the person would be
// shown a job whose only explanation is that a query returned no rows.
func TestRetryingAnImportOfARequestAlreadyDecidedAboutFinishesTheJob(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	artistID, albumID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx,
		"INSERT INTO artists (id, name, sort_name) VALUES ($1, 'Artist', 'Artist')", artistID,
	); err != nil {
		t.Fatalf("seed artist: %v", err)
	}
	if _, err := pool.Exec(ctx,
		"INSERT INTO albums (id, artist_id, title) VALUES ($1, $2, 'Album')", albumID, artistID,
	); err != nil {
		t.Fatalf("seed album: %v", err)
	}
	queries := db.New(pool)
	requestID, _, err := queries.CreateDownloadRequest(ctx, db.CreateDownloadRequestParams{
		AlbumID: albumID, Provider: "slskd", SourceUsername: "peer",
		SourceDirectory: "Music/Album", FileCount: 1, ExpectedTrackCount: 1,
		TotalSizeBytes: 3, Format: "flac", Score: 1,
		Files: []db.DownloadRequestFile{{Path: `Music\Album\01.flac`, Name: "01.flac", SizeBytes: 3}},
	})
	if err != nil {
		t.Fatalf("seed download request: %v", err)
	}
	if _, err := queries.StartDownloadRequest(ctx, requestID); err != nil {
		t.Fatalf("start download request: %v", err)
	}
	if _, err := queries.RecordTransferProgress(ctx, requestID, []db.TransferProgress{
		{Path: `Music\Album\01.flac`, State: "completed", SizeBytes: 3, TransferredBytes: 3},
	}); err != nil {
		t.Fatalf("finish transfer: %v", err)
	}
	if _, err := queries.ClaimDownloadImport(ctx, requestID); err != nil {
		t.Fatalf("claim import: %v", err)
	}
	const reason = "01.flac could not be matched to a track of this release"
	if err := queries.MarkDownloadImportNeedsReview(ctx, requestID, reason, nil); err != nil {
		t.Fatalf("pause import: %v", err)
	}

	payload, _ := json.Marshal(map[string]uuid.UUID{"requestId": requestID})
	if _, err := pool.Exec(ctx,
		"INSERT INTO jobs (kind, payload) VALUES ($1, $2)", ImportDownload, payload,
	); err != nil {
		t.Fatalf("queue import job: %v", err)
	}
	repository := NewRepository(pool)
	job, err := repository.Claim(ctx, Lane{})
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	importer := downloads.NewImporter(queries, t.TempDir(), t.TempDir(), zerolog.Nop())
	worker := NewWorker(repository, fakeProvider{}, zerolog.Nop()).WithDownloadImporter(importer)

	worker.process(ctx, job)

	var jobStatus, jobError string
	if err := pool.QueryRow(ctx, `
		SELECT status, coalesce(error_message, '') FROM jobs WHERE id = $1
	`, job.ID).Scan(&jobStatus, &jobError); err != nil {
		t.Fatalf("read the job back: %v", err)
	}
	if jobStatus != "completed" {
		t.Errorf("job status = %q with %q, want completed", jobStatus, jobError)
	}
	stored, err := queries.DownloadRequest(ctx, requestID)
	if err != nil {
		t.Fatalf("read the request back: %v", err)
	}
	if stored.ImportError.String != reason {
		t.Errorf("import_error = %q, want %q unchanged", stored.ImportError.String, reason)
	}
	if stored.ImportStatus != "needs_review" {
		t.Errorf("import_status = %q, want needs_review", stored.ImportStatus)
	}
}

// A release refresh records the genres, empty included, so an album the artist
// refresh cannot reach stops reading as never asked about.
//
// The artist refresh only writes the release groups MusicBrainz credits to that
// artist. A remix filed under somebody else's name is not among them, so before
// this its genres stayed NULL however often it was refreshed, and the genre
// backfill read that NULL as its artist still needing a refresh — every ten
// minutes, forever.
func TestAReleaseRefreshRecordsThatMusicBrainzHasNoGenres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	var albumID uuid.UUID
	if err := pool.QueryRow(ctx, `
		WITH artist AS (
		    INSERT INTO artists (musicbrainz_id, name, sort_name)
		    VALUES ($1, 'Skrillex', 'Skrillex')
		    RETURNING id
		)
		INSERT INTO albums (artist_id, musicbrainz_release_group_id, title)
		SELECT id, $2, 'Make It Bun Dem (Laudz Trap Remix)' FROM artist
		RETURNING id
	`, uuid.New(), uuid.New()).Scan(&albumID); err != nil {
		t.Fatal(err)
	}

	if err := NewRepository(pool).SaveReleaseCatalogue(ctx, albumID, musicbrainz.ReleaseCatalogue{
		Edition: musicbrainz.ReleaseEdition{
			ID: uuid.New(), Title: "Make It Bun Dem (Laudz Trap Remix)",
			SelectionReason: "Earliest official edition",
		},
		Genres: []string{},
	}); err != nil {
		t.Fatalf("SaveReleaseCatalogue() error = %v", err)
	}

	var genres []string
	if err := pool.QueryRow(ctx, `SELECT genres FROM albums WHERE id = $1`, albumID).Scan(&genres); err != nil {
		t.Fatalf("read album genres: %v", err)
	}
	if genres == nil {
		t.Fatal("genres = NULL, want an empty list: MusicBrainz was asked and holds none")
	}
	if len(genres) != 0 {
		t.Fatalf("genres = %v, want empty", genres)
	}
}

// A release group MusicBrainz could not be asked about leaves the column alone,
// so a failed lookup is never recorded as an answer.
func TestAReleaseRefreshLeavesGenresAloneWhenNobodyAsked(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	var albumID uuid.UUID
	if err := pool.QueryRow(ctx, `
		WITH artist AS (
		    INSERT INTO artists (musicbrainz_id, name, sort_name)
		    VALUES ($1, 'Talk Talk', 'Talk Talk')
		    RETURNING id
		)
		INSERT INTO albums (artist_id, musicbrainz_release_group_id, title, genres)
		SELECT id, $2, 'Laughing Stock', '{"post-rock"}' FROM artist
		RETURNING id
	`, uuid.New(), uuid.New()).Scan(&albumID); err != nil {
		t.Fatal(err)
	}

	if err := NewRepository(pool).SaveReleaseCatalogue(ctx, albumID, musicbrainz.ReleaseCatalogue{
		Edition: musicbrainz.ReleaseEdition{
			ID: uuid.New(), Title: "Laughing Stock",
			SelectionReason: "Earliest official edition",
		},
	}); err != nil {
		t.Fatalf("SaveReleaseCatalogue() error = %v", err)
	}

	var genres []string
	if err := pool.QueryRow(ctx, `SELECT genres FROM albums WHERE id = $1`, albumID).Scan(&genres); err != nil {
		t.Fatalf("read album genres: %v", err)
	}
	if len(genres) != 1 || genres[0] != "post-rock" {
		t.Fatalf("genres = %v, want the genres already on record", genres)
	}
}

// An artist refresh queues a release refresh only for what it changed.
//
// It used to queue one for every release group it saw, so refreshing an artist
// with 185 of them cost 185 jobs whether or not MusicBrainz had said anything
// new, and the general queue spent hours re-fetching release groups that had
// not moved.
func TestAnArtistRefreshDoesNotRequeueAnUnchangedRelease(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	repository := NewRepository(pool)

	musicBrainzID, releaseGroupID := uuid.New(), uuid.New()
	var artistID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO artists (musicbrainz_id, name, sort_name)
		VALUES ($1, 'Ninajirachi', 'Ninajirachi')
		RETURNING id
	`, musicBrainzID).Scan(&artistID); err != nil {
		t.Fatal(err)
	}

	catalogue := musicbrainz.ArtistCatalogue{
		Artist:   musicbrainz.Artist{ID: musicBrainzID, Name: "Ninajirachi", SortName: "Ninajirachi"},
		Metadata: json.RawMessage(`{"id":"` + musicBrainzID.String() + `"}`),
		ReleaseGroups: []musicbrainz.ReleaseGroup{{
			ID:               releaseGroupID,
			Title:            "girl EDM",
			FirstReleaseDate: "2024-11-15",
			PrimaryType:      "Album",
			Genres:           []string{"electronic"},
			Metadata:         json.RawMessage(`{"id":"` + releaseGroupID.String() + `"}`),
		}},
	}

	if err := repository.SaveArtistCatalogue(ctx, artistID, catalogue); err != nil {
		t.Fatalf("first SaveArtistCatalogue() error = %v", err)
	}
	// The first pass is a discovery: the album is new and has no tracks, so its
	// refresh is what fetches them.
	if got := countAlbumRefreshes(ctx, t, pool); got != 1 {
		t.Fatalf("after discovering the release, %d refreshes queued, want one", got)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO tracks (album_id, title, track_number)
		SELECT id, 'Rush Hour', 1 FROM albums WHERE musicbrainz_release_group_id = $1
	`, releaseGroupID); err != nil {
		t.Fatalf("seed the fetched track: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM jobs WHERE kind = 'refresh_album_metadata'`); err != nil {
		t.Fatalf("clear the first refresh: %v", err)
	}

	if err := repository.SaveArtistCatalogue(ctx, artistID, catalogue); err != nil {
		t.Fatalf("second SaveArtistCatalogue() error = %v", err)
	}
	if got := countAlbumRefreshes(ctx, t, pool); got != 0 {
		t.Fatalf("after an unchanged pass, %d refreshes queued, want none", got)
	}

	catalogue.ReleaseGroups[0].Title = "girl EDM (deluxe)"
	if err := repository.SaveArtistCatalogue(ctx, artistID, catalogue); err != nil {
		t.Fatalf("third SaveArtistCatalogue() error = %v", err)
	}
	if got := countAlbumRefreshes(ctx, t, pool); got != 1 {
		t.Fatalf("after a retitled release, %d refreshes queued, want one", got)
	}
}

func countAlbumRefreshes(ctx context.Context, t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM jobs WHERE kind = 'refresh_album_metadata'
	`).Scan(&count); err != nil {
		t.Fatalf("count queued release refreshes: %v", err)
	}
	return count
}

// The genre backfill reads NULL genres as "nobody has asked". A refusal that
// will be the same tomorrow is therefore recorded as an empty answer, or the
// backfill queues it again every day and counts the artist as waiting in
// between — which is what held the sweep at ten minutes for a placeholder row
// it could never answer (issue #494).
func TestMarkArtistGenresAskedStopsTheBackfillCountingTheArtist(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	artistID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO artists (id, musicbrainz_id, name, sort_name)
		VALUES ($1, $2, 'Various Artists', 'Various Artists')
	`, artistID, musicbrainz.VariousArtistsID); err != nil {
		t.Fatalf("seed artist: %v", err)
	}
	repository := NewRepository(pool)

	if err := repository.MarkArtistGenresAsked(ctx, artistID); err != nil {
		t.Fatalf("MarkArtistGenresAsked() error = %v", err)
	}

	result, err := repository.BackfillGenres(ctx, 25)
	if err != nil {
		t.Fatalf("BackfillGenres() error = %v", err)
	}
	if result.WaitingCount != 0 {
		t.Fatalf("waiting = %d, want nobody left to ask about", result.WaitingCount)
	}
}

// A recorded answer is not overwritten by a later refusal, so an artist whose
// genres are known keeps them.
func TestMarkArtistGenresAskedLeavesGenresAlreadyKnown(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	artistID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO artists (id, musicbrainz_id, name, sort_name, genres)
		VALUES ($1, $2, 'Talk Talk', 'Talk Talk', '{post-rock}')
	`, artistID, uuid.New()); err != nil {
		t.Fatalf("seed artist: %v", err)
	}
	repository := NewRepository(pool)

	if err := repository.MarkArtistGenresAsked(ctx, artistID); err != nil {
		t.Fatalf("MarkArtistGenresAsked() error = %v", err)
	}

	var genres []string
	if err := pool.QueryRow(ctx, `SELECT genres FROM artists WHERE id = $1`, artistID).
		Scan(&genres); err != nil {
		t.Fatalf("read genres: %v", err)
	}
	if len(genres) != 1 || genres[0] != "post-rock" {
		t.Fatalf("genres = %v, want the answer already recorded", genres)
	}
}

// A release group with no edition Schall can read is answered the same way.
func TestMarkAlbumGenresAskedStopsTheBackfillCountingTheRelease(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	artistID, albumID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO artists (id, musicbrainz_id, name, sort_name, genres)
		VALUES ($1, $2, 'Talk Talk', 'Talk Talk', '{post-rock}')
	`, artistID, uuid.New()); err != nil {
		t.Fatalf("seed artist: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO albums (id, artist_id, musicbrainz_release_group_id, title, album_type)
		VALUES ($1, $2, $3, 'Spirit of Eden', 'album')
	`, albumID, artistID, uuid.New()); err != nil {
		t.Fatalf("seed album: %v", err)
	}
	repository := NewRepository(pool)

	if err := repository.MarkAlbumGenresAsked(ctx, albumID); err != nil {
		t.Fatalf("MarkAlbumGenresAsked() error = %v", err)
	}

	result, err := repository.BackfillGenres(ctx, 25)
	if err != nil {
		t.Fatalf("BackfillGenres() error = %v", err)
	}
	if result.WaitingCount != 0 {
		t.Fatalf("waiting = %d, want nobody left to ask about", result.WaitingCount)
	}
}
