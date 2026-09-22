package navidrome

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
)

// Playlist is a playlist as Navidrome holds it.
//
// Entries are only filled in by Playlist, which asks for one by id. The listing
// answers with songCount and no tracks, and that count is worth keeping even
// though it is not evidence of anything: ADR 0010 watched it fall
// from 3 to 2 while the row it stopped counting was still in the database, so
// it says how many tracks Navidrome can currently show and nothing about how
// many the playlist has.
type Playlist struct {
	ID        string
	Name      string
	Owner     string
	Comment   string
	SongCount int
	Entries   []Song
}

// Song is one file as Navidrome knows it.
//
// Path is the path the server reported, which is not the same thing as the path
// the file is at. By default Navidrome answers with a display path composed
// from the album's first folder and the track's own tags; real paths need
// Subsonic.DefaultReportRealPath to have been true when this client's player
// row was created (ADR 0010). A display path can equal the real one
// and stop equalling it later, so a caller that joins on it against a server
// that reports display paths will find nothing here and mis-join nowhere else.
//
// This client reports what it was told and does not judge it. Refusing to run
// against a server that reports display paths is the sync pass's job, because
// sync is what has something to refuse — a client that only reads has no
// business deciding the deployment is wrong.
type Song struct {
	ID    string
	Title string
	Path  string
}

// PlaylistUpdate is one change to a playlist that already exists. Every field
// except PlaylistID is optional, and an empty one means "leave this alone"
// rather than "make it empty".
//
// RemoveIndexes are positions in the playlist as the server holds it now, and
// the server reads all of them against that one listing. So they come from a
// single reading of the playlist and are not adjusted for each other: removing
// positions 0 and 1 means the first two tracks, never the first and then
// whatever moved up into second place.
type PlaylistUpdate struct {
	PlaylistID    string
	Name          string
	Comment       string
	AddSongIDs    []string
	RemoveIndexes []int
}

// searchCandidates bounds how many songs one search asks the server for. It is
// the width of a net and not a ranking: nothing about where a candidate came in
// the results decides anything, so this only has to be wide enough that an
// exact match is not pushed off the end by namesakes.
const searchCandidates = 100

// candidateEvidence bounds how many candidate paths a refusal carries with it.
// It is there to be read by a person deciding which of two causes they are
// looking at, so a handful is the whole point; the count of candidates says how
// wide the net was.
const candidateEvidence = 3

// SongLookup is one question put to the player: which song is this file?
//
// Path is what the answer is judged by and the only thing that is. Artist and
// Title are how the server is asked — Subsonic has no lookup by path and
// Navidrome indexes tags, so something has to be searched for, and the tags
// Schall read from this very file are the closest thing it has to the words the
// server filed it under. They are optional; without them the file name is used,
// which is what this always did.
type SongLookup struct {
	Path   string
	Artist string
	Title  string
}

// SongMatch is a song the player answered with, and how the answer was reached.
type SongMatch struct {
	Song Song
	// Term is the search the accepted candidate came back from. It says which
	// net caught the file, which is the difference between "the tags Schall
	// holds describe it" and "the file name happens to appear in its tags".
	Term string
}

// MissReason is which of the ways a lookup came back with nothing. The two in
// the middle are the two competing explanations for a file the player will not
// answer for, and telling them apart per file is the point of recording it: a
// search that returned nothing found the wrong words, a search that returned
// songs at other paths found the wrong file.
type MissReason string

const (
	// MissNoTerm is a path with no file name and no tags — nothing to ask about.
	MissNoTerm MissReason = "no_search_term"
	// MissNoCandidates is a search the server answered with no songs at all.
	MissNoCandidates MissReason = "no_candidates"
	// MissNoPathMatch is songs coming back, none of them at that path.
	MissNoPathMatch MissReason = "no_path_match"
	// MissPathRepeated is more than one song reporting the same path.
	MissPathRepeated MissReason = "path_repeated"
)

// LookupAttempt is one search made on the way to a refusal: what was asked, how
// many songs came back, and a few of the paths they were at.
type LookupAttempt struct {
	Term       string
	Candidates int
	// Evidence are candidate paths, escaped. They are never a match and were
	// never considered as one — the lookup was already refused. They are here so
	// that a person can see what the server offered instead.
	Evidence []string
}

// LookupMiss is a path the player did not answer for, carrying what was asked
// and what came back. It reads as ErrNotFound, because to every caller that is
// still the answer: this file is not one the player can be told about yet.
//
// The evidence exists because the count alone cannot tell the two causes apart.
// A file the search never reached and a file the search reached under another
// path both end here, and only the attempts say which.
type LookupMiss struct {
	Path     string
	Reason   MissReason
	Attempts []LookupAttempt
}

func (miss *LookupMiss) Error() string {
	return fmt.Sprintf("find the navidrome song at %s: %s", Escaped(miss.Path), miss.detail())
}

func (miss *LookupMiss) detail() string {
	switch miss.Reason {
	case MissNoTerm:
		return "there was nothing to search for"
	case MissNoCandidates:
		return fmt.Sprintf("%s found no songs at all", miss.terms())
	case MissPathRepeated:
		return "more than one song reported that path"
	default:
		return fmt.Sprintf("%s found songs, none of them at that path", miss.terms())
	}
}

func (miss *LookupMiss) terms() string {
	quoted := make([]string, 0, len(miss.Attempts))
	for _, attempt := range miss.Attempts {
		quoted = append(quoted, strconv.Quote(attempt.Term))
	}
	if len(quoted) == 0 {
		return "the search"
	}
	return "searching " + strings.Join(quoted, " and ")
}

// Candidates is how many songs the server offered across every attempt.
func (miss *LookupMiss) Candidates() int {
	total := 0
	for _, attempt := range miss.Attempts {
		total += attempt.Candidates
	}
	return total
}

// Is reads every miss as ErrNotFound. The reason is for whoever is looking into
// it; the sync pass acts on the absence and nothing else, exactly as before.
func (miss *LookupMiss) Is(target error) bool { return target == ErrNotFound }

// Escaped renders a path so that two paths which print identically can still be
// told apart: non-ASCII runes as their escapes, and the byte length alongside.
//
// A log that shows the two Unicode normal forms of the same name as the same
// characters is worse than no evidence at all — the whole question being asked
// is why a byte-for-byte comparison of two strings that look alike said no.
func Escaped(path string) string {
	return fmt.Sprintf("%s (%d bytes)", strconv.QuoteToASCII(path), len(path))
}

// Playlists lists the playlists the account can see, without their tracks.
func (client *Client) Playlists(ctx context.Context) ([]Playlist, error) {
	payload, err := client.call(ctx, "getPlaylists", nil)
	if err != nil {
		return nil, client.describe(ctx, err)
	}
	if payload.Playlists == nil {
		return nil, nil
	}
	playlists := make([]Playlist, 0, len(payload.Playlists.Playlist))
	for _, listed := range payload.Playlists.Playlist {
		playlists = append(playlists, listed.playlist())
	}
	return playlists, nil
}

// Playlist reads one playlist and the entries the server is currently willing
// to list for it, in the order it listed them.
//
// The order is the playlist's own and is carried through untouched, because a
// removal is expressed as a position in it.
func (client *Client) Playlist(ctx context.Context, id string) (Playlist, error) {
	payload, err := client.call(ctx, "getPlaylist", url.Values{"id": {id}})
	if err != nil {
		return Playlist{}, client.describe(ctx, err)
	}
	if payload.Playlist == nil {
		return Playlist{}, fmt.Errorf("read navidrome playlist %s: %w", id, ErrNotFound)
	}
	return payload.Playlist.playlist(), nil
}

// CreatePlaylist makes a playlist holding the given songs, in the given order,
// and reports the id the server gave it.
//
// The id is the point of the call rather than a detail of it: it is what a
// snapshot of the pushed state is keyed by, so a server that made the playlist
// without saying what it is leaves nothing to write down and is an error.
func (client *Client) CreatePlaylist(
	ctx context.Context, name string, songIDs []string,
) (string, error) {
	parameters := url.Values{"name": {name}}
	for _, songID := range songIDs {
		parameters.Add("songId", songID)
	}

	payload, err := client.post(ctx, "createPlaylist", parameters)
	if err != nil {
		return "", client.describe(ctx, err)
	}
	if payload.Playlist == nil || strings.TrimSpace(payload.Playlist.ID) == "" {
		return "", errors.New("navidrome made the playlist without saying what its id is")
	}
	return payload.Playlist.ID, nil
}

// UpdatePlaylist applies one change to an existing playlist: songs added at the
// end, songs removed by the position they hold now, and the name or comment
// rewritten.
func (client *Client) UpdatePlaylist(ctx context.Context, update PlaylistUpdate) error {
	playlistID := strings.TrimSpace(update.PlaylistID)
	if playlistID == "" {
		return errors.New("a playlist update has to say which playlist it updates")
	}

	parameters := url.Values{"playlistId": {playlistID}}
	if name := strings.TrimSpace(update.Name); name != "" {
		parameters.Set("name", name)
	}
	if comment := strings.TrimSpace(update.Comment); comment != "" {
		parameters.Set("comment", comment)
	}
	for _, songID := range update.AddSongIDs {
		parameters.Add("songIdToAdd", songID)
	}
	for _, index := range update.RemoveIndexes {
		parameters.Add("songIndexToRemove", strconv.Itoa(index))
	}

	if _, err := client.post(ctx, "updatePlaylist", parameters); err != nil {
		return client.describe(ctx, err)
	}
	return nil
}

// Song reads one song by the id it was last known under.
//
// An id that no longer resolves comes back as ErrNotFound, and that is the
// whole reason this exists: ADR 0010 found that a track whose
// identity broke disappears from getPlaylist while getSong still answers "ok",
// so the id resolving is not evidence that it still means the same file. This
// call reports what the id resolves to, or that it resolves to nothing, and
// leaves the comparison of the path against the one Schall pushed to the
// caller that holds it.
func (client *Client) Song(ctx context.Context, id string) (Song, error) {
	payload, err := client.call(ctx, "getSong", url.Values{"id": {id}})
	if err != nil {
		return Song{}, client.describe(ctx, err)
	}
	if payload.Song == nil {
		return Song{}, fmt.Errorf("read navidrome song %s: %w", id, ErrNotFound)
	}
	return payload.Song.song(), nil
}

// FindSongByPath finds the song Navidrome holds for a path — exactly that path
// or nothing at all.
//
// Subsonic has no lookup by path, so the search is a way of getting a handful
// of songs to look at and never a way of choosing between them: a candidate is
// accepted only when the path the server reports is byte-for-byte the path that
// was asked for. Anything else is ErrNotFound. Not the nearest hit, not the one
// whose file name matches, not a case-insensitive equal, not the only candidate
// there was — a search that returns one song is not thereby a search that found
// the right one.
//
// This is the project's governing rule at this boundary. The thing being
// decided is which file a playlist entry is, and a wrong answer here writes a
// wrong pairing into the snapshot, which is what everything downstream then
// reasons from. Refusing costs a re-resolution; guessing costs the user's
// playlist.
//
// What is searched for may be tried more than once, because a net that caught
// nothing is not evidence about the file. Each term is asked in turn until one
// of them returns the path, and a refusal carries every attempt with it.
func (client *Client) FindSongByPath(ctx context.Context, lookup SongLookup) (SongMatch, error) {
	terms := searchTerms(lookup)
	if len(terms) == 0 {
		return SongMatch{}, &LookupMiss{Path: lookup.Path, Reason: MissNoTerm}
	}

	miss := &LookupMiss{Path: lookup.Path, Reason: MissNoCandidates}
	for _, term := range terms {
		payload, err := client.call(ctx, "search3", url.Values{
			"query":       {term},
			"artistCount": {"0"},
			"albumCount":  {"0"},
			"songCount":   {strconv.Itoa(searchCandidates)},
		})
		if err != nil {
			return SongMatch{}, client.describe(ctx, err)
		}
		var candidates []songPayload
		if payload.SearchResult3 != nil {
			candidates = payload.SearchResult3.Song
		}

		var found Song
		matches := 0
		for _, candidate := range candidates {
			if candidate.Path != lookup.Path {
				continue
			}
			found = candidate.song()
			matches++
		}
		switch {
		case matches == 1:
			return SongMatch{Song: found, Term: term}, nil
		case matches > 1:
			// Two songs reporting the same path is not a pair to choose between;
			// it is the server saying something this client has no way to
			// resolve. Nothing else is asked afterwards, because another term
			// answering would be this client shopping for a search that agrees.
			miss.Reason = MissPathRepeated
			miss.Attempts = append(miss.Attempts, attempt(term, candidates, lookup.Path))
			return SongMatch{}, miss
		}
		if len(candidates) > 0 {
			miss.Reason = MissNoPathMatch
		}
		miss.Attempts = append(miss.Attempts, attempt(term, candidates, lookup.Path))
	}
	return SongMatch{}, miss
}

// attempt records one search that did not answer, with a few of the paths it
// offered instead. The ones sharing the file name come first, because a path
// map that describes the wrong mount and a name that is filed in another folder
// both show up there; where nothing shares the name, the first few candidates
// say what the term found instead, which is the other half of the diagnosis.
//
// None of this chooses anything. The lookup has already been refused when this
// runs, and a candidate here is evidence about the search and never about the
// file.
func attempt(term string, candidates []songPayload, path string) LookupAttempt {
	recorded := LookupAttempt{Term: term, Candidates: len(candidates)}
	name := filepath.Base(path)
	for _, candidate := range candidates {
		if len(recorded.Evidence) >= candidateEvidence {
			return recorded
		}
		if filepath.Base(candidate.Path) == name {
			recorded.Evidence = append(recorded.Evidence, Escaped(candidate.Path))
		}
	}
	if len(recorded.Evidence) > 0 {
		return recorded
	}
	for _, candidate := range candidates {
		if len(recorded.Evidence) >= candidateEvidence {
			break
		}
		recorded.Evidence = append(recorded.Evidence, Escaped(candidate.Path))
	}
	return recorded
}

// searchTerms are the ways of asking after one file, widest answer first.
//
// The tags come first because they are what Navidrome indexes: it files a song
// under the words in it, and the words Schall read from the same file are the
// nearest thing to them. The file name is second and is a poor question — a
// name Schall composed carries bookkeeping no tag will ever hold, such as the
// position a track had in the run that acquired it, and a server that requires
// every word of a query to match finds nothing for it however healthy its index
// is. It stays because a file with no tags has nothing else to be asked about.
func searchTerms(lookup SongLookup) []string {
	var terms []string
	add := func(term string) {
		term = strings.Join(strings.Fields(term), " ")
		if term == "" {
			return
		}
		for _, existing := range terms {
			if existing == term {
				return
			}
		}
		terms = append(terms, term)
	}
	// Only with a title. An artist on their own is not a question about this
	// file — it is a question about everything they recorded, answered by a
	// hundred songs none of which need be this one.
	if strings.TrimSpace(lookup.Title) != "" {
		add(lookup.Artist + " " + lookup.Title)
	}
	add(fileNameTerm(lookup.Path))
	return terms
}

// fileNameTerm is the base name without its extension: the part of a path most
// likely to appear in a tag. Whether a result is the file is decided afterwards,
// by the path alone.
func fileNameTerm(path string) string {
	base := filepath.Base(strings.TrimSpace(path))
	if base == "." || base == string(filepath.Separator) {
		return ""
	}
	return strings.TrimSpace(strings.TrimSuffix(base, filepath.Ext(base)))
}

type playlistPayload struct {
	ID        string        `json:"id"`
	Name      string        `json:"name"`
	Owner     string        `json:"owner"`
	Comment   string        `json:"comment"`
	SongCount int           `json:"songCount"`
	Entry     []songPayload `json:"entry"`
}

type songPayload struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Path  string `json:"path"`
	// Starred is the time the account starred the song, and is absent when it
	// did not. Only songStar in keeps.go reads it, and only off getSong: the
	// field is decoded here because this is where a song is decoded, not
	// because a song in a playlist or a search result carries a meaningful one.
	Starred string `json:"starred"`
}

func (payload playlistPayload) playlist() Playlist {
	playlist := Playlist{
		ID:        payload.ID,
		Name:      payload.Name,
		Owner:     payload.Owner,
		Comment:   payload.Comment,
		SongCount: payload.SongCount,
	}
	// A playlist with nothing in it omits the entries entirely, which is a
	// playlist holding no tracks and not a failure to answer.
	for _, entry := range payload.Entry {
		playlist.Entries = append(playlist.Entries, entry.song())
	}
	return playlist
}

func (payload songPayload) song() Song {
	return Song{ID: payload.ID, Title: payload.Title, Path: payload.Path}
}
