package acquisition

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/dbtest"
	"github.com/pxldi/schall/internal/musicbrainz"
)

// A want stopped on two recordings that share audio and no registration code
// takes exactly one answer: the person reads both credits and says the file in
// the library is the recording they asked for.
//
// Nothing proved it, and nothing about the pair changed. Their answer is written
// onto the file as a manual decision, which is permanent, and the sweep that
// follows settles the want against the file already on the disc.
func TestAcceptingTheFiledCopyAnswersAStoppedWant(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	wanted, filed := uuid.New(), uuid.New()
	provider := &catalogueProvider{recordings: map[uuid.UUID]musicbrainz.Recording{
		wanted: recording(wanted, "Glory Box", "Portishead", 301000, "GBAAA9400001"),
		filed:  recording(filed, "Glory Box (Live)", "Portishead", 305000, "GBAAA9400002"),
	}}
	service := filingService(pool, provider)
	target := createWant(ctx, t, service, wanted)
	fileID := acceptedCopy(ctx, t, pool, target.ID, wanted)
	anchorOf(ctx, t, pool, target.ID, 0.07)
	filedAs(ctx, t, pool, fileID, filed, false)
	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	stopped := targetRow(ctx, t, service, target.ID)
	if stopped.Status != "pending" || stopped.NextAttemptAt.Valid {
		t.Fatalf("status = %s next = %v, want the want stopped on the disagreement",
			stopped.Status, stopped.NextAttemptAt)
	}

	if err := service.AcceptFiledCopy(ctx, target.ID); err != nil {
		t.Fatalf("AcceptFiledCopy() error = %v", err)
	}

	stored, method, manual := identityOfFile(ctx, t, pool, fileID)
	if stored != wanted {
		t.Fatalf("identity = %s, want the recording the person chose %s", stored, wanted)
	}
	if method != "manual" || !manual {
		t.Errorf("method = %q manual = %v, want a decision recorded by hand", method, manual)
	}
	// Rearmed rather than settled here. The want gets its next look back and the
	// sweep settles it through the check every settled want goes through, against
	// the file the library now names.
	rearmed := targetRow(ctx, t, service, target.ID)
	if rearmed.Status != "pending" || !rearmed.NextAttemptAt.Valid {
		t.Fatalf("status = %s next = %v, want a want looking again",
			rearmed.Status, rearmed.NextAttemptAt)
	}

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	settled := targetRow(ctx, t, service, target.ID)
	if settled.Status != "acquired" {
		t.Fatalf("status = %s (%q), want acquired", settled.Status, settled.Summary)
	}
	if settled.AcquiredLibraryFileID.UUID != fileID {
		t.Errorf("acquired file = %v, want %s", settled.AcquiredLibraryFileID, fileID)
	}
}

// The answer is only for the want that is asking. A want with a time to be
// looked at again is not stopped, and accepting its file would write a decision
// about a question nobody was put.
func TestAcceptingTheFiledCopyOfAWantThatIsNotStoppedIsRefused(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	wanted, filed := uuid.New(), uuid.New()
	service := filingService(pool, twoRowsOf(wanted, filed, "GBAAA9400001"))
	target := createWant(ctx, t, service, wanted)
	fileID := acceptedCopy(ctx, t, pool, target.ID, wanted)
	filedAs(ctx, t, pool, fileID, filed, false)

	if err := service.AcceptFiledCopy(ctx, target.ID); !errors.Is(err, ErrWantIsNotStopped) {
		t.Fatalf("AcceptFiledCopy() error = %v, want ErrWantIsNotStopped", err)
	}
	stored, _, manual := identityOfFile(ctx, t, pool, fileID)
	if stored != filed || manual {
		t.Errorf("identity = %s manual = %v, want the library's own answer %s untouched",
			stored, manual, filed)
	}
}
