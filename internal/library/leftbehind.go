package library

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/db"
)

// LeftBehindRemovalActor names who removed a copy a failed import left behind.
// The person chose this cleanup on 2026-10-16 (thread "Import scheitert an
// doppeltem Pfad"), which is the decision ADR 0021 asks for.
const LeftBehindRemovalActor = "the cleanup of copies failed imports left behind"

// ClearLeftBehindCopies removes the extra copies db.LeftBehindCopies lists and
// keeps one file of each recording.
//
// Each file goes through releaseCopy, the same step the duplicates screen
// takes: the licence is written first, and a file that stopped being a copy of
// the recording, or also answers other music, is left alone. The keeper is
// then matched again so it can take the track the extras kept it from.
func (remover *Remover) ClearLeftBehindCopies(ctx context.Context) (SweptDuplicates, error) {
	swept := SweptDuplicates{RemovedPaths: []string{}, Why: []string{}, StillOnDisc: []string{}}

	groups, err := db.New(remover.pool).LeftBehindCopies(ctx)
	if err != nil {
		return swept, err
	}
	remover.RetryUnlinks(ctx)

	for _, group := range groups {
		var removed []heldCopy
		var failed error
		for _, extra := range group.Extras {
			gone, err := remover.releaseCopy(ctx, group.RecordingID, group.KeeperID, extra,
				LeftBehindRemovalActor)
			if err != nil {
				failed = err
				break
			}
			if gone == nil {
				continue
			}
			removed = append(removed, *gone)
			swept.RemovedPaths = append(swept.RemovedPaths, gone.path)
			if gone.strandedOnDisc {
				swept.StillOnDisc = append(swept.StillOnDisc, gone.path)
				continue
			}
			discardEmptied(filepath.Dir(gone.path), gone.rootPath)
		}
		if len(removed) > 0 {
			remover.judgeAgain(ctx, []uuid.UUID{group.KeeperID}, removed)
		}
		if failed != nil {
			swept.LeftAlone++
			swept.Why = append(swept.Why, fmt.Sprintf("recording %s: %s", group.RecordingID, failed))
			continue
		}
		swept.Resolved++
	}
	if len(swept.RemovedPaths) > 0 {
		remover.queueNotifyPlayer(ctx)
	}
	return swept, nil
}
