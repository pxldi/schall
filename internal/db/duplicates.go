package db

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// maxDuplicateFiles bounds what one answer carries. A release has tens of
// tracks, not hundreds, so a query that reaches this limit describes a library
// that needs attention rather than a request that needs a longer list.
const maxDuplicateFiles = 500

// DuplicateEvidence is what the library already holds that looks like one
// catalogue release. It exists because a download is an acquisition: Schall
// must be able to say what is already owned before it asks a peer for it
// again.
//
// Evidence is not a verdict. An owned file proves the release is partly there;
// an unresolved file proves only that something in the library says it is this
// release and nothing settled which track it is. Neither decides the request —
// they are what the user is shown so the user can decide.
type DuplicateEvidence struct {
	AlbumID    uuid.UUID `json:"albumId"`
	AlbumTitle string    `json:"albumTitle"`
	ArtistName string    `json:"artistName"`
	// TrackCount is what the catalogue edition expects; OwnedTrackCount is how
	// many of those tracks a present local file is already matched to.
	TrackCount      int `json:"trackCount"`
	OwnedTrackCount int `json:"ownedTrackCount"`
	// UnresolvedCount is how many present files look like this release without
	// any proven track identity.
	UnresolvedCount int `json:"unresolvedCount"`
	// Summary reads as one sentence, so a refusal can be explained without the
	// caller having to assemble it from the counts.
	Summary string          `json:"summary"`
	Files   []DuplicateFile `json:"files"`
}

// Any reports whether the library says anything at all about this release. It
// is the whole condition for asking the user first.
func (evidence DuplicateEvidence) Any() bool { return len(evidence.Files) > 0 }

// DuplicateFile is one local file that the library holds against this release.
type DuplicateFile struct {
	ID   uuid.UUID `json:"id"`
	Path string    `json:"path"`
	// State is 'owned' for a file already matched to a track of this release,
	// and 'unresolved' for one that looks like the release but is matched to
	// nothing.
	State string `json:"state"`
	// MatchStatus is the file's own matching state: matched, unmatched, or
	// ambiguous.
	MatchStatus string `json:"matchStatus"`
	// Track names the catalogue track an owned file is matched to.
	Track string `json:"track,omitempty"`
	// Tags is what the file itself says, when it says anything.
	Tags string `json:"tags,omitempty"`
	// Evidence reads as a sentence: why this file counts against the request.
	Evidence string `json:"evidence"`
}

type duplicateRow struct {
	id          uuid.UUID
	path        string
	state       string
	matchStatus string
	reason      string
	trackID     uuid.NullUUID
	trackTitle  pgtype.Text
	discNumber  pgtype.Int4
	trackNumber pgtype.Int4
	artistTag   pgtype.Text
	albumTag    pgtype.Text
	titleTag    pgtype.Text
}

// AlbumDuplicateEvidence reports what the library already holds against one
// release.
//
// The unresolved half deliberately uses the same identifiers and the same
// exact tag comparison the matcher uses, not a looser one. A file is listed
// because it carries an identifier from this release or is tagged as this
// release, never because it merely resembles it. Duplicate protection asks a
// question rather than making a decision, but a question raised by a guess
// would train the user to dismiss it.
func (q *Queries) AlbumDuplicateEvidence(ctx context.Context, albumID uuid.UUID) (DuplicateEvidence, error) {
	evidence := DuplicateEvidence{AlbumID: albumID, Files: []DuplicateFile{}}
	err := q.db.QueryRow(ctx, `
		SELECT albums.title, artists.name,
		       (SELECT count(*)::int FROM tracks WHERE tracks.album_id = albums.id)
		FROM albums
		JOIN artists ON artists.id = albums.artist_id
		WHERE albums.id = $1
	`, albumID).Scan(&evidence.AlbumTitle, &evidence.ArtistName, &evidence.TrackCount)
	if err != nil {
		return DuplicateEvidence{}, err
	}

	rows, err := q.db.Query(ctx, duplicateEvidenceQuery, albumID, maxDuplicateFiles)
	if err != nil {
		return DuplicateEvidence{}, err
	}
	defer rows.Close()

	ownedTracks := make(map[uuid.UUID]bool)
	for rows.Next() {
		var row duplicateRow
		if err := rows.Scan(
			&row.id, &row.path, &row.state, &row.matchStatus, &row.reason,
			&row.trackID, &row.trackTitle, &row.discNumber, &row.trackNumber,
			&row.artistTag, &row.albumTag, &row.titleTag,
		); err != nil {
			return DuplicateEvidence{}, err
		}
		file := DuplicateFile{
			ID: row.id, Path: row.path, State: row.state,
			MatchStatus: row.matchStatus, Track: trackLabel(row), Tags: tagLabel(row),
		}
		file.Evidence = duplicateSentence(row, file.Track)
		if row.state == "owned" {
			if row.trackID.Valid {
				ownedTracks[row.trackID.UUID] = true
			}
		} else {
			evidence.UnresolvedCount++
		}
		evidence.Files = append(evidence.Files, file)
	}
	if err := rows.Err(); err != nil {
		return DuplicateEvidence{}, err
	}
	evidence.OwnedTrackCount = len(ownedTracks)
	evidence.Summary = duplicateSummary(evidence)
	return evidence, nil
}

// AcknowledgeDuplicates records that the user saw this evidence and asked for
// the download anyway. It is stored on the request so the answer survives the
// scan that changes the library underneath it, and so a request can never be
// started on an acknowledgement nobody gave.
func (q *Queries) AcknowledgeDuplicates(
	ctx context.Context, requestID uuid.UUID, evidence DuplicateEvidence,
) (DownloadRequestRow, error) {
	encoded, err := encodeDuplicateEvidence(evidence)
	if err != nil {
		return DownloadRequestRow{}, err
	}
	tag, err := q.db.Exec(ctx, `
		UPDATE download_requests
		SET duplicate_acknowledged_at = now(), duplicate_evidence = $2::jsonb,
		    updated_at = now()
		WHERE id = $1 AND status = 'requested'
	`, requestID, encoded)
	if err != nil {
		return DownloadRequestRow{}, err
	}
	if tag.RowsAffected() == 0 {
		return DownloadRequestRow{}, pgx.ErrNoRows
	}
	return q.DownloadRequest(ctx, requestID)
}

func encodeDuplicateEvidence(evidence DuplicateEvidence) ([]byte, error) {
	encoded, err := json.Marshal(evidence)
	if err != nil {
		return nil, fmt.Errorf("encode duplicate evidence: %w", err)
	}
	return encoded, nil
}

func trackLabel(row duplicateRow) string {
	if !row.trackTitle.Valid {
		return ""
	}
	position := ""
	switch {
	case row.discNumber.Valid && row.discNumber.Int32 > 1 && row.trackNumber.Valid:
		position = fmt.Sprintf("%d-%d. ", row.discNumber.Int32, row.trackNumber.Int32)
	case row.trackNumber.Valid:
		position = fmt.Sprintf("%d. ", row.trackNumber.Int32)
	}
	return position + row.trackTitle.String
}

func tagLabel(row duplicateRow) string {
	parts := make([]string, 0, 3)
	for _, tag := range []pgtype.Text{row.artistTag, row.albumTag, row.titleTag} {
		if value := strings.TrimSpace(tag.String); tag.Valid && value != "" {
			parts = append(parts, value)
		}
	}
	return strings.Join(parts, " — ")
}

// duplicateSentence says why one file stands against the request, in the words
// of the evidence that put it there.
func duplicateSentence(row duplicateRow, track string) string {
	if row.state == "owned" {
		return "is already " + ownedBy(row.reason) + named(track) + "."
	}
	return looksLike(row.reason) + ", " + unsettled(row.matchStatus) + "."
}

func ownedBy(method string) string {
	switch method {
	case "manual":
		return "matched by hand"
	case "verified_download":
		return "matched through a download Schall imported"
	case "musicbrainz_recording_id":
		return "matched by MusicBrainz recording ID"
	case "resolved_identity":
		return "matched through the identity resolved for it"
	case "isrc":
		return "matched by ISRC"
	case "album_disc_track":
		return "matched by artist, album, and track position"
	default:
		return "matched"
	}
}

func named(track string) string {
	if track == "" {
		return " to a track of this release"
	}
	return " to " + track
}

func looksLike(reason string) string {
	switch reason {
	case "recording_id":
		return "carries a MusicBrainz recording ID from this release"
	case "identity":
		return "was resolved to a recording from this release"
	case "isrc":
		return "carries an ISRC from this release"
	default:
		return "is tagged as this release"
	}
}

func unsettled(matchStatus string) string {
	if matchStatus == "ambiguous" {
		return "and more than one catalogue track fits it"
	}
	return "but no catalogue track was proven for it"
}

// duplicateSummary states the case as a whole. It is what a refusal says
// first, so it has to read for one file as well as for many.
func duplicateSummary(evidence DuplicateEvidence) string {
	if !evidence.Any() {
		return "The library holds nothing that looks like this release."
	}
	parts := make([]string, 0, 2)
	if evidence.OwnedTrackCount > 0 {
		switch {
		// A single-track release has no "all" to speak of, and "all 1 track"
		// is not a sentence anyone would write.
		case evidence.TrackCount == 1 && evidence.OwnedTrackCount >= 1:
			parts = append(parts, "The library already holds the only track of this release")
		case evidence.TrackCount > 0 && evidence.OwnedTrackCount >= evidence.TrackCount:
			parts = append(parts, fmt.Sprintf(
				"The library already holds all %d tracks of this release",
				evidence.TrackCount))
		case evidence.TrackCount > 0:
			parts = append(parts, fmt.Sprintf(
				"The library already holds %d of the %s of this release",
				evidence.OwnedTrackCount, count(evidence.TrackCount, "track", "tracks")))
		default:
			parts = append(parts, fmt.Sprintf(
				"The library already holds %s of this release",
				count(evidence.OwnedTrackCount, "track", "tracks")))
		}
	}
	if evidence.UnresolvedCount > 0 {
		unresolved := fmt.Sprintf("%s that %s like this release without a proven track identity",
			count(evidence.UnresolvedCount, "file", "files"),
			plural(evidence.UnresolvedCount, "looks", "look"))
		if len(parts) > 0 {
			parts = append(parts, "and "+unresolved)
		} else {
			parts = append(parts, "The library holds "+unresolved)
		}
	}
	return strings.Join(parts, ", ") + "."
}

func count(value int, singular, plural string) string {
	if value == 1 {
		return "1 " + singular
	}
	return fmt.Sprintf("%d %s", value, plural)
}

func plural(value int, singular, many string) string {
	if value == 1 {
		return singular
	}
	return many
}

// MaxHeldTwiceRecordings bounds KeepOneOfEach's sweep, which reads every
// recording held twice at once rather than a page of them. A library that
// holds hundreds of recordings more than once is describing a problem to work
// through rather than a page to scroll, and the count beside the list says
// how much of it is here.
const MaxHeldTwiceRecordings = 200

// HeldTwice is every recording the library holds more than one copy of.
//
// Total counts the recordings before the page is cut, so a list that stops at
// the limit can still say how many there are.
type HeldTwice struct {
	Recordings []HeldTwiceRecording `json:"recordings"`
	Total      int                  `json:"total"`
	// StillOnDisc is every file the library has let go of and could not delete.
	// It belongs on this answer rather than beside a recording, because such a
	// file has no library row left and its recording is no longer held twice —
	// so nothing else on this screen would ever mention it again.
	StillOnDisc []RemovedFileStillOnDisc `json:"stillOnDisc"`
}

// RemovedFileStillOnDisc is one audio file the library removed and the disc
// kept: a read-only mount, a permission, or a crash between the two steps.
type RemovedFileStillOnDisc struct {
	Path      string    `json:"path"`
	Reason    string    `json:"reason,omitempty"`
	RemovedAt time.Time `json:"removedAt"`
}

// maxStillOnDisc bounds that list. It is normally empty; a library where it is
// long has one problem, not fifty, and the first few say what it is.
const maxStillOnDisc = 50

// RemovalsStillOnDisc reads the files the library let go of and could not
// delete. An empty answer is the ordinary one.
func (q *Queries) RemovalsStillOnDisc(ctx context.Context) ([]RemovedFileStillOnDisc, error) {
	rows, err := q.db.Query(ctx, `
		SELECT library_path, unlink_error, removed_at
		FROM library_file_removals
		WHERE unlinked_at IS NULL
		ORDER BY removed_at DESC
		LIMIT $1
	`, maxStillOnDisc)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	stranded := []RemovedFileStillOnDisc{}
	for rows.Next() {
		var found RemovedFileStillOnDisc
		var removedAt pgtype.Timestamptz
		if err := rows.Scan(&found.Path, &found.Reason, &removedAt); err != nil {
			return nil, err
		}
		found.RemovedAt = removedAt.Time
		stranded = append(stranded, found)
	}
	return stranded, rows.Err()
}

// HeldTwiceRecording is one recording and the copies of it sitting on disc.
//
// Title and artist are what something already decided calls this recording — a
// resolved identity first, then a catalogue track. Both can be empty: a
// recording only ever reached through a mapping onto a track nobody titled is
// still a recording held twice, and saying so with an empty name is honest
// where inventing one would not be.
type HeldTwiceRecording struct {
	RecordingID uuid.UUID       `json:"recordingId"`
	Title       string          `json:"title,omitempty"`
	Artist      string          `json:"artist,omitempty"`
	Copies      []HeldTwiceCopy `json:"copies"`
	// Verdict reads as one sentence: which copy is worth keeping and what makes
	// it the better one, or — when nothing stored separates them — why Schall
	// will not say. It is advice about files and never a decision about music.
	Verdict string `json:"verdict"`
	// Decided reports whether one copy was proven better than every other, which
	// is the whole condition for offering to remove the rest.
	Decided bool `json:"decided"`
}

// HeldTwiceCopy is one file answering that recording, described by what the
// library actually stores about it.
//
// Format is still read off the path, because the container is the one thing the
// stream will not say: an .m4a holds AAC or ALAC and taglib reports neither. The
// numbers beside it are read off the audio (migration 00045) and are null for a
// file nothing has listened to yet, which is why a comparison can refuse.
type HeldTwiceCopy struct {
	ID           uuid.UUID `json:"id"`
	Path         string    `json:"path"`
	Format       string    `json:"format,omitempty"`
	SizeBytes    int64     `json:"sizeBytes"`
	DurationMS   *int32    `json:"durationMs,omitempty"`
	BitRateKbps  *int32    `json:"bitRateKbps,omitempty"`
	SampleRateHz *int32    `json:"sampleRateHz,omitempty"`
	Channels     *int32    `json:"channels,omitempty"`
	// SpectralCutoffHz is the frequency the music in this copy stops at, and
	// TranscodeSuspected says that stop is where a lossy encoder would have put
	// it in a file whose container or bit rate says otherwise (migration 00067).
	// Both are absent for a copy nothing has decoded yet, and absent is never
	// agreement that a copy is fine.
	//
	// They are shown and never counted. judge does not read them: which copy is
	// the better one is decided on the stored facts it has always been decided
	// on, and a suspicion is for the person to weigh.
	SpectralCutoffHz   *int32 `json:"spectralCutoffHz,omitempty"`
	TranscodeSuspected *bool  `json:"transcodeSuspected,omitempty"`
	// Keeper is the copy worth holding on to, and Redundant the copies proven
	// to be lesser versions of it. Both are false on every copy of a group
	// nothing could separate, so a list that cannot decide offers nothing.
	Keeper    bool `json:"keeper,omitempty"`
	Redundant bool `json:"redundant,omitempty"`
	// AnswersOther says this file is also a copy of different music: it carries
	// a second identity, or a mapping onto a track of another recording. Keeping
	// one copy of this recording never deletes it, because the press said
	// nothing about the other music, and the screen has to say so before the
	// person presses.
	AnswersOther bool `json:"answersOther,omitempty"`
	// measured is the length read off the stream, which unlike DurationMS
	// exists for an MP3. It is what the comparison uses and is not published:
	// nothing outside this file may read a duration that matching does not.
	measured int
	// mapped says this copy is matched to a catalogue track rather than only
	// identified, and byHand that somebody decided it themselves.
	mapped bool
	byHand bool
	// Release is where this copy sits: the catalogue release it is mapped to
	// on, failing that the album its own tags claim. It is what usually tells
	// the album copy from the single.
	Release string `json:"release,omitempty"`
	// Reason says how this file came to answer the recording, in the words of
	// the two things that can answer it.
	Reason string `json:"reason"`
	// KeptAt is when somebody said they meant to hold this copy too.
	KeptAt *time.Time `json:"keptAt,omitempty"`
}

// RecordingsHeldTwice reports every recording that more than one file in the
// library answers.
//
// It reaches a recording exactly the two ways OwnedFileForRecording reaches it:
// an identity resolved against the file, or a mapping onto a catalogue track
// carrying the recording. Only decided identities count, for the same reason
// they do there — a bare tag is what identity resolution exists to grade, and
// two files agreeing about an unproven identifier is not the library holding
// the same recording twice.
//
// This is not the matcher's 'duplicate' status and does not touch it. That
// status is about a track another file was given by hand; this is about the
// music itself, which one MusicBrainz recording holds across every release that
// carries it. A recording on two releases the library has one file for is not
// here, because one file is not twice.
//
// kept splits the list in two rather than filtering it: false is what somebody
// still has to answer, true is what somebody said they meant to have. The split
// is made over the whole group, once the copies are counted — a recording is
// kept when every copy of it is, and is still to decide while any copy is not.
// Counting only the copies on one side of the answer would hide a third copy
// that turned up after a pair was kept: two answered files and one new one
// would be a group of one on either side, and the new copy would be listed
// nowhere.
//
// limit and offset page the recording groups, not the copies inside them — a
// group of three copies is still one row of the page it counts against. total
// is read with its own query rather than a window function over the page, so
// it stays right even when offset lands past the end of the list.
func (q *Queries) RecordingsHeldTwice(ctx context.Context, kept bool, limit, offset int32) (HeldTwice, error) {
	result := HeldTwice{Recordings: []HeldTwiceRecording{}, StillOnDisc: []RemovedFileStillOnDisc{}}
	var total int
	if err := q.db.QueryRow(ctx, heldTwiceCountQuery, kept).Scan(&total); err != nil {
		return HeldTwice{}, err
	}
	result.Total = total

	rows, err := q.db.Query(ctx, heldTwiceQuery, kept, limit, offset)
	if err != nil {
		return HeldTwice{}, err
	}
	defer rows.Close()

	for rows.Next() {
		var recordingID uuid.UUID
		var title, artist string
		var copied HeldTwiceCopy
		var duration, bitRate, sampleRate, channels, measured pgtype.Int4
		var cutoff pgtype.Int4
		var suspected pgtype.Bool
		var keptAt pgtype.Timestamptz
		var byMapping, byIdentity, byHand bool
		var release, albumTag pgtype.Text
		if err := rows.Scan(
			&recordingID, &title, &artist,
			&copied.ID, &copied.Path, &copied.SizeBytes, &duration, &keptAt,
			&byMapping, &byIdentity, &byHand,
			&bitRate, &sampleRate, &channels, &measured, &cutoff, &suspected,
			&release, &albumTag, &copied.AnswersOther,
		); err != nil {
			return HeldTwice{}, err
		}
		copied.Format = fileFormat(copied.Path)
		copied.DurationMS = int32Pointer(duration)
		copied.BitRateKbps = int32Pointer(bitRate)
		copied.SampleRateHz = int32Pointer(sampleRate)
		copied.Channels = int32Pointer(channels)
		copied.SpectralCutoffHz = int32Pointer(cutoff)
		if suspected.Valid {
			copied.TranscodeSuspected = &suspected.Bool
		}
		copied.KeptAt = nullableTime(keptAt)
		copied.Release = firstText(release, albumTag)
		copied.Reason = heldTwiceReason(byMapping, byIdentity)
		copied.measured = int(measured.Int32)
		copied.mapped = byMapping
		copied.byHand = byHand

		last := len(result.Recordings) - 1
		if last < 0 || result.Recordings[last].RecordingID != recordingID {
			result.Recordings = append(result.Recordings, HeldTwiceRecording{
				RecordingID: recordingID, Title: title, Artist: artist,
				Copies: []HeldTwiceCopy{},
			})
			last++
		}
		result.Recordings[last].Copies = append(result.Recordings[last].Copies, copied)
	}
	if err := rows.Err(); err != nil {
		return HeldTwice{}, err
	}
	// Judged once every copy of a group is in hand, because which copy is the
	// better one is a fact about all of them together.
	//
	// Only the half still to decide is judged. A group somebody answered is not
	// waiting on advice, and telling them which copy they should have kept is
	// arguing with a decision rather than reporting a fact.
	if !kept {
		for index := range result.Recordings {
			recording := &result.Recordings[index]
			recording.Verdict, recording.Decided = judge(recording.Copies)
		}
	}

	// Read on the same trip, because a file the disc kept is the one thing on
	// this screen that nothing else would ever mention again.
	stranded, err := q.RemovalsStillOnDisc(ctx)
	if err != nil {
		return HeldTwice{}, err
	}
	result.StillOnDisc = stranded
	return result, nil
}

// KeepRecordingHeldTwice records, or withdraws, the answer that the library is
// meant to hold this recording more than once.
//
// It writes nothing about the music and moves no file: every copy stays where
// it is, and the group only stops being listed. The whole group is answered at
// once because the question was asked about the group — answering one copy of
// two would leave the other on its own. An answer already given keeps its own
// date, so withdrawing and giving it again is the only way to change when it
// was made.
//
// Only a recording the library really does hold more than once can be kept:
// "yes, on purpose" is an answer to a question, and there is no question about
// a file that is the only copy of anything. Withdrawing is not held to that,
// because an answer has to be undoable after the fact it was about has changed
// — a pair one of whose copies went away is a single file with a stale
// timestamp, and 00042 already says that timestamp means nothing once the file
// is not a duplicate.
//
// It returns how many files were answered. None means there was nothing to
// answer: no present file in the library carries this recording, or only one
// does and it was being kept.
func (q *Queries) KeepRecordingHeldTwice(ctx context.Context, recordingID uuid.UUID, kept bool) (int64, error) {
	tag, err := q.db.Exec(ctx, `
		WITH answering AS (
		    SELECT library_files.id
		    FROM library_files
		    WHERE library_files.missing_at IS NULL
		      AND (EXISTS (
		              SELECT 1 FROM library_file_identities
		              WHERE library_file_identities.library_file_id = library_files.id
		                AND library_file_identities.musicbrainz_recording_id = $1
		          ) OR EXISTS (
		              SELECT 1 FROM track_mappings
		              JOIN tracks ON tracks.id = track_mappings.track_id
		              WHERE track_mappings.library_file_id = library_files.id
		                AND tracks.musicbrainz_recording_id = $1
		          ))
		)
		UPDATE library_files
		SET duplicate_kept_at = CASE
		        WHEN $2 THEN coalesce(duplicate_kept_at, now())
		        ELSE NULL
		    END,
		    updated_at = now()
		WHERE library_files.id IN (SELECT id FROM answering)
		  AND (NOT $2 OR (SELECT count(*) FROM answering) > 1)
	`, recordingID, kept)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// fileFormat is the container the path claims, upper-cased. Nothing stores what
// the audio inside actually is, so this is a label off the name and never
// evidence about the music.
func fileFormat(path string) string {
	extension := strings.TrimPrefix(filepath.Ext(path), ".")
	if extension == "" {
		return ""
	}
	return strings.ToUpper(extension)
}

func heldTwiceReason(byMapping, byIdentity bool) string {
	switch {
	case byMapping && byIdentity:
		return "Matched to a catalogue track, and identified as this recording"
	case byMapping:
		return "Matched to a catalogue track"
	default:
		return "Identified as this recording"
	}
}

func firstText(values ...pgtype.Text) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value.String); value.Valid && trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func int32Pointer(value pgtype.Int4) *int32 {
	if !value.Valid {
		return nil
	}
	return &value.Int32
}

func nullableTime(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}
	return &value.Time
}

// A recording is held twice when two present files answer it, reached the two
// ways a file can be proven to be a recording at all. The grouping is on the
// recording rather than the track on purpose: the catalogue carries one row per
// release a recording appears on, so counting tracks would call an album copy
// and the single lifted from it two different pieces of music, which is exactly
// how the library came to hold both without anything noticing.
// heldTwiceCTEs is the recording-groups-held-twice computation shared by the
// count and the page: which recordings qualify does not depend on which page
// is being asked for, so both queries build it the same way.
const heldTwiceCTEs = `
WITH answers AS (
    SELECT library_files.id AS file_id,
           library_file_identities.musicbrainz_recording_id AS recording_id,
           false AS by_mapping, true AS by_identity,
           library_file_identities.is_manual AS by_hand
    FROM library_files
    JOIN library_file_identities
      ON library_file_identities.library_file_id = library_files.id
    WHERE library_files.missing_at IS NULL
      AND library_file_identities.musicbrainz_recording_id IS NOT NULL
    UNION ALL
    SELECT library_files.id, tracks.musicbrainz_recording_id, true, false,
           track_mappings.is_manual
    FROM library_files
    JOIN track_mappings ON track_mappings.library_file_id = library_files.id
    JOIN tracks ON tracks.id = track_mappings.track_id
    WHERE library_files.missing_at IS NULL
      AND tracks.musicbrainz_recording_id IS NOT NULL
), copies AS (
    -- One file can answer the same recording both ways, and can be mapped to
    -- the recording's track on several releases at once (migration 00033).
    -- That is one copy on disc however many rows say so, so the count that
    -- decides "more than once" is over files.
    SELECT file_id, recording_id,
           bool_or(by_mapping) AS by_mapping,
           bool_or(by_identity) AS by_identity,
           bool_or(by_hand) AS by_hand
    FROM answers
    GROUP BY file_id, recording_id
), held AS (
    -- Counted first, answered second. Which list a group belongs to is a fact
    -- about the whole group -- every copy answered, or not yet -- so it is read
    -- after the copies are counted rather than by keeping only the files on one
    -- side of the answer, which would drop a group below two and hide it from
    -- both lists.
    SELECT copies.recording_id
    FROM copies
    JOIN library_files ON library_files.id = copies.file_id
    GROUP BY copies.recording_id
    HAVING count(*) > 1
       AND bool_and(library_files.duplicate_kept_at IS NOT NULL) = $1
)`

// heldTwiceCountQuery counts the recording groups before any page is cut, so
// the count stays right even for a page past the end of the list -- a window
// function over the page rows would read zero there instead of the real total.
const heldTwiceCountQuery = heldTwiceCTEs + `
SELECT count(*) FROM held
`

const heldTwiceQuery = heldTwiceCTEs + `
, named AS (
    SELECT held.recording_id,
           coalesce(
               (SELECT identity.track_title
                FROM library_file_identities identity
                WHERE identity.musicbrainz_recording_id = held.recording_id
                  AND identity.track_title IS NOT NULL
                ORDER BY identity.decided_at, identity.id
                LIMIT 1),
               (SELECT tracks.title
                FROM tracks
                WHERE tracks.musicbrainz_recording_id = held.recording_id
                ORDER BY tracks.title
                LIMIT 1),
               '') AS title,
           coalesce(
               (SELECT identity.artist_name
                FROM library_file_identities identity
                WHERE identity.musicbrainz_recording_id = held.recording_id
                  AND identity.artist_name IS NOT NULL
                ORDER BY identity.decided_at, identity.id
                LIMIT 1),
               (SELECT artists.name
                FROM tracks
                JOIN albums ON albums.id = tracks.album_id
                JOIN artists ON artists.id = albums.artist_id
                WHERE tracks.musicbrainz_recording_id = held.recording_id
                ORDER BY artists.name
                LIMIT 1),
               '') AS artist
    FROM held
), page AS (
    SELECT * FROM named
    ORDER BY lower(artist), lower(title), recording_id
    LIMIT $2 OFFSET $3
)
SELECT page.recording_id, page.title, page.artist,
       library_files.id, library_files.path, library_files.size_bytes,
       library_files.duration_ms, library_files.duplicate_kept_at,
       copies.by_mapping, copies.by_identity, copies.by_hand,
       library_files.bit_rate_kbps, library_files.sample_rate_hz,
       library_files.channels, library_files.measured_duration_ms,
       library_files.spectral_cutoff_hz, library_files.transcode_suspected,
       (SELECT albums.title
        FROM track_mappings
        JOIN tracks ON tracks.id = track_mappings.track_id
        JOIN albums ON albums.id = tracks.album_id
        WHERE track_mappings.library_file_id = library_files.id
          AND tracks.musicbrainz_recording_id = page.recording_id
        ORDER BY albums.title
        LIMIT 1),
       library_files.album_tag,
       -- Whether this file is also a copy of different music. One file can
       -- answer two recordings — an identity is resolved per file and the
       -- catalogue holds a recording as one track row per release — and such a
       -- file is never deleted when somebody keeps one copy of this one.
       EXISTS (
           SELECT 1 FROM library_file_identities other
           WHERE other.library_file_id = library_files.id
             AND other.musicbrainz_recording_id IS NOT NULL
             AND other.musicbrainz_recording_id <> page.recording_id
       ) OR EXISTS (
           SELECT 1 FROM track_mappings mapping
           JOIN tracks other_track ON other_track.id = mapping.track_id
           WHERE mapping.library_file_id = library_files.id
             AND other_track.musicbrainz_recording_id IS NOT NULL
             AND other_track.musicbrainz_recording_id <> page.recording_id
       )
FROM page
JOIN copies ON copies.recording_id = page.recording_id
JOIN library_files ON library_files.id = copies.file_id
ORDER BY lower(page.artist), lower(page.title), page.recording_id, library_files.path
`

// The owned half is a fact: a present file is mapped to a track of this
// release. The unresolved half is a suspicion, held to the same identifiers
// and the same exact tag comparison the matcher uses. A file already matched
// to some other release is neither, because its identity is settled and is not
// this one.
const duplicateEvidenceQuery = `
WITH release_tracks AS (
    SELECT id, title, disc_number, track_number, musicbrainz_recording_id, isrc
    FROM tracks
    WHERE album_id = $1
), owned AS (
    SELECT library_files.id, library_files.path, 'owned'::text AS state,
           library_files.match_status,
           CASE WHEN track_mappings.is_manual THEN 'manual'
                ELSE track_mappings.method END AS reason,
           release_tracks.id AS track_id, release_tracks.title AS track_title,
           release_tracks.disc_number, release_tracks.track_number,
           library_files.artist_tag, library_files.album_tag, library_files.title_tag
    FROM library_files
    JOIN track_mappings ON track_mappings.library_file_id = library_files.id
    JOIN release_tracks ON release_tracks.id = track_mappings.track_id
    WHERE library_files.missing_at IS NULL
), unresolved AS (
    SELECT library_files.id, library_files.path, 'unresolved'::text AS state,
           library_files.match_status, evidence.reason,
           NULL::uuid AS track_id, NULL::text AS track_title,
           NULL::integer AS disc_number, NULL::integer AS track_number,
           library_files.artist_tag, library_files.album_tag, library_files.title_tag
    FROM library_files
    CROSS JOIN LATERAL (
        SELECT CASE
            WHEN library_files.musicbrainz_recording_id IS NOT NULL AND EXISTS (
                SELECT 1 FROM release_tracks
                WHERE release_tracks.musicbrainz_recording_id
                      = library_files.musicbrainz_recording_id
            ) THEN 'recording_id'
            WHEN EXISTS (
                SELECT 1
                FROM library_file_identities identity
                JOIN release_tracks
                  ON release_tracks.musicbrainz_recording_id
                     = identity.musicbrainz_recording_id
                WHERE identity.library_file_id = library_files.id
            ) THEN 'identity'
            WHEN library_files.isrc IS NOT NULL AND EXISTS (
                SELECT 1 FROM release_tracks
                WHERE upper(btrim(release_tracks.isrc)) = upper(btrim(library_files.isrc))
            ) THEN 'isrc'
            WHEN library_files.artist_tag IS NOT NULL AND library_files.album_tag IS NOT NULL
                 AND EXISTS (
                SELECT 1
                FROM albums
                JOIN artists ON artists.id = albums.artist_id
                WHERE albums.id = $1
                  AND lower(btrim(artists.name)) = lower(btrim(library_files.artist_tag))
                  AND lower(btrim(albums.title)) = lower(btrim(library_files.album_tag))
            ) THEN 'tags'
        END AS reason
    ) evidence
    WHERE library_files.missing_at IS NULL
      AND library_files.match_status <> 'matched'
      AND evidence.reason IS NOT NULL
)
SELECT id, path, state, match_status, reason, track_id, track_title,
       disc_number, track_number, artist_tag, album_tag, title_tag
FROM (SELECT * FROM owned UNION ALL SELECT * FROM unresolved) files
ORDER BY state, disc_number NULLS LAST, track_number NULLS LAST, path
LIMIT $2
`
