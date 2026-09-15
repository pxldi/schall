package db

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// NavidromePlaylistSnapshotRow is what Navidrome was last told about one
// playlist: which remote object it is, which side created it, and when the
// telling happened. The tracks are read separately, because most callers want
// to know whether a list is joined at all before they want its contents.
type NavidromePlaylistSnapshotRow struct {
	PlaylistID          uuid.UUID
	NavidromePlaylistID string
	Adopted             bool
	PushedAt            pgtype.Timestamptz
	CreatedAt           pgtype.Timestamptz
	UpdatedAt           pgtype.Timestamptz
}

// NavidromePlaylistSnapshotTrackRow is one track as it was pushed. The id and
// the path travel together and are read together: ADR 0010 reads an entry's
// absence as a removal only when the id still reports the path it was pushed
// at, so the path is the snapshot's own copy and never a join to the library.
type NavidromePlaylistSnapshotTrackRow struct {
	PlaylistID      uuid.UUID
	Position        int32
	NavidromeSongID string
	LibraryPath     string
	LibraryFileID   uuid.NullUUID
	PlaylistEntryID uuid.NullUUID
	PushedAt        pgtype.Timestamptz
}

// NavidromePlaylistSnapshotParams is the header one push writes.
type NavidromePlaylistSnapshotParams struct {
	PlaylistID          uuid.UUID
	NavidromePlaylistID string
	Adopted             bool
}

// NavidromePlaylistSnapshotTrackParams is one pushed track. Only tracks that
// were actually sent belong here — a want nobody could acquire has no row, and
// that absence is what stops the next sync reading it as a removal.
type NavidromePlaylistSnapshotTrackParams struct {
	Position        int32
	NavidromeSongID string
	LibraryPath     string
	LibraryFileID   uuid.NullUUID
	PlaylistEntryID uuid.NullUUID
}

const navidromeSnapshotColumns = `
	playlist_id, navidrome_playlist_id, adopted, pushed_at, created_at, updated_at
`

// NavidromePlaylistSnapshot reads what one playlist was last told, or
// pgx.ErrNoRows when it has never been pushed.
func (q *Queries) NavidromePlaylistSnapshot(
	ctx context.Context, playlistID uuid.UUID,
) (NavidromePlaylistSnapshotRow, error) {
	return scanNavidromeSnapshot(q.db.QueryRow(ctx, `
		SELECT`+navidromeSnapshotColumns+`
		FROM navidrome_playlist_snapshots
		WHERE playlist_id = $1
	`, playlistID))
}

// NavidromePlaylistSnapshots reads every joined list, oldest join first.
func (q *Queries) NavidromePlaylistSnapshots(ctx context.Context) ([]NavidromePlaylistSnapshotRow, error) {
	rows, err := q.db.Query(ctx, `
		SELECT`+navidromeSnapshotColumns+`
		FROM navidrome_playlist_snapshots
		ORDER BY created_at, playlist_id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]NavidromePlaylistSnapshotRow, 0, 16)
	for rows.Next() {
		snapshot, err := scanNavidromeSnapshot(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, snapshot)
	}
	return result, rows.Err()
}

// SnapshotByNavidromeID reads the snapshot for a remote list, or pgx.ErrNoRows.
// This is how a playlist found in Navidrome is told apart from one to adopt:
// an id already answered here belongs to a Schall list, and adopting it again
// would make two want lists push into one remote object.
func (q *Queries) SnapshotByNavidromeID(
	ctx context.Context, navidromeID string,
) (NavidromePlaylistSnapshotRow, error) {
	return scanNavidromeSnapshot(q.db.QueryRow(ctx, `
		SELECT`+navidromeSnapshotColumns+`
		FROM navidrome_playlist_snapshots
		WHERE navidrome_playlist_id = $1
	`, navidromeID))
}

// NavidromePlaylistSnapshotTracks reads the pushed tracks in the order they
// were pushed in.
func (q *Queries) NavidromePlaylistSnapshotTracks(
	ctx context.Context, playlistID uuid.UUID,
) ([]NavidromePlaylistSnapshotTrackRow, error) {
	rows, err := q.db.Query(ctx, `
		SELECT playlist_id, position, navidrome_song_id, library_path,
		       library_file_id, playlist_entry_id, pushed_at
		FROM navidrome_playlist_snapshot_tracks
		WHERE playlist_id = $1
		ORDER BY position
	`, playlistID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]NavidromePlaylistSnapshotTrackRow, 0, 64)
	for rows.Next() {
		var track NavidromePlaylistSnapshotTrackRow
		if err := rows.Scan(
			&track.PlaylistID, &track.Position, &track.NavidromeSongID,
			&track.LibraryPath, &track.LibraryFileID, &track.PlaylistEntryID,
			&track.PushedAt,
		); err != nil {
			return nil, err
		}
		result = append(result, track)
	}
	return result, rows.Err()
}

// ReplaceNavidromePlaylistSnapshot records what a push has just said, header
// and tracks together in one transaction. A push replaces the last statement
// in full rather than amending it: a half-written snapshot would describe a
// state Navidrome was never in, and the next sync would read the difference
// between it and the listing as the user's doing.
func (q *Queries) ReplaceNavidromePlaylistSnapshot(
	ctx context.Context,
	header NavidromePlaylistSnapshotParams,
	tracks []NavidromePlaylistSnapshotTrackParams,
) error {
	return q.inTransaction(ctx, "recording what was pushed to Navidrome", func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO navidrome_playlist_snapshots (
				playlist_id, navidrome_playlist_id, adopted
			)
			VALUES ($1, $2, $3)
			ON CONFLICT (playlist_id) DO UPDATE SET
				navidrome_playlist_id = excluded.navidrome_playlist_id,
				adopted = excluded.adopted,
				pushed_at = now(),
				updated_at = now()
		`, header.PlaylistID, header.NavidromePlaylistID, header.Adopted); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			DELETE FROM navidrome_playlist_snapshot_tracks WHERE playlist_id = $1
		`, header.PlaylistID); err != nil {
			return err
		}
		for _, track := range tracks {
			if _, err := tx.Exec(ctx, `
				INSERT INTO navidrome_playlist_snapshot_tracks (
					playlist_id, position, navidrome_song_id, library_path,
					library_file_id, playlist_entry_id
				)
				VALUES ($1, $2, $3, $4, $5, $6)
			`,
				header.PlaylistID, track.Position, track.NavidromeSongID,
				track.LibraryPath, track.LibraryFileID, track.PlaylistEntryID,
			); err != nil {
				return err
			}
		}
		return nil
	})
}

// RepairNavidromeSnapshotTrack re-points one pushed track at the id the same
// file now answers to. 0010 leaves the broken identity healable and says
// nothing was removed, so the position and the path stay exactly as they were
// pushed: the path is the evidence the repair was made from, and rewriting it
// here would turn a repair into a claim that a different file was sent.
func (q *Queries) RepairNavidromeSnapshotTrack(
	ctx context.Context, playlistID uuid.UUID, position int32, newSongID string,
) error {
	tag, err := q.db.Exec(ctx, `
		UPDATE navidrome_playlist_snapshot_tracks
		SET navidrome_song_id = $3
		WHERE playlist_id = $1 AND position = $2
	`, playlistID, position, newSongID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// DeleteNavidromePlaylistSnapshot forgets that a list was ever pushed. Its
// tracks go with it; the playlist and its wants are untouched, because leaving
// Navidrome is not the statement "stop wanting this music".
func (q *Queries) DeleteNavidromePlaylistSnapshot(ctx context.Context, playlistID uuid.UUID) error {
	tag, err := q.db.Exec(ctx, `
		DELETE FROM navidrome_playlist_snapshots WHERE playlist_id = $1
	`, playlistID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// QueueNavidromePlaylistSync asks for one sync pass over one playlist.
//
// The partial unique index from 00038 allows one live pass per list, so asking
// while one is queued or running is answered by the pass that already exists: a
// second pass would diff against a snapshot the first is in the middle of
// replacing, and the difference it saw would read as the user's doing.
//
// A list that does not exist is pgx.ErrNoRows rather than a job that fails
// later, so that the caller who asked hears about it.
func (q *Queries) QueueNavidromePlaylistSync(ctx context.Context, playlistID uuid.UUID) error {
	var known bool
	if err := q.db.QueryRow(ctx, `
		SELECT true FROM playlists WHERE id = $1
	`, playlistID).Scan(&known); err != nil {
		return err
	}
	_, err := q.db.Exec(ctx, `
		INSERT INTO jobs (kind, payload, max_attempts)
		VALUES ('sync_navidrome_playlists', jsonb_build_object('playlistId', $1::uuid), 3)
		ON CONFLICT (kind, (payload ->> 'playlistId'))
		    WHERE kind = 'sync_navidrome_playlists' AND status IN ('queued', 'running')
		DO NOTHING
	`, playlistID)
	return err
}

// QueueNavidromePlaylistSweep asks for one look at the whole player: the lists
// it holds that Schall has not joined, and then a pass over every want list.
//
// It names no playlist, which is also why 00038's index does not collapse two
// of them: the index is keyed on the playlist a pass diffs against, and a sweep
// diffs against nothing. Two sweeps meeting cost a repeated reading of the
// player's playlists and adopt nothing twice — SnapshotByNavidromeID and the
// unique index behind it decide that, not the order they arrive in.
//
// That was harmless while a sweep only ever happened because somebody pressed
// something. It is not harmless now that a sweep queues its own replacement: a
// second self-scheduling sweep does not run twice, it doubles the rate of every
// pass after it. So one is kept, by hand rather than by an index — the index
// 00038 holds cannot express it, because the column it would key on is null for
// every sweep and null never collides.
//
// A waiting sweep is moved earlier rather than joined, which is what lets the
// button mean something: an idle schedule six hours out is not an answer to
// somebody asking now.
func (q *Queries) QueueNavidromePlaylistSweep(ctx context.Context, runAfter time.Time) error {
	tag, err := q.db.Exec(ctx, `
		UPDATE jobs
		SET run_after = least(run_after, $1), updated_at = now()
		WHERE kind = 'sync_navidrome_playlists'
		  AND payload ->> 'playlistId' IS NULL
		  AND status = 'queued'
	`, runAfter)
	if err != nil {
		return err
	}
	if tag.RowsAffected() > 0 {
		return nil
	}
	// Nothing waiting. One already running is left to finish and to queue its
	// own replacement; adding a second here would be the doubling above.
	_, err = q.db.Exec(ctx, `
		INSERT INTO jobs (kind, payload, max_attempts, run_after)
		SELECT 'sync_navidrome_playlists', '{}'::jsonb, 3, $1
		WHERE NOT EXISTS (
			SELECT 1 FROM jobs
			WHERE kind = 'sync_navidrome_playlists'
			  AND payload ->> 'playlistId' IS NULL
			  AND status IN ('queued', 'running')
		)
	`, runAfter)
	return err
}

func scanNavidromeSnapshot(row pgx.Row) (NavidromePlaylistSnapshotRow, error) {
	var result NavidromePlaylistSnapshotRow
	err := row.Scan(
		&result.PlaylistID, &result.NavidromePlaylistID, &result.Adopted,
		&result.PushedAt, &result.CreatedAt, &result.UpdatedAt,
	)
	return result, err
}

// AcquiredPlaylistTrackRow is one entry Schall holds a file for, with the file
// a push would send.
//
// Artist and Title are the words for that file, the file's own tags where it
// has them and the entry's words where it does not. They are carried because a
// push has to ask the player which song this file is, and a player that indexes
// tags has to be asked in words: they are the question, never the answer — what
// the file is stays decided by the path.
type AcquiredPlaylistTrackRow struct {
	EntryID       uuid.UUID
	Position      int32
	LibraryFileID uuid.UUID
	LibraryPath   string
	Artist        string
	Title         string
}

// AcquiredPlaylistTracks reads the acquired subset of a playlist, in entry
// order: the tracks a push has something to send. An entry with no file is
// simply absent — it is not an error, and by 0004 it is not a removal either,
// because it was never pushed in the first place.
//
// An entry is answered from either side of the same question. The ownership
// record is written when the list is imported and only re-asked there
// (internal/playlists/service.go), which is exactly why the second arm exists:
// a want acquired since the last import has no ownership record yet, and
// reading ownership alone would leave the music the user waited for out of
// every push until the next import happened to run.
//
// Where a want was superseded the trail is followed to the survivor, the same
// walk PlaylistEntries makes and the one 0006 built — a superseded target is
// the same want under another row, and the copy was acquired on the survivor.
//
// The two arms below are answeringFile in playlists.go, which is what a
// playlist's owned count is made of, with the file's path and tags carried out
// alongside. They are written twice because only this caller needs the file
// itself, and they have to keep answering alike: what a list says is owned is
// what a push has to have something to send for.
//
// More than one accepted file is not an ambiguity. Every accepted row was
// proven to be that recording before it was accepted, so all of them are the
// wanted music and the only open question is which copy the player is handed;
// the most recently decided one wins. Nothing here compares words, and no
// identity is being settled by this ordering.
func (q *Queries) AcquiredPlaylistTracks(
	ctx context.Context, playlistID uuid.UUID,
) ([]AcquiredPlaylistTrackRow, error) {
	rows, err := q.db.Query(ctx, `
		SELECT playlist_entries.id, playlist_entries.position,
		       acquired.library_file_id, acquired.library_path,
		       coalesce(nullif(btrim(acquired.artist_tag), ''), playlist_entries.entry_artist),
		       coalesce(nullif(btrim(acquired.title_tag), ''), playlist_entries.entry_title)
		FROM playlist_entries
		JOIN LATERAL (
			SELECT library_files.id AS library_file_id,
			       library_files.path AS library_path,
			       library_files.artist_tag,
			       library_files.title_tag,
			       NULL::timestamptz AS decided_at
			FROM library_files
			WHERE library_files.id = playlist_entries.owned_library_file_id

			UNION ALL

			SELECT library_files.id, library_files.path,
			       library_files.artist_tag, library_files.title_tag,
			       acquisition_target_files.decided_at
			FROM playlist_entry_targets
			JOIN acquisition_targets joined
			    ON joined.id = playlist_entry_targets.acquisition_target_id
			LEFT JOIN acquisition_targets survivor
			    ON survivor.id = joined.superseded_by_id
			JOIN acquisition_target_files
			    ON acquisition_target_files.acquisition_target_id
			       = coalesce(survivor.id, joined.id)
			JOIN library_files
			    ON library_files.id = acquisition_target_files.library_file_id
			WHERE playlist_entry_targets.playlist_entry_id = playlist_entries.id
			  AND acquisition_target_files.verdict = 'accepted'

			ORDER BY decided_at DESC NULLS LAST, library_file_id
			LIMIT 1
		) acquired ON true
		WHERE playlist_entries.playlist_id = $1
		ORDER BY playlist_entries.position, playlist_entries.id
	`, playlistID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]AcquiredPlaylistTrackRow, 0, 64)
	for rows.Next() {
		var track AcquiredPlaylistTrackRow
		if err := rows.Scan(
			&track.EntryID, &track.Position, &track.LibraryFileID, &track.LibraryPath,
			&track.Artist, &track.Title,
		); err != nil {
			return nil, err
		}
		result = append(result, track)
	}
	return result, rows.Err()
}
