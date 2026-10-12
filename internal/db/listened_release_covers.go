package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ListenedReleaseCover is the picture held for a release somebody listened
// to, by its MusicBrainz release ID. Found is false when nobody has asked the
// archive yet, or the answer "there is none" is old enough to ask again; an
// empty Image with Found true is that answer, still fresh.
type ListenedReleaseCover struct {
	Found       bool
	Image       []byte
	ContentType string
}

// ListenedReleaseCover reads the picture held for one release ID.
func (q *Queries) ListenedReleaseCover(ctx context.Context, releaseID uuid.UUID) (ListenedReleaseCover, error) {
	var out ListenedReleaseCover
	err := q.db.QueryRow(ctx, `
		SELECT coalesce(image, ''::bytea), content_type
		FROM listened_release_covers
		WHERE musicbrainz_release_id = $1
		  AND (image IS NOT NULL OR fetched_at > now() - interval '30 days')`,
		releaseID).Scan(&out.Image, &out.ContentType)
	if errors.Is(err, pgx.ErrNoRows) {
		return ListenedReleaseCover{}, nil
	}
	if err != nil {
		return ListenedReleaseCover{}, fmt.Errorf("read the listened cover %s: %w", releaseID, err)
	}
	out.Found = true
	return out, nil
}

// SaveListenedReleaseCover records what the archive answered for one release
// ID. An empty image records that it has none.
func (q *Queries) SaveListenedReleaseCover(
	ctx context.Context, releaseID uuid.UUID, image []byte, contentType, source string,
) error {
	var stored any
	if len(image) > 0 {
		stored = image
	} else {
		contentType = ""
	}
	_, err := q.db.Exec(ctx, `
		INSERT INTO listened_release_covers (musicbrainz_release_id, image, content_type, source)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (musicbrainz_release_id) DO UPDATE
		SET image = EXCLUDED.image, content_type = EXCLUDED.content_type,
		    source = EXCLUDED.source, fetched_at = now()`,
		releaseID, stored, contentType, source)
	if err != nil {
		return fmt.Errorf("save the listened cover %s: %w", releaseID, err)
	}
	return nil
}
