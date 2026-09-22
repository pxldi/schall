package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// The verdicts one offer for one want can reach. Two of them are judgements
// about the music and are final; the rest are not, and a want may come back to
// the same offer another day.
const (
	// AcquiredFileFetching is an offer a transfer is in flight for.
	AcquiredFileFetching = "fetching"
	// AcquiredFileUndelivered is bytes that never arrived, or arrived as
	// something other than what was offered. A fact about the transfer.
	AcquiredFileUndelivered = "undelivered"
	// AcquiredFileAccepted is proven to be the wanted recording, and imported.
	AcquiredFileAccepted = "accepted"
	// AcquiredFileDiscardedAudio is audio that was identified and is not this
	// recording. Final.
	AcquiredFileDiscardedAudio = "discarded_audio"
	// AcquiredFileDiscardedTags is a file whose own identifiers or length
	// contradict the recording. Final.
	AcquiredFileDiscardedTags = "discarded_tags"
	// AcquiredFileHeld is a copy nothing could decide about, kept as a question
	// rather than accepted or thrown away.
	AcquiredFileHeld = "held"
)

// AcquiredFileEvidence is everything one candidate file came to against the
// recording a want is waiting for.
//
// It is deliberately not the release-shaped import evidence: there is no
// edition, no position, and no track list here — one file, one recording, and
// what the two had to say to each other. It has to stay readable once the peer's
// copy is gone, so the recording it was checked against is copied in rather than
// looked up again.
type AcquiredFileEvidence struct {
	Name string `json:"name"`
	// Observed is the file's own account of itself, read from its tags.
	Observed *ImportTags `json:"observed,omitempty"`
	// Wanted is the recording it was checked against, as MusicBrainz has it.
	Wanted *ImportTags `json:"wanted,omitempty"`
	// Acoustic is what the audio was identified as. It is the only evidence here
	// the file could not have written itself.
	Acoustic *ImportAcoustic `json:"acoustic,omitempty"`
	// Anchor is what this copy sounds like beside the excerpt the distributor
	// publishes for the want's own ISRC. It is the second witness that needs no
	// catalogue entry, and like Acoustic it is computed from the sound rather
	// than read off the file.
	Anchor *ImportAnchor `json:"anchor,omitempty"`
	// Witness is what this copy sounds like beside a file the library already
	// holds and has already proven. It is the third witness computed from the
	// sound, and the only one that needs nobody outside the machine.
	Witness *ImportWitness `json:"witness,omitempty"`
	Agrees  []string       `json:"agrees"`
	Differs []string       `json:"differs"`
	// Method names what proved it and is empty when nothing did; Confidence is
	// what that method is worth. Both are taken from the grader rather than
	// decided here, so the identity the library file is given later says exactly
	// what verification concluded.
	Method     string  `json:"method,omitempty"`
	Confidence float32 `json:"confidence,omitempty"`
	// Problems are facts about the bytes: absent, truncated, unreadable. They are
	// never a judgement call.
	Problems  []string `json:"problems"`
	SizeBytes int64    `json:"sizeBytes,omitempty"`
	BitRate   int      `json:"bitRate,omitempty"`
	// Audio is what this copy's own sound turned out to be: where the music in
	// it stops, and what the file says made it. It is quality and never
	// identity — a transcode of the wanted recording is still the wanted
	// recording — so it is read by nobody who admits or refuses a copy, and it
	// is here for the person choosing between two of them.
	Audio *ImportAudioQuality `json:"audio,omitempty"`
	// AudioFingerprint is what this copy's audio sounds like, as about eight
	// numbers a second computed from the decoded sound — the same form
	// AcoustID uses and the same form a want's published sample is kept in.
	// AudioFingerprintSeconds is how much of the file was read for it, because
	// a fingerprint says nothing without it: a copy read to two minutes and one
	// read whole are different measurements of the same file.
	//
	// The two live in columns of their own rather than in this document, which
	// is why they are not written to JSON. They travel here because this is the
	// bag one candidate's findings are carried in, and a second bag beside it
	// would be two places to forget.
	//
	// Empty means nothing was measured — no fpcalc, no readable audio, or the
	// bytes were gone. That is silence and never a verdict: nothing is admitted
	// or refused by what is written here.
	AudioFingerprint        string `json:"-"`
	AudioFingerprintSeconds int    `json:"-"`
	// Source is the address a copy for a want with no recording was admitted or
	// accepted as: the want's anchor, written as a source identity key
	// (ADR 0037 §4). It is set only on that path, and it is what completing the
	// copy reads to write a source identity instead of a recording.
	Source *ImportSource `json:"source,omitempty"`
}

// ImportSource is a source identity key: the service, its own identifier for
// the track, and the page the track is published at.
type ImportSource struct {
	Source      string `json:"source"`
	ExternalID  string `json:"externalId"`
	ExternalURL string `json:"externalUrl"`
}

// SourceOfAnchor is the source identity key an admitting anchor names
// (ADR 0037 §4). A Deezer anchor's reference is the Deezer track id, and a
// Topic upload's is the YouTube video id. Any other anchor names no key, which
// is why a name-found upload's copy cannot be written a source identity.
func SourceOfAnchor(anchorSource, reference string) (ImportSource, bool) {
	reference = strings.TrimSpace(reference)
	if reference == "" {
		return ImportSource{}, false
	}
	switch anchorSource {
	case "deezer":
		return ImportSource{
			Source: "deezer", ExternalID: "deezer:" + reference,
			ExternalURL: "https://www.deezer.com/track/" + reference,
		}, true
	case "youtube-topic":
		return ImportSource{
			Source: "youtube", ExternalID: "youtube:" + reference,
			ExternalURL: "https://www.youtube.com/watch?v=" + reference,
		}, true
	}
	return ImportSource{}, false
}

// ImportAudioQuality is what one look at a copy's audio found. Absent means
// nothing was measured, which is never agreement that the copy is fine.
type ImportAudioQuality struct {
	// CutoffHz is the frequency the music in the file stops at.
	CutoffHz int `json:"cutoffHz,omitempty"`
	// Encoder is what the file says made it, and is what the file claims rather
	// than anything that was checked.
	Encoder string `json:"encoder,omitempty"`
	// TranscodeSuspected says the audio stops where a lossy encoder would have
	// stopped it, in a file whose container or bit rate says it should not.
	TranscodeSuspected bool `json:"transcodeSuspected,omitempty"`
}

// AcquisitionTargetFileRow is one offer for one want, and what it came to.
type AcquisitionTargetFileRow struct {
	ID                  uuid.UUID
	AcquisitionTargetID uuid.UUID
	Provider            string
	SourceUsername      string
	RemotePath          string
	FileName            string
	SizeBytes           pgtype.Int8
	Verdict             string
	DecidedBy           string
	Summary             string
	Evidence            *AcquiredFileEvidence
	DownloadRequestID   uuid.NullUUID
	LibraryFileID       uuid.NullUUID
	DecidedAt           time.Time
	// Waveform is the shape of this copy's audio, one byte a bar. Empty means
	// nobody has read it, which is never a statement that the file is silent.
	Waveform []byte
}

// TargetImportRow is one fetched candidate waiting to be checked against the
// recording its want names. There is exactly one file: a want is pursued one
// candidate at a time.
type TargetImportRow struct {
	RequestID                 uuid.UUID
	AcquisitionTargetID       uuid.UUID
	MusicBrainzRecordingID    uuid.UUID
	MusicBrainzReleaseGroupID uuid.NullUUID
	EntryArtist               string
	EntryTitle                string
	// EntryAlbum is the album the entry named, in the words the playlist used.
	// It is the entry's own account of where the track comes from, no better
	// and no worse than the artist and the title beside it.
	EntryAlbum string
	// EntryISRC and EntryDurationMS are the rest of the entry. A copy for a
	// want with no recording is checked against the entry itself, and these can
	// contradict it (ADR 0037 §3). Empty and zero are silence.
	EntryISRC       string
	EntryDurationMS int
	// AlbumTitle, ReleaseID and ReleaseDate are the release the want was
	// resolved through, as the catalogue holds it: the album for that release
	// group, and the edition chosen for it. They are the same answer a whole
	// release gives its files, so a track fetched one at a time lands in the
	// user's music server under the album the rest of it is under. A release
	// group nothing in the catalogue answers for has none of them, and a
	// catalogued album whose edition has not been chosen yet has the title
	// without the identifier or the date.
	AlbumTitle  string
	ReleaseID   uuid.NullUUID
	ReleaseDate string
	// AlbumArtist is the one artist that album is filed under — the row in
	// artists the album points at, not the credit printed on this track. The
	// two differ on every guest appearance ("Ufo361" against "Ufo361 feat. KC
	// Rebell") and on stylised spellings ("RIN" against "Rin"), and a folder
	// named after the credit puts one album in several folders. It is the same
	// name a whole release's import files its folder under, so both paths file
	// one album in one place. An uncatalogued release group has none.
	AlbumArtist string
	// Genre is what the MusicBrainz community voted this release group is,
	// falling back to the artist's leading genre. It rides with the album the
	// want was resolved through, like the title and the date beside it, so a
	// track fetched one at a time browses under the same genre as the rest of
	// the album. Empty where the catalogue answers for neither.
	Genre string
	// DiscNumber and TrackNumber are where that album puts this recording: the
	// row in tracks for it, found by the recording identifier the want names. A
	// recording the catalogued album has no track for, or has two tracks for,
	// leaves both at zero, which is where the file keeps whatever numbering it
	// arrived with.
	DiscNumber  int
	TrackNumber int
	// TrackCredits is who the catalogue says that track is by, and AlbumCredits
	// who it says the album is by — the same two lists a whole release's import
	// writes into its own files, read off the same album and the same track the
	// numbers above came from. A credit is a credit on a release, so a release
	// the catalogue does not answer for has neither.
	TrackCredits    []ArtistCredit
	AlbumCredits    []ArtistCredit
	Provider        string
	SourceUsername  string
	SourceDirectory string
	File            DownloadRequestFile
	// Anchor is the audio the want was anchored to: the fingerprint of the
	// thirty-second excerpt the distributor publishes for the want's own ISRC,
	// fetched once when the want was created and kept on it. A copy a peer sent
	// is compared against it directly, without asking anybody who the audio is.
	//
	// Empty means the want has no anchor, which is silence and never a verdict:
	// most wants have none, and a copy is judged exactly as it was before.
	// See ADR 0024.
	AnchorFingerprint string
	// AnchorSource names who published the excerpt and AnchorReference is their
	// own identifier for the track. They describe where the proof came from,
	// long after the address it was fetched from has stopped answering.
	AnchorSource    string
	AnchorReference string
	// AnchorSeconds is how much audio the excerpt held.
	AnchorSeconds int
	// AnchorLabel and AnchorViews describe an excerpt found by searching for the
	// want's name: the upload's own title and channel, and how many views it had
	// when it was chosen. A keyed excerpt has neither. They are carried onto the
	// copy's evidence so a person reading the row can see which upload the copy
	// was measured against (ADR 0029 §4).
	AnchorLabel string
	AnchorViews int64
}

// ErrNotATargetImport reports a request that is not one file fetched for a want.
// A whole release chosen by hand is validated against its edition instead, and
// the two paths must never take each other's work.
var ErrNotATargetImport = errors.New("this request was not one file fetched for a wishlist entry")

// whyTargetImportCouldNotBeClaimed reads the request a want's claim did not
// take and says what stopped it. Four things can: the request is gone, it is a
// whole release somebody chose a folder for rather than one file fetched for a
// want, its import has already been decided about, or it is not ready yet. The
// claim itself can only report that it changed no row, which is the same empty
// answer for all four, and only the first three are final.
func (q *Queries) whyTargetImportCouldNotBeClaimed(ctx context.Context, requestID uuid.UUID) error {
	var servesWant bool
	err := q.db.QueryRow(ctx, `
		SELECT acquisition_target_id IS NOT NULL FROM download_requests WHERE id = $1
	`, requestID).Scan(&servesWant)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: %s", ErrDownloadRequestGone, requestID)
	}
	if err != nil {
		return fmt.Errorf("read why %s could not be claimed: %w", requestID, err)
	}
	if !servesWant {
		return ErrNotATargetImport
	}
	return q.whyImportCouldNotBeClaimed(ctx, requestID)
}

// ClaimTargetImport moves one fetched candidate into validation. A running
// import is accepted too, so an interrupted job can resume rather than leaving a
// file nobody will look at.
func (q *Queries) ClaimTargetImport(ctx context.Context, requestID uuid.UUID) (TargetImportRow, error) {
	var result TargetImportRow
	var (
		files  []byte
		target uuid.NullUUID
	)
	err := q.db.QueryRow(ctx, `
		UPDATE download_requests
		SET import_status = 'validating', import_error = NULL, updated_at = now()
		WHERE id = $1
		  AND status = 'completed'
		  AND import_status IN ('pending', 'validating')
		  AND acquisition_target_id IS NOT NULL
		RETURNING id, acquisition_target_id, provider, source_username,
		          source_directory, files
	`, requestID).Scan(
		&result.RequestID, &target, &result.Provider, &result.SourceUsername,
		&result.SourceDirectory, &files,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, q.whyTargetImportCouldNotBeClaimed(ctx, requestID)
	}
	if err != nil {
		return result, err
	}
	if !target.Valid {
		return result, ErrNotATargetImport
	}
	result.AcquisitionTargetID = target.UUID

	var requested []DownloadRequestFile
	if err := json.Unmarshal(files, &requested); err != nil {
		return result, fmt.Errorf("decode requested files: %w", err)
	}
	if len(requested) != 1 {
		return result, fmt.Errorf("%w: it covers %d files", ErrNotATargetImport, len(requested))
	}
	result.File = requested[0]

	// The release group is joined out to the album the catalogue holds for it,
	// and that album to the edition chosen for it — the same walk a whole
	// release's import makes, so both paths tell a file the same thing about
	// the release it came from. A want whose release group is not catalogued
	// simply finds nothing, which is the answer.
	//
	// The track is the next step of that walk: the row of that album the want's
	// recording identifier names, which is the same track a whole release's
	// import would have written this file's position from. The count guards a
	// recording an album carries twice — a reprise, a continuous mix cut two
	// ways — because two positions are not one position, and picking whichever
	// row came back first would be inventing the answer.
	var (
		recording        uuid.NullUUID
		album            uuid.NullUUID
		track            uuid.NullUUID
		searchedOnAnchor bool
	)
	if err := q.db.QueryRow(ctx, `
		SELECT targets.musicbrainz_recording_id, targets.musicbrainz_release_group_id,
		       targets.entry_artist, targets.entry_title, targets.entry_album,
		       coalesce(targets.entry_isrc, ''), coalesce(targets.entry_duration_ms, 0),
		       targets.status = 'unresolved' AND `+anchorAdmits("targets.")+`,
		       albums.id, coalesce(albums.title, ''),
		       coalesce(artists.name, ''),
		       release_editions.musicbrainz_release_id,
		       coalesce(release_editions.release_date, ''),
		       coalesce(track.disc_number, 0), coalesce(track.track_number, 0),
		       track.id,
		       coalesce(targets.anchor_fingerprint, ''),
		       coalesce(targets.anchor_source, ''),
		       coalesce(targets.anchor_reference, ''),
		       coalesce(targets.anchor_seconds, 0),
		       coalesce(targets.anchor_label, ''),
		       coalesce(targets.anchor_views, 0),
		       -- The one genre written into this file: the leading community
		       -- vote on the release group the want was resolved through, or
		       -- the artist's where that release group has none. Empty where
		       -- the catalogue answers for neither.
		       coalesce(albums.genres[1], artists.genres[1], '')
		FROM acquisition_targets AS targets
		LEFT JOIN albums
		       ON albums.musicbrainz_release_group_id = targets.musicbrainz_release_group_id
		LEFT JOIN artists ON artists.id = albums.artist_id
		LEFT JOIN release_editions ON release_editions.album_id = albums.id
		LEFT JOIN LATERAL (
			SELECT min(tracks.disc_number) AS disc_number,
			       min(tracks.track_number) AS track_number,
			       -- The row itself, which the credit is then read off. There
			       -- is no min() for an identifier and none is wanted: the
			       -- count below admits one row, and the first of one row is
			       -- that row rather than a choice between rows.
			       (array_agg(tracks.id))[1] AS id
			FROM tracks
			WHERE tracks.album_id = albums.id
			  AND tracks.musicbrainz_recording_id = targets.musicbrainz_recording_id
			HAVING count(*) = 1
		) AS track ON true
		WHERE targets.id = $1
	`, result.AcquisitionTargetID).Scan(
		&recording, &result.MusicBrainzReleaseGroupID,
		&result.EntryArtist, &result.EntryTitle, &result.EntryAlbum,
		&result.EntryISRC, &result.EntryDurationMS, &searchedOnAnchor,
		&album, &result.AlbumTitle, &result.AlbumArtist,
		&result.ReleaseID, &result.ReleaseDate,
		&result.DiscNumber, &result.TrackNumber, &track,
		&result.AnchorFingerprint, &result.AnchorSource,
		&result.AnchorReference, &result.AnchorSeconds,
		&result.AnchorLabel, &result.AnchorViews, &result.Genre,
	); err != nil {
		return result, err
	}
	// The credit comes off that same album and that same track, and off nothing
	// else. A recording is credited one way on the release it was written for
	// and another on the compilation that gathers it, so a list taken from any
	// track carrying this recording would describe a release the file is not on
	// — and a recording this album carries twice is the same refusal here as it
	// is for the numbers: no track, no credit.
	if album.Valid {
		result.AlbumCredits, err = q.creditsOf(ctx, albumCreditsQuery, album.UUID)
		if err != nil {
			return result, fmt.Errorf("read the credit on album %s: %w", album.UUID, err)
		}
	}
	if track.Valid {
		result.TrackCredits, err = q.creditsOf(ctx, trackCreditQuery, track.UUID)
		if err != nil {
			return result, fmt.Errorf("read the credit on track %s: %w", track.UUID, err)
		}
	}
	// A want with no recording is checked against its anchor alone, and only
	// where that anchor can admit a copy (ADR 0037). Any other want with no
	// recording has nothing to verify against: a resolution the review queue
	// replaces can clear one, and guessing what the file was supposed to be is
	// exactly what must not happen. MusicBrainzRecordingID stays uuid.Nil for
	// the first.
	if !recording.Valid {
		if searchedOnAnchor {
			return result, nil
		}
		return result, fmt.Errorf("%w: the want no longer names a recording", ErrNotATargetImport)
	}
	result.MusicBrainzRecordingID = recording.UUID
	return result, nil
}

// RecordAcquiredFileParams is one verdict about one offer.
type RecordAcquiredFileParams struct {
	AcquisitionTargetID uuid.UUID
	Provider            string
	SourceUsername      string
	RemotePath          string
	FileName            string
	SizeBytes           int64
	Verdict             string
	Summary             string
	Evidence            *AcquiredFileEvidence
	DownloadRequestID   uuid.NullUUID
	// Waveform is the shape of the copy's audio, for the card that draws it.
	// Empty is written as NULL: the column is asked whether anybody has read this
	// file's shape, and no bytes is not a picture of silence.
	Waveform []byte
}

// RecordAcquiredFile writes what one offer for one want came to.
//
// It reconciles rather than appending: "have we already tried this file for this
// want" has one answer. A verdict a person recorded is never overwritten by a
// later machine one — a decision somebody made stands, exactly as a manual
// mapping does.
func (q *Queries) RecordAcquiredFile(ctx context.Context, params RecordAcquiredFileParams) error {
	return recordAcquiredFile(ctx, q.db, params)
}

func recordAcquiredFile(ctx context.Context, runner DBTX, params RecordAcquiredFileParams) error {
	evidence := []byte("{}")
	if params.Evidence != nil {
		encoded, err := encodeEvidence(params.Evidence)
		if err != nil {
			return fmt.Errorf("encode acquired file evidence: %w", err)
		}
		evidence = encoded
	}
	var size any
	if params.SizeBytes > 0 {
		size = params.SizeBytes
	}
	// A copy nothing could fingerprint writes NULL rather than an empty string.
	// The column is asked "is there a measurement", and "" is a measurement of
	// nothing.
	var fingerprint, fingerprintSeconds any
	if params.Evidence != nil && params.Evidence.AudioFingerprint != "" &&
		params.Evidence.AudioFingerprintSeconds > 0 {
		fingerprint = params.Evidence.AudioFingerprint
		fingerprintSeconds = params.Evidence.AudioFingerprintSeconds
	}
	var shape any
	if len(params.Waveform) > 0 {
		shape = params.Waveform
	}
	_, err := runner.Exec(ctx, `
		INSERT INTO acquisition_target_files (
			acquisition_target_id, provider, source_username, remote_path,
			file_name, size_bytes, verdict, decided_by, summary, evidence,
			download_request_id, audio_fingerprint, audio_fingerprint_seconds,
			waveform
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 'schall', $8, $9::jsonb, $10, $11, $12, $13)
		ON CONFLICT (acquisition_target_id, provider, source_username, remote_path)
		DO UPDATE SET
			file_name = excluded.file_name,
			size_bytes = excluded.size_bytes,
			verdict = excluded.verdict,
			summary = excluded.summary,
			evidence = excluded.evidence,
			-- A pass that measured nothing does not erase what an earlier pass
			-- measured. The two columns move together, so a fingerprint is never
			-- left beside the length of a different read of the file.
			audio_fingerprint = coalesce(
				excluded.audio_fingerprint, acquisition_target_files.audio_fingerprint),
			audio_fingerprint_seconds = coalesce(
				excluded.audio_fingerprint_seconds,
				acquisition_target_files.audio_fingerprint_seconds),
			-- The same for the shape: a pass that drew nothing leaves the picture
			-- an earlier pass drew.
			waveform = coalesce(excluded.waveform, acquisition_target_files.waveform),
			download_request_id = coalesce(
				excluded.download_request_id, acquisition_target_files.download_request_id),
			decided_at = now(),
			updated_at = now()
		WHERE acquisition_target_files.decided_by = 'schall'
	`,
		params.AcquisitionTargetID, params.Provider, params.SourceUsername,
		params.RemotePath, params.FileName, size, params.Verdict, params.Summary,
		evidence, params.DownloadRequestID, fingerprint, fingerprintSeconds, shape,
	)
	if err != nil {
		return fmt.Errorf("record what %q came to: %w", params.FileName, err)
	}
	return nil
}

// SettleAcquiredFileParams settles one fetched candidate that will not be
// imported. NextAttemptAt puts the want back in the queue: a candidate that came
// to nothing is not the want coming to nothing, and the next offer is tried
// without waiting out the ladder, because the search already found something.
type SettleAcquiredFileParams struct {
	RecordAcquiredFileParams
	// ImportStatus is 'discarded' for a file that was refused and 'needs_review'
	// for one nothing could decide, which is the state a request waiting for a
	// person has always been in.
	ImportStatus  string
	ImportError   string
	Evidence      *AcquiredFileEvidence
	TargetSummary string
	NextAttemptAt time.Time
	// Outcome is the attempt row appended for the want: 'rejected' for a refusal,
	// 'inconclusive' for a question.
	Outcome string
}

// SettleAcquiredFile records a verdict about a fetched file, closes the request
// that fetched it, and puts the want back in the queue for its next candidate.
//
// It never counts an attempt. attempts is how many times Schall went looking,
// and working through what one search offered is one attempt however many files
// it holds — otherwise a folder of five wrong copies would spend a want's whole
// ladder in an afternoon and leave it waiting three days.
func (q *Queries) SettleAcquiredFile(ctx context.Context, params SettleAcquiredFileParams) error {
	return q.inTransaction(ctx, "settling a fetched candidate", func(tx pgx.Tx) error {
		record := params.RecordAcquiredFileParams
		record.Evidence = params.Evidence
		if err := recordAcquiredFile(ctx, tx, record); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `
			UPDATE download_requests
			SET import_status = $2, import_error = nullif($3, ''), updated_at = now()
			WHERE id = $1 AND import_status = 'validating'
		`, params.DownloadRequestID.UUID, params.ImportStatus, params.ImportError)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("validating candidate import was not found")
		}
		// The review history is where a request explains itself, and a candidate
		// that was refused has to be able to say why as much as one that paused.
		kind := "paused"
		if params.ImportStatus == "discarded" {
			kind = "discarded"
		}
		if err := recordImportReview(
			ctx, tx, params.DownloadRequestID.UUID, kind, params.ImportError, nil,
		); err != nil {
			return err
		}
		return requeueForNextCandidate(ctx, tx, params)
	})
}

// requeueForNextCandidate puts the want back in the queue and appends what this
// candidate came to. A target somebody decided something about while the
// candidate was being checked is left exactly as they left it.
//
// It asks for a sweep at the same time it gives back, in the same transaction —
// a refused copy names now, and a want due now that nothing wakes the sweeper
// for waits out whatever pass was already scheduled instead of the pass right
// after this one.
func requeueForNextCandidate(
	ctx context.Context, tx pgx.Tx, params SettleAcquiredFileParams,
) error {
	var attempts int32
	err := tx.QueryRow(ctx, `
		UPDATE acquisition_targets
		SET summary = $2, `+scheduleLook("$3::timestamptz")+`, last_attempt_at = now(),
		    below_floor_streak = 0, below_floor_since = NULL,
		    updated_at = now()
		WHERE id = $1 AND `+searchedWant("")+`
		RETURNING `+lookCounted+`
	`, params.AcquisitionTargetID, params.TargetSummary, params.NextAttemptAt).Scan(&attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if attempts < 1 {
		attempts = 1
	}
	if _, err = tx.Exec(ctx, `
		INSERT INTO acquisition_target_attempts (
			acquisition_target_id, attempt, outcome, detail
		)
		VALUES ($1, $2, $3, nullif($4, ''))
	`, params.AcquisitionTargetID, attempts, params.Outcome, params.Summary); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, queueAcquisitionSweep, params.NextAttemptAt); err != nil {
		return fmt.Errorf("ask for a sweep after %s came to nothing: %w",
			params.AcquisitionTargetID, err)
	}
	return nil
}

// RecordingGoneParams is what settling one fetched copy needs when the
// recording its want was checked against is one MusicBrainz has deleted or
// merged away.
type RecordingGoneParams struct {
	RecordAcquiredFileParams
	// RecordingID is the recording MusicBrainz no longer knows, read when the
	// copy was claimed. The want is reopened only while it still names this
	// recording — a resolution written in the meantime is left exactly as
	// whatever wrote it left it.
	RecordingID uuid.UUID
	ImportError string
	// Summary is what the want says once reopened, and ManualSummary is what it
	// says instead when a person, not the resolver, chose the recording that
	// turned out to be gone: their choice was not wrong, MusicBrainz changed.
	Summary       string
	ManualSummary string
	NextAttemptAt time.Time
}

// SettleAcquiredFileForGoneRecording discards a fetched copy that could not be
// checked because MusicBrainz no longer knows the recording its want named, and
// sends the want back to be resolved again rather than back into the queue for
// another copy — another copy would fail at the same step.
//
// Recording the copy and closing the request is exactly what SettleAcquiredFile
// does for a refusal. What differs is how the want is put back: reopened to
// 'unresolved' with its recording cleared, the way
// RejectAcquisitionTargetResolution reopens a want whose resolution a person
// rejected, but without that path's other half — nothing is written down as a
// recording somebody rejected, because nobody did.
func (q *Queries) SettleAcquiredFileForGoneRecording(
	ctx context.Context, params RecordingGoneParams,
) error {
	return q.inTransaction(ctx, "sending a want back to resolution", func(tx pgx.Tx) error {
		if err := recordAcquiredFile(ctx, tx, params.RecordAcquiredFileParams); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `
			UPDATE download_requests
			SET import_status = 'discarded', import_error = nullif($2, ''), updated_at = now()
			WHERE id = $1 AND import_status = 'validating'
		`, params.DownloadRequestID.UUID, params.ImportError)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("validating candidate import was not found")
		}
		if err := recordImportReview(
			ctx, tx, params.DownloadRequestID.UUID, "discarded", params.ImportError, nil,
		); err != nil {
			return err
		}
		reopened, err := tx.Exec(ctx, `
			UPDATE acquisition_targets
			SET status = 'unresolved',
			    musicbrainz_recording_id = NULL,
			    musicbrainz_release_group_id = NULL,
			    resolution_method = NULL,
			    resolved_at = NULL,
			    review_reason = NULL,
			    summary = CASE WHEN resolution_method = 'manual' THEN $4 ELSE $3 END,
			    last_error = NULL,
			    next_attempt_at = $5,
			    `+armSearch("'unresolved'")+`,
			    last_attempt_at = now(),
			    below_floor_streak = 0,
			    below_floor_since = NULL,
			    updated_at = now()
			WHERE id = $1 AND musicbrainz_recording_id = $2
			  AND status IN ('pending', 'searching', 'awaiting_review')
		`, params.AcquisitionTargetID, params.RecordingID, params.Summary,
			params.ManualSummary, params.NextAttemptAt)
		if err != nil {
			return err
		}
		if reopened.RowsAffected() == 0 {
			// The want moved on while this copy was being checked — answered by
			// hand, resolved again, or given up on. The copy is still discarded;
			// there is nothing left here for a sweep to look for.
			return nil
		}
		if _, err := tx.Exec(ctx, queueAcquisitionSweep, params.NextAttemptAt); err != nil {
			return fmt.Errorf("ask for a sweep after %s lost its recording: %w",
				params.AcquisitionTargetID, err)
		}
		return nil
	})
}

// CompleteTargetImport imports one proven candidate and records that it was
// accepted, in one transaction.
//
// The two halves cannot be separate writes. A crash between them would leave a
// file in the library that no want knows about, and the want would go looking for
// another copy of music it already has.
//
// The offer does not name a library file yet, because there is no library row
// until the scan this queues has run. Completing that is what settles the want.
//
// The want is brought forward to now in the same transaction. It was stood down
// for half an hour when the transfer started — the time it was prepared to hear
// nothing for — and hearing something is exactly what has just happened, so
// leaving it stood down would have a want that is satisfied describing itself as
// still waiting for a copy until that half hour ran out. No attempt is counted:
// nothing was attempted here, and the settling is still the next sweep's, once
// the scan has made a library row of the file.
func (q *Queries) CompleteTargetImport(
	ctx context.Context,
	params RecordAcquiredFileParams,
	targetSummary string,
	rootPath string,
	importPath string,
	localPaths map[string]ImportedDownloadFile,
) error {
	return q.inTransaction(ctx, "importing a proven candidate", func(tx pgx.Tx) error {
		if err := completeDownloadImport(
			ctx, tx, params.DownloadRequestID.UUID, rootPath, importPath, localPaths,
		); err != nil {
			return err
		}
		if err := recordAcquiredFile(ctx, tx, params); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			UPDATE acquisition_targets
			SET summary = $2, `+scheduleLook("now()")+`,
			    below_floor_streak = 0, below_floor_since = NULL,
			    updated_at = now()
			WHERE id = $1 AND `+searchedWant("")+`
		`, params.AcquisitionTargetID, targetSummary)
		return err
	})
}

// AcquisitionTargetFiles reads every offer already judged for one want, newest
// first. The loop reads it to avoid fetching what it has already answered, and
// review reads it to show what has been tried.
func (q *Queries) AcquisitionTargetFiles(
	ctx context.Context, targetID uuid.UUID,
) ([]AcquisitionTargetFileRow, error) {
	rows, err := q.db.Query(ctx, `
		SELECT id, acquisition_target_id, provider, source_username, remote_path,
		       file_name, size_bytes, verdict, decided_by, summary, evidence,
		       download_request_id, library_file_id, decided_at,
		       audio_fingerprint, audio_fingerprint_seconds, waveform
		FROM acquisition_target_files
		WHERE acquisition_target_id = $1
		ORDER BY decided_at DESC, id
	`, targetID)
	if err != nil {
		return nil, fmt.Errorf("read what has been tried for want %s: %w", targetID, err)
	}
	defer rows.Close()
	return scanAcquiredFiles(rows)
}

// scanAcquiredFiles reads offer rows selected in the column order above.
func scanAcquiredFiles(rows pgx.Rows) ([]AcquisitionTargetFileRow, error) {
	result := make([]AcquisitionTargetFileRow, 0, 8)
	for rows.Next() {
		var (
			row         AcquisitionTargetFileRow
			evidence    []byte
			fingerprint pgtype.Text
			seconds     pgtype.Int4
		)
		if err := rows.Scan(
			&row.ID, &row.AcquisitionTargetID, &row.Provider, &row.SourceUsername,
			&row.RemotePath, &row.FileName, &row.SizeBytes, &row.Verdict,
			&row.DecidedBy, &row.Summary, &evidence, &row.DownloadRequestID,
			&row.LibraryFileID, &row.DecidedAt, &fingerprint, &seconds, &row.Waveform,
		); err != nil {
			return nil, err
		}
		if len(evidence) > 0 {
			var decoded AcquiredFileEvidence
			if err := json.Unmarshal(evidence, &decoded); err != nil {
				return nil, fmt.Errorf("decode evidence for %q: %w", row.FileName, err)
			}
			row.Evidence = &decoded
		}
		// The fingerprint is a column, and it is handed back inside the same bag
		// the rest of this copy's findings are read from, so a caller has one
		// place to look. A row with no evidence document and a fingerprint still
		// carries the fingerprint.
		if fingerprint.Valid && fingerprint.String != "" {
			if row.Evidence == nil {
				row.Evidence = &AcquiredFileEvidence{}
			}
			row.Evidence.AudioFingerprint = fingerprint.String
			row.Evidence.AudioFingerprintSeconds = int(seconds.Int32)
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

// AcquisitionReviewItem is one want waiting on a person.
//
// A want stops for one of two reasons and both are questions, so both are this.
// Files are the copies that arrived claiming to be the recording; Candidates are
// the recordings the entry could have named, when it never got that far.
//
// Usually one of the two is empty, but not always, and the exception matters. A
// want that was resolved, fetched a copy nobody could judge, and then had its
// resolution rejected as the wrong recording keeps that copy while going back to
// be looked up again — and it can come back ambiguous. Then both are here, and
// the resolution is the question to ask: nobody can say whether a file is the
// wanted recording while which recording is wanted is exactly what is in doubt.
// The copies are not lost by waiting. They are still held, and the queue offers
// them again as soon as the want knows what it is looking for.
//
// Ruled is how many other offers have already been struck off for this want,
// which is not shown as a list but is the difference between "nobody has looked"
// and "this is the eleventh try".
type AcquisitionReviewItem struct {
	Target     AcquisitionTargetRow
	Files      []AcquisitionTargetFileRow
	Candidates []AcquisitionTargetCandidateRow
	Ruled      int64
}

type AcquisitionReviewPage struct {
	Items []AcquisitionReviewItem
	Total int64
}

// reviewQuestions is every want that has stopped and is waiting on a person,
// with the moment it started waiting.
//
// There are two ways to be here and the queue holds both, because to the person
// working through it they are the same thing: a want that will not move again
// until they say something. One is a copy nobody could decide about; the other is
// an entry that fits several recordings equally well, which stopped before there
// was anything to fetch. A queue that listed only the first left the second
// waiting with no surface at all — stored, answerable, and invisible.
//
// Only 'held' copies count as a question. A copy the loop discarded on the audio
// or on contradicting identifiers is not a question — it was answered, and
// re-offering it would be asking somebody to overturn a proof.
//
// And only wants that are still being pursued. A held copy is a question about a
// want, not about a file, so once the want is settled the question is gone even
// though the copy that would have answered it is still on record. Accepting one
// copy does not rule on its siblings — nobody listened to them — so they stay
// held, and a queue that asked for held rows alone put an answered want straight
// back in front of the user under a name they had just confirmed. Answering it
// again imported a second copy of music already in the library.
//
// A want MusicBrainz holds no recording for used to be a third way to be here,
// and it is not one any more. It is not a decision: there is nothing to choose
// between and no answer to give, only the offer to go and write the release into
// MusicBrainz by hand. One playlist of 314 entries put 253 of those rows in the
// queue, against 34 rows that were actually asking something, and a queue where
// seven of every eight rows cannot be answered is a queue nobody reaches the
// bottom of. The offer itself is not withdrawn — every playlist entry carries
// the same filled-in release editor on the playlist page, which is where the
// entries came from and where a person browsing them already is.
const reviewQuestions = `
	held AS (
		SELECT acquisition_target_files.acquisition_target_id AS target_id,
		       min(acquisition_target_files.decided_at) AS asked_at
		FROM acquisition_target_files
		JOIN acquisition_targets
		    ON acquisition_targets.id = acquisition_target_files.acquisition_target_id
		WHERE acquisition_target_files.verdict = 'held'
		  AND acquisition_targets.status NOT IN ('acquired', 'not_wanted', 'superseded')
		  AND NOT EXISTS (
		      -- A want somebody already answered is not asked again. The answer is
		      -- the accepted copy itself rather than a status, because the target
		      -- only turns 'acquired' once the import lands, and in between the
		      -- leftover held siblings put the same song back on the queue --
		      -- where confirming a second one imported the same recording twice.
		      --
		      -- A failed import is not an answer. The question comes back with the
		      -- copies still waiting rather than being buried.
		      SELECT 1 FROM acquisition_target_files answered
		      JOIN download_requests
		          ON download_requests.id = answered.download_request_id
		      WHERE answered.acquisition_target_id
		            = acquisition_target_files.acquisition_target_id
		        AND answered.verdict = 'accepted'
		        AND download_requests.import_status
		            IN ('pending', 'validating', 'imported')
		        AND (download_requests.import_status = 'imported'
		             OR download_requests.import_error IS NULL)
		  )
		GROUP BY acquisition_target_files.acquisition_target_id
	),
	asked AS (
		-- The entry that fits more than one recording. It has been waiting since
		-- the attempt that gave up on it, which is what puts it in line with the
		-- copies: both are ordered by how long somebody has been meant to answer.
		SELECT id AS target_id, coalesce(last_attempt_at, updated_at) AS asked_at
		FROM acquisition_targets
		WHERE status = 'awaiting_review' AND review_reason = 'resolution'
	),
	stopped AS (
		-- The want that has its music and cannot be given it. Its copy was proven
		-- and imported, and the library calls that file a different recording, so
		-- nothing automatic may settle it and nothing may fetch for it either.
		--
		-- Two facts, and both are needed. The want is still wanted and has no time
		-- to be looked at again, which is what stopping looks like; and it holds a
		-- copy that was accepted and became a file, which is why it stopped. A
		-- want with no time and no copy is one nobody has scheduled yet, and it
		-- has nothing for a person to decide.
		SELECT acquisition_targets.id AS target_id,
		       coalesce(acquisition_targets.last_attempt_at,
		                acquisition_targets.updated_at) AS asked_at
		FROM acquisition_targets
		JOIN acquisition_target_files
		    ON acquisition_target_files.acquisition_target_id = acquisition_targets.id
		WHERE acquisition_targets.status = 'pending'
		  AND acquisition_targets.next_attempt_at IS NULL
		  AND acquisition_target_files.verdict = 'accepted'
		  AND acquisition_target_files.library_file_id IS NOT NULL
	),
	credit_only AS (
		-- A tag contradiction can be permanent for a want lifted from a mix or
		-- compilation. It is a question only when every arrived copy made the
		-- same artist-only contradiction; another verdict still means the want
		-- may have a copy elsewhere.
		SELECT acquisition_targets.id AS target_id,
		       coalesce(acquisition_targets.last_attempt_at,
		                acquisition_targets.updated_at) AS asked_at
		FROM acquisition_targets
		JOIN acquisition_target_files
		    ON acquisition_target_files.acquisition_target_id = acquisition_targets.id
		WHERE acquisition_targets.status = 'pending'
		  AND acquisition_targets.next_attempt_at IS NULL
		  AND acquisition_target_files.verdict = 'discarded_tags'
		  AND acquisition_target_files.evidence->'differs' = '["artist"]'::jsonb
		  AND NOT EXISTS (
		      SELECT 1 FROM acquisition_target_files other
		      WHERE other.acquisition_target_id = acquisition_targets.id
		        AND NOT (
		            other.verdict = 'discarded_tags'
		            AND other.evidence->'differs' = '["artist"]'::jsonb
		        )
		  )
	),
	questions AS (
		SELECT target_id, min(asked_at) AS asked_at
		FROM (
			SELECT * FROM held
			UNION ALL SELECT * FROM asked
			UNION ALL SELECT * FROM stopped
			UNION ALL SELECT * FROM credit_only
		) AS either
		GROUP BY target_id
	)
`

// AcquisitionReviewQueue reads the wants waiting on a person, oldest question
// first.
//
// Oldest first because the queue is worked through rather than browsed, and
// every want in it has stopped: nothing about it moves again until it is
// answered. Newest-first would bury the things that have been stuck longest
// under whatever arrived this morning.
func (q *Queries) AcquisitionReviewQueue(
	ctx context.Context, limit, offset int32,
) (AcquisitionReviewPage, error) {
	var page AcquisitionReviewPage
	if err := q.db.QueryRow(ctx, `
		WITH`+reviewQuestions+`
		SELECT count(*) FROM questions
	`).Scan(&page.Total); err != nil {
		return AcquisitionReviewPage{}, fmt.Errorf("count wants awaiting a decision: %w", err)
	}
	if page.Total == 0 {
		page.Items = []AcquisitionReviewItem{}
		return page, nil
	}

	rows, err := q.db.Query(ctx, `
		WITH`+reviewQuestions+`,
		waiting AS (
			SELECT target_id, asked_at
			FROM questions
			ORDER BY asked_at, target_id
			LIMIT $1 OFFSET $2
		)
		SELECT`+acquisitionTargetColumns+`,
		       (
		           SELECT count(*)
		           FROM acquisition_target_files ruled
		           WHERE ruled.acquisition_target_id = acquisition_targets.id
		             AND ruled.verdict IN ('discarded_audio', 'discarded_tags')
		       ) AS ruled
		FROM waiting
		JOIN acquisition_targets ON acquisition_targets.id = waiting.target_id
		ORDER BY waiting.asked_at, acquisition_targets.id
	`, limit, offset)
	if err != nil {
		return AcquisitionReviewPage{}, fmt.Errorf("read wants awaiting a decision: %w", err)
	}
	defer rows.Close()

	page.Items = make([]AcquisitionReviewItem, 0, limit)
	for rows.Next() {
		var item AcquisitionReviewItem
		if err := scanAcquisitionTargetInto(rows, &item.Target, &item.Ruled); err != nil {
			return AcquisitionReviewPage{}, err
		}
		item.Files = []AcquisitionTargetFileRow{}
		item.Candidates = []AcquisitionTargetCandidateRow{}
		page.Items = append(page.Items, item)
	}
	if err := rows.Err(); err != nil {
		return AcquisitionReviewPage{}, err
	}

	// The copies and the candidates come in one query each for the whole page
	// rather than one per want. A queue is read a screenful at a time and a
	// per-item round trip would make the page cost grow with how far behind the
	// user is.
	ids := make([]uuid.UUID, 0, len(page.Items))
	at := make(map[uuid.UUID]int, len(page.Items))
	for index, item := range page.Items {
		ids = append(ids, item.Target.ID)
		at[item.Target.ID] = index
	}
	held, err := q.heldFiles(ctx, ids)
	if err != nil {
		return AcquisitionReviewPage{}, err
	}
	for _, file := range held {
		index := at[file.AcquisitionTargetID]
		page.Items[index].Files = append(page.Items[index].Files, file)
	}
	offered, err := q.candidatesFor(ctx, ids)
	if err != nil {
		return AcquisitionReviewPage{}, err
	}
	for targetID, candidates := range offered {
		page.Items[at[targetID]].Candidates = candidates
	}
	return page, nil
}

// ErrNotWaitingForADecision reports a decision about a copy that is not waiting
// for one. Something already answered it, and an answer is never re-asked.
var ErrNotWaitingForADecision = errors.New("this copy is not waiting for a decision")

// AcceptHeldCopy records that a person says one held copy is the wanted
// recording, and re-opens its import so the file is actually brought in.
//
// The decision and the import are deliberately two steps, in the shape
// RevalidateDownloadImport already uses. Copying a file into the library is work
// that belongs to the import job — it reads tags, writes to disk and queues a
// scan — and doing it inside a request handler would put a filesystem copy behind
// an HTTP timeout. What is written here is only the decision, and it is written
// first, so a crash before the import leaves a decision that will be honoured
// rather than a file nobody decided about.
//
// The verdict is 'accepted' with decided_by 'user' before the file exists in the
// library, which is why the schema only requires library_file_id on a copy that
// has one. recordAcquiredFile refuses to overwrite a person's verdict, so the
// loop cannot undo this.
//
// ErrNotWaitingForADecision means the copy was already answered, or the want it
// was held for was. The want is checked as well as the copy because accepting one
// copy leaves its siblings held — nobody listened to them — and a want that has
// already been satisfied must not be able to take a second answer. Without this,
// one held sibling was enough to import a second copy of music already in the
// library.
func (q *Queries) AcceptHeldCopy(
	ctx context.Context, copyID uuid.UUID, summary string,
) (uuid.UUID, error) {
	var requestID uuid.UUID
	err := q.inTransaction(ctx, "accepting a held copy", func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			UPDATE acquisition_target_files
			SET verdict = 'accepted', decided_by = 'user', summary = $2,
			    decided_at = now(), updated_at = now()
			WHERE id = $1
			  AND (
			      verdict = 'held'
			      OR (
			          verdict = 'discarded_tags'
			          AND evidence->'differs' = '["artist"]'::jsonb
			          AND EXISTS (
			              SELECT 1 FROM acquisition_targets
			              WHERE acquisition_targets.id = acquisition_target_files.acquisition_target_id
			                AND acquisition_targets.status = 'pending'
			                AND acquisition_targets.next_attempt_at IS NULL
			          )
			          AND NOT EXISTS (
			              SELECT 1 FROM acquisition_target_files other
			              WHERE other.acquisition_target_id = acquisition_target_files.acquisition_target_id
			                AND NOT (
			                    other.verdict = 'discarded_tags'
			                    AND other.evidence->'differs' = '["artist"]'::jsonb
			                )
			          )
			      )
			  )
			  AND EXISTS (
			      SELECT 1 FROM acquisition_targets
			      WHERE acquisition_targets.id = acquisition_target_files.acquisition_target_id
			        AND acquisition_targets.status
			            NOT IN ('acquired', 'not_wanted', 'superseded')
			        -- Accepting a copy writes what it is. A want with no recording
			        -- has only its anchor's address to write, and a name-found
			        -- upload's address names no track (ADR 0037 §5).
			        AND (acquisition_targets.musicbrainz_recording_id IS NOT NULL
			             OR (acquisition_targets.status = 'unresolved'
			                 AND `+anchorAdmits("acquisition_targets.")+`))
			  )
			  AND NOT EXISTS (
			      -- One answer per want. The status guard above only bites once the
			      -- import has landed, and a second copy confirmed while the first
			      -- was still importing is how the library ended up with the same
			      -- recording twice.
			      SELECT 1 FROM acquisition_target_files answered
			      JOIN download_requests
			          ON download_requests.id = answered.download_request_id
			      WHERE answered.acquisition_target_id
			            = acquisition_target_files.acquisition_target_id
			        AND answered.verdict = 'accepted'
			        AND download_requests.import_status
		            IN ('pending', 'validating', 'imported')
		        AND (download_requests.import_status = 'imported'
		             OR download_requests.import_error IS NULL)
			  )
			RETURNING download_request_id
		`, copyID, summary).Scan(&requestID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE download_requests
			SET import_status = 'pending', import_error = NULL, updated_at = now()
			WHERE id = $1 AND status = 'completed'
		`, requestID); err != nil {
			return err
		}
		if err := recordImportReview(ctx, tx, requestID, "resolved", summary, nil); err != nil {
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
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrNotWaitingForADecision
	}
	return requestID, err
}

// RefuseHeldCopies records that a person says none of the copies waiting on this
// want are the recording, and sends the want back to look for another.
//
// It is the same sentence as a discard — this file is not that recording — so it
// goes to the same store rather than a second one, which is what stops the loop
// from offering back a file somebody has just rejected. A person listening and
// saying no is a judgement about the audio, so it is recorded as one; what
// separates it from the loop's own is decided_by, which is also what makes it
// permanent.
//
// ErrNotWaitingForADecision means there was nothing held to refuse, or the want
// was already settled. A refusal is permanent, so it must not be recordable
// against a want that stopped asking: the copies held on a satisfied want are
// siblings of the one that answered it, and nobody listened to them.
func (q *Queries) RefuseHeldCopies(
	ctx context.Context, targetID uuid.UUID, summary, targetSummary string,
	nextAttemptAt time.Time,
) error {
	return q.inTransaction(ctx, "refusing the copies held for a want", func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE acquisition_target_files
			SET verdict = 'discarded_audio', decided_by = 'user', summary = $2,
			    decided_at = now(), updated_at = now()
			WHERE acquisition_target_id = $1
			  AND (
			      verdict = 'held'
			      OR (
			          verdict = 'discarded_tags'
			          AND evidence->'differs' = '["artist"]'::jsonb
			          AND EXISTS (
			              SELECT 1 FROM acquisition_targets
			              WHERE acquisition_targets.id = $1
			                AND acquisition_targets.status = 'pending'
			                AND acquisition_targets.next_attempt_at IS NULL
			          )
			          AND NOT EXISTS (
			              SELECT 1 FROM acquisition_target_files other
			              WHERE other.acquisition_target_id = $1
			                AND NOT (
			                    other.verdict = 'discarded_tags'
			                    AND other.evidence->'differs' = '["artist"]'::jsonb
			                )
			          )
			      )
			  )
			  AND EXISTS (
			      SELECT 1 FROM acquisition_targets
			      WHERE acquisition_targets.id = $1
			        AND acquisition_targets.status
			            NOT IN ('acquired', 'not_wanted', 'superseded')
			  )
		`, targetID, summary)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotWaitingForADecision
		}
		// The requests that fetched them are answered too, or the pending sweep
		// would queue them for import again at the next boot.
		if _, err := tx.Exec(ctx, `
			UPDATE download_requests
			SET import_status = 'discarded', import_error = $2, updated_at = now()
			WHERE acquisition_target_id = $1 AND import_status IN ('pending', 'needs_review')
		`, targetID, summary); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			UPDATE acquisition_targets
			SET summary = $2, `+scheduleLook("$3::timestamptz")+`,
			    below_floor_streak = 0, below_floor_since = NULL,
			    updated_at = now()
			WHERE id = $1 AND `+searchedWant("")+`
		`, targetID, targetSummary, nextAttemptAt)
		return err
	})
}

// UserAcceptedCopy reports whether a person has already decided that the copy
// this request fetched is the wanted recording. The import reads it before it
// grades anything: a decision somebody made is not re-litigated by a check, in
// exactly the way a manual mapping is never replaced by an automatic one.
func (q *Queries) UserAcceptedCopy(ctx context.Context, requestID uuid.UUID) (bool, error) {
	var accepted bool
	err := q.db.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM acquisition_target_files
			WHERE download_request_id = $1
			  AND verdict = 'accepted' AND decided_by = 'user'
			  AND library_file_id IS NULL
		)
	`, requestID).Scan(&accepted)
	return accepted, err
}

// AcquiredCopy reads one offer by name, for the review queue to play or decide
// about. pgx.ErrNoRows means no such copy.
func (q *Queries) AcquiredCopy(
	ctx context.Context, id uuid.UUID,
) (AcquisitionTargetFileRow, error) {
	rows, err := q.db.Query(ctx, `
		SELECT id, acquisition_target_id, provider, source_username, remote_path,
		       file_name, size_bytes, verdict, decided_by, summary, evidence,
		       download_request_id, library_file_id, decided_at,
		       audio_fingerprint, audio_fingerprint_seconds, waveform
		FROM acquisition_target_files
		WHERE id = $1
	`, id)
	if err != nil {
		return AcquisitionTargetFileRow{}, fmt.Errorf("read copy %s: %w", id, err)
	}
	defer rows.Close()
	found, err := scanAcquiredFiles(rows)
	if err != nil {
		return AcquisitionTargetFileRow{}, err
	}
	if len(found) == 0 {
		return AcquisitionTargetFileRow{}, pgx.ErrNoRows
	}
	return found[0], nil
}

// heldFiles reads the copies a person is being shown for the wants on one page
// of the queue.
//
// Held copies are the question itself: nobody has decided about them. The one
// accepted copy that became a library file is read as well, because a want that
// stopped on a disagreement is asking about that copy and about no other, and a
// question with no evidence under it is a sentence nobody can act on.
//
// The two can arrive together. Accepting one copy leaves its siblings held —
// nobody listened to them — so a want that stopped on a disagreement carries
// both its accepted copy and whatever was never answered about the rest. That is
// the right thing to show: the accepted copy is what the question is about, and
// the held ones are what else arrived. Which question is being asked is read
// from the want, never from the length of this list.
func (q *Queries) heldFiles(
	ctx context.Context, targetIDs []uuid.UUID,
) ([]AcquisitionTargetFileRow, error) {
	if len(targetIDs) == 0 {
		return nil, nil
	}
	rows, err := q.db.Query(ctx, `
		SELECT id, acquisition_target_id, provider, source_username, remote_path,
		       file_name, size_bytes, verdict, decided_by, summary, evidence,
		       download_request_id, library_file_id, decided_at,
		       audio_fingerprint, audio_fingerprint_seconds, waveform
		FROM acquisition_target_files
		WHERE acquisition_target_id = ANY($1)
		  AND (verdict = 'held'
		       OR (verdict = 'accepted' AND library_file_id IS NOT NULL)
		       OR (
		           verdict = 'discarded_tags'
		           AND evidence->'differs' = '["artist"]'::jsonb
		           AND EXISTS (
		               SELECT 1 FROM acquisition_targets
		               WHERE acquisition_targets.id = acquisition_target_files.acquisition_target_id
		                 AND acquisition_targets.status = 'pending'
		                 AND acquisition_targets.next_attempt_at IS NULL
		           )
		           AND NOT EXISTS (
		               SELECT 1 FROM acquisition_target_files other
		               WHERE other.acquisition_target_id = acquisition_target_files.acquisition_target_id
		                 AND NOT (
		                     other.verdict = 'discarded_tags'
		                     AND other.evidence->'differs' = '["artist"]'::jsonb
		                 )
		           )
		       ))
		ORDER BY acquisition_target_id, decided_at, id
	`, targetIDs)
	if err != nil {
		return nil, fmt.Errorf("read copies awaiting a decision: %w", err)
	}
	defer rows.Close()
	return scanAcquiredFiles(rows)
}

// DownloadRequestServesWant reports whether a request is one file fetched for a
// want rather than a release somebody chose a folder for. The two are validated
// by different rules, and which one applies is a fact about the request rather
// than something to infer from what it happens to contain.
func (q *Queries) DownloadRequestServesWant(ctx context.Context, requestID uuid.UUID) (bool, error) {
	var serves bool
	err := q.db.QueryRow(ctx, `
		SELECT acquisition_target_id IS NOT NULL
		FROM download_requests
		WHERE id = $1
	`, requestID).Scan(&serves)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, fmt.Errorf("%w: %s", ErrDownloadRequestGone, requestID)
	}
	if err != nil {
		return false, err
	}
	return serves, nil
}

// OpenTargetRequest reports the request already fetching a copy for this want, if
// there is one. A want is pursued one candidate at a time, and the index enforces
// it; this is what lets a sweep pass over a want that is already busy instead of
// failing on that index.
func (q *Queries) OpenTargetRequest(ctx context.Context, targetID uuid.UUID) (uuid.NullUUID, error) {
	var requestID uuid.NullUUID
	// A request whose transfer has finished is not done with: the copy is waiting
	// to be checked, and fetching a second one while that is pending would be two
	// copies of the same music arriving for one want.
	err := q.db.QueryRow(ctx, `
		SELECT id FROM download_requests
		WHERE acquisition_target_id = $1
		  AND (status IN ('requested', 'started')
		       OR (status = 'completed' AND import_status IN ('pending', 'validating')))
		ORDER BY requested_at DESC
		LIMIT 1
	`, targetID).Scan(&requestID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.NullUUID{}, nil
	}
	if err != nil {
		return uuid.NullUUID{}, fmt.Errorf("read the open request for want %s: %w", targetID, err)
	}
	return requestID, nil
}

// OpenTargetTransferQueuedPast reports whether a request's transfer has sat in
// a peer's queue since before cutoff without one byte of it moving: every file
// it covers is still 'queued', and the oldest of them was enqueued — the
// downloads row written when slskd was asked to take it, not when the request
// itself was recorded — before cutoff. A request not yet sent to a peer at all
// has no downloads row yet and is never reported as stalled by this: it is
// waiting on its own turn, not on a peer's silence.
func (q *Queries) OpenTargetTransferQueuedPast(
	ctx context.Context, requestID uuid.UUID, cutoff time.Time,
) (bool, error) {
	var stalled bool
	err := q.db.QueryRow(ctx, `
		SELECT
			EXISTS (SELECT 1 FROM downloads WHERE download_request_id = $1)
			AND NOT EXISTS (
				SELECT 1 FROM downloads
				WHERE download_request_id = $1 AND status <> 'queued'
			)
			AND (SELECT min(queued_at) FROM downloads WHERE download_request_id = $1) < $2
	`, requestID, cutoff).Scan(&stalled)
	if err != nil {
		return false, fmt.Errorf("read whether request %s has stalled in a peer's queue: %w", requestID, err)
	}
	return stalled, nil
}

// AcquisitionTargetHasAcceptedCopy reports whether one copy of this want has
// already been accepted — proven by its audio, or said to be the right one by a
// person — whatever has happened to it since.
//
// It is the one question every path that is about to go looking has to ask, and
// it is deliberately blunt. An accepted copy is either being imported, waiting
// for the scan to make a library file of it, or already a file the library calls
// something else. All three mean the same thing to a search: this want has its
// music, and fetching more of it would be a second copy of what is already here.
//
// It is asked after the library, never before. The library settles a want it can
// answer, and asking this first would stop a want that a person had just decided
// about from ever being settled.
//
// What counts is the copy having become a file the library holds, which is the
// same fact the review queue reads, the same one the list of wants reads, and
// the same one a decision about a file looks for. One definition, in four
// places, because two definitions of "this want has its music" is how a want
// disappears from the queue while something still refuses to look for it: a file
// somebody deletes takes its id off the copy — the row says ON DELETE SET NULL —
// and a rule that read the import instead would still be saying the music is
// there.
//
// A copy whose import failed is not an answer either, and that half is written
// exactly as the review queue writes it.
//
// A copy that is accepted and not yet a file is not this question's business.
// Completing runs first and stands the want back while the scan catches up, so
// nothing reaches here with one.
func (q *Queries) AcquisitionTargetHasAcceptedCopy(
	ctx context.Context, targetID uuid.UUID,
) (bool, error) {
	var accepted bool
	if err := q.db.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM acquisition_target_files files
			JOIN download_requests ON download_requests.id = files.download_request_id
			WHERE files.acquisition_target_id = $1
			  AND files.verdict = 'accepted'
			  AND files.library_file_id IS NOT NULL
			  AND download_requests.import_status IN ('pending', 'validating', 'imported')
			  AND (download_requests.import_status = 'imported'
			       OR download_requests.import_error IS NULL)
		)
	`, targetID).Scan(&accepted); err != nil {
		return false, fmt.Errorf("look for an accepted copy of want %s: %w", targetID, err)
	}
	return accepted, nil
}

// AcquiredCopyFiling is what a want's accepted copy, and the library file it
// became, have to say about which recording that file is.
//
// It is read where a want is about to stop because the two do not agree. What
// stops a want is the library naming another recording, and that is three
// different facts sitting in two tables — whether the library has answered at
// all, what it answered, and who answered it — so they are read together rather
// than one at a time by callers who would each decide what a null means.
type AcquiredCopyFiling struct {
	// Found is false when the want holds no accepted copy that became a library
	// file. Nothing here means anything then.
	Found         bool
	LibraryFileID uuid.UUID
	// ResolutionStatus is how far the library has got with the file. It matters
	// only where there is no identity: a file nobody has asked about yet is
	// silence, and a file that was asked about and has no answer written down is
	// a hole.
	ResolutionStatus string
	// Identified reports an identity row of any kind. A file with none has not
	// been answered, which is silence and never a disagreement.
	Identified bool
	// FiledRecordingID is the recording the library calls the file. Invalid with
	// Identified true is the library's own answer that the file has no external
	// identity at all.
	FiledRecordingID uuid.NullUUID
	// FiledIsManual reports that a person wrote that answer. Theirs is permanent.
	FiledIsManual bool
	// FiledTitle and FiledArtist are that recording in the words it was written
	// down under, so a want can say which two recordings disagree without asking
	// a provider.
	FiledTitle  string
	FiledArtist string
	// WantedTitle and WantedArtist are the recording the copy was checked
	// against, as MusicBrainz had it when the copy was admitted.
	WantedTitle  string
	WantedArtist string
	// AnchorRate is what the copy came to beside the excerpt published for the
	// want's own ISRC. AnchorMeasured says the comparison ran and answers about
	// the recording the want names now. Zero is a real answer — a perfect one —
	// so the flag is what tells the two apart. What the number means is not
	// decided here.
	AnchorRate     float64
	AnchorMeasured bool
	// AnchorSource names who published that excerpt, read from the copy's own
	// evidence rather than from the want's current anchor: the measurement and
	// the publisher it was made against are one record, and a want re-anchored
	// since would otherwise lend its source to an older number. An excerpt
	// fetched by a registration code and one found by searching for the want's
	// name mean different things (ADR 0029).
	AnchorSource string
	// CopyDurationMS is how long the file on the disc runs, as the scanner read
	// it. Zero is unknown, and unknown agrees with nothing. It stands in for a
	// length MusicBrainz does not hold for one of two rows
	// (ADR 0031).
	CopyDurationMS int
	// ProvenMethod is what admitted the copy: the grader's own name for it, or
	// "manual" where a person listened and said so. It is empty where nothing is
	// recorded, and empty where the copy was judged before the want was resolved
	// to the recording it names now — a copy answered against another recording
	// has said nothing about this one.
	//
	// It is read where a want would otherwise stop on the library filing its copy
	// under a second row. Whether the two rows are one registration is the
	// catalogue's bookkeeping; whether the file is this want's music is what
	// admitted the file, and that is this.
	ProvenMethod string
}

// AcquiredCopyFiling reads what the library says about the file a want's
// accepted copy became.
func (q *Queries) AcquiredCopyFiling(
	ctx context.Context, targetID uuid.UUID,
) (AcquiredCopyFiling, error) {
	var filing AcquiredCopyFiling
	var evidence []byte
	var anchorAnswersThisWant, judgedAgainstThisWant bool
	err := q.db.QueryRow(ctx, `
		SELECT files.library_file_id, library_files.resolution_status,
		       coalesce(library_files.duration_ms, 0),
		       identities.library_file_id IS NOT NULL,
		       identities.musicbrainz_recording_id,
		       coalesce(identities.is_manual, false),
		       coalesce(identities.track_title, ''),
		       coalesce(identities.artist_name, ''),
		       files.evidence,
		       -- Whether the excerpt this copy was measured against was published
		       -- for the recording the want names now. Somebody saying a want
		       -- names the wrong recording sends it back to be resolved again and
		       -- leaves the copies and the anchor where they are, so a want
		       -- resolved a second time can carry an excerpt fetched by the old
		       -- recording's ISRC and a copy judged against it. Neither says
		       -- anything about the recording the want names now.
		       (targets.resolved_at IS NULL
		        OR (files.decided_at >= targets.resolved_at
		            AND targets.anchor_fetched_at IS NOT NULL
		            AND targets.anchor_fetched_at >= targets.resolved_at)),
		       -- Whether the copy was judged against the recording the want names
		       -- now. Same reason as above: a want resolved a second time carries
		       -- copies answered against the recording it used to name, and what
		       -- admitted one of those said nothing about this recording.
		       (targets.resolved_at IS NULL OR files.decided_at >= targets.resolved_at)
		FROM acquisition_target_files files
		JOIN acquisition_targets targets ON targets.id = files.acquisition_target_id
		JOIN library_files ON library_files.id = files.library_file_id
		LEFT JOIN library_file_identities identities
		    ON identities.library_file_id = files.library_file_id
		WHERE files.acquisition_target_id = $1
		  AND files.verdict = 'accepted'
		  AND files.library_file_id IS NOT NULL
		ORDER BY files.decided_at
		LIMIT 1
	`, targetID).Scan(
		&filing.LibraryFileID, &filing.ResolutionStatus, &filing.CopyDurationMS,
		&filing.Identified,
		&filing.FiledRecordingID, &filing.FiledIsManual, &filing.FiledTitle,
		&filing.FiledArtist, &evidence, &anchorAnswersThisWant, &judgedAgainstThisWant,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return AcquiredCopyFiling{}, nil
	}
	if err != nil {
		return AcquiredCopyFiling{}, fmt.Errorf(
			"read what the library calls the copy of want %s: %w", targetID, err)
	}
	filing.Found = true
	if len(evidence) > 0 {
		var decoded AcquiredFileEvidence
		if err := json.Unmarshal(evidence, &decoded); err != nil {
			return AcquiredCopyFiling{}, fmt.Errorf(
				"decode the evidence that admitted the copy of want %s: %w", targetID, err)
		}
		if decoded.Wanted != nil {
			filing.WantedTitle, filing.WantedArtist = decoded.Wanted.Title, decoded.Wanted.Artist
		}
		if decoded.Anchor != nil && anchorAnswersThisWant {
			filing.AnchorRate, filing.AnchorMeasured = decoded.Anchor.Rate, decoded.Anchor.Measured
			filing.AnchorSource = decoded.Anchor.Source
		}
		if judgedAgainstThisWant {
			filing.ProvenMethod = decoded.Method
		}
	}
	return filing, nil
}

// StopLookingForAcquisitionTarget takes a want's next look away and says why.
//
// The want keeps everything else: it is still wanted, still pending, and the
// copy it holds is untouched. It simply has no time to be looked at again,
// because it has its music and only a person can say what that music is. The
// review queue asks about it in exactly this state, and wanting it again puts a
// time back.
//
// No attempt is counted. Nothing was attempted: the want was stopped before it
// could look.
//
// It is waiting for evidence rather than for a person, and the row says so. What
// the library calls that file can still change — a resolution run again, a
// person deciding, the borrowed decision withdrawn — and every one of those
// gives the want its next look back through RearmWantsHoldingFile. No clock is
// set: nothing here gets better by asking again on a timer.
func (q *Queries) StopLookingForAcquisitionTarget(
	ctx context.Context, id uuid.UUID, summary string,
) error {
	_, err := q.db.Exec(ctx, `
		UPDATE acquisition_targets
		SET summary = $2, `+scheduleLook("NULL::timestamptz")+`, updated_at = now(),
		    -- An unresolved want has no library identity to be rechecked
		    -- against: its copy was judged on the anchor alone (ADR 0037).
		    recheck_reason = CASE WHEN status = 'pending' THEN $3 END,
		    recheck_after = NULL
		WHERE id = $1 AND `+searchedWant("")+`
	`, id, summary, RecheckForLibraryIdentity)
	if err != nil {
		return fmt.Errorf("stop looking for want %s: %w", id, err)
	}
	return nil
}

// StopAfterCreditOnlyRefusals stops a pending want when every copy it has
// recorded disagreed with the wanted recording on the artist credit alone.
// The exact evidence check keeps recording-ID, duration and audio refusals in
// the search loop, where another copy may still prove the want.
func (q *Queries) StopAfterCreditOnlyRefusals(
	ctx context.Context, id uuid.UUID, summary string,
) (bool, error) {
	var stopped bool
	err := q.db.QueryRow(ctx, `
		UPDATE acquisition_targets
		SET summary = $2, next_attempt_at = NULL, updated_at = now()
		WHERE id = $1 AND status = 'pending'
		  AND EXISTS (
		      SELECT 1 FROM acquisition_target_files files
		      WHERE files.acquisition_target_id = $1
		        AND files.verdict = 'discarded_tags'
		        AND files.evidence->'differs' = '["artist"]'::jsonb
		  )
		  AND NOT EXISTS (
		      SELECT 1 FROM acquisition_target_files files
		      WHERE files.acquisition_target_id = $1
		        AND NOT (
		            files.verdict = 'discarded_tags'
		            AND files.evidence->'differs' = '["artist"]'::jsonb
		        )
		  )
		RETURNING true
	`, id, summary).Scan(&stopped)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("stop want %s after credit-only refusals: %w", id, err)
	}
	return stopped, nil
}

// wantHoldsAnUnansweredCopy is "this want is holding a copy nobody has answered",
// in the same terms the review queue asks it (the held part of reviewQuestions):
// a copy the loop could not decide, and no accepted copy whose import is on its
// way or has landed. The stop and the wake below both read it, so the want that
// stops for a question is the same want an anchor lets go again.
const wantHoldsAnUnansweredCopy = `
	EXISTS (
	    SELECT 1 FROM acquisition_target_files waiting
	    WHERE waiting.acquisition_target_id = acquisition_targets.id
	      AND waiting.verdict = 'held'
	)
	AND NOT EXISTS (
	    SELECT 1 FROM acquisition_target_files answered
	    JOIN download_requests
	        ON download_requests.id = answered.download_request_id
	    WHERE answered.acquisition_target_id = acquisition_targets.id
	      AND answered.verdict = 'accepted'
	      AND download_requests.import_status IN ('pending', 'validating', 'imported')
	      AND (download_requests.import_status = 'imported'
	           OR download_requests.import_error IS NULL)
	)
`

// StopWhileACopyWaits takes away the next look of a pending want that is holding
// a copy nobody has answered and has no anchor to answer it with. It reports
// whether this call is what stopped it.
//
// Without an anchor the only thing that can admit a copy of this want is
// AcoustID naming the recording it asks for, and a want whose first copy
// AcoustID could not name rarely gets a second one it can. On 2026-09-08 the
// review queue held 1,941 copies over 727 wants; 1,278 of those were the second
// or later copy of a want that was already asking a question, one want held 38,
// and 653 of the 663 wants holding copies had no ISRC to fetch an anchor by.
// Every further copy costs a Soulseek search, a transfer, disk, and one more
// file for a person to listen to.
//
// It admits nothing and refuses nothing. The copies keep their verdicts and the
// question keeps its place in the queue; what is taken away is the next fetch.
// Answering the copies gives the want its time back (RefuseHeldCopies and
// AcceptHeldCopy), and so does an anchor landing (RearmWantNowAnchored).
func (q *Queries) StopWhileACopyWaits(
	ctx context.Context, id uuid.UUID, summary string,
) (bool, error) {
	var stopped bool
	err := q.db.QueryRow(ctx, `
		UPDATE acquisition_targets
		SET summary = $2, next_attempt_at = NULL, updated_at = now()
		WHERE id = $1 AND status = 'pending'
		  AND anchor_fingerprint IS NULL
		  AND`+wantHoldsAnUnansweredCopy+`
		RETURNING true
	`, id, summary).Scan(&stopped)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("stop want %s while a copy waits: %w", id, err)
	}
	return stopped, nil
}

// RearmWantNowAnchored gives back the next look of a want that stopped while a
// copy waited for an answer, once an anchor has landed and its copies have been
// measured against it. It reports whether this call woke the want, and asks for
// a sweep in the same transaction when it did.
//
// The condition is the stop's with the anchor test turned round. A want whose
// copy the anchor refused is due already, because settling a refused copy gives
// its want a time; a want whose copy the anchor admitted is being imported. This
// is for the want left over: its copies are still questions, so nothing settled
// them and nothing else would ever look at it again.
func (q *Queries) RearmWantNowAnchored(
	ctx context.Context, id uuid.UUID, summary string,
) (bool, error) {
	var woken bool
	err := q.inTransaction(ctx, "waking a want that has been anchored", func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE acquisition_targets
			SET summary = $2, next_attempt_at = now(), updated_at = now()
			WHERE id = $1 AND status = 'pending'
			  AND next_attempt_at IS NULL
			  AND anchor_fingerprint IS NOT NULL
			  AND`+wantHoldsAnUnansweredCopy+`
		`, id, summary)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return nil
		}
		woken = true
		_, err = tx.Exec(ctx, queueAcquisitionSweep, time.Now())
		return err
	})
	if err != nil {
		return false, fmt.Errorf("wake want %s after it was anchored: %w", id, err)
	}
	return woken, nil
}

// The things that give a stopped want its next look back, in its own words. None
// of them claims an outcome: what happens next is what the sweep is about to
// work out.
const (
	RearmedByADecisionSummary = "You decided what that file is. This wishlist entry is being looked at again."
	RearmedByADeletionSummary = "The copy in your library was deleted. This recording is being looked for again."
	RearmedByARefilingSummary = "Your library files that copy under this recording now. This wishlist entry is being looked at again."
	RearmedByAnAnchorSummary  = "There is a sample of this recording to compare copies against now. This wishlist entry is being looked at again."
	// What a want is told when the library answered what the file it holds is,
	// by itself rather than because somebody said so. It is the fact the want
	// stopped waiting for.
	RearmedByAResolutionSummary = "Your library has said what that file is. This wishlist entry is being looked at again."
)

// RecordFileRemoved writes down that this file is gone, on every copy that
// became it.
//
// It is a fact about the file, not a judgement about the copy: the verdict that
// admitted the copy is untouched, and what its audio proved is still on the
// record. What changes is only what the copy's own link can no longer say once
// the row is gone — a copy waiting for the scan to make its file and a copy
// whose file was deleted look identical afterwards, and they mean opposite
// things to a want: one says the music is arriving, the other says it is gone.
//
// Call this only from the one place a library file is actually deleted. Every
// other caller of RearmWantsHoldingFile is a decision about what a file is, not
// about the file leaving the library, and stamping it there would write down a
// removal that never happened.
//
// It must run before the row goes, in the same transaction as the delete: a
// copy's link to its file is ON DELETE SET NULL, so afterwards nothing could say
// which copies to stamp.
func RecordFileRemoved(ctx context.Context, tx pgx.Tx, fileID uuid.UUID) error {
	if _, err := tx.Exec(ctx, `
		UPDATE acquisition_target_files
		SET file_removed_at = now(), updated_at = now()
		WHERE library_file_id = $1 AND file_removed_at IS NULL
	`, fileID); err != nil {
		return fmt.Errorf("record that the file of the copies of %s is gone: %w", fileID, err)
	}
	return nil
}

// RearmWantsHoldingFile gives a next look back to every want that stopped
// because of this file, and asks for a sweep to take it.
//
// A want stops when its copy is on the disc and the library calls that file a
// different recording: it stays wanted, and it has no time to be looked at
// again, because nothing automatic may settle it. Two things unstick it, and
// the caller says which by the sentence it passes. A person saying what that
// file is — accepting an identity for it, matching it to a track by hand, or
// withdrawing either — is the first. The file being deleted is the second: the
// music is not in the library any more, so the want has something to look for
// again. Without this, both changed the library and nothing ever looked at the
// want again, because the sweeper reads times and the want had none.
//
// It runs inside the caller's transaction, so a want is never re-armed by a
// change that did not commit. Only a want with no time is touched — one that is
// already due keeps the time it has — and no attempt is counted, because nothing
// has been attempted.
func RearmWantsHoldingFile(
	ctx context.Context, tx pgx.Tx, fileID uuid.UUID, summary string,
) error {
	// The recheck goes with the look. The want was waiting for exactly this — the
	// library saying something else about the file it holds — and it is about to
	// be judged again, so its patience starts over rather than carrying the count
	// of a wait that is over.
	tag, err := tx.Exec(ctx, `
		UPDATE acquisition_targets
		SET next_attempt_at = now(), summary = $2, updated_at = now(),
		    recheck_reason = NULL, recheck_after = NULL, recheck_attempts = 0
		WHERE status = 'pending'
		  AND next_attempt_at IS NULL
		  AND EXISTS (
		      SELECT 1 FROM acquisition_target_files
		      WHERE acquisition_target_files.acquisition_target_id = acquisition_targets.id
		        AND acquisition_target_files.verdict = 'accepted'
		        AND acquisition_target_files.library_file_id = $1
		  )
	`, fileID, summary)
	if err != nil {
		return fmt.Errorf("give back the next look of the wants holding %s: %w", fileID, err)
	}
	// Nothing stopped on this file, so there is nothing for a sweep to read.
	if tag.RowsAffected() == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, queueAcquisitionSweep, time.Now()); err != nil {
		return fmt.Errorf("ask for a sweep after a change to %s: %w", fileID, err)
	}
	return nil
}

// RescheduleAcquisitionTarget moves when a want is next looked at, and nothing
// else: no attempt is counted and no attempt row is written, because nothing was
// attempted. It is what a want whose turn is taken by a copy in flight gets, so
// the sweeper has a time to come back at rather than a want that is permanently
// due — which would be a sweep queueing itself over and over, and the single
// worker never reaching the poller that would settle the transfer.
func (q *Queries) RescheduleAcquisitionTarget(
	ctx context.Context, id uuid.UUID, summary string, nextAttemptAt time.Time,
) error {
	_, err := q.db.Exec(ctx, `
		UPDATE acquisition_targets
		SET summary = $2, `+scheduleLook("$3::timestamptz")+`, updated_at = now()
		WHERE id = $1 AND `+searchedWant("")+`
	`, id, summary, nextAttemptAt)
	if err != nil {
		return fmt.Errorf("reschedule want %s: %w", id, err)
	}
	return nil
}

// RescheduleFailedTransfer brings a want back to now when the copy it was
// waiting on failed to arrive at all, and asks for a sweep to look at it
// again in the same transaction.
//
// waitOut gives an in-flight copy inFlightDelay before the want is looked at
// again, on the understanding that a copy refused or proven reschedules the
// want itself, immediately. A transfer that fails outright — never refused,
// never proven, just gone — is the same case: nothing about the music was
// learned, but the copy the want was following is done for, so the wait meant
// for a copy that might still arrive no longer applies.
func (q *Queries) RescheduleFailedTransfer(
	ctx context.Context, targetID uuid.UUID, summary string,
) error {
	return q.inTransaction(ctx, "rescheduling a want after a failed transfer", func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE acquisition_targets
			SET summary = $2, `+scheduleLook("now()")+`,
			    below_floor_streak = 0, below_floor_since = NULL,
			    updated_at = now()
			WHERE id = $1 AND `+searchedWant("")+`
		`, targetID, summary)
		if err != nil {
			return fmt.Errorf("reschedule want %s after a failed transfer: %w", targetID, err)
		}
		if tag.RowsAffected() == 0 {
			return nil
		}
		if _, err := tx.Exec(ctx, queueAcquisitionSweep, time.Now()); err != nil {
			return fmt.Errorf("ask for a sweep after a failed transfer for %s: %w", targetID, err)
		}
		return nil
	})
}

// DeferAcquisitionTarget moves when a want is next looked at and records why,
// without counting an attempt.
//
// It is what a want gets when the provider would not answer the question at all
// — being refused for asking too often is the provider's rate limiter, not a
// statement about whether anybody is sharing the music. Spending an attempt on
// it would push the want down a ladder that climbs to three days, so a burst of
// wants big enough to be throttled would punish itself for being asked, and the
// wants throttled first would be looked for least often afterwards.
func (q *Queries) DeferAcquisitionTarget(
	ctx context.Context, id uuid.UUID, summary, reason string, nextAttemptAt time.Time,
) error {
	_, err := q.db.Exec(ctx, `
		UPDATE acquisition_targets
		SET summary = $2, last_error = nullif($3, ''),
		    `+scheduleLook(`CASE
		        WHEN below_floor_since IS NOT NULL THEN now() + interval '7 days'
		        ELSE $4::timestamptz
		    END`)+`,
		    updated_at = now()
		WHERE id = $1 AND `+searchedWant("")+`
	`, id, summary, reason, nextAttemptAt)
	if err != nil {
		return fmt.Errorf("defer want %s: %w", id, err)
	}
	return nil
}

// DeferredByPeerSummary is what a copy says when the peer would not take the
// request now. It is load-bearing rather than decoration: the acquisition loop
// reads it back to tell this copy from one that genuinely failed to arrive, and
// rests it for minutes rather than for a day. Change it in one place only.
const DeferredByPeerSummary = "The peer is already sending as much as it will take at once, so this copy was not sent. Nothing about the music was learned."

// DeferAcquiredFetch records a copy a peer declined for load rather than
// refused. The verdict is the same 'undelivered' any transfer that did not
// arrive gets — no bytes came, and that much is true — but the sentence beside
// it says why, so the want may come back to this peer shortly instead of
// striking the copy off for a day on a refusal that was never about the music.
//
// No attempt is spent and the want's turn is not moved: the copy it was
// following is done for, the want is already scheduled to look again, and being
// told "not now" costs it nothing. Only the sentence it wears changes, because a
// want that says a copy is on its way when the peer sent nothing is a want
// nobody can read.
//
// Saying it twice costs nothing, which is what lets the caller write it before
// the request settles: only a copy still marked as being fetched is touched.
func (q *Queries) DeferAcquiredFetch(
	ctx context.Context, requestID uuid.UUID, targetSummary string,
) error {
	return q.inTransaction(ctx, "deferring a copy a peer would not send", func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			UPDATE acquisition_target_files
			SET verdict = 'undelivered', summary = $2,
			    decided_at = now(), updated_at = now()
			WHERE download_request_id = $1
			  AND verdict = 'fetching'
			  AND decided_by = 'schall'
		`, requestID, DeferredByPeerSummary); err != nil {
			return fmt.Errorf("defer the copy of request %s: %w", requestID, err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE acquisition_targets
			SET summary = $2, updated_at = now()
			WHERE `+searchedWant("")+`
			  AND id = (SELECT acquisition_target_id FROM download_requests WHERE id = $1)
		`, requestID, targetSummary); err != nil {
			return fmt.Errorf("say why the want of request %s is waiting: %w", requestID, err)
		}
		return nil
	})
}

// ReleaseStaleFetches frees the copies a want started fetching and never heard
// the end of: the request settled without an import, so the bytes never arrived
// as offered. It is recorded as the transport fact it is, which leaves the offer
// available another day rather than struck off on something that says nothing
// about the music.
func (q *Queries) ReleaseStaleFetches(ctx context.Context, targetID uuid.UUID) error {
	_, err := q.db.Exec(ctx, `
		UPDATE acquisition_target_files files
		SET verdict = 'undelivered',
		    summary = 'The transfer settled without the copy arriving.',
		    decided_at = now(), updated_at = now()
		FROM download_requests requests
		WHERE files.download_request_id = requests.id
		  AND files.acquisition_target_id = $1
		  AND files.verdict = 'fetching'
		  AND files.decided_by = 'schall'
		  AND (requests.status IN ('failed', 'cancelled')
		       OR (requests.status = 'completed'
		           AND requests.import_status NOT IN ('pending', 'validating')))
	`, targetID)
	if err != nil {
		return fmt.Errorf("release the stale fetches of want %s: %w", targetID, err)
	}
	return nil
}

// FetchAcquiredFileParams is one copy chosen for one want: the offer, and the
// request that will fetch it.
type FetchAcquiredFileParams struct {
	AcquisitionTargetID uuid.UUID
	Provider            string
	SourceUsername      string
	SourceDirectory     string
	RemotePath          string
	FileName            string
	SizeBytes           int64
	BitRate             int
	DurationSeconds     int
	Extension           string
	Score               float64
	Reasons             []string
	Summary             string
}

// ErrWantHasMovedOn reports a copy not fetched because the want it was chosen
// for had stopped waiting for one. A want is settled by a file the library
// turned out to hold, withdrawn by the person who asked for it, or superseded,
// and any of those can happen while its own turn is still out at a provider.
// Fetching afterwards would spend a peer's upload slot on music nobody is
// waiting for — and when what settled it was a copy, put a second copy of one
// recording on the disc, which is the one outcome the loop exists to avoid.
var ErrWantHasMovedOn = errors.New("this wishlist entry is no longer waiting for a copy")

// FetchAcquiredFile records the request that will fetch one chosen copy, and the
// offer it is fetching, in one transaction.
//
// The two cannot be separate writes: a request with no offer beside it would be
// fetched again on the next pass, and an offer with no request would look like a
// copy already being fetched that nothing was ever going to fetch.
//
// The request goes through download_requests exactly as a release chosen by hand
// does, so the transfer, the poller, the retry, the cancel and the history are
// the ones that already exist.
//
// It refuses a want that is no longer pending with ErrWantHasMovedOn, and one
// that has acquired an open request in the meantime with ErrWantHasAnotherCopy.
// Both are ordinary answers rather than failures: a turn that has been out at a
// provider for minutes is holding a row that was true when it left.
func (q *Queries) FetchAcquiredFile(
	ctx context.Context, params FetchAcquiredFileParams,
) (uuid.UUID, error) {
	var requestID uuid.UUID
	err := q.inTransaction(ctx, "fetching a chosen copy", func(tx pgx.Tx) error {
		files, err := json.Marshal([]DownloadRequestFile{{
			Path: params.RemotePath, Name: params.FileName,
			Extension: params.Extension, SizeBytes: params.SizeBytes,
			BitRate: params.BitRate, DurationSeconds: params.DurationSeconds,
		}})
		if err != nil {
			return fmt.Errorf("encode the chosen copy: %w", err)
		}
		reasons := params.Reasons
		if reasons == nil {
			reasons = []string{}
		}
		// The want is re-read as the insert's own source rather than before it. A
		// turn spends minutes at a provider, and in that time a sibling's harvest
		// can settle this want against a file the library holds, a person can
		// withdraw it, or a resolution can supersede it — all of which move it off
		// pending. Asking first and inserting after would only narrow that window,
		// so the row is conditional on the state that justifies writing it, and no
		// rows means the want moved on. Nothing is written then, offer included:
		// the whole thing is one transaction.
		if err := tx.QueryRow(ctx, `
			INSERT INTO download_requests (
				acquisition_target_id, provider, source_username, source_directory,
				file_count, expected_track_count, total_size_bytes, format,
				average_bit_rate, score, reasons, files
			)
			SELECT targets.id, $2::text, $3::text, $4::text, 1, 1, $5::bigint,
			       $6::text, nullif($7::integer, 0), $8::double precision,
			       $9::text[], $10::jsonb
			FROM acquisition_targets targets
			WHERE targets.id = $1 AND `+searchedWant("targets.")+`
			RETURNING id
		`,
			params.AcquisitionTargetID, params.Provider, params.SourceUsername,
			params.SourceDirectory, params.SizeBytes, params.Extension,
			params.BitRate, params.Score, reasons, files,
		).Scan(&requestID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrWantHasMovedOn
			}
			// The want is still pending and another pass opened its one request
			// between this statement's snapshot and this write.
			// download_requests_open_target_idx catches what no read could, and it
			// means the same thing the read would have: this want is already
			// following a copy, so it is told so rather than handed a constraint.
			if isUniqueViolation(err) {
				return ErrWantHasAnotherCopy
			}
			return fmt.Errorf("record a request for want %s: %w", params.AcquisitionTargetID, err)
		}
		return recordAcquiredFile(ctx, tx, RecordAcquiredFileParams{
			AcquisitionTargetID: params.AcquisitionTargetID,
			Provider:            params.Provider,
			SourceUsername:      params.SourceUsername,
			RemotePath:          params.RemotePath,
			FileName:            params.FileName,
			SizeBytes:           params.SizeBytes,
			Verdict:             AcquiredFileFetching,
			Summary:             params.Summary,
			DownloadRequestID:   uuid.NullUUID{UUID: requestID, Valid: true},
		})
	})
	return requestID, err
}

// AcquiredFileOutcome is what completing an accepted copy came to.
type AcquiredFileOutcome struct {
	// Settled is true when the want is now acquired.
	Settled bool
	// LibraryFileID is the library row the imported copy became.
	LibraryFileID uuid.NullUUID
	// Disagreed is true when the library already holds a different identity for
	// that file. The copy stays imported and nothing is overwritten: a decision
	// already recorded is never replaced by this one.
	Disagreed bool
	// Awaiting is true when a copy has been accepted and imported but the scan
	// has not made a library row of it yet. The want is answered — the music is
	// on the disc — and the only thing missing is the row to settle it against,
	// so nothing may go looking for a second copy in the meantime.
	Awaiting bool
	// AwaitingSince is when the oldest such copy was accepted. Waiting is normal
	// and brief; waiting for hours means the row is never coming, and the caller
	// needs the age to tell one from the other.
	AwaitingSince time.Time
}

// CompleteAcquiredFile finishes an accepted copy once the scan has created the
// library row for it: the file is given the identity verification proved, the
// release behind it is queued for the catalogue, and the want settles.
//
// It is deliberately a separate step from the import. The library row does not
// exist until the scan the import queued has run, and a want that outlived a
// restart in between is settled by the next sweep rather than lost.
//
// Nothing here overwrites an identity that already exists, manual or not. A file
// somebody has already decided about keeps their answer, and a want whose copy
// the library says is something else stops instead of being settled on a
// disagreement.
func (q *Queries) CompleteAcquiredFile(
	ctx context.Context, targetID uuid.UUID, summary, detail string,
) (AcquiredFileOutcome, error) {
	var outcome AcquiredFileOutcome
	err := q.inTransaction(ctx, "completing an acquired copy", func(tx pgx.Tx) error {
		var (
			offerID   uuid.UUID
			fileID    uuid.UUID
			evidence  []byte
			proven    string
			decidedBy string
		)
		err := tx.QueryRow(ctx, `
			SELECT files.id, downloads.library_file_id, files.evidence, files.summary,
			       files.decided_by
			FROM acquisition_target_files files
			JOIN downloads ON downloads.download_request_id = files.download_request_id
			JOIN acquisition_targets targets ON targets.id = files.acquisition_target_id
			WHERE files.acquisition_target_id = $1
			  AND files.verdict = 'accepted'
			  AND files.library_file_id IS NULL
			  AND downloads.library_file_id IS NOT NULL
			  -- A copy judged against a recording the want no longer names has
			  -- nothing to write on a want that names none. Only a copy
			  -- admitted on the anchor alone carries a key of its own.
			  AND (targets.musicbrainz_recording_id IS NOT NULL
			       OR files.evidence ? 'source')
			ORDER BY files.decided_at
			LIMIT 1
		`, targetID).Scan(&offerID, &fileID, &evidence, &proven, &decidedBy)
		if errors.Is(err, pgx.ErrNoRows) {
			// Nothing to complete. Whether that is because no copy was accepted
			// or because the scan has not reached the one that was is the whole
			// difference between a want with nothing yet and a want that already
			// has its music, so it is asked rather than assumed. How long it has
			// been waiting comes back with the answer, because a wait that never
			// ends is a different thing from a wait.
			var since pgtype.Timestamptz
			if err := tx.QueryRow(ctx, `
				SELECT min(decided_at)
				FROM acquisition_target_files
				WHERE acquisition_target_id = $1
				  AND verdict = 'accepted'
				  AND library_file_id IS NULL
				  -- A copy whose file somebody deleted has no library row coming.
				  -- Without this it reads as one the scan has not reached, and the
				  -- want says a copy is being taken into the library for as long as
				  -- the backstop allows, then stops without ever looking again.
				  AND file_removed_at IS NULL
			`, targetID).Scan(&since); err != nil {
				return err
			}
			outcome.Awaiting = since.Valid
			outcome.AwaitingSince = since.Time
			return nil
		}
		if err != nil {
			return err
		}

		var decoded AcquiredFileEvidence
		if len(evidence) > 0 {
			if err := json.Unmarshal(evidence, &decoded); err != nil {
				return fmt.Errorf("decode the evidence that admitted %s: %w", fileID, err)
			}
		}
		if decoded.Source != nil {
			return completeSourceCopy(ctx, tx, sourceCopy{
				targetID: targetID, offerID: offerID, fileID: fileID,
				evidence: decoded, proven: proven, byHand: decidedBy == "user",
				summary: summary, detail: detail,
			}, &outcome)
		}
		recordingID, releaseGroupID, err := acquiredRecording(ctx, tx, targetID)
		if err != nil {
			return err
		}

		// The file that admitted this one, where the library itself was the proof.
		// It is written with the identity rather than beside it, because
		// withdrawing the older decision has to be able to find everything that
		// rested on it — see WithdrawIdentitiesAdmittedBy.
		var admittedBy *uuid.UUID
		var admittedProof *string
		// A copy a person accepted rests on the person and on nobody else, so it
		// records no debt to a library file even where one happened to agree.
		// Written otherwise, withdrawing that file's decision would take back
		// somebody's own answer, and a manual decision is permanent.
		if decidedBy != "user" && decoded.Witness != nil &&
			strings.HasPrefix(decoded.Method, "library-witness") {
			if ancestor, err := uuid.Parse(decoded.Witness.FileID); err == nil {
				admittedBy = &ancestor
			}
			proof := decoded.Witness.Proof
			admittedProof = &proof
		}

		if _, err := tx.Exec(ctx, `
			INSERT INTO library_file_identities (
				library_file_id, kind, musicbrainz_recording_id,
				musicbrainz_release_group_id, artist_name, release_title, track_title,
				duration_ms, isrc, method, confidence, is_manual, summary, evidence,
				admitted_by_file_id, admitted_by_proof
			)
			VALUES (
				$1, 'external', $2, $3, nullif($4, ''), nullif($5, ''), nullif($6, ''),
				nullif($7, 0), nullif($8, ''), $9, $10, $13, $11, $12::jsonb, $14, $15
			)
			ON CONFLICT (library_file_id) DO NOTHING
		`,
			fileID, recordingID, releaseGroupID,
			wantedOrEmpty(decoded.Wanted, func(tags ImportTags) string { return tags.Artist }),
			wantedOrEmpty(decoded.Wanted, func(tags ImportTags) string { return tags.Album }),
			wantedOrEmpty(decoded.Wanted, func(tags ImportTags) string { return tags.Title }),
			wantedDuration(decoded.Wanted),
			wantedOrEmpty(decoded.Wanted, func(tags ImportTags) string { return tags.ISRC }),
			decoded.Method, decoded.Confidence, proven, orEmptyJSON(decoded.Agrees),
			// A copy a person accepted is a manual decision, and manual decisions
			// are permanent. Written as anything else, the resolution job is
			// entitled to delete it -- its first act is to drop every identity
			// nobody decided by hand -- and it did: an accepted copy was settled
			// at 16:37:20 and read "no recording MusicBrainz knows of matches this
			// file" one second later, because the proof that admitted it had
			// already been thrown away and the file was left to its own tags.
			decidedBy == "user", admittedBy, admittedProof,
		); err != nil {
			return fmt.Errorf("record the identity of %s: %w", fileID, err)
		}

		// What the library now says this file is. It is read back rather than
		// assumed, because the insert above does nothing when an identity already
		// exists — including the decision that the file has no external identity
		// at all, which is a null recording and a disagreement like any other.
		var stored uuid.NullUUID
		if err := tx.QueryRow(ctx, `
			SELECT musicbrainz_recording_id FROM library_file_identities
			WHERE library_file_id = $1
		`, fileID).Scan(&stored); err != nil {
			return err
		}

		if _, err := tx.Exec(ctx, `
			UPDATE acquisition_target_files
			SET library_file_id = $2, updated_at = now()
			WHERE id = $1
		`, offerID, fileID); err != nil {
			return err
		}
		outcome.LibraryFileID = uuid.NullUUID{UUID: fileID, Valid: true}

		if !stored.Valid || stored.UUID != recordingID {
			// The want stops looking. It keeps its place — still pending, still
			// wanted — and simply stops being due, because everything it could do
			// next is wrong: settling would assert that two recordings are one,
			// and searching would fetch another copy of music already on the disc.
			//
			// That is what this branch used to do. It left the want due again
			// immediately, and by the following turn nothing could see the copy
			// any more: the accepted row now carries a library file id, and the
			// library answers for the recording the file's own tag named rather
			// than the one the want asked for. One want fetched twelve copies of
			// the same song in an hour that way, because every peer's copy carried
			// the same tag and every pass reached the same conclusion (#368).
			//
			// A person decides from here. The queue says so, and both ways back
			// are theirs: stop looking, or match that file to the track by hand,
			// which is an answer the next pass settles the want against.
			outcome.Disagreed = true
			// The release is asked for exactly as it is when a copy settles.
			// Matching the file by hand means picking a catalogue track, and the
			// catalogue holds no track for a release nobody has ingested — so
			// without this the way back that reads best on screen leads to a
			// search that finds nothing, and the only real answer left is to stop
			// looking. It is the same guarded insert: a release already held is
			// not fetched again, and nothing here decides anything about the file.
			if err := queueAcquiredReleaseGroup(ctx, tx, releaseGroupID); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `
				UPDATE acquisition_targets
				SET summary = $2, next_attempt_at = NULL, updated_at = now()
				WHERE id = $1 AND status = 'pending'
			`, targetID, summary)
			return err
		}

		// The file is answered, so it leaves the queue of files nobody has asked
		// about. Without this the ordinary resolution job would run on it, and its
		// first act is to delete every identity nobody decided by hand — which
		// would throw away the one proof that admitted the copy and let the peer's
		// own tags decide what the file is, which is exactly what ADR 0002 forbids.
		if _, err := tx.Exec(ctx, `
			UPDATE library_files
			SET resolution_status = 'resolved', resolution_summary = $2,
			    resolution_error = NULL, resolution_attempted_at = now(), updated_at = now()
			WHERE id = $1
		`, fileID, proven); err != nil {
			return fmt.Errorf("record that %s is answered: %w", fileID, err)
		}
		if err := queueAcquiredReleaseGroup(ctx, tx, releaseGroupID); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `
			UPDATE acquisition_targets
			SET status = 'acquired', acquired_library_file_id = $2, acquired_at = now(),
			    summary = $3, next_attempt_at = NULL, last_error = NULL,
			    last_attempt_at = now(), updated_at = now()
			WHERE id = $1 AND status = 'pending'
		`, targetID, fileID, detail)
		if err != nil {
			return err
		}
		outcome.Settled = tag.RowsAffected() == 1
		return nil
	})
	return outcome, err
}

// sourceCopy is an accepted copy of a want with no recording, ready to be
// written its source identity.
type sourceCopy struct {
	targetID, offerID, fileID uuid.UUID
	evidence                  AcquiredFileEvidence
	proven                    string
	byHand                    bool
	summary, detail           string
}

// completeSourceCopy gives the library file a copy became the source identity
// its want's anchor names, and settles the want as acquired with no recording
// (ADR 0037 §4, §5).
//
// A file the resolver has already answered with "no provider knows this" gives
// way: that answer is silence, and the anchor proved the audio. Any other
// identity already on the file stays, and the want stops for a person, as it
// does on the recording path.
func completeSourceCopy(
	ctx context.Context, tx pgx.Tx, accepted sourceCopy, outcome *AcquiredFileOutcome,
) error {
	key := accepted.evidence.Source
	method := accepted.evidence.Method
	if accepted.byHand {
		method = "manual"
	}
	if method == "" {
		return fmt.Errorf("the accepted accepted of want %s records nothing that admitted it", accepted.targetID)
	}
	proof := append([]string{}, accepted.evidence.Agrees...)
	if anchor := accepted.evidence.Anchor; anchor != nil {
		proof = append(proof, fmt.Sprintf("anchor: %s %s", anchor.Source, anchor.Reference))
		if anchor.Measured {
			proof = append(proof, fmt.Sprintf("bit error rate against the anchor: %.4f", anchor.Rate))
		}
	}
	encoded, err := json.Marshal(proof)
	if err != nil {
		return fmt.Errorf("encode the proof of %s: %w", accepted.fileID, err)
	}
	var confidence any
	if accepted.evidence.Confidence > 0 {
		confidence = accepted.evidence.Confidence
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO library_file_identities (
			library_file_id, kind, provider, source, external_id, external_url,
			method, confidence, is_manual, summary, evidence
		)
		VALUES ($1, 'source', $2, $2, $3, $4, $5, $6, $7, $8, $9::jsonb)
		ON CONFLICT (library_file_id) DO UPDATE
		SET kind = 'source', provider = EXCLUDED.provider, source = EXCLUDED.source,
		    external_id = EXCLUDED.external_id, external_url = EXCLUDED.external_url,
		    musicbrainz_release_group_id = NULL, artist_name = NULL,
		    release_title = NULL, track_title = NULL, duration_ms = NULL, isrc = NULL,
		    method = EXCLUDED.method, confidence = EXCLUDED.confidence,
		    is_manual = EXCLUDED.is_manual, summary = EXCLUDED.summary,
		    evidence = EXCLUDED.evidence, decided_at = now(), updated_at = now()
		WHERE library_file_identities.kind = 'local_only'
		  AND NOT library_file_identities.is_manual
	`, accepted.fileID, key.Source, key.ExternalID, key.ExternalURL, method, confidence,
		accepted.byHand, accepted.proven, encoded); err != nil {
		return fmt.Errorf("record %s as %s: %w", accepted.fileID, key.ExternalID, err)
	}

	var stored string
	if err := tx.QueryRow(ctx, `
		SELECT coalesce(external_id, '') FROM library_file_identities
		WHERE library_file_id = $1 AND kind = 'source'
	`, accepted.fileID).Scan(&stored); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE acquisition_target_files
		SET library_file_id = $2, updated_at = now()
		WHERE id = $1
	`, accepted.offerID, accepted.fileID); err != nil {
		return err
	}
	outcome.LibraryFileID = uuid.NullUUID{UUID: accepted.fileID, Valid: true}

	if stored != key.ExternalID {
		outcome.Disagreed = true
		_, err := tx.Exec(ctx, `
			UPDATE acquisition_targets
			SET summary = $2, `+scheduleLook("NULL::timestamptz")+`, updated_at = now()
			WHERE id = $1 AND `+searchedWant("")+`
		`, accepted.targetID, accepted.summary)
		return err
	}

	// The scan queue asks only about 'pending' and 'failed' files, so this is
	// what keeps the resolver from replacing the proof with the peer's tags.
	if _, err := tx.Exec(ctx, `
		UPDATE library_files
		SET resolution_status = 'source', resolution_summary = $2,
		    resolution_error = NULL, resolution_attempted_at = now(), updated_at = now()
		WHERE id = $1
	`, accepted.fileID, accepted.proven); err != nil {
		return fmt.Errorf("record that %s is answered: %w", accepted.fileID, err)
	}
	tag, err := tx.Exec(ctx, `
		UPDATE acquisition_targets
		SET status = 'acquired', acquired_library_file_id = $2, acquired_at = now(),
		    summary = $3, next_attempt_at = NULL, next_search_at = NULL,
		    last_error = NULL, last_attempt_at = now(), updated_at = now()
		WHERE id = $1 AND status = 'unresolved' AND musicbrainz_recording_id IS NULL
	`, accepted.targetID, accepted.fileID, accepted.detail)
	if err != nil {
		return err
	}
	outcome.Settled = tag.RowsAffected() == 1
	if outcome.Settled {
		return nil
	}
	// The want was resolved while its accepted was being imported. The file proves
	// the anchor and says nothing about that recording, so the want stops for
	// a person instead of settling on it.
	outcome.Disagreed = true
	_, err = tx.Exec(ctx, `
		UPDATE acquisition_targets
		SET summary = $2, next_attempt_at = NULL, updated_at = now()
		WHERE id = $1 AND status = 'pending'
	`, accepted.targetID, accepted.summary)
	return err
}

// acquiredRecording reads what the want names, so the identity written for its
// copy is the recording that was verified rather than anything the file said.
func acquiredRecording(
	ctx context.Context, tx pgx.Tx, targetID uuid.UUID,
) (uuid.UUID, uuid.NullUUID, error) {
	var (
		recordingID    uuid.NullUUID
		releaseGroupID uuid.NullUUID
	)
	if err := tx.QueryRow(ctx, `
		SELECT musicbrainz_recording_id, musicbrainz_release_group_id
		FROM acquisition_targets WHERE id = $1
	`, targetID).Scan(&recordingID, &releaseGroupID); err != nil {
		return uuid.Nil, uuid.NullUUID{}, err
	}
	if !recordingID.Valid {
		return uuid.Nil, uuid.NullUUID{}, fmt.Errorf(
			"want %s no longer names a recording", targetID)
	}
	return recordingID.UUID, releaseGroupID, nil
}

// queueAcquiredReleaseGroup asks for the release behind an acquired copy, so the
// file has a track to be mapped to. It is the same guarded insert resolution
// uses: a release the catalogue already holds is not fetched again.
func queueAcquiredReleaseGroup(ctx context.Context, tx pgx.Tx, releaseGroupID uuid.NullUUID) error {
	if !releaseGroupID.Valid {
		return nil
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO jobs (kind, payload)
		SELECT 'ingest_release_group', jsonb_build_object('releaseGroupId', $1::text)
		WHERE NOT EXISTS (
		    SELECT 1 FROM albums WHERE albums.musicbrainz_release_group_id = $1::uuid
		)
		ON CONFLICT (kind, (payload->>'releaseGroupId'))
		    WHERE kind = 'ingest_release_group' AND status IN ('queued', 'running')
		DO NOTHING
	`, releaseGroupID.UUID.String())
	if err != nil {
		return fmt.Errorf("queue release group ingest %s: %w", releaseGroupID.UUID, err)
	}
	return nil
}

func wantedOrEmpty(tags *ImportTags, read func(ImportTags) string) string {
	if tags == nil {
		return ""
	}
	return read(*tags)
}

func wantedDuration(tags *ImportTags) int {
	if tags == nil {
		return 0
	}
	return tags.DurationMS
}

func orEmptyJSON(values []string) []byte {
	if values == nil {
		values = []string{}
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return []byte("[]")
	}
	return encoded
}

// WaitingQuestionsRow is how much of the review queue is waiting on a person,
// counted in the two shapes worth telling somebody about.
type WaitingQuestionsRow struct {
	// HeldCopies is wants whose fetched copy nothing could identify. The count
	// is of wants rather than of files: three copies of one song held is one
	// question, and saying three would make a slow night sound like a crisis.
	HeldCopies int64
	// PausedImports is whole-release imports that stopped and asked.
	PausedImports int64
}

// WaitingQuestions counts the two questions a notification is sent about.
//
// It is deliberately not the whole review queue. The queue also holds entries
// that fit more than one recording, which a person answers when they get to
// them rather than a moment where work has stopped and is waiting. The two
// counted here are the ones where the zero-false-positive machinery has a file
// in its hands and cannot proceed.
//
// The held half is the same selection the queue itself reads, so a count and the
// screen it describes cannot disagree.
func (q *Queries) WaitingQuestions(ctx context.Context) (WaitingQuestionsRow, error) {
	var row WaitingQuestionsRow
	err := q.db.QueryRow(ctx, `
		WITH`+reviewQuestions+`
		SELECT
			(SELECT count(*) FROM held),
			(SELECT count(*) FROM download_requests WHERE `+downloadViewReview+`)
	`).Scan(&row.HeldCopies, &row.PausedImports)
	if err != nil {
		return WaitingQuestionsRow{}, fmt.Errorf("count the questions waiting for a person: %w", err)
	}
	return row, nil
}

// QueueJudgeCopiesAgain asks for one pass over the copies already decided about.
//
// A pass already waiting or running is left alone and this adds nothing, so
// pressing twice does not queue two. It is deliberately not the conflict-index
// shape the scheduled sweeps use: this pass is asked for by a person, twice in a
// day at most, and a second pass would in any case skip every want the first one
// answered.
func (q *Queries) QueueJudgeCopiesAgain(ctx context.Context, runAfter time.Time) error {
	_, err := q.db.Exec(ctx, `
		INSERT INTO jobs (kind, payload, max_attempts, run_after)
		SELECT 'judge_copies_again', '{}'::jsonb, 1, $1
		WHERE NOT EXISTS (
			SELECT 1 FROM jobs
			WHERE kind = 'judge_copies_again' AND status IN ('queued', 'running')
		)
	`, runAfter)
	if err != nil {
		return fmt.Errorf("ask for a pass over the copies already decided about: %w", err)
	}
	return nil
}

// QueueWantCopyJudging asks for one want's held copies to be measured against
// the anchor that has just landed on it (ADR 0029 §5).
//
// Nobody presses this. An anchor arriving is a new witness for copies that were
// already questions, and leaving them as questions until somebody asked for a
// whole-collection pass would waste the witness.
//
// A pass already waiting or running for this want adds nothing, so a want
// anchored twice does not queue two.
func (q *Queries) QueueWantCopyJudging(
	ctx context.Context, targetID uuid.UUID, runAfter time.Time,
) error {
	payload, err := json.Marshal(map[string]string{"acquisition_target_id": targetID.String()})
	if err != nil {
		return fmt.Errorf("write the judging payload for want %s: %w", targetID, err)
	}
	if _, err := q.db.Exec(
		ctx, queueWantCopyJudging, runAfter, payload, targetID.String(),
	); err != nil {
		return fmt.Errorf("ask for the copies of want %s to be judged: %w", targetID, err)
	}
	return nil
}

// queueWantCopyJudging is QueueWantCopyJudging's statement, kept apart because
// the recheck below runs it on its own transaction. One statement, so a want
// cannot be queued twice by one route and once by the other.
const queueWantCopyJudging = `
	WITH running AS (
		UPDATE jobs
		SET payload = payload || jsonb_build_object('rejudge_after', $1::timestamptz)
		WHERE kind = 'judge_want_copies'
		  AND status = 'running'
		  AND payload->>'acquisition_target_id' = $3
		  AND NOT EXISTS (
			  SELECT 1 FROM jobs queued
			  WHERE queued.kind = 'judge_want_copies'
			    AND queued.status = 'queued'
			    AND queued.payload->>'acquisition_target_id' = $3
		  )
		RETURNING id
	)
	INSERT INTO jobs (kind, payload, max_attempts, run_after)
	SELECT 'judge_want_copies', $2::jsonb, 3, $1
	WHERE NOT EXISTS (SELECT 1 FROM running)
	  AND NOT EXISTS (
		SELECT 1 FROM jobs
		WHERE kind = 'judge_want_copies'
		  AND status IN ('queued', 'running')
		  AND payload->>'acquisition_target_id' = $3
	  )
`

// What a want in the review queue is waiting for, when it is waiting for
// anything. A want with neither is a question for a person: the evidence is in
// and it contradicts, or nothing more can arrive.
const (
	// RecheckForMusicBrainz is a copy whose audio named another MusicBrainz row
	// and MusicBrainz could not be asked whether the two rows are one track
	// entered twice. The provider may answer tomorrow.
	RecheckForMusicBrainz = "musicbrainz"
	// RecheckForLibraryIdentity is a copy that was proven, imported, and became
	// a library file the library calls a different recording. What the file is
	// may still change: the resolution may be run again, a person may decide, or
	// the decision it borrowed may be withdrawn.
	RecheckForLibraryIdentity = "library_identity"
)

// recheckSchedule is how long a want waiting on MusicBrainz waits before its
// copies go back through the grader, one entry per attempt.
//
// An hour covers an outage that has already cleared. A day covers one that has
// not. Then it runs out, because a want that has asked four times over two days
// and still cannot be told is not waiting for anything a clock will bring. Until
// this existed the ask repeated every thirty minutes for ever (migration 00089
// queued the first round), which is a want that never becomes a question and a
// decode of the same audio forty-eight times a day.
var recheckSchedule = []time.Duration{
	time.Hour, 6 * time.Hour, 24 * time.Hour, 24 * time.Hour,
}

// RecheckAfterMusicBrainzSilence records that a want is holding a copy nobody
// could answer because MusicBrainz could not be asked, and queues the next
// re-judge when the want has one left.
//
// It reports whether a re-judge was queued. False means the schedule is spent:
// the want has stopped waiting for evidence, it is a question for a person, and
// spentSummary is what it now says about itself.
//
// The re-judge admits nothing a fresh copy could not. It is the same
// judge_want_copies job the anchor sweep queues, which puts the copies back
// through ImportOne, which is the call a copy gets the moment it arrives.
func (q *Queries) RecheckAfterMusicBrainzSilence(
	ctx context.Context, targetID uuid.UUID, now time.Time, spentSummary string,
) (bool, error) {
	payload, err := json.Marshal(map[string]string{"acquisition_target_id": targetID.String()})
	if err != nil {
		return false, fmt.Errorf("write the judging payload for want %s: %w", targetID, err)
	}
	var queued bool
	err = q.inTransaction(ctx, "scheduling a want's next look at MusicBrainz", func(tx pgx.Tx) error {
		var (
			spent int32
			due   *time.Time
		)
		err := tx.QueryRow(ctx, `
			SELECT recheck_attempts, recheck_after FROM acquisition_targets
			WHERE id = $1 AND status = 'pending'
			FOR UPDATE
		`, targetID).Scan(&spent, &due)
		// The want stopped being pending while its copy was being judged.
		// Somebody answered it, and their answer is not overwritten with a
		// schedule.
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		// A want already waiting for its next look is not scheduled again, and
		// this is what counts one attempt per pass rather than one per copy. A
		// judging pass walks every held copy of a want, and a want holding five
		// copies of the same unanswerable question would otherwise spend the
		// whole schedule in a single pass. The same guard covers the ordinary
		// path: a second copy arriving while a look is pending is the same want
		// waiting on the same provider.
		if due != nil && due.After(now) {
			queued = true
			return nil
		}
		if int(spent) >= len(recheckSchedule) {
			// The attempts stay at the cap. They are what says why the want
			// stopped waiting, and the summary beside them is overwritten by
			// whatever this want does next.
			_, err := tx.Exec(ctx, `
				UPDATE acquisition_targets
				SET recheck_reason = NULL, recheck_after = NULL,
				    summary = $2, updated_at = now()
				WHERE id = $1 AND status = 'pending'
			`, targetID, spentSummary)
			return err
		}
		runAfter := now.Add(recheckSchedule[spent])
		if _, err := tx.Exec(ctx, `
			UPDATE acquisition_targets
			SET recheck_reason = $2, recheck_after = $3,
			    recheck_attempts = recheck_attempts + 1, updated_at = now()
			WHERE id = $1 AND status = 'pending'
		`, targetID, RecheckForMusicBrainz, runAfter); err != nil {
			return err
		}
		if _, err := tx.Exec(
			ctx, queueWantCopyJudging, runAfter, payload, targetID.String(),
		); err != nil {
			return err
		}
		queued = true
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("schedule the next look at want %s: %w", targetID, err)
	}
	return queued, nil
}

// EndTheMusicBrainzWaitIfAnswered takes the wait off a want that has no copy
// stopped on the registration question any more.
//
// It is asked after every copy of a want is settled, and it is the only way out
// of that wait other than the schedule running out. MusicBrainz recovering is
// what usually ends it: the copy goes back through the grader, the provider
// answers, and the copy is admitted, refused, or held for some other reason
// entirely — and the want is no longer waiting on a provider whatever of those
// happened.
//
// The condition is the state rather than the verdict, so a want holding several
// of these copies stops waiting when the last of them is answered and not when
// the first is. It is a no-op for a want whose copy is still stopped on that
// question, which is what makes it safe to ask after any decision at all.
//
// The attempts go back to zero. The wait is over, and a want that is asked this
// question again later starts with the patience a want asking it for the first
// time has.
func (q *Queries) EndTheMusicBrainzWaitIfAnswered(
	ctx context.Context, targetID uuid.UUID,
) error {
	_, err := q.db.Exec(ctx, `
		UPDATE acquisition_targets
		SET recheck_reason = NULL, recheck_after = NULL, recheck_attempts = 0,
		    updated_at = now()
		WHERE id = $1
		  AND recheck_reason = $2
		  AND NOT EXISTS (
		      SELECT 1 FROM acquisition_target_files still
		      WHERE still.acquisition_target_id = $1
		        AND still.verdict = 'held'
		        AND still.summary LIKE '`+unaskedRegistrationSummary+`%'
		  )
	`, targetID, RecheckForMusicBrainz)
	if err != nil {
		return fmt.Errorf("end the provider wait on want %s: %w", targetID, err)
	}
	return nil
}

// QueueJudgingForSettledWitness asks for the held copies of every want naming
// this recording to be judged again, now that the library holds a file proven to
// be it.
//
// It is called where the fact is written — a file's identity settling — because
// that is a witness those copies were judged without. A want whose recording the
// library still cannot speak for is skipped, so a resolution that concluded
// nothing conclusive queues nothing.
//
// Nothing is admitted here. The job puts each copy back through ImportOne, which
// compares it against the library's settled files exactly as it does for a copy
// arriving today.
func (q *Queries) QueueJudgingForSettledWitness(
	ctx context.Context, recordingID uuid.UUID, runAfter time.Time,
) error {
	if recordingID == uuid.Nil {
		return nil
	}
	_, err := q.db.Exec(ctx, `
		INSERT INTO jobs (kind, payload, max_attempts, run_after)
		SELECT 'judge_want_copies',
		       jsonb_build_object('acquisition_target_id', targets.id), 3, $3
		FROM acquisition_targets targets
		WHERE targets.status = 'pending'
		  AND targets.musicbrainz_recording_id = $1
		  AND EXISTS (
		      SELECT 1 FROM acquisition_target_files held
		      WHERE held.acquisition_target_id = targets.id
		        AND held.verdict = 'held'
		        AND held.decided_by = 'schall'
		  )
		  AND NOT EXISTS (
		      SELECT 1 FROM acquisition_target_files answered
		      WHERE answered.acquisition_target_id = targets.id
		        AND answered.verdict = 'accepted'
		  )
		  AND`+libraryCanSpeakForTheWant+`
		  AND NOT EXISTS (
		      SELECT 1 FROM jobs
		      WHERE kind = 'judge_want_copies'
		        AND status IN ('queued', 'running')
		        AND payload->>'acquisition_target_id' = targets.id::text
		  )
	`, recordingID, settledProofs, runAfter)
	if err != nil {
		return fmt.Errorf(
			"ask for the copies waiting on recording %s to be judged: %w", recordingID, err)
	}
	return nil
}

// queueJudgingForLearnedMerge asks for the held copies of the wants waiting on a
// registration question to be judged again, now that MusicBrainz has said these
// two identifiers are one recording.
//
// Either end of the merge is looked for. The question those copies wait on is
// whether the row the audio named and the row the want names are one track
// entered twice, and a redirect between them is that answer arriving.
//
// Two kinds of copy are waiting on it, and a merge answers both. One stopped
// because MusicBrainz could not be asked at all. The other was asked, was told
// the rows are alike, and was held anyway, because two rows of one name and one
// length are the catalogue's bookkeeping and not the music answering: those are
// found by the row their audio named, which is the other end of this merge.
//
// Nothing is decided here. Each copy goes back through ImportOne, where the
// provider now redirects one identifier to the other and the audio names the
// recording the want asked for.
func queueJudgingForLearnedMerge(
	ctx context.Context, db DBTX, retired, surviving uuid.UUID,
) error {
	_, err := db.Exec(ctx, `
		INSERT INTO jobs (kind, payload, max_attempts, run_after)
		SELECT 'judge_want_copies',
		       jsonb_build_object('acquisition_target_id', targets.id), 3, now()
		FROM acquisition_targets targets
		WHERE targets.status = 'pending'
		  AND targets.musicbrainz_recording_id IN ($1, $2)
		  AND EXISTS (
		      SELECT 1 FROM acquisition_target_files held
		      WHERE held.acquisition_target_id = targets.id
		        AND held.verdict = 'held'
		        AND held.decided_by = 'schall'
		        AND (held.summary LIKE '`+unaskedRegistrationSummary+`%'
		             OR held.evidence->'acoustic'->'recordingIds'
		                ?| array[$1::text, $2::text])
		  )
		  AND NOT EXISTS (
		      SELECT 1 FROM acquisition_target_files answered
		      WHERE answered.acquisition_target_id = targets.id
		        AND answered.verdict = 'accepted'
		  )
		  AND NOT EXISTS (
		      SELECT 1 FROM jobs
		      WHERE kind = 'judge_want_copies'
		        AND status IN ('queued', 'running')
		        AND payload->>'acquisition_target_id' = targets.id::text
		  )
	`, retired, surviving)
	if err != nil {
		return fmt.Errorf("ask for the copies waiting on the merge of %s to be judged: %w",
			retired, err)
	}
	return nil
}

// JudgeAgainRow is one copy a peer already sent that may be put through
// validation a second time: where its bytes are, which want it was fetched for,
// and the request that fetched it.
type JudgeAgainRow struct {
	CopyID              uuid.UUID
	AcquisitionTargetID uuid.UUID
	RequestID           uuid.UUID
	// SourceDirectory is the provider folder the copy was delivered into. It is
	// where the file is looked for, and it is read from the request rather than
	// from the copy because that is where validation has always read it.
	SourceDirectory string
	FileName        string
	Verdict         string
	// AudioFingerprint is what the copy's own audio was measured to be, kept on
	// the copy when it was judged. It is the only thing left to compare with once
	// the bytes have gone from the inbox.
	AudioFingerprint string
	// The want's anchor, so a copy with no bytes can still be measured against it
	// without a second query per copy.
	AnchorFingerprint string
	AnchorSource      string
	AnchorReference   string
	AnchorLabel       string
	AnchorViews       int64
	AnchorSeconds     int
}

// CopiesToJudgeAgain lists the copies whose verdict was reached by rules that
// have since changed, oldest decision first within each want.
//
// A verdict Schall reached is normally permanent, and re-opening one is not
// something the system does to itself. This exists because two rules changed
// under copies already decided: a want gained an audio anchor it did not have
// when its copies were judged, and an identification naming another MusicBrainz
// row of the same registered track stopped being read as a refusal
// (ADR 0024 and 0025). The copies are still on disk, so the honest
// answer is to put them through the same validation again rather than to leave
// wrong answers standing.
//
// Four things bound what is offered, and each of them is a way somebody could
// otherwise be overruled or a file could be imported twice:
//
//   - Only decisions Schall made. A copy somebody decided about by hand is
//     permanent and is never offered here.
//   - Only wants still looking for a copy, and only wants with no accepted copy
//     already. Validation imports what it proves without asking whether the want
//     was answered since, so a want that has its music must not be offered more.
//   - Only wants that hold an anchor. Without one nothing has changed for that
//     want and re-judging would reach the same answer at the cost of a decode.
//   - Only copies whose refusal could be the one that changed: a copy held for a
//     person, or one discarded because the audio was identified as another
//     recording. A copy the sample itself refused is left alone — nothing about
//     that has changed — as is one refused on its own tags.
func (q *Queries) CopiesToJudgeAgain(ctx context.Context) ([]JudgeAgainRow, error) {
	return q.copiesToJudgeAgain(ctx, uuid.NullUUID{})
}

// CopiesToJudgeAgainForWant is CopiesToJudgeAgain narrowed to one want. It is
// what a fact arriving for that want asks for: the copies of that want and of no
// other.
//
// It has one bound the whole-collection pass does not. A want whose recording
// the library now holds a settled file for is offered too, because that file is
// a witness its copies were judged without, and ImportOne compares every copy
// against the settled files for its recording. The pass a person presses keeps
// the narrower bounds: it walks every want in the queue, and offering it every
// copy of every want the library can now speak for would start a decode of most
// of the queue on one press.
func (q *Queries) CopiesToJudgeAgainForWant(
	ctx context.Context, targetID uuid.UUID,
) ([]JudgeAgainRow, error) {
	return q.copiesToJudgeAgain(ctx, uuid.NullUUID{UUID: targetID, Valid: true})
}

// unaskedRegistrationSummary is the opening of what a copy is told when its
// audio named another MusicBrainz row and MusicBrainz could not be asked whether
// the two rows are one track entered twice. The sentence itself belongs to
// downloads; this is the prefix the queries that look for those copies match on,
// written once so the three of them cannot drift apart. Migration 00089 carries
// a fourth copy of it, and an applied migration is never edited.
const unaskedRegistrationSummary = "The audio was identified as another recording, " +
	"and MusicBrainz could not be asked"

// libraryCanSpeakForTheWant is "the library holds a file proven to be the
// recording this want names", in the terms SettledWitnesses reads it: an
// external identity on a file that is still here and has a fingerprint, settled
// by one of the enumerated proofs. The proofs are passed in rather than written
// here, so there is one list (settledProofs) and not two.
//
// It reads targets.musicbrainz_recording_id and the proofs from $2.
const libraryCanSpeakForTheWant = `
	EXISTS (
	    SELECT 1 FROM library_files
	    JOIN library_file_identities
	        ON library_file_identities.library_file_id = library_files.id
	    WHERE library_files.missing_at IS NULL
	      AND library_files.resolution_status = 'resolved'
	      AND (
	          library_files.witness_fingerprint IS NOT NULL
	          OR coalesce(library_files.fingerprint, '') <> ''
	      )
	      AND library_file_identities.kind = 'external'
	      AND library_file_identities.musicbrainz_recording_id
	          = targets.musicbrainz_recording_id
	      AND (
	          library_file_identities.is_manual
	          OR regexp_replace(library_file_identities.method, '-and-release$', '')
	             = ANY ($2::text[])
	      )
	)
`

func (q *Queries) copiesToJudgeAgain(
	ctx context.Context, targetID uuid.NullUUID,
) ([]JudgeAgainRow, error) {
	rows, err := q.db.Query(ctx, `
		SELECT copies.id, copies.acquisition_target_id, requests.id,
		       requests.source_directory, copies.file_name, copies.verdict,
		       coalesce(copies.audio_fingerprint, ''),
		       coalesce(targets.anchor_fingerprint, ''),
		       coalesce(targets.anchor_source, ''),
		       coalesce(targets.anchor_reference, ''),
		       coalesce(targets.anchor_label, ''),
		       coalesce(targets.anchor_views, 0),
		       coalesce(targets.anchor_seconds, 0)
		FROM acquisition_target_files copies
		JOIN acquisition_targets targets ON targets.id = copies.acquisition_target_id
		JOIN download_requests requests ON requests.id = copies.download_request_id
		WHERE copies.decided_by = 'schall'
		  AND targets.status = 'pending'
		  AND (targets.anchor_fingerprint IS NOT NULL
		       OR copies.summary LIKE '`+unaskedRegistrationSummary+`%'
		       OR ($1::uuid IS NOT NULL AND`+libraryCanSpeakForTheWant+`))
		  AND requests.status = 'completed'
		  AND requests.import_status <> 'validating'
		  AND NOT EXISTS (
		      SELECT 1 FROM acquisition_target_files answered
		      WHERE answered.acquisition_target_id = targets.id
		        AND answered.verdict = 'accepted'
		  )
		  AND (
		      copies.verdict = 'held'
		      OR (
		          copies.verdict = 'discarded_audio'
		          AND jsonb_array_length(coalesce(
		              nullif(copies.evidence->'acoustic'->'recordingIds', 'null'::jsonb),
		              '[]'::jsonb)) > 0
		          AND coalesce(copies.evidence->'anchor'->>'agrees', '') <> 'false'
		      )
		  )
		  AND ($1::uuid IS NULL OR targets.id = $1)
		ORDER BY copies.acquisition_target_id, copies.decided_at, copies.id
	`, targetID, settledProofs)
	if err != nil {
		return nil, fmt.Errorf("list the copies worth judging again: %w", err)
	}
	defer rows.Close()

	judged := make([]JudgeAgainRow, 0)
	for rows.Next() {
		var row JudgeAgainRow
		if err := rows.Scan(
			&row.CopyID, &row.AcquisitionTargetID, &row.RequestID,
			&row.SourceDirectory, &row.FileName, &row.Verdict, &row.AudioFingerprint,
			&row.AnchorFingerprint, &row.AnchorSource, &row.AnchorReference,
			&row.AnchorLabel, &row.AnchorViews, &row.AnchorSeconds,
		); err != nil {
			return nil, err
		}
		judged = append(judged, row)
	}
	return judged, rows.Err()
}

// CopiesRefusedOnCreditAlone lists the copies Schall discarded because the
// artist tag was the one thing that disagreed, oldest decision first within each
// want.
//
// It exists for the same reason CopiesToJudgeAgain does and offers a different
// set. A credit is now compared as the set of artists it names, and a credit
// that disagrees no longer voids a pair the audio already identified
// (ADR 0030). Both changes bear on exactly these copies: 308 of them
// were refused on the artist tag alone, and the audio had already named the
// wanted recording in 110.
//
// The bounds are the ones judging again always has, with two of its own:
//
//   - Only the copies whose refusal was that one. The evidence has to say the
//     artist and nothing else disagreed, which is the same test that stopped the
//     want in StopAfterCreditOnlyRefusals.
//   - Only copies whose bytes are still here. A copy whose file was removed
//     cannot be imported by anything, and re-opening it would overwrite a
//     verdict with a wrong account of why nobody can decide.
//
// A want awaiting review is offered as well as a pending one. The copies of a
// stopped want are what somebody was going to be asked about, and answering them
// without asking is the point.
func (q *Queries) CopiesRefusedOnCreditAlone(ctx context.Context) ([]JudgeAgainRow, error) {
	rows, err := q.db.Query(ctx, `
		SELECT copies.id, copies.acquisition_target_id, requests.id,
		       requests.source_directory, copies.file_name, copies.verdict
		FROM acquisition_target_files copies
		JOIN acquisition_targets targets ON targets.id = copies.acquisition_target_id
		JOIN download_requests requests ON requests.id = copies.download_request_id
		WHERE copies.decided_by = 'schall'
		  AND copies.verdict = 'discarded_tags'
		  AND copies.evidence->'differs' = '["artist"]'::jsonb
		  AND copies.file_removed_at IS NULL
		  AND targets.status IN ('pending', 'awaiting_review')
		  AND requests.status = 'completed'
		  AND requests.import_status <> 'validating'
		  AND NOT EXISTS (
		      SELECT 1 FROM acquisition_target_files answered
		      WHERE answered.acquisition_target_id = targets.id
		        AND answered.verdict = 'accepted'
		  )
		ORDER BY copies.acquisition_target_id, copies.decided_at, copies.id
	`)
	if err != nil {
		return nil, fmt.Errorf("list the copies refused on the artist credit alone: %w", err)
	}
	defer rows.Close()

	judged := make([]JudgeAgainRow, 0)
	for rows.Next() {
		var row JudgeAgainRow
		if err := rows.Scan(
			&row.CopyID, &row.AcquisitionTargetID, &row.RequestID,
			&row.SourceDirectory, &row.FileName, &row.Verdict,
		); err != nil {
			return nil, err
		}
		judged = append(judged, row)
	}
	return judged, rows.Err()
}

// WantHasAcceptedCopy reports a want that already has a copy Schall or somebody
// let in.
//
// It is asked again between copies rather than trusted from the listing, because
// one pass may import a copy for a want whose other copies it is still holding.
// Validation imports whatever it proves; nothing in it asks whether the music
// arrived a moment ago, so this is what stops a want with fifteen copies taking
// fifteen of them.
func (q *Queries) WantHasAcceptedCopy(ctx context.Context, targetID uuid.UUID) (bool, error) {
	var found bool
	err := q.db.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM acquisition_target_files
			WHERE acquisition_target_id = $1 AND verdict = 'accepted'
		)
	`, targetID).Scan(&found)
	if err != nil {
		return false, fmt.Errorf("read whether want %s already has a copy: %w", targetID, err)
	}
	return found, nil
}

// ErrCopyNotReopened reports a copy that could not be put back into validation.
// The request settled some other way, or another pass took it first, and either
// way this copy is not this pass's to judge.
var ErrCopyNotReopened = errors.New("this copy could not be put back into validation")

// ReopenCopyForJudging puts one settled request back into the queue validation
// claims from.
//
// Nothing else is touched. The request's own status stays completed — the bytes
// did arrive and that is still true — and the copy's recorded verdict stays
// exactly as it is until validation reaches a new one and overwrites it. So a
// pass that stops halfway leaves every copy it did not reach saying what it
// always said.
func (q *Queries) ReopenCopyForJudging(ctx context.Context, requestID uuid.UUID) error {
	tag, err := q.db.Exec(ctx, `
		UPDATE download_requests
		SET import_status = 'pending', import_error = NULL, updated_at = now()
		WHERE id = $1 AND status = 'completed' AND import_status <> 'validating'
	`, requestID)
	if err != nil {
		return fmt.Errorf("put request %s back into validation: %w", requestID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: request %s", ErrCopyNotReopened, requestID)
	}
	return nil
}

// ReleaseCopyFromJudging puts a request back where it was before a judging pass
// claimed it, after the pass failed to reach a verdict.
//
// Validation's own retry re-claims a request left at 'validating', but a judging
// pass has no retry: its job completes whatever became of each copy. A request
// left at 'validating' by such a pass is then read as a copy still being checked,
// which hides the copy from the review queue and, through OpenTargetRequest,
// stops the want from fetching another. 105 requests sat that way on 2026-09-05.
// The recorded verdict says where the request belongs: a held copy is a question
// again, anything else was discarded.
func (q *Queries) ReleaseCopyFromJudging(ctx context.Context, requestID uuid.UUID) error {
	_, err := q.db.Exec(ctx, `
		UPDATE download_requests requests
		SET import_status = CASE WHEN copies.verdict = 'held' THEN 'needs_review' ELSE 'discarded' END,
		    updated_at = now()
		FROM acquisition_target_files copies
		WHERE requests.id = $1
		  AND copies.download_request_id = requests.id
		  AND copies.verdict IN ('held', 'discarded_audio', 'discarded_tags', 'undelivered')
		  AND requests.status = 'completed'
		  AND requests.import_status = 'validating'
	`, requestID)
	if err != nil {
		return fmt.Errorf("release request %s from judging: %w", requestID, err)
	}
	return nil
}

// UnfingerprintedCopyRow is one held copy whose audio has never been measured:
// where its bytes are, and which copy row to write the measurement back to.
//
// SourceDirectory is the provider folder the copy was delivered into. It is read
// from the request rather than from the copy, because that is where validation
// has always read it, and a second derivation that disagreed by one component
// would look in a folder that is not there.
type UnfingerprintedCopyRow struct {
	CopyID              uuid.UUID
	AcquisitionTargetID uuid.UUID
	SourceDirectory     string
	FileName            string
}

// CopiesMissingFingerprint lists the held copies with no fingerprint on record,
// oldest decision first.
//
// A held copy is one a peer sent that nothing could decide about, kept as a
// question for a person. Every copy fetched from now on is fingerprinted as it
// is judged; these are the ones judged before there was a column to keep the
// number in, and their files are still in the download inbox.
//
// Nothing about a copy changes by being measured. The fingerprint is read by the
// review queue to show the copies of one piece of audio as one row, and by
// nothing that admits or refuses anything.
func (q *Queries) CopiesMissingFingerprint(
	ctx context.Context, limit int32,
) ([]UnfingerprintedCopyRow, error) {
	rows, err := q.db.Query(ctx, `
		SELECT copies.id, copies.acquisition_target_id,
		       requests.source_directory, copies.file_name
		FROM acquisition_target_files copies
		JOIN download_requests requests ON requests.id = copies.download_request_id
		WHERE copies.verdict = 'held'
		  AND copies.audio_fingerprint IS NULL
		  AND requests.status = 'completed'
		ORDER BY copies.decided_at, copies.id
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("read the copies with no fingerprint: %w", err)
	}
	defer rows.Close()

	waiting := make([]UnfingerprintedCopyRow, 0, limit)
	for rows.Next() {
		var row UnfingerprintedCopyRow
		if err := rows.Scan(
			&row.CopyID, &row.AcquisitionTargetID, &row.SourceDirectory, &row.FileName,
		); err != nil {
			return nil, err
		}
		waiting = append(waiting, row)
	}
	return waiting, rows.Err()
}

// RecordCopyFingerprint writes down what one copy's audio sounds like.
//
// It writes the two columns and nothing else: not the verdict, not the summary,
// not the evidence a person is reading. A measurement is not a decision, and a
// pass that ran while somebody was answering the queue must not move what they
// are answering.
func (q *Queries) RecordCopyFingerprint(
	ctx context.Context, copyID uuid.UUID, fingerprint string, seconds int,
) error {
	if fingerprint == "" || seconds <= 0 {
		return fmt.Errorf("copy %s: a fingerprint of nothing is not recorded", copyID)
	}
	_, err := q.db.Exec(ctx, `
		UPDATE acquisition_target_files
		SET audio_fingerprint = $2, audio_fingerprint_seconds = $3, updated_at = now()
		WHERE id = $1
	`, copyID, fingerprint, seconds)
	if err != nil {
		return fmt.Errorf("record the fingerprint of copy %s: %w", copyID, err)
	}
	return nil
}

// RecordCopyWaveform writes down the shape of one copy's audio.
//
// Like the fingerprint beside it, it writes one column and nothing else. A
// picture is not a decision, and a card that drew itself while somebody was
// answering the queue must not move what they are answering.
func (q *Queries) RecordCopyWaveform(ctx context.Context, copyID uuid.UUID, peaks []byte) error {
	if len(peaks) == 0 {
		return fmt.Errorf("copy %s: a waveform of nothing is not recorded", copyID)
	}
	_, err := q.db.Exec(ctx, `
		UPDATE acquisition_target_files
		SET waveform = $2, updated_at = now()
		WHERE id = $1
	`, copyID, peaks)
	if err != nil {
		return fmt.Errorf("record the waveform of copy %s: %w", copyID, err)
	}
	return nil
}

// RecordCopyAnchorReading writes onto a held copy what its stored fingerprint
// came to beside the want's anchor.
//
// It is for the copies whose bytes have gone from the inbox. Those cannot be put
// back through validation — validation reads a file — and they cannot be
// imported either, so the verdict is deliberately left exactly as it is and only
// the measurement and the sentence under it are written. A copy that turns out
// to hold the wanted recording is still a question, and the sentence is what
// tells the person which question it is.
//
// Only a held copy Schall itself decided about is touched. A copy somebody
// decided by hand is permanent, and a copy that was admitted or refused has an
// account of itself that this must not overwrite.
func (q *Queries) RecordCopyAnchorReading(
	ctx context.Context, copyID uuid.UUID, anchor ImportAnchor, summary string,
) error {
	written, err := json.Marshal(anchor)
	if err != nil {
		return fmt.Errorf("write the anchor reading of copy %s: %w", copyID, err)
	}
	_, err = q.db.Exec(ctx, `
		UPDATE acquisition_target_files
		SET evidence = jsonb_set(
		        coalesce(evidence, '{}'::jsonb), '{anchor}', $2::jsonb, true),
		    summary = $3,
		    updated_at = now()
		WHERE id = $1 AND verdict = 'held' AND decided_by = 'schall'
	`, copyID, written, summary)
	if err != nil {
		return fmt.Errorf("record the anchor reading of copy %s: %w", copyID, err)
	}
	return nil
}
