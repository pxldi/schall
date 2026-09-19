package db

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// The releases list is the one catalogue query sqlc cannot hold. The browser
// sorts on a column the user picks, in a direction the user picks, and an
// ORDER BY built from a CASE is an expression no index matches — which would
// undo the point of paging it at all. So the clause is assembled here from a
// fixed vocabulary instead, the way ListLibraryFiles already assembles its own.
// Nothing the user typed reaches the SQL text: values travel as parameters, and
// scope, status and ordering are looked up in the tables below and rejected
// when they are not there.

type AlbumRow struct {
	ID                        uuid.UUID
	ArtistID                  uuid.UUID
	ArtistName                string
	ArtistFollowed            bool
	MusicbrainzReleaseGroupID uuid.NullUUID
	Title                     string
	ReleaseDate               pgtype.Date
	AlbumType                 string
	FirstReleaseDate          string
	TrackCount                int64
	OwnedTrackCount           int64
	// DismissedTrackCount is the tracks the user decided not to want and does
	// not own; they leave the completion denominator rather than being missing.
	DismissedTrackCount int64
	// Monitored says whether the artist's monitor level counts this release as
	// part of "complete". An unmonitored release stays browsable and wantable
	// by hand; it just stops being missing.
	Monitored bool
	// MonitorLevel is the artist's level, carried per row so every page
	// computes the same denominator the SQL does — at 'owned' the countable
	// tracks are the owned ones, not track_count minus dismissals.
	MonitorLevel           string
	MusicbrainzReleaseID   uuid.NullUUID
	EditionSelectionReason string
	TrackRefreshStatus     string
	// HasCover says whether a picture of this release is cached. A list row
	// asks for the picture only when it is, so a release nobody has pictured
	// costs the browser no request. A recorded absence counts as no cover.
	HasCover bool
}

type ListAlbumsParams struct {
	ArtistID uuid.NullUUID
	// Scope is why a release is worth showing: empty for the whole catalogue,
	// "followed" for artists the user keeps complete, "library" for those plus
	// whatever the library already holds a track of.
	Scope string
	Query string
	// Status is the shape a release is in, named as the browser names it.
	Status string
	// Completeness narrows to the releases that count towards an artist being
	// complete, and is how an artist's page asks for its own two chips. It is
	// not Status by another name: Status answers what shape a release is in,
	// where this answers whether it is part of what the user asked to keep
	// complete and is therefore monitor-level aware. See albumCompleteness.
	Completeness string
	// Sort and Descending are the ordering, which has to be one an index can
	// walk; see albumOrderings.
	Sort       string
	Descending bool
	// A Limit of zero asks for every release the filters reach, which is what
	// the views that answer questions about the whole catalogue still need.
	Limit  int32
	Offset int32
}

type AlbumPage struct {
	Items []AlbumRow
	// Total counts what the filters reach, past the end of the page.
	Total int64
	// ScopeTotal counts the same before Status narrows it, so a page can say how
	// much of the list a status filter is holding back.
	ScopeTotal int64
	// StatusTotals counts what each status would reach, keyed as
	// albumStatusFilters keys them. One pass answers all of them, so the browser
	// can say how many releases each chip holds without asking again.
	StatusTotals map[string]int64
}

// What shape a release is in is arithmetic on the release's own row. The three
// counts are maintained by the triggers in migration 00040 — a release says how
// many tracks it has, how many the library can play today, and how many the
// user decided not to want — so deciding which releases to show reads one row
// each instead of asking about every track, mapping, identity and file in the
// catalogue. That is what lets a status-filtered count be one pass.
//
// They are display counts and this is the only thing that reads them. Nothing
// that matches, grades or admits a file may: ownership is still proved one
// track at a time, where it was always proved.
const (
	albumHasTracks = `albums.track_count > 0`
	albumHasOwned  = `albums.owned_track_count > 0`
	// Unowned means still worth getting: not owned and not dismissed. A release
	// whose every gap was dismissed has nothing unowned in this sense. A track
	// is only counted dismissed while it is not owned, so the three counts
	// never overlap and the subtraction is the tracks still to get.
	albumHasUnowned = `(albums.track_count - albums.owned_track_count - albums.dismissed_track_count) > 0`

	// albumCountable is the denominator the browser puts under owned_track_count
	// on every row: dismissals leave it, and an artist watched only for what is
	// already owned has no gaps to count. Ordering by it means ordering by the
	// fraction the page actually shows.
	albumCountable = `CASE artists.monitor_level
			WHEN 'owned' THEN albums.owned_track_count
			ELSE albums.track_count - albums.dismissed_track_count
		END`

	// albumMonitored is whether the artist's monitor level counts this release as
	// part of "complete". It is written once because two things read it — the
	// column every release page carries, and the completeness an artist's page
	// states — and a second copy would drift the moment a level was added.
	albumMonitored = `CASE artists.monitor_level
			WHEN 'everything' THEN true
			WHEN 'main' THEN albums.album_type IN ('album', 'ep', 'single')
				AND NOT (albums.secondary_types
					&& '{live,compilation,remix,dj-mix,mixtape/street,demo,broadcast}'::text[])
			WHEN 'albums_eps' THEN albums.album_type IN ('album', 'ep')
				AND NOT (albums.secondary_types
					&& '{live,compilation,remix,dj-mix,mixtape/street,demo,broadcast}'::text[])
			ELSE albums.owned_track_count > 0
		END`
)

// How a release's metadata refresh went stays an existence test: it is read
// from jobs and release_editions, and no column on albums stands in for it.
// The lateral below answers the only two questions the filters ask of it.
//
//	settled — an edition has been imported, or nothing about this release is
//	          queued, running or failed
//	failed  — no edition, a refresh that failed, and nothing still trying
//
// Both start from whether an edition landed, because that answers them without
// reading jobs at all for a release whose refresh finished — which is most of
// them. The two OFFSET 0 are optimisation fences, and they are the point of
// writing it this way: without them the planner inlines the lateral into each
// of the six counts in turn and every release is looked up six times over. With
// them the lookup happens once a release, and counting all six shapes costs
// 122ms over 60k releases against 250ms inlined.
func albumRefreshIs(statuses string) string {
	return `EXISTS (
			SELECT 1 FROM jobs AS scoped_job
			WHERE scoped_job.kind = 'refresh_album_metadata'
			  AND scoped_job.payload->>'albumId' = albums.id::text
			  AND scoped_job.status IN (` + statuses + `)
		)`
}

var albumRefreshLateral = `
	CROSS JOIN LATERAL (
		SELECT edition.has_edition
		         OR NOT ` + albumRefreshIs(`'queued', 'running', 'failed'`) + ` AS settled,
		       NOT edition.has_edition
		         AND ` + albumRefreshIs(`'failed'`) + `
		         AND NOT ` + albumRefreshIs(`'queued', 'running'`) + ` AS failed
		FROM (
			SELECT EXISTS (
				SELECT 1 FROM release_editions AS scoped_edition
				WHERE scoped_edition.album_id = albums.id
			) AS has_edition
			OFFSET 0
		) AS edition
		OFFSET 0
	) AS refresh
`

const (
	albumRefreshSettled = `refresh.settled`
	albumRefreshFailed  = `refresh.failed`
)

// albumStatusFilters name the same shapes the browser shows. The order the
// browser reads them in matters and is kept here: a release whose refresh
// failed is a problem whatever its tracks say, and one still refreshing is
// not yet any of the others — so every shape but "failed" also asks that
// the refresh has settled.
// "owned" asks for something owned as well as nothing left unowned, and
// "missing" asks for something still unowned, because dismissals opened a
// gap between the two: a release whose every track was dismissed owns
// nothing and is missing nothing, and it belongs to neither shape.
var albumStatusFilters = map[string]string{
	"owned":     albumRefreshSettled + ` AND ` + albumHasOwned + ` AND NOT ` + albumHasUnowned,
	"partial":   albumRefreshSettled + ` AND ` + albumHasOwned + ` AND ` + albumHasUnowned,
	"missing":   albumRefreshSettled + ` AND NOT ` + albumHasOwned + ` AND ` + albumHasUnowned,
	"untracked": albumRefreshSettled + ` AND NOT ` + albumHasTracks,
	"failed":    albumRefreshFailed,
}

// albumStatusOrder is the order the browser shows the chips in, and the order
// their counts are read back in.
var albumStatusOrder = []string{"owned", "partial", "missing", "untracked", "failed"}

// albumOrderings are the orders the list can be asked for. The first three are
// each a btree index's key in the order that index holds it, so a page is a
// walk that stops at its own end rather than a sort of the catalogue; see
// migration 00022.
//
// Ascending puts an unknown release date last, which is what ASC means by
// default and therefore what the index already holds. Descending puts it first,
// which is what reading the same index backwards gives. Neither is worth a
// second index to change.
//
// "owned" is the exception and has no index. It is how complete a release is,
// which is a fraction of two columns that move whenever the library does, and
// an index on it would be rewritten by every mapping, every scan and every
// dismissal — the write cost of the counts again, to save a sort. What it costs
// instead is a top-N over the filtered set, which is bounded work now that
// deciding the set no longer reads the catalogue: 60ms over 60k releases
// against 12ms for an indexed order. A release with nothing to count sorts as
// -1, which puts it first ascending and last descending — where the browser put
// it before paging took the ordering away.
var albumOrderings = map[string][]string{
	"year":   {"albums.release_date", "albums.title", "albums.id"},
	"title":  {"albums.title", "albums.id"},
	"artist": {"artists.name", "albums.release_date", "albums.id"},
	"owned": {
		`COALESCE(albums.owned_track_count::numeric / NULLIF(` + albumCountable + `, 0), -1)`,
		"albums.title",
		"albums.id",
	},
}

// albumFilter is the WHERE that the page and the counts share. It appends the
// values it needs to args and numbers its placeholders after whatever is there.
//
// Completeness belongs here rather than beside Status because it narrows what
// the list is of, the way a scope does, rather than picking one shape out of
// it. So the totals are counted inside it, and a page of it is a page of the
// releases that count.
func albumFilter(params ListAlbumsParams, args *[]any) string {
	conditions := make([]string, 0, 4)
	if params.Completeness != "" {
		conditions = append(conditions, albumCompleteness[params.Completeness])
	}
	if params.ArtistID.Valid {
		*args = append(*args, params.ArtistID.UUID)
		conditions = append(conditions, fmt.Sprintf("albums.artist_id = $%d", len(*args)))
	}
	switch params.Scope {
	case "followed":
		conditions = append(conditions, "artists.followed_at IS NOT NULL")
	case "library":
		conditions = append(conditions, "(artists.followed_at IS NOT NULL OR "+albumHasOwned+")")
	}
	if params.Query != "" {
		*args = append(*args, params.Query)
		// `%` and `_` mean something inside an ILIKE pattern, so a query holding
		// either has to be escaped to match itself, and so does the escape
		// character or it would eat the escapes put in beside it. One regexp pass
		// over all three is what keeps that from depending on the order. The shape
		// is ListArtists' (queries/artists.sql); this was the last search still
		// treating both as wildcards.
		conditions = append(conditions, fmt.Sprintf(
			`concat_ws(' ', albums.title, artists.name)`+
				` ILIKE '%%' || regexp_replace($%d::text, '([\\%%_])', '\\\1', 'g') || '%%'`+
				` ESCAPE '\'`, len(*args)))
	}
	if len(conditions) == 0 {
		return "true"
	}
	return strings.Join(conditions, "\n\t\t  AND ")
}

func albumOrder(params ListAlbumsParams) (string, error) {
	sort := params.Sort
	if sort == "" {
		sort = "year"
	}
	keys, ok := albumOrderings[sort]
	if !ok {
		return "", fmt.Errorf("db: unknown release ordering %q", params.Sort)
	}
	if !params.Descending {
		return strings.Join(keys, ", "), nil
	}
	descending := make([]string, len(keys))
	for index, key := range keys {
		descending[index] = key + " DESC"
	}
	return strings.Join(descending, ", "), nil
}

// albumColumns is one release and everything that has to be joined to describe
// it. Refresh status is an aggregate over rows that fan out per past refresh,
// which is why the page decides which releases it holds before any of this
// runs. The track counts used to fan out the same way, over every track and
// mapping of every release on the page; they are columns now.
//
// Where the page is joined in matters: an inner join written after the outer
// ones cannot always be reordered before them, so a `JOIN page` at the end left
// Postgres joining every track and every past refresh in the catalogue first
// and cutting to the page afterwards — 1.4s a page, the exact failure this was
// written to avoid. The page is the first thing in the FROM instead.
var albumColumns = `
	SELECT
		albums.id,
		albums.artist_id,
		artists.name AS artist_name,
		(artists.followed_at IS NOT NULL)::boolean AS artist_followed,
		albums.musicbrainz_release_group_id,
		albums.title,
		albums.release_date,
		albums.album_type,
		COALESCE(metadata_sources.metadata->>'first-release-date', '')::text AS first_release_date,
		albums.track_count,
		albums.owned_track_count,
		albums.dismissed_track_count,
		` + albumMonitored + `::boolean AS monitored,
		artists.monitor_level,
		release_editions.musicbrainz_release_id,
		COALESCE(release_editions.selection_reason, '')::text AS edition_selection_reason,
		CASE
			WHEN release_editions.id IS NOT NULL THEN 'completed'
			WHEN bool_or(jobs.status = 'running') THEN 'running'
			WHEN bool_or(jobs.status = 'queued') THEN 'queued'
			WHEN bool_or(jobs.status = 'failed') THEN 'failed'
			ELSE 'pending'
		END::text AS track_refresh_status,
		EXISTS (
			SELECT 1 FROM release_cover_art
			WHERE release_cover_art.album_id = albums.id
			  AND release_cover_art.image IS NOT NULL
		)::boolean AS has_cover
`

const albumJoins = `
	JOIN artists ON artists.id = albums.artist_id
	LEFT JOIN metadata_sources
	    ON metadata_sources.entity_type = 'album'
	   AND metadata_sources.entity_id = albums.id
	   AND metadata_sources.provider = 'musicbrainz'
	LEFT JOIN release_editions ON release_editions.album_id = albums.id
	LEFT JOIN jobs
	    ON jobs.kind = 'refresh_album_metadata'
	   AND jobs.payload->>'albumId' = albums.id::text
`

const albumGrouping = `
	GROUP BY albums.id, artists.name, artists.followed_at, artists.monitor_level,
	         metadata_sources.metadata, release_editions.id
	ORDER BY `

// ListAlbums returns one page of the catalogue's releases and how many the
// filters reach beyond it.
//
// The page is chosen before it is described. LIMIT bounds the rows a query
// returns, but this list groups over joins that fan out per track and per past
// refresh, so a LIMIT on the grouped query would still build every group in the
// catalogue before throwing all but one page of them away. Choosing the page's
// releases first — from albums and artists alone, in an order an index holds —
// and aggregating only those makes the second half of the work proportional to
// the page rather than to the catalogue.
func (q *Queries) ListAlbums(ctx context.Context, params ListAlbumsParams) (AlbumPage, error) {
	var page AlbumPage

	switch params.Scope {
	case "", "followed", "library":
	default:
		return page, fmt.Errorf("db: unknown release scope %q", params.Scope)
	}
	// The refresh lateral is joined only where something reads it. A page in an
	// order the catalogue is indexed by stops at its own end and never asks how
	// any release's refresh went; joining it anyway would have every release the
	// sort walks past looked up first — 75ms rather than 23ms to order 60k
	// releases by how complete they are.
	statusFilter, statusJoin := "true", ""
	if params.Status != "" {
		predicate, ok := albumStatusFilters[params.Status]
		if !ok {
			return page, fmt.Errorf("db: unknown release status %q", params.Status)
		}
		statusFilter, statusJoin = predicate, albumRefreshLateral
	}
	// Completeness reads how the refresh went too, so it needs the same lateral
	// even when no status was asked for.
	if params.Completeness != "" {
		if _, ok := albumCompleteness[params.Completeness]; !ok {
			return page, fmt.Errorf("db: unknown release completeness %q", params.Completeness)
		}
		statusJoin = albumRefreshLateral
	}
	order, err := albumOrder(params)
	if err != nil {
		return page, err
	}

	args := make([]any, 0, 4)
	filter := albumFilter(params, &args)

	// A page has to say what it is a page of, so the counts are asked for
	// separately. An unpaged request is mostly its own answer and is not
	// counted again — except when a status narrows it, because how much that
	// status is holding back is a question the returned rows cannot answer.
	//
	// Every status is counted, not only the one asked for. Each is now a
	// comparison of three columns on the row already being read, so all six
	// answers cost the one pass the scope total costs on its own — and the
	// browser's chips can say how many releases each of them holds.
	counted := params.Limit > 0 || params.Status != ""
	if counted {
		counts := make([]string, len(albumStatusOrder))
		totals := make([]int64, len(albumStatusOrder))
		into := make([]any, 0, len(albumStatusOrder)+1)
		into = append(into, &page.ScopeTotal)
		for index, status := range albumStatusOrder {
			counts[index] = "count(*) FILTER (WHERE " + albumStatusFilters[status] + ")::bigint"
			into = append(into, &totals[index])
		}
		if err := q.db.QueryRow(ctx, `
			SELECT count(*)::bigint,
			       `+strings.Join(counts, ",\n\t\t\t       ")+`
			FROM albums
			JOIN artists ON artists.id = albums.artist_id`+albumRefreshLateral+`
			WHERE `+filter+`
		`, args...).Scan(into...); err != nil {
			return page, err
		}
		page.StatusTotals = make(map[string]int64, len(albumStatusOrder))
		for index, status := range albumStatusOrder {
			page.StatusTotals[status] = totals[index]
		}
		page.Total = page.ScopeTotal
		if params.Status != "" {
			page.Total = page.StatusTotals[params.Status]
		}
	}

	query := albumColumns + "\tFROM albums" + albumJoins + statusJoin +
		"\tWHERE " + filter + "\n\t  AND " + statusFilter
	if params.Limit > 0 {
		args = append(args, params.Limit, params.Offset)
		query = fmt.Sprintf(`
	WITH page AS (
		SELECT albums.id
		FROM albums
		JOIN artists ON artists.id = albums.artist_id%s
		WHERE %s
		  AND %s
		ORDER BY %s
		LIMIT $%d OFFSET $%d
	)`, statusJoin, filter, statusFilter, order, len(args)-1, len(args)) +
			albumColumns + "\tFROM page\n\tJOIN albums ON albums.id = page.id" + albumJoins
	}
	query += albumGrouping + order

	rows, err := q.db.Query(ctx, query, args...)
	if err != nil {
		return page, err
	}
	defer rows.Close()

	page.Items = make([]AlbumRow, 0)
	for rows.Next() {
		var row AlbumRow
		if err := rows.Scan(
			&row.ID, &row.ArtistID, &row.ArtistName, &row.ArtistFollowed,
			&row.MusicbrainzReleaseGroupID, &row.Title, &row.ReleaseDate,
			&row.AlbumType, &row.FirstReleaseDate, &row.TrackCount,
			&row.OwnedTrackCount, &row.DismissedTrackCount, &row.Monitored,
			&row.MonitorLevel, &row.MusicbrainzReleaseID,
			&row.EditionSelectionReason, &row.TrackRefreshStatus, &row.HasCover,
		); err != nil {
			return page, err
		}
		page.Items = append(page.Items, row)
	}
	if err := rows.Err(); err != nil {
		return page, err
	}
	if !counted {
		page.Total = int64(len(page.Items))
		page.ScopeTotal = page.Total
	}
	return page, nil
}

// albumTile is the shape an artist's page draws a release as, in SQL. The page
// used to decide this per release in the browser, which it could do only while
// it held every release the artist has; asking the server for one page at a
// time takes that away, and a completeness figure computed over a page is a
// worse answer than a slow one.
//
// So it is transcribed rather than approximated. albumStatusFilters answers a
// neighbouring question and cannot stand in for this one: it has no name for a
// release whose every gap was dismissed, none for one still refreshing, and it
// reads the denominator as track_count less dismissals where an artist watched
// only for what is owned counts the owned tracks instead — under which a
// release with unowned tracks is complete, not partial. The order of the
// branches is the meaning: how a refresh went outranks what the tracks say.
const albumTile = `CASE
		WHEN ` + albumRefreshFailed + ` THEN 'failed'
		WHEN NOT ` + albumRefreshSettled + ` THEN 'busy'
		WHEN NOT ` + albumHasTracks + ` THEN 'unknown'
		WHEN albums.owned_track_count = 0 AND (` + albumCountable + `) = 0
			THEN CASE WHEN artists.monitor_level = 'owned' THEN 'missing' ELSE 'dismissed' END
		WHEN albums.owned_track_count = 0 THEN 'missing'
		WHEN albums.owned_track_count < (` + albumCountable + `) THEN 'partial'
		ELSE 'owned'
	END`

// albumCompleteness is the two halves of the fraction an artist's page states,
// as something the list can be narrowed to. They are written from albumTile and
// albumMonitored, which is the point of them: a chip that narrows the grid by
// one rule while the header counts by another can say a release is missing on
// the same screen that says nothing is missing, and the two rules have to be
// one rule for that to be impossible. albumStatusFilters cannot serve here — it
// has no reading of the monitor level, so it would offer an unmonitored release
// under a heading the header does not count it in.
//
// Neither is index-backed, which is what keeps them off the catalogue-wide
// browser: they are arithmetic per row, bounded by the artist they are asked
// about.
var albumCompleteness = map[string]string{
	"owned":   `(` + albumMonitored + `) AND (` + albumTile + `) = 'owned'`,
	"missing": `(` + albumMonitored + `) AND (` + albumTile + `) IN ('missing', 'partial')`,
}

// ArtistDiscography is how much of one artist the library holds, counted over
// everything they have rather than over a page of it.
type ArtistDiscography struct {
	// Releases is every release the artist has, monitored or not, because the
	// page shows them all and says so.
	Releases int64
	// Owned, Missing and Dismissed count only monitored releases: the monitor
	// level filters counting, never what is shown. Missing is a release with
	// anything still to get, whether it holds none of its tracks or some.
	Owned     int64
	Missing   int64
	Dismissed int64
	// Counted is the denominator under Owned — the monitored releases, less the
	// ones whose every gap the user decided about. A release nobody wants
	// belongs to neither half of the fraction.
	Counted int64
	// Loading is monitored releases whose tracklist has not arrived. Following
	// an artist queues one release refresh per release group and they land one
	// at a time behind MusicBrainz's rate limit, so for the first hours a page
	// can hold releases that are neither owned nor missing yet — the page says
	// how many rather than counting them as either.
	Loading int64
}

// ArtistDiscography counts the shapes of one artist's releases in a single
// pass. It is one row over the rows albums_artist_id_idx already reaches, so
// the page can state a whole-discography completeness while asking for its
// releases one page at a time.
func (q *Queries) ArtistDiscography(ctx context.Context, artistID uuid.UUID) (ArtistDiscography, error) {
	var counts ArtistDiscography
	err := q.db.QueryRow(ctx, `
		SELECT count(*)::bigint,
		       count(*) FILTER (WHERE monitored AND tile = 'owned')::bigint,
		       count(*) FILTER (WHERE monitored AND tile IN ('missing', 'partial'))::bigint,
		       count(*) FILTER (WHERE monitored AND tile = 'dismissed')::bigint,
		       count(*) FILTER (WHERE monitored AND tile <> 'dismissed')::bigint,
		       count(*) FILTER (WHERE monitored AND NOT has_tracks)::bigint
		FROM (
			SELECT `+albumMonitored+` AS monitored,
			       `+albumTile+` AS tile,
			       `+albumHasTracks+` AS has_tracks
			FROM albums
			JOIN artists ON artists.id = albums.artist_id`+albumRefreshLateral+`
			WHERE albums.artist_id = $1
		) AS shape
	`, artistID).Scan(
		&counts.Releases, &counts.Owned, &counts.Missing,
		&counts.Dismissed, &counts.Counted, &counts.Loading,
	)
	if err != nil {
		return counts, fmt.Errorf("count discography %s: %w", artistID, err)
	}
	return counts, nil
}

// ArtistMusicBrainzIDsByName reads which of these names the catalogue already
// holds a MusicBrainz artist under, so a form filled in for MusicBrainz can name
// the artist rather than describe them.
//
// The comparison is exact and case-sensitive, which is both the only agreement
// worth acting on here and what artists_name_order_idx answers without a scan. A
// name two artists in the catalogue answer to is left out entirely: which of them
// was meant is a question, and this is not the kind of question anything in
// Schall answers by picking one.
func (q *Queries) ArtistMusicBrainzIDsByName(
	ctx context.Context, names []string,
) (map[string]uuid.UUID, error) {
	if len(names) == 0 {
		return nil, nil
	}
	rows, err := q.db.Query(ctx, `
		SELECT name, musicbrainz_id FROM artists WHERE name = ANY($1)
	`, names)
	if err != nil {
		return nil, fmt.Errorf("read artist identifiers by name: %w", err)
	}
	defer rows.Close()
	identified := make(map[string]uuid.UUID, len(names))
	carriers := make(map[string]int, len(names))
	for rows.Next() {
		var name string
		var id uuid.NullUUID
		if err := rows.Scan(&name, &id); err != nil {
			return nil, fmt.Errorf("read artist identifiers by name: %w", err)
		}
		carriers[name]++
		if id.Valid {
			identified[name] = id.UUID
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read artist identifiers by name: %w", err)
	}
	for name, carrying := range carriers {
		if carrying > 1 {
			delete(identified, name)
		}
	}
	return identified, nil
}

// QueueGenreSweep asks for a pass over the artists whose genres nobody has
// asked MusicBrainz for. A pass already on the books keeps the earlier of the
// two times.
//
// One sweeper, enforced by jobs_genre_sweep_idx. The pass queues artist
// refreshes, and two passes running would queue the same refreshes twice.
func (q *Queries) QueueGenreSweep(ctx context.Context, runAfter time.Time) error {
	_, err := q.db.Exec(ctx, `
		INSERT INTO jobs (kind, payload, max_attempts, run_after)
		VALUES ('sweep_genres', '{}'::jsonb, 3, $1)
		ON CONFLICT (kind)
		    WHERE kind = 'sweep_genres' AND status IN ('queued', 'running')
		DO UPDATE
		SET run_after = least(jobs.run_after, EXCLUDED.run_after),
		    updated_at = now()
		WHERE jobs.status = 'queued'
	`, runAfter)
	return err
}

// EnsureGenreSweepQueued adds a pass only when none is queued and none is
// running. Startup is the only caller, and it means "there must be a pass on
// the books" rather than "refresh every artist now": a pass that has just
// queued a batch waits for those refreshes to run, and a pod replaced inside
// that wait must not cancel the wait.
func (q *Queries) EnsureGenreSweepQueued(ctx context.Context, runAfter time.Time) error {
	_, err := q.db.Exec(ctx, `
		INSERT INTO jobs (kind, payload, max_attempts, run_after)
		VALUES ('sweep_genres', '{}'::jsonb, 3, $1)
		ON CONFLICT (kind)
		    WHERE kind = 'sweep_genres' AND status IN ('queued', 'running')
		DO NOTHING
	`, runAfter)
	return err
}
