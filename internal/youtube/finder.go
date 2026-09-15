package youtube

import (
	"context"
	"errors"
	"fmt"

	"github.com/pxldi/schall/internal/anchor"
)

// Finder adapts the client to what the anchor service asks of an upload
// search. The two packages keep their own types so that internal/anchor never
// imports yt-dlp, and the sentences the anchor records are the anchor's own.
type Finder struct {
	client *Client
}

// NewFinder wraps a client for the anchor service.
func NewFinder(client *Client) *Finder {
	return &Finder{client: client}
}

// Available reports whether yt-dlp can be run at all.
func (finder *Finder) Available() bool {
	return finder.client.Available()
}

// Search returns up to limit uploads for a free-text query.
func (finder *Finder) Search(ctx context.Context, query string, limit int) ([]anchor.Upload, error) {
	videos, err := finder.client.Search(ctx, query, limit)
	if err != nil {
		return nil, translate(err)
	}
	uploads := make([]anchor.Upload, 0, len(videos))
	for _, video := range videos {
		uploads = append(uploads, anchor.Upload{
			ID: video.ID, Title: video.Title, Channel: video.Channel,
			Seconds: video.Seconds, Views: video.Views,
		})
	}
	return uploads, nil
}

// Excerpt returns thirty seconds from the middle of the upload as MP3 bytes.
func (finder *Finder) Excerpt(ctx context.Context, id string, durationSeconds int) ([]byte, error) {
	audio, err := finder.client.Excerpt(ctx, id, durationSeconds)
	if err != nil {
		return nil, translate(err)
	}
	return audio, nil
}

// translate turns the client's sentinels into the anchor's. Anything else is
// passed through, which the anchor reads as a fetch to try again.
func translate(err error) error {
	switch {
	case errors.Is(err, ErrUnavailable):
		return fmt.Errorf("%w: %w", anchor.ErrFinderUnavailable, err)
	case errors.Is(err, ErrRateLimited):
		return fmt.Errorf("%w: %w", anchor.ErrFinderRateLimited, err)
	case errors.Is(err, ErrNoVideo):
		return fmt.Errorf("%w: %w", anchor.ErrNoUpload, err)
	default:
		return err
	}
}
