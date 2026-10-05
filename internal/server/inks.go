package server

import (
	"context"

	"github.com/google/uuid"
)

// inksResponse is the two inks a release's page is printed in, read from its
// cover (ADR on Duoton). Both are already clamped for legibility. Palette is the
// cover's colours, most common first, and only a release's own page carries it.
//
// A release with no cover, or a cover nothing could be read from, has no inks:
// the field is null and the page uses the house inks.
type inksResponse struct {
	Dark    string   `json:"dark"`
	Light   string   `json:"light"`
	Palette []string `json:"palette,omitempty"`
}

// releaseInks reads the inks of the given releases in one query. Inks are
// decoration, so a failed read is logged and every release answers with none
// rather than failing the page that asked.
func (api *API) releaseInks(ctx context.Context, albumIDs []uuid.UUID) map[uuid.UUID]inksResponse {
	inks := make(map[uuid.UUID]inksResponse, len(albumIDs))
	if api.covers == nil || len(albumIDs) == 0 {
		return inks
	}
	rows, err := api.covers.ReleaseInks(ctx, albumIDs)
	if err != nil {
		api.logger.Warn().Err(err).Int("releases", len(albumIDs)).Msg("read release inks")
		return inks
	}
	for _, row := range rows {
		inks[row.AlbumID] = inksResponse{Dark: row.InkDark, Light: row.InkLight, Palette: row.Palette}
	}
	return inks
}

// rowInks is the inks a list row carries: the two inks without the palette, or
// nil for a release that has none.
func rowInks(inks map[uuid.UUID]inksResponse, albumID uuid.UUID) *inksResponse {
	found, ok := inks[albumID]
	if !ok {
		return nil
	}
	return &inksResponse{Dark: found.Dark, Light: found.Light}
}
