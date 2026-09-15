package weekly

import (
	"context"

	"github.com/pxldi/schall/internal/navidrome"
)

// PlayerStars reads the star out of the user's player.
//
// It is the one place this package knows which player that is. Everything above
// it works in the three-valued Reading, so a second kind of player — or none at
// all — changes this file and nothing else.
type PlayerStars struct {
	keeps *navidrome.Keeps
}

// NewPlayerStars adapts the Navidrome reader to the reading this package
// decides on.
func NewPlayerStars(keeps *navidrome.Keeps) *PlayerStars {
	return &PlayerStars{keeps: keeps}
}

// Stars asks the player about every song at once and translates each answer.
//
// The translation is the whole file, and it is deliberately total: an answer
// this package does not recognise becomes Unreadable, which is the reading
// nothing is ever deleted on.
func (player *PlayerStars) Stars(ctx context.Context, songs []Song) (map[string]SignalRead, error) {
	asked := make([]navidrome.SongKeep, 0, len(songs))
	for _, song := range songs {
		asked = append(asked, navidrome.SongKeep{
			Path: song.Path, Artist: song.Artist, Title: song.Title,
		})
	}

	answers, err := player.keeps.Stars(ctx, asked)
	if err != nil {
		return nil, err
	}

	read := make(map[string]SignalRead, len(answers))
	for path, star := range answers {
		read[path] = SignalRead{Reading: readingFor(star.Reading), Detail: star.Detail}
	}
	return read, nil
}

func readingFor(reading navidrome.Reading) Reading {
	switch reading {
	case navidrome.Present:
		return Present
	case navidrome.Absent:
		return Absent
	default:
		return Unreadable
	}
}
