package navidrome

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pxldi/schall/internal/db"
	"github.com/rs/zerolog"
)

// starsTimeout bounds one reading. A reading asks the player twice per song —
// once to find which song the file is, once to read the star off it — so a
// batch is a run of small requests rather than one long wait. It is the budget
// a sync pass gets for the same shape of work.
const starsTimeout = 5 * time.Minute

// Reading is what one look at one signal in the player returned. The three
// values are the whole point of this type: "not starred" and "could not ask"
// must never collapse into one answer, because a song nobody kept is deleted
// and a song Schall could not ask about must not be.
type Reading int

const (
	// Unreadable is the question not asked or not answered. It is the zero
	// value on purpose, so that a Star nobody filled in is the answer nothing
	// is deleted on.
	Unreadable Reading = iota
	// Absent is asked and answered: the user did not star the song.
	Absent
	// Present is asked and answered: the user starred the song.
	Present
)

func (reading Reading) String() string {
	switch reading {
	case Present:
		return "present"
	case Absent:
		return "absent"
	}
	// Unreadable, and anything that is not one of the two real answers. A value
	// from outside the three is not a fourth reading, and it must not read as
	// "the user did not keep this".
	return "unreadable"
}

// Star is one answer about one song.
type Star struct {
	Reading Reading
	// Detail is why the reading is unreadable, in words a person can act on. It
	// is empty when the answer is real.
	Detail string
}

// ErrNoPlayer is the refusal when there is nothing to ask.
//
// It is an error rather than an answer of Absent for every song, because an
// installation with no player has not told Schall that nobody keeps this music.
// It has told Schall nothing at all.
var ErrNoPlayer = errors.New("no navidrome player is configured, so the stars cannot be read")

// KeepsStore is where the player's connection lives.
type KeepsStore interface {
	NavidromeSettings(context.Context) (db.NavidromeSettingsRow, error)
}

// Keeps reads the keep signal out of the player.
//
// The signal is the star: the heart a person presses on a song in Feishin or
// Symfonium, which both write through the same Subsonic API into the same
// Navidrome account. Schall never writes it and only ever reads it, one song at
// a time, on the account the settings row names.
//
// Like the Notifier and the Syncer it builds a client per reading rather than
// holding one, because the settings are the operator's to change while Schall
// is running. The client introduces itself under SyncClientName, because
// finding the song for a file is done by comparing the path Navidrome reports
// against the path Schall holds, and a player row created before
// Subsonic.DefaultReportRealPath was turned on reports a made-up display path
// forever (ADR 0010).
type Keeps struct {
	store  KeepsStore
	paths  PathMap
	logger zerolog.Logger
}

func NewKeeps(store KeepsStore, paths PathMap, logger zerolog.Logger) *Keeps {
	return &Keeps{store: store, paths: paths, logger: logger}
}

// SongKeep is one file to ask about: the path Schall wrote it to, and what the
// tags say it is.
//
// The tags are how the player is asked, because Navidrome indexes the words in
// a song and not the place it sits. The path is what the answer is judged by,
// and it is the only thing that is. A file with no tags is still asked about —
// by its name, which is what the lookup falls back to — and is answered for
// less often, which is a thinner reading and never a looser one.
type SongKeep struct {
	Path   string
	Artist string
	Title  string
}

// Stars answers, for each library path given, whether the user starred that
// song in their player. The map is keyed by SongKeep.Path, as the caller gave
// it — the path Schall holds, not the one the player reported.
//
// The error is returned only when Schall could not ask at all: there is no
// player, it is switched off, or its settings could not be read. A song the
// player would not answer for is one Unreadable entry in the map and never an
// error, because the other songs in the batch were still answered and each
// answer is about one song.
//
// A path missing from the answer reads as Unreadable, that being the zero
// value of Star, so a caller cannot be handed a deletion by a key it forgot to
// look up.
//
// Every song is asked about one at a time rather than the account's whole
// starred list being read once. The list cannot answer this question: a song
// missing from a list of everything starred is either a song the user did not
// star or a song Navidrome does not hold, and telling those two apart is the
// only reason this type exists.
func (keeps *Keeps) Stars(ctx context.Context, songs []SongKeep) (map[string]Star, error) {
	wanted := distinctSongs(songs)
	if len(wanted) == 0 {
		return map[string]Star{}, nil
	}

	client, err := keeps.connect(ctx)
	if err != nil {
		return nil, err
	}

	bounded, cancel := context.WithTimeout(ctx, starsTimeout)
	defer cancel()

	stars := make(map[string]Star, len(wanted))
	counts := map[Reading]int{}
	for _, song := range wanted {
		// A reading that ran out of time is not a batch of songs nobody kept. It
		// is a batch that was never finished, so nothing of it is reported.
		if bounded.Err() != nil {
			return nil, fmt.Errorf("read the navidrome stars: %w", bounded.Err())
		}
		star := keeps.star(bounded, client, song)
		stars[song.Path] = star
		counts[star.Reading]++
	}

	keeps.logger.Debug().
		Int("asked", len(wanted)).
		Int("present", counts[Present]).
		Int("absent", counts[Absent]).
		Int("unreadable", counts[Unreadable]).
		Msg("read the stars from the player")
	return stars, nil
}

// star reads one signal: which song the player holds for this file, then
// whether the account starred that song.
//
// Every way of failing ends as Unreadable with the reason on it. The reason is
// carried rather than logged and dropped because the two causes look identical
// from the outside — a player that has not scanned yet and a player that would
// not answer — and only one of them is fixed by waiting.
func (keeps *Keeps) star(ctx context.Context, client *Client, song SongKeep) Star {
	remote := keeps.paths.ToRemote(song.Path)

	match, err := client.FindSongByPath(ctx, SongLookup{
		Path:   remote,
		Artist: song.Artist,
		Title:  song.Title,
	})
	if errors.Is(err, ErrNotFound) {
		// The heart of rule one. Navidrome not answering for the file is not the
		// user declining to keep it: nobody can star a song their player never
		// showed them.
		return Star{Reading: Unreadable, Detail: unreadableDetail(remote, err)}
	}
	if err != nil {
		return Star{Reading: Unreadable, Detail: fmt.Sprintf(
			"ask navidrome which song is at %s: %v", Escaped(remote), err)}
	}

	star, err := client.songStar(ctx, match.Song.ID)
	if err != nil {
		return Star{Reading: Unreadable, Detail: fmt.Sprintf(
			"read the star on navidrome song %s: %v", match.Song.ID, err)}
	}
	return star
}

// unreadableDetail says, in words a person can act on, why the player did not
// answer for this file.
//
// The two causes are told apart because only one of them is fixed by waiting. A
// search that reached nothing is a file the player has probably not indexed
// yet. A search that reached songs at other paths is the player holding another
// copy of this music, which is a different file and not this one — the lookup
// refuses it, and so must the reading: a star on somebody else's copy is not a
// statement about this file at all.
func unreadableDetail(remote string, err error) string {
	var miss *LookupMiss
	if errors.As(err, &miss) {
		switch miss.Reason {
		case MissNoPathMatch:
			return fmt.Sprintf(
				"navidrome answered with a different file: it holds songs matching those tags, "+
					"none of them at %s", Escaped(remote))
		case MissPathRepeated:
			return fmt.Sprintf("more than one navidrome song reports the path %s", Escaped(remote))
		}
	}
	return fmt.Sprintf(
		"navidrome does not have a song at %s; "+
			"it may not have looked at the library since schall wrote the file", Escaped(remote))
}

// connect builds the client for one reading from the settings as they are now.
func (keeps *Keeps) connect(ctx context.Context) (*Client, error) {
	row, err := keeps.store.NavidromeSettings(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoPlayer
	}
	if err != nil {
		return nil, fmt.Errorf("read navidrome settings: %w", err)
	}
	if !row.Enabled {
		return nil, ErrNoPlayer
	}
	return NewClient(Options{
		BaseURL:    row.BaseURL,
		Username:   row.Username,
		Password:   row.Password,
		ClientName: SyncClientName,
	})
}

// distinctSongs keeps the files in the order they were given and drops the
// repeats, so a batch naming one path twice asks the player about it once. The
// first mention wins, since the answer is keyed by the path and a second one
// would only overwrite it.
func distinctSongs(songs []SongKeep) []SongKeep {
	seen := make(map[string]struct{}, len(songs))
	kept := make([]SongKeep, 0, len(songs))
	for _, song := range songs {
		if _, repeat := seen[song.Path]; repeat {
			continue
		}
		seen[song.Path] = struct{}{}
		kept = append(kept, song)
	}
	return kept
}

// songStar asks the player about one song by id and reads the account's star
// off the answer.
//
// Subsonic reports a star as the time it was pressed and reports nothing at all
// when there is none, so a song described without the field is the real answer
// "this account did not star it" and never a failure to answer.
//
// The star is read here, from the call whose whole job is to describe one song,
// and not from the search that found the song. What a search result carries is
// the search's business and can change; reading "not starred" out of a field a
// search never fills in would delete music the user kept.
func (client *Client) songStar(ctx context.Context, id string) (Star, error) {
	payload, err := client.call(ctx, "getSong", url.Values{"id": {id}})
	if err != nil {
		return Star{}, client.describe(ctx, err)
	}
	if payload.Song == nil {
		return Star{}, fmt.Errorf("read navidrome song %s: %w", id, ErrNotFound)
	}
	if strings.TrimSpace(payload.Song.Starred) == "" {
		return Star{Reading: Absent}, nil
	}
	return Star{Reading: Present}, nil
}
