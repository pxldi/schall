package identity

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pxldi/schall/internal/musicbrainz"
	"github.com/pxldi/schall/internal/tagmatch"
)

// SourceWant is the want a source-identified file's copy was fetched for: the
// entry it was asked for in, and the recordings somebody said it is not.
type SourceWant struct {
	Entry    Evidence
	Excluded []uuid.UUID
}

// SourceRecheck is what asking again about a file with an automatic source
// identity came to (ADR 0037 §6).
type SourceRecheck struct {
	// Moved says the file's identity is now an external one on RecordingID.
	Moved       bool
	RecordingID uuid.UUID
	// Contradiction is what was found against the source identity. The file is
	// left as it is.
	Contradiction string
}

// automaticSource is the source identity a file holds when nobody wrote it by
// hand.
type automaticSource struct {
	source, externalID, externalURL string
	// isrc is the code the Deezer preview was fetched by. A Topic upload has
	// none.
	isrc string
}

// RecheckSource asks again what a file with an automatic source identity is.
//
// The file is resolved the ordinary way, from its own tags and its audio, and
// the want by its entry, as an unresolved want is. The file moves onto a
// recording when its own resolution proves one, or, where it proves nothing,
// when the want resolves to a recording that carries the ISRC the file's Deezer
// preview was fetched by (ADR 0024). A Topic upload was found by name and names
// no recording, so it waits for the file's own proof.
//
// A contradiction leaves the file as it is and is returned to be recorded: the
// file's own evidence in conflict, a recording that does not carry the
// preview's ISRC or the entry's, or a file and an entry that resolve to two
// recordings. A person's source identity is never asked about again (ADR 0026
// §2), and this does nothing to one.
func (service *Service) RecheckSource(
	ctx context.Context, fileID uuid.UUID, want *SourceWant,
) (SourceRecheck, error) {
	source, found, err := service.automaticSource(ctx, fileID)
	if err != nil || !found {
		return SourceRecheck{}, err
	}
	file, err := service.load(ctx, fileID)
	if errors.Is(err, pgx.ErrNoRows) {
		return SourceRecheck{}, nil
	}
	if err != nil {
		return SourceRecheck{}, err
	}
	if file.missing {
		return SourceRecheck{}, nil
	}

	outcome, err := service.resolver.Resolve(ctx, file.evidence)
	if err != nil {
		return SourceRecheck{}, err
	}
	outcome, err = service.listenFor(ctx, fileID, file, outcome)
	if err != nil {
		return SourceRecheck{}, err
	}
	var entry Outcome
	if want != nil {
		entry, err = service.resolver.Resolve(ctx, want.Entry, want.Excluded...)
		if err != nil {
			return SourceRecheck{}, err
		}
	}
	entryISRC := ""
	if want != nil {
		entryISRC = want.Entry.ISRC
	}

	if outcome.Status == StatusConflict {
		return SourceRecheck{Contradiction: outcome.Summary}, nil
	}
	if outcome.Status == StatusResolved && outcome.Identity != nil {
		proven := *outcome.Identity
		if entry.Status == StatusResolved && entry.Identity != nil &&
			entry.Identity.RecordingID != proven.RecordingID {
			return SourceRecheck{Contradiction: fmt.Sprintf(
				"The file resolves to %q by %s, and its entry to another recording.",
				proven.TrackTitle, credited(proven.ArtistName))}, nil
		}
		recording, err := service.resolver.provider.Recording(ctx, proven.RecordingID)
		if err != nil {
			return SourceRecheck{}, fmt.Errorf("look up recording %s: %w", proven.RecordingID, err)
		}
		if contradiction := isrcContradiction(recording, source.isrc, entryISRC); contradiction != "" {
			return SourceRecheck{Contradiction: contradiction}, nil
		}
		return service.moveOntoRecording(ctx, fileID, source, proven)
	}

	if entry.Status != StatusResolved || entry.Identity == nil {
		return SourceRecheck{}, nil
	}
	named := *entry.Identity
	recording, err := service.resolver.provider.Recording(ctx, named.RecordingID)
	if errors.Is(err, musicbrainz.ErrNotFound) {
		return SourceRecheck{}, nil
	}
	if err != nil {
		return SourceRecheck{}, fmt.Errorf("look up recording %s: %w", named.RecordingID, err)
	}
	if contradiction := isrcContradiction(recording, source.isrc, entryISRC); contradiction != "" {
		return SourceRecheck{Contradiction: contradiction}, nil
	}
	// The preview was published for this recording only when the recording
	// carries the ISRC it was fetched by. Silence on either side is not that.
	if source.source != AnchorSourceDeezer ||
		compareISRC(source.isrc, recording.ISRCs) != tagmatch.Agrees {
		return SourceRecheck{}, nil
	}
	named.Method = MethodPublishedSample
	named.Confidence = bySample.confidence()
	named.Summary = fmt.Sprintf("%q by %s — %s.", named.TrackTitle, credited(named.ArtistName),
		bySample.summary())
	named.Evidence = []string{"audio (matches the published sample of this recording)", "ISRC"}
	return service.moveOntoRecording(ctx, fileID, source, named)
}

// isrcContradiction says why a recording cannot be the track a file was proven
// to be, when it carries ISRCs and none of them is the preview's or the
// entry's. A recording with none, or an empty code, is silence.
func isrcContradiction(recording musicbrainz.Recording, codes ...string) string {
	for _, code := range codes {
		if compareISRC(code, recording.ISRCs) == tagmatch.Differs {
			return fmt.Sprintf("MusicBrainz names %q by %s, which does not carry the ISRC %s.",
				recording.Title, credited(recording.ArtistCredit), code)
		}
	}
	return ""
}

// automaticSource reads the source identity a file holds, when nobody wrote it
// by hand.
func (service *Service) automaticSource(
	ctx context.Context, fileID uuid.UUID,
) (automaticSource, bool, error) {
	var source automaticSource
	err := service.pool.QueryRow(ctx, `
		SELECT source, external_id, coalesce(external_url, ''), coalesce(isrc, '')
		FROM library_file_identities
		WHERE library_file_id = $1 AND kind = 'source' AND NOT is_manual
	`, fileID).Scan(&source.source, &source.externalID, &source.externalURL, &source.isrc)
	if errors.Is(err, pgx.ErrNoRows) {
		return automaticSource{}, false, nil
	}
	if err != nil {
		return automaticSource{}, false, fmt.Errorf("read the source identity of %s: %w", fileID, err)
	}
	return source, true, nil
}

// moveOntoRecording replaces a file's automatic source identity with an
// external identity on a recording, and gives the want whose copy it is the
// same recording, in one transaction. The source key goes into the new
// identity's evidence, so the address is kept.
//
// The want keeps no recording where another live want already has this one:
// one want per recording is what the schema holds, and that want is settled
// against this file by the library, as any want is.
func (service *Service) moveOntoRecording(
	ctx context.Context, fileID uuid.UUID, source automaticSource, found Identity,
) (SourceRecheck, error) {
	found.Evidence = append(append([]string{}, found.Evidence...),
		"source: "+source.externalID+" "+source.externalURL)

	transaction, err := service.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return SourceRecheck{}, err
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	// The delete is the check. A person who decided about the file meanwhile
	// wrote a manual identity, which this deletes nothing of, and theirs stands.
	tag, err := transaction.Exec(ctx, `
		DELETE FROM library_file_identities
		WHERE library_file_id = $1 AND kind = 'source' AND NOT is_manual
		  AND external_id = $2
	`, fileID, source.externalID)
	if err != nil {
		return SourceRecheck{}, err
	}
	if tag.RowsAffected() == 0 {
		return SourceRecheck{}, nil
	}
	if err := insertIdentity(ctx, transaction, fileID, found, false); err != nil {
		return SourceRecheck{}, fmt.Errorf("record %s as recording %s: %w", fileID, found.RecordingID, err)
	}
	if _, err := transaction.Exec(ctx, `
		UPDATE library_files
		SET resolution_status = 'resolved', resolution_summary = $2,
		    resolution_error = NULL, resolution_attempted_at = now(), updated_at = now()
		WHERE id = $1
	`, fileID, found.Summary); err != nil {
		return SourceRecheck{}, err
	}
	if err := queueReleaseGroupIngest(ctx, transaction, found.ReleaseGroupID); err != nil {
		return SourceRecheck{}, err
	}
	if _, err := transaction.Exec(ctx, `
		UPDATE acquisition_targets
		SET musicbrainz_recording_id = $2, musicbrainz_release_group_id = $3,
		    resolution_method = $4, resolved_at = now(), updated_at = now()
		WHERE id = (
		    SELECT id FROM acquisition_targets
		    WHERE acquired_library_file_id = $1
		      AND status = 'acquired'
		      AND musicbrainz_recording_id IS NULL
		    ORDER BY acquired_at, id
		    LIMIT 1
		)
		  AND NOT EXISTS (
		      SELECT 1 FROM acquisition_targets others
		      WHERE others.musicbrainz_recording_id = $2 AND others.status <> 'superseded'
		  )
	`, fileID, found.RecordingID, nullableUUID(found.ReleaseGroupID), found.Method); err != nil {
		return SourceRecheck{}, fmt.Errorf("give the want of %s recording %s: %w",
			fileID, found.RecordingID, err)
	}
	if err := transaction.Commit(ctx); err != nil {
		return SourceRecheck{}, err
	}
	if err := service.reconcile(ctx, fileID); err != nil {
		return SourceRecheck{}, err
	}
	return SourceRecheck{Moved: true, RecordingID: found.RecordingID}, nil
}
