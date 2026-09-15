package library

import (
	"bytes"
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
)

// The ways "keep this copy" is refused. Each says what is wrong with the
// request rather than what the caller should have sent, because the caller is a
// screen reading a list that may be a minute out of date.
var (
	// ErrNotHeldTwice covers both halves of the same thing: the recording has
	// no second present copy, so there is nothing this press could delete.
	ErrNotHeldTwice = errors.New("the library does not hold that recording more than once")
	// ErrNotACopy is the list having moved on — the file the person pressed is
	// no longer one of the copies of this recording.
	ErrNotACopy = errors.New("that file is not one of the copies of this recording")
	// ErrKeeperMissing stops the worst outcome this route has: deleting every
	// other copy in favour of one that is not on disc.
	ErrKeeperMissing = errors.New("the copy to keep is not on disc")
	// ErrOnlyOtherMusicLeft is a real state and not an empty list. Every other
	// copy is also a copy of different music, so all of them stay and this
	// press has nothing to do.
	ErrOnlyOtherMusicLeft = errors.New("every other copy of this recording is also a copy of other music")
	// ErrCopiesChanged is the screen and the library disagreeing about which
	// files are about to go. The person read one list and this would delete a
	// different one, so nothing is deleted.
	ErrCopiesChanged = errors.New("the copies of this recording have changed since the list was read")
)

// DuplicateScreenRemovalActor identifies a removal chosen on the duplicates
// screen.
const DuplicateScreenRemovalActor = "the person at the duplicates screen"

const removalReason = "kept one copy of a recording the library held more than once"

// KeptCopy is what one press deleted and what it left standing.
type KeptCopy struct {
	// Kept is the copy that stays and RemovedPaths is where the others were.
	Kept         uuid.UUID
	KeptPath     string
	RemovedPaths []string
	// StillOnDisc is the paths whose library row went and whose audio could not
	// be deleted. The music is out of the library either way; these are the
	// files somebody has to be told about, because the disc still holds them.
	StillOnDisc []string
}

// KeepOne deletes every other present copy of one recording and leaves the copy
// the person chose.
//
// It decides nothing about music. That these files are copies of one recording
// was proven elsewhere — a resolved identity, or a mapping onto a catalogue
// track carrying the recording — and this reads exactly those two routes and no
// third one. Two files that merely sound alike, or that carry the same tag, are
// not reached from here at all, so nothing this does can turn a resemblance
// into a deletion.
//
// Which copy to keep is the person's choice and is not weighed against the
// advice on the screen: somebody who picks the smaller file has picked the
// smaller file. What is not the person's choice is what else goes with it. A
// copy that also answers a different recording is left alone whatever was
// pressed, because deleting it would take music the press said nothing about.
//
// expected is the set of files the screen showed as going. It is what the
// person read before pressing, and if the library no longer agrees with it the
// press is refused rather than applied to a different set of files.
//
// One file is one transaction, and each one commits before its audio is
// touched. Deleting three copies is three commits: the third file's unlink
// failing must not roll back the record of the first file's unlink, which has
// already happened and cannot be taken back. A crash between a commit and its
// unlink leaves the row here with unlinked_at empty and the audio on disc,
// which is what the retry reads and what the screen names.
//
// The returned KeptCopy names whatever went, even when the error is not nil. A
// failure partway through the doomed files stops the loop, but it cannot take
// back the commits before it: the caller has to be told which files are really
// gone rather than being handed an empty result that reads as "nothing
// happened" about a press that already deleted something.
// removedBy names the actor written to each removal licence.
func (remover *Remover) KeepOne(
	ctx context.Context, recordingID, keeperID uuid.UUID, expected []uuid.UUID, removedBy string,
) (KeptCopy, error) {
	// Repair before deleting anything else. A file whose unlink failed last
	// time is still on disc, and pressing again is how a person expects to be
	// rid of it.
	remover.RetryUnlinks(ctx)

	copies, err := remover.copiesOf(ctx, remover.pool, recordingID)
	if err != nil {
		return KeptCopy{}, err
	}
	if len(copies) < 2 {
		return KeptCopy{}, ErrNotHeldTwice
	}

	var keeper *heldCopy
	doomed := make([]heldCopy, 0, len(copies)-1)
	others := 0
	for index := range copies {
		switch {
		case copies[index].id == keeperID:
			keeper = &copies[index]
		// A file that is also a copy of other music is not surplus here. One
		// file can answer two recordings — the catalogue holds a recording as
		// one track row per release, and an identity is resolved per file — and
		// the press was about one of them.
		case copies[index].answersOther:
			others++
		default:
			doomed = append(doomed, copies[index])
		}
	}
	if keeper == nil {
		return KeptCopy{}, ErrNotACopy
	}
	if len(doomed) == 0 {
		if others > 0 {
			return KeptCopy{}, ErrOnlyOtherMusicLeft
		}
		return KeptCopy{}, ErrNotHeldTwice
	}
	if !sameFiles(doomed, expected) {
		return KeptCopy{}, ErrCopiesChanged
	}

	// The files that contested these copies, read while they still exist.
	// Afterwards there is nothing left to compare the rest of the library
	// against.
	var peers []uuid.UUID
	if remover.matcher != nil {
		for _, copied := range doomed {
			found, err := remover.matcher.Peers(ctx, copied.id)
			if err != nil {
				return KeptCopy{}, fmt.Errorf("read the files contesting %s: %w", copied.id, err)
			}
			peers = append(peers, found...)
		}
	}

	// One file is one transaction, so a failure partway through this loop
	// leaves earlier files really gone: their rows deleted and committed, some
	// of their audio already off the disc. That cannot be taken back, so
	// neither can what this returns pretend it can — whatever went is named in
	// the result the caller gets, whether or not the loop also returns an
	// error. A caller that discarded the result on any error would report
	// "nothing was deleted" about a press that had already deleted something.
	var removed []heldCopy
	var stranded []string
	var loopErr error
	for _, candidate := range doomed {
		gone, err := remover.releaseCopy(ctx, recordingID, keeperID, candidate.id, removedBy)
		if err != nil {
			loopErr = err
			break
		}
		// The file was already out of the library by the time its turn came.
		if gone == nil {
			continue
		}
		removed = append(removed, *gone)
		if gone.strandedOnDisc {
			stranded = append(stranded, gone.path)
			continue
		}
		discardEmptied(filepath.Dir(gone.path), gone.rootPath)
	}
	if len(removed) == 0 {
		if loopErr != nil {
			return KeptCopy{}, loopErr
		}
		return KeptCopy{}, ErrNotHeldTwice
	}

	// The survivor is judged again first. Until the surplus went it was one of
	// several files answering one recording, which is why assignment left it
	// unmatched: one file for that track, one track for that file. It is now
	// the only claimant. This runs whether or not loopErr is set: what changed,
	// changed, and the rest of the library has to be told about it whatever
	// stopped the loop from finishing.
	remover.judgeAgain(ctx, append([]uuid.UUID{keeperID}, peers...), removed)
	// The player was showing every copy just deleted, whatever stopped the loop
	// from finishing; it is told about a burst of these the way an import is.
	remover.queueNotifyPlayer(ctx)

	paths := make([]string, 0, len(removed))
	for _, copied := range removed {
		paths = append(paths, copied.path)
	}
	return KeptCopy{
		Kept: keeperID, KeptPath: keeper.path, RemovedPaths: paths, StillOnDisc: stranded,
	}, loopErr
}

// releaseCopy takes one surplus copy out of the library and then off the disc.
//
// The transaction covers the licence row and the library row and nothing else,
// and it commits before the audio is touched. Everything the transaction
// decides is read again inside it under a lock, so a file that became a copy of
// other music, or stopped being a copy of this one, between the list and this
// moment is left alone.
//
// It returns nil when there was nothing left to do to this file, which is the
// outcome that was asked for rather than a failure.
func (remover *Remover) releaseCopy(
	ctx context.Context, recordingID, keeperID, doomedID uuid.UUID, removedBy string,
) (*heldCopy, error) {
	transaction, err := remover.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	// Locked by ascending file id, whichever of the two that makes first — not
	// by role. "The keeper first" is only a per-call order: a file can be the
	// keeper of this group and the doomed copy of another (one file can answer
	// two recordings), so two presses about different groups could each lock
	// what the other one wants next. Ordering by id instead is the same order
	// for every call anywhere in the program, which is what actually rules out
	// the deadlock rather than just moving it to a rarer pair of presses.
	firstID, secondID := keeperID, doomedID
	if bytes.Compare(doomedID[:], keeperID[:]) < 0 {
		firstID, secondID = doomedID, keeperID
	}
	first, err := remover.lockedCopy(ctx, transaction, recordingID, firstID)
	if err != nil {
		return nil, err
	}
	second, err := remover.lockedCopy(ctx, transaction, recordingID, secondID)
	if err != nil {
		return nil, err
	}
	keeper, doomed := second, first
	if firstID == keeperID {
		keeper, doomed = first, second
	}
	// The keeper is read again under the lock, and its audio is checked at the
	// path this transaction read rather than the one the list showed.
	if keeper == nil {
		return nil, ErrKeeperMissing
	}
	if _, err := os.Stat(keeper.path); err != nil {
		return nil, ErrKeeperMissing
	}

	// Gone already, no longer a copy of this recording, or a copy of other
	// music as well. All three mean the same thing here: not this press's to
	// delete.
	if doomed == nil || doomed.answersOther || doomed.id == keeper.id {
		return nil, nil
	}
	if err := containedIn(doomed.rootPath, doomed.path); err != nil {
		return nil, err
	}

	var removalID uuid.UUID
	if err := transaction.QueryRow(ctx, `
		INSERT INTO library_file_removals (
		    library_file_id, library_path, size_bytes,
		    musicbrainz_recording_id, kept_library_file_id, kept_path,
		    removed_by, reason
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id
	`, doomed.id, doomed.path, doomed.size, recordingID, keeper.id, keeper.path,
		removedBy, removalReason).Scan(&removalID); err != nil {
		return nil, fmt.Errorf("record the removal of %s: %w", doomed.path, err)
	}
	// Everything this file admitted goes back to a question before the file
	// itself goes — the same reason Remove withdraws it first: a copy was let
	// into the library because its audio agreed with this one, and this file
	// leaving does not unprove that agreement, but nothing is left here any
	// more to have proven it.
	fallen, err := identity.WithdrawAdmittedBy(ctx, transaction, doomed.id)
	if err != nil {
		return nil, err
	}
	// Before the row goes, and not after — the same reason Remove writes these
	// down first: a copy's link to its file is ON DELETE SET NULL, so afterwards
	// nothing could say which copies became this file, or which want to wake. A
	// want whose copy this file was, or one of the files that fell with it, may
	// have stopped on it, with no time to be looked at again; deleting the file
	// gives every one of them something to look for.
	if err := db.RecordFileRemoved(ctx, transaction, doomed.id); err != nil {
		return nil, err
	}
	if err := identity.RearmWantsHoldingFiles(ctx, transaction,
		append([]uuid.UUID{doomed.id}, fallen...), db.RearmedByADeletionSummary); err != nil {
		return nil, err
	}
	if _, err := transaction.Exec(ctx,
		`DELETE FROM library_files WHERE id = $1`, doomed.id); err != nil {
		return nil, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return nil, err
	}

	doomed.strandedOnDisc = !remover.unlink(ctx, removalID, doomed.path)
	remover.logger.Info().
		Str("file_id", doomed.id.String()).
		Str("path", doomed.path).
		Str("musicbrainz_recording_id", recordingID.String()).
		Str("kept_file_id", keeper.id.String()).
		Bool("still_on_disc", doomed.strandedOnDisc).
		Msg("library file deleted so one copy of a recording is kept")
	return doomed, nil
}

// unlink deletes the audio and stamps the removal row with what happened. It
// reports whether the audio is off the disc.
//
// A failure here is recorded and not returned. The library row is already gone
// and committed; the caller cannot undo that, and a file left on disc with its
// reason written down is something a person can be told about and something the
// retry can pick up. Already gone is the outcome that was asked for.
//
// A library_files row at this path is checked for and refused first, on every
// call, not only the retry's. The row going and this unlink are two separate
// commits; a scan that ran between them and found nothing here would have
// indexed the disk file as a new row — the scanner is taught to skip a pending
// removal so that should not happen, but this is the operation that cannot be
// taken back, so it does not trust that alone. Deleting audio a live row now
// owns would take music out from under a file nobody decided to remove.
func (remover *Remover) unlink(ctx context.Context, removalID uuid.UUID, path string) bool {
	var owned bool
	if err := remover.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM library_files WHERE path = $1)`, path,
	).Scan(&owned); err != nil {
		remover.logger.Error().Err(err).Str("path", path).
			Msg("check whether a path is still owned before deleting its audio")
		return false
	}
	if owned {
		remover.logger.Warn().Str("path", path).
			Msg("refuse to delete audio a library file now owns")
		if _, err := remover.pool.Exec(ctx, `
			UPDATE library_file_removals SET unlink_error = $2 WHERE id = $1
		`, removalID, "a library file now owns this path"); err != nil {
			remover.logger.Error().Err(err).Str("removal_id", removalID.String()).
				Msg("record why a removed file was left on disc")
		}
		return false
	}
	err := os.Remove(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		remover.logger.Error().Err(err).Str("path", path).
			Msg("delete the audio of a file the library has already let go of")
		if _, markErr := remover.pool.Exec(ctx, `
			UPDATE library_file_removals SET unlink_error = $2 WHERE id = $1
		`, removalID, err.Error()); markErr != nil {
			remover.logger.Error().Err(markErr).Str("removal_id", removalID.String()).
				Msg("record why a removed file is still on disc")
		}
		return false
	}
	if _, err := remover.pool.Exec(ctx, `
		UPDATE library_file_removals
		SET unlinked_at = now(), unlink_error = ''
		WHERE id = $1
	`, removalID); err != nil {
		remover.logger.Error().Err(err).Str("removal_id", removalID.String()).
			Msg("record that a removed file left the disc")
	}
	return true
}

// RetryUnlinks tries again on every file the library has let go of and could
// not delete: a read-only mount, a permission that has since been fixed, a
// crash between the commit and the unlink.
//
// It reports how many files left the disc. Failures are logged and not
// returned: this runs alongside somebody else's press, and a file that is still
// stuck stays exactly as it was, on record and listed.
func (remover *Remover) RetryUnlinks(ctx context.Context) int {
	rows, err := remover.pool.Query(ctx, `
		SELECT removals.id, removals.library_path, roots.path
		FROM library_file_removals removals
		LEFT JOIN library_roots roots
		       ON removals.library_path LIKE roots.path || '/%'
		WHERE removals.unlinked_at IS NULL
		ORDER BY removals.removed_at
		LIMIT 500
	`)
	if err != nil {
		remover.logger.Warn().Err(err).Msg("read the files the library let go of and could not delete")
		return 0
	}
	type pending struct {
		id       uuid.UUID
		path     string
		rootPath string
	}
	var waiting []pending
	for rows.Next() {
		var found pending
		var root *string
		if err := rows.Scan(&found.id, &found.path, &root); err != nil {
			rows.Close()
			remover.logger.Warn().Err(err).Msg("read a file the library let go of and could not delete")
			return 0
		}
		if root != nil {
			found.rootPath = *root
		}
		waiting = append(waiting, found)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		remover.logger.Warn().Err(err).Msg("read the files the library let go of and could not delete")
		return 0
	}

	deleted := 0
	for _, found := range waiting {
		// A path outside every library root is not this program's to delete,
		// however it came to be on the row.
		if found.rootPath == "" || containedIn(found.rootPath, found.path) != nil {
			continue
		}
		if !remover.unlink(ctx, found.id, found.path) {
			continue
		}
		deleted++
		discardEmptied(filepath.Dir(found.path), found.rootPath)
	}
	return deleted
}

// judgeAgain matches the surviving file and everything that contested the
// deleted ones, then asks for the survivor's tags to be brought into line.
//
// Failures here are logged and not returned. The music is already gone and that
// is what was asked for; a file left describing a library it is no longer in is
// stale rather than wrong, and the next scan judges it again.
func (remover *Remover) judgeAgain(
	ctx context.Context, fileIDs []uuid.UUID, removed []heldCopy,
) {
	if remover.matcher == nil {
		return
	}
	gone := make(map[uuid.UUID]bool, len(removed))
	for _, copied := range removed {
		gone[copied.id] = true
	}
	seen := make(map[uuid.UUID]bool, len(fileIDs))
	for _, fileID := range fileIDs {
		if seen[fileID] || gone[fileID] {
			continue
		}
		seen[fileID] = true
		if err := remover.matcher.ReconcileFile(ctx, fileID); err != nil {
			remover.logger.Warn().Err(err).Str("file_id", fileID.String()).
				Msg("judge a file again after a surplus copy was deleted")
		}
	}
	remover.queueTagging(ctx, fileIDs[0])
}

// queueNotifyPlayer asks for the player to be told the library changed — the
// same job internal/jobs schedules from an import or a tagging pass, reached
// here by direct SQL because this deletion runs on the package's own pool
// rather than inside a job the worker is already running.
func (remover *Remover) queueNotifyPlayer(ctx context.Context) {
	if _, err := remover.pool.Exec(ctx, `
		INSERT INTO jobs (kind, payload, max_attempts, run_after)
		VALUES ('notify_player', '{}'::jsonb, 1, now() + interval '30 seconds')
		ON CONFLICT (kind)
		    WHERE kind = 'notify_player' AND status IN ('queued', 'running')
		DO NOTHING
	`); err != nil {
		remover.logger.Warn().Err(err).Msg("queue telling the player the library changed")
	}
}

// queueTagging asks for the surviving file's tags to be written, the same way
// matching does when a file's mappings change. A reconcile that gains a mapping
// already asks; this asks for the file that was matched before and is matched
// to the same track after, whose tags nothing else would revisit.
//
// One queued job per file is enough — a second job on a file that needs nothing
// reads its tags and writes nothing.
func (remover *Remover) queueTagging(ctx context.Context, fileID uuid.UUID) {
	if _, err := remover.pool.Exec(ctx, `
		INSERT INTO jobs (kind, payload, max_attempts)
		SELECT 'tag_file', jsonb_build_object('fileId', $1::text), 3
		WHERE NOT EXISTS (
			SELECT 1 FROM jobs
			WHERE kind = 'tag_file'
			  AND payload->>'fileId' = $1::text
			  AND status = 'queued'
		)
	`, fileID); err != nil {
		remover.logger.Warn().Err(err).Str("file_id", fileID.String()).
			Msg("queue the tags of the copy that was kept")
	}
}

// heldCopy is one present file answering the recording somebody is clearing.
type heldCopy struct {
	id       uuid.UUID
	path     string
	rootPath string
	size     int64
	// answersOther says this file is also a copy of different music, which is
	// what makes it untouchable here however the press went.
	answersOther bool
	// strandedOnDisc says the library row went and the audio did not.
	strandedOnDisc bool
}

// sameFiles reports whether the library is about to delete exactly the files
// the screen said it would. Order does not matter; membership does.
func sameFiles(doomed []heldCopy, expected []uuid.UUID) bool {
	if len(doomed) != len(expected) {
		return false
	}
	named := make(map[uuid.UUID]bool, len(expected))
	for _, fileID := range expected {
		named[fileID] = true
	}
	for _, copied := range doomed {
		if !named[copied.id] {
			return false
		}
	}
	return true
}

// containedIn refuses a path that is not inside the library root it claims to
// be in. Nothing should ever produce one, which is exactly why it is checked
// before the one operation that cannot be taken back.
func containedIn(rootPath, path string) error {
	root := filepath.Clean(rootPath)
	inside, err := filepath.Rel(root, filepath.Clean(path))
	if err != nil || inside == ".." || strings.HasPrefix(inside, ".."+string(filepath.Separator)) {
		return fmt.Errorf("refuse to delete %s: it is not inside the library root %s", path, root)
	}
	return nil
}

// rowSource is the part of a pool and a transaction these reads need, so the
// same query serves the list taken beforehand and the row locked inside the
// transaction that acts on it.
type rowSource interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

// copiesQuery reads the present files answering one recording.
//
// The two routes are exactly the two RecordingsHeldTwice reads, and exactly the
// two OwnedFileForRecording reads: an identity resolved against the file, or a
// mapping onto a catalogue track carrying the recording. Nothing else counts as
// a copy, so files the library can only say are similar are not here and cannot
// be deleted from here.
//
// $2 narrows it to one file and locks that row; the null form reads the whole
// group and locks nothing.
const copiesQuery = `
	SELECT files.id, files.path, roots.path, files.size_bytes,
	       EXISTS (
	           SELECT 1 FROM library_file_identities other
	           WHERE other.library_file_id = files.id
	             AND other.musicbrainz_recording_id IS NOT NULL
	             AND other.musicbrainz_recording_id <> $1
	       ) OR EXISTS (
	           SELECT 1 FROM track_mappings mapping
	           JOIN tracks ON tracks.id = mapping.track_id
	           WHERE mapping.library_file_id = files.id
	             AND tracks.musicbrainz_recording_id IS NOT NULL
	             AND tracks.musicbrainz_recording_id <> $1
	       )
	FROM library_files files
	JOIN library_roots roots ON roots.id = files.library_root_id
	WHERE files.missing_at IS NULL
	  AND ($2::uuid IS NULL OR files.id = $2)
	  AND (EXISTS (
	          SELECT 1 FROM library_file_identities identity
	          WHERE identity.library_file_id = files.id
	            AND identity.musicbrainz_recording_id = $1
	      ) OR EXISTS (
	          SELECT 1 FROM track_mappings mapping
	          JOIN tracks ON tracks.id = mapping.track_id
	          WHERE mapping.library_file_id = files.id
	            AND tracks.musicbrainz_recording_id = $1
	      ))
	ORDER BY files.path
`

func (remover *Remover) copiesOf(
	ctx context.Context, source rowSource, recordingID uuid.UUID,
) ([]heldCopy, error) {
	rows, err := source.Query(ctx, copiesQuery, recordingID, nil)
	if err != nil {
		return nil, fmt.Errorf("read the copies of recording %s: %w", recordingID, err)
	}
	defer rows.Close()

	var copies []heldCopy
	for rows.Next() {
		var copied heldCopy
		if err := rows.Scan(&copied.id, &copied.path, &copied.rootPath,
			&copied.size, &copied.answersOther); err != nil {
			return nil, err
		}
		copies = append(copies, copied)
	}
	return copies, rows.Err()
}

// lockedCopy reads one file as a copy of this recording and holds the row until
// the transaction ends. It answers nil when the file is not a copy of this
// recording any more, which includes it not being in the library at all.
//
// This is where the question is really settled. The list the caller worked from
// was read outside the transaction and may be a minute old; this is the same
// question asked again, of the row nothing else can change while it is held.
func (remover *Remover) lockedCopy(
	ctx context.Context, transaction pgx.Tx, recordingID, fileID uuid.UUID,
) (*heldCopy, error) {
	// Locked first and read second. Locking through the copies query itself is
	// not open to us — FOR UPDATE cannot be used with the EXISTS subqueries
	// aggregated above — and the row is what needs holding, not the joins.
	var exists bool
	err := transaction.QueryRow(ctx,
		`SELECT true FROM library_files WHERE id = $1 FOR UPDATE`, fileID).Scan(&exists)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	rows, err := transaction.Query(ctx, copiesQuery, recordingID, fileID)
	if err != nil {
		return nil, fmt.Errorf("read file %s as a copy of recording %s: %w",
			fileID, recordingID, err)
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	var copied heldCopy
	if err := rows.Scan(&copied.id, &copied.path, &copied.rootPath,
		&copied.size, &copied.answersOther); err != nil {
		return nil, err
	}
	return &copied, rows.Err()
}
