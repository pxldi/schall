package acquisition

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/identity"
	"github.com/rs/zerolog"
)

type strandedStore struct {
	Store
	outcome     db.AcquiredFileOutcome
	requeues    int
	reschedules int
	summary     string
}

func (store *strandedStore) RescheduleAcquisitionTarget(
	_ context.Context, _ uuid.UUID, summary string, _ time.Time,
) error {
	store.reschedules++
	store.summary = summary
	return nil
}

func (store *strandedStore) CompleteAcquiredFile(
	context.Context, uuid.UUID, string, string,
) (db.AcquiredFileOutcome, error) {
	return store.outcome, nil
}

func (store *strandedStore) RequeueAcquisitionTarget(
	context.Context, uuid.UUID, string, string, string, string, time.Time,
) error {
	store.requeues++
	return nil
}

// A second pass sees a stranded copy still awaiting its library row. It must
// keep the first settlement and stay quiet until the row appears.
func TestASecondPassSkipsAnAlreadyStrandedCopy(t *testing.T) {
	var logs bytes.Buffer
	store := &strandedStore{outcome: db.AcquiredFileOutcome{
		Awaiting:      true,
		AwaitingSince: time.Now().Add(-importedPatience - time.Minute),
	}}
	service := NewService(store, zerolog.New(&logs))

	target := db.AcquisitionTargetRow{
		ID:                     uuid.New(),
		MusicBrainzRecordingID: uuid.NullUUID{UUID: uuid.New(), Valid: true},
		Summary:                strandedSummary,
	}
	if _, err := service.attempt(context.Background(), target, &passRefusal{}); err != nil {
		t.Fatalf("attempt() error = %v", err)
	}

	if logs.Len() != 0 {
		t.Fatalf("logs = %q, want none", logs.String())
	}
	if store.requeues != 0 {
		t.Fatalf("requeues = %d, want no new attempt", store.requeues)
	}
	if store.reschedules != 1 || store.summary != strandedSummary {
		t.Fatalf("reschedules = %d with summary %q, want one check later as still stranded",
			store.reschedules, store.summary)
	}
}

type failedRegistrationLookupStore struct {
	Store
	filing  db.AcquiredCopyFiling
	settles int
	stops   int
}

func (store *failedRegistrationLookupStore) CompleteAcquiredFile(
	context.Context, uuid.UUID, string, string,
) (db.AcquiredFileOutcome, error) {
	return db.AcquiredFileOutcome{}, nil
}

func (store *failedRegistrationLookupStore) OwnedFileForRecording(
	context.Context, uuid.UUID,
) (uuid.NullUUID, error) {
	return uuid.NullUUID{}, nil
}

func (store *failedRegistrationLookupStore) AcquisitionTargetHasAcceptedCopy(
	context.Context, uuid.UUID,
) (bool, error) {
	return true, nil
}

func (store *failedRegistrationLookupStore) AcquiredCopyFiling(
	context.Context, uuid.UUID,
) (db.AcquiredCopyFiling, error) {
	return store.filing, nil
}

func (store *failedRegistrationLookupStore) SettleAcquiredTarget(
	context.Context, uuid.UUID, uuid.UUID, string, string,
) error {
	store.settles++
	return nil
}

func (store *failedRegistrationLookupStore) StopLookingForAcquisitionTarget(
	context.Context, uuid.UUID, string,
) error {
	store.stops++
	return nil
}

type failedRegistrationResolver struct{}

func (failedRegistrationResolver) Resolve(
	context.Context, identity.Evidence, ...uuid.UUID,
) (identity.Outcome, error) {
	return identity.Outcome{}, nil
}

func (failedRegistrationResolver) OneRegistration(
	context.Context, uuid.UUID, uuid.UUID, identity.FilingEvidence,
) (identity.FilingVerdict, error) {
	return identity.FilingOtherRecording, errors.New("MusicBrainz unavailable")
}

func TestAFailedRegistrationLookupLeavesTheWantUnchanged(t *testing.T) {
	fileID := uuid.New()
	store := &failedRegistrationLookupStore{filing: db.AcquiredCopyFiling{
		Found:            true,
		LibraryFileID:    fileID,
		Identified:       true,
		FiledRecordingID: uuid.NullUUID{UUID: uuid.New(), Valid: true},
	}}
	service := NewService(store, zerolog.Nop()).WithResolver(failedRegistrationResolver{})
	target := db.AcquisitionTargetRow{
		ID:                     uuid.New(),
		MusicBrainzRecordingID: uuid.NullUUID{UUID: uuid.New(), Valid: true},
		Status:                 "pending",
		Summary:                "keep looking",
	}
	original := target

	if _, err := service.attempt(context.Background(), target, &passRefusal{}); err != nil {
		t.Fatalf("attempt() error = %v", err)
	}

	if !reflect.DeepEqual(target, original) {
		t.Fatalf("target = %#v, want unchanged %#v", target, original)
	}
	if store.settles != 0 {
		t.Fatalf("settles = %d, want none", store.settles)
	}
	if store.stops != 0 {
		t.Fatalf("stops = %d, want none", store.stops)
	}
}
