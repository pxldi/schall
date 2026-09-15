package upgrade

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/db"
	"github.com/rs/zerolog"
)

type fakeStore struct {
	settings    db.UpgradeSettingsRow
	preferences db.SourcePreferencesRow
	candidates  []db.UpgradeCandidateRow
	raised      []db.CreateUpgradeTargetParams
	notWanted   map[uuid.UUID]bool
}

func (store *fakeStore) UpgradeSettings(context.Context) (db.UpgradeSettingsRow, error) {
	return store.settings, nil
}

func (store *fakeStore) SaveUpgradeSettings(context.Context, bool) (db.UpgradeSettingsRow, error) {
	return store.settings, nil
}

func (store *fakeStore) SourcePreferences(context.Context) (db.SourcePreferencesRow, error) {
	return store.preferences, nil
}

func (store *fakeStore) UpgradeCandidates(
	_ context.Context, after uuid.UUID, limit int32,
) ([]db.UpgradeCandidateRow, error) {
	// after names the last id already read; the fake candidates are a flat
	// slice already in order, so a page is everything strictly following it.
	var page []db.UpgradeCandidateRow
	seenAfter := after == uuid.Nil
	for _, candidate := range store.candidates {
		if seenAfter {
			page = append(page, candidate)
		}
		if candidate.FileID == after {
			seenAfter = true
		}
	}
	if int32(len(page)) > limit {
		page = page[:limit]
	}
	return page, nil
}

func (store *fakeStore) CreateUpgradeTarget(
	_ context.Context, params db.CreateUpgradeTargetParams,
) (uuid.UUID, bool, error) {
	if store.notWanted[params.RecordingID] {
		return uuid.Nil, false, db.ErrRecordingNotWanted
	}
	store.raised = append(store.raised, params)
	return uuid.New(), true, nil
}

func (store *fakeStore) QueueUpgradeSweep(context.Context, time.Time, json.RawMessage) error {
	return nil
}

func candidate(path string, bitRate int) db.UpgradeCandidateRow {
	return db.UpgradeCandidateRow{
		FileID: uuid.New(), Path: path, BitRateKbps: bitRate,
		RecordingID: uuid.New(), ArtistName: "An Artist", TrackTitle: "A Title",
	}
}

func TestSweepDoesNothingWhenSwitchedOff(t *testing.T) {
	store := &fakeStore{
		settings:    db.UpgradeSettingsRow{Enabled: false},
		preferences: db.SourcePreferencesRow{MinimumBitRate: 320},
		candidates:  []db.UpgradeCandidateRow{candidate("/music/track.mp3", 128)},
	}
	service := NewService(store, zerolog.Nop())

	result, err := service.Sweep(context.Background(), uuid.Nil)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if result.Raised != 0 || len(store.raised) != 0 {
		t.Fatalf("a switched-off sweep raised %d wants, want none", result.Raised)
	}
}

func TestSweepDoesNothingWithNoFloorStated(t *testing.T) {
	store := &fakeStore{
		settings:   db.UpgradeSettingsRow{Enabled: true},
		candidates: []db.UpgradeCandidateRow{candidate("/music/track.mp3", 128)},
	}
	service := NewService(store, zerolog.Nop())

	result, err := service.Sweep(context.Background(), uuid.Nil)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if result.Raised != 0 {
		t.Fatalf("a sweep with no stated floor raised %d wants, want none", result.Raised)
	}
}

func TestSweepRaisesAWantForAFileBelowTheFloor(t *testing.T) {
	below := candidate("/music/track.mp3", 128)
	store := &fakeStore{
		settings:    db.UpgradeSettingsRow{Enabled: true},
		preferences: db.SourcePreferencesRow{MinimumBitRate: 320},
		candidates:  []db.UpgradeCandidateRow{below},
	}
	service := NewService(store, zerolog.Nop())

	result, err := service.Sweep(context.Background(), uuid.Nil)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if result.Raised != 1 || len(store.raised) != 1 {
		t.Fatalf("raised %d wants, want one", result.Raised)
	}
	if store.raised[0].LibraryFileID != below.FileID {
		t.Fatalf("raised a want for the wrong file")
	}
}

// A lossless file is never below the floor, whatever number is stored: FLAC
// has no bitrate a floor is measured against, and BelowFloor already refuses
// to treat one as under anything.
func TestSweepNeverTargetsALosslessFile(t *testing.T) {
	store := &fakeStore{
		settings:    db.UpgradeSettingsRow{Enabled: true},
		preferences: db.SourcePreferencesRow{MinimumBitRate: 1411},
		candidates:  []db.UpgradeCandidateRow{candidate("/music/track.flac", 900)},
	}
	service := NewService(store, zerolog.Nop())

	result, err := service.Sweep(context.Background(), uuid.Nil)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if result.Raised != 0 {
		t.Fatalf("a lossless file was raised as an upgrade candidate")
	}
}

// A file at or above the floor is left alone.
func TestSweepLeavesAFileAtTheFloorAlone(t *testing.T) {
	store := &fakeStore{
		settings:    db.UpgradeSettingsRow{Enabled: true},
		preferences: db.SourcePreferencesRow{MinimumBitRate: 320},
		candidates:  []db.UpgradeCandidateRow{candidate("/music/track.mp3", 320)},
	}
	service := NewService(store, zerolog.Nop())

	result, err := service.Sweep(context.Background(), uuid.Nil)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if result.Raised != 0 {
		t.Fatalf("a file at the floor was raised as an upgrade candidate")
	}
}

// A manual decision to stop pursuing a recording wins over the sweep.
func TestSweepSkipsARecordingSomebodySaidNotToPursue(t *testing.T) {
	below := candidate("/music/track.mp3", 128)
	store := &fakeStore{
		settings:    db.UpgradeSettingsRow{Enabled: true},
		preferences: db.SourcePreferencesRow{MinimumBitRate: 320},
		candidates:  []db.UpgradeCandidateRow{below},
		notWanted:   map[uuid.UUID]bool{below.RecordingID: true},
	}
	service := NewService(store, zerolog.Nop())

	result, err := service.Sweep(context.Background(), uuid.Nil)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if result.Raised != 0 {
		t.Fatalf("raised a want for a recording somebody said not to pursue")
	}
}

func TestCountBelowFloorCountsAcrossPages(t *testing.T) {
	store := &fakeStore{
		preferences: db.SourcePreferencesRow{MinimumBitRate: 320},
		candidates: []db.UpgradeCandidateRow{
			candidate("/music/one.mp3", 128),
			candidate("/music/two.flac", 900),
			candidate("/music/three.mp3", 320),
			candidate("/music/four.mp3", 192),
		},
	}
	service := NewService(store, zerolog.Nop())

	count, err := service.CountBelowFloor(context.Background())
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 2 {
		t.Fatalf("counted %d files below the floor, want 2", count)
	}
}
