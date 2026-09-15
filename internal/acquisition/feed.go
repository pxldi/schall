package acquisition

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pxldi/schall/internal/db"
	"github.com/rs/zerolog"
)

// feedRefreshIdle is how stale a followed artist's catalogue may get before the
// feed asks MusicBrainz about them again. Following is a standing request to
// keep an artist complete, and a release published today should not wait a week
// to be noticed; a day is also slow enough that a follow list of any realistic
// size costs a few requests against a limiter shared with interactive search.
const feedRefreshIdle = 24 * time.Hour

// feedRefreshBatch is how many artists one pass asks about. The refreshes are
// queued rather than performed here, and each of them queues a release refresh
// per release group, so a large follow list arrives at MusicBrainz steadily
// instead of all at once. What is left over is picked up by the next pass.
const feedRefreshBatch = 10

// feedLabelBatch is how many labels one pass asks about. Fewer than artists,
// because one label's catalogue is several pages where an artist's is one
// request, and each of those pages ingests release groups that queue refreshes
// of their own. The rest wait for the next pass.
const feedLabelBatch = 5

// feedWantBatch is how many wants one pass creates. An artist who released a
// box set overnight should not become one pass that walks a thousand tracks, and
// the query already excludes everything already wanted — so a full batch means
// there is more, and the pass says so rather than waiting six hours to find out.
const feedWantBatch = 200

// FeedStore is what the follow feed reads and asks for, and nothing else.
type FeedStore interface {
	StaleFollowedArtists(context.Context, db.StaleFollowedArtistsParams) ([]uuid.UUID, error)
	QueueArtistRefresh(context.Context, uuid.UUID) (db.QueueArtistRefreshRow, error)
	FollowFeedTracks(context.Context, int32) ([]db.FollowFeedTracksRow, error)
	StaleFollowedLabels(context.Context, db.StaleFollowedLabelsParams) ([]uuid.UUID, error)
	QueueLabelRefresh(context.Context, uuid.UUID) (db.QueueLabelRefreshRow, error)
	FollowFeedLabelTracks(context.Context, int32) ([]db.FollowFeedLabelTracksRow, error)
}

// Feed is what following an artist or a label actually buys: their catalogue is asked
// about again without anybody pressing anything, and a release published after
// the follow becomes wants on its own.
//
// It creates reach and never a decision. Every copy the loop later fetches for
// these wants is still proven by its audio before it enters the library, so the
// worst an over-eager feed can cost is bandwidth and a longer queue of
// questions — never a wrong file in the library.
type Feed struct {
	store  FeedStore
	wants  *Service
	logger zerolog.Logger
	now    func() time.Time
}

func NewFeed(store FeedStore, wants *Service, logger zerolog.Logger) *Feed {
	return &Feed{store: store, wants: wants, logger: logger, now: time.Now}
}

// Sweep does one pass and reports whether there is more waiting.
//
// The two halves are deliberately not chained. Asking about an artist queues a
// refresh, which queues a release refresh per release group, which is what
// finally lands the tracks a want is made of — so a pass wants what the passes
// before it turned up rather than waiting on jobs it has just queued. A new
// release therefore becomes wants on the pass after the one that noticed it,
// which is the difference between a feed that is a few hours late and one that
// holds a worker open waiting for MusicBrainz.
func (feed *Feed) Sweep(ctx context.Context) (bool, error) {
	asked, err := feed.askAboutStaleArtists(ctx)
	if err != nil {
		return false, err
	}
	askedLabels, err := feed.askAboutStaleLabels(ctx)
	if err != nil {
		return false, err
	}
	created, more, err := feed.wantNewReleases(ctx)
	if err != nil {
		return false, err
	}
	if asked > 0 || askedLabels > 0 || created > 0 {
		feed.logger.Info().
			Int("artists_asked_about", asked).
			Int("labels_asked_about", askedLabels).
			Int("wanted", created).
			Bool("more", more).
			Msg("follow feed swept")
	}
	return more, nil
}

// askAboutStaleArtists queues a catalogue refresh for the followed artists
// nobody has looked at lately. Queueing is all it does: the refresh is one
// rate-limited request and belongs on the queue with everything else asking
// MusicBrainz questions, not inside a sweep holding a worker.
func (feed *Feed) askAboutStaleArtists(ctx context.Context) (int, error) {
	artists, err := feed.store.StaleFollowedArtists(ctx, db.StaleFollowedArtistsParams{
		RefreshedBefore: pgtype.Timestamptz{Time: feed.now().Add(-feedRefreshIdle), Valid: true},
		RowLimit:        feedRefreshBatch,
	})
	if err != nil {
		return 0, fmt.Errorf("read stale followed artists: %w", err)
	}
	for _, artistID := range artists {
		// Queueing twice is not two refreshes: the query answers with the
		// refresh already queued or running for that artist. So a pass that
		// runs while yesterday's is still waiting its turn changes nothing.
		if _, err := feed.store.QueueArtistRefresh(ctx, artistID); err != nil {
			return 0, fmt.Errorf("queue refresh for artist %s: %w", artistID, err)
		}
	}
	return len(artists), nil
}

// askAboutStaleLabels queues a walk over the releases of the followed labels
// nobody has looked at lately, and over the ones a previous pass left part way
// through. Queueing is all it does, for the same reason as for an artist: the
// walk is a run of rate-limited requests and belongs on the queue.
func (feed *Feed) askAboutStaleLabels(ctx context.Context) (int, error) {
	labels, err := feed.store.StaleFollowedLabels(ctx, db.StaleFollowedLabelsParams{
		RefreshedBefore: pgtype.Timestamptz{Time: feed.now().Add(-feedRefreshIdle), Valid: true},
		RowLimit:        feedLabelBatch,
	})
	if err != nil {
		return 0, fmt.Errorf("read stale followed labels: %w", err)
	}
	for _, labelID := range labels {
		// Queueing twice is not two walks: the query answers with the pass
		// already queued or running for that label.
		if _, err := feed.store.QueueLabelRefresh(ctx, labelID); err != nil {
			return 0, fmt.Errorf("queue refresh for label %s: %w", labelID, err)
		}
	}
	return len(labels), nil
}

// wantNewReleases makes a want of every track the feed reaches. The queries
// have already excluded what the library holds, what somebody already asked for
// and what a monitor level does not count, so everything here is new music by
// somebody, or from a label, the user is following.
func (feed *Feed) wantNewReleases(ctx context.Context) (int, bool, error) {
	tracks, err := feed.store.FollowFeedTracks(ctx, feedWantBatch)
	if err != nil {
		return 0, false, fmt.Errorf("read follow feed tracks: %w", err)
	}
	labelTracks, err := feed.store.FollowFeedLabelTracks(ctx, feedWantBatch)
	if err != nil {
		return 0, false, fmt.Errorf("read follow feed label tracks: %w", err)
	}

	entries := make([]Entry, 0, len(tracks)+len(labelTracks))
	for _, track := range tracks {
		entry := Entry{
			Origin: "follow_feed",
			// The release the track sits on is the release context a copy of it
			// will be filed under, exactly as a want made from a release page
			// carries the release it was pressed on.
			OriginAlbumID:  uuid.NullUUID{UUID: track.AlbumID, Valid: true},
			Artist:         track.ArtistName,
			Title:          track.Title,
			Album:          track.AlbumTitle,
			ISRC:           track.Isrc.String,
			RecordingID:    track.MusicbrainzRecordingID,
			ReleaseGroupID: track.MusicbrainzReleaseGroupID,
		}
		if track.DurationMs.Valid {
			duration := track.DurationMs.Int32
			entry.DurationMS = &duration
		}
		entries = append(entries, entry)
	}
	for _, track := range labelTracks {
		// The origin is the label and not the artist. The user may not follow
		// this artist, and may never have heard of them; what they asked for is
		// everything this label publishes.
		entry := Entry{
			Origin:         "label_feed",
			OriginAlbumID:  uuid.NullUUID{UUID: track.AlbumID, Valid: true},
			Artist:         track.ArtistName,
			Title:          track.Title,
			Album:          track.AlbumTitle,
			ISRC:           track.Isrc.String,
			RecordingID:    track.MusicbrainzRecordingID,
			ReleaseGroupID: track.MusicbrainzReleaseGroupID,
		}
		if track.DurationMs.Valid {
			duration := track.DurationMs.Int32
			entry.DurationMS = &duration
		}
		entries = append(entries, entry)
	}

	created := 0
	// One recording is one want however many new releases carry it, and by
	// whichever of the two halves the feed reached it. The queries cannot
	// exclude a recording this same pass is about to want, so the pass
	// remembers — otherwise a single and the album it lands on would spend two
	// of the batch's places on the same music, and a record by a followed
	// artist on a followed label would be wanted twice.
	seen := make(map[uuid.UUID]struct{}, len(entries))
	for _, entry := range entries {
		if _, repeat := seen[entry.RecordingID.UUID]; repeat {
			continue
		}
		seen[entry.RecordingID.UUID] = struct{}{}

		if _, made, err := feed.wants.Create(ctx, entry); err != nil {
			// A failure halfway through leaves the wants made so far, which is
			// correct: they are the wants the next pass would make, and the next
			// pass is in six hours.
			return created, false, fmt.Errorf("want %s by %s: %w",
				entry.Title, entry.Artist, err)
		} else if made {
			created++
		}
	}
	// A full batch on either side means there is more new music waiting, and
	// the pass says so rather than waiting six hours to find out.
	more := len(tracks) == feedWantBatch || len(labelTracks) == feedWantBatch
	return created, more, nil
}
