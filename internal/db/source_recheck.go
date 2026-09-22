package db

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// SourceRecheckRow is one library file with an automatic source identity whose
// turn it is to be asked about again (ADR 0037 §6), and the want its copy was
// fetched for, when that want is still waiting for a recording.
type SourceRecheckRow struct {
	LibraryFileID uuid.UUID
	// Attempts is how many times the file has been asked about again, which is
	// the rung of the unfound ladder it is on.
	Attempts int32
	// TargetID is the acquired want whose file this is and which names no
	// recording yet. It is invalid where there is none, and then only the file
	// is asked about.
	TargetID        uuid.NullUUID
	EntryArtist     string
	EntryTitle      string
	EntryAlbum      string
	EntryISRC       string
	EntryDurationMS int
	EntryExplicit   bool
}

// DueSourceRechecks returns the files with an automatic source identity that
// are due to be asked about again, oldest schedule first. A file that has gone
// missing is left out: there is nothing to read.
func (q *Queries) DueSourceRechecks(
	ctx context.Context, now time.Time, limit int32,
) ([]SourceRecheckRow, error) {
	rows, err := q.db.Query(ctx, `
		SELECT identities.library_file_id, identities.source_recheck_attempts,
		       want.id, coalesce(want.entry_artist, ''), coalesce(want.entry_title, ''),
		       coalesce(want.entry_album, ''), coalesce(want.entry_isrc, ''),
		       coalesce(want.entry_duration_ms, 0), coalesce(want.entry_explicit, false)
		FROM library_file_identities identities
		JOIN library_files files ON files.id = identities.library_file_id
		LEFT JOIN LATERAL (
			SELECT targets.id, targets.entry_artist, targets.entry_title,
			       targets.entry_album, targets.entry_isrc, targets.entry_duration_ms,
			       targets.entry_explicit
			FROM acquisition_targets targets
			WHERE targets.acquired_library_file_id = identities.library_file_id
			  AND targets.status = 'acquired'
			  AND targets.musicbrainz_recording_id IS NULL
			ORDER BY targets.acquired_at, targets.id
			LIMIT 1
		) want ON true
		WHERE identities.kind = 'source'
		  AND NOT identities.is_manual
		  AND identities.source_recheck_after <= $1
		  AND files.missing_at IS NULL
		ORDER BY identities.source_recheck_after, identities.library_file_id
		LIMIT $2
	`, now, limit)
	if err != nil {
		return nil, fmt.Errorf("list the source files due another look: %w", err)
	}
	defer rows.Close()

	due := make([]SourceRecheckRow, 0)
	for rows.Next() {
		var row SourceRecheckRow
		if err := rows.Scan(
			&row.LibraryFileID, &row.Attempts, &row.TargetID,
			&row.EntryArtist, &row.EntryTitle, &row.EntryAlbum, &row.EntryISRC,
			&row.EntryDurationMS, &row.EntryExplicit,
		); err != nil {
			return nil, err
		}
		due = append(due, row)
	}
	return due, rows.Err()
}

// NextSourceRecheckDue reports when the earliest file with an automatic source
// identity is due to be asked about again. An invalid timestamp means none is.
func (q *Queries) NextSourceRecheckDue(ctx context.Context) (pgtype.Timestamptz, error) {
	var due pgtype.Timestamptz
	err := q.db.QueryRow(ctx, `
		SELECT min(identities.source_recheck_after)
		FROM library_file_identities identities
		JOIN library_files files ON files.id = identities.library_file_id
		WHERE identities.source_recheck_after IS NOT NULL
		  AND files.missing_at IS NULL
	`).Scan(&due)
	return due, err
}

// RescheduleSourceRecheck records what asking about a source file again came
// to, and when to ask next. counted says whether the ask happened: a provider
// that refused to be asked at all moves the time and not the rung.
// contradiction is what the ask found against the identity, and empty clears
// what an earlier ask found. A file whose identity is no longer an automatic
// source identity is left alone.
func (q *Queries) RescheduleSourceRecheck(
	ctx context.Context, fileID uuid.UUID, next time.Time, counted bool, contradiction string,
) error {
	_, err := q.db.Exec(ctx, `
		UPDATE library_file_identities
		SET source_recheck_after = $2,
		    source_recheck_attempts = source_recheck_attempts
		        + CASE WHEN $3::boolean THEN 1 ELSE 0 END,
		    source_contradiction = CASE
		        WHEN $3::boolean THEN nullif($4, '')
		        ELSE source_contradiction
		    END,
		    updated_at = now()
		WHERE library_file_id = $1 AND kind = 'source' AND NOT is_manual
	`, fileID, next, counted, contradiction)
	if err != nil {
		return fmt.Errorf("reschedule the next look at source file %s: %w", fileID, err)
	}
	return nil
}
