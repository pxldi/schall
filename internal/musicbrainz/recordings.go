package musicbrainz

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ErrNotFound reports that the provider has no such entity. It is a fact about
// the music, not a failure of Schall, so a caller can tell the two apart.
var ErrNotFound = errors.New("MusicBrainz has no such entity")

// Credit is one artist as MusicBrainz credits them: the name this release
// prints, the artist that name is, and what the provider prints between this
// entry and the next.
//
// A credit is a list because MusicBrainz holds it as one, and it is only ever
// split where MusicBrainz already split it — never by cutting the printed
// string at a comma, which would make a band whose name holds one into two
// artists forever.
type Credit struct {
	// Name is the name this release prints, which is not always the artist's
	// own: MusicBrainz records "P!nk" on the sleeve and "Pink" as the artist.
	// The sleeve is what a listener reads, so the sleeve is what is kept.
	Name string
	// ArtistID is uuid.Nil where the credit names somebody MusicBrainz holds no
	// entity for. That is silence about the artist rather than a reason to lose
	// the name: the entry keeps its place, or the next artist's identifier ends
	// up standing against this one's name.
	ArtistID uuid.UUID
	// JoinPhrase is what the provider prints between this entry and the next —
	// ", ", " feat. ", " & ". Empty on the last one, and kept verbatim: joining
	// the list back up has to reproduce the printed credit character for
	// character.
	JoinPhrase string
	// ArtistName is the artist's own name, which is what Name is not: the sleeve
	// prints "Travi$ Scott" and MusicBrainz holds the artist as Travis Scott.
	// Both are kept because both are names a tagger writes, and reading one as
	// the other is the provider's statement rather than a resemblance.
	ArtistName string
	// Aliases are the other names the provider records for this artist. They
	// arrive with the credit where the lookup asked for them and are empty
	// otherwise, which is silence about the other names rather than a claim that
	// there are none.
	Aliases []string
}

// Recording is one recording as the provider describes it: what a local file
// could be. Releases are the editions the recording appears on, which is how a
// file that names an album can be told from one that names another.
type Recording struct {
	ID    uuid.UUID
	Title string
	// Disambiguation is the comment MusicBrainz editors write to tell two rows
	// of one title apart: "live", "clean", "instrumental". It is the provider's
	// own words about the row and never a resemblance, which is why the clean
	// edition is read from it (docs/decisions/0032).
	Disambiguation string
	// ArtistCredit is the credit as the provider prints it, join phrases and
	// all; Credits is the same credit as the list it was printed from. Neither
	// is derived from the other after the fact — both are read off what
	// MusicBrainz sent, and joining Credits back up reproduces ArtistCredit.
	ArtistCredit string
	Credits      []Credit
	DurationMS   *int
	ISRCs        []string
	// Score is the provider's own confidence in a search result, 0 to 100. It
	// ranks candidates for display and never decides anything.
	Score    int
	Releases []RecordingRelease
	// KnownAs is every ID this recording answers to: its own, and any merged-away
	// ID a lookup arrived by. MusicBrainz answers a lookup on a merged GID with
	// the recording it was merged into, so the ID that was asked for and the ID
	// that came back are the provider's own statement that both name this
	// recording. Recorded here rather than discarded, because a file tagged
	// before the merge otherwise reads as naming different music.
	KnownAs []uuid.UUID
}

// Answers reports whether id names this recording. It is the provider speaking,
// not a resemblance: an ID is here only because a lookup on it returned this
// entity.
func (recording Recording) Answers(id uuid.UUID) bool {
	if id == uuid.Nil {
		return false
	}
	if id == recording.ID {
		return true
	}
	for _, known := range recording.KnownAs {
		if known == id {
			return true
		}
	}
	return false
}

// RecordingRelease is one edition a recording appears on.
type RecordingRelease struct {
	ID             uuid.UUID
	ReleaseGroupID uuid.UUID
	Title          string
	Disambiguation string
	Date           string
	Status         string
	PrimaryType    string
	DiscNumber     int
	TrackNumber    int
}

// RecordingQuery is the evidence a local file offers when nothing identifies it
// outright. An ISRC is searched on its own, because an identifier that agrees
// needs no corroboration to be worth looking at.
type RecordingQuery struct {
	ISRC   string
	Artist string
	Title  string
}

// usable reports whether there is anything to ask the provider about. A file
// with neither an identifier nor a title cannot be searched for, and guessing
// from its path would be exactly the kind of resemblance Schall refuses to
// treat as evidence.
func (query RecordingQuery) usable() bool {
	return strings.TrimSpace(query.ISRC) != "" || strings.TrimSpace(query.Title) != ""
}

// Recording looks one recording up by its MusicBrainz ID.
func (client *Client) Recording(ctx context.Context, id uuid.UUID) (Recording, error) {
	if id == uuid.Nil {
		return Recording{}, errors.New("recording MusicBrainz ID is required")
	}
	var payload recordingItem
	_, err := client.getJSON(ctx, "/recording/"+id.String(), url.Values{
		"fmt": []string{"json"},
		// aliases costs no extra request and arrives on the credited artists as
		// well as on the recording. It is what lets a file tagged "Travis Scott"
		// be read against a credit printed "Travi$ Scott" as the one artist
		// MusicBrainz says they are (docs/decisions/0030).
		"inc": []string{"artist-credits+aliases+isrcs+releases+release-groups"},
	}, &payload)
	var httpErr *HTTPError
	if errors.As(err, &httpErr) && httpErr.StatusCode == 404 {
		client.rememberRecordingID(id, uuid.Nil)
		return Recording{}, ErrNotFound
	}
	if err != nil {
		return Recording{}, fmt.Errorf("look up MusicBrainz recording: %w", err)
	}
	recording, ok := decodeRecording(payload)
	if !ok {
		return Recording{}, ErrNotFound
	}
	// A different ID coming back is a merge: the recording asked for was merged
	// into this one, and both IDs name it.
	if recording.ID != id {
		recording.KnownAs = append(recording.KnownAs, id)
	}
	// The answer settles the narrower question too, so it is written down rather
	// than asked again: resolution's own lookups leave the client knowing what the
	// IDs it has seen name now.
	client.rememberRecordingID(id, recording.ID)
	return recording, nil
}

// ResolveRecordingID answers which recording an ID names now.
//
// MusicBrainz merges recordings, and it answers a lookup on a merged-away GID
// with the recording that GID was merged into. That redirect is the whole of the
// evidence here: two IDs name one recording because the provider arrived at the
// second when asked about the first, never because their titles agree, their
// durations agree, or they share an ISRC. Where the provider does not say so, the
// IDs go on naming different music.
//
// The direction is the provider's as well. A merged-away ID resolves to its
// successor; a successor resolves to itself and never back to what it replaced,
// so a caller holding one answer cannot read it the other way round.
//
// An ID MusicBrainz has never heard of comes back ErrNotFound, which is not a
// resolution and must not be read as one — silence about an ID is not the ID
// naming this recording. Any other failure is a failure: nothing was learnt, and
// a caller that turned it into a verdict would be writing an outage down as a
// fact about the music.
//
// An answer is remembered for a while so that the copies of one want arriving
// together cost one request between them rather than one each, and only for a
// while, because MusicBrainz is edited while Schall runs.
func (client *Client) ResolveRecordingID(ctx context.Context, id uuid.UUID) (uuid.UUID, error) {
	if id == uuid.Nil {
		return uuid.Nil, errors.New("recording MusicBrainz ID is required")
	}
	if resolved, known := client.knownRecordingID(id); known {
		if resolved == uuid.Nil {
			return uuid.Nil, ErrNotFound
		}
		return resolved, nil
	}
	// Only the ID is wanted, so only the ID is asked for: no artist credits, no
	// ISRCs, no releases. A merge redirects whatever the answer carries.
	var payload struct {
		ID string `json:"id"`
	}
	_, err := client.getJSON(ctx, "/recording/"+id.String(), url.Values{
		"fmt": []string{"json"},
	}, &payload)
	var httpErr *HTTPError
	if errors.As(err, &httpErr) && httpErr.StatusCode == 404 {
		client.rememberRecordingID(id, uuid.Nil)
		return uuid.Nil, ErrNotFound
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("resolve MusicBrainz recording %s: %w", id, err)
	}
	resolved, err := uuid.Parse(strings.TrimSpace(payload.ID))
	if err != nil {
		return uuid.Nil, fmt.Errorf("resolve MusicBrainz recording %s: %w", id, err)
	}
	client.rememberRecordingID(id, resolved)
	return resolved, nil
}

// maxKnownRecordingIDs bounds what one process remembers. A library asks about
// the same few recordings over and over, so the cap is generous and reaching it
// says this process has seen more distinct IDs than a cache is buying anything
// for. It starts again rather than choosing which answer to drop: everything here
// can be asked for a second time, and a rule for forgetting is a rule to get
// wrong.
const maxKnownRecordingIDs = 8192

// rememberFor is how long this process takes its own word for what an ID names.
// Nothing here is remembered permanently, because MusicBrainz is edited while
// Schall runs and every answer can be overtaken: an ID that names itself today is
// merged tomorrow, and so is the recording another was merged into. A want the
// acquisition loop is still retrying is exactly where a merge has to be noticed,
// and a server that runs for weeks would otherwise go on refusing copies the
// provider had since reconciled.
//
// An hour is long enough to cover the copies of one want arriving together, which
// is what the cache is for, and short enough that an editor's afternoon is not
// waited out.
const rememberFor = time.Hour

// knownRecordingID reports what this process has been told about an ID lately. A
// resolved ID of uuid.Nil is the provider having said it knows of no such
// recording, which is remembered because repeating it costs a request a second
// and can never become an agreement.
func (client *Client) knownRecordingID(id uuid.UUID) (uuid.UUID, bool) {
	client.knownMu.Lock()
	defer client.knownMu.Unlock()
	known, ok := client.known[id]
	if !ok {
		return uuid.Nil, false
	}
	if !known.expires.After(client.at()) {
		delete(client.known, id)
		return uuid.Nil, false
	}
	return known.id, true
}

// rememberRecordingID writes down what the provider said and nothing beyond it.
// The answer is kept under the ID that was asked, and under the ID that came back
// — that one is the entity the provider has just described, so a lookup on it
// arrives at itself. Nothing is written the other way round: a successor does not
// resolve to the ID it replaced, and inventing that entry would let one merge
// admit a file the provider never spoke about.
//
// Only what a request established is kept. A provider that could not be reached
// leaves the cache as it was, so an outage is retried rather than remembered.
func (client *Client) rememberRecordingID(asked, resolved uuid.UUID) {
	client.knownMu.Lock()
	defer client.knownMu.Unlock()
	if client.known == nil || len(client.known) >= maxKnownRecordingIDs {
		client.known = make(map[uuid.UUID]resolution, 64)
	}
	expires := client.at().Add(rememberFor)
	client.known[asked] = resolution{id: resolved, expires: expires}
	if resolved != uuid.Nil {
		client.known[resolved] = resolution{id: resolved, expires: expires}
	}
}

// at is the clock the answers age by, usable on a Client nobody constructed.
func (client *Client) at() time.Time {
	if client.now == nil {
		return time.Now()
	}
	return client.now()
}

// SearchRecordings asks the provider what a file might be. It is a search, so
// what comes back is candidates: the caller decides whether any of them is
// proof.
func (client *Client) SearchRecordings(ctx context.Context, query RecordingQuery, limit int) ([]Recording, error) {
	if !query.usable() {
		return nil, errors.New("recording search needs an ISRC or a title")
	}
	if limit < 1 || limit > 100 {
		return nil, errors.New("recording search limit must be between 1 and 100")
	}
	var payload recordingSearchResponse
	_, err := client.getJSON(ctx, "/recording", url.Values{
		"query": []string{luceneQuery(query)},
		"limit": []string{strconv.Itoa(limit)},
		"fmt":   []string{"json"},
	}, &payload)
	if err != nil {
		return nil, fmt.Errorf("search MusicBrainz recordings: %w", err)
	}
	result := make([]Recording, 0, len(payload.Recordings))
	for _, item := range payload.Recordings {
		if recording, ok := decodeRecording(item); ok {
			result = append(result, recording)
		}
	}
	return result, nil
}

// luceneQuery states the file's evidence in the provider's query language. An
// ISRC is asked on its own: it identifies a recording, and adding a title the
// tagger may have mangled could only ever lose the answer.
func luceneQuery(query RecordingQuery) string {
	if isrc := strings.TrimSpace(query.ISRC); isrc != "" {
		return "isrc:" + luceneTerm(isrc)
	}
	clauses := []string{"recording:" + lucenePhrase(query.Title)}
	if artist := strings.TrimSpace(query.Artist); artist != "" {
		clauses = append(clauses, "artist:"+lucenePhrase(artist))
	}
	return strings.Join(clauses, " AND ")
}

// luceneSpecial are the characters Lucene reads as syntax. A title containing
// any of them is ordinary; a query that lets them through is not.
const luceneSpecial = `+-&|!(){}[]^"~*?:\/`

func luceneTerm(value string) string {
	var builder strings.Builder
	for _, character := range value {
		if strings.ContainsRune(luceneSpecial, character) {
			builder.WriteRune('\\')
		}
		builder.WriteRune(character)
	}
	return builder.String()
}

// lucenePhrase quotes a value so its words are searched together. Only the
// quote and the escape need escaping inside a phrase.
func lucenePhrase(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + replacer.Replace(strings.TrimSpace(value)) + `"`
}

func decodeRecording(item recordingItem) (Recording, bool) {
	id, err := uuid.Parse(item.ID)
	if err != nil || strings.TrimSpace(item.Title) == "" {
		return Recording{}, false
	}
	recording := Recording{
		ID:             id,
		Title:          strings.TrimSpace(item.Title),
		Disambiguation: strings.TrimSpace(item.Disambiguation),
		ArtistCredit:   artistCredit(item.ArtistCredit),
		Credits:        artistCredits(item.ArtistCredit),
		DurationMS:     item.Length,
		Score:          item.Score.Int(),
	}
	for _, isrc := range item.ISRCs {
		if value := strings.TrimSpace(isrc); value != "" {
			recording.ISRCs = append(recording.ISRCs, value)
		}
	}
	sort.Strings(recording.ISRCs)
	for _, release := range item.Releases {
		releaseID, err := uuid.Parse(release.ID)
		if err != nil {
			continue
		}
		decoded := RecordingRelease{
			ID:             releaseID,
			Title:          strings.TrimSpace(release.Title),
			Disambiguation: strings.TrimSpace(release.Disambiguation),
			Date:           release.Date,
			Status:         release.Status,
			PrimaryType:    release.ReleaseGroup.PrimaryType,
		}
		if groupID, err := uuid.Parse(release.ReleaseGroup.ID); err == nil {
			decoded.ReleaseGroupID = groupID
		}
		decoded.DiscNumber, decoded.TrackNumber = releasePosition(release)
		recording.Releases = append(recording.Releases, decoded)
	}
	sort.SliceStable(recording.Releases, func(left, right int) bool {
		a, b := recording.Releases[left], recording.Releases[right]
		aOfficial := strings.EqualFold(a.Status, "official")
		bOfficial := strings.EqualFold(b.Status, "official")
		if aOfficial != bOfficial {
			return aOfficial
		}
		if normalizedReleaseDate(a.Date) != normalizedReleaseDate(b.Date) {
			return normalizedReleaseDate(a.Date) < normalizedReleaseDate(b.Date)
		}
		return a.ID.String() < b.ID.String()
	})
	return recording, true
}

// releasePosition reads where the recording sits on one release, when the
// provider says. Search results carry it; a plain lookup does not, and an
// absent position is silence rather than position zero.
func releasePosition(release releaseWithMedia) (disc, track int) {
	for _, medium := range release.Media {
		for _, item := range medium.Tracks {
			number := item.Position
			if number < 1 {
				number, _ = strconv.Atoi(strings.TrimSpace(item.Number))
			}
			if number < 1 {
				continue
			}
			position := medium.Position
			if position < 1 {
				position = 1
			}
			return position, number
		}
	}
	return 0, 0
}

// artistCredit joins a credit the way the provider prints it, so a track
// credited to two artists reads as one name rather than as the first of them.
func artistCredit(credits []artistCreditItem) string {
	var builder strings.Builder
	for _, credit := range credits {
		name := strings.TrimSpace(credit.Name)
		if name == "" {
			name = strings.TrimSpace(credit.Artist.Name)
		}
		builder.WriteString(name)
		builder.WriteString(credit.JoinPhrase)
	}
	return strings.TrimSpace(builder.String())
}

// artistCredits keeps the credit as the list the provider sent it as, so a
// track credited to two artists can be read back as two rather than as one
// artist called both their names.
//
// It walks the same entries as artistCredit and resolves each name the same
// way, and that is the point: joining Name and JoinPhrase back up in order and
// trimming has to reproduce artistCredit's string exactly, because this list is
// a decomposition of that string and not a second opinion about it. So nothing
// is dropped and nothing is reordered — an entry left out would take its join
// phrase with it, and the two readings would stop agreeing with no way to tell
// which one was wrong.
func artistCredits(credits []artistCreditItem) []Credit {
	if len(credits) == 0 {
		return nil
	}
	list := make([]Credit, 0, len(credits))
	for _, credit := range credits {
		name := strings.TrimSpace(credit.Name)
		if name == "" {
			name = strings.TrimSpace(credit.Artist.Name)
		}
		entry := Credit{
			Name:       name,
			JoinPhrase: credit.JoinPhrase,
			ArtistName: strings.TrimSpace(credit.Artist.Name),
		}
		for _, alias := range credit.Artist.Aliases {
			if value := strings.TrimSpace(alias.Name); value != "" {
				entry.Aliases = append(entry.Aliases, value)
			}
		}
		if id, err := uuid.Parse(credit.Artist.ID); err == nil {
			entry.ArtistID = id
		}
		list = append(list, entry)
	}
	return list
}

type recordingSearchResponse struct {
	Count      int             `json:"count"`
	Recordings []recordingItem `json:"recordings"`
}

type recordingItem struct {
	ID             string             `json:"id"`
	Title          string             `json:"title"`
	Disambiguation string             `json:"disambiguation"`
	Length         *int               `json:"length"`
	Score          score              `json:"score"`
	ISRCs          []string           `json:"isrcs"`
	ArtistCredit   []artistCreditItem `json:"artist-credit"`
	Releases       []releaseWithMedia `json:"releases"`
}

type artistCreditItem struct {
	Name       string `json:"name"`
	JoinPhrase string `json:"joinphrase"`
	Artist     struct {
		ID             string `json:"id"`
		Name           string `json:"name"`
		SortName       string `json:"sort-name"`
		Disambiguation string `json:"disambiguation"`
		// Aliases arrive only where the lookup asked for them. The sort name is
		// deliberately not read as one: "Scott, Travis" is a filing order rather
		// than a name anybody writes into a tag.
		Aliases []struct {
			Name string `json:"name"`
		} `json:"aliases"`
	} `json:"artist"`
}

type releaseWithMedia struct {
	ID             string `json:"id"`
	Title          string `json:"title"`
	Disambiguation string `json:"disambiguation"`
	Status         string `json:"status"`
	Date           string `json:"date"`
	ReleaseGroup   struct {
		ID          string `json:"id"`
		PrimaryType string `json:"primary-type"`
	} `json:"release-group"`
	Media []struct {
		Position int `json:"position"`
		Tracks   []struct {
			Position int    `json:"position"`
			Number   string `json:"number"`
			Title    string `json:"title"`
		} `json:"track"`
	} `json:"media"`
}
