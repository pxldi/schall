// Package upgrade looks for library files whose audio was acquired before a
// bit-rate floor was set, or before it was raised, and asks the ordinary
// acquisition loop for a better copy of each one.
//
// It decides nothing about what a file is. A file only qualifies here once
// something has already proven what recording it holds — the same settled
// proofs internal/db/witness.go reads for a library witness — and once it
// qualifies, the only thing this package does is raise a want. Whether a copy
// that arrives for that want is the right recording is proven exactly as
// every other want is proven: AcoustID at the leading cluster, the anchor, or
// a settled library witness. This package widens what gets looked for. It
// admits nothing that the matching rules did not already admit.
package upgrade

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/sources"
	"github.com/rs/zerolog"
)

// sweepBatch is how many candidates one pass reads before deciding whether to
// come back for more. It is a page of the keyset walk, not a bound on how many
// wants get raised — a page mostly above the floor costs one query and the
// pass reads the next page in the same turn.
const sweepBatch = 200

// Store is what the sweep and the count both read and write. It is the whole
// of this package's reach into the database.
type Store interface {
	UpgradeSettings(context.Context) (db.UpgradeSettingsRow, error)
	SaveUpgradeSettings(ctx context.Context, enabled bool) (db.UpgradeSettingsRow, error)
	SourcePreferences(context.Context) (db.SourcePreferencesRow, error)
	UpgradeCandidates(ctx context.Context, after uuid.UUID, limit int32) ([]db.UpgradeCandidateRow, error)
	CreateUpgradeTarget(context.Context, db.CreateUpgradeTargetParams) (uuid.UUID, bool, error)
	QueueUpgradeSweep(ctx context.Context, runAfter time.Time, payload json.RawMessage) error
}

// Service walks the library's files, a batch at a time, and raises a want for
// each one whose stored quality is under the floor the user set.
type Service struct {
	store  Store
	logger zerolog.Logger
}

func NewService(store Store, logger zerolog.Logger) *Service {
	return &Service{store: store, logger: logger}
}

// Settings reads whether the sweep is switched on.
func (service *Service) Settings(ctx context.Context) (db.UpgradeSettingsRow, error) {
	return service.store.UpgradeSettings(ctx)
}

// SaveSettings stores the switch.
func (service *Service) SaveSettings(ctx context.Context, enabled bool) (db.UpgradeSettingsRow, error) {
	return service.store.SaveUpgradeSettings(ctx, enabled)
}

// ScanNow asks for a pass over the whole library straight away, from the
// beginning, rather than waiting for whatever pass is already on the books to
// come round to it. It is the "Scan now" button's whole implementation: the
// pass it queues is the ordinary sweep, running once immediately instead of
// after upgradeIdle.
func (service *Service) ScanNow(ctx context.Context) error {
	return service.store.QueueUpgradeSweep(ctx, time.Now(), json.RawMessage(`{}`))
}

// Result is what one pass found.
type Result struct {
	// Looked is how many candidates this pass read.
	Looked int
	// Raised is how many of them got a want — new or, for a recording already
	// acquired, superseding the target that acquired it.
	Raised int
	// Next is where the following pass should start. It is the zero UUID once
	// the walk reached the end of the library.
	Next uuid.UUID
	// More says whether this pass filled its page, which is what the caller
	// reads to decide whether to come straight back rather than wait.
	More bool
}

// Sweep reads one page of candidates starting after the given file id and
// raises a want for every one under the floor.
//
// A pass that is switched off, or whose preferences state no floor at all,
// reads nothing: there is no question to ask a file that no floor applies to,
// so the walk is skipped rather than run to conclude nothing for every file in
// the library.
func (service *Service) Sweep(ctx context.Context, after uuid.UUID) (Result, error) {
	settings, err := service.store.UpgradeSettings(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("read the upgrade settings: %w", err)
	}
	if !settings.Enabled {
		return Result{}, nil
	}
	preferences, err := loadPreferences(ctx, service.store)
	if err != nil {
		return Result{}, err
	}
	if !hasAFloor(preferences) {
		return Result{}, nil
	}

	candidates, err := service.store.UpgradeCandidates(ctx, after, sweepBatch)
	if err != nil {
		return Result{}, fmt.Errorf("list upgrade candidates: %w", err)
	}
	result := Result{Looked: len(candidates), More: len(candidates) == sweepBatch}
	for _, candidate := range candidates {
		result.Next = candidate.FileID
		if !belowFloor(candidate, preferences) {
			continue
		}
		raised, err := service.raise(ctx, candidate)
		if err != nil {
			return result, err
		}
		if raised {
			result.Raised++
		}
	}
	return result, nil
}

// CountBelowFloor walks the whole library and counts how many files are under
// the floor right now. It reads through the same candidates query the sweep
// uses, a page at a time, so "how many" and "which ones get a want" can never
// answer two different questions.
//
// It is a full walk rather than a stored count because the floor a person set
// can change between two presses of "Scan now", and a stale count would be
// read as a promise about files that no longer qualify.
func (service *Service) CountBelowFloor(ctx context.Context) (int, error) {
	preferences, err := loadPreferences(ctx, service.store)
	if err != nil {
		return 0, err
	}
	if !hasAFloor(preferences) {
		return 0, nil
	}
	var (
		count int
		after uuid.UUID
	)
	for {
		candidates, err := service.store.UpgradeCandidates(ctx, after, sweepBatch)
		if err != nil {
			return 0, fmt.Errorf("list upgrade candidates: %w", err)
		}
		for _, candidate := range candidates {
			after = candidate.FileID
			if belowFloor(candidate, preferences) {
				count++
			}
		}
		if len(candidates) < sweepBatch {
			return count, nil
		}
		if ctx.Err() != nil {
			return count, ctx.Err()
		}
	}
}

// raiseSummary is the sentence a raised want carries. It names why the want
// exists rather than what will happen to it, which is exactly what every other
// want's initial summary says.
const raiseSummary = "the copy the library holds is below the bit rate you asked for"

// raise asks for an upgrade want to exist for one candidate and reports
// whether one now does — false covers both a manual "not wanted" decision and
// a live want the recording already had for some other reason, neither of
// which this sweep raised.
func (service *Service) raise(ctx context.Context, candidate db.UpgradeCandidateRow) (bool, error) {
	params := db.CreateUpgradeTargetParams{
		LibraryFileID: candidate.FileID,
		RecordingID:   candidate.RecordingID,
		EntryArtist:   candidate.ArtistName,
		EntryTitle:    candidate.TrackTitle,
		EntryAlbum:    candidate.ReleaseTitle,
		Summary:       raiseSummary,
	}
	if candidate.DurationMS > 0 {
		params.EntryDurationMS = pgtype.Int4{Int32: int32(candidate.DurationMS), Valid: true}
	}
	if candidate.ISRC != "" {
		params.EntryISRC = pgtype.Text{String: candidate.ISRC, Valid: true}
	}
	_, created, err := service.store.CreateUpgradeTarget(ctx, params)
	if errors.Is(err, db.ErrRecordingNotWanted) {
		service.logger.Debug().
			Str("library_file_id", candidate.FileID.String()).
			Str("musicbrainz_recording_id", candidate.RecordingID.String()).
			Msg("a file is below the bit rate floor, but somebody already said not to pursue its recording")
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("raise an upgrade want for file %s: %w", candidate.FileID, err)
	}
	return created, nil
}

func loadPreferences(ctx context.Context, store Store) (sources.Preferences, error) {
	row, err := store.SourcePreferences(ctx)
	if err != nil {
		return sources.Preferences{}, fmt.Errorf("read the stored format preferences: %w", err)
	}
	floors := make(map[string]int, len(row.FormatMinimumBitRate))
	for format, floor := range row.FormatMinimumBitRate {
		floors[format] = int(floor)
	}
	return sources.Preferences{
		MinimumBitRate:       int(row.MinimumBitRate),
		FormatMinimumBitRate: floors,
	}, nil
}

// hasAFloor reports whether these preferences say anything a stored file could
// be measured against. Preferences stated only as a preferred order, or as
// formats refused outright, set no bit-rate floor and the sweep has nothing to
// ask.
func hasAFloor(preferences sources.Preferences) bool {
	return preferences.MinimumBitRate > 0 || len(preferences.FormatMinimumBitRate) > 0
}

// belowFloor asks the one question this package ever asks about a file's
// quality, through the same rule that refuses a source candidate under the
// floor (internal/sources/preferences.go). A lossless file is never below the
// floor: BelowFloor already reads no format that keeps every bit of the
// recording as one that can be under anything.
func belowFloor(candidate db.UpgradeCandidateRow, preferences sources.Preferences) bool {
	return preferences.BelowFloor(format(candidate.Path), candidate.BitRateKbps)
}
