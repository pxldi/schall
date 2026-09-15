package coverart

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pxldi/schall/internal/db"
	"github.com/rs/zerolog"
)

const (
	// sweepBatch is how many releases one pass pictures. The pass reports whether
	// more are waiting, so this is how often the work checks back rather than how
	// much of it ever gets done.
	sweepBatch = 25
	// perRelease bounds one release's or one artist's worth of asking. Every
	// client here carries its own request timeout already, but a pass is a run of
	// requests and a redirect chain, and one release that never finishes would
	// wedge the sweep for as long as the process lives — which is exactly what a
	// deployment did: a pass sat claimed for a quarter of an hour having stopped
	// mid-batch. So the work for one row is bounded here as well, and a row that
	// exhausts it is skipped rather than recorded, because nothing was learned
	// about it and the next pass will ask again.
	perRelease = 30 * time.Second
	// consecutiveFailures is how many rows in a row may fail before the pass gives
	// up. One release nothing can answer about must not stop the catalogue being
	// pictured; an archive that is down must not be asked about every remaining
	// release and then immediately asked again, which is what a pass that skipped
	// everything and reported success would do.
	consecutiveFailures = 3
	// betweenRequests is the pace. Neither archive publishes a limit, and neither
	// is being paid: a picture is worth having and not worth hammering somebody's
	// server for, so a catalogue fills over minutes rather than in a burst.
	betweenRequests = time.Second
)

// Store is the persistence the sweep needs, and nothing else.
type Store interface {
	ReleasesMissingCoverArt(context.Context, int32) ([]db.ReleasesMissingCoverArtRow, error)
	SaveReleaseCoverArt(context.Context, db.SaveReleaseCoverArtParams) error
	ArtistsMissingImage(context.Context, db.ArtistsMissingImageParams) ([]db.ArtistsMissingImageRow, error)
	SaveArtistImage(context.Context, db.SaveArtistImageParams) error
	ArtistsMissingBiography(context.Context, int32) ([]db.ArtistsMissingBiographyRow, error)
	SaveArtistBiography(context.Context, db.SaveArtistBiographyParams) error
}

// Sweeper fills the cache, and is the only thing that talks to an archive on its
// own initiative.
//
// It exists so the rate is a decision. A page of a hundred releases rendering a
// hundred remote images would be a hundred requests made on the user's behalf, at
// whatever speed their browser felt like; this asks one archive at a time, a
// second apart, and every page afterwards is served from here.
type Sweeper struct {
	store   Store
	sources Sources
	// artists pictures the people rather than the records. Optional: without it
	// an artist simply has no face, which is what they had before.
	artists *Artists
	// biographies says who those people are. Optional in the same way: without
	// it an artist page shows what the library holds and not a word about them.
	biographies *Biographies
	// disk writes the same pictures beside the music, where the playback server
	// can read them. Optional: without it the pictures stay in the cache and
	// Schall's own pages are unaffected.
	disk   PictureWriter
	logger zerolog.Logger
	// pause is a field so a test does not wait out the real pace.
	pause time.Duration
}

func NewSweeper(store Store, sources Sources, logger zerolog.Logger) *Sweeper {
	return &Sweeper{store: store, sources: sources, logger: logger, pause: betweenRequests}
}

// PictureWriter puts the cached pictures on disc and reports whether more are
// waiting. Its own package doc says why that is worth doing.
type PictureWriter interface {
	Write(context.Context) (bool, error)
}

// WithDisk registers what writes the cached pictures beside the music.
func (sweeper *Sweeper) WithDisk(disk PictureWriter) *Sweeper {
	sweeper.disk = disk
	return sweeper
}

// WithArtists registers where artist pictures come from.
func (sweeper *Sweeper) WithArtists(artists *Artists) *Sweeper {
	sweeper.artists = artists
	return sweeper
}

// WithBiographies registers where the words about an artist come from.
func (sweeper *Sweeper) WithBiographies(biographies *Biographies) *Sweeper {
	sweeper.biographies = biographies
	return sweeper
}

// Sweep pictures the releases nobody has looked for a picture of yet, and reports
// whether any were left over.
//
// A release the archives have no picture of is recorded as having none, which is
// what stops it being asked about forever. An archive that could not be reached
// ends the pass with an error instead: the job retries with its own backoff, and
// nothing is written down, because an outage is not an absence.
func (sweeper *Sweeper) Sweep(ctx context.Context) (bool, error) {
	releases, err := sweeper.store.ReleasesMissingCoverArt(ctx, sweepBatch)
	if err != nil {
		return false, fmt.Errorf("list releases with no cover art: %w", err)
	}
	failures := 0
	for index, release := range releases {
		if index > 0 && !wait(ctx, sweeper.pause) {
			return true, ctx.Err()
		}
		bounded, cancel := context.WithTimeout(ctx, perRelease)
		artwork, err := sweeper.sources.Front(bounded, Release{
			AlbumID:                   release.ID,
			MusicBrainzReleaseID:      release.MusicbrainzReleaseID,
			MusicBrainzReleaseGroupID: release.MusicbrainzReleaseGroupID,
			Artist:                    release.ArtistName,
			Title:                     release.Title,
		})
		cancel()
		if err != nil && !errors.Is(err, ErrNoArtwork) {
			if ctx.Err() != nil {
				return true, ctx.Err()
			}
			// One release nobody could get an answer about is skipped, not
			// recorded: an absence written here would be permanent, and what
			// happened was that nothing was learned.
			failures++
			if failures >= consecutiveFailures {
				return true, fmt.Errorf("fetch cover art for release %s: %w", release.ID, err)
			}
			sweeper.logger.Warn().Err(err).
				Str("album_id", release.ID.String()).
				Msg("a release could not be pictured")
			continue
		}
		stored := Saved(artwork)
		if err := sweeper.store.SaveReleaseCoverArt(ctx, db.SaveReleaseCoverArtParams{
			AlbumID: release.ID, Image: stored.Image, ContentType: stored.ContentType,
			Source: stored.Source,
		}); err != nil {
			return true, fmt.Errorf("cache cover art for release %s: %w", release.ID, err)
		}
		failures = 0
		sweeper.logger.Debug().
			Str("album_id", release.ID.String()).
			Str("source", stored.Source).
			Bool("pictured", len(stored.Image) > 0).
			Msg("a release was looked up for cover art")
	}
	moreArtists, err := sweeper.pictureArtists(ctx)
	if err != nil {
		return true, err
	}
	// And who those people are, at the same pace and under the same rules. It
	// belongs to this pass because it walks the same artists through the same
	// chain: MusicBrainz names the Wikidata entity, and the picture is one thing
	// that entity holds and the article about them is another.
	moreBiographies, err := sweeper.describeArtists(ctx)
	if err != nil {
		return true, err
	}
	// And then the same pictures, written where the player can see them. It is a
	// step of this pass rather than a sweep of its own because it consumes
	// exactly what the two steps above produce: a picture in the cache is the
	// only thing that can be written out, so a pass that filled the cache is the
	// pass that has new work for the disc.
	moreOnDisc := false
	if sweeper.disk != nil {
		moreOnDisc, err = sweeper.disk.Write(ctx)
		if err != nil {
			// The cache is filled and the disc is not. That costs the player its
			// pictures until the next pass and costs Schall's own pages nothing,
			// so it is reported and the pass is not failed on it.
			sweeper.logger.Warn().Err(err).Msg("write pictures into the library")
			moreOnDisc = true
		}
	}
	return len(releases) == sweepBatch || moreArtists || moreBiographies || moreOnDisc, nil
}

// describeArtists fetches a few lines about the artists nobody has asked an
// encyclopaedia about.
//
// Same shape as the two passes above, and for the same reasons: an absence is
// written down so nobody is asked twice, an outage is not written down at all,
// and one artist nothing can be learned about does not stop the rest.
func (sweeper *Sweeper) describeArtists(ctx context.Context) (bool, error) {
	if sweeper.biographies == nil {
		return false, nil
	}
	waiting, err := sweeper.store.ArtistsMissingBiography(ctx, sweepBatch)
	if err != nil {
		return false, fmt.Errorf("list artists with no biography: %w", err)
	}
	failures := 0
	for index, artist := range waiting {
		if index > 0 && !wait(ctx, sweeper.pause) {
			return true, ctx.Err()
		}
		bounded, cancel := context.WithTimeout(ctx, perRelease)
		biography, err := sweeper.biographies.Of(bounded, artist.MusicbrainzID.UUID)
		cancel()
		if err != nil && !errors.Is(err, ErrNoBiography) {
			if ctx.Err() != nil {
				return true, ctx.Err()
			}
			failures++
			if failures >= consecutiveFailures {
				return true, fmt.Errorf("fetch a biography of artist %s: %w", artist.ID, err)
			}
			sweeper.logger.Warn().Err(err).
				Str("artist_id", artist.ID.String()).
				Msg("an artist could not be described")
			continue
		}
		// Written whatever the answer was. An empty biography with a time beside
		// it is "asked, and no encyclopaedia has heard of them", which is what
		// stops this artist being asked about every pass.
		if err := sweeper.store.SaveArtistBiography(ctx, db.SaveArtistBiographyParams{
			ArtistID: artist.ID, Biography: biography.Text, SourceUrl: biography.SourceURL,
		}); err != nil {
			return true, fmt.Errorf("cache a biography of artist %s: %w", artist.ID, err)
		}
		failures = 0
	}
	return len(waiting) == sweepBatch, nil
}

// pictureArtists does for the people what the pass above does for the records.
// It is the same shape deliberately: an absence is written down, an outage is
// not, and the pace is the one pace.
func (sweeper *Sweeper) pictureArtists(ctx context.Context) (bool, error) {
	if sweeper.artists == nil {
		return false, nil
	}
	// The artists asked about are those nobody has looked for, and those written
	// off as having no picture by a chain older than the one asking now. Every
	// row this pass touches is recorded, absence included, so a re-asked artist
	// is answered once rather than each pass finding the same batch.
	waiting, err := sweeper.store.ArtistsMissingImage(ctx, db.ArtistsMissingImageParams{
		ChainChanged: pgtype.Timestamptz{Time: chainChanged, Valid: true},
		Batch:        sweepBatch,
	})
	if err != nil {
		return false, fmt.Errorf("list artists with no picture: %w", err)
	}
	failures := 0
	for index, artist := range waiting {
		if index > 0 && !wait(ctx, sweeper.pause) {
			return true, ctx.Err()
		}
		bounded, cancel := context.WithTimeout(ctx, perRelease)
		artwork, err := sweeper.artists.Picture(bounded, artist.MusicbrainzID.UUID)
		cancel()
		if err != nil && !errors.Is(err, ErrNoArtwork) {
			if ctx.Err() != nil {
				return true, ctx.Err()
			}
			failures++
			if failures >= consecutiveFailures {
				return true, fmt.Errorf("fetch a picture of artist %s: %w", artist.ID, err)
			}
			sweeper.logger.Warn().Err(err).
				Str("artist_id", artist.ID.String()).
				Msg("an artist could not be pictured")
			continue
		}
		stored := Saved(artwork)
		if err := sweeper.store.SaveArtistImage(ctx, db.SaveArtistImageParams{
			ArtistID: artist.ID, Image: stored.Image, ContentType: stored.ContentType,
			Source: stored.Source,
		}); err != nil {
			return true, fmt.Errorf("cache a picture of artist %s: %w", artist.ID, err)
		}
		failures = 0
	}
	return len(waiting) == sweepBatch, nil
}

func wait(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
