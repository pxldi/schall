package db

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// NewReleasesPlaylistSettingsRow is whether the new-releases playlist is kept
// at all, and how far back it reaches.
type NewReleasesPlaylistSettingsRow struct {
	Enabled    bool
	WindowDays int32
	UpdatedAt  pgtype.Timestamptz
}

// SaveNewReleasesPlaylistSettingsParams is what a person set on the settings
// screen.
type SaveNewReleasesPlaylistSettingsParams struct {
	Enabled    bool
	WindowDays int32
}

const newReleasesPlaylistSettingsColumns = `
	new_releases_playlist_settings.enabled,
	new_releases_playlist_settings.window_days,
	new_releases_playlist_settings.updated_at
`

// NewReleasesPlaylistSettings returns the settings row, or pgx.ErrNoRows when
// nobody has ever saved one. No row means the defaults, in the shape the weekly
// playlist settings use.
func (q *Queries) NewReleasesPlaylistSettings(ctx context.Context) (NewReleasesPlaylistSettingsRow, error) {
	return scanNewReleasesPlaylistSettings(q.db.QueryRow(ctx, `
		SELECT`+newReleasesPlaylistSettingsColumns+`
		FROM new_releases_playlist_settings
		WHERE singleton
	`))
}

// SaveNewReleasesPlaylistSettings writes the one settings row.
func (q *Queries) SaveNewReleasesPlaylistSettings(
	ctx context.Context, params SaveNewReleasesPlaylistSettingsParams,
) (NewReleasesPlaylistSettingsRow, error) {
	return scanNewReleasesPlaylistSettings(q.db.QueryRow(ctx, `
		INSERT INTO new_releases_playlist_settings (enabled, window_days)
		VALUES ($1, $2)
		ON CONFLICT (singleton) DO UPDATE SET
			enabled = excluded.enabled,
			window_days = excluded.window_days,
			updated_at = now()
		RETURNING`+newReleasesPlaylistSettingsColumns,
		params.Enabled, params.WindowDays,
	))
}

func scanNewReleasesPlaylistSettings(row pgx.Row) (NewReleasesPlaylistSettingsRow, error) {
	var settings NewReleasesPlaylistSettingsRow
	if err := row.Scan(&settings.Enabled, &settings.WindowDays, &settings.UpdatedAt); err != nil {
		return NewReleasesPlaylistSettingsRow{}, err
	}
	return settings, nil
}

// EnsureNewReleasesPlaylist returns the one new-releases playlist, creating it
// the first time. There is one for the life of the installation: the window
// moves over it, and it is never replaced by a second list.
func (q *Queries) EnsureNewReleasesPlaylist(ctx context.Context, name string) (PlaylistRow, error) {
	return scanPlaylist(q.db.QueryRow(ctx, `
		INSERT INTO playlists (source, name, description)
		VALUES ('new_releases', $1, $2)
		ON CONFLICT ((source = 'new_releases')) WHERE source = 'new_releases' DO UPDATE SET
			updated_at = now()
		RETURNING`+playlistColumns+`,`+playlistEntryCounts,
		name, "New music from the artists and labels you follow, newest release first.",
	))
}

// NewReleasesPlaylist returns the new-releases playlist, or pgx.ErrNoRows
// before the first refresh has made one.
func (q *Queries) NewReleasesPlaylist(ctx context.Context) (PlaylistRow, error) {
	return scanPlaylist(q.db.QueryRow(ctx, `
		SELECT`+playlistColumns+`,`+playlistEntryCounts+`
		FROM playlists
		WHERE source = 'new_releases'
	`))
}

// NewReleaseCandidateRow is one track the library owns from a release the
// follow feed surfaced: the file, the release it belongs to, the day that
// release was published, and a name a person can read.
type NewReleaseCandidateRow struct {
	LibraryFileID uuid.UUID
	AlbumID       uuid.NullUUID
	ReleasedOn    pgtype.Date
	Title         string
}

// NewReleaseCandidates reads every track the library owns from a release the
// follow feed surfaced on or after the given day, newest release first.
//
// A release is one the feed surfaced when the feed made a want against it: the
// feed writes `origin_album_id` on every want it creates, either as
// `origin = 'follow_feed'` from a followed artist or `origin = 'label_feed'`
// from a followed label, and that pair is the only record that a release was
// found by following somebody rather than by a person asking for it.
//
// Ownership is read from both sides, because a release the feed found is
// usually owned from both. The first arm is the copy the acquisition loop
// fetched for one of those wants, recorded on the want itself. The second is
// the ordinary library mapping: a track on that release matched to a file, which
// is how the tracks the user already had are owned. Nothing here compares words
// or decides an identity — both arms read rows Schall wrote down itself.
//
// A file the library has lost is not owned. `missing_at` is set when a scan no
// longer finds the audio, and a playlist entry for it would be a track the
// player cannot play.
func (q *Queries) NewReleaseCandidates(
	ctx context.Context, releasedOnOrAfter time.Time,
) ([]NewReleaseCandidateRow, error) {
	rows, err := q.db.Query(ctx, `
		WITH feed_albums AS (
			SELECT DISTINCT albums.id, albums.release_date
			FROM acquisition_targets
			JOIN albums ON albums.id = acquisition_targets.origin_album_id
			WHERE acquisition_targets.origin IN ('follow_feed', 'label_feed')
			  AND albums.release_date IS NOT NULL
			  AND albums.release_date >= $1::date
		),
		owned AS (
			SELECT feed_albums.id AS album_id,
			       feed_albums.release_date,
			       library_files.id AS library_file_id,
			       coalesce(
			           nullif(btrim(library_files.title_tag), ''),
			           regexp_replace(library_files.path, '^.*/', '')
			       ) AS title
			FROM feed_albums
			JOIN acquisition_targets
			    ON acquisition_targets.origin_album_id = feed_albums.id
			   AND acquisition_targets.origin IN ('follow_feed', 'label_feed')
			JOIN library_files
			    ON library_files.id = acquisition_targets.acquired_library_file_id
			WHERE library_files.missing_at IS NULL

			UNION

			SELECT feed_albums.id,
			       feed_albums.release_date,
			       library_files.id,
			       coalesce(
			           nullif(btrim(tracks.title), ''),
			           nullif(btrim(library_files.title_tag), ''),
			           regexp_replace(library_files.path, '^.*/', '')
			       )
			FROM feed_albums
			JOIN tracks ON tracks.album_id = feed_albums.id
			JOIN track_mappings ON track_mappings.track_id = tracks.id
			JOIN library_files ON library_files.id = track_mappings.library_file_id
			WHERE library_files.missing_at IS NULL
		)
		SELECT DISTINCT ON (library_file_id)
		       library_file_id, album_id, release_date, title
		FROM owned
		ORDER BY library_file_id, release_date DESC
	`, releasedOnOrAfter)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]NewReleaseCandidateRow, 0, 64)
	for rows.Next() {
		var candidate NewReleaseCandidateRow
		if err := rows.Scan(
			&candidate.LibraryFileID, &candidate.AlbumID,
			&candidate.ReleasedOn, &candidate.Title,
		); err != nil {
			return nil, err
		}
		result = append(result, candidate)
	}
	return result, rows.Err()
}

// NewReleasePlaylistMemberRow is one file the refresh put on the list, and what
// has become of the entry it made.
type NewReleasePlaylistMemberRow struct {
	LibraryFileID uuid.UUID
	EntryID       uuid.NullUUID
	AlbumID       uuid.NullUUID
	ReleasedOn    pgtype.Date
	AddedAt       pgtype.Timestamptz
	WithdrawnAt   pgtype.Timestamptz
}

// NewReleasePlaylistMembers reads every member row, newest release first.
func (q *Queries) NewReleasePlaylistMembers(ctx context.Context) ([]NewReleasePlaylistMemberRow, error) {
	rows, err := q.db.Query(ctx, `
		SELECT library_file_id, playlist_entry_id, album_id,
		       released_on, added_at, withdrawn_at
		FROM new_release_playlist_members
		ORDER BY released_on DESC, added_at DESC, library_file_id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]NewReleasePlaylistMemberRow, 0, 64)
	for rows.Next() {
		var member NewReleasePlaylistMemberRow
		if err := rows.Scan(
			&member.LibraryFileID, &member.EntryID, &member.AlbumID,
			&member.ReleasedOn, &member.AddedAt, &member.WithdrawnAt,
		); err != nil {
			return nil, err
		}
		result = append(result, member)
	}
	return result, rows.Err()
}

// WithdrawRemovedNewReleaseMembers records the tracks somebody took out of this
// playlist in their player, and reports how many there were.
//
// It reads one fact and no more: a member row whose entry has gone while the
// refresh itself deleted nothing. The sync deletes the entry when it confirms a
// removal (ADR 0013) and the member row's reference falls to NULL with it. The
// refresh calls this first, before it removes anything of its own, so the only
// missing entries it can see are the ones somebody else took.
func (q *Queries) WithdrawRemovedNewReleaseMembers(ctx context.Context) (int64, error) {
	tag, err := q.db.Exec(ctx, `
		UPDATE new_release_playlist_members
		SET withdrawn_at = now()
		WHERE playlist_entry_id IS NULL AND withdrawn_at IS NULL
	`)
	if err != nil {
		return 0, fmt.Errorf("record the tracks taken out of the new-releases playlist: %w", err)
	}
	return tag.RowsAffected(), nil
}

// AddNewReleasePlaylistMemberParams is one owned track joining the list.
type AddNewReleasePlaylistMemberParams struct {
	PlaylistID    uuid.UUID
	LibraryFileID uuid.UUID
	AlbumID       uuid.NullUUID
	ReleasedOn    pgtype.Date
	Title         string
}

// AddNewReleasePlaylistMember puts one track on the list: the entry and the
// member row that outlives it, in one transaction. A crash between the two
// would leave an entry no refresh knows it made, which the next pass would read
// as a track somebody added by hand and never rotate out.
func (q *Queries) AddNewReleasePlaylistMember(
	ctx context.Context, params AddNewReleasePlaylistMemberParams,
) error {
	return q.inTransaction(ctx, "adding a new-releases playlist track", func(tx pgx.Tx) error {
		queries := q.WithTx(tx)
		entry, err := queries.AppendPlaylistEntry(ctx, AppendPlaylistEntryParams{
			PlaylistID:    params.PlaylistID,
			EntryTitle:    params.Title,
			LibraryFileID: params.LibraryFileID,
		})
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO new_release_playlist_members (
				library_file_id, playlist_entry_id, album_id, released_on
			)
			VALUES ($1, $2, $3, $4)
		`, params.LibraryFileID, entry.ID, params.AlbumID, params.ReleasedOn)
		return err
	})
}

// DropNewReleasePlaylistMember takes one track off the list because its release
// has moved out of the window, or because the library no longer holds it.
//
// The member row goes first and the entry second, in one transaction, and the
// order is the whole point: deleting the entry sets the member row's reference
// to NULL, and a member row with no entry is how a person's own removal is
// recognised. A member deleted first cannot be mistaken for one.
func (q *Queries) DropNewReleasePlaylistMember(ctx context.Context, libraryFileID uuid.UUID) error {
	return q.inTransaction(ctx, "removing a new-releases playlist track", func(tx pgx.Tx) error {
		var entryID uuid.NullUUID
		err := tx.QueryRow(ctx, `
			DELETE FROM new_release_playlist_members
			WHERE library_file_id = $1
			RETURNING playlist_entry_id
		`, libraryFileID).Scan(&entryID)
		if err != nil {
			return err
		}
		if !entryID.Valid {
			return nil
		}
		_, err = tx.Exec(ctx, `DELETE FROM playlist_entries WHERE id = $1`, entryID.UUID)
		return err
	})
}

// ForgetNewReleasePlaylistMembers drops every member row, withdrawals included.
//
// It is called in one situation: the playlist itself is gone. Deleting the list
// deleted its entries, so every member row is entryless and would otherwise be
// read as a removal by hand — a whole list of tracks pinned out of a playlist
// nobody meant to pin anything out of.
func (q *Queries) ForgetNewReleasePlaylistMembers(ctx context.Context) (int64, error) {
	tag, err := q.db.Exec(ctx, `DELETE FROM new_release_playlist_members`)
	if err != nil {
		return 0, fmt.Errorf("forget the new-releases playlist members: %w", err)
	}
	return tag.RowsAffected(), nil
}

// OrderNewReleasePlaylistEntries writes the running order: newest release
// first, and the most recently added track first within one day.
//
// Entries with no member row keep their place after those — a track somebody
// added to this list in their player is theirs, and the refresh neither rotates
// it out nor sorts it in among the ones it chose.
func (q *Queries) OrderNewReleasePlaylistEntries(ctx context.Context, playlistID uuid.UUID) error {
	_, err := q.db.Exec(ctx, `
		WITH ordered AS (
			SELECT playlist_entries.id,
			       row_number() OVER (
			           ORDER BY members.released_on DESC NULLS LAST,
			                    members.added_at DESC NULLS LAST,
			                    playlist_entries.position,
			                    playlist_entries.id
			       ) AS place
			FROM playlist_entries
			LEFT JOIN new_release_playlist_members members
			    ON members.playlist_entry_id = playlist_entries.id
			WHERE playlist_entries.playlist_id = $1
		)
		UPDATE playlist_entries SET position = ordered.place, updated_at = now()
		FROM ordered
		WHERE playlist_entries.id = ordered.id
		  AND playlist_entries.position <> ordered.place
	`, playlistID)
	if err != nil {
		return fmt.Errorf("order the new-releases playlist: %w", err)
	}
	return nil
}

// QueueNewReleasesPlaylistRefresh asks for one refresh of the list. The partial
// unique index from 00075 allows one queued or running refresh, so asking while
// one is waiting is answered by the one that already exists.
func (q *Queries) QueueNewReleasesPlaylistRefresh(ctx context.Context, runAfter time.Time) error {
	_, err := q.db.Exec(ctx, `
		INSERT INTO jobs (kind, payload, run_after, max_attempts)
		VALUES ('refresh_new_releases_playlist', '{}'::jsonb, $1, 1)
		ON CONFLICT (kind)
		    WHERE kind = 'refresh_new_releases_playlist' AND status IN ('queued', 'running')
		DO NOTHING
	`, runAfter)
	if err != nil {
		return fmt.Errorf("queue new releases playlist refresh: %w", err)
	}
	return nil
}
