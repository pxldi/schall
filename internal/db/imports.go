package db

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pxldi/schall/internal/chromaprint"
)

// ArtistCredit is one artist of a credit as the catalogue holds it: the name
// the release prints and the MusicBrainz artist that name is. An artist
// MusicBrainz has no entity for has no identifier, and the credit still takes
// its place in the list — rejoining the names has to reproduce the credit the
// release was published with, and one left out for being unidentified would
// name the record as by somebody it is not. What a gap costs the identifiers is
// settled where they are written, not here: see internal/tagging.
//
// The phrase MusicBrainz prints between one credit and the next is not here.
// The joined credit is a string the catalogue already holds, and rejoining the
// list to make a second copy of it is how the two come to disagree.
type ArtistCredit struct {
	Name     string
	ArtistID uuid.UUID
}

type ImportTrack struct {
	ID                     uuid.UUID
	Title                  string
	DiscNumber             int
	TrackNumber            int
	DurationMS             int
	MusicBrainzRecordingID *uuid.UUID
	ISRC                   string
	// Credits is who the catalogue says this track is by, in MusicBrainz's own
	// order. A track the catalogue holds no credit for has none, which is
	// silence: what is written into the file is then the joined string alone.
	Credits []ArtistCredit
}

type DownloadImportRow struct {
	RequestID uuid.UUID
	AlbumID   uuid.UUID
	// ReleaseGroupID is what this release is called everywhere else in Schall,
	// and it is what the folder is filed under. A file acquired one at a time
	// for a want knows the same identifier and nothing else in common with this
	// row, so filing both by it is what puts them in one folder.
	ReleaseGroupID uuid.NullUUID
	// ReleaseID and ReleaseDate are the edition the tracks were taken from —
	// the release itself rather than the group of releases it belongs to. They
	// are what a music server groups an album by, so they are written into the
	// files. An album whose edition has not been chosen yet has neither.
	ReleaseID   uuid.NullUUID
	ReleaseDate string
	ArtistName  string
	AlbumTitle  string
	// AlbumCredits is who the catalogue says the album is by. It is a different
	// claim from the track's own credit and is written into a different tag:
	// this one is the album's artists, and ArtistName beside it is the one
	// artist the album is filed under.
	AlbumCredits []ArtistCredit
	// Genre is what the MusicBrainz community voted this release group is,
	// falling back to the artist's leading genre. One genre, because a music
	// server browses by one. Empty where MusicBrainz holds no votes for either.
	Genre           string
	SourceDirectory string
	Files           []DownloadRequestFile
	Tracks          []ImportTrack
	// Decisions are the per-track judgements a reviewer recorded, by file name.
	Decisions map[string]ImportDecision
}

// ImportedDownloadFile is where one downloaded file ended up, and the catalogue
// track it was identified as. A zero TrackID is a file that has no catalogue
// track — one fetched for a want, proven against a recording rather than against
// an edition.
type ImportedDownloadFile struct {
	LocalPath string
	TrackID   uuid.UUID
}

// ImportEvidence is the full account of one validation attempt. Validation
// gathers every disagreement in a single pass, so review can show what the
// downloaded files claim beside what the catalogue edition expects instead of
// only the first reason validation stopped at.
type ImportEvidence struct {
	// Files covers every requested file, in request order, matched or not.
	Files []ImportFileEvidence `json:"files"`
	// UnmatchedTracks are catalogue tracks that no downloaded file claimed.
	UnmatchedTracks []ImportTags `json:"unmatchedTracks"`
	// Problems are disagreements about the release as a whole rather than
	// about one file, such as a file count that cannot cover the edition.
	Problems []string `json:"problems"`
}

// ImportFileEvidence is one downloaded file beside the catalogue track it
// claimed. Expected is nil when no catalogue position matches, which is itself
// the finding. Problems is empty for a file that validated cleanly; such files
// are still reported so review sees the whole release, not only its failures.
type ImportFileEvidence struct {
	Name     string      `json:"name"`
	Position string      `json:"position"`
	Observed *ImportTags `json:"observed,omitempty"`
	Expected *ImportTags `json:"expected,omitempty"`
	// Match is how this file was tied to the catalogue track in Expected. It is
	// nil when nothing matched it, which is what Candidates is then for.
	Match *ImportMatch `json:"match,omitempty"`
	// Candidates are the catalogue tracks an unmatched file might still be,
	// best first, each with what agrees and what stands against it. They are
	// the answers review can give; matching itself refused to choose.
	Candidates []ImportCandidate `json:"candidates,omitempty"`
	Problems   []string          `json:"problems"`
	// Acoustic is what the decoded audio says this file is, when the acoustic
	// check ran. Every other field here repeats a claim somebody made about the
	// file; this one is computed from the sound, so it is the only evidence a
	// mislabelled file cannot forge.
	Acoustic *ImportAcoustic `json:"acoustic,omitempty"`
	// Resolvable reports whether a person could settle this file by naming the
	// catalogue track it belongs to. Only disagreements about identity qualify.
	// Damage, absence, and unreadable audio are never a judgement call, so they
	// stay refusals no decision can overrule.
	Resolvable bool `json:"resolvable"`
	// Decision is the judgement that was applied to this file during the
	// attempt that produced this evidence.
	Decision *ImportFileDecision `json:"decision,omitempty"`
}

// ImportMatch is how one downloaded file was identified as one catalogue
// track. Method is the stable name of the evidence that settled it; Notes are
// the things the file says that the catalogue does not, recorded because they
// are worth knowing even though they did not stand in the way — a file
// collected from a different release is still the right recording.
type ImportMatch struct {
	Method  string   `json:"method"`
	Summary string   `json:"summary"`
	Notes   []string `json:"notes,omitempty"`
}

// ImportCandidate is a catalogue track one downloaded file might be, together
// with the evidence for and against it. A candidate is never enough to import:
// it exists so that review can decide what matching would not.
type ImportCandidate struct {
	Track   ImportTags `json:"track"`
	Agrees  []string   `json:"agrees"`
	Differs []string   `json:"differs"`
	// TakenBy names the file already holding this track, when one does.
	TakenBy string `json:"takenBy,omitempty"`
}

// ImportFileDecision is a recorded judgement as it appeared to validation.
type ImportFileDecision struct {
	TrackID    uuid.UUID `json:"trackId"`
	TrackTitle string    `json:"trackTitle"`
	Note       string    `json:"note,omitempty"`
	DecidedAt  time.Time `json:"decidedAt"`
}

// ImportTags is one side of the comparison: either the tags read from a
// downloaded file or the catalogue track it was measured against. TrackID is
// set only on the catalogue side, where review needs to be able to name the
// track it is deciding about.
// ImportAcoustic is the verdict of identifying a file by its audio.
//
// Agreement is strong evidence and disagreement is stronger still, but silence
// is not evidence at all: AcoustID has never heard most self-released music,
// and an unknown recording says nothing whatever about the file.
type ImportAcoustic struct {
	// RecordingIDs are every MusicBrainz recording the audio matched, best
	// first and across all the clusters. Empty means the audio was fingerprinted
	// and not recognised.
	//
	// It is the whole answer rather than the part that decides, and it stays that
	// way: it is what the review page has always read, and evidence written
	// before clusters were recorded has nothing else in it.
	RecordingIDs []string `json:"recordingIds"`
	// Clusters is the same answer with AcoustID's own boundaries left in, best
	// first. A cluster naming several recordings is AcoustID saying they are one
	// piece of audio; a second cluster is different audio that resembled the
	// fingerprint. Which is which is what decides a copy, and the flat list above
	// cannot say.
	Clusters []ImportAcousticCluster `json:"clusters,omitempty"`
	// Score is the service's confidence in the best cluster, 0 when there is none.
	Score float64 `json:"score,omitempty"`
	// Agrees is set only when the catalogue track has a recording ID to compare
	// against and the audio was recognised. Nil means the question could not be
	// asked, which is different from a "no".
	Agrees *bool `json:"agrees,omitempty"`
	// Unavailable records why no acoustic evidence was gathered, when something
	// was tried and did not work. A check that silently did nothing would be
	// worse than an absent one.
	Unavailable string `json:"unavailable,omitempty"`
	// Damaged records what the decoder complained about while reading audio it
	// nevertheless got through, and is empty when the decode was clean. It is a
	// statement about the copy rather than about its identity — the fingerprint
	// covered the file either way, or there would be no evidence here at all —
	// so it decides nothing and is kept for whoever reads this later.
	Damaged string `json:"damaged,omitempty"`
}

// ImportAcousticCluster is one of AcoustID's fingerprint clusters as it was
// answered: what it scored, and the MusicBrainz recordings linked to it.
type ImportAcousticCluster struct {
	ID           string   `json:"id,omitempty"`
	Score        float64  `json:"score"`
	RecordingIDs []string `json:"recordingIds"`
}

// ImportAnchor is one copy measured against the want's anchor.
//
// An anchor is the thirty-second excerpt a distributor publishes for the want's
// own ISRC — the code the recording was registered under, an exact key and not
// a text search — so it is the audio of exactly the recording the want names.
// For a want no distributor carries it is instead thirty seconds of a YouTube
// upload chosen by the want's name and length. A matching Topic upload may admit
// a copy; another name-found upload leaves it a question and never refuses it.
// Comparing a copy against it answers the question AcoustID answers, without
// AcoustID and without MusicBrainz holding the music at all.
//
// The comparison is one number: how many bits of the excerpt's fingerprint
// differ from the copy's at the best alignment. It is kept here so that whoever
// reads this later sees what the copy was measured against and what it came to,
// rather than a verdict with nothing behind it.
type ImportAnchor struct {
	// Source names who published the excerpt and Reference is their own
	// identifier for the track. Seconds is how much audio the excerpt held.
	Source    string `json:"source,omitempty"`
	Reference string `json:"reference,omitempty"`
	Seconds   int    `json:"seconds,omitempty"`
	// Label and Views describe an excerpt found by searching for the want's name
	// rather than fetched by a key: the upload's own title and channel, and how
	// many views it had when it was chosen. Both are empty for a keyed excerpt.
	// They are here because such a reference can be another version of the song,
	// and the person reading the row is the one who can tell
	// (docs/decisions/0029 §4).
	Label string `json:"label,omitempty"`
	Views int64  `json:"views,omitempty"`
	// Rate is the measured bit error rate, and Measured says the comparison ran.
	// Zero is a real answer — a perfect one — so the flag is what tells a reader
	// apart from a comparison that never happened.
	Rate                     float64                 `json:"rate"`
	Measured                 bool                    `json:"measured"`
	Comparison               *chromaprint.Comparison `json:"comparison,omitempty"`
	ReferenceCoverageSeconds int                     `json:"referenceCoverageSeconds,omitempty"`
	CandidateCoverageSeconds int                     `json:"candidateCoverageSeconds,omitempty"`
	// Agrees is set only when the comparison ran and reached one of the two
	// answers. Nil means nothing was concluded, which includes a rate between the
	// thresholds: that is a question for a person and never a weaker yes.
	Agrees *bool `json:"agrees,omitempty"`
	// Unavailable records why no comparison was made, when there was an anchor to
	// make one against and something did not work. A check that silently did
	// nothing would be worse than an absent one.
	Unavailable string `json:"unavailable,omitempty"`
}

// ImportWitness is one copy measured against a file the library already holds.
//
// The library file is one whose identity is settled: a person decided it, or
// AcoustID, an ISRC or a MusicBrainz identifier proved it. A copy that holds the
// same music as that file is the recording that file was proven to be, and what
// proved the older file is what proves this copy — so the older file and its
// proof are both kept here, and both are shown to whoever reads the question.
//
// The comparison keeps its global rate and local-window evidence, read from the
// start of both files, so a shared prefix cannot hide an unverified ending.
type ImportWitness struct {
	// FileID is the owned file, Path is where it is, and Proof is what settled
	// its identity. The path is kept rather than looked up later, because the
	// file may be moved or removed and the question would then name nothing.
	FileID string `json:"fileId,omitempty"`
	Path   string `json:"path,omitempty"`
	Proof  string `json:"proof,omitempty"`
	// Rate is the measured bit error rate, and Measured says a comparison ran.
	// Zero is a real answer — a perfect one — so the flag is what tells a reader
	// apart from a comparison that never happened.
	Rate                     float64                 `json:"rate"`
	Measured                 bool                    `json:"measured"`
	Comparison               *chromaprint.Comparison `json:"comparison,omitempty"`
	ReferenceCoverageSeconds int                     `json:"referenceCoverageSeconds,omitempty"`
	CandidateCoverageSeconds int                     `json:"candidateCoverageSeconds,omitempty"`
	// Agrees is set only where the copy holds the same music. It is never set to
	// false: a copy far from an owned file is a different master of the same
	// recording as often as it is other music, so this witness admits and never
	// refuses.
	Agrees *bool `json:"agrees,omitempty"`
	// FileDurationMS and CandidateDurationMS are the two decoded lengths the
	// admission is checked against, neither ever a tag: the settled file's own
	// length and the copy's, as fpcalc measured them. The comparison above is
	// read only over the span the two fingerprints share, so a low rate proves
	// nothing by itself about a copy many times the settled file's length —
	// they are kept so a reader, and the grader, can tell a true copy from an
	// hour-long mix that happens to open with the wanted recording.
	FileDurationMS      int `json:"fileDurationMs,omitempty"`
	CandidateDurationMS int `json:"candidateDurationMs,omitempty"`
	// Considered is how many settled files the library offered for this
	// recording, so a reader can tell a library with nothing to say from one that
	// was asked and did not recognise the copy.
	Considered int `json:"considered,omitempty"`
	// Unavailable records why no comparison was made when the library did hold a
	// settled file to make one against. A check that silently did nothing would
	// be worse than an absent one.
	Unavailable string `json:"unavailable,omitempty"`
}

type ImportTags struct {
	TrackID                string `json:"trackId,omitempty"`
	Artist                 string `json:"artist,omitempty"`
	Album                  string `json:"album,omitempty"`
	Title                  string `json:"title,omitempty"`
	DiscNumber             int    `json:"discNumber,omitempty"`
	TrackNumber            int    `json:"trackNumber,omitempty"`
	DurationMS             int    `json:"durationMs,omitempty"`
	MusicBrainzRecordingID string `json:"musicbrainzRecordingId,omitempty"`
	ISRC                   string `json:"isrc,omitempty"`
}

// encodeEvidence encodes evidence for a jsonb column, with NUL removed.
//
// Evidence is largely what a stranger's file said about itself, and a tag is
// arbitrary bytes: two copies arrived carrying a NUL inside a tag value, Go
// encoded it as the escape \u0000, and jsonb refused the whole document
// (SQLSTATE 22P05 — the one Unicode escape jsonb has no way to store). The write
// that failed was the verdict refusing those copies, so the refusal could never
// be recorded and the loop fetched the same two files again on every wave.
//
// A byte no text type can hold is not evidence about the music, so it is dropped
// and everything else is kept. The strip happens on the decoded strings rather
// than on the encoded bytes, because a tag whose text is literally "\u0000"
// encodes to an escaped backslash and cutting that as if it were the escape
// would leave the document malformed. Nothing is decoded at all unless the escape
// is present, so the ordinary write is one Marshal exactly as before.
func encodeEvidence(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if !bytes.Contains(encoded, []byte(`\u0000`)) {
		return encoded, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	// Numbers stay as they were written. Decoded into interface{} they would
	// become float64 and come back as exponents, which would quietly rewrite
	// every size and duration in the document being repaired.
	decoder.UseNumber()
	var tree any
	if err := decoder.Decode(&tree); err != nil {
		return nil, err
	}
	return json.Marshal(withoutNUL(tree))
}

// withoutNUL copies a decoded JSON value with NUL taken out of every string in
// it, keys included.
func withoutNUL(value any) any {
	switch typed := value.(type) {
	case string:
		return strings.ReplaceAll(typed, "\x00", "")
	case []any:
		for index, item := range typed {
			typed[index] = withoutNUL(item)
		}
		return typed
	case map[string]any:
		cleaned := make(map[string]any, len(typed))
		for key, item := range typed {
			cleaned[strings.ReplaceAll(key, "\x00", "")] = withoutNUL(item)
		}
		return cleaned
	}
	return value
}

// Fingerprint identifies what a file was observed to say. A recorded decision
// carries the fingerprint it was made against, so the judgement applies only
// while the file still says the same thing: remembering a decision must never
// become a way to admit audio nobody looked at.
func (tags ImportTags) Fingerprint() string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		tags.Artist, tags.Album, tags.Title,
		strconv.Itoa(tags.DiscNumber), strconv.Itoa(tags.TrackNumber),
		strconv.Itoa(tags.DurationMS), tags.MusicBrainzRecordingID, tags.ISRC,
	}, "\x1f")))
	return hex.EncodeToString(sum[:])
}

// ImportDecision is a stored judgement: this downloaded file is this catalogue
// track, decided by a person against evidence Schall could not resolve.
type ImportDecision struct {
	ID          uuid.UUID `json:"id"`
	FileName    string    `json:"fileName"`
	TrackID     uuid.UUID `json:"trackId"`
	TrackTitle  string    `json:"trackTitle"`
	Fingerprint string    `json:"-"`
	Note        string    `json:"note,omitempty"`
	DecidedAt   time.Time `json:"decidedAt"`
}

type RecordImportDecisionParams struct {
	RequestID   uuid.UUID
	FileName    string
	TrackID     uuid.UUID
	Fingerprint string
	Note        string
}

// RecordImportDecision stores one per-track judgement about a paused import.
// It does not import anything and does not re-run validation: the decision is
// new evidence, and validation still decides what it means. ErrNoRows means no
// paused import with that ID exists.
func (q *Queries) RecordImportDecision(
	ctx context.Context, params RecordImportDecisionParams,
) (DownloadRequestRow, error) {
	err := q.inTransaction(ctx, "resolving an import track", func(tx pgx.Tx) error {
		var albumID uuid.NullUUID
		if err := tx.QueryRow(ctx, `
			SELECT album_id FROM download_requests
			WHERE id = $1 AND status = 'completed' AND import_status = 'needs_review'
		`, params.RequestID).Scan(&albumID); err != nil {
			return err
		}
		// A file can only be resolved to a track of the release it was
		// requested for. Provenance is the whole point of the record, and a
		// request that was never about a release has no track to resolve to.
		if !albumID.Valid {
			return ErrTrackNotInRelease
		}
		var belongs bool
		if err := tx.QueryRow(ctx, `
			SELECT exists(SELECT 1 FROM tracks WHERE id = $1 AND album_id = $2)
		`, params.TrackID, albumID.UUID).Scan(&belongs); err != nil {
			return err
		}
		if !belongs {
			return ErrTrackNotInRelease
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO download_import_decisions (
				download_request_id, file_name, track_id, observed_fingerprint, note
			)
			VALUES ($1, $2, $3, $4, nullif($5, ''))
			ON CONFLICT (download_request_id, file_name) DO UPDATE
			SET track_id = excluded.track_id,
			    observed_fingerprint = excluded.observed_fingerprint,
			    note = excluded.note, decided_at = now()
		`, params.RequestID, params.FileName, params.TrackID,
			params.Fingerprint, params.Note); err != nil {
			return err
		}
		return recordImportReview(ctx, tx, params.RequestID, "resolved",
			fmt.Sprintf("%s resolved by hand", params.FileName), nil)
	})
	if err != nil {
		return DownloadRequestRow{}, err
	}
	return q.DownloadRequest(ctx, params.RequestID)
}

// WithdrawImportDecision removes a judgement again. The history keeps both the
// decision and its withdrawal.
func (q *Queries) WithdrawImportDecision(
	ctx context.Context, requestID, decisionID uuid.UUID,
) (DownloadRequestRow, error) {
	err := q.inTransaction(ctx, "withdrawing an import decision", func(tx pgx.Tx) error {
		var fileName string
		if err := tx.QueryRow(ctx, `
			DELETE FROM download_import_decisions
			WHERE id = $1 AND download_request_id = $2
			RETURNING file_name
		`, decisionID, requestID).Scan(&fileName); err != nil {
			return err
		}
		return recordImportReview(ctx, tx, requestID, "withdrawn",
			fmt.Sprintf("%s left unresolved again", fileName), nil)
	})
	if err != nil {
		return DownloadRequestRow{}, err
	}
	return q.DownloadRequest(ctx, requestID)
}

// ErrTrackNotInRelease reports a decision naming a track from another release.
var ErrTrackNotInRelease = errors.New("the chosen track does not belong to this release")

// ErrImportAlreadyDecided reports a download whose import is no longer waiting
// to be done. The import job claims the request before it does anything, and
// the claim only takes a request whose transfer finished and whose import is
// still pending or running. Once the import has been paused for review,
// finished, or set aside, there is nothing left for the job to take.
var ErrImportAlreadyDecided = errors.New("this download's import has already been decided")

// ErrDownloadRequestGone reports a download request that is not there at all.
// A job carries only the identifier, and the request it names can be deleted
// between the job being queued and the job being run.
var ErrDownloadRequestGone = errors.New("this download request no longer exists")

// ErrImportNotReadyYet reports a request whose import has not been decided about
// and cannot be started either: the transfer is still moving, or it stopped
// without finishing. This is the answer that must not be read as settled. The
// transfer is watched by a poller that writes what it finds, so a request can
// become claimable a second later without anybody touching it, and the job that
// waits and asks again is the one that imports it.
var ErrImportNotReadyYet = errors.New("this download is not ready to be imported")

// decidedImportStates are the states an import does not leave on its own. An
// import that finished, one that was paused for review, and one whose copy was
// refused are all somebody's answer or the system's; each of them is changed
// only by a person acting, and the action queues a job of its own. Nothing here
// is waiting for the job that found it.
var decidedImportStates = map[string]bool{
	"imported": true, "needs_review": true, "discarded": true,
}

// whyImportCouldNotBeClaimed reads the request the claim did not take and says
// what stopped it. The claim itself can only report that it changed no row,
// which is the same empty answer for three different situations: a request that
// is gone, one that has been decided about, and one that is simply not ready. A
// job that repeats a raw "no rows in result set" six times tells the person
// nothing about any of them.
//
// The three are told apart because only the first two are final. Reading a
// request that is still on its way as "already decided" would finish the job and
// leave the import undone for good, which is worse than the failed job this is
// replacing — so anything that is not one of the two final answers keeps its
// attempts and is asked about again.
func (q *Queries) whyImportCouldNotBeClaimed(ctx context.Context, requestID uuid.UUID) error {
	var status, importStatus string
	err := q.db.QueryRow(ctx, `
		SELECT status, import_status FROM download_requests WHERE id = $1
	`, requestID).Scan(&status, &importStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: %s", ErrDownloadRequestGone, requestID)
	}
	if err != nil {
		return fmt.Errorf("read why %s could not be claimed: %w", requestID, err)
	}
	if decidedImportStates[importStatus] {
		return fmt.Errorf("%w: the transfer is %s and the import is %s",
			ErrImportAlreadyDecided, status, importStatus)
	}
	return fmt.Errorf("%w: the transfer is %s and the import is %s",
		ErrImportNotReadyYet, status, importStatus)
}

// ClaimDownloadImport moves one completed transfer into validation. A running
// import is accepted as well so an interrupted job can safely resume.
func (q *Queries) ClaimDownloadImport(ctx context.Context, requestID uuid.UUID) (DownloadImportRow, error) {
	var result DownloadImportRow
	var files []byte
	err := q.db.QueryRow(ctx, `
		UPDATE download_requests
		SET import_status = 'validating', import_error = NULL, updated_at = now()
		WHERE id = $1
		  AND status = 'completed'
		  AND import_status IN ('pending', 'validating')
		RETURNING id, album_id, source_directory, files
	`, requestID).Scan(&result.RequestID, &result.AlbumID, &result.SourceDirectory, &files)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, q.whyImportCouldNotBeClaimed(ctx, requestID)
	}
	if err != nil {
		return result, err
	}
	if err := json.Unmarshal(files, &result.Files); err != nil {
		return result, fmt.Errorf("decode requested files: %w", err)
	}
	if err := q.db.QueryRow(ctx, `
		SELECT artists.name, albums.title, albums.musicbrainz_release_group_id,
		       release_editions.musicbrainz_release_id,
		       coalesce(release_editions.release_date, ''),
		       -- The one genre written into these files: the release group's
		       -- leading community vote, or the artist's where the release
		       -- group has none. Empty where neither has been voted for.
		       coalesce(albums.genres[1], artists.genres[1], '')
		FROM albums
		JOIN artists ON artists.id = albums.artist_id
		LEFT JOIN release_editions ON release_editions.album_id = albums.id
		WHERE albums.id = $1
	`, result.AlbumID).Scan(
		&result.ArtistName, &result.AlbumTitle, &result.ReleaseGroupID,
		&result.ReleaseID, &result.ReleaseDate, &result.Genre,
	); err != nil {
		return result, err
	}
	rows, err := q.db.Query(ctx, `
		SELECT id, title, disc_number, track_number, coalesce(duration_ms, 0),
		       musicbrainz_recording_id, coalesce(isrc, '')
		FROM tracks
		WHERE album_id = $1 AND track_number IS NOT NULL
		ORDER BY disc_number, track_number, id
	`, result.AlbumID)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var track ImportTrack
		if err := rows.Scan(
			&track.ID, &track.Title, &track.DiscNumber, &track.TrackNumber,
			&track.DurationMS, &track.MusicBrainzRecordingID, &track.ISRC,
		); err != nil {
			return result, err
		}
		result.Tracks = append(result.Tracks, track)
	}
	if err := rows.Err(); err != nil {
		return result, err
	}
	result.AlbumCredits, err = q.creditsOf(ctx, albumCreditsQuery, result.AlbumID)
	if err != nil {
		return result, fmt.Errorf("read the credit on album %s: %w", result.AlbumID, err)
	}
	credited, err := q.trackCredits(ctx, result.AlbumID)
	if err != nil {
		return result, fmt.Errorf("read the credits on the tracks of album %s: %w", result.AlbumID, err)
	}
	for index, track := range result.Tracks {
		result.Tracks[index].Credits = credited[track.ID]
	}
	result.Decisions, err = q.importDecisions(ctx, requestID)
	return result, err
}

// The credit an entity is held under, in the order it is held in. The order is
// the whole of it: each row carries its own artist, so nothing drifts onto the
// wrong name, but rejoining the names has to reproduce the credit the release
// printed, and a list read back out of order names it in an order nobody
// published.
const (
	albumCreditsQuery = `
		SELECT credited_name, musicbrainz_artist_id
		FROM album_artist_credits
		WHERE album_id = $1
		ORDER BY position
	`
	trackCreditQuery = `
		SELECT credited_name, musicbrainz_artist_id
		FROM track_artist_credits
		WHERE track_id = $1
		ORDER BY position
	`
)

// creditsOf reads one entity's credit. Nothing held for it is an empty list,
// which is silence: the joined string the catalogue holds is written on its own
// rather than a list cut out of it.
func (q *Queries) creditsOf(ctx context.Context, query string, id uuid.UUID) ([]ArtistCredit, error) {
	rows, err := q.db.Query(ctx, query, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var credits []ArtistCredit
	for rows.Next() {
		var (
			credit ArtistCredit
			artist uuid.NullUUID
		)
		if err := rows.Scan(&credit.Name, &artist); err != nil {
			return nil, err
		}
		// A name MusicBrainz has no artist for keeps its place with no
		// identifier, which is what a zero identifier says further on.
		credit.ArtistID = artist.UUID
		credits = append(credits, credit)
	}
	return credits, rows.Err()
}

// trackCredits reads the credit on every track of one album, by track. A whole
// release is imported at once and every one of its files is written from the
// same catalogue, so its credits are read the same way rather than a query at a
// time down the track listing.
func (q *Queries) trackCredits(
	ctx context.Context, albumID uuid.UUID,
) (map[uuid.UUID][]ArtistCredit, error) {
	rows, err := q.db.Query(ctx, `
		SELECT credits.track_id, credits.credited_name, credits.musicbrainz_artist_id
		FROM track_artist_credits credits
		JOIN tracks ON tracks.id = credits.track_id
		WHERE tracks.album_id = $1
		ORDER BY credits.track_id, credits.position
	`, albumID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	credited := make(map[uuid.UUID][]ArtistCredit)
	for rows.Next() {
		var (
			trackID uuid.UUID
			credit  ArtistCredit
			artist  uuid.NullUUID
		)
		if err := rows.Scan(&trackID, &credit.Name, &artist); err != nil {
			return nil, err
		}
		credit.ArtistID = artist.UUID
		credited[trackID] = append(credited[trackID], credit)
	}
	return credited, rows.Err()
}

func (q *Queries) importDecisions(
	ctx context.Context, requestID uuid.UUID,
) (map[string]ImportDecision, error) {
	rows, err := q.db.Query(ctx, `
		SELECT decisions.id, decisions.file_name, decisions.track_id, tracks.title,
		       decisions.observed_fingerprint, coalesce(decisions.note, ''),
		       decisions.decided_at
		FROM download_import_decisions decisions
		JOIN tracks ON tracks.id = decisions.track_id
		WHERE decisions.download_request_id = $1
	`, requestID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	decisions := make(map[string]ImportDecision)
	for rows.Next() {
		var decision ImportDecision
		if err := rows.Scan(
			&decision.ID, &decision.FileName, &decision.TrackID, &decision.TrackTitle,
			&decision.Fingerprint, &decision.Note, &decision.DecidedAt,
		); err != nil {
			return nil, err
		}
		decisions[decision.FileName] = decision
	}
	return decisions, rows.Err()
}

// ImportAttempts is how many times an import is tried before it stops and says
// so.
//
// What validation decides is never retried — a file that does not belong to the
// release is a decision, and it pauses the import on the first pass. So every
// attempt counted here is something outside the import that did not work: the
// download folder, the database, the metadata provider. Those recover on their
// own within a minute or two, and the delay between attempts doubles from five
// seconds, so this many of them is about five minutes of patience. Three was
// half a minute, which turned an ordinary restart into something a person had to
// come and click.
const ImportAttempts = 6

// MarkDownloadImportNeedsReview pauses an import and records why. The reason is
// written to the audit history as well as the request, so a later attempt
// cannot erase what an earlier one found. Evidence may be nil when the import
// failed before any file could be compared.
func (q *Queries) MarkDownloadImportNeedsReview(
	ctx context.Context, requestID uuid.UUID, detail string, evidence *ImportEvidence,
) error {
	return q.inTransaction(ctx, "pausing an import", func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE download_requests
			SET import_status = 'needs_review', import_error = $2, updated_at = now()
			WHERE id = $1 AND import_status = 'validating'
		`, requestID, detail)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("validating download import was not found")
		}
		return recordImportReview(ctx, tx, requestID, "paused", detail, evidence)
	})
}

// RevalidateDownloadImport sends a paused import back through validation after
// the tags or the representative edition changed. It re-runs the same checks:
// nothing here accepts a file that validation rejected, it only asks again.
// pgx.ErrNoRows means no paused import with that ID exists.
func (q *Queries) RevalidateDownloadImport(ctx context.Context, requestID uuid.UUID) (DownloadRequestRow, error) {
	err := q.inTransaction(ctx, "retrying an import", func(tx pgx.Tx) error {
		var id uuid.UUID
		if err := tx.QueryRow(ctx, `
			UPDATE download_requests
			SET import_status = 'pending', import_error = NULL, updated_at = now()
			WHERE id = $1 AND status = 'completed' AND import_status = 'needs_review'
			RETURNING id
		`, requestID).Scan(&id); err != nil {
			return err
		}
		if err := recordImportReview(ctx, tx, requestID, "revalidated", "", nil); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO jobs (kind, payload, max_attempts)
			VALUES ('import_download', jsonb_build_object('requestId', $1::text), $2)
			ON CONFLICT ((payload->>'requestId'))
			    WHERE kind = 'import_download' AND status IN ('queued', 'running')
			DO NOTHING
		`, requestID, ImportAttempts)
		return err
	})
	if err != nil {
		return DownloadRequestRow{}, err
	}
	return q.DownloadRequest(ctx, requestID)
}

func recordImportReview(
	ctx context.Context, tx pgx.Tx, requestID uuid.UUID, kind, detail string, evidence *ImportEvidence,
) error {
	var encoded []byte
	if evidence != nil {
		var err error
		if encoded, err = encodeEvidence(evidence); err != nil {
			return fmt.Errorf("encode import evidence: %w", err)
		}
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO download_import_reviews (download_request_id, kind, detail, evidence)
		VALUES ($1, $2, nullif($3, ''), $4)
	`, requestID, kind, detail, encoded)
	return err
}

func (q *Queries) inTransaction(ctx context.Context, what string, run func(pgx.Tx) error) error {
	connection, ok := q.db.(beginner)
	if !ok {
		return errors.New(what + " requires a transactional connection")
	}
	tx, err := connection.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := run(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// CompleteDownloadImport records every source-to-library path atomically,
// ensures the managed library root exists, and queues a scan. The files are
// already present when this runs; repeating it after a lost response is safe.
func (q *Queries) CompleteDownloadImport(
	ctx context.Context,
	requestID uuid.UUID,
	rootPath string,
	importPath string,
	localPaths map[string]ImportedDownloadFile,
) error {
	return q.inTransaction(ctx, "completing an import", func(tx pgx.Tx) error {
		return completeDownloadImport(ctx, tx, requestID, rootPath, importPath, localPaths)
	})
}

// completeDownloadImport is the import itself, so that a whole release and a
// single fetched candidate commit it the same way. A candidate additionally
// records the verdict that admitted it, in the same transaction.
func completeDownloadImport(
	ctx context.Context,
	tx pgx.Tx,
	requestID uuid.UUID,
	rootPath string,
	importPath string,
	localPaths map[string]ImportedDownloadFile,
) error {
	for remotePath, imported := range localPaths {
		// A file fetched for a want has no catalogue track: what it is was proven
		// against a MusicBrainz recording, and the track it maps to is decided by
		// matching once the release is in the catalogue.
		var track any
		if imported.TrackID != uuid.Nil {
			track = imported.TrackID
		}
		tag, err := tx.Exec(ctx, `
			UPDATE downloads SET local_path = $3, track_id = $4, updated_at = now()
			WHERE download_request_id = $1 AND remote_path = $2
		`, requestID, remotePath, imported.LocalPath, track)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("download row for %q was not found", remotePath)
		}
	}
	tag, err := tx.Exec(ctx, `
		UPDATE download_requests
		SET import_status = 'imported', import_error = NULL, import_path = $2,
		    imported_at = now(), updated_at = now()
		WHERE id = $1 AND import_status = 'validating'
	`, requestID, importPath)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("validating download import was not found")
	}
	if err := recordImportReview(ctx, tx, requestID, "imported", importPath, nil); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO library_roots (path) VALUES ($1)
		ON CONFLICT (path) DO UPDATE SET enabled = true, updated_at = now()
	`, rootPath); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO jobs (kind, payload, max_attempts)
		VALUES ('scan_library', '{}'::jsonb, 1)
		ON CONFLICT (kind)
		    WHERE kind = 'scan_library' AND status IN ('queued', 'running')
		DO NOTHING
	`); err != nil {
		return err
	}
	return nil
}

// RecordSourceRetention appends what became of the provider's copy to the
// import history. It is deliberately a separate statement from the import
// itself: removal happens after the import is committed, so it can only ever
// be reported as a later entry, never as part of the transaction that made the
// managed copy authoritative.
func (q *Queries) RecordSourceRetention(
	ctx context.Context, requestID uuid.UUID, removed bool, detail string,
) error {
	kind := "source_retained"
	if removed {
		kind = "source_removed"
	}
	_, err := q.db.Exec(ctx, `
		INSERT INTO download_import_reviews (download_request_id, kind, detail)
		VALUES ($1, $2, nullif($3, ''))
	`, requestID, kind, detail)
	return err
}

func (q *Queries) QueueDownloadImport(ctx context.Context, requestID uuid.UUID) error {
	_, err := q.db.Exec(ctx, `
		INSERT INTO jobs (kind, payload, max_attempts)
		SELECT 'import_download', jsonb_build_object('requestId', id::text), $2
		FROM download_requests
		WHERE id = $1 AND status = 'completed' AND import_status = 'pending'
		ON CONFLICT ((payload->>'requestId'))
		    WHERE kind = 'import_download' AND status IN ('queued', 'running')
		DO NOTHING
	`, requestID, ImportAttempts)
	return err
}

func (q *Queries) QueuePendingDownloadImports(ctx context.Context) error {
	_, err := q.db.Exec(ctx, `
		INSERT INTO jobs (kind, payload, max_attempts)
		SELECT 'import_download', jsonb_build_object('requestId', id::text), $1
		FROM download_requests
		WHERE status = 'completed' AND import_status = 'pending'
		ON CONFLICT ((payload->>'requestId'))
		    WHERE kind = 'import_download' AND status IN ('queued', 'running')
		DO NOTHING
	`, ImportAttempts)
	return err
}
