package acquisition

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/db"
	"github.com/rs/zerolog"
)

// MissingTrack is one catalogue track a walk may want, with the release context
// it would be wanted from and whether the library already holds it. It is what
// the two queries that answer "what is missing here" have in common, so the
// walk over them is written once.
type MissingTrack struct {
	AlbumID        uuid.UUID
	AlbumTitle     string
	ArtistName     string
	Title          string
	DurationMS     *int32
	ISRC           string
	RecordingID    uuid.NullUUID
	ReleaseGroupID uuid.NullUUID
	// Held is either way the library can already have it: a mapping onto a
	// present file, or a present file proven to be that recording without being
	// mapped onto this release at all. Both are reasons not to want it, because
	// the loop settles a want against either (OwnedFileForRecording).
	Held bool
}

// Counts says what became of every track one walk went over, because "wanted 3
// of 12" is the answer somebody pressing this needs and a bare count of new
// wants would read as the other nine having failed.
//
// It counts recordings rather than track rows. A discography reaches the same
// recording several times over — the album, the single, the compilation — and
// those are one want, so counting them as three would say a walk did three
// times the work it did.
type Counts struct {
	// Wanted is targets this walk created; AlreadyWanted is the ones that
	// existed before it, which includes a track somebody stopped pursuing.
	Wanted        int `json:"wanted"`
	AlreadyWanted int `json:"alreadyWanted"`
	Owned         int `json:"owned"`
	// Unresolvable is a track this cannot usefully ask for: the catalogue holds
	// no MusicBrainz recording against it, or nothing to identify it at all.
	Unresolvable int `json:"unresolvable"`
}

// Missing is Counts with the number of releases something was missing from,
// which is the one thing that says how wide the walk was.
type Missing struct {
	Counts
	Releases int `json:"releases"`
}

// Wanter records one want. *Service is the one in production; a handler under
// test hands in its own.
type Wanter interface {
	Create(ctx context.Context, entry Entry) (db.AcquisitionTargetRow, bool, error)
}

// WantMissing records a want for every track here the library does not hold,
// and counts what the walk came to.
//
// A track the catalogue holds no MusicBrainz recording against is counted and
// skipped rather than wanted. Creation is idempotent on the recording ID and a
// target without one has no recording to be the same as, so it is always new —
// a second walk would make a second want for the same track, and a third a
// third. The recording is also what lands a want resolved and due immediately,
// which is the whole reason wanting from the catalogue is worth doing.
//
// A recording reached twice is passed over in silence rather than counted
// again: it is the same music, the want for it was made or found the first
// time, and counting it as "already wanted" would tell somebody walking this
// for the first time that they had asked before.
//
// What this creates is reach, never a decision. Every copy the loop later
// fetches for these wants is still proven by its audio before it enters the
// library.
func WantMissing(
	ctx context.Context, wanter Wanter, origin string, tracks []MissingTrack,
) (Missing, error) {
	var result Missing
	releases := make(map[uuid.UUID]struct{})
	seen := make(map[uuid.UUID]struct{}, len(tracks))
	for _, track := range tracks {
		if track.Held {
			result.Owned++
			continue
		}
		releases[track.AlbumID] = struct{}{}
		if !track.RecordingID.Valid {
			result.Unresolvable++
			continue
		}
		if _, repeat := seen[track.RecordingID.UUID]; repeat {
			continue
		}
		seen[track.RecordingID.UUID] = struct{}{}

		// The release the track sits on is the release context the resolution
		// ran through, which is the first rule of the release-choice order at
		// import time. Passing it is why a track wanted from here files under
		// the release it was asked for rather than the earliest one carrying it.
		_, created, err := wanter.Create(ctx, Entry{
			Origin:         origin,
			OriginAlbumID:  uuid.NullUUID{UUID: track.AlbumID, Valid: true},
			Artist:         track.ArtistName,
			Title:          track.Title,
			Album:          track.AlbumTitle,
			DurationMS:     track.DurationMS,
			ISRC:           track.ISRC,
			RecordingID:    track.RecordingID,
			ReleaseGroupID: track.ReleaseGroupID,
		})
		switch {
		case errors.Is(err, ErrEntryNotIdentifiable):
			result.Unresolvable++
		case err != nil:
			// A failure halfway through leaves the wants made so far, which is
			// correct: they are the same wants the next walk would make, and a
			// walk is what the caller retries.
			return result, fmt.Errorf("want %s by %s: %w", track.Title, track.ArtistName, err)
		case created:
			result.Wanted++
		default:
			result.AlreadyWanted++
		}
	}
	result.Releases = len(releases)
	return result, nil
}

// MissingFromArtistTracks reads the discography-wide walk's rows.
func MissingFromArtistTracks(rows []db.ArtistTracksForWantingRow) []MissingTrack {
	tracks := make([]MissingTrack, 0, len(rows))
	for _, row := range rows {
		tracks = append(tracks, MissingTrack{
			AlbumID:        row.AlbumID,
			AlbumTitle:     row.AlbumTitle,
			ArtistName:     row.ArtistName,
			Title:          row.Title,
			DurationMS:     duration(row.DurationMs.Int32, row.DurationMs.Valid),
			ISRC:           row.Isrc.String,
			RecordingID:    row.MusicbrainzRecordingID,
			ReleaseGroupID: row.MusicbrainzReleaseGroupID,
			Held:           row.Owned || row.HeldElsewhere,
		})
	}
	return tracks
}

// MissingFromStandingTracks reads the same walk narrowed to one release.
func MissingFromStandingTracks(rows []db.StandingWantTracksRow) []MissingTrack {
	tracks := make([]MissingTrack, 0, len(rows))
	for _, row := range rows {
		tracks = append(tracks, MissingTrack{
			AlbumID:        row.AlbumID,
			AlbumTitle:     row.AlbumTitle,
			ArtistName:     row.ArtistName,
			Title:          row.Title,
			DurationMS:     duration(row.DurationMs.Int32, row.DurationMs.Valid),
			ISRC:           row.Isrc.String,
			RecordingID:    row.MusicbrainzRecordingID,
			ReleaseGroupID: row.MusicbrainzReleaseGroupID,
			Held:           row.Owned || row.HeldElsewhere,
		})
	}
	return tracks
}

func duration(value int32, valid bool) *int32 {
	if !valid {
		return nil
	}
	return &value
}

// StandingStore is what a standing want reads, and nothing else.
type StandingStore interface {
	StandingWantTracks(context.Context, uuid.UUID) ([]db.StandingWantTracksRow, error)
}

// Standing is the half of the standing "want what is missing" that nobody
// presses: a release whose tracklist arrives after the press is wanted the
// moment it lands.
//
// Following an artist saves their release groups and queues one release refresh
// each, and those come back one at a time behind MusicBrainz's rate limit. A
// press on the artist's page therefore reaches only the releases whose tracks
// are already in the catalogue, and the rest arrive over the following hours
// with nobody there to press again.
type Standing struct {
	store  StandingStore
	wants  *Service
	logger zerolog.Logger
}

func NewStanding(store StandingStore, wants *Service, logger zerolog.Logger) *Standing {
	return &Standing{store: store, wants: wants, logger: logger}
}

// WantAlbumMissing wants the tracks of one release the library does not hold,
// and reports how many wants it made.
//
// It applies no rule of its own. The query answers with no rows unless the
// artist has a standing want and their monitor level counts this release, which
// is the same rule the discography counts by — so the header, the chips and the
// standing want agree about which releases count.
func (standing *Standing) WantAlbumMissing(ctx context.Context, albumID uuid.UUID) (int, error) {
	rows, err := standing.store.StandingWantTracks(ctx, albumID)
	if err != nil {
		return 0, fmt.Errorf("read standing want tracks for release %s: %w", albumID, err)
	}
	if len(rows) == 0 {
		return 0, nil
	}
	// The origin is "manual" because the standing want is: a person pressed it
	// once, and this is that press reaching a release that had not arrived yet.
	result, err := WantMissing(ctx, standing.wants, "manual", MissingFromStandingTracks(rows))
	if result.Wanted > 0 {
		standing.logger.Info().
			Str("album_id", albumID.String()).
			Int("wanted", result.Wanted).
			Int("already_wanted", result.AlreadyWanted).
			Int("owned", result.Owned).
			Msg("standing want reached a new tracklist")
	}
	if err != nil {
		return result.Wanted, err
	}
	return result.Wanted, nil
}
