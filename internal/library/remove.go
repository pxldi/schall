package library

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/identity"
	"github.com/rs/zerolog"
)

var ErrFileGone = errors.New("the library no longer holds this file")

// FileContender answers which other files are about the same music, and judges
// one again. Removing a file needs both: what a peer was told — that a track was
// taken, that a copy was already in place — stops being true the moment the file
// it was told about is gone.
type FileContender interface {
	Peers(context.Context, uuid.UUID) ([]uuid.UUID, error)
	ReconcileFile(context.Context, uuid.UUID) error
}

// Remover takes a file out of the library: the audio off the disc and the row
// that described it.
//
// This is the one thing Schall does that cannot be undone, so it does exactly
// what it was asked and nothing more. It chooses no file, reads no duplicate
// rule and consults no quality score: something else decided, and this carries
// the decision out. Everything hanging off the row goes with it by the foreign
// keys already declared — the mapping, the identity, the candidates — and the
// links that merely point at it (a want it satisfied, a playlist entry it
// answered) fall back to holding nothing, which is what they held before this
// file arrived and is true again. One of them does more than fall back: a want
// that stopped because this file was its copy is looked for again, because the
// music it was told about is not in the library any more.
//
// A file can also be a witness other files borrowed their identity from
// (ROADMAP 10b): a copy that agreed with this file's audio was admitted on the
// strength of whatever proved this one. Deleting the file must not sever that
// silently — a mistaken decision on this file could then never be corrected
// through it, because it would no longer be there to correct. So everything
// admitted on this file's strength is withdrawn first, in the same transaction,
// exactly as identity.Service.Clear withdraws it when a person takes the
// decision back by hand.
type Remover struct {
	pool    *pgxpool.Pool
	logger  zerolog.Logger
	matcher FileContender
}

func NewRemover(pool *pgxpool.Pool, logger zerolog.Logger) *Remover {
	return &Remover{pool: pool, logger: logger}
}

func (remover *Remover) WithMatcher(matcher FileContender) *Remover {
	remover.matcher = matcher
	return remover
}

// Remove deletes the file and returns where it was.
//
// The audio goes before the row is committed, and never the other way round. A
// row deleted first and a delete that then fails would leave music on disc the
// library has forgotten, which the next scan finds and adds back as a new file:
// the duplicate somebody just resolved would return under a new id. This order
// fails the other way instead — the file gone and the row still there — and the
// next scan reads that as a file that went missing, which it is.
func (remover *Remover) Remove(ctx context.Context, fileID uuid.UUID) (string, error) {
	// Read before the row goes: afterwards there is nothing left to compare
	// the rest of the library against.
	var peers []uuid.UUID
	if remover.matcher != nil {
		found, err := remover.matcher.Peers(ctx, fileID)
		if err != nil {
			return "", fmt.Errorf("read the files contesting %s: %w", fileID, err)
		}
		peers = found
	}

	transaction, err := remover.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return "", err
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	var path, rootPath string
	err = transaction.QueryRow(ctx, `
		SELECT files.path, roots.path
		FROM library_files files
		JOIN library_roots roots ON roots.id = files.library_root_id
		WHERE files.id = $1
		FOR UPDATE OF files
	`, fileID).Scan(&path, &rootPath)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrFileGone
	}
	if err != nil {
		return "", err
	}
	// Nothing should ever record a library file outside its own root, which is
	// exactly why it is checked before the one operation that cannot be taken
	// back.
	if err := containedIn(rootPath, path); err != nil {
		return "", err
	}
	// Everything this file admitted goes back to a question before the file
	// itself goes: a copy was let into the library because its audio agreed
	// with this one, and this file leaving does not unprove that agreement, but
	// nothing is left here any more to have proven it. See the doc comment
	// above.
	fallen, err := identity.WithdrawAdmittedBy(ctx, transaction, fileID)
	if err != nil {
		return "", err
	}
	// Before the row goes, and not after. A copy's link to its file is ON
	// DELETE SET NULL, so afterwards nothing could say which copies became this
	// file, or which want to wake. Writing the removal down first is what lets a
	// want re-armed later still be told "gone" apart from "not scanned yet" —
	// the two look identical once the link is cleared.
	if err := db.RecordFileRemoved(ctx, transaction, fileID); err != nil {
		return "", err
	}
	// A want whose copy is this file, or one of the files that fell with it, may
	// have stopped on it — its music on the disc, the library calling that file
	// another recording — and a want that stopped has no time to be looked at
	// again. Deleting the file gives every one of them something to look for,
	// so each is given a next look here.
	if err := identity.RearmWantsHoldingFiles(ctx, transaction,
		append([]uuid.UUID{fileID}, fallen...), db.RearmedByADeletionSummary); err != nil {
		return "", err
	}
	if _, err := transaction.Exec(ctx, `DELETE FROM library_files WHERE id = $1`, fileID); err != nil {
		return "", err
	}
	// Already gone is the outcome that was asked for, so it is not a failure.
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("delete %s: %w", path, err)
	}
	if err := transaction.Commit(ctx); err != nil {
		return "", err
	}
	discardEmptied(filepath.Dir(path), rootPath)
	remover.logger.Info().Str("file_id", fileID.String()).Str("path", path).
		Msg("library file deleted")

	if remover.matcher != nil {
		for _, peer := range peers {
			if err := remover.matcher.ReconcileFile(ctx, peer); err != nil {
				// The file is gone and that is what was asked for. A peer left
				// describing a library it is no longer in is stale rather than
				// wrong, and the next scan judges it again.
				remover.logger.Warn().Err(err).Str("file_id", peer.String()).
					Msg("judge a file again after its counterpart was deleted")
			}
		}
	}
	return path, nil
}
