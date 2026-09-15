package db

import (
	"context"

	"github.com/google/uuid"
)

// RecordRecordingAlias remembers that MusicBrainz answered one recording
// identifier with another — that the first was merged into the second.
//
// It is written from one place only: the identity path, immediately after the
// provider was asked which recording an identifier names now and answered with
// a different one. See migration 00056 for why nothing else may write it.
//
// A row already there is left alone rather than refreshed. What it records is
// the provider's answer, and asking again cannot make an answer more true; the
// timestamp is when Schall first learned it, which is the useful reading.
//
// Learning a merge is one of the facts a stopped want is waiting for, so the
// wants that stopped on exactly this question are put back through the grader
// when the row is new. Nothing is decided here: the copies go through ImportOne,
// which now has the answer it did not have.
func (q *Queries) RecordRecordingAlias(ctx context.Context, retired, surviving uuid.UUID) error {
	if retired == uuid.Nil || surviving == uuid.Nil || retired == surviving {
		return nil
	}
	tag, err := q.db.Exec(ctx, `
		INSERT INTO recording_aliases (retired_id, surviving_id)
		VALUES ($1, $2)
		ON CONFLICT (retired_id) DO NOTHING
	`, retired, surviving)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	return queueJudgingForLearnedMerge(ctx, q.db, retired, surviving)
}

// CanonicalRecordingIDs answers, for each identifier handed in, the identifier
// MusicBrainz answers with today. An identifier nothing was merged into is its
// own answer, so every input appears in the result and a caller never has to
// check whether it did.
//
// It is a batch because the callers hold pairs: comparing a file's identifier
// with a track's needs both canonicalised, and two round trips to answer one
// comparison is a round trip too many.
func (q *Queries) CanonicalRecordingIDs(
	ctx context.Context, ids []uuid.UUID,
) (map[uuid.UUID]uuid.UUID, error) {
	canonical := make(map[uuid.UUID]uuid.UUID, len(ids))
	wanted := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if id == uuid.Nil {
			continue
		}
		if _, seen := canonical[id]; seen {
			continue
		}
		// Its own answer until the table says otherwise.
		canonical[id] = id
		wanted = append(wanted, id)
	}
	if len(wanted) == 0 {
		return canonical, nil
	}

	rows, err := q.db.Query(ctx, `
		SELECT retired_id, surviving_id
		FROM recording_aliases
		WHERE retired_id = ANY($1::uuid[])
	`, wanted)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var retired, surviving uuid.UUID
		if err := rows.Scan(&retired, &surviving); err != nil {
			return nil, err
		}
		canonical[retired] = surviving
	}
	return canonical, rows.Err()
}
