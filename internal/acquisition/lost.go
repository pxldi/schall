package acquisition

import (
	"context"
	"fmt"

	"github.com/pxldi/schall/internal/db"
)

// lostWantsReview bounds how many settled wants one sweep looks at again, and
// lostWantsRepair how many the first sweep of a process does. The list query is
// cheap; a want with no copy can cost a cached MusicBrainz lookup.
const (
	lostWantsReview = 50
	lostWantsRepair = 500
)

// lostSummary is what a reopened want says until the sweep has judged it.
const lostSummary = "Library now files this song's file as another song. Checking again."

// reviewLostWants looks again at the settled wants whose file the library has
// since filed under another MusicBrainz row.
//
// Such a want stayed acquired over a file that answers another row, and its
// track read "Fetched once, file gone" over a file that is there. 69 of them on
// 2026-10-14, most after AcoustID filed the file under another row.
//
// A want holding a copy of its own goes back through the filing rules, the same
// ones a copy arriving today meets: it settles where the copy's proof or
// MusicBrainz says the two rows are one song, and stops for a person in Review
// otherwise. It never searches, because the copy is on the disc.
//
// A want with no copy settled on the library's word alone. Where MusicBrainz
// proves the row the file answers is a second row of the wanted one (ADR 0031),
// the file counts for the want's row too and the want stays settled.
//
// Each sweep reads only the wants holding a copy whose file was answered again
// since they settled (see db.LostWants). The first sweep of a process reads them
// all, once, which is how the wants lost before this existed are repaired and
// how a want with no copy is looked at.
func (service *Service) reviewLostWants(ctx context.Context, changedOnly bool) error {
	limit := int32(lostWantsReview)
	if !changedOnly {
		limit = lostWantsRepair
	}
	lost, err := service.store.LostWants(ctx, limit, changedOnly)
	if err != nil {
		return err
	}
	changed := false
	for _, want := range lost {
		if ctx.Err() != nil {
			break
		}
		if want.HoldsCopy {
			reopened, err := service.store.ReopenLostWant(ctx, want.Target.ID, lostSummary)
			if err != nil {
				return err
			}
			if reopened {
				service.logger.Info().
					Str("acquisition_target_id", want.Target.ID.String()).
					Msg("a settled want's file is filed as another song, so its copy is judged again")
				changed = true
			}
			continue
		}
		settled, err := service.settleLostOnSecondRow(ctx, want.Target)
		if err != nil {
			service.logger.Error().Err(err).
				Str("acquisition_target_id", want.Target.ID.String()).
				Msg("ask whether a settled want's file is a second row of its song")
			continue
		}
		changed = changed || settled
	}
	if changed {
		service.announce()
	}
	return nil
}

// settleLostOnSecondRow keeps a want with no copy settled when the library holds
// its song under a second MusicBrainz row, and records that row on the file.
func (service *Service) settleLostOnSecondRow(
	ctx context.Context, target db.AcquisitionTargetRow,
) (bool, error) {
	fileID, err := service.ownedSecondRow(ctx, target)
	if err != nil || !fileID.Valid {
		return false, err
	}
	if err := service.store.RecordSecondRow(
		ctx, fileID.UUID, target.MusicBrainzRecordingID.UUID); err != nil {
		return false, err
	}
	if err := service.store.MoveSettledFile(
		ctx, target.ID, fileID.UUID, acquiredSummary); err != nil {
		return false, fmt.Errorf("point want %s at its second row: %w", target.ID, err)
	}
	service.logger.Info().
		Str("acquisition_target_id", target.ID.String()).
		Str("library_file_id", fileID.UUID.String()).
		Msg("a settled want's song is held under a second MusicBrainz row")
	if service.matcher != nil {
		if err := service.matcher.ReconcileFile(ctx, fileID.UUID); err != nil {
			service.logger.Error().Err(err).
				Str("library_file_id", fileID.UUID.String()).
				Msg("offer a file held under a second row to the catalogue")
		}
	}
	return true, nil
}
