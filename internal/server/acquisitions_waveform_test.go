package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/acquisition"
	"github.com/pxldi/schall/internal/db"
	"github.com/rs/zerolog"
)

// stubWaveforms stands in for the reader that runs ffmpeg.
type stubWaveforms struct {
	available bool
	peaks     []byte
	err       error
	read      []string
}

func (stub *stubWaveforms) Available() bool { return stub.available }

func (stub *stubWaveforms) Peaks(_ context.Context, path string) ([]byte, error) {
	stub.read = append(stub.read, path)
	return stub.peaks, stub.err
}

// copyInInbox writes a file where the inbox path of a fetched copy resolves to.
func copyInInbox(t *testing.T) string {
	t.Helper()
	inbox := t.TempDir()
	if err := os.MkdirAll(filepath.Join(inbox, "Anetha"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inbox, "Anetha", "03.flac"), []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	return inbox
}

func waveformPeaks(t *testing.T, body *httptest.ResponseRecorder) []int {
	t.Helper()
	var payload struct {
		Peaks []int `json:"peaks"`
	}
	if err := json.NewDecoder(body.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	return payload.Peaks
}

// A copy the importer drew is served from the column. Nothing is decoded again:
// the bytes have not moved, so neither has the picture.
func TestAStoredWaveformIsServedWithoutReadingTheFile(t *testing.T) {
	reader := &stubWaveforms{available: true, peaks: []byte{9, 9, 9}}
	targets := &fakeAcquisitionTargets{
		copies: []db.AcquisitionTargetFileRow{{
			ID: uuid.New(), Verdict: db.AcquiredFileHeld,
			RemotePath: `Music\Anetha\03.flac`, FileName: "03.flac",
			Waveform: []byte{0, 128, 255},
		}},
	}
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithAcquisitionTargets(targets), WithDownloadInbox(copyInInbox(t)),
		WithWaveforms(reader))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/review-queue/copies/"+uuid.NewString()+"/waveform", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.Code)
	}
	if peaks := waveformPeaks(t, response); len(peaks) != 3 ||
		peaks[0] != 0 || peaks[1] != 128 || peaks[2] != 255 {
		t.Fatalf("peaks = %v, want the stored bytes as numbers", peaks)
	}
	if len(reader.read) != 0 {
		t.Fatalf("the reader ran on %v, want a stored waveform served as it is", reader.read)
	}
	if got := response.Header().Get("Cache-Control"); got != "private, max-age=86400" {
		t.Errorf("cache-control = %q, want a picture that never changes cached", got)
	}
}

// A phone that already drew this shape sends back the ETag it was given, and
// gets a 304 rather than the peaks it already holds.
func TestAWaveformAnswersIfNoneMatchWithNotModified(t *testing.T) {
	targets := &fakeAcquisitionTargets{
		copies: []db.AcquisitionTargetFileRow{{
			ID: uuid.New(), Verdict: db.AcquiredFileHeld,
			RemotePath: `Music\Anetha\03.flac`, FileName: "03.flac",
			Waveform: []byte{0, 128, 255},
		}},
	}
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithAcquisitionTargets(targets), WithDownloadInbox(copyInInbox(t)))

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, httptest.NewRequest(http.MethodGet,
		"/api/v1/review-queue/copies/"+uuid.NewString()+"/waveform", nil))
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag on the first response")
	}

	request := httptest.NewRequest(http.MethodGet,
		"/api/v1/review-queue/copies/"+uuid.NewString()+"/waveform", nil)
	request.Header.Set("If-None-Match", etag)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304", response.Code)
	}
	if response.Body.Len() != 0 {
		t.Fatalf("body = %q, want empty on a 304", response.Body.String())
	}
}

// The backfill. A copy held before the importer kept a waveform has none, and
// asking for it reads the file in the inbox once and keeps the answer.
func TestAWaveformIsReadAndKeptForACopyHeldWithoutOne(t *testing.T) {
	inbox := copyInInbox(t)
	drawn := make([]byte, 200)
	drawn[7] = 255
	reader := &stubWaveforms{available: true, peaks: drawn}
	copyID := uuid.New()
	targets := &fakeAcquisitionTargets{
		copies: []db.AcquisitionTargetFileRow{{
			ID: copyID, Verdict: db.AcquiredFileHeld,
			RemotePath: `Music\Anetha\03.flac`, FileName: "03.flac",
		}},
	}
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithAcquisitionTargets(targets), WithDownloadInbox(inbox), WithWaveforms(reader))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/review-queue/copies/"+copyID.String()+"/waveform", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.Code)
	}
	peaks := waveformPeaks(t, response)
	if len(peaks) != 200 || peaks[7] != 255 {
		t.Fatalf("peaks = %d numbers with peaks[7] = %v, want the 200 that were read",
			len(peaks), peaks[:min(8, len(peaks))])
	}
	if len(reader.read) != 1 {
		t.Fatalf("the reader ran %d times, want one read of the file in the inbox", len(reader.read))
	}
	if want := filepath.Join(inbox, "Anetha", "03.flac"); reader.read[0] != want {
		t.Errorf("read %q, want %q", reader.read[0], want)
	}
	// Kept, so the next card costs no decode.
	if targets.waveformFor != copyID || len(targets.waveformWritten) != 200 {
		t.Errorf("stored %d bytes for %s, want the 200 read for %s",
			len(targets.waveformWritten), targets.waveformFor, copyID)
	}
}

// A copy nothing could read has no waveform, and that is a fact about the file
// rather than a broken installation.
func TestACopyNothingCanReadHasNoWaveform(t *testing.T) {
	targets := &fakeAcquisitionTargets{
		copies: []db.AcquisitionTargetFileRow{{
			ID: uuid.New(), Verdict: db.AcquiredFileHeld,
			RemotePath: `Music\Anetha\03.flac`, FileName: "03.flac",
		}},
	}
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithAcquisitionTargets(targets), WithDownloadInbox(copyInInbox(t)),
		WithWaveforms(&stubWaveforms{available: true}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/review-queue/copies/"+uuid.NewString()+"/waveform", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
}

// The waveform is gated exactly as playback is: a copy somebody already answered
// is not drawn for a decision nobody is making.
func TestAnAnsweredCopyHasNoWaveform(t *testing.T) {
	targets := &fakeAcquisitionTargets{
		target: db.AcquisitionTargetRow{Status: "acquired"},
		copies: []db.AcquisitionTargetFileRow{{
			ID: uuid.New(), Verdict: db.AcquiredFileAccepted,
			RemotePath: `Music\Anetha\03.flac`, FileName: "03.flac",
			Waveform: []byte{1, 2, 3},
		}},
	}
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithAcquisitionTargets(targets), WithDownloadInbox(copyInInbox(t)),
		WithWaveforms(&stubWaveforms{available: true, peaks: []byte{4, 5, 6}}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/review-queue/copies/"+uuid.NewString()+"/waveform", nil))
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", response.Code)
	}
}

// The one answer a stopped want takes, in one request.
func TestAcceptingTheLibraryFileAnswersAStoppedWant(t *testing.T) {
	targetID := uuid.New()
	targets := &fakeAcquisitionTargets{}
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithAcquisitionTargets(targets))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/review-queue/wants/"+targetID.String()+"/accept-file", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s, want 200", response.Code, response.Body)
	}
	var payload struct {
		Accepted bool `json:"accepted"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if !payload.Accepted {
		t.Error("accepted = false, want the answer recorded")
	}
	if targets.acceptedFile != targetID {
		t.Errorf("answered for %s, want %s", targets.acceptedFile, targetID)
	}
}

// A want that is no longer asking says so rather than writing a decision about
// something the queue has moved on from.
func TestAcceptingTheLibraryFileOfAWantThatIsNotStoppedIsRefused(t *testing.T) {
	targets := &fakeAcquisitionTargets{acceptFileErr: acquisition.ErrWantIsNotStopped}
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithAcquisitionTargets(targets))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/review-queue/wants/"+uuid.NewString()+"/accept-file", nil))
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", response.Code)
	}
}
