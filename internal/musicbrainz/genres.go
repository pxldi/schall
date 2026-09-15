package musicbrainz

import (
	"sort"
	"strings"
)

// maxGenres is how many genres one entity is read down to. MusicBrainz lets
// anybody add a genre to anything, so the long tail of one vote each is where
// the junk is: "seen live", a misspelling, somebody's private joke. The first
// few by vote count are what the community agrees the music is, and a page
// shows a handful of chips rather than a paragraph of them.
const maxGenres = 5

// genreItem is one genre as MusicBrainz prints it: the name, and how many
// people voted for it.
type genreItem struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// topGenres puts the genres MusicBrainz holds for one entity in the order the
// community put them, and keeps the first few.
//
// A genre nobody voted for is dropped. MusicBrainz reports a count of zero for
// a genre whose votes were all withdrawn, and that is the community having
// changed its mind rather than an opinion to print.
//
// The result is never nil once the provider has answered: an entity with no
// votes comes back as an empty list, which is the answer "asked, and there are
// none" and not the absence of an answer. The store keeps that difference.
func topGenres(items []genreItem) []string {
	ranked := make([]genreItem, 0, len(items))
	for _, item := range items {
		name := strings.TrimSpace(item.Name)
		if name == "" || item.Count <= 0 {
			continue
		}
		ranked = append(ranked, genreItem{Name: name, Count: item.Count})
	}
	// Most voted first, and ties by name so the same votes always print in the
	// same order rather than in whatever order the response arrived in.
	sort.SliceStable(ranked, func(left, right int) bool {
		if ranked[left].Count != ranked[right].Count {
			return ranked[left].Count > ranked[right].Count
		}
		return ranked[left].Name < ranked[right].Name
	})
	if len(ranked) > maxGenres {
		ranked = ranked[:maxGenres]
	}
	names := make([]string, 0, len(ranked))
	for _, item := range ranked {
		names = append(names, item.Name)
	}
	return names
}
