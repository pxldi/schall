package spectrum

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/db"
	"github.com/rs/zerolog"
)

type recordingStore struct {
	due     []db.FileToMeasureRow
	written []db.FileSpectrumParams
	asked   int32
}

func (store *recordingStore) FilesToMeasure(
	_ context.Context, limit int32,
) ([]db.FileToMeasureRow, error) {
	store.asked = limit
	return store.due, nil
}

func (store *recordingStore) RecordFileSpectrum(
	_ context.Context, params db.FileSpectrumParams,
) error {
	store.written = append(store.written, params)
	return nil
}

type fixedReader struct {
	reading   Reading
	err       error
	available bool
}

func (reader fixedReader) Available() bool { return reader.available }

func (reader fixedReader) Binary() string { return "ffmpeg" }

func (reader fixedReader) ReadWithBitRate(
	context.Context, string, int, int, int,
) (Reading, error) {
	return reader.reading, reader.err
}

func TestSweepWritesWhatEachFileTurnedOutToBe(t *testing.T) {
	fileID := uuid.New()
	store := &recordingStore{due: []db.FileToMeasureRow{
		{ID: fileID, Path: "/music/track.flac", SampleRateHz: 44100, DurationMS: 200_000},
	}}
	service := NewService(store, fixedReader{
		available: true,
		reading: Reading{
			Measurement:        Measurement{CutoffHz: 15000, Judged: true},
			Encoder:            "LAME3.100",
			TranscodeSuspected: true,
		},
	}, zerolog.Nop())

	more, err := service.Sweep(context.Background())
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if more {
		t.Fatal("a pass that did not fill its batch asked to come straight back")
	}
	if len(store.written) != 1 {
		t.Fatalf("wrote %d rows, want one per file", len(store.written))
	}
	written := store.written[0]
	if written.FileID != fileID || !written.Judged || written.CutoffHz != 15000 ||
		!written.TranscodeSuspected || written.Encoder != "LAME3.100" {
		t.Fatalf("wrote %+v, want what the reader measured", written)
	}
}

// A file that concluded nothing is still written down. Otherwise the sweep asks
// about it on every pass forever, and the three facts stay null — which is the
// right answer: nothing was learned, and that is never agreement that the file
// is fine.
func TestSweepRecordsAFileThatConcludedNothing(t *testing.T) {
	store := &recordingStore{due: []db.FileToMeasureRow{
		{ID: uuid.New(), Path: "/music/silence.flac", SampleRateHz: 44100},
	}}
	service := NewService(store, fixedReader{available: true}, zerolog.Nop())

	if _, err := service.Sweep(context.Background()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(store.written) != 1 {
		t.Fatalf("wrote %d rows, want the look to be written down", len(store.written))
	}
	if store.written[0].Judged {
		t.Fatal("a file nothing was concluded about was written down as judged")
	}
}

// No decoder is a pass that could not run, and not a failure: a machine
// without ffmpeg will never grow one by being retried. Nothing is written — a
// library marked as looked at by a machine that cannot look would never be
// looked at again — and the pass completes clean with nothing left to do.
func TestSweepWritesNothingWithoutADecoder(t *testing.T) {
	store := &recordingStore{due: []db.FileToMeasureRow{
		{ID: uuid.New(), Path: "/music/track.flac", SampleRateHz: 44100},
	}}
	service := NewService(store, fixedReader{}, zerolog.Nop())

	more, err := service.Sweep(context.Background())
	if err != nil {
		t.Fatalf("sweep: %v, want no ffmpeg to be a clean pass rather than a failure", err)
	}
	if more {
		t.Fatal("a pass with no decoder asked for another turn")
	}
	if len(store.written) != 0 {
		t.Fatalf("wrote %d rows without a decoder", len(store.written))
	}
}

func TestSweepAsksForAnotherPassWhenItFilledItsBatch(t *testing.T) {
	store := &recordingStore{}
	for range sweepBatch {
		store.due = append(store.due, db.FileToMeasureRow{
			ID: uuid.New(), Path: "/music/track.flac", SampleRateHz: 44100,
		})
	}
	service := NewService(store, fixedReader{available: true}, zerolog.Nop())

	more, err := service.Sweep(context.Background())
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if !more {
		t.Fatal("a full batch did not ask for the next one")
	}
	if store.asked != sweepBatch {
		t.Fatalf("asked for %d files, want the batch to be bounded at %d", store.asked, sweepBatch)
	}
}
