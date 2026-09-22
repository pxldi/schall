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
)

// SourceLookup is what yt-dlp said about the address a want is keyed to, when
// the person confirmed it (ADR 0038 §2). The admitted file is named with it
// (§5), so it is kept as the person saw it.
type SourceLookup struct {
	Title      string `json:"title"`
	Artist     string `json:"artist"`
	Uploader   string `json:"uploader"`
	AccountID  string `json:"accountId,omitempty"`
	DurationMS int    `json:"durationMs,omitempty"`
	ArtworkURL string `json:"artworkUrl,omitempty"`
}

// KeyedMinimumBitrate is the floor keying writes, in kbit/s (ADR 0038 §7).
const KeyedMinimumBitrate = 320

var (
	// ErrNotKeyable reports a want that cannot take an address: only one with
	// no recording and no answer yet, or one waiting on the Version question,
	// can (ADR 0038 §8).
	ErrNotKeyable = errors.New("only a want with no recording and no accepted copy can be keyed to an address")
	// ErrAlreadyKeyed reports a want a person already keyed. The key is a manual
	// decision and permanent (ADR 0038 §1).
	ErrAlreadyKeyed = errors.New("this want is already keyed to an address")
	// ErrNotKeyed reports a floor set on a want with no address. Recording wants
	// keep the format floors from the source preferences (ADR 0038 §7).
	ErrNotKeyed = errors.New("only a want keyed to an address has its own minimum bit rate")
)

// KeySourceParams is one confirmed address and the excerpt fetched from it.
type KeySourceParams struct {
	TargetID    uuid.UUID
	Source      string
	ExternalID  string
	ExternalURL string
	Lookup      SourceLookup
	// Fingerprint and Seconds are the excerpt's, as anchor.Measure read them.
	Fingerprint string
	Seconds     int
	// Summary is what the want says while it is searched, and AcquiredSummary
	// what it says when the library already holds the track.
	Summary         string
	AcquiredSummary string
}

// KeyAcquisitionTargetToSource writes the address a person confirmed onto a
// want, with the excerpt fetched from it as the want's anchor (ADR 0038 §1, §3).
//
// The anchor replaces any the want held: the address defines the want, so a
// Deezer preview or a name-found upload no longer speaks for it. The want stays
// unresolved, loses its resolution schedule and its candidates, and is due a
// search now. A want waiting on the Version question stops waiting. If the
// library already holds a file with the same source identity, the want is
// acquired with it in the same transaction. A sweep and a judging of the want's
// held copies are queued alongside (ADR 0029 §5), unless it was acquired.
func (q *Queries) KeyAcquisitionTargetToSource(
	ctx context.Context, params KeySourceParams,
) (AcquisitionTargetRow, error) {
	lookup, err := json.Marshal(params.Lookup)
	if err != nil {
		return AcquisitionTargetRow{}, fmt.Errorf("encode the lookup for want %s: %w", params.TargetID, err)
	}
	label := strings.TrimSpace(params.Lookup.Title)
	if artist := strings.TrimSpace(params.Lookup.Artist); artist != "" && label != "" {
		label = artist + " — " + label
	}

	var target AcquisitionTargetRow
	err = q.inTransaction(ctx, "keying a want to an address", func(tx pgx.Tx) error {
		var status, source string
		err := tx.QueryRow(ctx, `
			SELECT status || coalesce(':' || review_reason, ''), coalesce(source, '')
			FROM acquisition_targets
			WHERE id = $1
			FOR UPDATE
		`, params.TargetID).Scan(&status, &source)
		if err != nil {
			return err
		}
		switch {
		case source != "":
			return ErrAlreadyKeyed
		case status != "unresolved" && status != "awaiting_review:resolution":
			return ErrNotKeyable
		}
		// A copy already accepted on the want's earlier anchor is an answer, and
		// it proves that anchor, not the address.
		var answered bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
			    SELECT 1 FROM acquisition_target_files
			    WHERE acquisition_target_id = $1 AND verdict = 'accepted'
			)
		`, params.TargetID).Scan(&answered); err != nil {
			return err
		}
		if answered {
			return ErrNotKeyable
		}

		if _, err := tx.Exec(ctx, `
			UPDATE acquisition_targets
			SET source = $2, external_id = $3, external_url = $4, source_lookup = $5::jsonb,
			    minimum_bitrate = $6,
			    status = 'unresolved', review_reason = NULL,
			    next_attempt_at = NULL, last_error = NULL,
			    anchor_fingerprint = $7, anchor_source = 'source', anchor_reference = $3,
			    anchor_seconds = $8, anchor_label = nullif($9, ''), anchor_views = NULL,
			    anchor_fetched_at = now(), anchor_unavailable = NULL,
			    anchor_next_attempt_at = NULL,
			    next_search_at = now(), search_attempts = 0,
			    summary = $10, updated_at = now()
			WHERE id = $1
		`, params.TargetID, params.Source, params.ExternalID, params.ExternalURL, lookup,
			KeyedMinimumBitrate, params.Fingerprint, params.Seconds, label,
			params.Summary); err != nil {
			return fmt.Errorf("key want %s to %s: %w", params.TargetID, params.ExternalID, err)
		}
		if _, err := tx.Exec(ctx, `
			DELETE FROM acquisition_target_candidates WHERE acquisition_target_id = $1
		`, params.TargetID); err != nil {
			return fmt.Errorf("clear the candidates of want %s: %w", params.TargetID, err)
		}

		var held uuid.NullUUID
		err = tx.QueryRow(ctx, `
			SELECT library_files.id
			FROM library_file_identities identities
			JOIN library_files ON library_files.id = identities.library_file_id
			WHERE identities.kind = 'source'
			  AND identities.source = $1
			  AND identities.external_id = $2
			  AND library_files.missing_at IS NULL
			ORDER BY library_files.created_at, library_files.id
			LIMIT 1
		`, params.Source, params.ExternalID).Scan(&held)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("look for a file already holding %s: %w", params.ExternalID, err)
		}
		if held.Valid {
			target, err = scanAcquisitionTarget(tx.QueryRow(ctx, `
				UPDATE acquisition_targets
				SET status = 'acquired', acquired_library_file_id = $2, acquired_at = now(),
				    next_search_at = NULL, summary = $3, updated_at = now()
				WHERE id = $1
				RETURNING`+acquisitionTargetColumns, params.TargetID, held.UUID,
				params.AcquiredSummary))
			return err
		}

		target, err = scanAcquisitionTarget(tx.QueryRow(ctx, `
			SELECT`+acquisitionTargetColumns+`FROM acquisition_targets WHERE id = $1
		`, params.TargetID))
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, queueAcquisitionSweep, time.Now()); err != nil {
			return fmt.Errorf("queue a sweep for want %s: %w", params.TargetID, err)
		}
		payload, err := json.Marshal(map[string]string{"acquisition_target_id": params.TargetID.String()})
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, queueWantCopyJudging, time.Now(), payload,
			params.TargetID.String()); err != nil {
			return fmt.Errorf("queue the copies of want %s for judging: %w", params.TargetID, err)
		}
		return nil
	})
	return target, err
}

// SetAcquisitionTargetMinimumBitrate changes the floor of a keyed want and asks
// for its held copies to be judged again, because a lower floor can admit a
// copy already held (ADR 0038 §7).
//
// pgx.ErrNoRows means there is no such want. ErrNotKeyed means it has no
// address.
func (q *Queries) SetAcquisitionTargetMinimumBitrate(
	ctx context.Context, id uuid.UUID, kbps int,
) (AcquisitionTargetRow, error) {
	var target AcquisitionTargetRow
	err := q.inTransaction(ctx, "setting a want's minimum bit rate", func(tx pgx.Tx) error {
		var keyed bool
		if err := tx.QueryRow(ctx, `
			SELECT source IS NOT NULL FROM acquisition_targets WHERE id = $1 FOR UPDATE
		`, id).Scan(&keyed); err != nil {
			return err
		}
		if !keyed {
			return ErrNotKeyed
		}
		var err error
		target, err = scanAcquisitionTarget(tx.QueryRow(ctx, `
			UPDATE acquisition_targets
			SET minimum_bitrate = $2, updated_at = now()
			WHERE id = $1
			RETURNING`+acquisitionTargetColumns, id, kbps))
		if err != nil {
			return err
		}
		if target.Status != "unresolved" {
			return nil
		}
		payload, err := json.Marshal(map[string]string{"acquisition_target_id": id.String()})
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, queueWantCopyJudging, time.Now(), payload, id.String())
		return err
	})
	return target, err
}

// SourceFileToName is a file admitted for a keyed want, with what the person
// confirmed about the address (ADR 0038 §5).
type SourceFileToName struct {
	LibraryFileID uuid.UUID
	Path          string
	Source        string
	ExternalID    string
	ExternalURL   string
	Lookup        SourceLookup
}

// KeyedFileToName reads the file a keyed want was acquired with and what it is
// to be named. pgx.ErrNoRows means the want is not keyed, is not acquired, or
// its file has gone.
func (q *Queries) KeyedFileToName(
	ctx context.Context, targetID uuid.UUID,
) (SourceFileToName, error) {
	var (
		named  SourceFileToName
		lookup []byte
	)
	err := q.db.QueryRow(ctx, `
		SELECT library_files.id, library_files.path, targets.source, targets.external_id,
		       targets.external_url, targets.source_lookup
		FROM acquisition_targets targets
		JOIN library_files ON library_files.id = targets.acquired_library_file_id
		WHERE targets.id = $1
		  AND targets.status = 'acquired'
		  AND targets.source IS NOT NULL
		  AND library_files.missing_at IS NULL
	`, targetID).Scan(&named.LibraryFileID, &named.Path, &named.Source, &named.ExternalID,
		&named.ExternalURL, &lookup)
	if err != nil {
		return SourceFileToName{}, err
	}
	if len(lookup) > 0 {
		if err := json.Unmarshal(lookup, &named.Lookup); err != nil {
			return SourceFileToName{}, fmt.Errorf("decode the lookup of want %s: %w", targetID, err)
		}
	}
	return named, nil
}

// NameSourceFileParams is what naming one file from its address writes.
type NameSourceFileParams struct {
	LibraryFileID uuid.UUID
	Source        string
	ExternalID    string
	Title         string
	Artist        string
	Uploader      string
	AccountID     string
	// UploaderSummary is the catalogue_summary a new uploader row carries.
	UploaderSummary string
	// Image and ContentType are the artwork. Empty records that there was none.
	Image       []byte
	ContentType string
}

// NameSourceFile writes the names a person confirmed onto a file's source
// identity, the uploader as an artist row keyed by service and account, and
// the artwork held against the file (ADR 0026 §4-§6, ADR 0038 §5). It returns
// the uploader's artist row. It writes only onto the source identity with the
// same key, so it cannot rename a file that carries another answer.
func (q *Queries) NameSourceFile(
	ctx context.Context, params NameSourceFileParams,
) (uuid.UUID, error) {
	var artistID uuid.UUID
	err := q.inTransaction(ctx, "naming a file from its address", func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE library_file_identities
			SET artist_name = nullif($3, ''), track_title = nullif($4, ''),
			    release_title = nullif($4, ''), updated_at = now()
			WHERE library_file_id = $1 AND kind = 'source' AND external_id = $2
		`, params.LibraryFileID, params.ExternalID, params.Artist, params.Title)
		if err != nil {
			return fmt.Errorf("name the file %s: %w", params.LibraryFileID, err)
		}
		if tag.RowsAffected() == 0 {
			return pgx.ErrNoRows
		}
		if strings.TrimSpace(params.AccountID) != "" {
			artistID, err = UpsertSourceUploader(ctx, tx, params.Source, params.AccountID,
				params.Uploader, params.UploaderSummary)
			if err != nil {
				return err
			}
		}
		return WriteFileCoverArt(ctx, tx, params.LibraryFileID, params.Image,
			params.ContentType, params.Source)
	})
	return artistID, err
}

// UpsertSourceUploader finds or makes the artist row for the account that
// published a track, keyed by the service and the account's identifier there,
// never by name (ADR 0026 §5). "Abo" on SoundCloud and "Abo" in MusicBrainz
// are two names that happen to match. The uploader is not followed.
func UpsertSourceUploader(
	ctx context.Context, tx pgx.Tx, source, accountID, name, summary string,
) (uuid.UUID, error) {
	if strings.TrimSpace(name) == "" {
		name = "Unknown uploader"
	}
	var artistID uuid.UUID
	err := tx.QueryRow(ctx, `
		INSERT INTO artists (name, sort_name, followed_at, catalogue_summary, source, external_id)
		VALUES ($1, $1, NULL, $2, $3, $4)
		ON CONFLICT (source, external_id) WHERE source IS NOT NULL AND external_id IS NOT NULL
		DO UPDATE SET name = EXCLUDED.name, sort_name = EXCLUDED.sort_name, updated_at = now()
		RETURNING id
	`, name, summary, source, accountID).Scan(&artistID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("record the %s uploader %s: %w", source, name, err)
	}
	return artistID, nil
}

// WriteFileCoverArt holds a picture against a file that belongs to no release.
// A row with no image records that there was none, so the question is not
// asked again (ADR 0026 §6).
func WriteFileCoverArt(
	ctx context.Context, tx pgx.Tx, fileID uuid.UUID, image []byte, contentType, source string,
) error {
	if len(image) == 0 {
		image, contentType = nil, ""
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO file_cover_art (library_file_id, image, content_type, source, fetched_at)
		VALUES ($1, $2, $3, $4, now())
		ON CONFLICT (library_file_id) DO UPDATE
		SET image = EXCLUDED.image, content_type = EXCLUDED.content_type,
		    source = EXCLUDED.source, fetched_at = EXCLUDED.fetched_at
	`, fileID, image, contentType, source); err != nil {
		return fmt.Errorf("record the artwork for the file %s: %w", fileID, err)
	}
	return nil
}

// ErrSourceAlreadyFetched reports a want that already holds a copy fetched from
// its address. Schall fetches from the source once per want (ADR 0038 §6).
var ErrSourceAlreadyFetched = errors.New("this want already holds a copy fetched from its address")

// holdsSourceCopy is "the want holds a copy fetched from its own address". A
// fetched copy records the service as its provider. A copy whose bytes never
// arrived does not count: nothing was judged, so the fetch has not happened.
// It reads targets.
const holdsSourceCopy = `
	EXISTS (
	    SELECT 1 FROM acquisition_target_files copies
	    WHERE copies.acquisition_target_id = targets.id
	      AND copies.provider = targets.source
	      AND copies.verdict <> 'undelivered'
	)`

// SourceCopyFetched reports whether a keyed want already holds a copy fetched
// from its address. pgx.ErrNoRows means there is no such want.
func (q *Queries) SourceCopyFetched(ctx context.Context, targetID uuid.UUID) (bool, error) {
	var fetched bool
	err := q.db.QueryRow(ctx, `
		SELECT `+holdsSourceCopy+`
		FROM acquisition_targets targets
		WHERE targets.id = $1
	`, targetID).Scan(&fetched)
	return fetched, err
}

// SourceFetchParams is one whole track fetched from a keyed want's address and
// written into the fetch folder.
type SourceFetchParams struct {
	TargetID uuid.UUID
	// Source and ExternalID are the want's key. They are recorded as the copy's
	// provider and user, the way a peer's copy records slskd and the peer.
	Source     string
	ExternalID string
	// Directory is the copy's folder under the fetch folder, and FileName the
	// file inside it.
	Directory string
	FileName  string
	Extension string
	SizeBytes int64
	Reasons   []string
	Summary   string
}

// RecordSourceFetch records a track fetched from a keyed want's address as a
// finished transfer, and queues its import (ADR 0038 §6). The import then
// judges it exactly as it judges a peer's copy: the same rows, the same job,
// the same verdicts.
//
// Everything is one transaction, and it re-reads the want first.
// ErrWantHasMovedOn means it stopped looking for a copy, or is not keyed to
// this address. ErrSourceAlreadyFetched means another pass got there first.
func (q *Queries) RecordSourceFetch(ctx context.Context, params SourceFetchParams) (uuid.UUID, error) {
	remotePath := params.Directory + "/" + params.FileName
	var requestID uuid.UUID
	err := q.inTransaction(ctx, "recording a track fetched from its address", func(tx pgx.Tx) error {
		var (
			source, externalID string
			searched, fetched  bool
		)
		if err := tx.QueryRow(ctx, `
			SELECT coalesce(targets.source, ''), coalesce(targets.external_id, ''),
			       `+searchedWant("targets.")+`, `+holdsSourceCopy+`
			FROM acquisition_targets targets
			WHERE targets.id = $1
			FOR UPDATE
		`, params.TargetID).Scan(&source, &externalID, &searched, &fetched); err != nil {
			return err
		}
		switch {
		case source != params.Source || externalID != params.ExternalID || !searched:
			return ErrWantHasMovedOn
		case fetched:
			return ErrSourceAlreadyFetched
		}

		files, err := json.Marshal([]DownloadRequestFile{{
			Path: remotePath, Name: params.FileName, Extension: params.Extension,
			SizeBytes: params.SizeBytes,
		}})
		if err != nil {
			return fmt.Errorf("encode the fetched track: %w", err)
		}
		reasons := params.Reasons
		if reasons == nil {
			reasons = []string{}
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO download_requests (
				acquisition_target_id, provider, source_username, source_directory,
				status, started_at, file_count, expected_track_count, total_size_bytes,
				format, score, reasons, files
			)
			VALUES ($1, $2, $3, $4, 'completed', now(), 1, 1, $5, $6, 0, $7, $8)
			RETURNING id
		`, params.TargetID, params.Source, params.ExternalID, params.Directory,
			params.SizeBytes, params.Extension, reasons, files).Scan(&requestID); err != nil {
			return fmt.Errorf("record the fetch of want %s: %w", params.TargetID, err)
		}
		// The import writes the library path onto this row, and completing the
		// want reads the library file back through it.
		if _, err := tx.Exec(ctx, `
			INSERT INTO downloads (
				download_request_id, provider, remote_path, source_username, source_path,
				size_bytes, transferred_bytes, status, started_at, completed_at
			)
			VALUES ($1, $2, $3, $4, $5, $6, $6, 'completed', now(), now())
		`, requestID, params.Source, remotePath, params.ExternalID, params.Directory,
			params.SizeBytes); err != nil {
			return fmt.Errorf("record the transfer of want %s: %w", params.TargetID, err)
		}
		if err := recordAcquiredFile(ctx, tx, RecordAcquiredFileParams{
			AcquisitionTargetID: params.TargetID,
			Provider:            params.Source,
			SourceUsername:      params.ExternalID,
			RemotePath:          remotePath,
			FileName:            params.FileName,
			SizeBytes:           params.SizeBytes,
			Verdict:             AcquiredFileFetching,
			Summary:             params.Summary,
			DownloadRequestID:   uuid.NullUUID{UUID: requestID, Valid: true},
		}); err != nil {
			return err
		}
		return q.WithTx(tx).QueueDownloadImport(ctx, requestID)
	})
	return requestID, err
}
