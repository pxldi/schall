package db

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// SourceSearchFile and SourceSearchCandidate are the stored shape of what a
// search found. They mirror sources.Candidate rather than reusing it, for the
// same reason DownloadRequestFile does: what is written to the database is a
// record of what somebody was offered, and it must not change shape because an
// unrelated struct in another package gained a field.
type SourceSearchFile struct {
	Path            string `json:"path"`
	Name            string `json:"name"`
	Extension       string `json:"extension"`
	SizeBytes       int64  `json:"sizeBytes"`
	BitRate         int    `json:"bitRate,omitempty"`
	DurationSeconds int    `json:"durationSeconds,omitempty"`
	VariableBitRate bool   `json:"variableBitRate,omitempty"`
}

// SourceSearchMatch is the corroboration recorded alongside a candidate: how
// many catalogue tracks its files actually evidence. It is stored rather than
// recomputed because it is the reason a decision was offered the way it was,
// and the catalogue can change afterwards.
type SourceSearchMatch struct {
	Expected  int `json:"expected"`
	Confirmed int `json:"confirmed"`
	Partial   int `json:"partial"`
	Absent    int `json:"absent"`
}

type SourceSearchCandidate struct {
	Provider       string             `json:"provider"`
	Username       string             `json:"username"`
	Directory      string             `json:"directory"`
	TotalSizeBytes int64              `json:"totalSizeBytes"`
	Format         string             `json:"format"`
	AverageBitRate int                `json:"averageBitRate,omitempty"`
	FreeUploadSlot bool               `json:"freeUploadSlot"`
	QueueLength    int                `json:"queueLength"`
	UploadSpeed    int64              `json:"uploadSpeed"`
	Score          float64            `json:"score"`
	Match          SourceSearchMatch  `json:"match"`
	Reasons        []string           `json:"reasons"`
	Files          []SourceSearchFile `json:"files"`
}

// SourceSearchRefusal is one copy the user's stored format preferences took out
// of a search, and why. Kind is which rule did it, in the words
// sources.RefusedFormat and sources.RefusedBitRate use, so the interface can
// count one rule's refusals without reading the sentence back.
type SourceSearchRefusal struct {
	Username  string `json:"username"`
	Directory string `json:"directory"`
	Format    string `json:"format"`
	Reason    string `json:"reason"`
	Kind      string `json:"kind"`
}

// SourceSearchRow is one release's search within a run, with enough of the
// release attached to render it without a second query.
type SourceSearchRow struct {
	ID               uuid.UUID
	RunID            uuid.UUID
	AlbumID          uuid.UUID
	AlbumTitle       string
	ArtistID         uuid.UUID
	ArtistName       string
	TrackCount       int32
	OwnedTrackCount  int32
	FirstReleaseDate string
	Status           string
	Query            string
	Candidates       []SourceSearchCandidate
	// Refused are the copies the format preferences took out, kept so a search
	// that found only refused copies can say so instead of reading as an empty
	// network.
	Refused      []SourceSearchRefusal
	ErrorMessage pgtype.Text
	RequestedAt  pgtype.Timestamptz
	SearchedAt   pgtype.Timestamptz
	// RequestedDownloadID is the download request already recorded for this
	// release, if there is one. A run that has been acted on has to say so, or
	// reloading the page invites requesting the same folder twice.
	RequestedDownloadID uuid.NullUUID
	// AutoRequestedAt is when the run recorded the download itself, if it did.
	// A download nobody remembers asking for has to be able to say where it
	// came from.
	AutoRequestedAt pgtype.Timestamptz
}

// SourceSearchAlbum is what the worker needs to search for one release.
type SourceSearchAlbum struct {
	AlbumID    uuid.UUID
	AlbumTitle string
	ArtistName string
	TrackCount int32
	// Tracks are what a candidate is corroborated against. Without them a
	// search can only report how good a copy is, never whether it holds the
	// release that was asked for.
	Tracks []SourceSearchTrack
	// AutoRequest is the permission the run was started with: may this search
	// record the download itself when the evidence leaves nothing to decide?
	AutoRequest bool
}

// SourceSearchTrack is one catalogue track reduced to what a peer discloses
// about a file it has not sent: its name and how long it runs.
type SourceSearchTrack struct {
	Title           string `json:"title"`
	DurationSeconds int    `json:"durationSeconds"`
}

const sourceSearchColumns = `
	source_searches.id, source_searches.run_id, source_searches.album_id,
	albums.title, albums.artist_id, artists.name,
	counts.total, counts.owned, coalesce(metadata_sources.metadata->>'first-release-date', ''),
	CASE WHEN stopped.album_id IS NOT NULL AND source_searches.status IN ('queued', 'searching')
	     THEN 'cancelled' ELSE source_searches.status END,
	source_searches.query, source_searches.candidates, source_searches.refused_candidates,
	source_searches.error, source_searches.requested_at, source_searches.searched_at,
	requested.id, source_searches.auto_requested_at
`

// stoppedSourceSearches names the releases of a run that will never be
// searched. A cancelled release has no status of its own, and deliberately so:
// its row still says 'queued' because that is true — nothing ever looked for
// it — and what makes that queue permanent is the job, which is cancelled and
// can no longer be claimed. Reading it back from there keeps a run's progress
// an aggregate over its members with no second copy that could disagree, the
// same reason a run has no row of its own.
//
// The status the row does carry still wins where it is an answer: a release
// that was searched keeps what the search came to whatever became of its job,
// because a result is never withdrawn by anything that happens afterwards.
//
// `jobs_source_search_run_idx` covers the run ID this filters on, so the lookup
// reaches its rows directly rather than scanning a table that grows with every
// scan, refresh and search and is never purged. It stays MATERIALIZED and gated
// on the run having something unsettled, which lets the planner skip the work
// outright for a run that was searched to the end — the common one, and the one
// whose page is read again afterwards.
const stoppedSourceSearches = `
	WITH stopped AS MATERIALIZED (
		SELECT (jobs.payload->>'albumId')::uuid AS album_id
		FROM jobs
		WHERE jobs.kind = 'search_album_sources'
		  AND jobs.status = 'cancelled'
		  AND (jobs.payload->>'runId')::uuid = $1
		  AND EXISTS (
		      SELECT 1 FROM source_searches
		      WHERE source_searches.run_id = $1
		        AND source_searches.status IN ('queued', 'searching')
		  )
	)
`

const sourceSearchJoins = `
	FROM source_searches
	JOIN albums ON albums.id = source_searches.album_id
	JOIN artists ON artists.id = albums.artist_id
	LEFT JOIN metadata_sources
		ON metadata_sources.entity_type = 'album'
	   AND metadata_sources.entity_id = albums.id
	   AND metadata_sources.provider = 'musicbrainz'
	LEFT JOIN LATERAL (
		SELECT count(*)::int AS total,
		       count(*) FILTER (WHERE EXISTS (
		           SELECT 1 FROM track_mappings WHERE track_mappings.track_id = tracks.id
		       ))::int AS owned
		FROM tracks
		WHERE tracks.album_id = albums.id
	) counts ON true
	LEFT JOIN LATERAL (
		SELECT download_requests.id
		FROM download_requests
		WHERE download_requests.album_id = source_searches.album_id
		  AND download_requests.status <> 'cancelled'
		ORDER BY download_requests.requested_at DESC
		LIMIT 1
	) requested ON true
	LEFT JOIN stopped ON stopped.album_id = source_searches.album_id
`

// CreateSourceSearchRun records one search per release and queues a job for
// each. One job per release rather than one per run is what keeps a single
// unreachable peer from taking the other eleven down with it, and it is why a
// retry re-searches only the release that failed.
func (q *Queries) CreateSourceSearchRun(
	ctx context.Context, albumIDs []uuid.UUID, autoRequest bool,
) (uuid.UUID, int, error) {
	pool, ok := q.db.(beginner)
	if !ok {
		return uuid.Nil, 0, errors.New("queueing a source search requires a transactional connection")
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return uuid.Nil, 0, err
	}
	defer tx.Rollback(ctx)

	runID := uuid.New()
	// Unknown album IDs are dropped by the join rather than refused: the caller
	// validated them, and a release deleted between validating and inserting is
	// not a reason to lose the other eleven.
	tag, err := tx.Exec(ctx, `
		INSERT INTO source_searches (run_id, album_id, auto_request)
		SELECT $1, albums.id, $3
		FROM albums
		WHERE albums.id = ANY($2::uuid[])
		ON CONFLICT (run_id, album_id) DO NOTHING
	`, runID, albumIDs, autoRequest)
	if err != nil {
		return uuid.Nil, 0, err
	}
	queued := int(tag.RowsAffected())
	if queued == 0 {
		return uuid.Nil, 0, pgx.ErrNoRows
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO jobs (kind, payload, max_attempts)
		SELECT 'search_album_sources',
		       jsonb_build_object('runId', run_id::text, 'albumId', album_id::text),
		       2
		FROM source_searches
		WHERE run_id = $1
	`, runID); err != nil {
		return uuid.Nil, 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return uuid.Nil, 0, err
	}
	return runID, queued, nil
}

// SourceSearchRun reads every result of a run, newest release request first.
func (q *Queries) SourceSearchRun(ctx context.Context, runID uuid.UUID) ([]SourceSearchRow, error) {
	rows, err := q.db.Query(ctx, stoppedSourceSearches+`
		SELECT`+sourceSearchColumns+sourceSearchJoins+`
		WHERE source_searches.run_id = $1
		ORDER BY artists.name, albums.title, source_searches.id
	`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]SourceSearchRow, 0)
	for rows.Next() {
		var row SourceSearchRow
		var candidates, refusals []byte
		if err := rows.Scan(
			&row.ID, &row.RunID, &row.AlbumID,
			&row.AlbumTitle, &row.ArtistID, &row.ArtistName,
			&row.TrackCount, &row.OwnedTrackCount, &row.FirstReleaseDate,
			&row.Status, &row.Query, &candidates, &refusals,
			&row.ErrorMessage, &row.RequestedAt, &row.SearchedAt,
			&row.RequestedDownloadID, &row.AutoRequestedAt,
		); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(candidates, &row.Candidates); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(refusals, &row.Refused); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(result) == 0 {
		return nil, pgx.ErrNoRows
	}
	return result, nil
}

// ErrNothingToCancel reports a run with no release left waiting — every one of
// them has been searched, or stopped already. There is nothing to withdraw and
// the answers are still there to act on, so it is refused rather than reported
// as a cancellation that cancelled nothing.
var ErrNothingToCancel = errors.New("no release of this run is still waiting to be searched")

// CancelSourceSearchRun stops the releases of a run that have not been searched
// yet and returns the run as it now stands, with how many releases it stopped.
// A run nobody has any part of returns pgx.ErrNoRows; a run that is entirely
// searched returns ErrNothingToCancel.
//
// Only a queued job is cancelled. One already claimed is left to finish, the
// same way a running metadata refresh is: the worker holds it, and one release
// more than was asked for is a better outcome than a result recorded halfway.
// What has already been answered is never touched — a result is something
// somebody may act on, and cancelling a run must not withdraw it.
func (q *Queries) CancelSourceSearchRun(
	ctx context.Context, runID uuid.UUID,
) ([]SourceSearchRow, int, error) {
	tag, err := q.db.Exec(ctx, `
		UPDATE jobs
		SET status = 'cancelled', completed_at = now(), updated_at = now()
		WHERE kind = 'search_album_sources'
		  AND status = 'queued'
		  AND (payload->>'runId')::uuid = $1
		  AND EXISTS (
		      SELECT 1 FROM source_searches
		      WHERE source_searches.run_id = $1
		        AND source_searches.album_id = (jobs.payload->>'albumId')::uuid
		        AND source_searches.status IN ('queued', 'searching')
		  )
	`, runID)
	if err != nil {
		return nil, 0, err
	}
	stopped := int(tag.RowsAffected())

	// Read back before deciding what to report: a run that does not exist and a
	// run with nothing left to stop both cancelled nothing, and only its rows
	// tell them apart.
	rows, err := q.SourceSearchRun(ctx, runID)
	if err != nil {
		return nil, 0, err
	}
	if stopped == 0 {
		return nil, 0, ErrNothingToCancel
	}
	return rows, stopped, nil
}

// SourceSearchRunCancelled reports whether a run was stopped. It is the same
// mark the run itself is read by: cancelling sets the jobs of the releases
// nobody has searched to 'cancelled', and a cancellation that stopped no job is
// refused, so a run with no cancelled job of its own was never cancelled.
//
// It exists for the release that was being searched when the run was stopped.
// That job was running, so nothing cancelled it, and only this tells the worker
// that the run it belongs to is over.
func (q *Queries) SourceSearchRunCancelled(ctx context.Context, runID uuid.UUID) (bool, error) {
	var cancelled bool
	err := q.db.QueryRow(ctx, `
		SELECT EXISTS (
		    SELECT 1 FROM jobs
		    WHERE kind = 'search_album_sources'
		      AND status = 'cancelled'
		      AND (payload->>'runId')::uuid = $1
		)
	`, runID).Scan(&cancelled)
	return cancelled, err
}

// ClaimSourceSearch marks one release as being searched and returns what the
// query has to be built from. A row that has already been answered is claimed
// again on purpose: the worker recovering an interrupted job should re-search
// rather than leave a result nobody can tell apart from a finished one.
func (q *Queries) ClaimSourceSearch(
	ctx context.Context, runID, albumID uuid.UUID,
) (SourceSearchAlbum, error) {
	var album SourceSearchAlbum
	var tracks []byte
	err := q.db.QueryRow(ctx, `
		WITH claimed AS (
			UPDATE source_searches
			SET status = 'searching', error = NULL
			WHERE run_id = $1 AND album_id = $2
			RETURNING album_id, auto_request
		)
		SELECT albums.id, albums.title, artists.name, claimed.auto_request,
		       (SELECT count(*)::int FROM tracks WHERE tracks.album_id = albums.id),
		       coalesce((
		           SELECT json_agg(json_build_object(
		               'title', tracks.title,
		               'durationSeconds', coalesce(tracks.duration_ms, 0) / 1000
		           ) ORDER BY tracks.disc_number, tracks.track_number)
		           FROM tracks WHERE tracks.album_id = albums.id
		       ), '[]'::json)
		FROM claimed
		JOIN albums ON albums.id = claimed.album_id
		JOIN artists ON artists.id = albums.artist_id
	`, runID, albumID).Scan(
		&album.AlbumID, &album.AlbumTitle, &album.ArtistName, &album.AutoRequest,
		&album.TrackCount, &tracks,
	)
	if err != nil {
		return album, err
	}
	if err := json.Unmarshal(tracks, &album.Tracks); err != nil {
		return album, err
	}
	return album, nil
}

// MarkSourceSearchAutoRequested records that the run recorded the download for
// this release itself. It is a separate statement from RecordSourceSearch
// because the search settling and the request being recorded are different
// events, and only the second one may be missing.
func (q *Queries) MarkSourceSearchAutoRequested(
	ctx context.Context, runID, albumID uuid.UUID, at time.Time,
) error {
	_, err := q.db.Exec(ctx, `
		UPDATE source_searches
		SET auto_requested_at = $3
		WHERE run_id = $1 AND album_id = $2 AND auto_request
	`, runID, albumID, at)
	return err
}

type RecordSourceSearchParams struct {
	RunID      uuid.UUID
	AlbumID    uuid.UUID
	Status     string
	Query      string
	Candidates []SourceSearchCandidate
	// Refused are the copies the user's own format preferences took out of this
	// search. They are stored with the ones that stayed because "no peer is
	// sharing it" and "no peer is sharing it in a form you accept" are different
	// answers, and only the second has somebody who can act on it.
	Refused    []SourceSearchRefusal
	Error      string
	SearchedAt time.Time
}

// RecordSourceSearch stores what one search came to. Finding nothing and
// failing to look are stored as different statuses, because they are different
// answers: one says no peer had it, the other says nobody asked.
func (q *Queries) RecordSourceSearch(ctx context.Context, params RecordSourceSearchParams) error {
	candidates := params.Candidates
	if candidates == nil {
		candidates = []SourceSearchCandidate{}
	}
	encoded, err := json.Marshal(candidates)
	if err != nil {
		return err
	}
	refused := params.Refused
	if refused == nil {
		refused = []SourceSearchRefusal{}
	}
	encodedRefusals, err := json.Marshal(refused)
	if err != nil {
		return err
	}
	_, err = q.db.Exec(ctx, `
		UPDATE source_searches
		SET status = $3,
		    query = $4,
		    candidates = $5::jsonb,
		    refused_candidates = $8::jsonb,
		    error = nullif($6, ''),
		    searched_at = $7
		WHERE run_id = $1 AND album_id = $2
	`, params.RunID, params.AlbumID, params.Status, params.Query,
		encoded, params.Error, params.SearchedAt, encodedRefusals)
	return err
}
