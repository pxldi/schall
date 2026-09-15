package acoustid

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func fakeFpcalc(output string, err error) func(context.Context, string, ...string) ([]byte, error) {
	return func(context.Context, string, ...string) ([]byte, error) {
		if err != nil {
			return nil, err
		}
		return []byte(output), nil
	}
}

// fpcalc printing a fingerprint and complaining at the same time, which is what
// a file with one damaged frame in sound audio does.
func fakeFpcalcSaying(
	output string, err error,
) func(context.Context, string, ...string) ([]byte, error) {
	return func(context.Context, string, ...string) ([]byte, error) {
		return []byte(output), err
	}
}

// A fingerprint standing for a given number of items, which is how much audio
// went into it. Only the four-byte header is real, because that is the only part
// anything here reads.
func printOf(items int) string {
	raw := []byte{1, byte(items >> 16), byte(items >> 8), byte(items), 0x42, 0x42}
	return base64.RawURLEncoding.EncodeToString(raw)
}

func TestFingerprintReadsTheDecodedLengthAndPrint(t *testing.T) {
	client := NewClient(Options{
		Run: fakeFpcalc(`{"duration": 253.72, "fingerprint": "AQABZ0mUaEkS"}`, nil),
	})

	print, err := client.Fingerprint(context.Background(), "/music/01.flac")
	if err != nil {
		t.Fatalf("Fingerprint() error = %v", err)
	}
	if print.Value != "AQABZ0mUaEkS" {
		t.Fatalf("fingerprint = %q", print.Value)
	}
	// The length comes from the decoded audio, which is the point: a tag can
	// claim any duration, and the ones that lie are the ones worth catching.
	if print.DurationSeconds != 254 {
		t.Fatalf("duration = %d, want the decoded length rounded to 254", print.DurationSeconds)
	}
}

// A file that will not decode is a fact about the bytes. Retrying cannot change
// it, so it must not surface as a failure worth retrying.
func TestFingerprintReportsUndecodableAudioAsSuch(t *testing.T) {
	client := NewClient(Options{
		Run: fakeFpcalc("", errors.New("ERROR: couldn't open the file")),
	})

	_, err := client.Fingerprint(context.Background(), "/music/broken.flac")
	if !errors.Is(err, ErrNoFingerprint) {
		t.Fatalf("error = %v, want %v", err, ErrNoFingerprint)
	}
}

// fpcalc reads past a damaged frame, prints the fingerprint it computed, and
// exits non-zero to report the complaint. The fingerprint covers the file, so it
// is the answer; the complaint is carried as evidence about the copy.
func TestFingerprintKeepsAPrintComputedFromDamagedAudio(t *testing.T) {
	client := NewClient(Options{
		Run: fakeFpcalcSaying(
			`{"duration": 96.03, "fingerprint": "`+printOf(752)+`"}`,
			errors.New("ERROR: Error reading from the audio source"),
		),
	})

	print, err := client.Fingerprint(context.Background(), "/music/clicky.flac")
	if err != nil {
		t.Fatalf("Fingerprint() error = %v", err)
	}
	if print.Value == "" || print.DurationSeconds != 96 {
		t.Fatalf("print = %+v", print)
	}
	if print.DecodedSeconds != 96 {
		t.Fatalf("decoded = %d, want the whole 96s of audio", print.DecodedSeconds)
	}
	if !strings.Contains(print.Damaged, "Error reading") {
		t.Fatalf("damaged = %q, want the decoder's complaint", print.Damaged)
	}
}

// A fingerprint of the opening of a long track is complete, because that is all
// fpcalc reads. It must not be mistaken for a decode that gave up.
func TestFingerprintAcceptsTheOpeningItReadsOfALongTrack(t *testing.T) {
	client := NewClient(Options{
		Run: fakeFpcalcSaying(
			`{"duration": 600, "fingerprint": "`+printOf(948)+`"}`,
			errors.New("ERROR: Error reading from the audio source"),
		),
	})

	print, err := client.Fingerprint(context.Background(), "/music/long.flac")
	if err != nil {
		t.Fatalf("Fingerprint() error = %v", err)
	}
	if print.DecodedSeconds != 120 {
		t.Fatalf("decoded = %d, want fpcalc's two-minute window", print.DecodedSeconds)
	}
}

// A fingerprint of part of a file describes part of a file. Offering it as a
// description of the whole is the guess this package exists to refuse.
func TestFingerprintRefusesAPrintThatCoversLittleOfTheFile(t *testing.T) {
	client := NewClient(Options{
		Run: fakeFpcalcSaying(
			`{"duration": 96.03, "fingerprint": "`+printOf(100)+`"}`,
			errors.New("ERROR: Error reading from the audio source"),
		),
	})

	_, err := client.Fingerprint(context.Background(), "/music/truncated.flac")
	if !errors.Is(err, ErrNoFingerprint) {
		t.Fatalf("error = %v, want %v", err, ErrNoFingerprint)
	}
	if !strings.Contains(err.Error(), "15s of 96s") {
		t.Fatalf("error = %v, want it to say how far the decode got", err)
	}
}

// A clean decode is not asked how much it covered. Nothing complained, so there
// is nothing to weigh the fingerprint against.
func TestFingerprintAsksNothingOfACleanDecode(t *testing.T) {
	client := NewClient(Options{
		Run: fakeFpcalc(`{"duration": 96.03, "fingerprint": "`+printOf(100)+`"}`, nil),
	})

	print, err := client.Fingerprint(context.Background(), "/music/short-window.flac")
	if err != nil {
		t.Fatalf("Fingerprint() error = %v", err)
	}
	if print.Damaged != "" {
		t.Fatalf("damaged = %q, want nothing", print.Damaged)
	}
}

func TestFingerprintRejectsOutputWithNoAudio(t *testing.T) {
	client := NewClient(Options{
		Run: fakeFpcalc(`{"duration": 0, "fingerprint": ""}`, nil),
	})

	if _, err := client.Fingerprint(context.Background(), "/music/silence.flac"); !errors.Is(
		err, ErrNoFingerprint,
	) {
		t.Fatalf("error = %v, want %v", err, ErrNoFingerprint)
	}
}

// Two results are two pieces of audio, one of which merely resembled the
// fingerprint. They arrive as two clusters, each with its own score, because
// flattening them makes a resemblance read exactly like the real thing.
func TestIdentifyKeepsAcoustIDsClustersApartAndScored(t *testing.T) {
	first, second := uuid.New(), uuid.New()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","results":[
			{"id":"track-one","score":0.97,"recordings":[{"id":"` + first.String() + `"}]},
			{"score":0.41,"recordings":[{"id":"` + second.String() + `"}]}]}`))
	}))
	defer server.Close()

	client := NewClient(Options{APIKey: "key", BaseURL: server.URL})
	clusters, err := client.Identify(context.Background(),
		Fingerprint{DurationSeconds: 254, Value: "AQABZ0mUaEkS"})
	if err != nil {
		t.Fatalf("Identify() error = %v", err)
	}

	if len(clusters) != 2 {
		t.Fatalf("got %d clusters, want AcoustID's two kept apart", len(clusters))
	}
	if clusters[0].ID != "track-one" || clusters[0].Score != 0.97 || len(clusters[0].Recordings) != 1 ||
		clusters[0].Recordings[0] != first {
		t.Fatalf("best = %#v, want %s alone at 0.97", clusters[0], first)
	}
	if clusters[1].Score != 0.41 || len(clusters[1].Recordings) != 1 ||
		clusters[1].Recordings[0] != second {
		t.Fatalf("second = %#v, want %s alone at 0.41", clusters[1], second)
	}
}

// The lookup is formed on the decoded length, which is the length AcoustID
// matches against and the one number here nobody typed.
func TestIdentifyLooksUpTheDecodedDuration(t *testing.T) {
	var received string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, 4096)
		n, _ := r.Body.Read(body)
		received = string(body[:n])
		_, _ = w.Write([]byte(`{"status":"ok","results":[]}`))
	}))
	defer server.Close()

	client := NewClient(Options{APIKey: "key", BaseURL: server.URL})
	if _, err := client.Identify(context.Background(),
		Fingerprint{DurationSeconds: 254, Value: "AQABZ0mUaEkS"}); err != nil {
		t.Fatalf("Identify() error = %v", err)
	}
	if !strings.Contains(received, "duration=254") {
		t.Fatalf("request body %q did not carry the decoded duration", received)
	}
}

// One performance can appear on an album and a compilation under the same
// recording. Reporting it twice would make agreement look like corroboration.
func TestIdentifyReportsEachRecordingOnceWithinACluster(t *testing.T) {
	id := uuid.New()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok","results":[
			{"score":0.9,"recordings":[{"id":"` + id.String() + `"},{"id":"` + id.String() + `"}]}]}`))
	}))
	defer server.Close()

	client := NewClient(Options{APIKey: "key", BaseURL: server.URL})
	clusters, err := client.Identify(context.Background(),
		Fingerprint{DurationSeconds: 100, Value: "print"})
	if err != nil {
		t.Fatalf("Identify() error = %v", err)
	}
	if len(clusters) != 1 || len(clusters[0].Recordings) != 1 {
		t.Fatalf("got %#v, want the duplicate collapsed", clusters)
	}
}

// The same recording in two clusters is two different statements about it: one
// cluster says this is that audio, the other says something that merely sounds
// like it was linked to the same row. Collapsing them across would silently
// answer a question only the reader can.
func TestIdentifyKeepsARecordingNamedByTwoClusters(t *testing.T) {
	id := uuid.New()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok","results":[
			{"score":0.9,"recordings":[{"id":"` + id.String() + `"}]},
			{"score":0.3,"recordings":[{"id":"` + id.String() + `"}]}]}`))
	}))
	defer server.Close()

	client := NewClient(Options{APIKey: "key", BaseURL: server.URL})
	clusters, err := client.Identify(context.Background(),
		Fingerprint{DurationSeconds: 100, Value: "print"})
	if err != nil {
		t.Fatalf("Identify() error = %v", err)
	}
	if len(clusters) != 2 || len(clusters[1].Recordings) != 1 {
		t.Fatalf("got %#v, want the name kept in both clusters", clusters)
	}
}

// A recording ID the service returns unparseable is not a recording. Dropping it
// is the only honest thing to do with it, and the cluster it sat in stands.
func TestIdentifyDropsANameThatIsNotAnIdentifier(t *testing.T) {
	id := uuid.New()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok","results":[
			{"score":0.9,"recordings":[{"id":"not-a-uuid"},{"id":"` + id.String() + `"}]}]}`))
	}))
	defer server.Close()

	client := NewClient(Options{APIKey: "key", BaseURL: server.URL})
	clusters, err := client.Identify(context.Background(),
		Fingerprint{DurationSeconds: 100, Value: "print"})
	if err != nil {
		t.Fatalf("Identify() error = %v", err)
	}
	if len(clusters) != 1 || len(clusters[0].Recordings) != 1 ||
		clusters[0].Recordings[0] != id {
		t.Fatalf("got %#v, want only the identifier that parsed", clusters)
	}
}

// Audio AcoustID has never seen is common for small labels and self-released
// music. It says nothing against the file and must not read as a failure.
func TestIdentifyTreatsAnUnknownRecordingAsAnAnswer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok","results":[]}`))
	}))
	defer server.Close()

	client := NewClient(Options{APIKey: "key", BaseURL: server.URL})
	clusters, err := client.Identify(context.Background(),
		Fingerprint{DurationSeconds: 100, Value: "print"})
	if err != nil {
		t.Fatalf("Identify() error = %v, want an empty answer rather than a failure", err)
	}
	if len(clusters) != 0 {
		t.Fatalf("got %#v, want nothing", clusters)
	}
}

func TestIdentifyRefusesWithoutAnAPIKey(t *testing.T) {
	client := NewClient(Options{})
	if _, err := client.Identify(context.Background(),
		Fingerprint{DurationSeconds: 100, Value: "print"},
	); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("error = %v, want %v", err, ErrNotConfigured)
	}
}

func TestIdentifySurfacesAServiceRefusal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"error","error":{"message":"invalid API key"}}`))
	}))
	defer server.Close()

	client := NewClient(Options{APIKey: "wrong", BaseURL: server.URL})
	_, err := client.Identify(context.Background(),
		Fingerprint{DurationSeconds: 100, Value: "print"})
	if err == nil || !strings.Contains(err.Error(), "invalid API key") {
		t.Fatalf("error = %v, want the service's own reason", err)
	}
}
