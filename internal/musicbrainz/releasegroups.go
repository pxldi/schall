package musicbrainz

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/google/uuid"
)

// ReleaseGroupDetail is one release group together with the artist it is
// credited to. Resolution proves what a file is, and what it proves is a
// recording on a release group; the artist has to be looked up before the
// release can be held anywhere.
type ReleaseGroupDetail struct {
	ReleaseGroup ReleaseGroup
	// Artist is the first credited artist. A release group credited to several
	// artists is filed under the first of them, exactly as one browsed from an
	// artist's own catalogue is, because an album belongs to one artist row.
	// Credit records the full credit as MusicBrainz writes it, so a
	// collaboration can still be read back as one, and Credits records the list
	// that credit was written from, so it can be read back as several.
	Artist  Artist
	Credit  string
	Credits []Credit
}

// ReleaseGroup looks one release group up by its MusicBrainz ID, with the
// artist it is credited to.
func (client *Client) ReleaseGroup(ctx context.Context, id uuid.UUID) (ReleaseGroupDetail, error) {
	if id == uuid.Nil {
		return ReleaseGroupDetail{}, errors.New("release group MusicBrainz ID is required")
	}

	var payload releaseGroupLookupResponse
	metadata, err := client.getJSON(ctx, "/release-group/"+id.String(), url.Values{
		"fmt": []string{"json"},
		// Genres ride along on the lookup this refresh already makes, so a
		// release group is genred without a second request.
		"inc": []string{"artist-credits+genres"},
	}, &payload)
	var httpErr *HTTPError
	if errors.As(err, &httpErr) && httpErr.StatusCode == 404 {
		return ReleaseGroupDetail{}, ErrNotFound
	}
	if err != nil {
		return ReleaseGroupDetail{}, fmt.Errorf("look up MusicBrainz release group: %w", err)
	}

	title := strings.TrimSpace(payload.Title)
	if title == "" {
		return ReleaseGroupDetail{}, ErrNotFound
	}
	artist, ok := creditedArtist(payload.ArtistCredit)
	if !ok {
		// A release group Schall cannot attribute cannot be filed, and inventing
		// an artist for it would put music under a name nobody proved.
		return ReleaseGroupDetail{}, ErrNotFound
	}

	return ReleaseGroupDetail{
		ReleaseGroup: ReleaseGroup{
			ID:               id,
			Title:            title,
			FirstReleaseDate: payload.FirstReleaseDate,
			PrimaryType:      payload.PrimaryType,
			SecondaryTypes:   payload.SecondaryTypes,
			Genres:           topGenres(payload.Genres),
			// The lookup response is stored as it arrived, like every other
			// metadata source. It carries first-release-date, which is what the
			// album views read back out of it.
			Metadata: metadata,
		},
		Artist:  artist,
		Credit:  artistCredit(payload.ArtistCredit),
		Credits: artistCredits(payload.ArtistCredit),
	}, nil
}

// creditedArtist takes the first credited artist that identifies itself. A
// credit without an ID names nobody Schall can hold a release under.
func creditedArtist(credits []artistCreditItem) (Artist, bool) {
	for _, credit := range credits {
		id, err := uuid.Parse(credit.Artist.ID)
		if err != nil {
			continue
		}
		name := strings.TrimSpace(credit.Artist.Name)
		if name == "" {
			continue
		}
		sortName := strings.TrimSpace(credit.Artist.SortName)
		if sortName == "" {
			sortName = name
		}
		return Artist{
			ID:             id,
			Name:           name,
			SortName:       sortName,
			Disambiguation: strings.TrimSpace(credit.Artist.Disambiguation),
		}, true
	}
	return Artist{}, false
}

type releaseGroupLookupResponse struct {
	ID               string             `json:"id"`
	Title            string             `json:"title"`
	FirstReleaseDate string             `json:"first-release-date"`
	PrimaryType      string             `json:"primary-type"`
	SecondaryTypes   []string           `json:"secondary-types"`
	Genres           []genreItem        `json:"genres"`
	ArtistCredit     []artistCreditItem `json:"artist-credit"`
}
