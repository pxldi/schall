package library

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ChosenCopy names one file a person chose to remove and the file of the same
// recording that stays.
type ChosenCopy struct {
	Remove string `json:"remove"`
	Keep   string `json:"keep"`
}

// ChosenCopyRemovalActor names who removed a file the person picked by name in
// a thread. The thread is the decision ADR 0021 asks for.
const ChosenCopyRemovalActor = "the person, in the thread about failed imports"

const chosenUnidentifiedReason = "the person chose to remove this copy; an import had proven it, " +
	"placed it and failed to record it, and another file of the recording was kept"

// errNotTheChosenCopy means the library no longer matches what the person was
// shown: a file moved or went, the kept file no longer holds a recording, or
// the removed file now answers other music.
var errNotTheChosenCopy = errors.New("the library no longer matches the copy the person chose")

// RemoveChosenCopies removes each file a person chose by path and matches the
// file kept beside it again, so it can take the track.
//
// A file that holds the kept file's recording goes through releaseCopy, the
// duplicates screen's step. A file with no identity row at all, which a failed
// import can leave, is removed through RemoveIf with the same checks made
// under the row lock. Anything else is left alone and reported.
func (remover *Remover) RemoveChosenCopies(ctx context.Context, chosen []ChosenCopy) (SweptDuplicates, error) {
	swept := SweptDuplicates{RemovedPaths: []string{}, Why: []string{}, StillOnDisc: []string{}}
	remover.RetryUnlinks(ctx)

	for _, pick := range chosen {
		path, stranded, err := remover.removeChosen(ctx, pick)
		if err != nil {
			swept.LeftAlone++
			swept.Why = append(swept.Why, fmt.Sprintf("%s: %s", pick.Remove, err))
			continue
		}
		swept.Resolved++
		swept.RemovedPaths = append(swept.RemovedPaths, path)
		if stranded {
			swept.StillOnDisc = append(swept.StillOnDisc, path)
		}
	}
	if len(swept.RemovedPaths) > 0 {
		remover.queueNotifyPlayer(ctx)
	}
	return swept, nil
}

func (remover *Remover) removeChosen(ctx context.Context, pick ChosenCopy) (string, bool, error) {
	var doomedID, keptID uuid.UUID
	var recordingID uuid.NullUUID
	var doomedIdentified bool
	err := remover.pool.QueryRow(ctx, `
		SELECT doomed.id, kept.id,
		       (SELECT i.musicbrainz_recording_id FROM library_file_identities i
		        WHERE i.library_file_id = kept.id AND i.kind = 'external'),
		       EXISTS (SELECT 1 FROM library_file_identities i WHERE i.library_file_id = doomed.id)
		FROM library_files doomed, library_files kept
		WHERE doomed.path = $1 AND doomed.missing_at IS NULL
		  AND kept.path = $2 AND kept.missing_at IS NULL
	`, pick.Remove, pick.Keep).Scan(&doomedID, &keptID, &recordingID, &doomedIdentified)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, errNotTheChosenCopy
	}
	if err != nil {
		return "", false, err
	}
	if !recordingID.Valid {
		return "", false, errNotTheChosenCopy
	}

	if doomedIdentified {
		gone, err := remover.releaseCopy(ctx, recordingID.UUID, keptID, doomedID, ChosenCopyRemovalActor)
		if err != nil {
			return "", false, err
		}
		if gone == nil {
			return "", false, errNotTheChosenCopy
		}
		if !gone.strandedOnDisc {
			discardEmptied(filepath.Dir(gone.path), gone.rootPath)
		}
		remover.judgeAgain(ctx, []uuid.UUID{keptID}, []heldCopy{*gone})
		return gone.path, gone.strandedOnDisc, nil
	}

	if _, err := os.Stat(pick.Keep); err != nil {
		return "", false, ErrKeeperMissing
	}
	path, err := remover.RemoveIf(ctx, doomedID, func(ctx context.Context, tx pgx.Tx) error {
		var still bool
		if err := tx.QueryRow(ctx, `
			SELECT NOT EXISTS (SELECT 1 FROM library_file_identities WHERE library_file_id = $1)
			   AND NOT EXISTS (SELECT 1 FROM track_mappings WHERE library_file_id = $1)
			   AND EXISTS (
			       SELECT 1 FROM library_files kept
			       JOIN library_file_identities i ON i.library_file_id = kept.id
			       WHERE kept.id = $2 AND kept.missing_at IS NULL
			         AND i.musicbrainz_recording_id = $3)
		`, doomedID, keptID, recordingID.UUID).Scan(&still); err != nil {
			return err
		}
		if !still {
			return errNotTheChosenCopy
		}
		// RemoveIf unlinks before it commits, so the licence only stands
		// once the audio is gone.
		_, err := tx.Exec(ctx, `
			INSERT INTO library_file_removals (
			    library_file_id, library_path, size_bytes, musicbrainz_recording_id,
			    kept_library_file_id, kept_path, removed_by, reason, unlinked_at
			)
			SELECT id, path, size_bytes, $2, $3, $4, $5, $6, now()
			FROM library_files WHERE id = $1
		`, doomedID, recordingID.UUID, keptID, pick.Keep,
			ChosenCopyRemovalActor, chosenUnidentifiedReason)
		return err
	})
	if err != nil {
		return "", false, err
	}
	remover.judgeAgain(ctx, []uuid.UUID{keptID}, nil)
	return path, false, nil
}
