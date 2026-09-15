package library

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/identity"
	"github.com/pxldi/schall/internal/sources"
)

// upgradeRemovedBy names who a deletion this feature carries out is on record
// as, in the shape removedBy names the duplicates screen's press.
const upgradeRemovedBy = "the automatic quality upgrade"

// upgradeReplacedReason and upgradeDiscardedReason are what the licence row
// says about why a file went: replaced by something better, or discarded for
// not being better than the file it was fetched to replace.
const (
	upgradeReplacedReason  = "a better copy of this recording was verified and replaced this one"
	upgradeDiscardedReason = "this copy was fetched for an upgrade want and was not better than the file it was meant to replace"
	uploadRemovedBy        = "the person who uploaded a better copy"
	uploadReplacedReason   = "a better copy of this recording was uploaded and replaced this one"
)

// ErrReplacementMissing means the copy ReplaceIfBetter was asked to weigh is
// not actually readable on disk — its library row says it exists, but the
// audio underneath it does not, or cannot be opened. Licensing a deletion on
// the strength of a copy that is not really there would destroy music for
// nothing, so nothing is deleted or discarded when this is returned.
var ErrReplacementMissing = errors.New("the replacement copy is not readable on disk")

// upgradeCandidate is what ReplaceIfBetter needs to know about one side of the
// comparison.
type upgradeCandidate struct {
	id          uuid.UUID
	path        string
	rootPath    string
	size        int64
	bitRateKbps int
	recordingID uuid.NullUUID
	present     bool
}

// ReplaceIfBetter carries out what a proven upgrade want exists for.
//
// It decides nothing about what either file is: that was proven already, by
// the same rules that prove every file — a settled library witness put the old
// file below the floor onto the want in the first place, and the copy that
// filled the want was verified by AcoustID, the anchor, or that same witness
// before it ever reached here. All this does is ask whether the new file is
// actually a better copy of the music both files already agree they are, and
// carry out a deletion under a recorded licence either way.
//
// "Better" is deliberately narrow: a lossless file beats a lossy one outright,
// and between two lossy files a higher bitrate wins. A lower bitrate, or the
// same format at the same bitrate, is not better. In that case the old file
// is kept — but the new copy is not left standing beside it either: the want
// wanted an upgrade, not a second copy, so the fetched file is discarded under
// the same licence a deletion always needs, and the old file stands back from
// being offered as a candidate again for a while (db.upgradeStandBackDays). That
// stand-back is what stops a recording with no better copy on offer from being
// searched again on every single pass — the file it would otherwise raise a
// want for is right here, imported, and not good enough, over and over.
//
// The switch is read again here, not only at sweep time (migration 00072's
// upgrade_settings): turning it off has to stop deletions immediately,
// including for a want that was already in flight when it was switched off.
//
// It reports whether the old file was replaced by a genuinely better copy.
// False covers both "nothing to do" and "a worse copy was discarded" — the
// caller logs only the case that matters to a person watching this feature
// work, and both outcomes are recorded in the database either way.
func (remover *Remover) ReplaceIfBetter(
	ctx context.Context, oldFileID, newFileID uuid.UUID,
) (bool, error) {
	var enabled bool
	if err := remover.pool.QueryRow(ctx,
		`SELECT enabled FROM upgrade_settings WHERE singleton`,
	).Scan(&enabled); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, fmt.Errorf("read the upgrade settings: %w", err)
	}
	if !enabled {
		return false, nil
	}

	old, err := remover.upgradeCandidateByID(ctx, remover.pool, oldFileID)
	if err != nil {
		return false, err
	}
	fresh, err := remover.upgradeCandidateByID(ctx, remover.pool, newFileID)
	if err != nil {
		return false, err
	}
	// The old file may already be gone — a person deleted it by hand, or a
	// second pass reached a want the first one already settled. Either way
	// there is nothing here to replace, and reporting that as "not replaced"
	// is exactly right: the caller marks the want done regardless.
	if !old.present || !fresh.present {
		return false, nil
	}
	// The two files have to agree on what recording they are before one may
	// stand in for the other. They always should — the want named the
	// recording and the copy was proven against it — but a manual
	// re-identification of either file between the want being raised and the
	// copy arriving is exactly the case this refuses rather than guesses about.
	if !old.recordingID.Valid || !fresh.recordingID.Valid ||
		old.recordingID.UUID != fresh.recordingID.UUID {
		return false, nil
	}

	if better(old, fresh) {
		replaced, err := remover.replaceCandidate(ctx, old, fresh, upgradeReplacedReason, fresh.path, upgradeRemovedBy)
		if err != nil {
			if errors.Is(err, ErrReplacementMissing) {
				remover.logger.Warn().
					Str("library_file_id", fresh.id.String()).
					Msg("an upgrade copy is not readable on disk, so nothing was replaced")
				return false, nil
			}
			return false, err
		}
		if !replaced {
			return false, nil
		}
		remover.logger.Info().
			Str("library_file_id", old.id.String()).
			Str("path", old.path).
			Str("replaced_by_library_file_id", fresh.id.String()).
			Msg("a file below the quality floor was replaced by a better copy")
		// The mapping the old file held is free the moment its row is gone;
		// the new file is judged again so it can claim it, exactly as
		// keepone.go's judgeAgain lets the surviving copy claim what a
		// deleted surplus held.
		if remover.matcher != nil {
			if err := remover.matcher.ReconcileFile(ctx, fresh.id); err != nil {
				remover.logger.Warn().Err(err).Str("file_id", fresh.id.String()).
					Msg("judge the replacement file again after the file it replaced was deleted")
			}
		}
		remover.queueTagging(ctx, fresh.id)
		return true, nil
	}

	// Not better: the fetched copy is discarded rather than kept as a second
	// file, and the old file stands back from the sweep for a while so the
	// same non-upgrade is not fetched again on the next pass.
	discarded, err := remover.replaceCandidate(ctx, fresh, old, upgradeDiscardedReason, old.path, upgradeRemovedBy)
	if err != nil {
		if errors.Is(err, ErrReplacementMissing) {
			// The old file itself is what this branch was about to keep, and
			// it is the one that turned out unreadable. Nothing is deleted:
			// there is no copy to fall back on that has already been shown
			// worse, and a want in this state is left for the next pass to
			// find the old file well again or not at all.
			remover.logger.Warn().
				Str("library_file_id", old.id.String()).
				Msg("the file an upgrade want exists to replace is not readable on disk, so nothing was discarded")
			return false, nil
		}
		return false, err
	}
	if discarded {
		if _, err := remover.pool.Exec(ctx,
			`UPDATE library_files SET upgrade_refused_at = now() WHERE id = $1`, old.id,
		); err != nil {
			remover.logger.Warn().Err(err).Str("library_file_id", old.id.String()).
				Msg("record that an upgrade copy for this file was not good enough")
		}
		remover.logger.Info().
			Str("library_file_id", old.id.String()).
			Str("discarded_library_file_id", fresh.id.String()).
			Msg("a fetched copy was not a genuine upgrade and was discarded")
	}
	return false, nil
}

// ReplaceOwnedWithUpload replaces one present copy when an automatic upload
// identity proves it is strictly better. A worse upload stays in the library,
// and an ambiguous group stays for the duplicates screen to answer.
func (remover *Remover) ReplaceOwnedWithUpload(
	ctx context.Context, uploadedFileID uuid.UUID,
) (bool, error) {
	uploaded, err := remover.upgradeCandidateByID(ctx, remover.pool, uploadedFileID)
	if err != nil {
		return false, err
	}
	if !uploaded.present || !uploaded.recordingID.Valid {
		return false, nil
	}

	copies, err := remover.copiesOf(ctx, remover.pool, uploaded.recordingID.UUID)
	if err != nil {
		return false, err
	}
	var old *upgradeCandidate
	for _, copied := range copies {
		if copied.id == uploadedFileID {
			if copied.answersOther {
				return false, nil
			}
			continue
		}
		if copied.answersOther {
			continue
		}
		candidate, err := remover.upgradeCandidateByID(ctx, remover.pool, copied.id)
		if err != nil {
			return false, err
		}
		if !candidate.present || !candidate.recordingID.Valid ||
			candidate.recordingID.UUID != uploaded.recordingID.UUID {
			continue
		}
		if old != nil {
			return false, nil
		}
		candidateCopy := candidate
		old = &candidateCopy
	}
	if old == nil || !better(*old, uploaded) {
		return false, nil
	}

	replaced, err := remover.replaceCandidate(
		ctx, *old, uploaded, uploadReplacedReason, uploaded.path, uploadRemovedBy,
	)
	if err != nil {
		if errors.Is(err, ErrReplacementMissing) {
			remover.logger.Warn().Str("library_file_id", uploaded.id.String()).
				Msg("an uploaded copy is not readable on disk, so nothing was replaced")
			return false, nil
		}
		return false, err
	}
	if !replaced {
		return false, nil
	}
	remover.logger.Info().
		Str("library_file_id", old.id.String()).
		Str("path", old.path).
		Str("replaced_by_library_file_id", uploaded.id.String()).
		Msg("a file was replaced by a better uploaded copy")
	if remover.matcher != nil {
		if err := remover.matcher.ReconcileFile(ctx, uploaded.id); err != nil {
			remover.logger.Warn().Err(err).Str("file_id", uploaded.id.String()).
				Msg("judge the uploaded replacement file again after the file it replaced was deleted")
		}
	}
	remover.queueTagging(ctx, uploaded.id)
	return true, nil
}

// better reports whether fresh is a genuine upgrade over old.
//
// A file below the floor is never lossless (sources.Preferences.BelowFloor
// never says so), so old is always the lossy side of this comparison in
// practice; the lossless branches below exist so the rule reads correctly on
// its own rather than relying on that.
func better(old, fresh upgradeCandidate) bool {
	oldLossless := sources.Lossless(extension(old.path))
	freshLossless := sources.Lossless(extension(fresh.path))
	if freshLossless && !oldLossless {
		return true
	}
	if oldLossless {
		return false
	}
	if freshLossless {
		return true
	}
	if fresh.bitRateKbps < old.bitRateKbps {
		return false
	}
	if extension(fresh.path) == extension(old.path) && fresh.bitRateKbps == old.bitRateKbps {
		return false
	}
	return true
}

func extension(path string) string {
	return strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")
}

// upgradeCandidateByID reads what a replacement needs to know about one file.
// present is false for a file the library no longer holds, which is the answer
// to "does this file still exist" rather than an error — the caller asked a
// real question about a file that may have left in the meantime.
//
// It locks the library_files row it reads. That is what makes replaceCandidate's
// second read a read under the lock, which is what its comment always claimed.
// A caller reading through the pool holds the lock for the length of the
// statement and no longer.
func (remover *Remover) upgradeCandidateByID(
	ctx context.Context, source rowSource, fileID uuid.UUID,
) (upgradeCandidate, error) {
	rows, err := source.Query(ctx, `
		SELECT files.id, files.path, roots.path, files.size_bytes,
		       coalesce(files.bit_rate_kbps, 0),
		       identities.musicbrainz_recording_id
		FROM library_files files
		JOIN library_roots roots ON roots.id = files.library_root_id
		LEFT JOIN library_file_identities identities
		    ON identities.library_file_id = files.id AND identities.kind = 'external'
		WHERE files.id = $1 AND files.missing_at IS NULL
		FOR UPDATE OF files
	`, fileID)
	if err != nil {
		return upgradeCandidate{}, fmt.Errorf("read file %s: %w", fileID, err)
	}
	defer rows.Close()
	if !rows.Next() {
		return upgradeCandidate{}, rows.Err()
	}
	var candidate upgradeCandidate
	if err := rows.Scan(&candidate.id, &candidate.path, &candidate.rootPath,
		&candidate.size, &candidate.bitRateKbps, &candidate.recordingID); err != nil {
		return upgradeCandidate{}, err
	}
	candidate.present = true
	return candidate, rows.Err()
}

// readable confirms a file's audio is actually on disk and can be opened, not
// merely that its library row says so. keepone.go checks the same thing about
// the copy it is about to leave standing (os.Stat(keeper.path)) before
// deleting anything else; this is the same check for the copy this deletion
// is staked on, whichever side of the comparison it is on.
func readable(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return ErrReplacementMissing
	}
	_ = file.Close()
	return nil
}

// replaceCandidate deletes doomed in favour of kept, in the same per-file
// transaction shape releaseCopy uses in keepone.go: the licence row and the
// library row are committed together and before the audio is touched, and the
// audio leaves the disc only after that commit succeeds.
//
// keptPath is recorded on the licence row and is read from the caller's own,
// unlocked copy of kept: what matters for the record is where the file was
// understood to be when the decision was made. Both rows are read again under
// the lock all the same, because a deletion is licensed on the survivor
// proving the same recording, and either side can be re-identified between the
// caller's read and this transaction.
func (remover *Remover) replaceCandidate(
	ctx context.Context, doomed, kept upgradeCandidate, reason, keptPath, removedBy string,
) (bool, error) {
	// The survivor is checked directly on disk before anything is deleted on
	// its account — the same guarantee keepone.go gives the file it is about
	// to leave standing. A row that says a file exists is not the same fact
	// as the audio still being there and readable, and licensing a deletion
	// on a copy that turns out to be missing would destroy music for nothing.
	if err := readable(keptPath); err != nil {
		return false, err
	}

	transaction, err := remover.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, err
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	// Read again under the lock. Nothing read outside a transaction is trusted
	// for the one operation that cannot be taken back — the file may have been
	// deleted, or re-identified as something else, in the time since the
	// caller's own read.
	locked, err := remover.upgradeCandidateByID(ctx, transaction, doomed.id)
	if err != nil {
		return false, err
	}
	if !locked.present || !locked.recordingID.Valid ||
		locked.recordingID.UUID != doomed.recordingID.UUID {
		return false, nil
	}
	// The copy we keep must still prove the same recording under this
	// transaction too. An upload can be re-identified after the caller read it;
	// its old proof must never authorize deleting another recording's file.
	keptLocked, err := remover.upgradeCandidateByID(ctx, transaction, kept.id)
	if err != nil {
		return false, err
	}
	if !keptLocked.present || !keptLocked.recordingID.Valid ||
		keptLocked.recordingID.UUID != doomed.recordingID.UUID {
		return false, nil
	}
	if err := containedIn(locked.rootPath, locked.path); err != nil {
		return false, err
	}

	var removalID uuid.UUID
	if err := transaction.QueryRow(ctx, `
		INSERT INTO library_file_removals (
		    library_file_id, library_path, size_bytes,
		    musicbrainz_recording_id, kept_library_file_id, kept_path,
		    removed_by, reason
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id
	`, locked.id, locked.path, locked.size, doomed.recordingID.UUID, kept.id, keptPath,
		removedBy, reason).Scan(&removalID); err != nil {
		return false, fmt.Errorf("record the removal of %s: %w", locked.path, err)
	}
	// A playlist entry that already resolved to the doomed file follows it to
	// the survivor, so a person's playlist keeps pointing at owned music
	// rather than reading "not yet obtained" the moment the doomed file is
	// gone.
	if _, err := transaction.Exec(ctx, `
		UPDATE playlist_entries SET owned_library_file_id = $2, updated_at = now()
		WHERE owned_library_file_id = $1
	`, locked.id, kept.id); err != nil {
		return false, fmt.Errorf("carry a playlist's reference to the surviving file: %w", err)
	}
	// Deliberately no identity.WithdrawAdmittedBy here. That call is for taking
	// a decision back; the doomed file's identity was never wrong; it is only
	// leaving. Everything it admitted keeps its own identity — the proof that
	// admitted it is still true — and admitted_by_file_id is ON DELETE SET
	// NULL (migration 00069), so the chain of custody is dropped rather than
	// the identity it recorded.
	if err := db.RecordFileRemoved(ctx, transaction, locked.id); err != nil {
		return false, err
	}
	if err := identity.RearmWantsHoldingFiles(ctx, transaction,
		[]uuid.UUID{locked.id}, db.RearmedByADeletionSummary); err != nil {
		return false, err
	}
	if _, err := transaction.Exec(ctx,
		`DELETE FROM library_files WHERE id = $1`, locked.id); err != nil {
		return false, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return false, err
	}

	strandedOnDisc := !remover.unlink(ctx, removalID, locked.path)
	if !strandedOnDisc {
		discardEmptied(filepath.Dir(locked.path), locked.rootPath)
	}
	return true, nil
}
