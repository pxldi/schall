package library

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/chromaprint"
	"github.com/rs/zerolog"
)

// WitnessFingerprintSweep computes the full-length acoustic fingerprints used by
// library witnesses. The job queue calls Fingerprint once per file so a large
// library never holds one worker for the whole pass.
type WitnessFingerprintSweep struct {
	pool   *pgxpool.Pool
	prints *chromaprint.Fingerprinter
	logger zerolog.Logger
}

func NewWitnessFingerprintSweep(
	pool *pgxpool.Pool, prints *chromaprint.Fingerprinter, logger zerolog.Logger,
) *WitnessFingerprintSweep {
	return &WitnessFingerprintSweep{pool: pool, prints: prints, logger: logger}
}

// Fingerprint computes one file's fingerprint over the existing 900-second cap
// and keeps the decoded coverage beside it. A file that disappeared while its
// job waited is no work, and a missing fpcalc result stays retryable.
func (sweep *WitnessFingerprintSweep) Fingerprint(ctx context.Context, fileID uuid.UUID) error {
	var path string
	var updatedAt time.Time
	err := sweep.pool.QueryRow(ctx, `
		SELECT path, updated_at
		FROM library_files
		WHERE id = $1 AND missing_at IS NULL
		  AND witness_fingerprint IS NULL
	`, fileID).Scan(&path, &updatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read library file %s for witness fingerprint: %w", fileID, err)
	}
	if sweep.prints == nil || !sweep.prints.Available() {
		return chromaprint.ErrNoFpcalc
	}
	value, seconds, err := sweep.prints.Compute(ctx, path, chromaprint.CandidateLengthSeconds)
	if err != nil {
		return fmt.Errorf("fingerprint library file %s: %w", fileID, err)
	}
	if value == "" || seconds <= 0 {
		return fmt.Errorf("fingerprint library file %s: %w", fileID, chromaprint.ErrNoFingerprint)
	}
	kept := strconv.Itoa(seconds) + ":" + value
	_, err = sweep.pool.Exec(ctx, `
		UPDATE library_files
		SET witness_fingerprint = $2, witness_fingerprint_seconds = $3, updated_at = now()
		WHERE id = $1 AND missing_at IS NULL AND witness_fingerprint IS NULL
		  AND updated_at = $4
	`, fileID, kept, seconds, updatedAt)
	if err != nil {
		return fmt.Errorf("keep the witness fingerprint of library file %s: %w", fileID, err)
	}
	sweep.logger.Debug().Str("file_id", fileID.String()).Int("seconds", seconds).
		Msg("library witness fingerprinted")
	return nil
}
