package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/musicbrainz"
)

type Repository struct {
	pool    *pgxpool.Pool
	matcher AlbumMatcher
}

type AlbumMatcher interface {
	ReconcileAlbum(context.Context, uuid.UUID) error
}

func NewRepository(pool *pgxpool.Pool, matchers ...AlbumMatcher) *Repository {
	repository := &Repository{pool: pool}
	if len(matchers) > 0 {
		repository.matcher = matchers[0]
	}
	return repository
}

// RecoverInterrupted puts back the jobs a shutdown left running. The statement
// lives with the retry that shares it, so there is one definition of how a job
// goes back on the queue — a job an operator retries and a job a restart
// recovered arrive back in exactly the same state.
func (repository *Repository) RecoverInterrupted(ctx context.Context) error {
	return db.New(repository.pool).RequeueInterruptedJobs(ctx)
}

// ReapStalled puts back the jobs that are running with nobody running them. It
// is RecoverInterrupted bounded by how long their ownership lease has gone
// without renewal, which makes it safe to run while other workers are working,
// and it shares the same statement as both.
func (repository *Repository) ReapStalled(ctx context.Context, staleFor time.Duration) (int64, error) {
	return db.New(repository.pool).RequeueStalledJobs(ctx, staleFor)
}

// Claim takes the next job the lane will have. SKIP LOCKED is what makes two
// lanes safe against each other: neither can see a row the other is taking, so
// the same job is never claimed twice however many workers are asking.
//
// The lane's First kinds are taken before anything else queued, in the order
// the lane lists them; the rest of the queue keeps its own order behind them.
// That is what stops a button a person pressed from waiting behind hundreds of
// rows of background work, and what lets the judging lane answer a waiting want
// before it decodes another library file.
//
// An empty array is "no restriction" rather than "nothing", which is why every
// parameter is coerced from nil — a NULL array would filter every row out and
// the lane would quietly claim nothing at all.
func (repository *Repository) Claim(ctx context.Context, lane Lane) (Job, error) {
	only, except, first := lane.Only, lane.Except, lane.First
	if only == nil {
		only = []string{}
	}
	if except == nil {
		except = []string{}
	}
	if first == nil {
		first = []string{}
	}
	row := repository.pool.QueryRow(ctx, `
		WITH next_job AS (
			SELECT id
			FROM jobs
			WHERE status = 'queued' AND run_after <= now()
			  AND (cardinality($1::text[]) = 0 OR kind = ANY($1::text[]))
			  AND NOT (kind = ANY($2::text[]))
			ORDER BY coalesce(array_position($3::text[], kind), cardinality($3::text[]) + 1),
			         run_after, created_at
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		UPDATE jobs
		SET status = 'running',
		    attempts = attempts + 1,
		    started_at = now(),
		    error_message = NULL,
		    updated_at = now()
		FROM next_job
		WHERE jobs.id = next_job.id
		RETURNING jobs.id, jobs.kind, jobs.payload, jobs.attempts, jobs.max_attempts, jobs.started_at
	`, only, except, first)

	var job Job
	if err := row.Scan(
		&job.ID, &job.Kind, &job.Payload, &job.Attempts, &job.MaxAttempts, &job.StartedAt,
	); err != nil {
		return Job{}, err
	}
	return job, nil
}

// Heartbeat renews the lease on the exact claim the worker is still running.
// The attempt and claim timestamp together are its generation: a worker from an
// older claim must not renew or settle the row after a reaper has put it back
// and another worker has taken it.
func (repository *Repository) Heartbeat(ctx context.Context, job Job) (bool, error) {
	tag, err := repository.pool.Exec(ctx, `
		UPDATE jobs
		SET updated_at = now()
		WHERE id = $1 AND status = 'running' AND attempts = $2 AND started_at = $3
	`, job.ID, job.Attempts, job.StartedAt)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// SpentJobs lists the jobs that failed for good before the given moment and
// have not already been given a free attempt.
func (repository *Repository) SpentJobs(
	ctx context.Context, failedBefore time.Time, limit int32,
) ([]SpentJob, error) {
	rows, err := db.New(repository.pool).SpentJobs(ctx, failedBefore, limit)
	if err != nil {
		return nil, err
	}
	spent := make([]SpentJob, 0, len(rows))
	for _, row := range rows {
		spent = append(spent, SpentJob{
			ID:    row.ID,
			Kind:  row.Kind,
			Error: row.ErrorMessage.String,
		})
	}
	return spent, nil
}

// ReviveSpentJobs puts those jobs back for one more attempt each, and answers
// how many went back and how many were left where they were because the same
// work is already queued or running. The statement lives with the other ways a
// job goes back on the queue, so there is one definition of each of them.
func (repository *Repository) ReviveSpentJobs(
	ctx context.Context, jobIDs []uuid.UUID,
) (int64, int64, error) {
	return db.New(repository.pool).ReviveSpentJobs(ctx, jobIDs)
}

// ForgetSpentJobs removes the failures older than the window they are kept for.
func (repository *Repository) ForgetSpentJobs(ctx context.Context, failedBefore time.Time) (int64, error) {
	return db.New(repository.pool).ForgetSpentJobs(ctx, failedBefore)
}

func (repository *Repository) ArtistMusicBrainzID(ctx context.Context, artistID uuid.UUID) (uuid.UUID, error) {
	var musicBrainzID *uuid.UUID
	err := repository.pool.QueryRow(ctx, `
		SELECT musicbrainz_id
		FROM artists
		WHERE id = $1
	`, artistID).Scan(&musicBrainzID)
	if err != nil {
		return uuid.Nil, err
	}
	if musicBrainzID == nil {
		return uuid.Nil, errors.New("artist has no MusicBrainz identity")
	}
	return *musicBrainzID, nil
}

func (repository *Repository) AlbumMusicBrainzIDs(ctx context.Context, albumID uuid.UUID) (uuid.UUID, uuid.UUID, error) {
	var releaseGroupID *uuid.UUID
	var selectedReleaseID *uuid.UUID
	err := repository.pool.QueryRow(ctx, `
		SELECT albums.musicbrainz_release_group_id,
		       coalesce(albums.preferred_musicbrainz_release_id, release_editions.musicbrainz_release_id)
		FROM albums
		LEFT JOIN release_editions ON release_editions.album_id = albums.id
		WHERE albums.id = $1
	`, albumID).Scan(&releaseGroupID, &selectedReleaseID)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	if releaseGroupID == nil {
		return uuid.Nil, uuid.Nil, errors.New("album has no MusicBrainz release group identity")
	}
	if selectedReleaseID == nil {
		return *releaseGroupID, uuid.Nil, nil
	}
	return *releaseGroupID, *selectedReleaseID, nil
}

func (repository *Repository) SaveArtistCatalogue(ctx context.Context, artistID uuid.UUID, catalogue musicbrainz.ArtistCatalogue) error {
	transaction, err := repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	tag, err := transaction.Exec(ctx, `
		UPDATE artists
		SET name = $2,
		    sort_name = $3,
		    genres = $5,
		    last_refreshed_at = now(),
		    updated_at = now()
		WHERE id = $1 AND musicbrainz_id = $4
	`, artistID, catalogue.Artist.Name, catalogue.Artist.SortName, catalogue.Artist.ID,
		// The list is written even when it is empty, because empty is an answer:
		// MusicBrainz was asked and holds no votes for this artist. Only NULL
		// means nobody has asked, and that is what the backfill looks for.
		catalogue.Artist.Genres)
	if err != nil {
		return fmt.Errorf("update artist: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return errors.New("artist identity changed during refresh")
	}

	if err := upsertMetadataSource(ctx, transaction, "artist", artistID, catalogue.Artist.ID.String(), catalogue.Metadata); err != nil {
		return err
	}

	for _, releaseGroup := range catalogue.ReleaseGroups {
		var albumID uuid.UUID
		var worthRefreshing bool
		// The refresh is queued only for a release group this pass actually
		// changed, or one that has no tracks yet. An artist refresh used to
		// queue one for every release group it saw, so refreshing Skrillex cost
		// 185 jobs whether or not MusicBrainz had said anything new, and the
		// general queue spent hours re-fetching release groups that had not
		// moved. A release group whose title, date, type and genres are all
		// unchanged has nothing for the release refresh to find.
		err := transaction.QueryRow(ctx, `
			WITH upserted AS (
			    INSERT INTO albums (
			        artist_id, musicbrainz_release_group_id, title, release_date, album_type,
			        secondary_types, genres
			    )
			    VALUES ($1, $2, $3, $4, $5, $6, $7)
			    ON CONFLICT (musicbrainz_release_group_id) DO UPDATE
			    SET artist_id = EXCLUDED.artist_id,
			        title = EXCLUDED.title,
			        release_date = EXCLUDED.release_date,
			        album_type = EXCLUDED.album_type,
			        secondary_types = EXCLUDED.secondary_types,
			        genres = EXCLUDED.genres,
			        updated_at = now()
			    WHERE (
			        albums.artist_id, albums.title, albums.release_date, albums.album_type,
			        albums.secondary_types, albums.genres
			    ) IS DISTINCT FROM (
			        EXCLUDED.artist_id, EXCLUDED.title, EXCLUDED.release_date, EXCLUDED.album_type,
			        EXCLUDED.secondary_types, EXCLUDED.genres
			    )
			    RETURNING id
			)
			SELECT id, true FROM upserted
			UNION ALL
			SELECT albums.id, NOT EXISTS (SELECT 1 FROM tracks WHERE tracks.album_id = albums.id)
			FROM albums
			WHERE albums.musicbrainz_release_group_id = $2
			  AND NOT EXISTS (SELECT 1 FROM upserted)
			LIMIT 1
		`, artistID, releaseGroup.ID, releaseGroup.Title, exactReleaseDate(releaseGroup.FirstReleaseDate),
			albumType(releaseGroup.PrimaryType), secondaryTypes(releaseGroup.SecondaryTypes),
			releaseGroup.Genres).Scan(&albumID, &worthRefreshing)
		if err != nil {
			return fmt.Errorf("upsert release group %s: %w", releaseGroup.ID, err)
		}
		if err := upsertMetadataSource(ctx, transaction, "album", albumID, releaseGroup.ID.String(), releaseGroup.Metadata); err != nil {
			return err
		}
		if !worthRefreshing {
			continue
		}
		if _, err := transaction.Exec(ctx, `
			INSERT INTO jobs (kind, payload)
			VALUES ('refresh_album_metadata', jsonb_build_object('albumId', $1::text))
			ON CONFLICT (kind, (payload->>'albumId'))
			    WHERE kind = 'refresh_album_metadata' AND status IN ('queued', 'running')
			DO NOTHING
		`, albumID); err != nil {
			return fmt.Errorf("queue release refresh %s: %w", releaseGroup.ID, err)
		}
	}

	if err := wakeCoverArtSweep(ctx, transaction); err != nil {
		return err
	}

	return transaction.Commit(ctx)
}

// wakeCoverArtSweep asks for the picture cache to be filled now, because a
// release has just entered the catalogue.
//
// The sweep is the only thing that fills release_cover_art, and on its own it
// runs every six hours: a release ingested a minute after a pass went by is a
// grey rectangle for the rest of the evening, which is what #290 measured — 109
// of 824 albums with no row at all, every one of them added that day. Waking the
// sweep here is the same move the library scan makes on the acquisition sweeper,
// and it leaves the six-hourly pass as what its comment says it is: the floor
// under the mechanism rather than the mechanism.
//
// It runs in the ingest's own transaction, so a release is never in the
// catalogue with nothing having asked about it. The queue method moves an
// existing request earlier rather than adding a second one, so ingesting a
// hundred releases at once still asks once — and a wake that finds nothing
// missing costs one query and puts itself back six hours out. That is why this
// is unconditional rather than checking first whether anything is uncovered:
// asking that question here would be a second copy of the question the sweep
// itself asks, in a second place, free to drift from it.
//
// The artists come along with it. sweep_cover_art pictures the people and the
// records in one pass, so the artist ingested alongside a release is asked about
// at the same moment rather than at the next idle cycle.
func wakeCoverArtSweep(ctx context.Context, transaction pgx.Tx) error {
	if err := db.New(transaction).QueueCoverArtSweep(ctx, time.Now()); err != nil {
		return fmt.Errorf("wake the cover art sweep: %w", err)
	}
	return nil
}

func (repository *Repository) SaveReleaseCatalogue(ctx context.Context, albumID uuid.UUID, catalogue musicbrainz.ReleaseCatalogue) error {
	transaction, err := repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	var editionID uuid.UUID
	var preferredReleaseID uuid.NullUUID
	if err := transaction.QueryRow(ctx, `
		SELECT preferred_musicbrainz_release_id
		FROM albums
		WHERE id = $1
	`, albumID).Scan(&preferredReleaseID); err != nil {
		return fmt.Errorf("load representative preference: %w", err)
	}
	if preferredReleaseID.Valid && preferredReleaseID.UUID != catalogue.Edition.ID {
		return errors.New("representative edition changed during refresh")
	}
	selectedManually := preferredReleaseID.Valid
	selectionReason := catalogue.Edition.SelectionReason
	if selectedManually {
		selectionReason = "Selected manually"
	}

	// The genres are written here as well as on the artist catalogue, because a
	// release group credited to somebody else never appears in its Schall
	// artist's catalogue and nothing else would fill them. An empty list is
	// written, because empty is an answer; only NULL means nobody has asked, and
	// that is what the genre backfill looks for.
	if catalogue.Genres != nil {
		if _, err := transaction.Exec(ctx, `
			UPDATE albums SET genres = $2, updated_at = now() WHERE id = $1
		`, albumID, catalogue.Genres); err != nil {
			return fmt.Errorf("update album genres: %w", err)
		}
	}
	err = transaction.QueryRow(ctx, `
		INSERT INTO release_editions (
			album_id, musicbrainz_release_id, title, status, country, barcode,
			release_date, media_count, track_count, selected_automatically, selection_reason
		)
		VALUES ($1, $2, $3, nullif($4, ''), nullif($5, ''), nullif($6, ''),
		        nullif($7, ''), $8, $9, NOT $10, $11)
		ON CONFLICT (album_id) DO UPDATE
		SET musicbrainz_release_id = EXCLUDED.musicbrainz_release_id,
		    title = EXCLUDED.title,
		    status = EXCLUDED.status,
		    country = EXCLUDED.country,
		    barcode = EXCLUDED.barcode,
		    release_date = EXCLUDED.release_date,
		    media_count = EXCLUDED.media_count,
		    track_count = EXCLUDED.track_count,
		    selected_automatically = EXCLUDED.selected_automatically,
		    selection_reason = CASE
		        WHEN release_editions.musicbrainz_release_id = EXCLUDED.musicbrainz_release_id
		        THEN release_editions.selection_reason
		        ELSE EXCLUDED.selection_reason
		    END,
		    updated_at = now()
		RETURNING id
	`, albumID, catalogue.Edition.ID, catalogue.Edition.Title, catalogue.Edition.Status,
		catalogue.Edition.Country, catalogue.Edition.Barcode, catalogue.Edition.Date,
		catalogue.Edition.MediaCount, catalogue.Edition.TrackCount, selectedManually,
		selectionReason).Scan(&editionID)
	if err != nil {
		return fmt.Errorf("upsert representative release: %w", err)
	}
	if err := upsertMetadataSource(ctx, transaction, "release_edition", editionID, catalogue.Edition.ID.String(), catalogue.Metadata); err != nil {
		return err
	}
	if err := replaceCredits(
		ctx, transaction, clearAlbumCredits, writeAlbumCredit, albumID, catalogue.Edition.Credits,
	); err != nil {
		return fmt.Errorf("write album credit %s: %w", albumID, err)
	}

	trackIDs := make([]uuid.UUID, 0, len(catalogue.Tracks))
	for _, track := range catalogue.Tracks {
		var isrc any
		if track.ISRC != "" {
			isrc = track.ISRC
		}
		var trackID uuid.UUID
		if err := transaction.QueryRow(ctx, `
			INSERT INTO tracks (
				album_id, musicbrainz_recording_id, title, disc_number,
				track_number, duration_ms, isrc
			)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (album_id, disc_number, track_number) DO UPDATE
			SET musicbrainz_recording_id = EXCLUDED.musicbrainz_recording_id,
			    title = EXCLUDED.title,
			    duration_ms = EXCLUDED.duration_ms,
			    isrc = EXCLUDED.isrc,
			    updated_at = now()
			RETURNING id
		`, albumID, track.RecordingID, track.Title, track.DiscNumber,
			track.TrackNumber, track.DurationMS, isrc).Scan(&trackID); err != nil {
			return fmt.Errorf("insert track %d-%d: %w", track.DiscNumber, track.TrackNumber, err)
		}
		if err := replaceCredits(
			ctx, transaction, clearTrackCredits, writeTrackCredit, trackID, track.Credits,
		); err != nil {
			return fmt.Errorf("write credit for track %d-%d: %w", track.DiscNumber, track.TrackNumber, err)
		}
		trackIDs = append(trackIDs, trackID)
	}
	if len(trackIDs) == 0 {
		trackIDs = append(trackIDs, uuid.Nil)
	}
	if _, err := transaction.Exec(ctx, `
		DELETE FROM tracks
		WHERE album_id = $1
		  AND NOT (id = ANY($2::uuid[]))
		  AND NOT EXISTS (
		      SELECT 1 FROM track_mappings WHERE track_mappings.track_id = tracks.id
		  )
	`, albumID, trackIDs); err != nil {
		return fmt.Errorf("remove stale tracks: %w", err)
	}
	if err := transaction.Commit(ctx); err != nil {
		return err
	}
	if repository.matcher != nil {
		if err := repository.matcher.ReconcileAlbum(ctx, albumID); err != nil {
			return fmt.Errorf("reconcile album matches: %w", err)
		}
	}
	return nil
}

// The four statements a credit list is written with. A credit belongs to one
// album or to one track and is written the same way for both, so the pair of
// tables differ here and nowhere else.
const (
	clearAlbumCredits = `DELETE FROM album_artist_credits WHERE album_id = $1`
	writeAlbumCredit  = `
		INSERT INTO album_artist_credits (
			album_id, position, credited_name, musicbrainz_artist_id, join_phrase
		)
		VALUES ($1, $2, $3, $4, $5)
	`
	clearTrackCredits = `DELETE FROM track_artist_credits WHERE track_id = $1`
	writeTrackCredit  = `
		INSERT INTO track_artist_credits (
			track_id, position, credited_name, musicbrainz_artist_id, join_phrase
		)
		VALUES ($1, $2, $3, $4, $5)
	`
)

// creditRow is one credit as the catalogue stores it: the place in the list is
// part of the row, because a tag write reads the names and the identifiers off
// against each other by position.
type creditRow struct {
	position   int
	name       string
	artistID   any
	joinPhrase string
}

// creditRows numbers a credit list from 1 in the order the provider sent it.
//
// Nothing is dropped, reordered or tidied on the way in. An entry with no
// artist keeps its place with a NULL identifier — dropping it would hand the
// next artist's identifier to this one's name — and an entry with no printed
// name keeps its place for the same reason. What MusicBrainz sent is what the
// catalogue holds, so that joining the list back up still reproduces the credit
// as the provider printed it.
func creditRows(credits []musicbrainz.Credit) []creditRow {
	rows := make([]creditRow, 0, len(credits))
	for index, credit := range credits {
		row := creditRow{position: index + 1, name: credit.Name, joinPhrase: credit.JoinPhrase}
		if credit.ArtistID != uuid.Nil {
			row.artistID = credit.ArtistID
		}
		rows = append(rows, row)
	}
	return rows
}

// replaceCredits writes one credit list over whatever was held for that album
// or track before. A refresh replaces the credit wholesale rather than adding
// to it: an artist MusicBrainz has since dropped from the credit would
// otherwise stay in the list forever, standing at a position that is now
// somebody else's.
//
// An empty list writes nothing and clears nothing. A response that carried no
// credit array is the provider not having said, which is silence — and silence
// is no reason to throw away a credit the catalogue already has on record.
func replaceCredits(
	ctx context.Context, transaction pgx.Tx, clearStatement, writeStatement string,
	entityID uuid.UUID, credits []musicbrainz.Credit,
) error {
	if len(credits) == 0 {
		return nil
	}
	if _, err := transaction.Exec(ctx, clearStatement, entityID); err != nil {
		return fmt.Errorf("clear held credit: %w", err)
	}
	for _, row := range creditRows(credits) {
		if _, err := transaction.Exec(
			ctx, writeStatement, entityID, row.position, row.name, row.artistID, row.joinPhrase,
		); err != nil {
			return fmt.Errorf("insert credit at position %d: %w", row.position, err)
		}
	}
	return nil
}

func upsertMetadataSource(ctx context.Context, transaction pgx.Tx, entityType string, entityID uuid.UUID, externalID string, metadata json.RawMessage) error {
	if len(metadata) == 0 {
		metadata = json.RawMessage(`{}`)
	}
	_, err := transaction.Exec(ctx, `
		INSERT INTO metadata_sources (entity_type, entity_id, provider, external_id, metadata)
		VALUES ($1, $2, 'musicbrainz', $3, $4)
		ON CONFLICT (entity_type, entity_id, provider) DO UPDATE
		SET external_id = EXCLUDED.external_id,
		    metadata = EXCLUDED.metadata,
		    fetched_at = now(),
		    updated_at = now()
	`, entityType, entityID, externalID, metadata)
	if err != nil {
		return fmt.Errorf("upsert %s metadata source: %w", entityType, err)
	}
	return nil
}

// QueueDownloadPoll reschedules the transfer poller. The query lives with the
// other download queries, so there is one definition of how the poller is
// queued.
func (repository *Repository) QueueDownloadPoll(ctx context.Context, runAfter time.Time) error {
	return db.New(repository.pool).QueueDownloadPoll(ctx, runAfter)
}

// QueuePeerChallenges reschedules the pass that reads the private chat of the
// peers holding transfers.
func (repository *Repository) QueuePeerChallenges(ctx context.Context, runAfter time.Time) error {
	return db.New(repository.pool).QueuePeerChallenges(ctx, runAfter)
}

// QueueAcquisitionSweep reschedules the sweep over wanted recordings. The query
// lives with the other acquisition queries, so there is one definition of how
// the sweeper is queued.
func (repository *Repository) QueueAcquisitionSweep(ctx context.Context, runAfter time.Time) error {
	return db.New(repository.pool).QueueAcquisitionSweep(ctx, runAfter)
}

func (repository *Repository) QueueCoverArtSweep(ctx context.Context, runAfter time.Time) error {
	return db.New(repository.pool).QueueCoverArtSweep(ctx, runAfter)
}

func (repository *Repository) QueueLoudnessSweep(ctx context.Context, runAfter time.Time) error {
	return db.New(repository.pool).QueueLoudnessSweep(ctx, runAfter)
}

func (repository *Repository) QueueFollowFeedSweep(ctx context.Context, runAfter time.Time) error {
	return db.New(repository.pool).QueueFollowFeedSweep(ctx, runAfter)
}

func (repository *Repository) QueueRecommendationSweep(ctx context.Context, runAfter time.Time) error {
	return db.New(repository.pool).QueueRecommendationSweep(ctx, runAfter)
}

func (repository *Repository) QueueWeeklyPlaylistRefresh(ctx context.Context, runAfter time.Time) error {
	return db.New(repository.pool).QueueWeeklyPlaylistRefresh(ctx, runAfter)
}

func (repository *Repository) QueueNewReleasesPlaylistRefresh(ctx context.Context, runAfter time.Time) error {
	return db.New(repository.pool).QueueNewReleasesPlaylistRefresh(ctx, runAfter)
}

func (repository *Repository) QueueRecommendationSweepContinuation(
	ctx context.Context, runAfter time.Time, payload json.RawMessage,
) error {
	return db.New(repository.pool).QueueRecommendationSweepContinuation(ctx, runAfter, payload)
}

// QueueListensSync asks for one listening-history sync at the given time.
func (repository *Repository) QueueListensSync(ctx context.Context, runAfter time.Time) error {
	return db.New(repository.pool).QueueListensSync(ctx, runAfter)
}

func (repository *Repository) QueueLyricsSweep(
	ctx context.Context, runAfter time.Time, payload json.RawMessage,
) error {
	return db.New(repository.pool).QueueLyricsSweep(ctx, runAfter, payload)
}

func (repository *Repository) JobPayload(ctx context.Context, jobID uuid.UUID) ([]byte, error) {
	return db.New(repository.pool).JobPayload(ctx, jobID)
}

func (repository *Repository) QueuePreviewAnchorSweep(ctx context.Context, runAfter time.Time) error {
	return db.New(repository.pool).QueuePreviewAnchorSweep(ctx, runAfter)
}

func (repository *Repository) QueueWantCopyJudging(
	ctx context.Context, targetID uuid.UUID, runAfter time.Time,
) error {
	return db.New(repository.pool).QueueWantCopyJudging(ctx, targetID, runAfter)
}

func (repository *Repository) QueueGenreSweep(ctx context.Context, runAfter time.Time) error {
	return db.New(repository.pool).QueueGenreSweep(ctx, runAfter)
}

// MarkArtistGenresAsked records that MusicBrainz was asked about this artist
// and had no genres to give.
//
// An empty list is the answer; only NULL means nobody has asked, and that is
// what BackfillGenres looks for. It is written only over a NULL, so an answer
// already recorded is left alone.
func (repository *Repository) MarkArtistGenresAsked(ctx context.Context, artistID uuid.UUID) error {
	if _, err := repository.pool.Exec(ctx, `
		UPDATE artists SET genres = '{}', updated_at = now()
		WHERE id = $1 AND genres IS NULL
	`, artistID); err != nil {
		return fmt.Errorf("mark artist genres asked %s: %w", artistID, err)
	}
	return nil
}

// MarkAlbumGenresAsked is MarkArtistGenresAsked for a release group.
func (repository *Repository) MarkAlbumGenresAsked(ctx context.Context, albumID uuid.UUID) error {
	if _, err := repository.pool.Exec(ctx, `
		UPDATE albums SET genres = '{}', updated_at = now()
		WHERE id = $1 AND genres IS NULL
	`, albumID); err != nil {
		return fmt.Errorf("mark album genres asked %s: %w", albumID, err)
	}
	return nil
}

// BackfillGenres queues an artist refresh for the artists whose genres nobody
// has asked MusicBrainz for, and reports how many were waiting and how many
// were queued.
func (repository *Repository) BackfillGenres(
	ctx context.Context, batch int32,
) (db.BackfillGenresRow, error) {
	return db.New(repository.pool).BackfillGenres(ctx, batch)
}

// QueueCopyFingerprintSweep asks for a pass over the held copies with no
// fingerprint on record. A pass already on the books keeps the earlier of the
// two times.
func (repository *Repository) QueueCopyFingerprintSweep(
	ctx context.Context, runAfter time.Time,
) error {
	return db.New(repository.pool).QueueCopyFingerprintSweep(ctx, runAfter)
}

func (repository *Repository) QueueSpectrumSweep(ctx context.Context, runAfter time.Time) error {
	return db.New(repository.pool).QueueSpectrumSweep(ctx, runAfter)
}

func (repository *Repository) QueueUpgradeSweep(
	ctx context.Context, runAfter time.Time, payload json.RawMessage,
) error {
	return db.New(repository.pool).QueueUpgradeSweep(ctx, runAfter, payload)
}

func (repository *Repository) QueueNavidromePlaylistSweep(ctx context.Context, runAfter time.Time) error {
	return db.New(repository.pool).QueueNavidromePlaylistSweep(ctx, runAfter)
}

func (repository *Repository) Complete(ctx context.Context, job Job) error {
	return repository.setFinished(ctx, job, "completed", "")
}

// CompleteLibraryWitnessSweep finishes a witness sweep and queues a successor
// when a scan woke it while it was running.
func (repository *Repository) CompleteLibraryWitnessSweep(ctx context.Context, job Job) (bool, error) {
	transaction, err := repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, err
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	var payload []byte
	err = transaction.QueryRow(ctx, "UPDATE jobs\n"+
		"SET status = $q$completed$q$,\n"+
		"    completed_at = now(),\n"+
		"    error_message = NULL,\n"+
		"    updated_at = now()\n"+
		"WHERE id = $1 AND status = $q$running$q$ AND attempts = $2 AND started_at = $3\n"+
		"RETURNING payload\n", job.ID, job.Attempts, job.StartedAt).Scan(&payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, errors.New("running job was not found")
	}
	if err != nil {
		return false, err
	}

	var marker map[string]bool
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &marker); err != nil {
			return false, fmt.Errorf("read library witness sweep payload: %w", err)
		}
	}
	if marker["woken_while_running"] {
		if err := db.New(transaction).QueueLibraryWitnessFingerprintSweep(ctx, time.Now()); err != nil {
			return false, fmt.Errorf("queue a sweep for the wake this pass received: %w", err)
		}
	}
	if err := transaction.Commit(ctx); err != nil {
		return false, err
	}
	return marker["woken_while_running"], nil
}

// CompleteAcquisitionSweep finishes a sweep job the way Complete does, and
// queues another sweep if something tried to wake this one while it was still
// running. It reports whether it did, for the caller to log.
//
// Such a wake cannot queue a second sweep itself — the partial index over
// ('queued', 'running') allows only one live sweeper — so QueueAcquisitionSweep
// leaves a mark on this row's payload instead of being silently dropped. This
// reads the mark and retires the row in the same statement, and queues the
// follow-up sweep in the same transaction, which closes the race a separate
// read would leave open: a wake landing between a read and this update would
// be missed by the read and then overwritten by the update. A wake landing
// after this transaction commits finds no row in ('queued', 'running') to
// conflict with, and queues a fresh one itself.
func (repository *Repository) CompleteAcquisitionSweep(ctx context.Context, job Job) (bool, error) {
	transaction, err := repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, err
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	var payload []byte
	err = transaction.QueryRow(ctx, `
		UPDATE jobs
		SET status = 'completed',
		    completed_at = now(),
		    error_message = NULL,
		    updated_at = now()
		WHERE id = $1 AND status = 'running' AND attempts = $2 AND started_at = $3
		RETURNING payload
	`, job.ID, job.Attempts, job.StartedAt).Scan(&payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, errors.New("running job was not found")
	}
	if err != nil {
		return false, err
	}

	var marker struct {
		WokenWhileRunning bool `json:"woken_while_running"`
	}
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &marker); err != nil {
			return false, fmt.Errorf("read acquisition sweep payload: %w", err)
		}
	}
	if marker.WokenWhileRunning {
		if err := db.New(transaction).QueueAcquisitionSweep(ctx, time.Now()); err != nil {
			return false, fmt.Errorf("queue a sweep for the wake this pass received: %w", err)
		}
	}
	if err := transaction.Commit(ctx); err != nil {
		return false, err
	}
	return marker.WokenWhileRunning, nil
}

func (repository *Repository) Retry(
	ctx context.Context, job Job, message string, runAfter time.Time,
) error {
	tag, err := repository.pool.Exec(ctx, `
		UPDATE jobs
		SET status = 'queued',
		    run_after = $4,
		    started_at = NULL,
		    error_message = $5,
		    updated_at = now()
		WHERE id = $1 AND status = 'running' AND attempts = $2 AND started_at = $3
	`, job.ID, job.Attempts, job.StartedAt, runAfter, message)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("running job was not found")
	}
	return nil
}

func (repository *Repository) Fail(ctx context.Context, job Job, message string) error {
	return repository.setFinished(ctx, job, "failed", message)
}

// Cancel ends a running job for a reason that is not its own: the work it
// belongs to was called off while it held it. Kept apart from Fail because the
// two read differently — a cancelled job was stopped, a failed one was tried.
func (repository *Repository) Cancel(ctx context.Context, job Job, message string) error {
	return repository.setFinished(ctx, job, "cancelled", message)
}

// SourceSearchRunCancelled reports whether a bulk source search was stopped.
// The query lives with the other source-search queries, so there is one
// definition of what a cancelled run is.
func (repository *Repository) SourceSearchRunCancelled(
	ctx context.Context, runID uuid.UUID,
) (bool, error) {
	return db.New(repository.pool).SourceSearchRunCancelled(ctx, runID)
}

func (repository *Repository) setFinished(
	ctx context.Context, job Job, status string, message string,
) error {
	var errorMessage any
	if message != "" {
		errorMessage = message
	}
	tag, err := repository.pool.Exec(ctx, `
		UPDATE jobs
		SET status = $4,
		    completed_at = now(),
		    error_message = $5,
		    updated_at = now()
		WHERE id = $1 AND status = 'running' AND attempts = $2 AND started_at = $3
	`, job.ID, job.Attempts, job.StartedAt, status, errorMessage)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("running job was not found")
	}
	return nil
}

func exactReleaseDate(value string) *time.Time {
	parsed, err := time.Parse("2006-01-02", strings.TrimSpace(value))
	if err != nil {
		return nil
	}
	return &parsed
}

func albumType(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "album"
	}
	return value
}

// secondaryTypes normalises MusicBrainz's secondary types the same way
// albumType does the primary one, so "Live" and "DJ-mix" compare equal to the
// lowercase sets the monitor level filters by. Never nil: an absent list is
// "none known", stored as the empty array the column defaults to.
func secondaryTypes(values []string) []string {
	normalised := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value != "" {
			normalised = append(normalised, value)
		}
	}
	return normalised
}

// QueueFileResolutions asks about the files nobody has asked the providers
// about yet.
func (repository *Repository) QueueFileResolutions(ctx context.Context, limit int32) (int64, error) {
	return db.New(repository.pool).QueueFileResolutions(ctx, limit)
}

func (repository *Repository) QueueLibraryWitnessFingerprintSweep(
	ctx context.Context, runAfter time.Time,
) error {
	return db.New(repository.pool).QueueLibraryWitnessFingerprintSweep(ctx, runAfter)
}

func (repository *Repository) QueueLibraryWitnessFingerprintJobs(
	ctx context.Context, limit int32,
) (int64, error) {
	return db.New(repository.pool).QueueLibraryWitnessFingerprintJobs(ctx, limit)
}

func (repository *Repository) MarkUploadedFiles(
	ctx context.Context, uploadID uuid.UUID, paths []string,
) error {
	return db.New(repository.pool).MarkUploadedFiles(ctx, uploadID, paths)
}

// QueueNotifyPlayer asks for the player to be told the library changed.
func (repository *Repository) QueueNotifyPlayer(ctx context.Context) error {
	return db.New(repository.pool).QueueNotifyPlayer(ctx)
}

// SaveCatalogueReleaseGroup brings the release behind a proven file identity
// into the catalogue.
//
// The artist it is credited to is held without being followed. Following is a
// statement of intent — keep this artist's releases complete — and nobody made
// it. Owning one track of an album is only the fact that the library contains
// this artist's music, so the artist is recorded as exactly that, and the album
// is filed under them.
//
// An artist who is already followed stays followed: a proven file is a reason
// to hold an artist, never a reason to stop tracking one.
func (repository *Repository) SaveCatalogueReleaseGroup(
	ctx context.Context, detail musicbrainz.ReleaseGroupDetail,
) (uuid.UUID, error) {
	transaction, err := repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return uuid.Nil, err
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	// An artist refresh may have brought this release group in between the
	// ingest being queued and it running. Holding an artist for a release
	// somebody else already filed would leave that artist holding nothing, and
	// explaining itself with an album it does not own.
	var existingID uuid.UUID
	err = transaction.QueryRow(ctx, `
		SELECT id FROM albums WHERE musicbrainz_release_group_id = $1
	`, detail.ReleaseGroup.ID).Scan(&existingID)
	switch {
	case err == nil:
		return existingID, transaction.Commit(ctx)
	case !errors.Is(err, pgx.ErrNoRows):
		return uuid.Nil, fmt.Errorf("look up release group %s: %w", detail.ReleaseGroup.ID, err)
	}

	summary := fmt.Sprintf("In the catalogue because your library holds %q, credited to %s.",
		detail.ReleaseGroup.Title, credited(detail.Credit, detail.Artist.Name))

	var artistID uuid.UUID
	err = transaction.QueryRow(ctx, `
		INSERT INTO artists (musicbrainz_id, name, sort_name, followed_at, catalogue_summary)
		VALUES ($1, $2, $3, NULL, $4)
		ON CONFLICT (musicbrainz_id) DO UPDATE
		SET name = EXCLUDED.name,
		    sort_name = EXCLUDED.sort_name,
		    updated_at = now()
		RETURNING id
	`, detail.Artist.ID, detail.Artist.Name, detail.Artist.SortName, summary).Scan(&artistID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("upsert catalogue artist %s: %w", detail.Artist.ID, err)
	}

	var albumID uuid.UUID
	err = transaction.QueryRow(ctx, `
		INSERT INTO albums (
			artist_id, musicbrainz_release_group_id, title, release_date, album_type,
			secondary_types, genres
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (musicbrainz_release_group_id) DO UPDATE
		SET title = EXCLUDED.title,
		    release_date = EXCLUDED.release_date,
		    album_type = EXCLUDED.album_type,
		    secondary_types = EXCLUDED.secondary_types,
		    genres = EXCLUDED.genres,
		    updated_at = now()
		RETURNING id
	`, artistID, detail.ReleaseGroup.ID, detail.ReleaseGroup.Title,
		exactReleaseDate(detail.ReleaseGroup.FirstReleaseDate),
		albumType(detail.ReleaseGroup.PrimaryType),
		secondaryTypes(detail.ReleaseGroup.SecondaryTypes),
		detail.ReleaseGroup.Genres).Scan(&albumID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("upsert release group %s: %w", detail.ReleaseGroup.ID, err)
	}
	// The album keeps whichever artist it already had. A release group already
	// in the catalogue was put there by an artist refresh that browsed it, and
	// that attribution is better informed than one credited artist read off a
	// single release group.

	if err := upsertMetadataSource(
		ctx, transaction, "album", albumID,
		detail.ReleaseGroup.ID.String(), detail.ReleaseGroup.Metadata,
	); err != nil {
		return uuid.Nil, err
	}

	// The tracks are what the file actually needs, and they come from the
	// representative edition the existing refresh already knows how to choose.
	if _, err := transaction.Exec(ctx, `
		INSERT INTO jobs (kind, payload)
		VALUES ('refresh_album_metadata', jsonb_build_object('albumId', $1::text))
		ON CONFLICT (kind, (payload->>'albumId'))
		    WHERE kind = 'refresh_album_metadata' AND status IN ('queued', 'running')
		DO NOTHING
	`, albumID); err != nil {
		return uuid.Nil, fmt.Errorf("queue release refresh %s: %w", detail.ReleaseGroup.ID, err)
	}

	if err := wakeCoverArtSweep(ctx, transaction); err != nil {
		return uuid.Nil, err
	}

	if err := transaction.Commit(ctx); err != nil {
		return uuid.Nil, err
	}
	return albumID, nil
}

// credited prefers the full credit as MusicBrainz writes it, so a collaboration
// is named as one rather than as whichever artist happens to be first.
func credited(credit, artistName string) string {
	if trimmed := strings.TrimSpace(credit); trimmed != "" {
		return trimmed
	}
	return artistName
}
