package acquisition

import "context"

// lostWantsReview bounds how many settled wants one sweep looks at again, and
// lostWantsRepair how many the first sweep of a process does. The list query is
// cheap, and a reopened want is judged by the sweep's own bounded batch.
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
// Each is put back in the queue and judged by the same pass as a want arriving
// today. One holding a copy of its own goes through the filing rules: it settles
// where the copy's proof or MusicBrainz says the two rows are one song, and
// stops for a person in Review otherwise. It never searches, because the copy
// is on the disc. One with no copy settled on the library's word alone. It
// settles again where the library holds its song, under its own row or a second
// row MusicBrainz proves (ADR 0031), and is looked for otherwise: the library's
// answer about that file is no longer the song, and only a copy AcoustID
// proves will settle it (Poldi chose this on 2026-10-14).
//
// Each sweep reads only the wants whose file was answered again since they
// settled (see db.LostWants). The first sweep of a process reads them all, once,
// which is how the wants lost before this existed are repaired.
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
		reopened, err := service.store.ReopenLostWant(ctx, want.Target.ID, lostSummary)
		if err != nil {
			return err
		}
		if reopened {
			service.logger.Info().
				Str("acquisition_target_id", want.Target.ID.String()).
				Bool("holds_copy", want.HoldsCopy).
				Msg("a settled want's file is filed as another song, so the want is judged again")
			changed = true
		}
	}
	if changed {
		service.announce()
	}
	return nil
}
