// Package lyrics writes the words of a song beside the music.
//
// The chain this exists to complete has four links and Schall was missing the
// first. Symfonium, the phone app, never fetches lyrics; it asks its server.
// Navidrome, the server, never fetches them either; it serves what it finds in
// the library directory. So lyrics exist for a song if, and only if, something
// put a file there — and Schall is the library's only writer.
//
// What gets written is an .lrc sidecar: the same basename as the audio file, in
// the same folder, holding the timed words. Navidrome reads that format and
// hands the timing on to the phone, which scrolls the words as the song plays.
//
// A sidecar rather than a tag inside the audio, deliberately. Writing into the
// audio file would rewrite the music: it would change the file's size and its
// modified time, which the library scanner reads, and it would need the audio
// re-identified afterwards to prove nothing was damaged. None of that arises
// here. The audio is byte-for-byte the same before and after, and the sidecar is
// a plain text file anybody can read or delete.
//
// Lyrics decide nothing. Nothing in this package is read by matching, by
// identity, or by any import grader, and nothing it does reaches a decision
// store.
package lyrics

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/lrclib"
	"github.com/rs/zerolog"
)

const (
	// sweepBatch is how many files one pass looks at. Most of them cost nothing
	// — a file whose sidecar is already there is answered by the folder — so
	// this is how much of the library one pass walks rather than how many
	// requests it makes.
	sweepBatch = 200
	// betweenRequests is the pace. LRCLIB is free, keyless and run by somebody
	// who did not ask to be asked, and it publishes no rate limit; a second
	// between requests is slow enough to be a good neighbour and fast enough
	// that a collection fills over an evening.
	betweenRequests = time.Second
	// perSong bounds one song's worth of asking, so one request that never
	// finishes cannot wedge the pass for as long as the process lives.
	perSong = 20 * time.Second
	// consecutiveFailures is how many songs in a row may fail before the pass
	// gives up. One song nobody can answer about must not stop the library being
	// worded; a service that is down must not be asked about every remaining
	// song and then immediately asked again.
	consecutiveFailures = 5
	// extension is what Navidrome reads timed words from.
	extension = ".lrc"
)

// Store is the persistence this needs, and nothing else: where the music is and
// what the catalogue says it is.
type Store interface {
	TracksToLyric(context.Context, db.TracksToLyricParams) ([]db.TracksToLyricRow, error)
	TrackToLyric(context.Context, uuid.UUID) (db.TrackToLyricRow, error)
}

// Fetcher asks a lyrics service about one recording.
type Fetcher interface {
	Get(context.Context, lrclib.Song) (lrclib.Lyrics, error)
}

// Position is where a pass stopped, carried into the next one.
//
// Nothing in the database records which files have been asked about, because the
// sidecar beside the music is that record and its absence is the record of a
// miss. What the file on disc cannot say is where a pass got to before it ran
// out of batch, so the pass says it here and its successor starts from there.
//
// An empty position starts at the beginning of the library, which is what a pass
// that reached the end asks for next.
type Position struct {
	AfterFileID uuid.UUID `json:"afterFileId,omitempty"`
}

// Result is what one pass did, for the log line that says so.
type Result struct {
	// Looked is how many files the pass considered.
	Looked int
	// Written is how many sidecars it put on disc.
	Written int
	// Instrumental is how many songs the service said have no words at all.
	Instrumental int
	// Next is where the following pass starts.
	Next Position
	// More reports whether there is library left to walk.
	More bool
}

// Service fetches the words and writes them beside the music.
type Service struct {
	store   Store
	fetcher Fetcher
	logger  zerolog.Logger

	// batch and pause are fields rather than the constants so a test does not
	// wait out the real pace.
	batch int32
	pause time.Duration
}

func NewService(store Store, fetcher Fetcher, logger zerolog.Logger) *Service {
	return &Service{
		store:   store,
		fetcher: fetcher,
		logger:  logger,
		batch:   sweepBatch,
		pause:   betweenRequests,
	}
}

// Sweep walks one page of the library and writes what it can.
//
// A song the service has no words for costs nothing and is written down nowhere:
// a miss is silence, and the next pass over the library will ask again, which is
// the right answer for a public database that gains songs every day.
func (service *Service) Sweep(ctx context.Context, from Position) (Result, error) {
	rows, err := service.store.TracksToLyric(ctx, db.TracksToLyricParams{
		After: from.AfterFileID,
		Batch: service.batch,
	})
	if err != nil {
		return Result{}, fmt.Errorf("list the music to word: %w", err)
	}

	result := Result{Next: from, Looked: len(rows), More: int32(len(rows)) == service.batch}
	failures := 0
	asked := false
	for _, row := range rows {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		result.Next = Position{AfterFileID: row.LibraryFileID}
		written, instrumental, err := service.write(ctx, song(row), row.Path, &asked)
		if err != nil {
			failures++
			if failures >= consecutiveFailures {
				// The service is not answering. Stopping here keeps the position
				// this pass reached, so the next one carries on from it rather
				// than walking the same page again.
				return result, fmt.Errorf("fetch lyrics: %w", err)
			}
			service.logger.Debug().Err(err).Str("path", row.Path).Msg("fetch lyrics for one file")
			continue
		}
		failures = 0
		if written {
			result.Written++
		}
		if instrumental {
			result.Instrumental++
		}
	}
	if !result.More {
		// The end of the library. The next pass starts at the beginning again,
		// which is what picks up songs LRCLIB has gained since.
		result.Next = Position{}
	}
	return result, nil
}

// ForFile words one file, for the moment it is imported.
//
// It is the same work a pass does, asked about one row. A file with no mapping,
// or one the library has already lost, is not asked about at all — there is
// nothing truthful to ask, because nothing has said what the file is.
func (service *Service) ForFile(ctx context.Context, fileID uuid.UUID) {
	row, err := service.store.TrackToLyric(ctx, fileID)
	if err != nil {
		return
	}
	asked := false
	if _, _, err := service.write(ctx, song(db.TracksToLyricRow(row)), row.Path, &asked); err != nil {
		service.logger.Debug().Err(err).Str("file_id", fileID.String()).
			Msg("fetch lyrics for an imported file")
	}
}

// write puts one song's words beside it, if there are any and nothing is there
// already.
//
// asked carries whether this pass has already made a request, so the pace is
// only paid between requests rather than before the first one and after every
// file that needed none.
func (service *Service) write(
	ctx context.Context, want lrclib.Song, path string, asked *bool,
) (written, instrumental bool, err error) {
	sidecar := sidecarPath(path)
	if sidecar == "" {
		return false, false, nil
	}
	if _, statErr := os.Stat(sidecar); statErr == nil {
		// Somebody's words are already there — the user's, or this feature's
		// from an earlier pass. Either way they are the answer and they stay.
		return false, false, nil
	}

	if *asked && !wait(ctx, service.pause) {
		return false, false, ctx.Err()
	}
	*asked = true

	bounded, cancel := context.WithTimeout(ctx, perSong)
	found, err := service.fetcher.Get(bounded, want)
	cancel()
	if errors.Is(err, lrclib.ErrNoLyrics) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	if found.Instrumental {
		// A positive answer that this recording has no words. Nothing is
		// written, because an empty .lrc file would tell the player there are
		// lyrics and then show it none.
		return false, true, nil
	}

	words := found.Synced
	if words == "" {
		// Untimed words still show on the phone; they simply do not scroll.
		words = found.Plain
	}
	if err := place(sidecar, words); err != nil {
		return false, false, err
	}
	service.logger.Debug().Str("path", sidecar).Bool("timed", found.Timed()).
		Msg("lyrics written beside the music")
	return true, false, nil
}

// song is the catalogue's account of one recording, as the question a lyrics
// service is asked.
func song(row db.TracksToLyricRow) lrclib.Song {
	return lrclib.Song{
		Artist:   row.ArtistName,
		Title:    row.Title,
		Album:    row.AlbumTitle,
		Duration: time.Duration(row.DurationMs.Int32) * time.Millisecond,
	}
}

// SidecarFor is where the lyrics of one audio file are kept, whether or not
// anything has been written there. It is the audio file's own path with the
// extension replaced.
//
// It is exported for the layout migration, which has to carry the sidecar along
// when the audio moves: left behind, the words are lost to the new folder and
// the old folder is not empty, so it survives (internal/library/moves.go).
func SidecarFor(audioPath string) string { return sidecarPath(audioPath) }

// sidecarPath is the audio file's path with its extension replaced. Same
// basename, same folder, which is where Navidrome looks.
func sidecarPath(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" || !filepath.IsAbs(trimmed) {
		return ""
	}
	return strings.TrimSuffix(trimmed, filepath.Ext(trimmed)) + extension
}

// place writes one sidecar, and never half of one.
//
// The words go to a temporary file in the same folder and are renamed into
// place. A scan walking past would not index either file — the scanner reads
// audio extensions and nothing else — but Navidrome does read the sidecar, and
// half a lyric file is a song that stops scrolling in the middle.
func place(destination, words string) error {
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".schall-lyrics-*")
	if err != nil {
		return fmt.Errorf("open a place for %s: %w", destination, err)
	}
	name := temporary.Name()
	defer func() { _ = os.Remove(name) }()

	if _, err := temporary.WriteString(words + "\n"); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write %s: %w", destination, err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("write %s: %w", destination, err)
	}
	// Readable by whatever reads the music, which is a different process under a
	// different account as often as not.
	if err := os.Chmod(name, 0o644); err != nil {
		return fmt.Errorf("set the mode of %s: %w", destination, err)
	}
	if err := os.Rename(name, destination); err != nil {
		return fmt.Errorf("put %s in place: %w", destination, err)
	}
	return nil
}

// wait pauses, and reports whether the wait was seen through rather than cut
// short by the caller giving up.
func wait(ctx context.Context, duration time.Duration) bool {
	if duration <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
