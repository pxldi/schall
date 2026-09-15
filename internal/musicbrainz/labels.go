package musicbrainz

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

// Label is a company that publishes music, as MusicBrainz holds it. Type,
// country and disambiguation are what tell two labels of the same name apart —
// there are several called "Warp" — and they are display only: a label is
// identified by its MusicBrainz ID and never by its name.
type Label struct {
	ID             uuid.UUID
	Name           string
	Type           string
	Country        string
	Area           string
	Disambiguation string
	Score          int
}

// LabelReleasePage is one page of what a label published, reduced to the
// release groups behind those releases.
//
// MusicBrainz browses releases, not release groups: one album pressed on CD,
// vinyl and cassette is three releases of one release group, and Schall files
// music by release group. So a page can carry fewer release groups than
// releases, and Read reports how many releases were read rather than how many
// groups came out — that number is what the next page's offset is counted in.
type LabelReleasePage struct {
	// Total is how many releases the label has in all, as MusicBrainz counts
	// them. It is what says whether the walk has reached the end.
	Total int
	// Read is how many releases this page carried.
	Read int
	// ReleaseGroups is what the page found, each with the artist the release
	// credits it to, in the order MusicBrainz returned them and without
	// repeats.
	ReleaseGroups []ReleaseGroupDetail
}

// SearchLabels asks MusicBrainz which labels answer to a name.
//
// It is a search, so it answers with candidates and never with a decision: the
// user picks the one they meant, and what is stored is that label's MusicBrainz
// ID.
func (client *Client) SearchLabels(ctx context.Context, query string, limit int) ([]Label, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("label search query is required")
	}
	if limit < 1 || limit > 100 {
		return nil, errors.New("label search limit must be between 1 and 100")
	}

	var payload labelSearchResponse
	if _, err := client.getJSON(ctx, "/label/", url.Values{
		"query": []string{query},
		"limit": []string{strconv.Itoa(limit)},
		"fmt":   []string{"json"},
	}, &payload); err != nil {
		return nil, fmt.Errorf("search MusicBrainz labels: %w", err)
	}

	labels := make([]Label, 0, len(payload.Labels))
	for _, item := range payload.Labels {
		id, err := uuid.Parse(item.ID)
		if err != nil || strings.TrimSpace(item.Name) == "" {
			continue
		}
		labels = append(labels, Label{
			ID:             id,
			Name:           strings.TrimSpace(item.Name),
			Type:           item.Type,
			Country:        item.Country,
			Area:           item.Area.Name,
			Disambiguation: item.Disambiguation,
			Score:          item.Score.Int(),
		})
	}
	return labels, nil
}

// LabelReleases reads one page of the releases a label published.
//
// One page, not the whole catalogue: a label can have thousands of releases,
// and MusicBrainz is asked one question a second by a limiter shared with
// everything else Schall asks it, including a person typing in a search box. So
// the caller walks the pages across passes and keeps its place, rather than
// holding a worker open for an hour.
//
// The artist credit comes off the release, because a browse prints it there and
// not on the release group. It is the credit MusicBrainz publishes for that
// release; nothing is inferred from the label.
func (client *Client) LabelReleases(
	ctx context.Context, id uuid.UUID, offset, limit int,
) (LabelReleasePage, error) {
	if id == uuid.Nil {
		return LabelReleasePage{}, errors.New("label MusicBrainz ID is required")
	}
	if limit < 1 || limit > 100 {
		return LabelReleasePage{}, errors.New("label release limit must be between 1 and 100")
	}
	if offset < 0 {
		return LabelReleasePage{}, errors.New("label release offset cannot be negative")
	}

	var payload labelReleaseBrowseResponse
	_, err := client.getJSON(ctx, "/release", url.Values{
		"label":  []string{id.String()},
		"inc":    []string{"release-groups+artist-credits"},
		"fmt":    []string{"json"},
		"limit":  []string{strconv.Itoa(limit)},
		"offset": []string{strconv.Itoa(offset)},
	}, &payload)
	var httpErr *HTTPError
	if errors.As(err, &httpErr) && httpErr.StatusCode == 404 {
		return LabelReleasePage{}, ErrNotFound
	}
	if err != nil {
		return LabelReleasePage{}, fmt.Errorf("browse MusicBrainz releases of a label: %w", err)
	}

	page := LabelReleasePage{Total: payload.Count, Read: len(payload.Releases)}
	seen := make(map[uuid.UUID]struct{}, len(payload.Releases))
	for _, release := range payload.Releases {
		groupID, err := uuid.Parse(release.ReleaseGroup.ID)
		if err != nil {
			continue
		}
		title := strings.TrimSpace(release.ReleaseGroup.Title)
		if title == "" {
			continue
		}
		if _, repeat := seen[groupID]; repeat {
			continue
		}
		// A release group Schall cannot attribute cannot be filed, and
		// inventing an artist for it would put music under a name nobody
		// proved.
		artist, ok := creditedArtist(release.ArtistCredit)
		if !ok {
			continue
		}
		metadata, err := json.Marshal(release.ReleaseGroup)
		if err != nil {
			return LabelReleasePage{}, fmt.Errorf("encode MusicBrainz release group metadata: %w", err)
		}
		seen[groupID] = struct{}{}
		page.ReleaseGroups = append(page.ReleaseGroups, ReleaseGroupDetail{
			ReleaseGroup: ReleaseGroup{
				ID:               groupID,
				Title:            title,
				FirstReleaseDate: release.ReleaseGroup.FirstReleaseDate,
				PrimaryType:      release.ReleaseGroup.PrimaryType,
				SecondaryTypes:   release.ReleaseGroup.SecondaryTypes,
				Metadata:         metadata,
			},
			Artist:  artist,
			Credit:  artistCredit(release.ArtistCredit),
			Credits: artistCredits(release.ArtistCredit),
		})
	}
	return page, nil
}

type labelSearchResponse struct {
	Labels []struct {
		ID             string `json:"id"`
		Name           string `json:"name"`
		Type           string `json:"type"`
		Country        string `json:"country"`
		Disambiguation string `json:"disambiguation"`
		Score          score  `json:"score"`
		Area           struct {
			Name string `json:"name"`
		} `json:"area"`
	} `json:"labels"`
}

type labelReleaseBrowseResponse struct {
	Count    int `json:"release-count"`
	Releases []struct {
		ID           string             `json:"id"`
		Title        string             `json:"title"`
		Date         string             `json:"date"`
		Status       string             `json:"status"`
		ArtistCredit []artistCreditItem `json:"artist-credit"`
		ReleaseGroup releaseGroupItem   `json:"release-group"`
	} `json:"releases"`
}
