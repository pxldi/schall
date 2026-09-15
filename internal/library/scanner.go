package library

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dhowden/tag"
	"github.com/dhowden/tag/mbz"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/events"
	"github.com/pxldi/schall/internal/tagging"
)

// supportedExtensions is what the library can hold: the file types a scan
// indexes and therefore the only ones that can ever become music Schall knows
// about.
//
// It is the whole answer to "is this music", so anything that fetches audio has
// to ask it first (CanHold). A copy of a type absent from here is copied into the
// library and then ignored forever by every scan, which is not a file the library
// has — an AIFF fetched for a want was proven, imported, and then waited nine
// hours for a library row that no scan would ever create, while the want it
// answered reported that it was being taken in.
//
// AIFF and WAV carry no tags this parser reads, and that is not a reason to
// exclude them: an absent tag is silence rather than damage (TagsAbsent), the
// audio is what identifies a file anyway, and a lossless copy the user has is
// worth more than the tags it is missing.
var supportedExtensions = map[string]struct{}{
	".mp3": {}, ".flac": {}, ".ogg": {}, ".oga": {}, ".opus": {},
	".m4a": {}, ".m4b": {}, ".m4p": {}, ".mp4": {}, ".dsf": {},
	".aiff": {}, ".aif": {}, ".wav": {},
}

// CanHold reports whether a scan would index a file with this name, which is
// whether the library can hold it at all. The name may be a remote path a peer
// advertised — nothing is opened, only the extension is read.
func CanHold(name string) bool {
	return isSupported(name)
}

type ScanResult struct {
	Discovered int
	Updated    int
	Unchanged  int
	Missing    int
	Errors     int
}

// progressNotice is the shortest gap between two notices from a running scan. A
// scan touches files far faster than a view can usefully be redrawn, and every
// notice costs each open interface a refetch of the whole summary, so progress
// is announced at a pace a person reads at rather than at the pace of the disk.
const progressNotice = 2 * time.Second

type Scanner struct {
	pool    *pgxpool.Pool
	matcher FileMatcher
	// notices tells connected interfaces that the library changed. It is
	// optional: without it the views go back to asking on a timer.
	notices *events.Hub
}

type FileMatcher interface {
	ReconcileFile(context.Context, uuid.UUID) error
}

type extractedTags struct {
	Artist                 string
	Album                  string
	Title                  string
	DiscNumber             int
	TrackNumber            int
	DurationMS             int
	MusicBrainzRecordingID *uuid.UUID
	ISRC                   string
	Error                  string
	// TagsAbsent distinguishes a file that carries no tags at all — a WAV
	// with no metadata chunk — from one whose bytes could not be read. Error
	// is set either way, so every current reader keeps treating it as a
	// failure; a caller for whom absent tags are silence rather than damage
	// opts in by looking here.
	TagsAbsent bool
}

// AudioMetadata is the evidence embedded in one local audio file. Import
// validation uses the same parser as the library scanner, so a file cannot
// pass one interpretation and then be indexed under another.
type AudioMetadata struct {
	Artist                 string
	Album                  string
	Title                  string
	DiscNumber             int
	TrackNumber            int
	DurationMS             int
	MusicBrainzRecordingID *uuid.UUID
	ISRC                   string
	Error                  string
	// TagsAbsent carries extractedTags' distinction through: the file is
	// readable audio that simply says nothing about itself.
	TagsAbsent bool
}

func InspectAudio(path string) AudioMetadata {
	value := readTags(path)
	return AudioMetadata{
		Artist: value.Artist, Album: value.Album, Title: value.Title,
		DiscNumber: value.DiscNumber, TrackNumber: value.TrackNumber,
		DurationMS: value.DurationMS, MusicBrainzRecordingID: value.MusicBrainzRecordingID,
		ISRC: value.ISRC, Error: value.Error, TagsAbsent: value.TagsAbsent,
	}
}

func NewScanner(pool *pgxpool.Pool, matchers ...FileMatcher) *Scanner {
	scanner := &Scanner{pool: pool}
	if len(matchers) > 0 {
		scanner.matcher = matchers[0]
	}
	return scanner
}

// WithEvents registers the hub a running scan announces its progress on.
func (scanner *Scanner) WithEvents(hub *events.Hub) *Scanner {
	scanner.notices = hub
	return scanner
}

func (scanner *Scanner) Scan(ctx context.Context) (ScanResult, error) {
	rows, err := scanner.pool.Query(ctx, `SELECT id, path FROM library_roots WHERE enabled ORDER BY path`)
	if err != nil {
		return ScanResult{}, err
	}
	defer rows.Close()

	type root struct {
		id   uuid.UUID
		path string
	}
	roots := make([]root, 0)
	for rows.Next() {
		var item root
		if err := rows.Scan(&item.id, &item.path); err != nil {
			return ScanResult{}, err
		}
		roots = append(roots, item)
	}
	if err := rows.Err(); err != nil {
		return ScanResult{}, err
	}
	if len(roots) == 0 {
		return ScanResult{}, errors.New("no enabled music folders are configured")
	}

	var result ScanResult
	// Rationed per scan rather than per scanner, so that the first change a
	// scan finds is announced at once however recently the last scan ran.
	progress := events.NewRationed(scanner.notices, events.TopicLibrary, progressNotice)
	for _, root := range roots {
		rootResult, err := scanner.scanRoot(ctx, root.id, root.path, progress)
		if err != nil {
			return result, fmt.Errorf("scan %s: %w", root.path, err)
		}
		result.Discovered += rootResult.Discovered
		result.Updated += rootResult.Updated
		result.Unchanged += rootResult.Unchanged
		result.Missing += rootResult.Missing
		result.Errors += rootResult.Errors
	}
	return result, nil
}

func (scanner *Scanner) scanRoot(
	ctx context.Context, rootID uuid.UUID, rootPath string, progress *events.Rationed,
) (ScanResult, error) {
	startedAt := time.Now().UTC()
	var result ScanResult
	err := filepath.WalkDir(rootPath, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() || !isSupported(path) {
			return nil
		}

		info, err := entry.Info()
		if err != nil {
			return err
		}
		result.Discovered++
		unchanged, moved, fileID, err := scanner.touchUnchanged(ctx, rootID, path, info, startedAt)
		if err != nil {
			return err
		}
		if unchanged {
			result.Unchanged++
			if moved {
				if scanner.matcher != nil {
					if err := scanner.matcher.ReconcileFile(ctx, fileID); err != nil {
						return err
					}
				}
				// A file that came back is a change, and so is one that has just
				// been given a length it never had — matching reads that length,
				// so the file has to be put to it again. A file that was there
				// all along and said nothing new is not, so a rescan that finds
				// nothing stays quiet until it finishes.
				progress.Publish()
			}
			return nil
		}

		// touchUnchanged found no row at this path at all, which is what a
		// genuinely new file looks like — and also what a copy somebody just
		// asked to keep one of looks like, between its row going and its audio
		// leaving the disc. Indexing it here would give the duplicate a new row
		// before the unlink runs, and a later successful unlink would then take
		// audio out from under a file the library has never let go of.
		if fileID == uuid.Nil {
			pending, err := scanner.pendingRemoval(ctx, path)
			if err != nil {
				return err
			}
			if pending {
				return nil
			}
		}

		metadata := readTags(path)
		if metadata.Error != "" {
			result.Errors++
		}
		fileID, err = scanner.upsertFile(ctx, rootID, path, info, metadata, startedAt)
		if err != nil {
			return err
		}
		if err := scanner.recordProperties(ctx, fileID, path); err != nil {
			return err
		}
		if scanner.matcher != nil {
			if err := scanner.matcher.ReconcileFile(ctx, fileID); err != nil {
				return err
			}
		}
		result.Updated++
		progress.Publish()
		return nil
	})
	if err != nil {
		return result, err
	}

	rows, err := scanner.pool.Query(ctx, `
		UPDATE library_files
		SET missing_at = $2, updated_at = now()
		WHERE library_root_id = $1
		  AND scanned_at < $2
		  AND missing_at IS NULL
		RETURNING id
	`, rootID, startedAt)
	if err != nil {
		return result, err
	}
	var missingFileIDs []uuid.UUID
	for rows.Next() {
		var fileID uuid.UUID
		if err := rows.Scan(&fileID); err != nil {
			rows.Close()
			return result, err
		}
		missingFileIDs = append(missingFileIDs, fileID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return result, err
	}
	rows.Close()
	result.Missing = len(missingFileIDs)
	for _, fileID := range missingFileIDs {
		if scanner.matcher != nil {
			if err := scanner.matcher.ReconcileFile(ctx, fileID); err != nil {
				return result, err
			}
		}
		progress.Publish()
	}
	_, err = scanner.pool.Exec(ctx, `
		UPDATE library_roots
		SET last_scanned_at = now(), updated_at = now()
		WHERE id = $1
	`, rootID)
	return result, err
}

// touchUnchanged marks a file the scan found where it left it. The second
// return says whether anything about the row moved anyway — a file that came
// back from missing, or one that has just been given a length it never had —
// because those are the two things that happen to a file nobody touched and
// that the rest of the system still has to hear about.
func (scanner *Scanner) touchUnchanged(
	ctx context.Context,
	rootID uuid.UUID,
	path string,
	info os.FileInfo,
	scannedAt time.Time,
) (bool, bool, uuid.UUID, error) {
	var size int64
	var modified time.Time
	var fileID uuid.UUID
	var wasMissing, unread, untimed bool
	err := scanner.pool.QueryRow(ctx, `
		SELECT id, size_bytes, modified_at, missing_at IS NOT NULL,
		       properties_read_at IS NULL, duration_ms IS NULL
		FROM library_files
		WHERE path = $1
	`, path).Scan(&fileID, &size, &modified, &wasMissing, &unread, &untimed)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, false, uuid.Nil, nil
	}
	if err != nil {
		return false, false, uuid.Nil, err
	}
	if size != info.Size() || !modified.Equal(databaseTime(info.ModTime())) {
		return false, false, fileID, nil
	}
	_, err = scanner.pool.Exec(ctx, `
		UPDATE library_files
		SET library_root_id = $2, scanned_at = $3, missing_at = NULL, updated_at = now()
		WHERE path = $1
	`, path, rootID, scannedAt)
	if err == nil {
		err = scanner.linkDownloadProvenance(ctx, path, fileID)
	}
	// A file the library already holds and has never listened to is read once,
	// here, rather than by a pass of its own. Every file was indexed before
	// there was anywhere to put what its audio is, so without this the answer
	// would only ever exist for files that changed afterwards — which is to say
	// for almost none of them.
	if err == nil && unread {
		err = scanner.recordProperties(ctx, fileID, path)
	}
	// And the same argument for the length, for the same reason. Until every
	// container was taught to count its own, an MP3 had no duration unless a
	// tagger had written a TLEN frame, so almost every file in the library is
	// stored with none — and a file with none reaches the tag arm of matching as
	// album_disc_track_untimed, which is never claimed. The length exists now,
	// but only for a file the scan has a reason to re-read, which is to say for
	// almost none of them.
	//
	// Only a file that has no length is counted here. One that already has a
	// number keeps it until its bytes change: this pass is meant to give the
	// library the lengths it never had, not to relitigate the ones it holds. A
	// count that comes back with nothing leaves the column NULL, which is what
	// it already was.
	timed := false
	if err == nil && untimed {
		timed, err = scanner.recordDuration(ctx, fileID, path)
	}
	return true, wasMissing || timed, fileID, err
}

// recordDuration counts a file's length and writes it down, reporting whether
// the library learned anything. It is only ever called for a file stored with
// none, so the write can only fill a NULL in — never contradict a number some
// earlier scan already read off the same bytes.
func (scanner *Scanner) recordDuration(
	ctx context.Context, fileID uuid.UUID, path string,
) (bool, error) {
	counted := durationMS(path)
	if counted <= 0 {
		return false, nil
	}
	tag, err := scanner.pool.Exec(ctx, `
		UPDATE library_files
		SET duration_ms = $2, updated_at = now()
		WHERE id = $1 AND duration_ms IS NULL
	`, fileID, counted)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// recordProperties writes down what the audio in this file is.
//
// A file whose stream will not parse is recorded as read and left empty, so a
// scan asks once rather than every time and the comparison that reads these
// numbers refuses for want of the numbers rather than for want of an attempt.
// It is never a scan error: a file can be music the library is glad to hold and
// still say nothing this can read.
func (scanner *Scanner) recordProperties(ctx context.Context, fileID uuid.UUID, path string) error {
	properties, _ := tagging.ReadProperties(path)
	_, err := scanner.pool.Exec(ctx, `
		UPDATE library_files
		SET bit_rate_kbps = nullif($2::integer, 0),
		    sample_rate_hz = nullif($3::integer, 0),
		    channels = nullif($4::integer, 0),
		    measured_duration_ms = nullif($5::integer, 0),
		    properties_read_at = now(),
		    updated_at = now()
		WHERE id = $1
	`, fileID, properties.BitRateKbps, properties.SampleRateHz,
		properties.Channels, properties.DurationMS)
	return err
}

func (scanner *Scanner) upsertFile(
	ctx context.Context,
	rootID uuid.UUID,
	path string,
	info os.FileInfo,
	metadata extractedTags,
	scannedAt time.Time,
) (uuid.UUID, error) {
	var fileID uuid.UUID
	err := scanner.pool.QueryRow(ctx, `
		INSERT INTO library_files (
			library_root_id, path, size_bytes, modified_at, artist_tag, album_tag,
			title_tag, disc_number, track_number, duration_ms,
			musicbrainz_recording_id, isrc, scanned_at, missing_at, scan_error,
			transcoded_from_format, original_size_bytes
		)
		VALUES (
			$1, $2, $3, $4, nullif($5, ''), nullif($6, ''), nullif($7, ''),
			nullif($8, 0), nullif($9, 0), nullif($10, 0), $11,
			nullif($12, ''), $13, NULL, nullif($14, ''),
			-- What this file was before an import re-encoded it. The import
			-- wrote the note against the path it wrote; this is the scan that
			-- turns the path into a row, and it is the only moment the two can
			-- be joined. Nothing here is true of a file nobody re-encoded.
			(SELECT source_format FROM import_transcodes WHERE local_path = $2),
			(SELECT source_size_bytes FROM import_transcodes WHERE local_path = $2)
		)
		ON CONFLICT (path) DO UPDATE
		SET library_root_id = EXCLUDED.library_root_id,
		    size_bytes = EXCLUDED.size_bytes,
		    modified_at = EXCLUDED.modified_at,
		    artist_tag = EXCLUDED.artist_tag,
		    album_tag = EXCLUDED.album_tag,
		    title_tag = EXCLUDED.title_tag,
		    disc_number = EXCLUDED.disc_number,
		    track_number = EXCLUDED.track_number,
		    duration_ms = EXCLUDED.duration_ms,
		    musicbrainz_recording_id = EXCLUDED.musicbrainz_recording_id,
		    isrc = EXCLUDED.isrc,
		    -- A fingerprint describes audio, so it is kept only while the audio is
		    -- the audio it was computed from. Arriving here at all means the scan
		    -- found a size or a modification time this row did not have, which is
		    -- the one thing that can mean the bytes changed underneath it.
		    --
		    -- A tag Schall writes itself never arrives here: the tagger records the
		    -- size and time of what it wrote, so the next scan sees an unchanged
		    -- file and the fingerprint stands. That is the point — Schall rewriting
		    -- a title does not make the music something else, and throwing the
		    -- fingerprint away would buy a second decode for no new fact.
		    fingerprint = NULL,
		    witness_fingerprint = NULL,
		    witness_fingerprint_seconds = NULL,
		    -- What the audio is describes the same audio, and is cleared for the
		    -- same reason. recordProperties fills these in again immediately
		    -- afterwards; clearing them first means a file whose stream stops
		    -- parsing is left saying nothing rather than still saying what the
		    -- bytes before it said.
		    bit_rate_kbps = NULL,
		    sample_rate_hz = NULL,
		    channels = NULL,
		    measured_duration_ms = NULL,
		    properties_read_at = NULL,
		    -- How loud the audio is describes the same audio, and goes for the
		    -- same reason. Nothing refills these here: measuring means decoding
		    -- the whole stream, which is far too slow for a scan, so the file
		    -- rejoins the queue the loudness pass works through. Until it gets
		    -- there the file carries no gain rather than the gain of bytes it
		    -- no longer holds.
		    replaygain_track_gain = NULL,
		    replaygain_track_peak = NULL,
		    loudness_measured_at = NULL,
		    -- And where the music in it stops, which is a measurement of the
		    -- same audio and is worthless the moment the bytes are different
		    -- ones. Clearing spectrum_read_at is what puts the file back in
		    -- front of the sweep that decodes it.
		    spectral_cutoff_hz = NULL,
		    audio_encoder = NULL,
		    transcode_suspected = NULL,
		    spectrum_read_at = NULL,
		    scanned_at = EXCLUDED.scanned_at,
		    missing_at = NULL,
		    scan_error = EXCLUDED.scan_error,
		    -- What the file used to be is kept once it is known. The tagging
		    -- pass rewrites tags into the file the moment it is imported, which
		    -- changes its size and brings the next scan here; taking the note
		    -- away then would lose the one record that the file was a FLAC.
		    transcoded_from_format = coalesce(
		        EXCLUDED.transcoded_from_format, library_files.transcoded_from_format),
		    original_size_bytes = coalesce(
		        EXCLUDED.original_size_bytes, library_files.original_size_bytes),
		    -- A file whose tags changed is a file whose identity may have
		    -- changed with them, so it goes back into the resolution queue. A
		    -- decision a person made is never undone by a retag: they decided
		    -- about the music, not about the tags.
		    resolution_status = CASE
		        WHEN library_files.resolution_status = 'pending' OR EXISTS (
		            SELECT 1 FROM library_file_identities
		            WHERE library_file_identities.library_file_id = library_files.id
		              AND library_file_identities.is_manual
		        ) THEN library_files.resolution_status
		        ELSE 'pending'
		    END,
		    resolution_summary = CASE
		        WHEN library_files.resolution_status = 'pending' OR EXISTS (
		            SELECT 1 FROM library_file_identities
		            WHERE library_file_identities.library_file_id = library_files.id
		              AND library_file_identities.is_manual
		        ) THEN library_files.resolution_summary
		    END,
		    updated_at = now()
		RETURNING id
	`, rootID, path, info.Size(), databaseTime(info.ModTime()), metadata.Artist, metadata.Album,
		metadata.Title, metadata.DiscNumber, metadata.TrackNumber, metadata.DurationMS,
		metadata.MusicBrainzRecordingID, metadata.ISRC, scannedAt, metadata.Error).Scan(&fileID)
	if err == nil {
		err = scanner.linkDownloadProvenance(ctx, path, fileID)
	}
	return fileID, err
}

func (scanner *Scanner) linkDownloadProvenance(ctx context.Context, path string, fileID uuid.UUID) error {
	_, err := scanner.pool.Exec(ctx, `
		UPDATE downloads
		SET library_file_id = $2, updated_at = now()
		WHERE local_path = $1
	`, path, fileID)
	return err
}

// pendingRemoval reports whether this path is audio the library has let go of
// and not yet managed to delete: a library_file_removals row with no
// unlinked_at. It is the licence for "keep this copy" and the retry both read,
// and a scan must read it too — the row going and the unlink are two separate
// commits, and a scan running between them would otherwise index the audio as a
// new file before it is gone.
func (scanner *Scanner) pendingRemoval(ctx context.Context, path string) (bool, error) {
	var pending bool
	err := scanner.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM library_file_removals
			WHERE library_path = $1 AND unlinked_at IS NULL
		)
	`, path).Scan(&pending)
	if err != nil {
		return false, fmt.Errorf("check whether %s is audio the library let go of: %w", path, err)
	}
	return pending, nil
}

func isSupported(path string) bool {
	_, ok := supportedExtensions[strings.ToLower(filepath.Ext(path))]
	return ok
}

func databaseTime(value time.Time) time.Time {
	return value.Truncate(time.Microsecond)
}

func readTags(path string) extractedTags {
	file, err := os.Open(path)
	if err != nil {
		return extractedTags{Error: err.Error()}
	}
	defer file.Close()

	metadata, err := tag.ReadFrom(file)
	if err != nil {
		return extractedTags{
			DurationMS: durationMS(path), Error: err.Error(),
			TagsAbsent: errors.Is(err, tag.ErrNoTagsFound),
		}
	}
	trackNumber, _ := metadata.Track()
	discNumber, _ := metadata.Disc()
	artist := strings.TrimSpace(metadata.AlbumArtist())
	if artist == "" {
		artist = strings.TrimSpace(metadata.Artist())
	}
	result := extractedTags{
		Artist:      artist,
		Album:       strings.TrimSpace(metadata.Album()),
		Title:       strings.TrimSpace(metadata.Title()),
		DiscNumber:  discNumber,
		TrackNumber: trackNumber,
		DurationMS:  durationMS(path),
		ISRC:        rawString(metadata.Raw(), "isrc", "tsrc"),
	}
	if recordingID, err := uuid.Parse(strings.TrimSpace(mbz.Extract(metadata).Get(mbz.Recording))); err == nil {
		result.MusicBrainzRecordingID = &recordingID
	}
	return asArrived(path, result)
}

// asArrived hands back the account a managed copy arrived with, in place of the
// one Schall wrote into it.
//
// Schall writes what it proved into the files it imports, because a music
// server groups by tags and a folder tree reaches it not at all. Read back
// plainly, those tags would be Schall confirming its own past decision: a
// recording identifier it wrote itself is not a stranger agreeing with it, and
// a match has to be provable from something other than the match. So every file
// Schall tags carries the tags it arrived with, and that is what is read here —
// the evidence is exactly what it would have been had Schall never written
// anything.
//
// How long the audio is stays measured from the audio, which no tag write
// changes.
//
// The account is a tag like any other, so a tool that rewrites the file keeping
// the fields it recognises can drop it, and what is left then is what Schall
// wrote — a title, an artist, an album and a position, which agree with the
// catalogue because Schall copied them from it. A recording identifier and an
// ISRC are withheld from the file for that reason, so neither of the two
// readings that prove an identity alone can ever be Schall's own.
//
// The third reading — artist, album, disc and track agreeing together — used to
// be the hole this could not close, because that combination consulted no
// audio. It does now: it is proved against the length as well, and the length
// is measured off the audio rather than read out of a tag. So a file stripped
// of its account is a file whose tags say what Schall wrote and whose audio
// still has to agree with them, which is the same demand made of a stranger's
// copy.
func asArrived(path string, read extractedTags) extractedTags {
	arrived, tagged := tagging.AsArrived(path)
	if !tagged {
		return read
	}
	return extractedTags{
		Artist:                 arrived.Artist,
		Album:                  arrived.Album,
		Title:                  arrived.Title,
		DiscNumber:             arrived.DiscNumber,
		TrackNumber:            arrived.TrackNumber,
		DurationMS:             read.DurationMS,
		MusicBrainzRecordingID: arrived.MusicBrainzRecordingID,
		ISRC:                   arrived.ISRC,
		Error:                  read.Error,
		TagsAbsent:             read.TagsAbsent,
	}
}

func rawString(raw map[string]any, names ...string) string {
	for key, value := range raw {
		for _, name := range names {
			if !strings.EqualFold(key, name) {
				continue
			}
			switch typed := value.(type) {
			case string:
				return strings.TrimSpace(typed)
			case []string:
				if len(typed) > 0 {
					return strings.TrimSpace(typed[0])
				}
			}
		}
	}
	return ""
}

// durationMS is how long this audio is, counted off the stream's own structure
// or not known at all.
//
// It used to be a preference: measure a FLAC, and otherwise believe a `TLEN`
// frame. That fallback is gone. Duration is one of the things a match may rest
// on, and a difference of more than five seconds voids a pair whatever else
// agrees — so it is a number that can both make and unmake a decision, and a
// decision of that weight cannot rest on the file's own claim about itself.
// `TLEN` is a number somebody's tagger typed. It is wrong on files that were
// cut, on files that were transcoded, and on files whose tagger simply copied
// it from somewhere else, and every one of those wrongnesses was being read as
// evidence.
//
// What replaces it is every container saying its length out of its own
// structure — tagging.CountedDurationMS — which for an MP3 means walking the
// frames and for everything else means reading the field the encoder wrote.
// Nothing is decoded and nothing is estimated.
//
// A file that will not say still yields 0, which upsertFile stores as NULL and
// matching reads as silence rather than as agreement. That is the same answer
// as before for the same reason, and it is why an unreadable stream costs a
// scan nothing.
func durationMS(path string) int {
	counted, err := tagging.CountedDurationMS(path)
	if err != nil {
		return 0
	}
	return counted
}
