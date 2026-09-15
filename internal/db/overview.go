package db

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// The Overview page's figures. Every day, month, weekday and hour is counted
// in the zone the browser sent, because "21:00" means nothing in UTC to a
// person in Vienna. Nothing here matches anything: it counts rows.
//
// The top lists group by the names a listen carries, not by the MusicBrainz
// identifiers: ListenBrainz maps some listens of a song and not others, and
// keying on the identifier first showed one artist twice on 2026-09-06. The
// identifier is kept as an attribute of the group (the most recent one wins).

type OverviewDay struct {
	Date  string
	Count int64
}

type OverviewPlayed struct {
	Title          string
	Artist         string
	Listens        int64
	RecordingMBID  pgtype.Text
	ReleaseMBID    pgtype.Text
	CAAReleaseMBID pgtype.Text
	TrackID        pgtype.UUID
	AlbumID        pgtype.UUID
	HasCover       bool
	FileID         pgtype.UUID
	InLibrary      bool
	Wanted         bool
}

type OverviewArtist struct {
	Name       string
	ArtistMBID pgtype.Text
	Listens    int64
	// ArtistID is the catalogue artist with a cached picture, found by the
	// MusicBrainz ID the listen carried or, without one, by the exact name.
	// For the picture only.
	ArtistID pgtype.UUID
}

type OverviewAlbum struct {
	Title          string
	Artist         string
	Listens        int64
	ReleaseMBID    pgtype.Text
	CAAReleaseMBID pgtype.Text
}

// OverviewSession is one stretch of continuous listening: listens less than
// sessionGap apart, in the order they were heard.
type OverviewSession struct {
	StartedAt time.Time
	EndedAt   time.Time
	// Ongoing is true while the last listen is less than sessionGap ago: the
	// next listen would still join this session.
	Ongoing bool
	Songs   int64
	// Artists in order of first appearance, one entry per name apart from case.
	Artists []string
	// Covers are the first three distinct pictures among the session's
	// listens, resolved the way a Most played row's is.
	Covers []SessionCover
}

// SessionCover is what a session's listen can be pictured by; the handler
// turns it into an address.
type SessionCover struct {
	AlbumID        pgtype.UUID
	HasCover       bool
	FileID         pgtype.UUID
	ReleaseMBID    pgtype.Text
	CAAReleaseMBID pgtype.Text
}

type OverviewMonth struct {
	Month string
	Files int64
}

type OverviewAdded struct {
	Title    string
	Artist   string
	AddedAt  time.Time
	TrackID  pgtype.UUID
	AlbumID  pgtype.UUID
	HasCover bool
	FileID   pgtype.UUID
}

type Overview struct {
	ListenCount   int64
	ListenDays    []OverviewDay
	ListensTotal  int64
	ListensBefore int64
	MostPlayed    []OverviewPlayed
	TopArtists    []OverviewArtist
	Heat          [7][24]int64
	TopAlbums     []OverviewAlbum
	Sessions      []OverviewSession
	ArrivedDays   []OverviewDay
	Downloading   int64
	FileCount     int64
	TotalBytes    int64
	Growth        []OverviewMonth
	RecentlyAdded []OverviewAdded
}

// ValidTimeZone says whether Postgres knows the zone by that name. An unknown
// one is answered with UTC rather than an error, so a browser with an odd
// setting still gets a page.
func (q *Queries) ValidTimeZone(ctx context.Context, zone string) bool {
	if zone == "" {
		return false
	}
	var probe time.Time
	err := q.db.QueryRow(ctx, `SELECT now() AT TIME ZONE $1`, zone).Scan(&probe)
	return err == nil
}

// Overview reads every figure the page shows, at the moment `now`, in `zone`.
func (q *Queries) Overview(ctx context.Context, zone string, now time.Time) (Overview, error) {
	var out Overview
	var err error

	if out.ListenCount, err = q.scalar(ctx, `SELECT count(*) FROM listens`); err != nil {
		return out, err
	}
	if out.ListenDays, err = q.dayCounts(ctx, zone, now, 30, `
		SELECT (listened_at AT TIME ZONE $1)::date AS day, count(*) AS n
		FROM listens WHERE listened_at >= $2::timestamptz - interval '32 days'
		GROUP BY 1`); err != nil {
		return out, err
	}
	for _, day := range out.ListenDays {
		out.ListensTotal += day.Count
	}
	err = q.db.QueryRow(ctx, `
		SELECT count(*) FROM listens
		WHERE (listened_at AT TIME ZONE $1)::date
			BETWEEN ($2::timestamptz AT TIME ZONE $1)::date - 59
			    AND ($2::timestamptz AT TIME ZONE $1)::date - 30`,
		zone, now).Scan(&out.ListensBefore)
	if err != nil {
		return out, fmt.Errorf("count the listens before: %w", err)
	}

	if out.MostPlayed, err = q.mostPlayed(ctx, zone, now); err != nil {
		return out, err
	}
	if out.TopArtists, err = q.topArtists(ctx, zone, now); err != nil {
		return out, err
	}
	if out.Heat, err = q.listenHeat(ctx, zone, now); err != nil {
		return out, err
	}
	if out.TopAlbums, err = q.topAlbums(ctx, zone, now); err != nil {
		return out, err
	}
	if out.Sessions, err = q.sessions(ctx, now); err != nil {
		return out, err
	}

	// Files that joined the library, whatever brought them: a download of
	// twelve songs is twelve arrivals, and an upload is one.
	if out.ArrivedDays, err = q.dayCounts(ctx, zone, now, 14, `
		SELECT (created_at AT TIME ZONE $1)::date AS day, count(*) AS n
		FROM library_files
		WHERE created_at >= $2::timestamptz - interval '16 days'
		GROUP BY 1`); err != nil {
		return out, err
	}
	// The same rule the dashboard badge uses: one per request not finished.
	if out.Downloading, err = q.scalar(ctx, `
		SELECT count(*) FROM download_requests WHERE status IN ('requested', 'started')`); err != nil {
		return out, err
	}

	err = q.db.QueryRow(ctx, `
		SELECT count(*), coalesce(sum(size_bytes), 0)::bigint
		FROM library_files WHERE missing_at IS NULL`).Scan(&out.FileCount, &out.TotalBytes)
	if err != nil {
		return out, fmt.Errorf("count the library: %w", err)
	}
	if out.Growth, err = q.libraryGrowth(ctx, zone, now); err != nil {
		return out, err
	}
	if out.RecentlyAdded, err = q.recentlyAdded(ctx); err != nil {
		return out, err
	}
	return out, nil
}

func (q *Queries) scalar(ctx context.Context, sql string) (int64, error) {
	var n int64
	if err := q.db.QueryRow(ctx, sql).Scan(&n); err != nil {
		return 0, fmt.Errorf("count for the overview: %w", err)
	}
	return n, nil
}

// dayCounts fills the last `days` local days, oldest first and today last,
// from a query that yields (day date, n) pairs for at least that range.
func (q *Queries) dayCounts(
	ctx context.Context, zone string, now time.Time, days int, counted string,
) ([]OverviewDay, error) {
	rows, err := q.db.Query(ctx, `
		WITH local AS (SELECT ($2::timestamptz AT TIME ZONE $1)::date AS today),
		counted AS (`+counted+`)
		SELECT to_char(d::date, 'YYYY-MM-DD'), coalesce(counted.n, 0)::bigint
		FROM local,
		     generate_series(local.today - ($3::int - 1), local.today, interval '1 day') AS d
		LEFT JOIN counted ON counted.day = d::date
		ORDER BY d`, zone, now, days)
	if err != nil {
		return nil, fmt.Errorf("count by day: %w", err)
	}
	defer rows.Close()
	out := make([]OverviewDay, 0, days)
	for rows.Next() {
		var day OverviewDay
		if err := rows.Scan(&day.Date, &day.Count); err != nil {
			return nil, fmt.Errorf("read a day: %w", err)
		}
		out = append(out, day)
	}
	return out, rows.Err()
}

// listRows is how many rows each list on the Overview carries. The page shows
// a few and scrolls the rest inside the panel.
const listRows = "25"

const thisMonth = `date_trunc('month', listened_at AT TIME ZONE $1) = date_trunc('month', $2::timestamptz AT TIME ZONE $1)`

func (q *Queries) mostPlayed(ctx context.Context, zone string, now time.Time) ([]OverviewPlayed, error) {
	rows, err := q.db.Query(ctx, `
		SELECT min(track_name), min(artist_name), count(*) AS n,
		       max(recording_mbid::text), max(release_mbid::text), max(caa_release_mbid::text)
		FROM listens
		WHERE `+thisMonth+`
		GROUP BY lower(artist_name), lower(track_name)
		ORDER BY n DESC, min(track_name)
		LIMIT `+listRows, zone, now)
	if err != nil {
		return nil, fmt.Errorf("read the most played: %w", err)
	}
	played := make([]OverviewPlayed, 0, 25)
	for rows.Next() {
		var row OverviewPlayed
		if err := rows.Scan(&row.Title, &row.Artist, &row.Listens,
			&row.RecordingMBID, &row.ReleaseMBID, &row.CAAReleaseMBID); err != nil {
			rows.Close()
			return nil, fmt.Errorf("read a most played row: %w", err)
		}
		played = append(played, row)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Whether the recording is a catalogue track, held, or wanted. Identity is
	// the MusicBrainz recording ID and nothing else; a listen without one is
	// simply not looked up.
	for i := range played {
		if !played[i].RecordingMBID.Valid {
			continue
		}
		err := q.db.QueryRow(ctx, `
			SELECT t.id, t.album_id,
			       EXISTS (SELECT 1 FROM track_mappings m
			               JOIN library_files f ON f.id = m.library_file_id
			               WHERE m.track_id = t.id AND f.missing_at IS NULL) AS in_library,
			       EXISTS (SELECT 1 FROM release_cover_art c
			               WHERE c.album_id = t.album_id AND c.image IS NOT NULL),
			       EXISTS (SELECT 1 FROM acquisition_targets w
			               WHERE w.musicbrainz_recording_id = t.musicbrainz_recording_id
			                 AND w.status IN ('unresolved', 'pending', 'searching'))
			FROM tracks t
			WHERE t.musicbrainz_recording_id = $1::uuid
			ORDER BY in_library DESC
			LIMIT 1`, played[i].RecordingMBID.String).Scan(
			&played[i].TrackID, &played[i].AlbumID, &played[i].InLibrary,
			&played[i].HasCover, &played[i].Wanted)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("look up a played recording: %w", err)
		}
	}
	for i := range played {
		if played[i].HasCover {
			continue
		}
		held, err := q.heldCopy(ctx, played[i].RecordingMBID, played[i].Title, played[i].Artist)
		if err != nil {
			return nil, err
		}
		if held.AlbumID.Valid {
			played[i].AlbumID, played[i].HasCover = held.AlbumID, held.HasCover
		}
		played[i].FileID = held.FileID
	}
	return played, nil
}

func (q *Queries) topArtists(ctx context.Context, zone string, now time.Time) ([]OverviewArtist, error) {
	rows, err := q.db.Query(ctx, `
		SELECT min(artist_name), count(*) AS n, max(artist_mbids[1]::text)
		FROM listens
		WHERE `+thisMonth+`
		GROUP BY lower(artist_name)
		ORDER BY n DESC, min(artist_name)
		LIMIT `+listRows, zone, now)
	if err != nil {
		return nil, fmt.Errorf("read the top artists: %w", err)
	}
	out := make([]OverviewArtist, 0, 25)
	for rows.Next() {
		var row OverviewArtist
		if err := rows.Scan(&row.Name, &row.Listens, &row.ArtistMBID); err != nil {
			rows.Close()
			return nil, fmt.Errorf("read a top artist: %w", err)
		}
		out = append(out, row)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		var mbid *string
		if out[i].ArtistMBID.Valid && out[i].ArtistMBID.String != "" {
			mbid = &out[i].ArtistMBID.String
		}
		err := q.db.QueryRow(ctx, `
			SELECT a.id FROM artists a
			JOIN artist_images i ON i.artist_id = a.id AND i.image IS NOT NULL
			WHERE a.musicbrainz_id = $1::uuid OR lower(a.name) = lower($2)
			ORDER BY (a.musicbrainz_id = $1::uuid) DESC NULLS LAST
			LIMIT 1`, mbid, out[i].Name).Scan(&out[i].ArtistID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("look up the picture of %q: %w", out[i].Name, err)
		}
	}
	return out, nil
}

func (q *Queries) listenHeat(ctx context.Context, zone string, now time.Time) ([7][24]int64, error) {
	var heat [7][24]int64
	rows, err := q.db.Query(ctx, `
		SELECT extract(isodow FROM (listened_at AT TIME ZONE $1))::int - 1,
		       extract(hour FROM (listened_at AT TIME ZONE $1))::int,
		       count(*)
		FROM listens
		WHERE (listened_at AT TIME ZONE $1)::date > ($2::timestamptz AT TIME ZONE $1)::date - 30
		  AND listened_at <= $2::timestamptz
		GROUP BY 1, 2`, zone, now)
	if err != nil {
		return heat, fmt.Errorf("read the listening hours: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var dow, hour int
		var n int64
		if err := rows.Scan(&dow, &hour, &n); err != nil {
			return heat, fmt.Errorf("read a listening hour: %w", err)
		}
		if dow >= 0 && dow < 7 && hour >= 0 && hour < 24 {
			heat[dow][hour] = n
		}
	}
	return heat, rows.Err()
}

func (q *Queries) topAlbums(ctx context.Context, zone string, now time.Time) ([]OverviewAlbum, error) {
	rows, err := q.db.Query(ctx, `
		SELECT min(release_name), min(artist_name), count(*) AS n,
		       max(release_mbid::text), max(caa_release_mbid::text)
		FROM listens
		WHERE release_name IS NOT NULL AND `+thisMonth+`
		GROUP BY lower(artist_name), lower(release_name)
		ORDER BY n DESC, min(release_name)
		LIMIT `+listRows, zone, now)
	if err != nil {
		return nil, fmt.Errorf("read the top albums: %w", err)
	}
	defer rows.Close()
	out := make([]OverviewAlbum, 0, 4)
	for rows.Next() {
		var row OverviewAlbum
		if err := rows.Scan(&row.Title, &row.Artist, &row.Listens, &row.ReleaseMBID, &row.CAAReleaseMBID); err != nil {
			return nil, fmt.Errorf("read a top album: %w", err)
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// sessionGap is the silence that ends a listening session. Thirty minutes is
// longer than any song and shorter than the break between two sittings.
const sessionGap = "30 minutes"

// sessionGapDuration is sessionGap for Go.
const sessionGapDuration = 30 * time.Minute

// sessionCoverListens bounds how many of a session's listens are looked at
// for its pictures: enough to find three distinct ones in a sitting that
// stays on one album for a while.
const sessionCoverListens = 8

// sessions groups the last thirty days of listens into sittings, newest
// first. A listen starts a new session when more than sessionGap passed since
// the one before it.
func (q *Queries) sessions(ctx context.Context, now time.Time) ([]OverviewSession, error) {
	rows, err := q.db.Query(ctx, `
		WITH marked AS (
			SELECT listened_at, artist_name,
			       CASE WHEN listened_at - lag(listened_at) OVER (ORDER BY listened_at)
			                 > interval '`+sessionGap+`' THEN 1 ELSE 0 END AS starts
			FROM listens
			WHERE listened_at > $1::timestamptz - interval '30 days'
		), numbered AS (
			SELECT listened_at, artist_name,
			       sum(starts) OVER (ORDER BY listened_at ROWS UNBOUNDED PRECEDING) AS session
			FROM marked
		)
		SELECT min(listened_at), max(listened_at), count(*),
		       array_agg(artist_name ORDER BY listened_at)
		FROM numbered
		GROUP BY session
		ORDER BY min(listened_at) DESC
		LIMIT `+listRows, now)
	if err != nil {
		return nil, fmt.Errorf("read the listening sessions: %w", err)
	}
	defer rows.Close()
	out := make([]OverviewSession, 0, 25)
	for rows.Next() {
		var row OverviewSession
		var artists []string
		if err := rows.Scan(&row.StartedAt, &row.EndedAt, &row.Songs, &artists); err != nil {
			return nil, fmt.Errorf("read a listening session: %w", err)
		}
		row.Artists = distinctNames(artists)
		row.Ongoing = now.Sub(row.EndedAt) < sessionGapDuration
		out = append(out, row)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		covers, err := q.sessionCovers(ctx, out[i].StartedAt, out[i].EndedAt)
		if err != nil {
			return nil, err
		}
		out[i].Covers = covers
	}
	return out, nil
}

// sessionCovers walks a session's first listens and keeps the first three
// that can be pictured, each picture once.
func (q *Queries) sessionCovers(ctx context.Context, from, to time.Time) ([]SessionCover, error) {
	rows, err := q.db.Query(ctx, `
		SELECT track_name, artist_name,
		       recording_mbid::text, release_mbid::text, caa_release_mbid::text
		FROM listens
		WHERE listened_at BETWEEN $1 AND $2
		ORDER BY listened_at
		LIMIT `+strconv.Itoa(sessionCoverListens), from, to)
	if err != nil {
		return nil, fmt.Errorf("read a session's listens: %w", err)
	}
	type listen struct {
		title, artist                  string
		recording, release, caaRelease pgtype.Text
	}
	var listens []listen
	for rows.Next() {
		var l listen
		if err := rows.Scan(&l.title, &l.artist, &l.recording, &l.release, &l.caaRelease); err != nil {
			rows.Close()
			return nil, fmt.Errorf("read a session listen: %w", err)
		}
		listens = append(listens, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	covers := make([]SessionCover, 0, 3)
	seen := make(map[string]bool, 3)
	for _, l := range listens {
		held, err := q.heldCopy(ctx, l.recording, l.title, l.artist)
		if err != nil {
			return nil, err
		}
		cover := SessionCover{
			AlbumID: held.AlbumID, HasCover: held.HasCover, FileID: held.FileID,
			ReleaseMBID: l.release, CAAReleaseMBID: l.caaRelease,
		}
		key := coverKey(cover)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		covers = append(covers, cover)
		if len(covers) == 3 {
			break
		}
	}
	return covers, nil
}

// coverKey names the picture a cover resolves to, in the handler's order of
// preference, so two listens of one album count once. Empty when nothing
// pictures the listen.
func coverKey(cover SessionCover) string {
	switch {
	case cover.HasCover && cover.AlbumID.Valid:
		return "album:" + uuid.UUID(cover.AlbumID.Bytes).String()
	case cover.CAAReleaseMBID.Valid && cover.CAAReleaseMBID.String != "":
		return "caa:" + cover.CAAReleaseMBID.String
	case cover.ReleaseMBID.Valid && cover.ReleaseMBID.String != "":
		return "caa:" + cover.ReleaseMBID.String
	case cover.AlbumID.Valid:
		return "album:" + uuid.UUID(cover.AlbumID.Bytes).String()
	case cover.FileID.Valid:
		return "file:" + uuid.UUID(cover.FileID.Bytes).String()
	}
	return ""
}

// distinctNames keeps the first spelling of each name, in order.
func distinctNames(names []string) []string {
	seen := make(map[string]bool, len(names))
	out := make([]string, 0, len(names))
	for _, name := range names {
		key := strings.ToLower(name)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, name)
	}
	return out
}

// heldCopy is the library's copy of a listened track, found for its picture
// and nothing else.
//
// A listen is a pair of names and, when the player knew them, MusicBrainz IDs.
// The recording ID finds the track outright. Without one, the file whose
// title and artist tags equal the listen's names is the file the player
// scrobbled, because the player read those names out of that file. Equality
// is exact apart from case; nothing here is scored or ranked by similarity,
// and nothing here reaches matching: the row found decides what picture sits
// beside a count, not what the file is.
type heldCopyRow struct {
	AlbumID  pgtype.UUID
	HasCover bool
	FileID   pgtype.UUID
}

func (q *Queries) heldCopy(ctx context.Context, recordingMBID pgtype.Text, title, artist string) (heldCopyRow, error) {
	var mbid *string
	if recordingMBID.Valid && recordingMBID.String != "" {
		mbid = &recordingMBID.String
	}
	var row heldCopyRow
	err := q.db.QueryRow(ctx, `
		SELECT t.album_id, f.id,
		       coalesce((SELECT c.image IS NOT NULL FROM release_cover_art c
		                 WHERE c.album_id = t.album_id), false) AS has_cover
		FROM library_files f
		LEFT JOIN track_mappings m ON m.library_file_id = f.id
		LEFT JOIN tracks t ON t.id = m.track_id
		WHERE f.missing_at IS NULL
		  AND (t.musicbrainz_recording_id = $1::uuid
		       OR (lower(f.title_tag) = lower($2) AND lower(f.artist_tag) = lower($3)))
		ORDER BY (t.musicbrainz_recording_id = $1::uuid) DESC NULLS LAST,
		         has_cover DESC, t.id IS NOT NULL DESC, f.path
		LIMIT 1`, mbid, title, artist).Scan(&row.AlbumID, &row.FileID, &row.HasCover)
	if errors.Is(err, pgx.ErrNoRows) {
		return heldCopyRow{}, nil
	}
	if err != nil {
		return heldCopyRow{}, fmt.Errorf("look up the held copy of %q: %w", title, err)
	}
	return row, nil
}

// libraryGrowth is how many files the library held at the end of each of the
// last twelve local months, the current month last. Files that have gone
// missing since are not counted in any month, because their rows carry no
// date of leaving.
func (q *Queries) libraryGrowth(ctx context.Context, zone string, now time.Time) ([]OverviewMonth, error) {
	rows, err := q.db.Query(ctx, `
		WITH local AS (SELECT date_trunc('month', ($2::timestamptz AT TIME ZONE $1))::date AS this_month)
		SELECT to_char(m, 'YYYY-MM'),
		       (SELECT count(*) FROM library_files f
		        WHERE f.missing_at IS NULL
		          AND (f.created_at AT TIME ZONE $1) < (m + interval '1 month'))::bigint
		FROM local,
		     generate_series(local.this_month - interval '11 months', local.this_month, interval '1 month') AS m
		ORDER BY m`, zone, now)
	if err != nil {
		return nil, fmt.Errorf("read the library growth: %w", err)
	}
	defer rows.Close()
	out := make([]OverviewMonth, 0, 12)
	for rows.Next() {
		var row OverviewMonth
		if err := rows.Scan(&row.Month, &row.Files); err != nil {
			return nil, fmt.Errorf("read a growth month: %w", err)
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (q *Queries) recentlyAdded(ctx context.Context) ([]OverviewAdded, error) {
	rows, err := q.db.Query(ctx, `
		SELECT coalesce(t.title, f.title_tag, ''), coalesce(a.name, f.artist_tag, ''),
		       f.created_at, t.id, al.id,
		       EXISTS (SELECT 1 FROM release_cover_art c WHERE c.album_id = al.id AND c.image IS NOT NULL),
		       f.id
		FROM library_files f
		LEFT JOIN track_mappings m ON m.library_file_id = f.id
		LEFT JOIN tracks t ON t.id = m.track_id
		LEFT JOIN albums al ON al.id = t.album_id
		LEFT JOIN artists a ON a.id = al.artist_id
		WHERE f.missing_at IS NULL
		ORDER BY f.created_at DESC
		LIMIT `+listRows)
	if err != nil {
		return nil, fmt.Errorf("read the recently added: %w", err)
	}
	defer rows.Close()
	out := make([]OverviewAdded, 0, 4)
	for rows.Next() {
		var row OverviewAdded
		if err := rows.Scan(&row.Title, &row.Artist, &row.AddedAt, &row.TrackID, &row.AlbumID, &row.HasCover, &row.FileID); err != nil {
			return nil, fmt.Errorf("read a recently added file: %w", err)
		}
		out = append(out, row)
	}
	return out, rows.Err()
}
