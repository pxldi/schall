// Package labels keeps the catalogue's account of what a record label has
// published.
//
// A label is a company that publishes music. MusicBrainz holds one as an entity
// with a stable identifier, and a person follows it in Schall the way they
// follow an artist: a standing request to keep what it publishes complete. This
// package is the half of that request that talks to MusicBrainz — it walks the
// releases the label published and brings each one into the catalogue as an
// ordinary release group, filed under the artist MusicBrainz credits it to.
//
// It decides nothing about any file. Bringing a release into the catalogue only
// makes it something the follow feed can want; every copy that is later fetched
// is still proven by its audio before it enters the library.
package labels

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/musicbrainz"
	"github.com/rs/zerolog"
)

// pageSize is how many releases one request asks for. It is the largest page
// MusicBrainz serves, so a walk of a given catalogue costs as few requests as
// it can.
const pageSize = 100

// pagesPerPass is how many of those requests one pass makes before it stops and
// asks to be continued.
//
// MusicBrainz answers one question a second, through a limiter this shares with
// interactive search. A label with four thousand releases is forty requests: run
// in one pass they would hold a worker for forty seconds and put every search a
// person types behind them. Three pages is a few seconds, and the pass leaves
// its place on the label's own row, so the next pass carries straight on.
const pagesPerPass = 3

// Store is what a refresh reads and writes, and nothing else.
type Store interface {
	LabelForRefresh(context.Context, uuid.UUID) (db.LabelForRefreshRow, error)
	LinkLabelRelease(context.Context, db.LinkLabelReleaseParams) error
	AdvanceLabelRefresh(context.Context, db.AdvanceLabelRefreshParams) error
	CompleteLabelRefresh(context.Context, uuid.UUID) error
	QueueLabelRefresh(context.Context, uuid.UUID) (db.QueueLabelRefreshRow, error)
}

// Provider is the metadata service the releases are read from.
type Provider interface {
	LabelReleases(
		ctx context.Context, id uuid.UUID, offset, limit int,
	) (musicbrainz.LabelReleasePage, error)
}

// Catalogue takes one release group into the catalogue and answers with the
// album row it became. It is the same call the ingest job makes for the release
// behind a proven file, so a release reached through a label is filed exactly
// as any other release is: under an artist held rather than followed, with the
// refresh that fetches its tracks queued behind it.
type Catalogue interface {
	SaveCatalogueReleaseGroup(context.Context, musicbrainz.ReleaseGroupDetail) (uuid.UUID, error)
}

// ErrUnknownLabel reports a label MusicBrainz will not answer about. It is an
// answer and not a failure: the identifier was chosen from a MusicBrainz search,
// so one that has since been merged away is worth recording and not worth
// asking about again.
var ErrUnknownLabel = errors.New("MusicBrainz does not know that label")

// Refresher walks a label's releases, a bounded number of pages at a time.
type Refresher struct {
	store     Store
	provider  Provider
	catalogue Catalogue
	logger    zerolog.Logger
}

func New(store Store, provider Provider, catalogue Catalogue, logger zerolog.Logger) *Refresher {
	return &Refresher{store: store, provider: provider, catalogue: catalogue, logger: logger}
}

// Refresh does one pass over a label's releases and reports whether there are
// more pages waiting.
//
// The place it reached is written to the label's row after every page, so a
// pass stopped by a restart, a rate limit or a provider outage resumes at the
// page it got to rather than starting the walk again. Only a walk that reached
// the end sets the refresh date, because that date is what the follow feed
// reads to decide the label has been looked at.
func (refresher *Refresher) Refresh(ctx context.Context, labelID uuid.UUID) (bool, error) {
	label, err := refresher.store.LabelForRefresh(ctx, labelID)
	if err != nil {
		return false, fmt.Errorf("read label %s: %w", labelID, err)
	}
	// An unfollow can land between one pass queuing the next and that pass
	// running. Reading the label here rather than trusting the caller is what
	// stops a walk over a label nobody follows any more: no MusicBrainz
	// request, and nothing left to requeue.
	if !label.FollowedAt.Valid {
		refresher.logger.Info().
			Str("label_id", labelID.String()).
			Str("label", label.Name).
			Msg("label was unfollowed; the walk over its releases stops here")
		return false, nil
	}

	offset := int(label.ReleasesOffset)
	ingested := 0
	for page := 0; page < pagesPerPass; page++ {
		read, total, count, err := refresher.readPage(ctx, labelID, label.MusicbrainzID, offset)
		if err != nil {
			return false, err
		}
		ingested += count
		offset += read
		// The end of the walk, either because the page came back empty or
		// because everything the label has was read.
		if read == 0 || offset >= total {
			if err := refresher.store.CompleteLabelRefresh(ctx, labelID); err != nil {
				return false, fmt.Errorf("complete refresh of label %s: %w", labelID, err)
			}
			refresher.logger.Info().
				Str("label_id", labelID.String()).
				Str("label", label.Name).
				Int("releases_read", offset).
				Int("release_groups_ingested", ingested).
				Msg("walked a label's releases to the end")
			return false, nil
		}
		if err := refresher.store.AdvanceLabelRefresh(ctx, db.AdvanceLabelRefreshParams{
			LabelID: labelID, ReleasesOffset: int32(offset),
		}); err != nil {
			return false, fmt.Errorf("record the place in label %s: %w", labelID, err)
		}
	}
	refresher.logger.Info().
		Str("label_id", labelID.String()).
		Str("label", label.Name).
		Int("releases_offset", offset).
		Int("release_groups_ingested", ingested).
		Msg("read a label's releases as far as one pass goes")
	return true, nil
}

// readPage brings one page of releases into the catalogue. It answers how many
// releases the page carried, how many the label has in all, and how many
// release groups this page put in the catalogue.
func (refresher *Refresher) readPage(
	ctx context.Context, labelID, musicBrainzID uuid.UUID, offset int,
) (int, int, int, error) {
	page, err := refresher.provider.LabelReleases(ctx, musicBrainzID, offset, pageSize)
	if errors.Is(err, musicbrainz.ErrNotFound) {
		return 0, 0, 0, fmt.Errorf("%w: %s", ErrUnknownLabel, musicBrainzID)
	}
	if err != nil {
		return 0, 0, 0, fmt.Errorf("read releases of label %s: %w", musicBrainzID, err)
	}
	ingested := 0
	for _, detail := range page.ReleaseGroups {
		albumID, err := refresher.catalogue.SaveCatalogueReleaseGroup(ctx, detail)
		if err != nil {
			return 0, 0, 0, fmt.Errorf("save release group %s: %w", detail.ReleaseGroup.ID, err)
		}
		if err := refresher.store.LinkLabelRelease(ctx, db.LinkLabelReleaseParams{
			LabelID: labelID, AlbumID: albumID,
		}); err != nil {
			return 0, 0, 0, fmt.Errorf("link release %s to label %s: %w", albumID, labelID, err)
		}
		ingested++
	}
	return page.Read, page.Total, ingested, nil
}

// QueueRefresh asks for the next pass over this label. Asking while a pass is
// already queued or running changes nothing.
func (refresher *Refresher) QueueRefresh(ctx context.Context, labelID uuid.UUID) error {
	if _, err := refresher.store.QueueLabelRefresh(ctx, labelID); err != nil {
		return fmt.Errorf("queue refresh of label %s: %w", labelID, err)
	}
	return nil
}
