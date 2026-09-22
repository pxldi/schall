package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// PlaylistRow is one want list as it is stored.
type PlaylistRow struct {
	ID             uuid.UUID
	Source         string
	SourceID       pgtype.Text
	SourceRevision pgtype.Text
	Name           string
	Description    string
	OwnerName      string
	TrackCount     int32
	ImportedAt     pgtype.Timestamptz
	CreatedAt      pgtype.Timestamptz
	UpdatedAt      pgtype.Timestamptz

	// Counted over the entries, because the list view answers "how much of
	// this do I own" without loading anybody's entries. Ownership is asked of
	// both sides of the question — see answeringFile.
	EntryCount int64
	OwnedCount int64
}

const playlistColumns = `
	playlists.id, playlists.source, playlists.source_id, playlists.source_revision,
	playlists.name, playlists.description, playlists.owner_name, playlists.track_count,
	playlists.imported_at, playlists.created_at, playlists.updated_at
`

// answeringFile is the library file that answers one playlist entry, as a
// lateral over playlist_entries: one row when the user has the music, none
// when they do not.
//
// An entry is answered from either side of the same question. The ownership
// record is written when the list is imported and only re-asked there
// (internal/playlists/service.go), so it is the answer for a file that was
// already in the library — and it is silent about every want acquired since.
// The second arm reads that: entry to want to the copy Schall fetched, proved
// and imported, which recorded the library file it became. Nothing here
// compares anything. Both arms are rows Schall wrote down itself, and reading
// only the first is why a list could say "14 owned" over thirty-eight rows the
// user owns.
//
// Where a want was superseded the trail is followed to the survivor, the same
// walk PlaylistEntries makes: a superseded target is the same want under
// another row, and the copy was acquired on the survivor.
//
// More than one accepted file is not an ambiguity — every accepted row was
// proven to be that recording before it was accepted — so the most recently
// decided one is taken and the ordering settles no identity.
//
// AcquiredPlaylistTracks in navidrome_playlists.go asks this same question and
// needs the file's path and tags with it, so it carries its own copy of these
// two arms. The two must keep agreeing: a push sends what this counts.
const answeringFile = `
	SELECT library_files.id AS library_file_id, NULL::timestamptz AS decided_at
	FROM library_files
	WHERE library_files.id = playlist_entries.owned_library_file_id

	UNION ALL

	SELECT library_files.id, acquisition_target_files.decided_at
	FROM playlist_entry_targets
	JOIN acquisition_targets joined
	    ON joined.id = playlist_entry_targets.acquisition_target_id
	LEFT JOIN acquisition_targets survivor
	    ON survivor.id = joined.superseded_by_id
	JOIN acquisition_target_files
	    ON acquisition_target_files.acquisition_target_id = coalesce(survivor.id, joined.id)
	JOIN library_files
	    ON library_files.id = acquisition_target_files.library_file_id
	WHERE playlist_entry_targets.playlist_entry_id = playlist_entries.id
	  AND acquisition_target_files.verdict = 'accepted'

	ORDER BY decided_at DESC NULLS LAST, library_file_id
	LIMIT 1
`

// playlistEntryCounts counts a list and the answered part of it. The owned
// count joins answeringFile rather than testing the stored column, so the
// header of a page and the state on each of its rows are the same sentence
// asked twice and cannot disagree.
const playlistEntryCounts = `
	(SELECT count(*) FROM playlist_entries WHERE playlist_entries.playlist_id = playlists.id),
	(SELECT count(*) FROM playlist_entries
	 JOIN LATERAL (` + answeringFile + `) answering ON true
	 WHERE playlist_entries.playlist_id = playlists.id)
`

// UpsertSpotifyPlaylist records that a Spotify playlist is followed. Adding a
// list that is already followed is answered with the list, not refused; the
// name refreshes because Spotify's copy is authoritative for what it is called.
func (q *Queries) UpsertSpotifyPlaylist(ctx context.Context, sourceID, name string) (PlaylistRow, error) {
	return scanPlaylist(q.db.QueryRow(ctx, `
		INSERT INTO playlists (source, source_id, name)
		VALUES ('spotify', $1, $2)
		ON CONFLICT (source, source_id) WHERE source_id IS NOT NULL DO UPDATE SET
			name = excluded.name,
			updated_at = now()
		RETURNING`+playlistColumns+`,`+playlistEntryCounts,
		sourceID, name,
	))
}

// UpsertNavidromePlaylist records a playlist created in Navidrome as a want
// list of Schall's own (ADR 0008: a playlist is a want list with a source).
//
// Adopting one that is already adopted is answered with the list rather than
// refused, in the shape the Spotify upsert has: the remote object is
// authoritative for what it is called, and a second sweep must not make a
// second list out of the same playlist.
func (q *Queries) UpsertNavidromePlaylist(ctx context.Context, sourceID, name string) (PlaylistRow, error) {
	return scanPlaylist(q.db.QueryRow(ctx, `
		INSERT INTO playlists (source, source_id, name)
		VALUES ('navidrome', $1, $2)
		ON CONFLICT (source, source_id) WHERE source_id IS NOT NULL DO UPDATE SET
			name = excluded.name,
			updated_at = now()
		RETURNING`+playlistColumns+`,`+playlistEntryCounts,
		sourceID, name,
	))
}

// UpsertFilePlaylist records the list an imported file makes, keyed by the
// file's own name (00074). Importing the same file again is answered with the
// list it made last time, so a second import refreshes that list instead of
// building another beside it.
//
// The name is the one the person confirmed, which starts as the file's name
// and may be anything after that; source_id keeps the file name whatever they
// call the list.
func (q *Queries) UpsertFilePlaylist(ctx context.Context, sourceID, name string) (PlaylistRow, error) {
	return scanPlaylist(q.db.QueryRow(ctx, `
		INSERT INTO playlists (source, source_id, name)
		VALUES ('file', $1, $2)
		ON CONFLICT (source, source_id) WHERE source_id IS NOT NULL DO UPDATE SET
			name = excluded.name,
			updated_at = now()
		RETURNING`+playlistColumns+`,`+playlistEntryCounts,
		sourceID, name,
	))
}

// CompleteFilePlaylistImport writes what one finished file import learned.
//
// It is CompletePlaylistImport without a revision: a file has none to record,
// and writing an empty string would say the list had been read at a revision
// called "" rather than at none at all.
func (q *Queries) CompleteFilePlaylistImport(
	ctx context.Context, id uuid.UUID, name string, trackCount int32,
) (PlaylistRow, error) {
	return scanPlaylist(q.db.QueryRow(ctx, `
		UPDATE playlists SET
			name = $2,
			track_count = $3,
			imported_at = now(),
			updated_at = now()
		WHERE id = $1
		RETURNING`+playlistColumns+`,`+playlistEntryCounts,
		id, name, trackCount,
	))
}

// Playlist returns one playlist, or pgx.ErrNoRows.
func (q *Queries) Playlist(ctx context.Context, id uuid.UUID) (PlaylistRow, error) {
	return scanPlaylist(q.db.QueryRow(ctx, `
		SELECT`+playlistColumns+`,`+playlistEntryCounts+`
		FROM playlists
		WHERE playlists.id = $1
	`, id))
}

// Playlists lists every playlist, newest first.
func (q *Queries) Playlists(ctx context.Context) ([]PlaylistRow, error) {
	rows, err := q.db.Query(ctx, `
		SELECT`+playlistColumns+`,`+playlistEntryCounts+`
		FROM playlists
		ORDER BY playlists.created_at DESC, playlists.id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]PlaylistRow, 0, 16)
	for rows.Next() {
		playlist, err := scanPlaylist(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, playlist)
	}
	return result, rows.Err()
}

// DeletePlaylist forgets the list and its entries. The targets it created are
// deliberately left standing: deleting a playlist is not the statement "stop
// wanting this music" — that statement is made per target, on the target.
func (q *Queries) DeletePlaylist(ctx context.Context, id uuid.UUID) error {
	tag, err := q.db.Exec(ctx, `DELETE FROM playlists WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// CompletePlaylistImport writes what one finished import learned. The revision
// is written only here — after every entry landed — so a crash mid-import
// leaves the old revision in place and the next run does the work again
// rather than believing it already happened.
func (q *Queries) CompletePlaylistImport(
	ctx context.Context, id uuid.UUID, revision, name, description, ownerName string, trackCount int32,
) (PlaylistRow, error) {
	return scanPlaylist(q.db.QueryRow(ctx, `
		UPDATE playlists SET
			source_revision = $2,
			name = $3,
			description = $4,
			owner_name = $5,
			track_count = $6,
			imported_at = now(),
			updated_at = now()
		WHERE id = $1
		RETURNING`+playlistColumns+`,`+playlistEntryCounts,
		id, revision, name, description, ownerName, trackCount,
	))
}

func scanPlaylist(row pgx.Row) (PlaylistRow, error) {
	var result PlaylistRow
	err := row.Scan(
		&result.ID, &result.Source, &result.SourceID, &result.SourceRevision,
		&result.Name, &result.Description, &result.OwnerName, &result.TrackCount,
		&result.ImportedAt, &result.CreatedAt, &result.UpdatedAt,
		&result.EntryCount, &result.OwnedCount,
	)
	return result, err
}

// PlaylistEntryRow is one entry with what became of it: the file that already
// answers it, or the target pursuing it. Where the joined target was
// superseded, the survivor it points at is reported instead — the join keeps
// the trail, the reader wants the live want.
type PlaylistEntryRow struct {
	ID              uuid.UUID
	PlaylistID      uuid.UUID
	Position        int32
	SourceTrackID   pgtype.Text
	EntryArtist     string
	EntryTitle      string
	EntryAlbum      string
	EntryDurationMS pgtype.Int4
	EntryISRC       pgtype.Text

	// OwnedFileID is the stored ownership record verbatim: what the last
	// import wrote by identifier agreement, and nothing else. It is what the
	// importer compares against to decide whether to write again.
	OwnedFileID uuid.NullUUID
	// AnsweringFileID is whether the user has this music, from either side of
	// the question (answeringFile). Read it to say "owned"; it is filled by
	// PlaylistEntries alone, because it is a reading rather than a column and
	// the writes that return a row have not asked it.
	AnsweringFileID uuid.NullUUID

	TargetID      uuid.NullUUID
	TargetStatus  pgtype.Text
	TargetSummary pgtype.Text
}

// PlaylistEntry returns one entry by its own id, with the same reading
// PlaylistEntries gives each row: the file that answers it, or the target
// pursuing it.
func (q *Queries) PlaylistEntry(ctx context.Context, entryID uuid.UUID) (PlaylistEntryRow, error) {
	var entry PlaylistEntryRow
	err := q.db.QueryRow(ctx, `
		SELECT
			playlist_entries.id, playlist_entries.playlist_id, playlist_entries.position,
			playlist_entries.source_track_id, playlist_entries.entry_artist,
			playlist_entries.entry_title, playlist_entries.entry_album,
			playlist_entries.entry_duration_ms, playlist_entries.entry_isrc,
			playlist_entries.owned_library_file_id, answering.library_file_id,
			live_target.id, live_target.status, live_target.summary
		FROM playlist_entries
		LEFT JOIN LATERAL (`+answeringFile+`) answering ON true
		LEFT JOIN LATERAL (
			SELECT coalesce(survivor.id, joined.id) AS id,
			       coalesce(survivor.status, joined.status) AS status,
			       coalesce(survivor.summary, joined.summary) AS summary
			FROM playlist_entry_targets
			JOIN acquisition_targets joined
			    ON joined.id = playlist_entry_targets.acquisition_target_id
			LEFT JOIN acquisition_targets survivor
			    ON survivor.id = joined.superseded_by_id
			WHERE playlist_entry_targets.playlist_entry_id = playlist_entries.id
			ORDER BY playlist_entry_targets.created_at DESC
			LIMIT 1
		) live_target ON true
		WHERE playlist_entries.id = $1
	`, entryID).Scan(
		&entry.ID, &entry.PlaylistID, &entry.Position,
		&entry.SourceTrackID, &entry.EntryArtist,
		&entry.EntryTitle, &entry.EntryAlbum,
		&entry.EntryDurationMS, &entry.EntryISRC,
		&entry.OwnedFileID, &entry.AnsweringFileID,
		&entry.TargetID, &entry.TargetStatus, &entry.TargetSummary,
	)
	return entry, err
}

// PlaylistEntries returns a playlist's entries in order.
func (q *Queries) PlaylistEntries(ctx context.Context, playlistID uuid.UUID) ([]PlaylistEntryRow, error) {
	rows, err := q.db.Query(ctx, `
		SELECT
			playlist_entries.id, playlist_entries.playlist_id, playlist_entries.position,
			playlist_entries.source_track_id, playlist_entries.entry_artist,
			playlist_entries.entry_title, playlist_entries.entry_album,
			playlist_entries.entry_duration_ms, playlist_entries.entry_isrc,
			playlist_entries.owned_library_file_id, answering.library_file_id,
			live_target.id, live_target.status, live_target.summary
		FROM playlist_entries
		LEFT JOIN LATERAL (`+answeringFile+`) answering ON true
		LEFT JOIN LATERAL (
			SELECT coalesce(survivor.id, joined.id) AS id,
			       coalesce(survivor.status, joined.status) AS status,
			       coalesce(survivor.summary, joined.summary) AS summary
			FROM playlist_entry_targets
			JOIN acquisition_targets joined
			    ON joined.id = playlist_entry_targets.acquisition_target_id
			LEFT JOIN acquisition_targets survivor
			    ON survivor.id = joined.superseded_by_id
			WHERE playlist_entry_targets.playlist_entry_id = playlist_entries.id
			ORDER BY playlist_entry_targets.created_at DESC
			LIMIT 1
		) live_target ON true
		WHERE playlist_entries.playlist_id = $1
		ORDER BY playlist_entries.position, playlist_entries.id
	`, playlistID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]PlaylistEntryRow, 0, 64)
	for rows.Next() {
		var entry PlaylistEntryRow
		if err := rows.Scan(
			&entry.ID, &entry.PlaylistID, &entry.Position,
			&entry.SourceTrackID, &entry.EntryArtist,
			&entry.EntryTitle, &entry.EntryAlbum,
			&entry.EntryDurationMS, &entry.EntryISRC,
			&entry.OwnedFileID, &entry.AnsweringFileID,
			&entry.TargetID, &entry.TargetStatus, &entry.TargetSummary,
		); err != nil {
			return nil, err
		}
		result = append(result, entry)
	}
	return result, rows.Err()
}

// UpsertPlaylistEntryParams is one remote track as this import saw it.
type UpsertPlaylistEntryParams struct {
	PlaylistID      uuid.UUID
	Position        int32
	SourceTrackID   string
	EntryArtist     string
	EntryTitle      string
	EntryAlbum      string
	EntryDurationMS pgtype.Int4
	EntryISRC       pgtype.Text
	// EntryExplicit is the source saying this track is the explicit recording.
	// Null is silence. It is written and never read back here: what reads it is
	// the want the entry created (ADR 0032).
	EntryExplicit pgtype.Bool
}

// UpsertPlaylistEntry reconciles one entry by its source track identity. The
// evidence refreshes to what the source says now — the entry is a mirror of
// the remote row, unlike the target, which keeps what it was originally asked.
func (q *Queries) UpsertPlaylistEntry(ctx context.Context, params UpsertPlaylistEntryParams) (PlaylistEntryRow, error) {
	var entry PlaylistEntryRow
	err := q.db.QueryRow(ctx, `
		INSERT INTO playlist_entries (
			playlist_id, position, source_track_id,
			entry_artist, entry_title, entry_album, entry_duration_ms, entry_isrc,
			entry_explicit
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (playlist_id, source_track_id) WHERE source_track_id IS NOT NULL
		DO UPDATE SET
			position = excluded.position,
			entry_artist = excluded.entry_artist,
			entry_title = excluded.entry_title,
			entry_album = excluded.entry_album,
			entry_duration_ms = excluded.entry_duration_ms,
			entry_isrc = excluded.entry_isrc,
			entry_explicit = excluded.entry_explicit,
			updated_at = now()
		RETURNING id, playlist_id, position, source_track_id, entry_artist,
			entry_title, entry_album, entry_duration_ms, entry_isrc,
			owned_library_file_id
	`,
		params.PlaylistID, params.Position, params.SourceTrackID,
		params.EntryArtist, params.EntryTitle, params.EntryAlbum,
		params.EntryDurationMS, params.EntryISRC, params.EntryExplicit,
	).Scan(
		&entry.ID, &entry.PlaylistID, &entry.Position, &entry.SourceTrackID,
		&entry.EntryArtist, &entry.EntryTitle, &entry.EntryAlbum,
		&entry.EntryDurationMS, &entry.EntryISRC, &entry.OwnedFileID,
	)
	if err != nil {
		return PlaylistEntryRow{}, err
	}
	return entry, nil
}

// AppendPlaylistEntryParams is one track added to a list by something other
// than an import: a song the user put in the playlist on the player's side.
//
// It names a file rather than a want. The entry exists because that file is
// already in the library, so there is nothing to search for and nothing to
// resolve, and the title is carried only so the entry can be read by a person —
// the answer to what it is has been given by the file.
type AppendPlaylistEntryParams struct {
	PlaylistID    uuid.UUID
	EntryTitle    string
	LibraryFileID uuid.UUID
}

// AppendPlaylistEntry adds one entry at the end of a list.
//
// The position is computed here rather than passed in, because the caller is
// adding to whatever the list currently holds and two callers deciding that for
// themselves would collide. It carries no source_track_id: nothing on the
// remote list this came from is a stable track identity, and inventing one
// would make the next import believe a Spotify track had vanished.
func (q *Queries) AppendPlaylistEntry(
	ctx context.Context, params AppendPlaylistEntryParams,
) (PlaylistEntryRow, error) {
	var entry PlaylistEntryRow
	err := q.db.QueryRow(ctx, `
		INSERT INTO playlist_entries (
			playlist_id, position, entry_title, owned_library_file_id
		)
		VALUES (
			$1,
			(SELECT coalesce(max(position), 0) + 1 FROM playlist_entries WHERE playlist_id = $1),
			$2, $3
		)
		RETURNING id, playlist_id, position, source_track_id, entry_artist,
			entry_title, entry_album, entry_duration_ms, entry_isrc,
			owned_library_file_id
	`, params.PlaylistID, params.EntryTitle, params.LibraryFileID).Scan(
		&entry.ID, &entry.PlaylistID, &entry.Position, &entry.SourceTrackID,
		&entry.EntryArtist, &entry.EntryTitle, &entry.EntryAlbum,
		&entry.EntryDurationMS, &entry.EntryISRC, &entry.OwnedFileID,
	)
	if err != nil {
		return PlaylistEntryRow{}, err
	}
	return entry, nil
}

// DeletePlaylistEntry takes one track off a list. The want it created is left
// standing, for the reason PrunePlaylistEntries leaves it: removing a track
// from a playlist is not the statement "stop trying to obtain this recording",
// and the music it already found stays in the library either way.
func (q *Queries) DeletePlaylistEntry(ctx context.Context, entryID uuid.UUID) error {
	tag, err := q.db.Exec(ctx, `DELETE FROM playlist_entries WHERE id = $1`, entryID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// PrunePlaylistEntries removes the entries whose source track disappeared from
// the remote list. Their join rows go with them; the targets stay untouched,
// because removing a track from a playlist is not the statement "stop trying
// to obtain this recording".
func (q *Queries) PrunePlaylistEntries(ctx context.Context, playlistID uuid.UUID, keepTrackIDs []string) (int64, error) {
	tag, err := q.db.Exec(ctx, `
		DELETE FROM playlist_entries
		WHERE playlist_id = $1
		  AND source_track_id IS NOT NULL
		  AND NOT (source_track_id = ANY($2))
	`, playlistID, keepTrackIDs)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// MarkPlaylistEntryOwned records the file that already answers this entry, or
// clears the record when it stopped being true.
func (q *Queries) MarkPlaylistEntryOwned(ctx context.Context, entryID uuid.UUID, fileID uuid.NullUUID) error {
	tag, err := q.db.Exec(ctx, `
		UPDATE playlist_entries SET
			owned_library_file_id = $2,
			updated_at = now()
		WHERE id = $1
	`, entryID, fileID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// CreatePlaylistEntryTarget records a want and joins it to the entry that
// asked for it, in one transaction: a crash between the two would leave a
// target no entry can see, and the next import — finding no join — would ask
// for the same music a second time.
func (q *Queries) CreatePlaylistEntryTarget(
	ctx context.Context, entryID uuid.UUID, params CreateAcquisitionTargetParams,
) (uuid.UUID, error) {
	var targetID uuid.UUID
	err := q.inTransaction(ctx, "creating a playlist entry want", func(tx pgx.Tx) error {
		queries := q.WithTx(tx)
		id, _, err := queries.CreateAcquisitionTarget(ctx, params)
		if err != nil {
			return err
		}
		if err := queries.LinkPlaylistEntryTarget(ctx, entryID, id); err != nil {
			return err
		}
		targetID = id
		return nil
	})
	if err != nil {
		return uuid.Nil, err
	}
	return targetID, nil
}

// LinkPlaylistEntryTarget joins an entry to the want that serves it. Linking
// twice is one link.
func (q *Queries) LinkPlaylistEntryTarget(ctx context.Context, entryID, targetID uuid.UUID) error {
	_, err := q.db.Exec(ctx, `
		INSERT INTO playlist_entry_targets (playlist_entry_id, acquisition_target_id)
		VALUES ($1, $2)
		ON CONFLICT DO NOTHING
	`, entryID, targetID)
	if err != nil {
		return fmt.Errorf("link playlist entry %s to target %s: %w", entryID, targetID, err)
	}
	return nil
}

// PlaylistEntryTargetID reports the most recent want joined to this entry,
// invalid when the entry has never created one.
func (q *Queries) PlaylistEntryTargetID(ctx context.Context, entryID uuid.UUID) (uuid.NullUUID, error) {
	var targetID uuid.NullUUID
	err := q.db.QueryRow(ctx, `
		SELECT acquisition_target_id
		FROM playlist_entry_targets
		WHERE playlist_entry_id = $1
		ORDER BY created_at DESC
		LIMIT 1
	`, entryID).Scan(&targetID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.NullUUID{}, nil
	}
	return targetID, err
}

// QueuePlaylistImport asks for one import of this playlist. The partial
// unique index allows one live import per list, so asking while one is queued
// or running is answered by the import that already exists.
func (q *Queries) QueuePlaylistImport(ctx context.Context, playlistID uuid.UUID) error {
	_, err := q.db.Exec(ctx, `
		INSERT INTO jobs (kind, payload, max_attempts)
		VALUES ('import_playlist', jsonb_build_object('playlistId', $1::uuid), 3)
		ON CONFLICT (kind, (payload ->> 'playlistId'))
		    WHERE kind = 'import_playlist' AND status IN ('queued', 'running')
		DO NOTHING
	`, playlistID)
	return err
}

// OwnedFileForISRC answers whether a present library file is identified by
// this ISRC — through its resolved identity or through the catalogue track it
// is mapped to. ISRC agreement is an identifier agreement, the same proof the
// matching rules accept; nothing here compares words.
func (q *Queries) OwnedFileForISRC(ctx context.Context, isrc string) (uuid.NullUUID, error) {
	var fileID uuid.NullUUID
	err := q.db.QueryRow(ctx, `
		SELECT library_files.id
		FROM library_files
		LEFT JOIN library_file_identities
		    ON library_file_identities.library_file_id = library_files.id
		LEFT JOIN track_mappings ON track_mappings.library_file_id = library_files.id
		LEFT JOIN tracks ON tracks.id = track_mappings.track_id
		WHERE library_files.missing_at IS NULL
		  AND (upper(library_file_identities.isrc) = upper($1)
		       OR upper(tracks.isrc) = upper($1))
		ORDER BY library_files.created_at, library_files.id
		LIMIT 1
	`, isrc).Scan(&fileID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.NullUUID{}, nil
	}
	return fileID, err
}
