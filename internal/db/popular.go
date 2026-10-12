package db

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// PopularTrackRow is what the catalogue says about one recording on an
// artist's popular list.
type PopularTrackRow struct {
	RecordingID uuid.UUID
	// The track and release that carry the recording here, when the catalogue
	// has one: this artist's own releases first, then one the library holds,
	// then one with a picture, then an album over a single.
	TrackID    uuid.NullUUID
	AlbumID    uuid.NullUUID
	AlbumTitle pgtype.Text
	HasCover   bool
	// Held is the recording proven on a library file, under any release.
	Held       bool
	WantID     uuid.NullUUID
	WantStatus pgtype.Text
}

// PopularTracks looks recordings up by MusicBrainz ID, in the order given. A
// recording the catalogue has no track for still comes back, with no track.
func (q *Queries) PopularTracks(ctx context.Context, artistID uuid.UUID, recordings []uuid.UUID) ([]PopularTrackRow, error) {
	rows, err := q.db.Query(ctx, `
		SELECT
			asked.recording_id,
			carrier.track_id,
			carrier.album_id,
			carrier.album_title,
			coalesce(carrier.has_cover, false)::boolean,
			EXISTS (
				SELECT 1
				FROM library_file_identities
				JOIN library_files ON library_files.id = library_file_identities.library_file_id
				WHERE library_file_identities.musicbrainz_recording_id = asked.recording_id
				  AND library_files.missing_at IS NULL
				UNION ALL
				SELECT 1
				FROM tracks
				JOIN track_mappings ON track_mappings.track_id = tracks.id
				JOIN library_files ON library_files.id = track_mappings.library_file_id
				WHERE tracks.musicbrainz_recording_id = asked.recording_id
				  AND library_files.missing_at IS NULL
			)::boolean,
			want.id,
			want.status
		FROM unnest($2::uuid[]) WITH ORDINALITY AS asked (recording_id, position)
		LEFT JOIN LATERAL (
			SELECT
				tracks.id AS track_id,
				albums.id AS album_id,
				albums.title AS album_title,
				EXISTS (
					SELECT 1 FROM release_cover_art
					WHERE release_cover_art.album_id = albums.id
					  AND release_cover_art.image IS NOT NULL
				) AS has_cover
			FROM tracks
			JOIN albums ON albums.id = tracks.album_id
			WHERE tracks.musicbrainz_recording_id = asked.recording_id
			ORDER BY
				(albums.artist_id = $1) DESC,
				(albums.owned_track_count > 0) DESC,
				has_cover DESC,
				(albums.album_type = 'album' AND albums.secondary_types = '{}') DESC,
				albums.release_date NULLS LAST,
				tracks.id
			LIMIT 1
		) carrier ON true
		LEFT JOIN acquisition_targets AS want
		    ON want.musicbrainz_recording_id = asked.recording_id
		   AND want.status <> 'superseded'
		ORDER BY asked.position
	`, artistID, recordings)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	found := make([]PopularTrackRow, 0, len(recordings))
	for rows.Next() {
		var row PopularTrackRow
		if err := rows.Scan(
			&row.RecordingID, &row.TrackID, &row.AlbumID, &row.AlbumTitle, &row.HasCover,
			&row.Held, &row.WantID, &row.WantStatus,
		); err != nil {
			return nil, err
		}
		found = append(found, row)
	}
	return found, rows.Err()
}
