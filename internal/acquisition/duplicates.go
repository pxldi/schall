package acquisition

import (
	"cmp"
	"context"
	"path/filepath"
	"slices"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/sources"
)

// duplicateSummary is what a set-aside copy says about itself.
const duplicateSummary = "Sounds the same as a better copy kept for this want. " +
	"Set aside, and its file is removed from the download folder."

// SetAsideDuplicates keeps the best held copy of each piece of audio a want
// holds and sets the others aside, for every want holding more than one.
//
// The owner asked for this on 2026-10-12: 32 wants held 4,490 of 6,349 fetched
// copies, most of them the same upload again. A set-aside copy is not a verdict
// about the music, and the copy kept beside it is still the question.
func (service *Service) SetAsideDuplicates(ctx context.Context) (int, error) {
	wants, err := service.store.WantsHoldingSeveralHeldCopies(ctx)
	if err != nil {
		return 0, err
	}
	total := 0
	for _, targetID := range wants {
		marked, err := service.setAsideDuplicatesOf(ctx, targetID)
		if err != nil {
			return total, err
		}
		total += marked
	}
	service.logger.Info().Int("wants", len(wants)).Int("copies", total).
		Msg("set aside duplicate copies")
	if total > 0 {
		service.announce()
	}
	return total, nil
}

// setAsideDuplicatesOf does that for one want.
func (service *Service) setAsideDuplicatesOf(ctx context.Context, targetID uuid.UUID) (int, error) {
	files, err := service.store.AcquisitionTargetFiles(ctx, targetID)
	if err != nil {
		return 0, err
	}
	held := make([]db.AcquisitionTargetFileRow, 0, len(files))
	for _, file := range files {
		if file.Verdict == db.AcquiredFileHeld && file.DecidedBy == "schall" {
			held = append(held, file)
		}
	}
	if len(held) < 2 {
		return 0, nil
	}
	// The store reads newest first; grouping reads arrival order.
	slices.SortStableFunc(held, func(a, b db.AcquisitionTargetFileRow) int {
		return a.DecidedAt.Compare(b.DecidedAt)
	})
	duplicates := Duplicates(held)
	marked, err := service.store.MarkCopiesDuplicate(ctx, targetID, duplicates, duplicateSummary)
	return int(marked), err
}

// Duplicates returns every copy but the best one in each group of copies that
// sound the same. A copy with no fingerprint is a group of its own and is
// always kept.
//
// The best copy is the lossless one, then the one with the highest bit rate,
// then the one that arrived first. Which copy is kept changes nothing about
// what the audio is.
func Duplicates(rows []db.AcquisitionTargetFileRow) []uuid.UUID {
	byID := make(map[uuid.UUID]db.AcquisitionTargetFileRow, len(rows))
	for _, row := range rows {
		byID[row.ID] = row
	}
	var duplicates []uuid.UUID
	for _, group := range GroupCopies(rows) {
		if len(group.CopyIDs) < 2 {
			continue
		}
		best := slices.MaxFunc(group.CopyIDs, func(a, b uuid.UUID) int {
			return betterCopy(byID[a], byID[b])
		})
		for _, id := range group.CopyIDs {
			if id != best {
				duplicates = append(duplicates, id)
			}
		}
	}
	return duplicates
}

// betterCopy orders two copies of the same audio, the better one greater.
func betterCopy(a, b db.AcquisitionTargetFileRow) int {
	lossless := func(row db.AcquisitionTargetFileRow) int {
		if sources.Lossless(filepath.Ext(row.FileName)) {
			return 1
		}
		return 0
	}
	bitRate := func(row db.AcquisitionTargetFileRow) int {
		if row.Evidence == nil {
			return 0
		}
		return row.Evidence.BitRate
	}
	return cmp.Or(
		cmp.Compare(lossless(a), lossless(b)),
		cmp.Compare(bitRate(a), bitRate(b)),
		// Earlier is better, so the comparison runs the other way.
		b.DecidedAt.Compare(a.DecidedAt),
	)
}
