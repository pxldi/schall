package musicbrainz

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"

	"github.com/google/uuid"
)

// DiscPlacement is one track a recording is on, with the tables of contents
// people read off CDs of that medium and submitted to MusicBrainz.
type DiscPlacement struct {
	ReleaseID uuid.UUID
	Medium    int
	Track     int
	Discs     []Disc
}

// Disc is one table of contents read off a CD: the sector each track starts
// at and the sector the lead-out starts at, counted the way a drive reports
// them, including the two-second lead-in.
type Disc struct {
	ID      string
	Offsets []int
	Sectors int
}

// discID is the shape of a MusicBrainz disc ID. It is checked because it goes
// into the URL path.
var discID = regexp.MustCompile(`^[A-Za-z0-9._]{27}-$`)

// RecordingDiscs lists the tracks a recording is on whose medium carries at
// least one table of contents read off a CD.
func (client *Client) RecordingDiscs(ctx context.Context, id uuid.UUID) ([]DiscPlacement, error) {
	if id == uuid.Nil {
		return nil, errors.New("recording MusicBrainz ID is required")
	}
	var payload struct {
		Releases []struct {
			ID    string `json:"id"`
			Media []struct {
				Position int `json:"position"`
				Tracks   []struct {
					Position int `json:"position"`
				} `json:"tracks"`
				Discs []struct {
					ID      string `json:"id"`
					Offsets []int  `json:"offsets"`
					Sectors int    `json:"sectors"`
				} `json:"discs"`
			} `json:"media"`
		} `json:"releases"`
	}
	_, err := client.getJSON(ctx, "/recording/"+id.String(), url.Values{
		"fmt": []string{"json"},
		"inc": []string{"releases+media+discids"},
	}, &payload)
	var httpErr *HTTPError
	if errors.As(err, &httpErr) && httpErr.StatusCode == 404 {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("look up MusicBrainz recording discs: %w", err)
	}
	var placements []DiscPlacement
	for _, release := range payload.Releases {
		releaseID, err := uuid.Parse(release.ID)
		if err != nil {
			continue
		}
		for _, medium := range release.Media {
			if len(medium.Discs) == 0 {
				continue
			}
			discs := make([]Disc, 0, len(medium.Discs))
			for _, disc := range medium.Discs {
				discs = append(discs, Disc{ID: disc.ID, Offsets: disc.Offsets, Sectors: disc.Sectors})
			}
			for _, track := range medium.Tracks {
				placements = append(placements, DiscPlacement{
					ReleaseID: releaseID, Medium: medium.Position, Track: track.Position, Discs: discs,
				})
			}
		}
	}
	return placements, nil
}

// RecordingsAtDiscTrack lists the recording at one track position on every
// medium MusicBrainz files a disc ID under. A position a medium does not have
// is listed as uuid.Nil, so a caller can tell a medium that disagrees from one
// that is silent.
func (client *Client) RecordingsAtDiscTrack(ctx context.Context, id string, track int) ([]uuid.UUID, error) {
	if !discID.MatchString(id) {
		return nil, fmt.Errorf("%q is not a MusicBrainz disc ID", id)
	}
	var payload struct {
		Releases []struct {
			Media []struct {
				Discs []struct {
					ID string `json:"id"`
				} `json:"discs"`
				Tracks []struct {
					Position  int `json:"position"`
					Recording struct {
						ID string `json:"id"`
					} `json:"recording"`
				} `json:"tracks"`
			} `json:"media"`
		} `json:"releases"`
	}
	_, err := client.getJSON(ctx, "/discid/"+id, url.Values{
		"fmt": []string{"json"},
		"inc": []string{"recordings"},
	}, &payload)
	var httpErr *HTTPError
	if errors.As(err, &httpErr) && httpErr.StatusCode == 404 {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("look up MusicBrainz disc %s: %w", id, err)
	}
	var recordings []uuid.UUID
	for _, release := range payload.Releases {
		for _, medium := range release.Media {
			carries := false
			for _, disc := range medium.Discs {
				carries = carries || disc.ID == id
			}
			if !carries {
				continue
			}
			found := uuid.Nil
			for _, candidate := range medium.Tracks {
				if candidate.Position == track {
					found, _ = uuid.Parse(candidate.Recording.ID)
				}
			}
			recordings = append(recordings, found)
		}
	}
	return recordings, nil
}
