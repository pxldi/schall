package db

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// ReplaceRecommendationSnapshot records one source's current usable answer,
// header and candidates together. A failed replacement leaves the previous
// answer intact; a successful empty replacement deliberately leaves no rows.
func (q *Queries) ReplaceRecommendationSnapshot(
	ctx context.Context,
	header UpsertRecommendationSnapshotParams,
	candidates []InsertRecommendationCandidateParams,
) error {
	return q.inTransaction(ctx, "replacing a recommendation snapshot", func(tx pgx.Tx) error {
		queries := q.WithTx(tx)
		if _, err := queries.UpsertRecommendationSnapshot(ctx, header); err != nil {
			return err
		}
		if err := queries.DeleteRecommendationCandidates(ctx, header.Source); err != nil {
			return err
		}
		for _, candidate := range candidates {
			candidate.Source = header.Source
			candidate.FetchedAt = header.FetchedAt
			if _, err := queries.InsertRecommendationCandidate(ctx, candidate); err != nil {
				return err
			}
		}
		return nil
	})
}

// ErrNoStagedSnapshot says a publish was asked for and there was nothing built
// to publish.
var ErrNoStagedSnapshot = errors.New("there is no half-built recommendation snapshot to publish")

// PublishRecommendationSnapshot makes the snapshot built under one source name
// the snapshot stored under another.
//
// A recommendation sweep reads a whole account over many short passes, which
// takes about half an hour. Both tables are keyed by source, so the sweep writes
// each pass under a second source name nothing reads — the staged snapshot —
// and calls this when it has walked the whole list. Until then the source people
// read holds the previous complete answer.
//
// The swap is one transaction, so a reader sees the old list or the new one and
// never a mixture of the two. Candidate rows name their source and point at the
// header row of that name, so they are moved rather than copied: the published
// header takes the staged header's values first, the candidates published before
// go, the staged candidates are moved onto the published header, and the staged
// header is removed with nothing left under it.
func (q *Queries) PublishRecommendationSnapshot(ctx context.Context, staged, published string) error {
	if staged == published {
		return errors.New("a recommendation snapshot cannot be published over itself")
	}
	return q.inTransaction(ctx, "publishing a recommendation snapshot", func(tx pgx.Tx) error {
		header, err := tx.Exec(ctx, `
			INSERT INTO recommendation_snapshots (
				source, source_snapshot_id, source_snapshot_at, status, detail, fetched_at
			)
			SELECT $2, source_snapshot_id, source_snapshot_at, status, detail, fetched_at
			FROM recommendation_snapshots
			WHERE source = $1
			ON CONFLICT (source) DO UPDATE SET
				source_snapshot_id = excluded.source_snapshot_id,
				source_snapshot_at = excluded.source_snapshot_at,
				status = excluded.status,
				detail = excluded.detail,
				fetched_at = excluded.fetched_at
		`, staged, published)
		if err != nil {
			return err
		}
		if header.RowsAffected() == 0 {
			return ErrNoStagedSnapshot
		}
		if _, err := tx.Exec(ctx,
			`DELETE FROM recommendation_candidates WHERE source = $1`, published); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`UPDATE recommendation_candidates SET source = $2 WHERE source = $1`,
			staged, published); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `DELETE FROM recommendation_snapshots WHERE source = $1`, staged)
		return err
	})
}

// DiscardRecommendationSnapshot removes one source's snapshot and every
// candidate under it. A candidate row points at its header row, so the rows go
// with the header.
//
// A sweep uses this on the snapshot it was still building and will not finish.
// Asking for a source that has no snapshot is not an error.
func (q *Queries) DiscardRecommendationSnapshot(ctx context.Context, source string) error {
	_, err := q.db.Exec(ctx, `DELETE FROM recommendation_snapshots WHERE source = $1`, source)
	return err
}

// RecommendationSnapshot returns one source's current usable snapshot.
func (q *Queries) RecommendationSnapshot(ctx context.Context, source string) (RecommendationSnapshot, error) {
	var result RecommendationSnapshot
	err := q.db.QueryRow(ctx, `
		SELECT source, source_snapshot_id, source_snapshot_at, status, detail, fetched_at
		FROM recommendation_snapshots
		WHERE source = $1
	`, source).Scan(
		&result.Source,
		&result.SourceSnapshotID,
		&result.SourceSnapshotAt,
		&result.Status,
		&result.Detail,
		&result.FetchedAt,
	)
	return result, err
}

// RecommendationSweepState is what the job queue holds about the background
// sweep that keeps the recommendation list up to date.
//
// The sweep is a row in the jobs table with kind 'sweep_recommendations'. It
// runs by itself, and nobody presses anything to start it. One sweep is a chain
// of short passes, each of which is its own row: a pass that ends leaves a
// finished row behind and queues the next one, with the wait written into that
// row's run_after. So the last finished row and the waiting row together say
// what the sweep last did and when it comes back.
//
// This is read to tell a reader that the list stopped being refreshed. A pass
// that reached nobody still finishes tidily, so the row's own status does not
// answer that on its own — see Started (issue #316).
type RecommendationSweepState struct {
	// Attempted is false when the queue holds no finished sweep at all. That is
	// an installation where the sweep has never run to an end, such as a freshly
	// seeded or restored database, and it is not a fault.
	Attempted bool
	// Started is when the last finished pass began, and Finished is when it
	// ended. Started is the one to compare a stored list against: a pass writes
	// the list before the queue marks the pass done, so a pass that refreshed the
	// list left it written at or after the pass started.
	Started  pgtype.Timestamptz
	Finished pgtype.Timestamptz
	// NextRunAfter is the earliest a waiting pass may run. It is unset when no
	// pass is waiting.
	NextRunAfter pgtype.Timestamptz
	// Running says a pass has been claimed and is working now.
	Running bool
}

// RecommendationSweepState reads the two job rows that say whether the sweep is
// still refreshing the list. The kind is written out here, as it is in the index
// that keeps one sweep at a time (migration 00048); internal/jobs owns the name.
func (q *Queries) RecommendationSweepState(ctx context.Context) (RecommendationSweepState, error) {
	var state RecommendationSweepState
	err := q.db.QueryRow(ctx, `
		WITH finished AS (
			SELECT started_at, completed_at
			FROM jobs
			WHERE kind = 'sweep_recommendations'
			  AND status IN ('completed', 'failed', 'cancelled')
			ORDER BY completed_at DESC
			LIMIT 1
		), waiting AS (
			SELECT
				min(run_after) FILTER (WHERE status = 'queued') AS next_run_after,
				count(*) FILTER (WHERE status = 'running') > 0 AS running
			FROM jobs
			WHERE kind = 'sweep_recommendations'
			  AND status IN ('queued', 'running')
		)
		SELECT
			EXISTS (SELECT 1 FROM finished),
			(SELECT started_at FROM finished),
			(SELECT completed_at FROM finished),
			waiting.next_run_after,
			coalesce(waiting.running, false)
		FROM waiting
	`).Scan(
		&state.Attempted,
		&state.Started,
		&state.Finished,
		&state.NextRunAfter,
		&state.Running,
	)
	return state, err
}

// RecommendationCandidateCount is how many candidate rows one source holds.
//
// A recommendation sweep asks this about the published source before it would
// publish an answer with no records in it. Both ListenBrainz services say the
// same thing when an account has nothing to suggest as when they are quiet, so
// an answer of nothing is believed only where nothing is published already
// (issue #317).
func (q *Queries) RecommendationCandidateCount(ctx context.Context, source string) (int, error) {
	var count int
	err := q.db.QueryRow(ctx,
		`SELECT count(*) FROM recommendation_candidates WHERE source = $1`, source).Scan(&count)
	return count, err
}

// RecommendationCandidates returns one source's stored provider answer without
// applying local suppression. It exists for lifecycle checks and refreshes;
// anything rendered uses ListRecommendationCandidateFacts instead.
func (q *Queries) RecommendationCandidates(ctx context.Context, source string) ([]RecommendationCandidate, error) {
	rows, err := q.db.Query(ctx, `
		SELECT
			source, musicbrainz_recording_id, musicbrainz_release_group_id,
			musicbrainz_artist_ids, recording_title, release_title, artist_name,
			rank, source_score, reason_codes, reason_context, fetched_at
		FROM recommendation_candidates
		WHERE source = $1
		ORDER BY rank
	`, source)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]RecommendationCandidate, 0, 32)
	for rows.Next() {
		var candidate RecommendationCandidate
		if err := rows.Scan(
			&candidate.Source,
			&candidate.MusicbrainzRecordingID,
			&candidate.MusicbrainzReleaseGroupID,
			&candidate.MusicbrainzArtistIds,
			&candidate.RecordingTitle,
			&candidate.ReleaseTitle,
			&candidate.ArtistName,
			&candidate.Rank,
			&candidate.SourceScore,
			&candidate.ReasonCodes,
			&candidate.ReasonContext,
			&candidate.FetchedAt,
		); err != nil {
			return nil, err
		}
		result = append(result, candidate)
	}
	return result, rows.Err()
}
