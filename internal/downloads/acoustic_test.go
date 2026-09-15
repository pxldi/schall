package downloads

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/acoustid"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/library"
	"github.com/rs/zerolog"
)

type fakeIdentifier struct {
	clusters []acoustid.Cluster
	// durationSeconds is what the decode measured, as fpcalc would report it.
	durationSeconds int
	err             error
	configured      bool
	asked           []string
}

func (identifier *fakeIdentifier) Configured(context.Context) bool { return identifier.configured }

func (identifier *fakeIdentifier) Identify(
	_ context.Context, path string,
) (acoustid.Identification, error) {
	identifier.asked = append(identifier.asked, filepath.Base(path))
	if identifier.err != nil {
		return acoustid.Identification{}, identifier.err
	}
	return acoustid.Identification{
		DurationSeconds: identifier.durationSeconds, Clusters: identifier.clusters,
	}, nil
}

// cluster is one of AcoustID's fingerprint clusters, written the way a test reads
// best: what it scored, and the recordings MusicBrainz links to it.
func cluster(score float64, ids ...uuid.UUID) acoustid.Cluster {
	return acoustid.Cluster{Score: score, Recordings: ids}
}

// acousticRelease sets up a one-track release whose tags agree with the
// catalogue in every way, so the only thing any test here can change is what
// the audio itself turns out to be.
func acousticRelease(t *testing.T, recordingID *uuid.UUID) (*fakeImportStore, string, string, uuid.UUID) {
	t.Helper()
	inbox, libraryPath := t.TempDir(), t.TempDir()
	folder := filepath.Join(inbox, "Album")
	if err := os.Mkdir(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, "01.flac"), []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}

	requestID := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	store := &fakeImportStore{row: db.DownloadImportRow{
		RequestID: requestID, ArtistName: "Artist", AlbumTitle: "Album",
		SourceDirectory: `Remote\Album`,
		Files:           []db.DownloadRequestFile{{Path: `Music\Album\01.flac`, Name: "01.flac", SizeBytes: 3}},
		Tracks: []db.ImportTrack{{
			Title: "First", DiscNumber: 1, TrackNumber: 1, DurationMS: 100_000,
			MusicBrainzRecordingID: recordingID,
		}},
	}}
	return store, inbox, libraryPath, requestID
}

func acousticImporter(
	store *fakeImportStore, inbox, libraryPath string, identifier AcousticIdentifier,
) *Importer {
	importer := NewImporter(store, inbox, libraryPath, zerolog.Nop())
	importer.inspect = func(string) library.AudioMetadata {
		return library.AudioMetadata{
			Artist: "Artist", Album: "Album", Title: "First",
			DiscNumber: 1, TrackNumber: 1, DurationMS: 100_000,
		}
	}
	return importer.WithAcousticIdentifier(identifier)
}

// Tags, length and position can all agree while the audio is something else
// entirely. This is the case nothing else in validation can see.
func TestImporterRefusesAFileWhoseAudioIsADifferentRecording(t *testing.T) {
	expected := uuid.New()
	store, inbox, libraryPath, requestID := acousticRelease(t, &expected)
	importer := acousticImporter(store, inbox, libraryPath, &fakeIdentifier{
		configured: true,
		clusters:   []acoustid.Cluster{cluster(0.98, uuid.New())},
	})

	if err := importer.Import(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}
	if store.review == "" {
		t.Fatal("a file whose audio is a different recording was imported")
	}
	if !strings.Contains(store.review, "different recording") {
		t.Fatalf("review = %q, want it to say the audio disagrees", store.review)
	}
}

func TestImporterImportsAFileWhoseAudioIsTheExpectedRecording(t *testing.T) {
	expected := uuid.New()
	store, inbox, libraryPath, requestID := acousticRelease(t, &expected)
	importer := acousticImporter(store, inbox, libraryPath, &fakeIdentifier{
		configured: true,
		clusters:   []acoustid.Cluster{cluster(0.99, expected)},
	})

	if err := importer.Import(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}
	if store.review != "" {
		t.Fatalf("review = %q, want the agreeing audio imported", store.review)
	}
}

// Validating a folder somebody chose, the audio may only object — nothing here
// is admitted by a fingerprint, so an answer naming the catalogue's recording
// anywhere in it is enough to stop objecting. That is deliberately not the rule
// for a copy fetched for a want, where the audio is the only thing that admits
// and is therefore read at its leading cluster alone.
func TestAFolderIsNotRefusedWhenALowerClusterNamesTheCatalogueTrack(t *testing.T) {
	expected := uuid.New()
	store, inbox, libraryPath, requestID := acousticRelease(t, &expected)
	importer := acousticImporter(store, inbox, libraryPath, &fakeIdentifier{
		configured: true,
		clusters:   []acoustid.Cluster{cluster(0.99, uuid.New()), cluster(0.4, expected)},
	})

	if err := importer.Import(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}
	if store.review != "" {
		t.Fatalf("review = %q, want the folder path to go on objecting only when nothing named it",
			store.review)
	}
}

// AcoustID has never heard most self-released music. An unrecognised recording
// says nothing whatever about the file, and treating silence as disagreement
// would refuse most of a small library.
func TestImporterImportsWhenTheAudioIsNotRecognised(t *testing.T) {
	expected := uuid.New()
	store, inbox, libraryPath, requestID := acousticRelease(t, &expected)
	importer := acousticImporter(store, inbox, libraryPath, &fakeIdentifier{configured: true})

	if err := importer.Import(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}
	if store.review != "" {
		t.Fatalf("review = %q, want unrecognised audio to decide nothing", store.review)
	}
}

// A check that could not run must not quietly pass as though it had, nor refuse
// a file it never looked at.
func TestImporterImportsButRecordsWhyTheAcousticCheckCouldNotRun(t *testing.T) {
	expected := uuid.New()
	store, inbox, libraryPath, requestID := acousticRelease(t, &expected)
	importer := acousticImporter(store, inbox, libraryPath, &fakeIdentifier{
		configured: true, err: errors.New("AcoustID is unreachable"),
	})

	if err := importer.Import(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}
	if store.review != "" {
		t.Fatalf("review = %q, want a check that could not run to refuse nothing", store.review)
	}
}

// A catalogue track with no recording ID has nothing to compare against, so the
// audio is recorded as evidence and decides nothing.
func TestImporterImportsWhenTheCatalogueHasNoRecordingToCompare(t *testing.T) {
	store, inbox, libraryPath, requestID := acousticRelease(t, nil)
	importer := acousticImporter(store, inbox, libraryPath, &fakeIdentifier{
		configured: true,
		clusters:   []acoustid.Cluster{cluster(0.97, uuid.New())},
	})

	if err := importer.Import(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}
	if store.review != "" {
		t.Fatalf("review = %q, want nothing to compare against to decide nothing", store.review)
	}
}

func TestImporterAsksNothingWhenAcousticCheckingIsOff(t *testing.T) {
	expected := uuid.New()
	store, inbox, libraryPath, requestID := acousticRelease(t, &expected)
	identifier := &fakeIdentifier{
		configured: false,
		clusters:   []acoustid.Cluster{cluster(0.99, uuid.New())},
	}
	importer := acousticImporter(store, inbox, libraryPath, identifier)

	if err := importer.Import(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}
	if len(identifier.asked) != 0 {
		t.Fatalf("asked %v, want nothing identified while the check is off", identifier.asked)
	}
	if store.review != "" {
		t.Fatalf("review = %q, want validation unchanged while the check is off", store.review)
	}
}
