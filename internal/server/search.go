package server

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/db"
)

// SearchStore answers one query across everything Schall holds. It is optional
// on the store the way the other narrow interfaces are: without it the route
// says search is unavailable and every list page still searches itself.
type SearchStore interface {
	Search(context.Context, db.SearchParams) (db.SearchResults, error)
}

// searchDefaultLimit is how many of each kind a front door shows. Five is a
// glance; the rest of the answer lives on the list page the group escapes into,
// which is paged and indexed and was always the workhorse.
const searchDefaultLimit = 5

// searchMaxLimit is as far as a caller may push that. It is low on purpose:
// these queries scan, and the answer to wanting more than a glance is the list
// page, not a bigger glance.
const searchMaxLimit = 20

type searchArtistResult struct {
	ID       uuid.UUID `json:"id"`
	Name     string    `json:"name"`
	SortName string    `json:"sortName"`
	// Followed is which half of the artists page this one is filed under —
	// followed on purpose, or held because their music is here.
	Followed bool `json:"followed"`
	// What the library holds of them: files it still has, and releases at least
	// one of those files sits on. Not the artists page's completeness fraction
	// and not an approximation of it; see Search in internal/db/search.go.
	FileCount    int64 `json:"fileCount"`
	ReleaseCount int64 `json:"releaseCount"`
}

type searchReleaseResult struct {
	ID         uuid.UUID `json:"id"`
	Title      string    `json:"title"`
	ArtistID   uuid.UUID `json:"artistId"`
	ArtistName string    `json:"artistName"`
	// AlbumType is what kind of release it is, as MusicBrainz files it.
	AlbumType string `json:"albumType"`
	// ReleaseDate is the date as the catalogue holds it, or absent. It is a
	// string rather than a time because a release is dated to the day at best
	// and to the year often, and the browser prints the year off it.
	ReleaseDate     string `json:"releaseDate,omitempty"`
	TrackCount      int32  `json:"trackCount"`
	OwnedTrackCount int32  `json:"ownedTrackCount"`
}

type searchTrackResult struct {
	ID          uuid.UUID `json:"id"`
	Title       string    `json:"title"`
	DiscNumber  int32     `json:"discNumber"`
	TrackNumber *int32    `json:"trackNumber,omitempty"`
	// DurationMs is how long the catalogue says the recording is, or absent.
	DurationMs *int32 `json:"durationMs,omitempty"`
	// A catalogue track has no page of its own, so the release it sits on is
	// what a result is linked by.
	AlbumID    uuid.UUID `json:"albumId"`
	AlbumTitle string    `json:"albumTitle"`
	ArtistID   uuid.UUID `json:"artistId"`
	ArtistName string    `json:"artistName"`
	// Owned says the library holds this recording somewhere, by the same rule
	// the rest of Schall reads it by. It never says which file, and no caller
	// may read it as a match.
	Owned bool `json:"owned"`
}

type searchFileResult struct {
	ID        uuid.UUID `json:"id"`
	Path      string    `json:"path"`
	ArtistTag *string   `json:"artistTag,omitempty"`
	AlbumTag  *string   `json:"albumTag,omitempty"`
	TitleTag  *string   `json:"titleTag,omitempty"`
	// SizeBytes is the figure a file is counted in, as the scanner recorded it.
	SizeBytes   int64  `json:"sizeBytes"`
	MatchStatus string `json:"matchStatus"`
	// Missing is a file the last scan could not find. It is still answered
	// because the library list still lists it, and saying so is better than
	// leading somewhere empty.
	Missing bool `json:"missing"`
}

type searchPlaylistResult struct {
	ID         uuid.UUID  `json:"id"`
	Name       string     `json:"name"`
	Source     string     `json:"source"`
	OwnerName  string     `json:"ownerName"`
	TrackCount int32      `json:"trackCount"`
	ImportedAt *time.Time `json:"importedAt"`
}

// searchTotals is how many results each kind has, which is not how many came
// back under the limit. A front door showing five of something has to be able
// to say how many there are before offering the way to the rest of them, and a
// truncated list cannot say it.
type searchTotals struct {
	Artists   int64 `json:"artists"`
	Releases  int64 `json:"releases"`
	Tracks    int64 `json:"tracks"`
	Files     int64 `json:"files"`
	Playlists int64 `json:"playlists"`
}

// searchResponse is grouped by kind rather than ranked into one list. Schall
// has no way to say an artist is a better answer than a file — they are not the
// same kind of thing — and inventing a score to interleave them would be the
// project's one forbidden move dressed up as presentation. Every group is
// present even when empty, so a caller can lay out its headings once.
type searchResponse struct {
	Query     string                 `json:"query"`
	Limit     int32                  `json:"limit"`
	Artists   []searchArtistResult   `json:"artists"`
	Releases  []searchReleaseResult  `json:"releases"`
	Tracks    []searchTrackResult    `json:"tracks"`
	Files     []searchFileResult     `json:"files"`
	Playlists []searchPlaylistResult `json:"playlists"`
	Totals    searchTotals           `json:"totals"`
}

func emptySearchResponse(query string, limit int32) searchResponse {
	return searchResponse{
		Query:     query,
		Limit:     limit,
		Artists:   []searchArtistResult{},
		Releases:  []searchReleaseResult{},
		Tracks:    []searchTrackResult{},
		Files:     []searchFileResult{},
		Playlists: []searchPlaylistResult{},
	}
}

// globalSearch answers one query across artists, releases, catalogue tracks,
// library files and playlists, so that "do I have anything by X" is one
// question rather than four scoped ones asked from four pages. It is called
// global to keep it apart from searchArtists, which asks MusicBrainz about
// artists Schall does not hold yet.
//
// It is display and navigation. Nothing it returns is evidence, nothing it
// reads is written back, and a result is a place to go rather than a claim
// about what a file is.
func (api *API) globalSearch(response http.ResponseWriter, request *http.Request) {
	if api.search == nil {
		api.problem(response, http.StatusServiceUnavailable, "search is not available", nil)
		return
	}
	limit := int32(searchDefaultLimit)
	if raw := strings.TrimSpace(request.URL.Query().Get("limit")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > searchMaxLimit {
			api.problem(response, http.StatusUnprocessableEntity, "validation failed",
				[]string{"limit must be between 1 and " + strconv.Itoa(searchMaxLimit)})
			return
		}
		limit = int32(value)
	}
	// An empty box is not a question, and it is certainly not a request for the
	// whole collection. It is answered with nothing at all, without asking the
	// database, because a palette opens empty and would otherwise ask for
	// everything before the first key is pressed.
	query := strings.TrimSpace(request.URL.Query().Get("q"))
	if query == "" {
		api.writeJSON(response, http.StatusOK, emptySearchResponse(query, limit))
		return
	}
	results, err := api.search.Search(request.Context(), db.SearchParams{Query: query, Limit: limit})
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	api.writeJSON(response, http.StatusOK, searchResults(query, limit, results))
}

func searchResults(query string, limit int32, results db.SearchResults) searchResponse {
	payload := emptySearchResponse(query, limit)
	payload.Totals = searchTotals{
		Artists:   results.Totals.Artists,
		Releases:  results.Totals.Releases,
		Tracks:    results.Totals.Tracks,
		Files:     results.Totals.Files,
		Playlists: results.Totals.Playlists,
	}
	for _, row := range results.Artists {
		payload.Artists = append(payload.Artists, searchArtistResult{
			ID: row.ID, Name: row.Name, SortName: row.SortName, Followed: row.Followed,
			FileCount: row.FileCount, ReleaseCount: row.ReleaseCount,
		})
	}
	for _, row := range results.Releases {
		release := searchReleaseResult{
			ID: row.ID, Title: row.Title, ArtistID: row.ArtistID,
			ArtistName: row.ArtistName, AlbumType: row.AlbumType, TrackCount: row.TrackCount,
			OwnedTrackCount: row.OwnedTrackCount,
		}
		if row.ReleaseDate.Valid {
			release.ReleaseDate = row.ReleaseDate.Time.Format("2006-01-02")
		}
		payload.Releases = append(payload.Releases, release)
	}
	for _, row := range results.Tracks {
		payload.Tracks = append(payload.Tracks, searchTrackResult{
			ID: row.ID, Title: row.Title, DiscNumber: row.DiscNumber,
			TrackNumber: int32Pointer(row.TrackNumber), DurationMs: int32Pointer(row.DurationMs),
			AlbumID: row.AlbumID, AlbumTitle: row.AlbumTitle, ArtistID: row.ArtistID,
			ArtistName: row.ArtistName, Owned: row.Owned,
		})
	}
	for _, row := range results.Files {
		payload.Files = append(payload.Files, searchFileResult{
			ID: row.ID, Path: row.Path, ArtistTag: textPointer(row.ArtistTag),
			AlbumTag: textPointer(row.AlbumTag), TitleTag: textPointer(row.TitleTag),
			SizeBytes: row.SizeBytes, MatchStatus: row.MatchStatus, Missing: row.Missing,
		})
	}
	for _, row := range results.Playlists {
		payload.Playlists = append(payload.Playlists, searchPlaylistResult{
			ID: row.ID, Name: row.Name, Source: row.Source,
			OwnerName: row.OwnerName, TrackCount: row.TrackCount,
			ImportedAt: nullableTime(row.ImportedAt.Valid, row.ImportedAt.Time),
		})
	}
	return payload
}
