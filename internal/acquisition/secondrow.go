package acquisition

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/identity"
	"github.com/pxldi/schall/internal/musicbrainz"
)

// waitingCopyReview bounds how many wants stopped on a waiting copy one start
// asks the library about again. Most cost one query; the few with an owned
// recording of the same title cost a cached MusicBrainz lookup each.
const waitingCopyReview = 500

// errSecondRowUnasked reports that MusicBrainz could not be asked about the
// rows a want and an owned file name. It says nothing about the library, so
// the want waits and asks again rather than going looking or failing the pass.
var errSecondRowUnasked = errors.New("MusicBrainz could not be asked about a second row")

// RowReader reads one MusicBrainz recording. *identity.Resolver is one, and a
// resolver that is not leaves the second-row question unasked.
type RowReader interface {
	Recording(ctx context.Context, id uuid.UUID) (musicbrainz.Recording, error)
}

// ownedSecondRow finds a present library file proven to be a second MusicBrainz
// row of the recording a want names: a row MusicBrainz merged into it, or one
// registered track it holds twice by ADR 0031's name-and-length test (same
// artists, same title, same length, no codes that disagree). The single and the
// album track of one song are usually two such rows, and owning one used to
// leave a want for the other fetching the same audio again.
//
// The title only narrows which owned rows are asked about. MusicBrainz's own
// rows decide, by the same test the recommendation lists already use to hide a
// song the library holds. The file keeps the identity it has, and the caller
// records the want's row as a second row of it.
func (service *Service) ownedSecondRow(
	ctx context.Context, target db.AcquisitionTargetRow,
) (uuid.NullUUID, error) {
	reader, ok := service.resolver.(RowReader)
	if !ok || !target.MusicBrainzRecordingID.Valid {
		return uuid.NullUUID{}, nil
	}
	wantedID := target.MusicBrainzRecordingID.UUID
	candidates, err := service.store.OwnedRecordingsTitled(ctx, target.EntryTitle, wantedID)
	if err != nil || len(candidates) == 0 {
		return uuid.NullUUID{}, err
	}
	wanted, err := reader.Recording(ctx, wantedID)
	if errors.Is(err, musicbrainz.ErrNotFound) {
		return uuid.NullUUID{}, nil
	}
	if err != nil {
		return uuid.NullUUID{}, fmt.Errorf("%w: look up recording %s: %w", errSecondRowUnasked, wantedID, err)
	}
	for _, ownedID := range candidates {
		owned, err := reader.Recording(ctx, ownedID)
		if errors.Is(err, musicbrainz.ErrNotFound) {
			continue
		}
		if err != nil {
			return uuid.NullUUID{}, fmt.Errorf("%w: look up owned recording %s: %w",
				errSecondRowUnasked, ownedID, err)
		}
		if owned.ID != wanted.ID && !identity.SameRegistrationByNameAndLength(wanted, owned) {
			continue
		}
		fileID, err := service.store.OwnedFileForRecording(ctx, ownedID)
		if err != nil || fileID.Valid {
			return fileID, err
		}
	}
	return uuid.NullUUID{}, nil
}

// reviewWaitingCopies asks the library again about the wants that stopped while
// a copy waits for a person. A stopped want is never due, so without this a
// want whose song the library came to hold under a second row would keep its
// question for ever.
func (service *Service) reviewWaitingCopies(ctx context.Context) {
	if _, ok := service.resolver.(RowReader); !ok {
		return
	}
	targets, err := service.store.TargetsStoppedOnAWaitingCopy(ctx, waitingCopyReview)
	if err != nil {
		service.logger.Error().Err(err).Msg("list the wants stopped on a waiting copy")
		return
	}
	for _, target := range targets {
		if ctx.Err() != nil {
			return
		}
		if _, err := service.heldAlready(ctx, target); err != nil {
			service.logger.Error().Err(err).
				Str("acquisition_target_id", target.ID.String()).
				Msg("ask the library again about a want stopped on a waiting copy")
		}
	}
}
