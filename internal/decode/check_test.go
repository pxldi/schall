package decode

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/db"
	"github.com/rs/zerolog"
)

// The stderr below is what ffmpeg 7 printed for the files in testdata. The
// three picture cases are the three kinds the live library had.
func TestClassifyIgnoresPictureOnlyErrors(t *testing.T) {
	for name, stderr := range map[string]string{
		"broken APP fields": "[mjpeg @ 0x55a560549a00] app: invalid len 32767\n" +
			"[mjpeg @ 0x55a560549a00] unable to decode APP fields: Invalid data found when processing input\n",
		"JPEG labelled PNG": "[png @ 0x556ae1e76a00] Invalid PNG signature 0xFFD8FFE000104A46.\n",
		"no MIME type":      "[in#0 @ 0x559de0eaae40] Could not read mimetype from an attached picture.\n",
		"nothing at all":    "",
	} {
		if got := classify(stderr, 0); got != "" {
			t.Errorf("%s: classified as damaged with %q", name, got)
		}
	}
}

func TestClassifyRecordsTheFirstAudioError(t *testing.T) {
	for name, test := range map[string]struct {
		stderr   string
		exitCode int
		want     string
	}{
		"FLAC residual": {
			stderr: "[flac @ 0x55f674afec40] illegal residual coding method 3\n" +
				"[flac @ 0x55f674afec40] decode_frame() failed\n" +
				"[aist#0:0/flac @ 0x55f674afe300] [dec:flac @ 0x55f674b10140] Decoding error: Invalid data found when processing input\n",
			exitCode: 183,
			want:     "illegal residual coding method 3",
		},
		"MP3 header": {
			stderr: "[mp3float @ 0x5565127bc8c0] Header missing\n" +
				"[aist#0:0/mp3 @ 0x5565127ac380] [dec:mp3float @ 0x5565127b3e80] Error submitting packet to decoder\n",
			exitCode: 183,
			want:     "Header missing",
		},
		"picture first, audio after": {
			stderr: "[png @ 0x1] Invalid PNG signature 0xFFD8FFE000104A46.\n" +
				"[aist#0:0/flac @ 0x2] [dec:flac @ 0x3] Decoding error: Invalid data found when processing input\n",
			exitCode: 183,
			want:     "Decoding error: Invalid data found when processing input",
		},
		"an error that did not stop ffmpeg": {
			stderr:   "[null @ 0x1] Application provided invalid, non monotonically increasing dts to muxer in stream 0: 2048 >= 1024\n",
			exitCode: 0,
			want:     "Application provided invalid, non monotonically increasing dts to muxer in stream 0: 2048 >= 1024",
		},
		"a failure with nothing said": {
			stderr:   "",
			exitCode: 183,
			want:     "ffmpeg could not decode the audio (exit status 183)",
		},
	} {
		if got := classify(test.stderr, test.exitCode); got != test.want {
			t.Errorf("%s: got %q; want %q", name, got, test.want)
		}
	}
}

// The same classification against real ffmpeg and real files. It is skipped
// where ffmpeg is not installed; the canned tests above cover the classifier
// there.
func TestCheckClassifiesTheTestdataFiles(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}
	checker := NewChecker(Options{})
	for file, damagedBy := range map[string]string{
		"clean.flac":            "",
		"clean.mp3":             "",
		"picture-app.flac":      "",
		"picture-labelled.flac": "",
		"picture-nomime.flac":   "",
		"damaged.flac":          "residual",
		"damaged.mp3":           "Header missing",
	} {
		got, err := checker.Check(context.Background(), "testdata/"+file)
		if err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		switch {
		case damagedBy == "" && got != "":
			t.Errorf("%s: classified as damaged with %q", file, got)
		case damagedBy != "" && !strings.Contains(got, damagedBy):
			t.Errorf("%s: got %q; want an error naming %q", file, got, damagedBy)
		case strings.HasPrefix(got, "["):
			t.Errorf("%s: kept ffmpeg's prefix in %q", file, got)
		}
	}
}

func TestCheckReportsAMissingDecoder(t *testing.T) {
	checker := NewChecker(Options{Run: func(context.Context, string, ...string) ([]byte, int, error) {
		return nil, 0, exec.ErrNotFound
	}})
	if _, err := checker.Check(context.Background(), "/music/track.flac"); err != ErrUnavailable {
		t.Fatalf("err = %v; want ErrUnavailable", err)
	}
}

type recordingStore struct {
	due     []db.FileToDecodeCheckRow
	written map[uuid.UUID]string
}

func (store *recordingStore) FilesToDecodeCheck(context.Context, int32) ([]db.FileToDecodeCheckRow, error) {
	return store.due, nil
}

func (store *recordingStore) RecordDecodeCheck(_ context.Context, fileID uuid.UUID, decodeError string) error {
	store.written[fileID] = decodeError
	return nil
}

type fixedChecker struct {
	results map[string]string
	failing map[string]error
}

func (fixedChecker) Available() bool { return true }
func (fixedChecker) Binary() string  { return "ffmpeg" }
func (checker fixedChecker) Check(_ context.Context, path string) (string, error) {
	if err := checker.failing[path]; err != nil {
		return "", err
	}
	return checker.results[path], nil
}

func TestSweepRecordsEachFileAndSkipsOnesItCouldNotCheck(t *testing.T) {
	clean, damaged, unreadable := uuid.New(), uuid.New(), uuid.New()
	store := &recordingStore{written: map[uuid.UUID]string{}, due: []db.FileToDecodeCheckRow{
		{ID: clean, Path: "/music/clean.flac"},
		{ID: damaged, Path: "/music/damaged.mp3"},
		{ID: unreadable, Path: "/music/gone.flac"},
	}}
	service := NewService(store, fixedChecker{
		results: map[string]string{"/music/damaged.mp3": "Header missing"},
		failing: map[string]error{"/music/gone.flac": context.DeadlineExceeded},
	}, zerolog.Nop())

	more, err := service.Sweep(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if more {
		t.Fatal("a pass that did not fill its batch asked to come straight back")
	}
	if got, ok := store.written[clean]; !ok || got != "" {
		t.Errorf("clean file recorded as %q (recorded %t)", got, ok)
	}
	if store.written[damaged] != "Header missing" {
		t.Errorf("damaged file recorded as %q", store.written[damaged])
	}
	if _, ok := store.written[unreadable]; ok {
		t.Error("a file nobody could check was recorded as checked")
	}
}
