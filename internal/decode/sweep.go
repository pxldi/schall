package decode

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/db"
	"github.com/rs/zerolog"
)

// sweepBatch is how many files one pass decodes. Each one is a whole decode, so
// a pass is short and schedules the next one.
const sweepBatch = 10

// Store is what the sweep reads and writes.
type Store interface {
	FilesToDecodeCheck(ctx context.Context, limit int32) ([]db.FileToDecodeCheckRow, error)
	RecordDecodeCheck(ctx context.Context, fileID uuid.UUID, decodeError string) error
}

// FileChecker decodes one file.
type FileChecker interface {
	Available() bool
	Binary() string
	Check(ctx context.Context, path string) (string, error)
}

// Service checks the library's files, a batch at a time.
type Service struct {
	store   Store
	checker FileChecker
	logger  zerolog.Logger
	// toldNobodyCanDecode keeps a machine without ffmpeg to one log line.
	toldNobodyCanDecode atomic.Bool
}

func NewService(store Store, checker FileChecker, logger zerolog.Logger) *Service {
	return &Service{store: store, checker: checker, logger: logger}
}

// Sweep decodes the files nobody has checked since they last changed and
// reports whether more are waiting.
//
// A check that could not run for one file records nothing for it and moves on.
// The pass reports more only when it recorded something, so a batch of files
// that cannot be read does not bring the pass straight back.
func (service *Service) Sweep(ctx context.Context) (bool, error) {
	if !service.checker.Available() {
		service.sayNobodyCanDecode()
		return false, nil
	}
	files, err := service.store.FilesToDecodeCheck(ctx, sweepBatch)
	if err != nil {
		return false, fmt.Errorf("list the files still to be decoded: %w", err)
	}
	recorded := 0
	for _, file := range files {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		decodeError, err := service.checker.Check(ctx, file.Path)
		if errors.Is(err, ErrUnavailable) {
			service.sayNobodyCanDecode()
			return false, nil
		}
		if err != nil {
			if ctx.Err() != nil {
				return false, ctx.Err()
			}
			service.logger.Warn().Err(err).Str("library_file_id", file.ID.String()).
				Msg("could not decode a library file to check it")
			continue
		}
		if decodeError != "" {
			service.logger.Info().Str("library_file_id", file.ID.String()).
				Str("decode_error", decodeError).Msg("a library file's audio is damaged")
		}
		if err := service.store.RecordDecodeCheck(ctx, file.ID, decodeError); err != nil {
			return false, fmt.Errorf("record the decode check of %s: %w", file.Path, err)
		}
		recorded++
	}
	return len(files) == sweepBatch && recorded > 0, nil
}

func (service *Service) sayNobodyCanDecode() {
	if service.toldNobodyCanDecode.CompareAndSwap(false, true) {
		service.logger.Info().Str("binary", service.checker.Binary()).
			Msg("audio will not be checked for damage: ffmpeg could not be found")
	}
}
