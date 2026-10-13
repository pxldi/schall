package musicbrainz

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/google/uuid"
)

// RecordingLinks is what MusicBrainz says about where a recording can be heard
// outside MusicBrainz: the store and streaming pages linked to the recording
// itself, and the place it holds on each release it appears on, so a page
// linked to one of those releases can be read at the right track.
type RecordingLinks struct {
	Links      []URLRelation
	Placements []Placement
}

// Placement is one track a recording is on: the release, the medium's position
// on it and the track's position on that medium.
type Placement struct {
	ReleaseID uuid.UUID
	Status    string
	Medium    int
	Track     int
}

// ReleaseLinks is what MusicBrainz links one release to, with how many tracks
// each of its media holds, in medium order. A store page for the release is
// only read at a position when the store holds the same number of tracks.
type ReleaseLinks struct {
	Links       []URLRelation
	MediaTracks []int
}

// Tracks is how many tracks the release holds across its media.
func (release ReleaseLinks) Tracks() int {
	total := 0
	for _, count := range release.MediaTracks {
		total += count
	}
	return total
}

// RecordingLinks looks up the pages MusicBrainz links one recording to, and the
// tracks it sits on.
//
// Every link is an editor's statement about this recording or one of its
// releases. It is a key in the same sense an ISRC is, and nothing is searched
// for by name here.
func (client *Client) RecordingLinks(ctx context.Context, id uuid.UUID) (RecordingLinks, error) {
	if id == uuid.Nil {
		return RecordingLinks{}, errors.New("recording MusicBrainz ID is required")
	}
	var payload struct {
		Relations []relationItem `json:"relations"`
		Releases  []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Media  []struct {
				Position int `json:"position"`
				Tracks   []struct {
					Position int `json:"position"`
				} `json:"tracks"`
			} `json:"media"`
		} `json:"releases"`
	}
	_, err := client.getJSON(ctx, "/recording/"+id.String(), url.Values{
		"fmt": []string{"json"},
		"inc": []string{"url-rels+releases+media"},
	}, &payload)
	var httpErr *HTTPError
	if errors.As(err, &httpErr) && httpErr.StatusCode == 404 {
		return RecordingLinks{}, ErrNotFound
	}
	if err != nil {
		return RecordingLinks{}, fmt.Errorf("look up MusicBrainz recording links: %w", err)
	}
	links := RecordingLinks{Links: urlRelations(payload.Relations)}
	for _, release := range payload.Releases {
		releaseID, err := uuid.Parse(release.ID)
		if err != nil {
			continue
		}
		for _, medium := range release.Media {
			for _, track := range medium.Tracks {
				if medium.Position <= 0 || track.Position <= 0 {
					continue
				}
				links.Placements = append(links.Placements, Placement{
					ReleaseID: releaseID, Status: release.Status,
					Medium: medium.Position, Track: track.Position,
				})
			}
		}
	}
	return links, nil
}

// ReleaseLinks looks up the pages MusicBrainz links one release to.
func (client *Client) ReleaseLinks(ctx context.Context, id uuid.UUID) (ReleaseLinks, error) {
	if id == uuid.Nil {
		return ReleaseLinks{}, errors.New("release MusicBrainz ID is required")
	}
	var payload struct {
		Relations []relationItem `json:"relations"`
		Media     []struct {
			Position   int `json:"position"`
			TrackCount int `json:"track-count"`
		} `json:"media"`
	}
	_, err := client.getJSON(ctx, "/release/"+id.String(), url.Values{
		"fmt": []string{"json"},
		"inc": []string{"url-rels"},
	}, &payload)
	var httpErr *HTTPError
	if errors.As(err, &httpErr) && httpErr.StatusCode == 404 {
		return ReleaseLinks{}, ErrNotFound
	}
	if err != nil {
		return ReleaseLinks{}, fmt.Errorf("look up MusicBrainz release links: %w", err)
	}
	release := ReleaseLinks{Links: urlRelations(payload.Relations)}
	for index, medium := range payload.Media {
		// Media arrive in order. A position that disagrees with the order would
		// make every count after it answer for the wrong medium, so the counts
		// are not given at all.
		if medium.Position != index+1 {
			release.MediaTracks = nil
			break
		}
		release.MediaTracks = append(release.MediaTracks, medium.TrackCount)
	}
	return release, nil
}

type relationItem struct {
	Type string `json:"type"`
	URL  struct {
		Resource string `json:"resource"`
	} `json:"url"`
}

func urlRelations(items []relationItem) []URLRelation {
	var found []URLRelation
	for _, relation := range items {
		if resource := strings.TrimSpace(relation.URL.Resource); resource != "" {
			found = append(found, URLRelation{Type: relation.Type, Resource: resource})
		}
	}
	return found
}
