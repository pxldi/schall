package db

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/dbtest"
)

// shape is a waveform in the shape the reader writes: one byte a bar, with the
// loudest bar at full scale.
func shape(loudestBar int) []byte {
	peaks := make([]byte, 200)
	for bar := range peaks {
		peaks[bar] = byte(bar % 64)
	}
	peaks[loudestBar] = 255
	return peaks
}

// The waveform is written with the verdict and read back with the copy, so the
// card can draw it without decoding the file again. A later pass that drew
// nothing leaves the picture alone.
func TestAHeldCopyKeepsTheShapeOfItsAudio(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
	if _, err := queries.ClaimTargetImport(ctx, requestID); err != nil {
		t.Fatal(err)
	}
	held := SettleAcquiredFileParams{
		RecordAcquiredFileParams: RecordAcquiredFileParams{
			AcquisitionTargetID: targetID, Provider: "slskd", SourceUsername: "peer",
			RemotePath: `Music\Anetha\03.flac`, FileName: "03.flac", SizeBytes: 9000000,
			Verdict: AcquiredFileHeld, Summary: "Nothing could decide.",
			DownloadRequestID: uuid.NullUUID{UUID: requestID, Valid: true},
			Waveform:          shape(7),
		},
		ImportStatus: "needs_review", ImportError: "nothing could decide",
		Evidence: &AcquiredFileEvidence{
			Name: "03.flac", Agrees: []string{}, Differs: []string{}, Problems: []string{},
		},
		TargetSummary: "The next copy will be tried.",
		NextAttemptAt: time.Now(), Outcome: "inconclusive",
	}
	if err := queries.SettleAcquiredFile(ctx, held); err != nil {
		t.Fatalf("SettleAcquiredFile() error = %v", err)
	}

	copies, err := queries.AcquisitionTargetFiles(ctx, targetID)
	if err != nil {
		t.Fatal(err)
	}
	if len(copies) != 1 {
		t.Fatalf("copies = %d, want the one held copy", len(copies))
	}
	if len(copies[0].Waveform) != 200 || copies[0].Waveform[7] != 255 {
		t.Fatalf("waveform = %d bytes, want the 200 that were written",
			len(copies[0].Waveform))
	}

	// A judging pass that could not read the audio must not erase the picture an
	// earlier one drew.
	held.RecordAcquiredFileParams.Waveform = nil
	if err := queries.RecordAcquiredFile(ctx, held.RecordAcquiredFileParams); err != nil {
		t.Fatalf("RecordAcquiredFile() error = %v", err)
	}
	again, err := queries.AcquisitionTargetFiles(ctx, targetID)
	if err != nil {
		t.Fatal(err)
	}
	if len(again[0].Waveform) != 200 || again[0].Waveform[7] != 255 {
		t.Fatalf("waveform = %d bytes, want the picture kept", len(again[0].Waveform))
	}

	// The backfill writes one column and leaves the question alone.
	if err := queries.RecordCopyWaveform(ctx, again[0].ID, shape(190)); err != nil {
		t.Fatalf("RecordCopyWaveform() error = %v", err)
	}
	drawn, err := queries.AcquiredCopy(ctx, again[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if drawn.Waveform[190] != 255 || drawn.Waveform[7] == 255 {
		t.Errorf("waveform = %v…, want the reading that was just kept", drawn.Waveform[:8])
	}
	if drawn.Verdict != AcquiredFileHeld || drawn.Summary != "Nothing could decide." {
		t.Errorf("copy = %+v, want the question untouched by a picture", drawn)
	}
}
