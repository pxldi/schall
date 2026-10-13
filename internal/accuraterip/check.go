package accuraterip

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/pxldi/schall/internal/musicbrainz"
)

// minimumTracks is the fewest tracks a disc needs before its table of
// contents is trusted to name one disc. A single-track CD's table is its
// length alone, which two unrelated singles can share to the sector; three
// tracks have to agree at every boundary.
const minimumTracks = 3

// discsAsked is how many tables of contents one copy is looked up under. The
// first with a matching checksum answers, and a recording on more CDs than
// this with the copy's exact length is a compilation series, where the rest
// are the same audio again.
const discsAsked = 6

// Catalogue is what MusicBrainz knows about the CDs a recording is on.
type Catalogue interface {
	RecordingDiscs(ctx context.Context, id uuid.UUID) ([]musicbrainz.DiscPlacement, error)
	RecordingsAtDiscTrack(ctx context.Context, discID string, track int) ([]uuid.UUID, error)
}

// Database is AccurateRip's list of submitted checksums.
type Database interface {
	Pressings(ctx context.Context, ids IDs) ([]Pressing, error)
}

// Summer reads a file's audio into Sums.
type Summer interface {
	Available() bool
	Sum(ctx context.Context, path string) (Sums, error)
}

// Match is the track of a CD a copy's audio was found to be.
type Match struct {
	ReleaseID uuid.UUID
	Medium    int
	Track     int
	DiscID    string
	// Confidence is how many rips submitted the checksum the copy reached.
	Confidence int
}

// Answer is what one check found. Match is nil when nothing matched, and
// Silence then says why in a sentence a person can read.
type Answer struct {
	Match   *Match
	Silence string
}

const (
	noDiscs             = "MusicBrainz lists no CD of this recording with the table of contents read off it."
	noTrackOfThatLength = "No CD MusicBrainz lists this recording on holds a track of exactly " +
		"this copy's length."
	noChecksum              = "No rip AccurateRip holds of those CDs has this copy's checksum."
	otherRecordingOnTheDisc = "The copy matched a CD track, and MusicBrainz lists another " +
		"recording at that place on a release with the same CD."
)

// Checker looks a copy up in AccurateRip under the CDs MusicBrainz lists its
// recording on.
type Checker struct {
	catalogue Catalogue
	database  Database
	summer    Summer
}

func NewChecker(catalogue Catalogue, database Database, summer Summer) *Checker {
	return &Checker{catalogue: catalogue, database: database, summer: summer}
}

// Available reports whether copies can be decoded at all.
func (checker *Checker) Available() bool {
	return checker != nil && checker.summer != nil && checker.summer.Available()
}

type candidate struct {
	placement musicbrainz.DiscPlacement
	disc      musicbrainz.Disc
	toc       TOC
}

// Check reports whether a copy is, bit for bit, the track the recording sits
// at on a CD MusicBrainz lists it on.
//
// Three things have to hold. The copy holds exactly as many samples as that
// track does on the disc. Its checksum is one somebody submitted to
// AccurateRip for that track of a disc with that table of contents. And every
// release MusicBrainz files that disc under has this recording at that place;
// one naming another recording there is a contradiction, and nothing is
// admitted on it.
//
// An error is a service that could not be asked. Everything else that finds
// nothing is an Answer with Silence, because none of it says anything about
// the copy.
func (checker *Checker) Check(ctx context.Context, recordingID uuid.UUID, path string) (Answer, error) {
	placements, err := checker.catalogue.RecordingDiscs(ctx, recordingID)
	if errors.Is(err, musicbrainz.ErrNotFound) {
		return Answer{Silence: noDiscs}, nil
	}
	if err != nil {
		return Answer{}, err
	}
	if len(placements) == 0 {
		return Answer{Silence: noDiscs}, nil
	}
	sums, err := checker.summer.Sum(ctx, path)
	if errors.Is(err, ErrNotCDAudio) {
		return Answer{Silence: noTrackOfThatLength}, nil
	}
	if err != nil {
		return Answer{}, err
	}

	type place struct {
		disc  string
		track int
	}
	var candidates []candidate
	seen := map[place]bool{}
	for _, placement := range placements {
		for _, disc := range placement.Discs {
			toc := TOC{Offsets: disc.Offsets, Sectors: disc.Sectors}
			if !toc.Valid() || len(toc.Offsets) < minimumTracks ||
				toc.Frames(placement.Track) != sums.Frames {
				continue
			}
			key := place{disc: disc.ID, track: placement.Track}
			if seen[key] {
				continue
			}
			seen[key] = true
			candidates = append(candidates, candidate{placement: placement, disc: disc, toc: toc})
		}
	}
	if len(candidates) == 0 {
		return Answer{Silence: noTrackOfThatLength}, nil
	}

	var unasked error
	for index, found := range candidates {
		if index == discsAsked {
			break
		}
		pressings, err := checker.database.Pressings(ctx, found.toc.IDs())
		if errors.Is(err, ErrNotListed) {
			continue
		}
		if err != nil {
			unasked = err
			continue
		}
		track := found.placement.Track
		checksums := sums.Checksums(track == 1, track == len(found.toc.Offsets))
		confidence := 0
		for _, pressing := range pressings {
			if len(pressing.Tracks) != len(found.toc.Offsets) {
				continue
			}
			submitted := pressing.Tracks[track-1]
			if submitted.Confidence > 0 && checksums.Matches(submitted.Checksum) {
				confidence = max(confidence, submitted.Confidence)
			}
		}
		if confidence == 0 {
			continue
		}
		recordings, err := checker.catalogue.RecordingsAtDiscTrack(ctx, found.disc.ID, track)
		if errors.Is(err, musicbrainz.ErrNotFound) || (err == nil && len(recordings) == 0) {
			// The disc ID was taken off between the two lookups. What it was
			// filed under can no longer be read, so nothing rests on it.
			continue
		}
		if err != nil {
			return Answer{}, err
		}
		for _, recording := range recordings {
			if recording != recordingID {
				return Answer{Silence: otherRecordingOnTheDisc}, nil
			}
		}
		return Answer{Match: &Match{
			ReleaseID: found.placement.ReleaseID, Medium: found.placement.Medium,
			Track: track, DiscID: found.disc.ID, Confidence: confidence,
		}}, nil
	}
	if unasked != nil {
		return Answer{}, unasked
	}
	return Answer{Silence: noChecksum}, nil
}
