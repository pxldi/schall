package matching

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/db"
	"github.com/rs/zerolog"
)

var ErrManualConflict = errors.New("track is already manually matched to another file")

type Service struct {
	pool   *pgxpool.Pool
	logger zerolog.Logger
}

type Candidate struct {
	TrackID        uuid.UUID
	Title          string
	AlbumTitle     string
	ArtistName     string
	DiscNumber     int32
	TrackNumber    *int32
	Method         string
	Confidence     float32
	AlreadyMapped  bool
	ManuallyMapped bool
	// HeldBy is the file this track already belongs to, when that is some other
	// file. Taking a track means taking it away from whatever holds it, and a
	// person cannot weigh that without being told what they would be taking it
	// from — most of the time it is the same music twice. It is read alongside
	// the candidates and decides nothing.
	HeldByFileID uuid.NullUUID
	HeldByPath   string
}

type candidateMatch struct {
	trackID    uuid.UUID
	method     string
	confidence float32
	priority   int
	fileCount  int
	available  bool
	// heldByHand is a mapping somebody made, on another file. It is apart from
	// available because the two are answered differently: a track another file
	// merely claims is contested and both files are asked about, and a track
	// another file was given is settled and this one is a second copy.
	heldByHand bool
}

// NewService takes a logger for the one thing here that is allowed to fail
// quietly: the request to describe a file it has just matched. See queueTagging.
func NewService(pool *pgxpool.Pool, logger zerolog.Logger) *Service {
	return &Service{pool: pool, logger: logger}
}

func (service *Service) ReconcileFile(ctx context.Context, fileID uuid.UUID) error {
	return service.reconcileFile(ctx, fileID, true)
}

func (service *Service) reconcileFile(ctx context.Context, fileID uuid.UUID, cascade bool) error {
	transaction, err := service.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	var missing bool
	var manual bool
	// A file resolved to 'source' carries a person's decision that it has no
	// MusicBrainz recording at all — the same standing as a manual mapping.
	// A manual local_only identity carries the same decision, while an automatic
	// local_only outcome remains eligible for matching.
	// Tags catalogue matching can act on (title, uploader-as-artist) reach the
	// file from Adopt, and a scan that let them satisfy an exact-tag
	// combination would create a mapping the person's decision already ruled
	// out, and the tagging pass would then overwrite the SoundCloud tags with
	// catalogue ones. So a source identity holds this branch exactly like
	// is_manual does: reconcile leaves it alone.
	err = transaction.QueryRow(ctx, `
		SELECT library_files.missing_at IS NOT NULL,
		       library_files.resolution_status = 'source'
		       OR EXISTS (
		           SELECT 1 FROM track_mappings
		           WHERE track_mappings.library_file_id = library_files.id
		             AND track_mappings.is_manual
		       ) OR EXISTS (
		           SELECT 1 FROM library_file_identities
		           WHERE library_file_identities.library_file_id = library_files.id
		             AND library_file_identities.kind = 'local_only'
		             AND library_file_identities.is_manual
		       )
		FROM library_files
		WHERE library_files.id = $1
		FOR UPDATE
	`, fileID).Scan(&missing, &manual)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var peerIDs []uuid.UUID
	if cascade {
		peerIDs, err = peersOf(ctx, transaction, fileID)
		if err != nil {
			return err
		}
	}
	if manual {
		if err := transaction.Commit(ctx); err != nil {
			return err
		}
		return service.reconcilePeers(ctx, peerIDs)
	}
	if missing {
		if _, err := transaction.Exec(ctx, `
			DELETE FROM track_mappings
			WHERE library_file_id = $1 AND NOT is_manual
		`, fileID); err != nil {
			return err
		}
		if _, err := transaction.Exec(ctx, `
			UPDATE library_files
			SET match_status = 'unmatched', match_candidate_count = 0, updated_at = now()
			WHERE id = $1
		`, fileID); err != nil {
			return err
		}
		if err := transaction.Commit(ctx); err != nil {
			return err
		}
		return service.reconcilePeers(ctx, peerIDs)
	}

	// The tracks the file already answers, read before the rows below replace
	// them. Every reconcile deletes and reinserts the same mappings, so the rows
	// cannot say whether anything about the file changed and only the tracks they
	// name can. See queueTagging.
	held, err := mappedTracks(ctx, transaction, fileID)
	if err != nil {
		return err
	}

	if _, err := transaction.Exec(ctx, `
		DELETE FROM track_mappings
		WHERE library_file_id = $1 AND NOT is_manual
	`, fileID); err != nil {
		return err
	}

	candidates, err := automaticCandidates(ctx, transaction, fileID)
	if err != nil {
		return err
	}
	status := "unmatched"
	candidateCount := 0
	gained := false
	if len(candidates) > 0 {
		topPriority := candidates[0].priority
		top := make([]candidateMatch, 0, len(candidates))
		for _, candidate := range candidates {
			if candidate.priority != topPriority {
				break
			}
			top = append(top, candidate)
		}
		candidateCount = len(top)
		for _, candidate := range top {
			if candidate.fileCount > candidateCount {
				candidateCount = candidate.fileCount
			}
		}
		if claimable(top) {
			claimed := 0
			for _, candidate := range top {
				tag, err := transaction.Exec(ctx, `
					INSERT INTO track_mappings (
						track_id, library_file_id, method, confidence, is_manual
					)
					VALUES ($1, $2, $3, $4, false)
					ON CONFLICT DO NOTHING
				`, candidate.trackID, fileID, candidate.method, candidate.confidence)
				if err != nil {
					return err
				}
				claimed += int(tag.RowsAffected())
				if tag.RowsAffected() > 0 && !held[candidate.trackID] {
					gained = true
				}
			}
			// Every track in the group or none of it. A group that lost a race
			// for one of its tracks is a group somebody else is already
			// answering, and half an answer is not one.
			if claimed == len(top) {
				status = "matched"
				candidateCount = len(top)
			} else {
				status = "ambiguous"
				// The half that did insert is taken back out. Each mapping is
				// written with ON CONFLICT DO NOTHING, so a track another file
				// already holds simply inserts nothing — and the ones that did
				// land used to stay, leaving a file whose own row says the
				// question is open while the catalogue counts its tracks as
				// owned on the strength of it. That is the invariant the whole
				// project rests on: nothing half-proven is written down.
				//
				// This can only remove rows, and only rows this same
				// transaction inserted a moment ago: every automatic mapping of
				// this file was deleted at the top of the pass, and manual rows
				// are excluded here as they were there. No pair is admitted,
				// and no file becomes matched that is not matched today.
				if _, err := transaction.Exec(ctx, `
					DELETE FROM track_mappings
					WHERE library_file_id = $1 AND NOT is_manual
				`, fileID); err != nil {
					return err
				}
				// Nothing was gained: what was written has been withdrawn.
				gained = false
			}
		} else if heldByOthers(top) || heldBySameRecording(top) {
			status = "duplicate"
		} else {
			status = "ambiguous"
		}
	}
	if _, err := transaction.Exec(ctx, `
		UPDATE library_files
		SET match_status = $2, match_candidate_count = $3, updated_at = now()
		WHERE id = $1
	`, fileID, status, candidateCount); err != nil {
		return err
	}
	if err := transaction.Commit(ctx); err != nil {
		return err
	}
	if status == "matched" && gained {
		service.queueTagging(ctx, fileID)
	}
	// A file that held tracks a moment ago and holds none now. Nothing about
	// the pass above changed: this reads the tracks it already read, after the
	// same commit, and asks for the file's own tags to be put back because the
	// account Schall wrote into it is a track it no longer answers.
	if status != "matched" && len(held) > 0 {
		service.queueTagging(ctx, fileID)
	}
	return service.reconcilePeers(ctx, peerIDs)
}

// mappedTracks is every catalogue track a file answers as things stand.
func mappedTracks(ctx context.Context, source rows, fileID uuid.UUID) (map[uuid.UUID]bool, error) {
	found, err := source.Query(ctx, `
		SELECT track_id FROM track_mappings WHERE library_file_id = $1
	`, fileID)
	if err != nil {
		return nil, err
	}
	defer found.Close()
	held := map[uuid.UUID]bool{}
	for found.Next() {
		var trackID uuid.UUID
		if err := found.Scan(&trackID); err != nil {
			return nil, err
		}
		held[trackID] = true
	}
	return held, found.Err()
}

// queueTagging asks for the tags of a file whose mappings have just changed to
// be brought back into line with them.
//
// The job settles which way that goes, because it reads the mappings when it
// runs rather than when it was asked for: a file that is a catalogue track's
// has the catalogue's account written into it, and a file that is no longer any
// track's is given back the tags it arrived with. So this can be asked for
// wherever a mapping was written or taken away, and nothing here has to predict
// what the file will be by the time the job reaches it.
//
// Writing the catalogue's account is the first half.
//
// The tags on a file are the account of whoever sent it until Schall writes its
// own, and a pass over the library writes only the files that were matched when
// somebody pressed the button. Matching is continuous; the button is not. So a
// file matched between two presses carried a year where its album carried a
// date, and a music server that groups by what it reads filed the album twice.
//
// Taking it back is the second. A file that loses its mapping keeps the tags
// Schall wrote, so it goes on telling the music server it is a track it is not
// mapped to any more — an album shown as holding a song nothing gives it. The
// mapping is what a music server never sees, so the tags are the only place
// this can be corrected.
//
// This decides nothing and observes. It runs after the mapping is committed,
// and a queue that could not be written to is a line in the log: the match is
// what it is whether or not the tags are written, and a reconcile that failed
// over a courtesy would be the false negative version of exactly the thing this
// project refuses. The write itself is a job's, because it copies the whole
// file.
//
// One queued job per file is enough. A file reconciled twice in a row wants one
// write, and the second job would find the file already saying what the first
// wrote.
//
// A job already running is not one of those. It read the file's mappings when
// it started, which was before whatever has just changed them, so it is
// answering the question as it stood and this request is a different question.
// Dropping it against a running job loses it for good in the one direction that
// has no other way back: the pass over the library covers the files that are
// mapped to a track, and a file that lost its last mapping is not among them,
// so nothing would ever ask again. A second job on a file that turns out to
// need nothing reads its tags and writes nothing, which is the cheap half of
// the trade.
func (service *Service) queueTagging(ctx context.Context, fileID uuid.UUID) {
	if _, err := service.pool.Exec(ctx, `
		INSERT INTO jobs (kind, payload, max_attempts)
		SELECT 'tag_file', jsonb_build_object('fileId', $1::text), 3
		WHERE NOT EXISTS (
			SELECT 1 FROM jobs
			WHERE kind = 'tag_file'
			  AND payload->>'fileId' = $1::text
			  AND status = 'queued'
		)
	`, fileID); err != nil {
		service.logger.Warn().Err(err).Str("file_id", fileID.String()).
			Msg("queue the tags of a file whose mappings changed to be brought into line")
	}
}

// claimable reports whether the file may take every track in the group.
//
// Each track must still be free and must still have this file as its only
// claimant: "one file for that track" is unchanged, and a track two files could
// answer is a question for the user as it always was.
//
// Beyond that, a group of more than one is only ever taken when an identifier
// found it. Tracks reached through a recording id, a resolved identity or an
// ISRC are joined to the file through that identifier, so a group of them is
// one recording appearing on several releases -- an EP and the compilation that
// gathers it -- rather than a doubt about which recording the file is. They do
// not disagree with each other; they are the same answer written down more than
// once, and the file is the honest answer to all of them.
//
// Tag agreement keeps the old rule. Two tracks that merely share an artist,
// album and position really are two different tracks, and nothing may choose
// between them.
func claimable(top []candidateMatch) bool {
	if len(top) == 0 {
		return false
	}
	for _, candidate := range top {
		if candidate.fileCount != 1 || !candidate.available {
			return false
		}
		// Tags that nothing outside them confirmed. The four fields agree, and
		// the one field that could have checked them against the audio was
		// missing from one side or the other. Schall writes those four itself,
		// so agreement alone cannot say whether it is reading a proof or its own
		// handwriting. That is a candidate to put to somebody, never an answer.
		if candidate.method == "album_disc_track_untimed" {
			return false
		}
	}
	return len(top) == 1 || foundByIdentifier(top[0].method)
}

// heldByOthers reports whether every track this file answers was given to
// another file by hand.
//
// Then there is nothing undecided about it. The track is held, manual decisions
// win and are permanent, and this file may not take it; a second file for music
// already in place is a duplicate, which is a state rather than a question. It
// is recorded as one so the review queue stops asking which track the file sits
// in over a list of tracks it is not allowed to have.
//
// A track that is free but that two files answer is the opposite case and stays
// ambiguous: nobody has decided it, so somebody has to. What to do about owning
// the same music twice is asked where duplicates are resolved, and nothing here
// answers it — this only stops calling it a matching question.
func heldByOthers(top []candidateMatch) bool {
	for _, candidate := range top {
		if !candidate.heldByHand {
			return false
		}
	}
	return len(top) > 0
}

// heldBySameRecording reports whether the group failed to claim because
// another file already resolves to the same recording, rather than because
// two files merely want the same track number.
//
// It reads only musicbrainz_recording_id and resolved_identity groups. Every
// track such a group names carries the file's own musicbrainz_recording_id
// (read through recording_id_aliases), so fileCount there counts files whose
// own identity resolves to that exact recording, and a track such a group
// already holds was won by a file that passed the identical test. So a group
// like this can only fail to be claimable because some other file resolves to
// the one recording it names: fileCount above one is that file counted
// alongside this one, and held-but-unclaimed is that file already sitting on
// the mapping.
//
// ISRC is excluded on purpose. MusicBrainz sometimes enters one performance as
// two recordings it never merges (ADR 0025), each keeping the same
// ISRC, so two files agreeing on the tag are not proven to be one recording
// the way two files agreeing on a musicbrainz_recording_id are. Telling the
// two apart needs the distributor's sample and AcoustID's leading cluster, and
// reconcile has neither on hand, so an ISRC-only contest stays a question.
//
// Tag agreement gives none of this either — a track two files merely describe
// the same way can carry no established recording at all — so a group tags
// alone produced never counts here.
func heldBySameRecording(top []candidateMatch) bool {
	for _, candidate := range top {
		if candidate.method != "musicbrainz_recording_id" && candidate.method != "resolved_identity" {
			return false
		}
		if candidate.fileCount == 1 && candidate.available {
			return false
		}
	}
	return len(top) > 0
}

// foundByIdentifier reports whether a candidate was reached through an
// identifier rather than through what its tags happened to say.
func foundByIdentifier(method string) bool {
	switch method {
	case "musicbrainz_recording_id", "resolved_identity", "isrc":
		return true
	default:
		return false
	}
}

// Peers reports the present files that answer the same music as this one, by
// the same test the cascade inside a reconcile uses.
//
// It is exported for one caller: something about to take a file out of the
// library. The peers have to be read while the row is still there — afterwards
// there is nothing left to compare them to — and whatever a peer was told about
// this file (a track it could not have, a copy already in place) stops being
// true the moment the file is gone.
func (service *Service) Peers(ctx context.Context, fileID uuid.UUID) ([]uuid.UUID, error) {
	return peersOf(ctx, service.pool, fileID)
}

// rows is the part of a pool and a transaction this needs: peers are read the
// same way inside the reconcile that is about to write, and outside one.
type rows interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func peersOf(ctx context.Context, source rows, fileID uuid.UUID) ([]uuid.UUID, error) {
	found, err := source.Query(ctx, `
		SELECT DISTINCT others.id
		FROM library_files file
		LEFT JOIN library_file_identities identity ON identity.library_file_id = file.id
		JOIN library_files others ON others.id <> file.id AND others.missing_at IS NULL
		LEFT JOIN library_file_identities other_identity
		       ON other_identity.library_file_id = others.id
		WHERE file.id = $1
		  AND (
		    (file.musicbrainz_recording_id IS NOT NULL
		     AND others.musicbrainz_recording_id = file.musicbrainz_recording_id)
		    -- Two files that resolved to the same recording contest the same
		    -- track, so settling one is a reason to look at the other.
		    OR (identity.musicbrainz_recording_id IS NOT NULL
		        AND identity.musicbrainz_recording_id IN (
		            others.musicbrainz_recording_id,
		            other_identity.musicbrainz_recording_id))
		    OR (file.musicbrainz_recording_id IS NOT NULL
		        AND other_identity.musicbrainz_recording_id = file.musicbrainz_recording_id)
		    OR (file.isrc IS NOT NULL
		        AND upper(btrim(others.isrc)) = upper(btrim(file.isrc)))
		    OR (file.artist_tag IS NOT NULL AND file.album_tag IS NOT NULL
		        AND file.disc_number IS NOT NULL AND file.track_number IS NOT NULL
		        AND lower(btrim(others.artist_tag)) = lower(btrim(file.artist_tag))
		        AND lower(btrim(others.album_tag)) = lower(btrim(file.album_tag))
		        AND others.disc_number = file.disc_number
		        AND others.track_number = file.track_number)
		  )
	`, fileID)
	if err != nil {
		return nil, err
	}
	defer found.Close()

	var peerIDs []uuid.UUID
	for found.Next() {
		var peerID uuid.UUID
		if err := found.Scan(&peerID); err != nil {
			return nil, err
		}
		peerIDs = append(peerIDs, peerID)
	}
	return peerIDs, found.Err()
}

func (service *Service) reconcilePeers(ctx context.Context, peerIDs []uuid.UUID) error {
	for _, peerID := range peerIDs {
		if err := service.reconcileFile(ctx, peerID, false); err != nil {
			return fmt.Errorf("reconcile peer file %s: %w", peerID, err)
		}
	}
	return nil
}

func (service *Service) ReconcileAlbum(ctx context.Context, albumID uuid.UUID) error {
	rows, err := service.pool.Query(ctx, `
		SELECT DISTINCT library_files.id
		FROM library_files
		LEFT JOIN track_mappings ON track_mappings.library_file_id = library_files.id
		LEFT JOIN tracks mapped_tracks ON mapped_tracks.id = track_mappings.track_id
		WHERE library_files.missing_at IS NULL
		  AND (
		    mapped_tracks.album_id = $1
		    OR library_files.musicbrainz_recording_id IN (
		        SELECT musicbrainz_recording_id FROM tracks
		        WHERE album_id = $1 AND musicbrainz_recording_id IS NOT NULL
		    )
		    -- A file resolved before this release existed locally is exactly the
		    -- file this refresh should now be able to match.
		    OR EXISTS (
		        SELECT 1
		        FROM library_file_identities identity
		        JOIN tracks ON tracks.musicbrainz_recording_id = identity.musicbrainz_recording_id
		        WHERE identity.library_file_id = library_files.id
		          AND tracks.album_id = $1
		    )
		    OR upper(btrim(library_files.isrc)) IN (
		        SELECT upper(btrim(isrc)) FROM tracks
		        WHERE album_id = $1 AND isrc IS NOT NULL
		    )
		    OR (
		        library_files.artist_tag IS NOT NULL
		        AND library_files.album_tag IS NOT NULL
		        AND library_files.disc_number IS NOT NULL
		        AND library_files.track_number IS NOT NULL
		        AND EXISTS (
		            SELECT 1
		            FROM tracks
		            JOIN albums ON albums.id = tracks.album_id
		            JOIN artists ON artists.id = albums.artist_id
		            WHERE tracks.album_id = $1
		              AND lower(btrim(artists.name)) = lower(btrim(library_files.artist_tag))
		              AND lower(btrim(albums.title)) = lower(btrim(library_files.album_tag))
		              AND tracks.disc_number = library_files.disc_number
		              AND tracks.track_number = library_files.track_number
		        )
		    )
		  )
	`, albumID)
	if err != nil {
		return err
	}
	defer rows.Close()
	var fileIDs []uuid.UUID
	for rows.Next() {
		var fileID uuid.UUID
		if err := rows.Scan(&fileID); err != nil {
			return err
		}
		fileIDs = append(fileIDs, fileID)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, fileID := range fileIDs {
		if err := service.ReconcileFile(ctx, fileID); err != nil {
			return fmt.Errorf("reconcile file %s: %w", fileID, err)
		}
	}
	return nil
}

func (service *Service) Candidates(ctx context.Context, fileID uuid.UUID) ([]Candidate, error) {
	rows, err := service.pool.Query(ctx, candidateQuery, fileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]Candidate, 0)
	for rows.Next() {
		var item Candidate
		var trackNumber *int32
		if err := rows.Scan(
			&item.TrackID, &item.Title, &item.AlbumTitle, &item.ArtistName,
			&item.DiscNumber, &trackNumber, &item.Method, &item.Confidence,
			&item.AlreadyMapped, &item.ManuallyMapped,
			&item.HeldByFileID, &item.HeldByPath,
		); err != nil {
			return nil, err
		}
		item.TrackNumber = trackNumber
		result = append(result, item)
	}
	return result, rows.Err()
}

func (service *Service) SearchTracks(ctx context.Context, fileID uuid.UUID, query string, limit int) ([]Candidate, error) {
	var fileExists bool
	if err := service.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM library_files WHERE id = $1 AND missing_at IS NULL
		)
	`, fileID).Scan(&fileExists); err != nil {
		return nil, err
	}
	if !fileExists {
		return nil, pgx.ErrNoRows
	}
	rows, err := service.pool.Query(ctx, `
		SELECT tracks.id, tracks.title, albums.title, artists.name,
		       tracks.disc_number, tracks.track_number,
		       (track_mappings.id IS NOT NULL AND track_mappings.library_file_id <> $1),
		       coalesce(track_mappings.is_manual AND track_mappings.library_file_id <> $1, false),
		       holder.id, coalesce(holder.path, '')
		FROM tracks
		JOIN albums ON albums.id = tracks.album_id
		JOIN artists ON artists.id = albums.artist_id
		LEFT JOIN track_mappings ON track_mappings.track_id = tracks.id
		LEFT JOIN library_files holder
		       ON holder.id = track_mappings.library_file_id AND holder.id <> $1
		-- A search box is not a pattern language, so % and _ have to be escaped
		-- to match themselves, and so does the escape character or it would eat
		-- the escapes put in beside it. One regexp pass over all three is what
		-- keeps that last part from depending on the order. The ordering below
		-- compares against the query as typed, which is not a pattern at all.
		WHERE concat_ws(' ', artists.name, albums.title, tracks.title)
		      ILIKE '%' || regexp_replace($2, '([\\%_])', '\\\1', 'g') || '%' ESCAPE '\'
		ORDER BY
		    CASE
		        WHEN lower(tracks.title) = lower($2) THEN 0
		        WHEN lower(artists.name) = lower($2) THEN 1
		        WHEN lower(albums.title) = lower($2) THEN 2
		        ELSE 3
		    END,
		    artists.name, albums.title, tracks.disc_number,
		    tracks.track_number NULLS LAST, tracks.title, tracks.id
		LIMIT $3
	`, fileID, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]Candidate, 0)
	for rows.Next() {
		var item Candidate
		if err := rows.Scan(
			&item.TrackID, &item.Title, &item.AlbumTitle, &item.ArtistName,
			&item.DiscNumber, &item.TrackNumber, &item.AlreadyMapped,
			&item.ManuallyMapped, &item.HeldByFileID, &item.HeldByPath,
		); err != nil {
			return nil, err
		}
		item.Method = "catalogue_search"
		result = append(result, item)
	}
	return result, rows.Err()
}

func (service *Service) SetManual(ctx context.Context, fileID, trackID uuid.UUID, reassign bool) error {
	transaction, err := service.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	var exists bool
	if err := transaction.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM library_files WHERE id = $1 AND missing_at IS NULL
		) AND EXISTS (
			SELECT 1 FROM tracks WHERE id = $2
		)
	`, fileID, trackID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return pgx.ErrNoRows
	}
	var conflictingManualFileID *uuid.UUID
	if err := transaction.QueryRow(ctx, `
		SELECT library_file_id
		FROM track_mappings
		WHERE track_id = $1 AND library_file_id <> $2 AND is_manual
	`, trackID, fileID).Scan(&conflictingManualFileID); errors.Is(err, pgx.ErrNoRows) {
		conflictingManualFileID = nil
	} else if err != nil {
		return err
	}
	if conflictingManualFileID != nil && !reassign {
		return ErrManualConflict
	}
	var displacedFileID *uuid.UUID
	if err := transaction.QueryRow(ctx, `
		SELECT library_file_id
		FROM track_mappings
		WHERE track_id = $1 AND library_file_id <> $2 AND NOT is_manual
	`, trackID, fileID).Scan(&displacedFileID); errors.Is(err, pgx.ErrNoRows) {
		displacedFileID = nil
	} else if err != nil {
		return err
	}
	if _, err := transaction.Exec(ctx, `
		DELETE FROM track_mappings
		WHERE library_file_id = $1
		   OR (track_id = $2 AND (NOT is_manual OR $3))
	`, fileID, trackID, reassign); err != nil {
		return err
	}
	if _, err := transaction.Exec(ctx, `
		INSERT INTO track_mappings (
			track_id, library_file_id, method, confidence, is_manual
		)
		VALUES ($1, $2, 'manual', 1, true)
	`, trackID, fileID); err != nil {
		return err
	}
	if _, err := transaction.Exec(ctx, `
		UPDATE library_files
		SET match_status = 'matched', match_candidate_count = 1, updated_at = now()
		WHERE id = $1
	`, fileID); err != nil {
		return err
	}
	// A want may be waiting on exactly this. Its copy is on the disc and the
	// library called that file another recording, so the want stopped with no
	// time to be looked at again. A mapping onto a catalogue track is one of the
	// two things that answer for a file, so the want is given a next look in the
	// same transaction as the mapping, and the sweep that follows settles it
	// against the file already there.
	if err := db.RearmWantsHoldingFile(ctx, transaction, fileID, db.RearmedByADecisionSummary); err != nil {
		return err
	}
	if err := transaction.Commit(ctx); err != nil {
		return err
	}
	// A file somebody matched by hand is a catalogue track's as surely as one
	// matching reached on its own, and the tags on it are just as much the
	// sender's account. The reconcile below leaves a manual mapping alone, so
	// nothing downstream of it would ever ask for this.
	service.queueTagging(ctx, fileID)
	if err := service.ReconcileFile(ctx, fileID); err != nil {
		return err
	}
	// The file the track was taken from. Its mapping was deleted by the
	// transaction above rather than by the reconcile, so the reconcile sees a
	// file that already answers nothing and asks for nothing on its behalf. It
	// is asked for here instead: whichever way the reconcile left the file, the
	// job reads its mappings for itself.
	if displacedFileID != nil {
		if err := service.ReconcileFile(ctx, *displacedFileID); err != nil {
			return err
		}
		service.queueTagging(ctx, *displacedFileID)
	}
	if conflictingManualFileID != nil {
		if err := service.ReconcileFile(ctx, *conflictingManualFileID); err != nil {
			return err
		}
		service.queueTagging(ctx, *conflictingManualFileID)
	}
	return nil
}

func (service *Service) ClearManual(ctx context.Context, fileID uuid.UUID) error {
	transaction, err := service.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	tag, err := transaction.Exec(ctx, `
		DELETE FROM track_mappings
		WHERE library_file_id = $1 AND is_manual
	`, fileID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	if _, err := transaction.Exec(ctx, `
		UPDATE library_files
		SET match_status = 'unmatched', match_candidate_count = 0, updated_at = now()
		WHERE id = $1
	`, fileID); err != nil {
		return err
	}
	// Taking the answer back is an answer about the file too, so a want that
	// stopped on it is looked at again rather than left on a decision that no
	// longer exists.
	if err := db.RearmWantsHoldingFile(ctx, transaction, fileID, db.RearmedByADecisionSummary); err != nil {
		return err
	}
	if err := transaction.Commit(ctx); err != nil {
		return err
	}
	if err := service.ReconcileFile(ctx, fileID); err != nil {
		return err
	}
	// The decision that is being withdrawn was written into the file's tags
	// when it was made. The reconcile above may have matched the file again on
	// its own, and may not have; the job asks the mappings which of those
	// happened and either writes the new track's account or gives the file back
	// the tags it arrived with.
	service.queueTagging(ctx, fileID)
	return nil
}

func automaticCandidates(ctx context.Context, transaction pgx.Tx, fileID uuid.UUID) ([]candidateMatch, error) {
	rows, err := transaction.Query(ctx, `
		WITH file AS (
		    SELECT * FROM library_files WHERE id = $1 AND missing_at IS NULL
		), known AS (
		    -- The recording this file is already known to be, from an identifier
		    -- it carries or from a resolution recorded against it. Read once and
		    -- on its own, because the only arm that needs it is the tag arm and
		    -- joining the identity row onto every candidate would say nothing to
		    -- the others.
		    --
		    -- Its own tag comes first. Both are identifiers, and a file that
		    -- carries one has already been matched through it above; reaching the
		    -- tag arm at all means the catalogue holds no track for it.
		    SELECT coalesce(
		               file.musicbrainz_recording_id,
		               (SELECT identity.musicbrainz_recording_id
		                FROM library_file_identities identity
		                WHERE identity.library_file_id = file.id)
		           ) AS recording_id
		    FROM file
		), candidates AS (
		    SELECT tracks.id AS track_id, 'verified_download'::text AS method,
		           1.0::real AS confidence, 1 AS priority,
		           (SELECT count(*)
		            FROM downloads others
		            JOIN library_files owned ON owned.id = others.library_file_id
		            WHERE others.track_id = tracks.id AND owned.missing_at IS NULL) AS file_count
		    FROM file
		    JOIN downloads ON downloads.library_file_id = file.id
		    JOIN download_requests ON download_requests.id = downloads.download_request_id
		    JOIN tracks ON tracks.id = downloads.track_id
		    WHERE download_requests.import_status = 'imported'
		    UNION ALL
		    -- Read through the identifiers a recording answers to rather than
		    -- against the one it happens to be written as. MusicBrainz merges
		    -- recordings, and a file tagged before a merge carries the retired
		    -- identifier for ever while a catalogue refreshed after it holds the
		    -- surviving one; comparing the two literally found nothing and put a
		    -- question to a person that MusicBrainz had already answered.
		    -- recording_id_aliases is the set the provider itself stated, so an
		    -- identifier nobody merged still answers to itself alone and a
		    -- genuinely different recording still fails to meet.
		    --
		    -- file_count is widened with it, and has to be. It is how many files
		    -- answer this track by this rule, and a group is claimable only when
		    -- that is one. Widening what meets without widening what is counted
		    -- would let a file carrying the retired identifier and a file
		    -- carrying the surviving one each believe it were alone.
		    SELECT tracks.id AS track_id, 'musicbrainz_recording_id'::text AS method,
		           1.0::real AS confidence, 2 AS priority,
		           (SELECT count(*) FROM library_files others
		            LEFT JOIN library_file_identities other_identity
		                   ON other_identity.library_file_id = others.id
		            WHERE others.missing_at IS NULL
		              AND (others.musicbrainz_recording_id IN (
		                       SELECT recording_id_aliases(file.musicbrainz_recording_id))
		                   OR other_identity.musicbrainz_recording_id IN (
		                       SELECT recording_id_aliases(file.musicbrainz_recording_id)))) AS file_count
		    FROM file
		    JOIN tracks ON tracks.musicbrainz_recording_id IN (
		        SELECT recording_id_aliases(file.musicbrainz_recording_id))
		    WHERE file.musicbrainz_recording_id IS NOT NULL
		    UNION ALL
		    -- An identity the providers proved for a file that carries no
		    -- identifier of its own is an identifier all the same. It is how
		    -- music acquired outside Schall reaches the catalogue: resolved
		    -- once, then matched the day the release is ingested.
		    SELECT tracks.id, 'resolved_identity', 0.99::real, 3,
		           (SELECT count(*) FROM library_files others
		            LEFT JOIN library_file_identities other_identity
		                   ON other_identity.library_file_id = others.id
		            WHERE others.missing_at IS NULL
		              AND (others.musicbrainz_recording_id IN (
		                       SELECT recording_id_aliases(identity.musicbrainz_recording_id))
		                   OR other_identity.musicbrainz_recording_id IN (
		                       SELECT recording_id_aliases(identity.musicbrainz_recording_id))))
		    FROM file
		    JOIN library_file_identities identity ON identity.library_file_id = file.id
		    JOIN tracks ON tracks.musicbrainz_recording_id IN (
		        SELECT recording_id_aliases(identity.musicbrainz_recording_id))
		    WHERE identity.musicbrainz_recording_id IS NOT NULL
		    UNION ALL
		    SELECT tracks.id, 'isrc', 0.98::real, 4,
		           (SELECT count(*) FROM library_files others
		            WHERE others.missing_at IS NULL
		              AND upper(btrim(others.isrc)) = upper(btrim(file.isrc)))
		    FROM file
		    JOIN tracks ON upper(btrim(tracks.isrc)) = upper(btrim(file.isrc))
		    WHERE file.isrc IS NOT NULL
		    UNION ALL
		    -- Tags, read beside the one field Schall does not write.
		    --
		    -- Artist, album, disc and track are exactly the four fields Schall's
		    -- own tag writer fills in from the catalogue. On a file Schall has
		    -- tagged they say back whatever conclusion produced them, so on their
		    -- own they cannot tell a pairing that was proved from one that was
		    -- merely written to disc. Length comes from somewhere else: it is
		    -- measured off the audio, and retagging does not move it. Read beside
		    -- the four, it is what stops the file being its own evidence.
		    --
		    -- So it answers three ways rather than two. Within five seconds
		    -- something outside the tags confirms them and the pair stands as it
		    -- always did. More than five seconds apart is a contradiction, and a
		    -- contradiction voids the pair however well the rest lines up -- those
		    -- rows are gone below rather than demoted. Absent from either side is
		    -- silence: the tags still point at one track, and that is a candidate
		    -- to put to somebody rather than an answer to write down.
		    SELECT tracks.id,
		           CASE WHEN audio.agrees THEN 'album_disc_track'
		                ELSE 'album_disc_track_untimed' END,
		           CASE WHEN audio.agrees THEN 0.95::real ELSE 0.9::real END,
		           CASE WHEN audio.agrees THEN 5 ELSE 6 END,
		           (SELECT count(*)
		            FROM library_files others
		            WHERE others.missing_at IS NULL
		              AND lower(btrim(others.artist_tag)) = lower(btrim(file.artist_tag))
		              AND lower(btrim(others.album_tag)) = lower(btrim(file.album_tag))
		              AND others.disc_number = file.disc_number
		              AND others.track_number = file.track_number)
		    FROM file
		    CROSS JOIN known
		    JOIN artists ON lower(btrim(artists.name)) = lower(btrim(file.artist_tag))
		    JOIN albums ON albums.artist_id = artists.id
		               AND lower(btrim(albums.title)) = lower(btrim(file.album_tag))
		    JOIN tracks ON tracks.album_id = albums.id
		               AND tracks.disc_number = file.disc_number
		               AND tracks.track_number = file.track_number
		    CROSS JOIN LATERAL (
		        SELECT abs(tracks.duration_ms - file.duration_ms) <= 5000 AS agrees
		    ) AS audio
		    WHERE file.artist_tag IS NOT NULL AND file.album_tag IS NOT NULL
		      AND file.disc_number IS NOT NULL AND file.track_number IS NOT NULL
		      -- Not merely false: a length neither side stated says nothing, and
		      -- silence has never been either an agreement or a disagreement here.
		      AND audio.agrees IS NOT FALSE
		      -- What the file is already known to be outranks what it says it is.
		      -- A track that is a different recording, or carries a different
		      -- ISRC, contradicts the file whatever its tags line up with, and a
		      -- contradiction voids the pair. Either side saying nothing is
		      -- silence again and leaves the pair alone.
		      -- The contradiction reads through the same aliases the agreement
		      -- does, and it must. A file tagged before a merge carries the
		      -- retired identifier while the track holds the survivor: read
		      -- literally that is a difference, and a difference voids the pair
		      -- whatever else agrees — so widening only what meets would have
		      -- left this to strike the same pair out again.
		      AND NOT (known.recording_id IS NOT NULL
		               AND tracks.musicbrainz_recording_id IS NOT NULL
		               AND tracks.musicbrainz_recording_id NOT IN (
		                   SELECT recording_id_aliases(known.recording_id)))
		      AND NOT (file.isrc IS NOT NULL AND tracks.isrc IS NOT NULL
		               AND upper(btrim(tracks.isrc)) <> upper(btrim(file.isrc)))
		), ranked AS (
		    SELECT DISTINCT ON (track_id) track_id, method, confidence, priority, file_count
		    FROM candidates
		    ORDER BY track_id, priority
		)
		SELECT ranked.track_id, ranked.method, ranked.confidence, ranked.priority,
		       ranked.file_count,
		       (track_mappings.id IS NULL OR track_mappings.library_file_id = $1),
		       -- Held by somebody's decision rather than by a claim. An automatic
		       -- mapping is contested by a second file answering the same track
		       -- and both are asked about; a manual one is permanent, so there is
		       -- nothing to ask and the second file is a duplicate.
		       coalesce(
		           track_mappings.is_manual AND track_mappings.library_file_id <> $1,
		           false)
		FROM ranked
		LEFT JOIN track_mappings ON track_mappings.track_id = ranked.track_id
		ORDER BY ranked.priority, ranked.track_id
	`, fileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []candidateMatch
	for rows.Next() {
		var candidate candidateMatch
		if err := rows.Scan(
			&candidate.trackID, &candidate.method, &candidate.confidence,
			&candidate.priority, &candidate.fileCount, &candidate.available,
			&candidate.heldByHand,
		); err != nil {
			return nil, err
		}
		result = append(result, candidate)
	}
	return result, rows.Err()
}

const candidateQuery = `
WITH file AS (
    SELECT * FROM library_files WHERE id = $1 AND missing_at IS NULL
), known AS (
    SELECT coalesce(
               file.musicbrainz_recording_id,
               (SELECT identity.musicbrainz_recording_id
                FROM library_file_identities identity
                WHERE identity.library_file_id = file.id)
           ) AS recording_id
    FROM file
), candidates AS (
    SELECT tracks.id AS track_id, 'verified_download'::text AS method,
           1.0::real AS confidence, 1 AS priority
    FROM file
    JOIN downloads ON downloads.library_file_id = file.id
    JOIN download_requests ON download_requests.id = downloads.download_request_id
    JOIN tracks ON tracks.id = downloads.track_id
    WHERE download_requests.import_status = 'imported'
    UNION ALL
    SELECT tracks.id AS track_id, 'musicbrainz_recording_id'::text AS method,
           1.0::real AS confidence, 2 AS priority
    FROM file JOIN tracks ON tracks.musicbrainz_recording_id = file.musicbrainz_recording_id
    WHERE file.musicbrainz_recording_id IS NOT NULL
    UNION ALL
    SELECT tracks.id, 'resolved_identity', 0.99::real, 3
    FROM file
    JOIN library_file_identities identity ON identity.library_file_id = file.id
    JOIN tracks ON tracks.musicbrainz_recording_id = identity.musicbrainz_recording_id
    WHERE identity.musicbrainz_recording_id IS NOT NULL
    UNION ALL
    SELECT tracks.id, 'isrc', 0.98::real, 4
    FROM file JOIN tracks ON upper(btrim(tracks.isrc)) = upper(btrim(file.isrc))
    WHERE file.isrc IS NOT NULL
    UNION ALL
    -- The same three answers the reconcile reads, so what a person is shown is
    -- what the machine was looking at: confirmed by the length, unconfirmed
    -- because no length was there to ask, or contradicted and therefore absent.
    SELECT tracks.id,
           CASE WHEN audio.agrees THEN 'album_disc_track'
                ELSE 'album_disc_track_untimed' END,
           CASE WHEN audio.agrees THEN 0.95::real ELSE 0.9::real END,
           CASE WHEN audio.agrees THEN 5 ELSE 6 END
    FROM file
    CROSS JOIN known
    JOIN artists ON lower(btrim(artists.name)) = lower(btrim(file.artist_tag))
    JOIN albums ON albums.artist_id = artists.id
               AND lower(btrim(albums.title)) = lower(btrim(file.album_tag))
    JOIN tracks ON tracks.album_id = albums.id
               AND tracks.disc_number = file.disc_number
               AND tracks.track_number = file.track_number
    CROSS JOIN LATERAL (
        SELECT abs(tracks.duration_ms - file.duration_ms) <= 5000 AS agrees
    ) AS audio
    WHERE file.artist_tag IS NOT NULL AND file.album_tag IS NOT NULL
      AND file.disc_number IS NOT NULL AND file.track_number IS NOT NULL
      AND audio.agrees IS NOT FALSE
      AND NOT (known.recording_id IS NOT NULL
               AND tracks.musicbrainz_recording_id IS NOT NULL
               AND tracks.musicbrainz_recording_id <> known.recording_id)
      AND NOT (file.isrc IS NOT NULL AND tracks.isrc IS NOT NULL
               AND upper(btrim(tracks.isrc)) <> upper(btrim(file.isrc)))
), ranked AS (
    SELECT DISTINCT ON (track_id) track_id, method, confidence, priority
    FROM candidates ORDER BY track_id, priority
)
SELECT tracks.id, tracks.title, albums.title, artists.name, tracks.disc_number,
       tracks.track_number, ranked.method, ranked.confidence,
       (track_mappings.id IS NOT NULL AND track_mappings.library_file_id <> $1),
       coalesce(track_mappings.is_manual AND track_mappings.library_file_id <> $1, false),
       holder.id, coalesce(holder.path, '')
FROM ranked
JOIN tracks ON tracks.id = ranked.track_id
JOIN albums ON albums.id = tracks.album_id
JOIN artists ON artists.id = albums.artist_id
LEFT JOIN track_mappings ON track_mappings.track_id = tracks.id
-- The file this track already belongs to, when it is not the one being asked
-- about. Read for the sentence rather than for the decision: nothing here
-- chooses, it only says what taking the track would take it from.
LEFT JOIN library_files holder
       ON holder.id = track_mappings.library_file_id AND holder.id <> $1
ORDER BY ranked.priority, artists.name, albums.title, tracks.disc_number,
         tracks.track_number NULLS LAST, tracks.id
`
