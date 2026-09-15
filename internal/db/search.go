package db

import (
	"context"
	"fmt"
	"strings"
)

// SearchParams is one question and how much of an answer is wanted per kind.
type SearchParams struct {
	Query string
	// Limit bounds each kind on its own rather than the answer as a whole, so
	// an artist with a hundred releases cannot crowd the files out of the
	// reply. The caller decides it; nothing here has an opinion beyond
	// refusing to run unbounded.
	Limit int32
}

// SearchTotals is how many rows each kind has under the query, which is not how
// many came back. The limit is what a front door shows; this is what it would
// have shown without one, and it is the only thing that can write "All 214 in
// Files" over a list of five.
type SearchTotals struct {
	Artists   int64
	Releases  int64
	Tracks    int64
	Files     int64
	Playlists int64
}

// SearchResults is what Schall holds under one query, grouped by the kind of
// thing it is. Every group is present whether or not it has rows: a caller
// showing five headings should not have to tell "nothing matched" apart from
// "this kind was not searched".
type SearchResults struct {
	Artists   []SearchArtistsRow
	Releases  []SearchReleasesRow
	Tracks    []SearchCatalogueTracksRow
	Files     []SearchLibraryFilesRow
	Playlists []SearchPlaylistsRow
	Totals    SearchTotals
}

// searchTotal reads the total off the answer that carries it. Every row of one
// kind carries the same number — it is counted over the whole match before the
// limit takes its five — so the first row is the answer, and no rows at all
// means nothing answered, which is a total of none.
func searchTotal[Row any](rows []Row, total func(Row) int64) int64 {
	if len(rows) == 0 {
		return 0
	}
	return total(rows[0])
}

// Search asks the same question of artists, releases, catalogue tracks, library
// files and playlists, and says under each heading what answered to it.
//
// It is five queries and not one union. The kinds have nothing in common except
// that a person can type their name, and flattening them into shared columns
// would cost each result the fields its own page needs to be linked to — a
// release wants its artist and its year, a track wants the release it sits on,
// a file wants its path. Five bounded index-free scans is also five small
// answers rather than one large sort over everything.
//
// Nothing here is evidence. A search result is somewhere to go; it never says
// what a file is, and no caller may read one as a match.
//
// Deliberately not carried: how complete an artist is. The artists page shows
// that under every name, but it is a grouped pass over the whole catalogue's
// tracks, and a cheaper approximation computed for five artists would print a
// different number beside the same artist in two places — worse than printing
// none. What is carried instead is what the library holds of them, files and
// releases it has something of, which is a plain count of things that exist and
// not a fraction of things that should.
func (q *Queries) Search(ctx context.Context, params SearchParams) (SearchResults, error) {
	var results SearchResults
	results.Artists = []SearchArtistsRow{}
	results.Releases = []SearchReleasesRow{}
	results.Tracks = []SearchCatalogueTracksRow{}
	results.Files = []SearchLibraryFilesRow{}
	results.Playlists = []SearchPlaylistsRow{}

	// A query of nothing is answered here rather than in the database. Every
	// one of these patterns would be '%%' — five sequential scans of the whole
	// collection to describe rows nobody asked about — and this is the layer
	// that would do the scanning, so it is the layer that refuses.
	query := strings.TrimSpace(params.Query)
	if query == "" {
		return results, nil
	}
	if params.Limit < 1 {
		return results, fmt.Errorf("search %q: limit must be at least 1", query)
	}

	artists, err := q.SearchArtists(ctx, SearchArtistsParams{Query: query, Limit: params.Limit})
	if err != nil {
		return results, fmt.Errorf("search artists for %q: %w", query, err)
	}
	results.Artists = artists

	releases, err := q.SearchReleases(ctx, SearchReleasesParams{Query: query, Limit: params.Limit})
	if err != nil {
		return results, fmt.Errorf("search releases for %q: %w", query, err)
	}
	results.Releases = releases

	tracks, err := q.SearchCatalogueTracks(ctx, SearchCatalogueTracksParams{Query: query, Limit: params.Limit})
	if err != nil {
		return results, fmt.Errorf("search catalogue tracks for %q: %w", query, err)
	}
	results.Tracks = tracks

	files, err := q.SearchLibraryFiles(ctx, SearchLibraryFilesParams{Query: query, Limit: params.Limit})
	if err != nil {
		return results, fmt.Errorf("search library files for %q: %w", query, err)
	}
	results.Files = files

	playlists, err := q.SearchPlaylists(ctx, SearchPlaylistsParams{Query: query, Limit: params.Limit})
	if err != nil {
		return results, fmt.Errorf("search playlists for %q: %w", query, err)
	}
	results.Playlists = playlists

	results.Totals = SearchTotals{
		Artists:   searchTotal(artists, func(row SearchArtistsRow) int64 { return row.TotalCount }),
		Releases:  searchTotal(releases, func(row SearchReleasesRow) int64 { return row.TotalCount }),
		Tracks:    searchTotal(tracks, func(row SearchCatalogueTracksRow) int64 { return row.TotalCount }),
		Files:     searchTotal(files, func(row SearchLibraryFilesRow) int64 { return row.TotalCount }),
		Playlists: searchTotal(playlists, func(row SearchPlaylistsRow) int64 { return row.TotalCount }),
	}

	return results, nil
}
