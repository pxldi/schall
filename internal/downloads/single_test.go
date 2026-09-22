package downloads

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/acoustid"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/identity"
	"github.com/pxldi/schall/internal/library"
	"github.com/pxldi/schall/internal/musicbrainz"
	"github.com/rs/zerolog"
)

// fakeVerifier stands in for the grader. What it returns is what the grader would
// have concluded; these tests are about what the importer then does with a file,
// and the rules themselves are pinned in internal/identity.
type fakeVerifier struct {
	verification identity.Verification
	err          error
	asked        []identity.Evidence
	recordings   []uuid.UUID
}

func (verifier *fakeVerifier) Verify(
	_ context.Context, evidence identity.Evidence, recordingID uuid.UUID,
) (identity.Verification, error) {
	verifier.asked = append(verifier.asked, evidence)
	verifier.recordings = append(verifier.recordings, recordingID)
	return verifier.verification, verifier.err
}

const wantedRecording = "3a3f2c26-1f8e-4b0a-9c6e-2f1d4d19f1aa"

// The release that recording belongs to, which is what a copy of it is filed
// under.
const wantedReleaseGroup = "44444444-4444-4444-4444-444444444444"

// fetchedCandidate sets up one file fetched for one want, with tags that agree
// with the recording in every way, so each test changes only the verdict.
func fetchedCandidate(t *testing.T, sizeBytes int64) (*fakeImportStore, string, string, uuid.UUID) {
	t.Helper()
	inbox, libraryPath := t.TempDir(), t.TempDir()
	folder := filepath.Join(inbox, "Anetha")
	if err := os.Mkdir(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, "03.flac"), []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	requestID := uuid.MustParse("33333333-3333-3333-3333-333333333333")
	store := &fakeImportStore{
		servesWant: true,
		targetRow: db.TargetImportRow{
			RequestID:              requestID,
			AcquisitionTargetID:    uuid.New(),
			MusicBrainzRecordingID: uuid.MustParse(wantedRecording),
			EntryArtist:            "Anetha",
			EntryTitle:             "Candy from Strangers",
			Provider:               "slskd",
			SourceUsername:         "peer",
			SourceDirectory:        `Music\Anetha`,
			File: db.DownloadRequestFile{
				Path: `Music\Anetha\03.flac`, Name: "03.flac", SizeBytes: sizeBytes,
			},
		},
	}
	return store, inbox, libraryPath, requestID
}

func candidateImporter(
	store *fakeImportStore, inbox, libraryPath string,
	identifier AcousticIdentifier, verifier Verifier,
) *Importer {
	importer := NewImporter(store, inbox, libraryPath, zerolog.Nop()).WithVerifier(verifier)
	importer.inspect = func(string) library.AudioMetadata {
		return library.AudioMetadata{
			Artist: "Anetha", Title: "Candy from Strangers", DurationMS: 487_000,
		}
	}
	if identifier != nil {
		importer = importer.WithAcousticIdentifier(identifier)
	}
	return importer
}

func provenByAudio() identity.Verification {
	durationMS := 487_278
	return identity.Verification{
		Identity: &identity.Identity{
			RecordingID: uuid.MustParse(wantedRecording), ArtistName: "Anetha",
			ReleaseGroupID: uuid.MustParse(wantedReleaseGroup),
			ReleaseTitle:   "Don't Rush to Grow Up", TrackTitle: "Candy from Strangers",
			DurationMS: &durationMS, Method: "fingerprint", Confidence: 1,
			Summary: "\"Candy from Strangers\" by Anetha — identified by its audio.",
		},
		Audio:   true,
		Agrees:  []string{"audio", "title"},
		Differs: []string{},
		Summary: "\"Candy from Strangers\" by Anetha — identified by its audio.",
	}
}

// A set-aside identification is silence in the recorded acoustic verdict. The
// wanted row agrees, and other audio disagrees.
func TestAcousticAgreementRecordsOnlyTheWantedRow(t *testing.T) {
	tests := []struct {
		name         string
		verification identity.Verification
		want         *bool
		identified   uuid.UUID
	}{
		{name: "set aside sibling", identified: uuid.New(), verification: func() identity.Verification {
			verification := provenByAudio()
			verification.AudioSetAside = true
			return verification
		}()},
		{name: "wanted recording", identified: uuid.MustParse(wantedRecording), verification: provenByAudio(), want: boolPointer(true)},
		{name: "other audio", identified: uuid.New(), verification: identity.Verification{
			AudioSaysOtherwise: true, Summary: "other audio", Agrees: []string{}, Differs: []string{"audio"},
		}, want: boolPointer(false)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, inbox, libraryPath, requestID := fetchedCandidate(t, 3)
			importer := candidateImporter(store, inbox, libraryPath, &fakeIdentifier{
				configured: true, clusters: []acoustid.Cluster{cluster(0.94, test.identified)},
			}, &fakeVerifier{verification: test.verification})
			if err := importer.Import(context.Background(), requestID); err != nil {
				t.Fatal(err)
			}
			var evidence *db.AcquiredFileEvidence
			if store.accepted != nil {
				evidence = store.accepted.Evidence
			} else if store.settled != nil {
				evidence = store.settled.Evidence
			}
			if evidence == nil || evidence.Acoustic == nil {
				t.Fatalf("evidence = %+v, want acoustic evidence", evidence)
			}
			if test.want == nil {
				if evidence.Acoustic.Agrees != nil {
					t.Errorf("acoustic agreement = %v, want silence", *evidence.Acoustic.Agrees)
				}
				return
			}
			if evidence.Acoustic.Agrees == nil || *evidence.Acoustic.Agrees != *test.want {
				t.Errorf("acoustic agreement = %v, want %v", evidence.Acoustic.Agrees, *test.want)
			}
		})
	}
}

func boolPointer(value bool) *bool { return &value }

// The audio proves it, so it goes in. This is the only thing that admits a file a
// stranger sent, and the verdict records what proved it.
func TestAProvenCandidateIsImported(t *testing.T) {
	store, inbox, libraryPath, requestID := fetchedCandidate(t, 3)
	identifier := &fakeIdentifier{
		configured: true,
		clusters:   []acoustid.Cluster{cluster(0.94, uuid.MustParse(wantedRecording))},
	}
	importer := candidateImporter(store, inbox, libraryPath, identifier, &fakeVerifier{
		verification: provenByAudio(),
	})

	if err := importer.Import(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}
	if store.accepted == nil || store.accepted.Verdict != db.AcquiredFileAccepted {
		t.Fatalf("verdict = %+v, want the file accepted", store.accepted)
	}
	if store.settled != nil {
		t.Errorf("settled = %+v, want an accepted file not to requeue the want", store.settled)
	}
	if store.accepted.Evidence == nil || store.accepted.Evidence.Method != "fingerprint" {
		t.Fatalf("evidence = %+v, want the method that proved it", store.accepted.Evidence)
	}
	if store.accepted.Evidence.Confidence != 1 {
		t.Errorf("confidence = %v", store.accepted.Evidence.Confidence)
	}
	if agrees := store.accepted.Evidence.Acoustic.Agrees; agrees == nil || !*agrees {
		t.Errorf("acoustic agreement = %v, want it recorded as having agreed", agrees)
	}
	local := store.localPaths[`Music\Anetha\03.flac`].LocalPath
	if _, err := os.Stat(local); err != nil {
		t.Fatalf("imported file = %q: %v", local, err)
	}
	// Named for the release rather than for the download. Every track of this
	// album renders the same folder, whenever each of them turns up, which is
	// what stops a track-at-a-time acquisition becoming a folder per track.
	if filepath.Base(filepath.Dir(local)) != "Don't Rush to Grow Up [44444444]" {
		t.Errorf("destination = %q, want the release the recording names", local)
	}
}

// The bug this filing exists to stop: two tracks of one album, wanted
// separately and arriving separately, are one album and go in one folder.
// Filing by the download gave each of them a folder of its own, and a library
// with a folder per file cannot tell a second copy of something from a first.
func TestTwoTracksOfOneReleaseAreFiledTogether(t *testing.T) {
	folders := make([]string, 0, 2)
	for index, track := range []string{"Candy from Strangers", "Nyx"} {
		store, inbox, libraryPath, requestID := fetchedCandidate(t, 3)
		// Two wants, two downloads, two recordings — everything about them
		// differs except the release, which is the only thing the folder is
		// named after.
		store.targetRow.RequestID = uuid.New()
		store.targetRow.AcquisitionTargetID = uuid.New()
		store.targetRow.EntryTitle = track
		requestID = store.targetRow.RequestID

		proven := provenByAudio()
		proven.Identity.TrackTitle = track
		importer := candidateImporter(
			store, inbox, libraryPath, nil, &fakeVerifier{verification: proven})
		if err := importer.Import(context.Background(), requestID); err != nil {
			t.Fatalf("track %d: %v", index, err)
		}
		local := store.localPaths[`Music\Anetha\03.flac`].LocalPath
		if local == "" {
			t.Fatalf("track %d was not imported: %+v", index, store.settled)
		}
		folders = append(folders, filepath.Base(filepath.Dir(local)))
	}
	if folders[0] != folders[1] {
		t.Fatalf("filed in %q and %q, want one folder for one release", folders[0], folders[1])
	}
}

// A file the tags fit and nobody listened to is not imported. A peer's copy
// carries no provenance, and consistently wrong tags on the wrong audio pass
// every check a tag can answer (ADR 0002). It is kept as a question instead.
func TestACandidateNobodyListenedToIsHeldRatherThanImported(t *testing.T) {
	store, inbox, libraryPath, requestID := fetchedCandidate(t, 3)
	provenByTags := provenByAudio()
	provenByTags.Audio = false
	provenByTags.Identity.Method = "title-artist-duration"
	importer := candidateImporter(store, inbox, libraryPath, nil, &fakeVerifier{
		verification: provenByTags,
	})

	if err := importer.Import(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}
	if store.accepted != nil {
		t.Fatalf("accepted = %+v, want tags alone to admit nothing", store.accepted)
	}
	if store.settled == nil || store.settled.Verdict != db.AcquiredFileHeld {
		t.Fatalf("verdict = %+v, want the copy held", store.settled)
	}
	if store.settled.ImportStatus != "needs_review" {
		t.Errorf("import status = %q, want the request to be waiting for a person",
			store.settled.ImportStatus)
	}
	if store.settled.NextAttemptAt.IsZero() {
		t.Error("a held copy must leave the want looking for a better one")
	}
}

// The audio was recognised and it is something else. That is the file saying what
// it is, and it ends this copy's chances for this want for good.
func TestACandidateTheAudioRefusesIsDiscarded(t *testing.T) {
	store, inbox, libraryPath, requestID := fetchedCandidate(t, 3)
	importer := candidateImporter(store, inbox, libraryPath,
		&fakeIdentifier{configured: true, clusters: []acoustid.Cluster{cluster(0, uuid.New())}},
		&fakeVerifier{verification: identity.Verification{
			AudioSaysOtherwise: true, Contradicted: true,
			Summary: "Its audio identifies another recording.",
			Agrees:  []string{}, Differs: []string{"audio (identified as another recording)"},
		}})

	if err := importer.Import(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}
	if store.accepted != nil {
		t.Fatalf("accepted = %+v, want nothing imported", store.accepted)
	}
	if store.settled == nil || store.settled.Verdict != db.AcquiredFileDiscardedAudio {
		t.Fatalf("verdict = %+v, want it discarded on the audio", store.settled)
	}
	if store.settled.ImportStatus != "discarded" || store.settled.Outcome != "rejected" {
		t.Errorf("settled = %+v, want a refusal rather than a question", store.settled)
	}
	if entries, err := os.ReadDir(libraryPath); err != nil || len(entries) != 0 {
		t.Errorf("library = %v (%v), want nothing copied", entries, err)
	}
}

// The audio named another MusicBrainz row and MusicBrainz could not be asked
// whether the two rows are one track entered twice. That answer is what the
// refusal rests on, so the copy is kept as a question (ADR 0031).
func TestACandidateWithAnUncheckedRegistrationIsHeld(t *testing.T) {
	store, inbox, libraryPath, requestID := fetchedCandidate(t, 3)
	importer := candidateImporter(store, inbox, libraryPath,
		&fakeIdentifier{configured: true, clusters: []acoustid.Cluster{cluster(0, uuid.New())}},
		&fakeVerifier{verification: identity.Verification{
			AudioSaysOtherwise: true, Contradicted: true, SameRegistrationUnchecked: true,
			Summary: "MusicBrainz could not be asked about the row AcoustID named.",
			Agrees:  []string{}, Differs: []string{"audio (identified as another recording)"},
		}})

	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	importer = importer.WithClock(func() time.Time { return now })
	if err := importer.Import(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}
	if store.settled == nil || store.settled.Verdict != db.AcquiredFileHeld {
		t.Fatalf("verdict = %+v, want the copy held", store.settled)
	}
	if len(store.judgingQueued) != 1 {
		t.Fatalf("recheck asks = %d, want the one this hold makes", len(store.judgingQueued))
	}
	if got := store.judgingQueued[0].runAfter; !got.Equal(now) {
		t.Fatalf("asked at %s, want the importer's own clock %s", got, now)
	}
	if store.recheckSpentSummary == "" {
		t.Error("the store was given no sentence for a want that runs out of patience")
	}
}

// The provider has had every chance the schedule allows. Nothing is queued, the
// copy is still the question it was, and the want is one for a person now.
func TestACandidateHeldOnAnUnaskableRegistrationStopsAskingEventually(t *testing.T) {
	store, inbox, libraryPath, requestID := fetchedCandidate(t, 3)
	store.recheckExhausted = true
	importer := candidateImporter(store, inbox, libraryPath,
		&fakeIdentifier{configured: true, clusters: []acoustid.Cluster{cluster(0, uuid.New())}},
		&fakeVerifier{verification: identity.Verification{
			AudioSaysOtherwise: true, Contradicted: true, SameRegistrationUnchecked: true,
			Summary: "MusicBrainz could not be asked about the row AcoustID named.",
			Agrees:  []string{}, Differs: []string{"audio (identified as another recording)"},
		}})

	if err := importer.Import(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}
	if store.settled == nil || store.settled.Verdict != db.AcquiredFileHeld {
		t.Fatalf("verdict = %+v, want the copy kept as the question it was", store.settled)
	}
	if len(store.judgingQueued) != 0 {
		t.Fatalf("recheck asks = %d, want none once the schedule is spent",
			len(store.judgingQueued))
	}
}

// MusicBrainz recovering is what usually ends the wait. The copy goes back
// through the grader, the provider answers, the copy is refused on its audio,
// and the want is asked to stop waiting on a provider it is no longer waiting
// for. Before this the reason, the due time and the spent attempts stayed on the
// row for ever.
func TestAnAnsweredRegistrationQuestionEndsTheProviderWait(t *testing.T) {
	store, inbox, libraryPath, requestID := fetchedCandidate(t, 3)
	importer := candidateImporter(store, inbox, libraryPath,
		&fakeIdentifier{configured: true, clusters: []acoustid.Cluster{cluster(0, uuid.New())}},
		&fakeVerifier{verification: identity.Verification{
			AudioSaysOtherwise: true, Summary: "The audio is a different recording.",
			Agrees: []string{}, Differs: []string{"audio (identified as another recording)"},
		}})

	if err := importer.Import(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}
	if store.settled == nil || store.settled.Verdict != db.AcquiredFileDiscardedAudio {
		t.Fatalf("verdict = %+v, want the copy refused on its audio", store.settled)
	}
	if len(store.waitEnded) != 1 {
		t.Fatalf("wants asked to stop waiting = %d, want the one this copy answers",
			len(store.waitEnded))
	}
}

// The copy that is still stopped on the question asks too, and the store reads
// the state rather than this verdict, so the no-op is where it belongs.
func TestAStillUnaskableCopyAsksToEndTheWaitAndKeepsIt(t *testing.T) {
	store, inbox, libraryPath, requestID := fetchedCandidate(t, 3)
	importer := candidateImporter(store, inbox, libraryPath,
		&fakeIdentifier{configured: true, clusters: []acoustid.Cluster{cluster(0, uuid.New())}},
		&fakeVerifier{verification: identity.Verification{
			AudioSaysOtherwise: true, Contradicted: true, SameRegistrationUnchecked: true,
			Summary: "MusicBrainz could not be asked about the row AcoustID named.",
			Agrees:  []string{}, Differs: []string{"audio (identified as another recording)"},
		}})

	if err := importer.Import(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}
	if len(store.waitEnded) != 1 {
		t.Fatalf("wants asked = %d, want the state asked about whatever the verdict",
			len(store.waitEnded))
	}
	if len(store.judgingQueued) != 1 {
		t.Fatalf("recheck asks = %d, want the copy still stopped on the question to schedule one",
			len(store.judgingQueued))
	}
}

// A refusal is about what Schall will use, never about what somebody else may
// keep. The peer's copy is left exactly where it was.
func TestARefusedCandidateIsNeverDeleted(t *testing.T) {
	store, inbox, libraryPath, requestID := fetchedCandidate(t, 3)
	store.retention = db.SourceRetentionDelete
	importer := candidateImporter(store, inbox, libraryPath, nil, &fakeVerifier{
		verification: identity.Verification{
			Contradicted: true, Summary: "Its duration disagrees.",
			Agrees: []string{}, Differs: []string{"duration (4 minutes out)"},
		}})

	if err := importer.Import(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}
	if store.settled == nil || store.settled.Verdict != db.AcquiredFileDiscardedTags {
		t.Fatalf("verdict = %+v, want it discarded on its own tags", store.settled)
	}
	if _, err := os.Stat(filepath.Join(inbox, "Anetha", "03.flac")); err != nil {
		t.Fatalf("the provider copy was disturbed: %v", err)
	}
	if store.retentionSet {
		t.Error("retention applies to an import, and nothing was imported")
	}
}

// A WAV with no tag chunk is readable audio that says nothing about itself. On
// this path tags admit nothing anyway, so absent tags are silence rather than
// damage: the audio is listened to, and an identification of exactly the wanted
// recording admits the copy as it would any other.
func TestAnUntaggedCandidateIsProvenByItsAudio(t *testing.T) {
	store, inbox, libraryPath, requestID := fetchedCandidate(t, 3)
	identifier := &fakeIdentifier{
		configured: true,
		clusters:   []acoustid.Cluster{cluster(0.99, uuid.MustParse(wantedRecording))},
	}
	verifier := &fakeVerifier{verification: provenByAudio()}
	importer := candidateImporter(store, inbox, libraryPath, identifier, verifier)
	importer.inspect = func(string) library.AudioMetadata {
		return library.AudioMetadata{Error: "no tags found", TagsAbsent: true}
	}

	if err := importer.Import(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}
	if len(verifier.asked) != 1 {
		t.Fatal("an untagged file's audio was never listened to")
	}
	if store.accepted == nil || store.accepted.Verdict != db.AcquiredFileAccepted {
		t.Fatalf("verdict = %+v, want the audio to admit it", store.accepted)
	}
}

// An untagged copy nobody could identify is a question, not a transport
// failure. Held, it reaches a person with its evidence; recorded as
// undelivered it was re-fetched from the same peer forever, failing
// identically every time.
func TestAnUntaggedCandidateNobodyIdentifiedIsHeld(t *testing.T) {
	store, inbox, libraryPath, requestID := fetchedCandidate(t, 3)
	identifier := &fakeIdentifier{configured: true}
	importer := candidateImporter(store, inbox, libraryPath, identifier, &fakeVerifier{
		verification: identity.Verification{
			Summary: "Nothing decided it.", Agrees: []string{}, Differs: []string{},
		}})
	importer.inspect = func(string) library.AudioMetadata {
		return library.AudioMetadata{Error: "no tags found", TagsAbsent: true}
	}

	if err := importer.Import(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}
	if store.accepted != nil {
		t.Fatalf("accepted = %+v, want nothing admitted without a proof", store.accepted)
	}
	if store.settled == nil || store.settled.Verdict != db.AcquiredFileHeld {
		t.Fatalf("verdict = %+v, want the copy held for a person", store.settled)
	}
}

// A file the parser cannot open at all is still nothing to judge. Only absent
// tags are silence; unreadable bytes stay a transport fact.
func TestAnUnreadableCandidateStaysUndelivered(t *testing.T) {
	store, inbox, libraryPath, requestID := fetchedCandidate(t, 3)
	verifier := &fakeVerifier{verification: provenByAudio()}
	importer := candidateImporter(store, inbox, libraryPath,
		&fakeIdentifier{configured: true}, verifier)
	importer.inspect = func(string) library.AudioMetadata {
		return library.AudioMetadata{Error: "unexpected EOF"}
	}

	if err := importer.Import(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}
	if store.settled == nil || store.settled.Verdict != db.AcquiredFileUndelivered {
		t.Fatalf("verdict = %+v, want unreadable bytes recorded as undelivered", store.settled)
	}
	if len(verifier.asked) != 0 {
		t.Error("a file that could not be read has nothing to verify")
	}
}

// Bytes that did not arrive as offered say nothing about whether this peer holds
// the right music. It is a fact about the transfer, so the offer may be tried
// again another day rather than being struck off.
func TestATruncatedCandidateIsNotAJudgementAboutTheMusic(t *testing.T) {
	store, inbox, libraryPath, requestID := fetchedCandidate(t, 9_000_000)
	verifier := &fakeVerifier{verification: provenByAudio()}
	importer := candidateImporter(store, inbox, libraryPath,
		&fakeIdentifier{configured: true}, verifier)

	if err := importer.Import(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}
	if store.settled == nil || store.settled.Verdict != db.AcquiredFileUndelivered {
		t.Fatalf("verdict = %+v, want the transport fact recorded", store.settled)
	}
	if len(verifier.asked) != 0 {
		t.Error("a file that did not arrive whole has nothing to verify")
	}
	if store.settled.Evidence == nil || len(store.settled.Evidence.Problems) == 0 {
		t.Fatalf("evidence = %+v, want it to say what was wrong with the bytes",
			store.settled.Evidence)
	}
}

// Verification is asked about the file, so what the file says about itself is
// what travels — including what the audio turned out to be, which is the one
// thing the file did not write.
func TestTheCandidatesOwnEvidenceIsWhatIsVerified(t *testing.T) {
	store, inbox, libraryPath, requestID := fetchedCandidate(t, 3)
	verifier := &fakeVerifier{verification: provenByAudio()}
	identified := uuid.MustParse(wantedRecording)
	importer := candidateImporter(store, inbox, libraryPath, &fakeIdentifier{
		configured: true, clusters: []acoustid.Cluster{cluster(0.9, identified)},
	}, verifier)

	if err := importer.Import(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}
	if len(verifier.asked) != 1 {
		t.Fatalf("asked %d times, want once", len(verifier.asked))
	}
	asked := verifier.asked[0]
	if asked.Title != "Candy from Strangers" || asked.Artist != "Anetha" {
		t.Errorf("evidence = %+v, want the file's own tags", asked)
	}
	if asked.Acoustic == nil || len(asked.Acoustic.Clusters) != 1 ||
		len(asked.Acoustic.Clusters[0].Recordings) != 1 ||
		asked.Acoustic.Clusters[0].Recordings[0] != identified {
		t.Errorf("acoustic evidence = %+v, want what the audio was identified as", asked.Acoustic)
	}
	if verifier.recordings[0] != uuid.MustParse(wantedRecording) {
		t.Errorf("checked against %s, want the recording the want names", verifier.recordings[0])
	}
}

// A check that did not run is recorded as not having run. Silence must never
// leave a trace suggesting the audio was heard and agreed.
func TestAnAbsentAcousticCheckSaysSoInTheEvidence(t *testing.T) {
	store, inbox, libraryPath, requestID := fetchedCandidate(t, 3)
	importer := candidateImporter(store, inbox, libraryPath, nil, &fakeVerifier{
		verification: identity.Verification{
			Summary: "Nothing identifies it.", Agrees: []string{}, Differs: []string{},
		}})

	if err := importer.Import(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}
	acoustic := store.settled.Evidence.Acoustic
	if acoustic == nil || acoustic.Unavailable == "" {
		t.Fatalf("acoustic = %+v, want it to say the check did not run", acoustic)
	}
	if acoustic.Agrees != nil {
		t.Error("a check that did not run cannot have agreed")
	}
}

// A held copy is the one somebody will read, so it keeps the recording it was
// measured against and not only the two words the entry carried. Without this
// the release, the length and the ISRC read as questions the check never asked.
func TestAHeldCopyRecordsTheRecordingItWasCheckedAgainst(t *testing.T) {
	store, inbox, libraryPath, requestID := fetchedCandidate(t, 3)
	durationMS := 487_278
	importer := candidateImporter(store, inbox, libraryPath, nil, &fakeVerifier{
		verification: identity.Verification{
			Summary: "Nothing identifies it.", Agrees: []string{}, Differs: []string{},
			Recording: &identity.Candidate{
				RecordingID: uuid.MustParse(wantedRecording), ArtistName: "Anetha",
				ReleaseTitle: "Don't Rush to Grow Up", TrackTitle: "Candy from Strangers",
				DurationMS: &durationMS, ISRC: "FRZ123456789",
			},
		}})

	if err := importer.Import(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}
	wanted := store.settled.Evidence.Wanted
	if wanted == nil {
		t.Fatal("a held copy says nothing about what it was supposed to be")
	}
	if wanted.Album != "Don't Rush to Grow Up" || wanted.ISRC != "FRZ123456789" ||
		wanted.DurationMS != durationMS {
		t.Errorf("wanted = %+v, want the recording's own release, ISRC and length", wanted)
	}
}

// A recording the provider described in no more detail than the entry did leaves
// the entry's words standing. An empty expected value would read as the file
// disagreeing with a recording that has no name.
func TestAHeldCopyKeepsTheEntrysWordsWhenTheRecordingHasNone(t *testing.T) {
	store, inbox, libraryPath, requestID := fetchedCandidate(t, 3)
	importer := candidateImporter(store, inbox, libraryPath, nil, &fakeVerifier{
		verification: identity.Verification{
			Summary: "Nothing identifies it.", Agrees: []string{}, Differs: []string{},
			Recording: &identity.Candidate{RecordingID: uuid.MustParse(wantedRecording)},
		}})

	if err := importer.Import(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}
	wanted := store.settled.Evidence.Wanted
	if wanted.Artist != "Anetha" || wanted.Title != "Candy from Strangers" {
		t.Errorf("wanted = %+v, want the entry's artist and title kept", wanted)
	}
}

// An installation with nothing to verify with fetched a file it cannot judge.
// That is a configuration failure, so it is retried and never written down as a
// verdict about the music.
func TestACandidateIsNeverJudgedWithoutAVerifier(t *testing.T) {
	store, inbox, libraryPath, requestID := fetchedCandidate(t, 3)
	importer := NewImporter(store, inbox, libraryPath, zerolog.Nop())

	err := importer.Import(context.Background(), requestID)
	if !errors.Is(err, ErrNoVerifier) {
		t.Fatalf("error = %v, want %v", err, ErrNoVerifier)
	}
	if store.settled != nil || store.accepted != nil {
		t.Error("nothing may be recorded about a file nobody could check")
	}
}

// A copy somebody accepted by hand is imported without being graded again.
// Grading it could only reach the same "nothing could decide" that sent it to
// them, and acting on that would overturn their answer with a shrug.
func TestACopyAcceptedByHandIsImportedWithoutBeingJudgedAgain(t *testing.T) {
	store, inbox, libraryPath, requestID := fetchedCandidate(t, 3)
	store.acceptedByHand = true
	// No verifier and no identifier: neither is consulted, and a nil verifier
	// would panic if anything tried.
	importer := candidateImporter(store, inbox, libraryPath, nil, nil)

	if err := importer.Import(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}
	if store.accepted == nil || store.accepted.Verdict != db.AcquiredFileAccepted {
		t.Fatalf("verdict = %+v, want the copy imported on the person's say-so", store.accepted)
	}
	if store.settled != nil {
		t.Errorf("settled = %+v, want an accepted copy not to requeue the want", store.settled)
	}
	if store.accepted.Evidence == nil || store.accepted.Evidence.Method != "manual" {
		t.Fatalf("evidence = %+v, want it to say a person decided rather than claim a proof",
			store.accepted.Evidence)
	}
	local := store.localPaths[`Music\Anetha\03.flac`].LocalPath
	if _, err := os.Stat(local); err != nil {
		t.Fatalf("imported file = %q: %v", local, err)
	}
}

// The bytes still have to be there. A person deciding about a copy says which
// recording it is, not that a truncated or missing file may be imported.
func TestACopyAcceptedByHandIsStillNotImportedIfTheBytesAreNotThere(t *testing.T) {
	store, inbox, libraryPath, requestID := fetchedCandidate(t, 3)
	store.acceptedByHand = true
	if err := os.RemoveAll(filepath.Join(inbox, "Anetha")); err != nil {
		t.Fatal(err)
	}
	importer := candidateImporter(store, inbox, libraryPath, nil, nil)

	if err := importer.Import(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}
	if store.accepted != nil {
		t.Fatalf("accepted = %+v, want nothing imported when nothing arrived", store.accepted)
	}
	if store.settled == nil || store.settled.Verdict != db.AcquiredFileUndelivered {
		t.Fatalf("verdict = %+v, want the transfer recorded as undelivered", store.settled)
	}
}

// The likeness AcoustID reported reaches the grader with the names it reported.
// Dropping it there is what let a single weak cluster conclude, because the
// grader then saw one recording and nothing to say how sure anybody was.
func TestTheLikenessTravelsToTheGraderWithTheNames(t *testing.T) {
	recordingID := uuid.New()
	evidence := &db.AcquiredFileEvidence{Acoustic: &db.ImportAcoustic{
		RecordingIDs: []string{recordingID.String()},
		Clusters: []db.ImportAcousticCluster{
			{Score: 0.62, RecordingIDs: []string{recordingID.String()}},
		},
		Score: 0.62,
	}}

	graded := candidateEvidence(inspected{}, evidence, 0, uuid.Nil, "")

	if graded.Acoustic == nil || len(graded.Acoustic.Clusters) != 1 ||
		graded.Acoustic.Clusters[0].Similarity != 0.62 {
		t.Fatalf("acoustic = %+v, want the likeness carried with the names", graded.Acoustic)
	}
}

// A recording ID the file carries is one of the two things that can turn an
// identification into two independent witnesses, so it has to reach the grader.
func TestTheCandidatesOwnRecordingIDTravelsToTheGrader(t *testing.T) {
	recordingID := uuid.MustParse(wantedRecording)
	file := inspected{tags: &db.ImportTags{MusicBrainzRecordingID: recordingID.String()}}

	graded := candidateEvidence(file, &db.AcquiredFileEvidence{}, 0, uuid.Nil, "")

	if graded.RecordingID != recordingID {
		t.Fatalf("recording = %s, want the one the file claims", graded.RecordingID)
	}
}

// A check that could not run says why and refuses nothing. Recording it as
// silence would leave a copy looking as though nobody had tried to listen.
func TestAnAcousticCheckThatFailedSaysWhyOnTheCopy(t *testing.T) {
	store, inbox, libraryPath, requestID := fetchedCandidate(t, 3)
	importer := candidateImporter(store, inbox, libraryPath, &fakeIdentifier{
		configured: true, err: errors.New("AcoustID is unreachable"),
	}, &fakeVerifier{verification: identity.Verification{
		Summary: "Nothing identifies it.", Agrees: []string{}, Differs: []string{},
	}})

	if err := importer.Import(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}
	acoustic := store.settled.Evidence.Acoustic
	if acoustic == nil || acoustic.Unavailable != "AcoustID is unreachable" {
		t.Fatalf("acoustic = %+v, want the reason the check did not run", acoustic)
	}
	if store.settled.Verdict != db.AcquiredFileHeld {
		t.Errorf("verdict = %q, want a copy nothing could decide held", store.settled.Verdict)
	}
}

// The clusters are what the grader decides on, so they are what travels. A flat
// list read as one cluster would say AcoustID had called a resemblance and the
// real thing one piece of audio, which is the whole distinction being kept.
func TestEvidenceWithNoClustersIsSilenceRatherThanOneClusterOfEverything(t *testing.T) {
	first, second := uuid.New(), uuid.New()
	evidence := &db.AcquiredFileEvidence{Acoustic: &db.ImportAcoustic{
		RecordingIDs: []string{first.String(), second.String()}, Score: 0.99,
	}}

	graded := candidateEvidence(inspected{}, evidence, 0, uuid.Nil, "")

	if graded.Acoustic == nil || len(graded.Acoustic.Clusters) != 0 {
		t.Fatalf("acoustic = %+v, want a flat list to identify nothing", graded.Acoustic)
	}
}

// What was heard is written down twice over: the clusters that decide it, and
// the flat list the review page has always read. A copy held before clusters
// were recorded still has to be readable.
func TestWhatWasHeardIsRecordedAsClustersAndAsTheFlatAnswer(t *testing.T) {
	store, inbox, libraryPath, requestID := fetchedCandidate(t, 3)
	first, second := uuid.New(), uuid.New()
	importer := candidateImporter(store, inbox, libraryPath, &fakeIdentifier{
		configured: true,
		clusters:   []acoustid.Cluster{cluster(0.99, first), cluster(0.4, second)},
	}, &fakeVerifier{verification: identity.Verification{
		Summary: "Nothing identifies it.", Agrees: []string{}, Differs: []string{},
	}})

	if err := importer.Import(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}
	acoustic := store.settled.Evidence.Acoustic
	if len(acoustic.Clusters) != 2 || acoustic.Clusters[0].Score != 0.99 ||
		acoustic.Clusters[1].RecordingIDs[0] != second.String() {
		t.Fatalf("clusters = %+v, want AcoustID's own boundaries kept", acoustic.Clusters)
	}
	if len(acoustic.RecordingIDs) != 2 || acoustic.RecordingIDs[0] != first.String() {
		t.Fatalf("recordingIds = %v, want the whole answer best first", acoustic.RecordingIDs)
	}
	if acoustic.Score != 0.99 {
		t.Errorf("score = %v, want the leading cluster's", acoustic.Score)
	}
}

// The length a peer's copy is compared on is the length fpcalc measured while
// decoding it, not the one written into its tags. On this path every tag is a
// stranger's, and the duration was the only one of them the decode could check.
func TestAFetchedCopyIsComparedOnTheLengthItsAudioTurnedOutToBe(t *testing.T) {
	file := inspected{tags: &db.ImportTags{Title: "卒業ですね", DurationMS: 252_000}}

	graded := candidateEvidence(file, &db.AcquiredFileEvidence{}, 267_000, uuid.Nil, "")

	if graded.DurationMS != 267_000 {
		t.Fatalf("duration = %d, want the decoded length rather than the tag's", graded.DurationMS)
	}
}

// Nothing decoded this copy, so the claim is all there is and it stands. The
// measurement is preferred where it exists; its absence invents nothing.
func TestACopyNobodyDecodedIsStillComparedOnWhatItClaims(t *testing.T) {
	file := inspected{tags: &db.ImportTags{Title: "卒業ですね", DurationMS: 252_000}}

	graded := candidateEvidence(file, &db.AcquiredFileEvidence{}, 0, uuid.Nil, "")

	if graded.DurationMS != 252_000 {
		t.Fatalf("duration = %d, want the file's own claim where nothing measured it",
			graded.DurationMS)
	}
}

// A recording the provider cannot describe, or a provider nobody can reach, is
// a statement about the want rather than about this file. It is retried, and
// nothing is written down about the music.
func TestACandidateIsRetriedWhenTheVerifierCannotAnswer(t *testing.T) {
	store, inbox, libraryPath, requestID := fetchedCandidate(t, 3)
	importer := candidateImporter(store, inbox, libraryPath, nil, &fakeVerifier{
		err: errors.New("MusicBrainz is unreachable"),
	})

	err := importer.Import(context.Background(), requestID)
	if err == nil || !strings.Contains(err.Error(), "MusicBrainz is unreachable") {
		t.Fatalf("error = %v, want the failure to reach the grader reported", err)
	}
	if store.settled != nil || store.accepted != nil {
		t.Fatal("a copy nobody could check was written down as a verdict")
	}
}

// MusicBrainz deleting or merging away the recording a want names is a
// statement about the want, not about this file. Retrying would only fetch
// another copy that fails at the same step, so the copy is discarded and the
// want is sent back to resolution instead of into the retry ladder.
func TestACandidateIsSentBackToResolutionWhenItsRecordingIsGone(t *testing.T) {
	store, inbox, libraryPath, requestID := fetchedCandidate(t, 3)
	importer := candidateImporter(store, inbox, libraryPath, nil, &fakeVerifier{
		err: identity.ErrRecordingUnknown,
	})
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	importer = importer.WithClock(func() time.Time { return now })

	if err := importer.Import(context.Background(), requestID); err != nil {
		t.Fatalf("error = %v, want a gone recording not to be retried", err)
	}
	if store.settled != nil || store.accepted != nil {
		t.Fatalf("settled = %+v, accepted = %+v, want neither the ordinary settle nor an import",
			store.settled, store.accepted)
	}
	gone := store.settledGoneRecording
	if gone == nil {
		t.Fatal("nothing was recorded about the want losing its recording")
	}
	if gone.Verdict != db.AcquiredFileUndelivered {
		t.Errorf("verdict = %q, want the copy left retriable rather than struck off for good",
			gone.Verdict)
	}
	if gone.RecordingID != uuid.MustParse(wantedRecording) {
		t.Errorf("recording = %s, want the one the want was claimed against", gone.RecordingID)
	}
	if gone.Summary == "" || gone.ManualSummary == "" {
		t.Error("the want needs a summary for both a resolved entry and a manually resolved one")
	}
	if !gone.NextAttemptAt.Equal(now) {
		t.Errorf("next attempt = %s, want it due now for the resolver", gone.NextAttemptAt)
	}
}

// A copy that could not be claimed is one another worker may already be
// judging, so nothing is read and nothing is recorded.
func TestNothingIsJudgedForACopyThatCannotBeClaimed(t *testing.T) {
	store, inbox, libraryPath, requestID := fetchedCandidate(t, 3)
	store.targetClaimErr = errors.New("the requests table is unreachable")
	importer := candidateImporter(store, inbox, libraryPath, nil, &fakeVerifier{
		verification: provenByAudio(),
	})

	err := importer.Import(context.Background(), requestID)
	if !errors.Is(err, store.targetClaimErr) {
		t.Fatalf("error = %v, want the claim failure reported", err)
	}
	if store.settled != nil || store.accepted != nil {
		t.Fatal("a copy nobody claimed was written down as a verdict")
	}
}

// A folder name that is nothing at all is nowhere to look for the bytes, so the
// copy is recorded as never having arrived rather than judged.
func TestACandidateInAFolderWithNoSafeNameIsUndelivered(t *testing.T) {
	store, inbox, libraryPath, requestID := fetchedCandidate(t, 3)
	store.targetRow.SourceDirectory = "  "
	verifier := &fakeVerifier{verification: provenByAudio()}
	importer := candidateImporter(store, inbox, libraryPath, nil, verifier)

	if err := importer.Import(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}
	if store.settled == nil || store.settled.Verdict != db.AcquiredFileUndelivered {
		t.Fatalf("verdict = %+v, want the copy recorded as never delivered", store.settled)
	}
	if len(verifier.asked) != 0 {
		t.Error("a copy nobody could find has nothing to verify")
	}
}

// A name the filesystem cannot even look up is a failure of this installation,
// not a fact about the peer's copy, so it is retried rather than recorded.
func TestACandidateNamedTooLongToLookUpFailsRatherThanSettles(t *testing.T) {
	store, inbox, libraryPath, requestID := fetchedCandidate(t, 3)
	store.targetRow.File.Name = strings.Repeat("n", 300) + ".flac"
	importer := candidateImporter(store, inbox, libraryPath, nil,
		&fakeVerifier{verification: provenByAudio()})

	err := importer.Import(context.Background(), requestID)
	if err == nil || !strings.Contains(err.Error(), "inspect downloaded file") {
		t.Fatalf("error = %v, want the lookup failure reported", err)
	}
	if store.settled != nil {
		t.Fatalf("settled = %+v, want nothing recorded about the music", store.settled)
	}
}

// A proven copy that could not be copied into the library is not a verdict. The
// want stays as it was and the attempt is retried.
func TestAProvenCandidateIsNotRecordedWhenItCannotBeCopied(t *testing.T) {
	store, inbox, libraryPath, requestID := fetchedCandidate(t, 3)
	if err := os.WriteFile(filepath.Join(libraryPath, "Anetha"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	importer := candidateImporter(store, inbox, libraryPath, nil, &fakeVerifier{
		verification: provenByAudio(),
	})

	if err := importer.Import(context.Background(), requestID); err == nil {
		t.Fatal("an import into a blocked library path reported success")
	}
	if store.accepted != nil || store.settled != nil {
		t.Fatal("a copy that was never saved was recorded as imported")
	}
}

// The same for a copy somebody accepted by hand: their decision stands, and it
// is honoured on the next attempt rather than written off here.
func TestACopyAcceptedByHandIsNotRecordedWhenItCannotBeCopied(t *testing.T) {
	store, inbox, libraryPath, requestID := fetchedCandidate(t, 3)
	store.acceptedByHand = true
	if err := os.WriteFile(filepath.Join(libraryPath, "Anetha"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	importer := candidateImporter(store, inbox, libraryPath, nil, nil)

	if err := importer.Import(context.Background(), requestID); err == nil {
		t.Fatal("an import into a blocked library path reported success")
	}
	if store.accepted != nil {
		t.Fatal("a copy that was never saved was recorded as imported")
	}
}

// The verdict and the import are one transaction, so a commit that fails leaves
// neither: the copy is not the want's answer until the row says so.
func TestAProvenCandidateIsNotAcceptedWhenTheVerdictCannotBeCommitted(t *testing.T) {
	store, inbox, libraryPath, requestID := fetchedCandidate(t, 3)
	store.completeErr = errors.New("the acquisitions table is unreachable")
	importer := candidateImporter(store, inbox, libraryPath, nil, &fakeVerifier{
		verification: provenByAudio(),
	})

	err := importer.Import(context.Background(), requestID)
	if !errors.Is(err, store.completeErr) {
		t.Fatalf("error = %v, want the commit failure reported", err)
	}
	if store.accepted != nil {
		t.Fatal("a verdict that never committed was recorded")
	}
}

func TestACopyAcceptedByHandIsNotImportedWhenTheVerdictCannotBeCommitted(t *testing.T) {
	store, inbox, libraryPath, requestID := fetchedCandidate(t, 3)
	store.acceptedByHand = true
	store.completeErr = errors.New("the acquisitions table is unreachable")
	importer := candidateImporter(store, inbox, libraryPath, nil, nil)

	err := importer.Import(context.Background(), requestID)
	if !errors.Is(err, store.completeErr) {
		t.Fatalf("error = %v, want the commit failure reported", err)
	}
	if store.accepted != nil {
		t.Fatal("a verdict that never committed was recorded")
	}
}

// A decision nobody could read is not permission to judge the copy again. The
// attempt is retried rather than a person's answer being overturned by a shrug.
func TestACopyIsNotJudgedWhenARecordedDecisionCannotBeRead(t *testing.T) {
	store, inbox, libraryPath, requestID := fetchedCandidate(t, 3)
	store.acceptedErr = errors.New("the acquisitions table is unreachable")
	importer := candidateImporter(store, inbox, libraryPath, nil, &fakeVerifier{
		verification: provenByAudio(),
	})

	err := importer.Import(context.Background(), requestID)
	if !errors.Is(err, store.acceptedErr) {
		t.Fatalf("error = %v, want the failure to read the decision reported", err)
	}
	if store.accepted != nil || store.settled != nil {
		t.Fatal("a copy was judged without knowing whether somebody already had")
	}
}

// A want whose entry says nothing and whose recording the provider could not
// name is still filed somewhere, under a name rather than an empty folder.
func TestAProvenCopyWithNoNamesAtAllIsFiledUnderAName(t *testing.T) {
	store, inbox, libraryPath, requestID := fetchedCandidate(t, 3)
	store.targetRow.EntryArtist, store.targetRow.EntryTitle = "", ""
	proven := provenByAudio()
	proven.Identity.ArtistName, proven.Identity.ReleaseTitle = "", ""
	importer := candidateImporter(store, inbox, libraryPath, nil, &fakeVerifier{verification: proven})

	if err := importer.Import(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}
	local := store.localPaths[`Music\Anetha\03.flac`].LocalPath
	if local == "" {
		t.Fatalf("nothing was imported: %+v", store.settled)
	}
	if filepath.Base(filepath.Dir(filepath.Dir(local))) != "Unknown" {
		t.Fatalf("imported to %q, want a named folder", local)
	}
}

// A reason too long to read is stored short rather than not at all: evidence
// that a check could not run is the whole point of writing it down.
func TestAReasonAnAcousticCheckGivesIsStoredAtAReadableLength(t *testing.T) {
	store, inbox, libraryPath, requestID := fetchedCandidate(t, 3)
	importer := candidateImporter(store, inbox, libraryPath, &fakeIdentifier{
		configured: true, err: errors.New(strings.Repeat("AcoustID said no. ", 40)),
	}, &fakeVerifier{verification: identity.Verification{
		Summary: "Nothing identifies it.", Agrees: []string{}, Differs: []string{},
	}})

	if err := importer.Import(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}
	unavailable := store.settled.Evidence.Acoustic.Unavailable
	if len(unavailable) != 200 {
		t.Fatalf("stored reason is %d characters, want it kept to 200", len(unavailable))
	}
}

// stubRecordings answers a lookup with recordings it was given, so the real
// grader can be put behind the importer. Only MusicBrainz is stood in for; every
// rule the copy is judged by is the one that ships.
type stubRecordings map[uuid.UUID]musicbrainz.Recording

func (stub stubRecordings) Recording(
	_ context.Context, id uuid.UUID,
) (musicbrainz.Recording, error) {
	recording, known := stub[id]
	if !known {
		return musicbrainz.Recording{}, musicbrainz.ErrNotFound
	}
	return recording, nil
}

// ResolveRecordingID follows a merge out of the same map: an ID the stub was
// given under one key and holds a recording with another is exactly what
// MusicBrainz answers after a merge.
func (stub stubRecordings) ResolveRecordingID(
	_ context.Context, id uuid.UUID,
) (uuid.UUID, error) {
	recording, known := stub[id]
	if !known {
		return uuid.Nil, musicbrainz.ErrNotFound
	}
	return recording.ID, nil
}

func (stub stubRecordings) SearchRecordings(
	context.Context, musicbrainz.RecordingQuery, int,
) ([]musicbrainz.Recording, error) {
	return nil, nil
}

// The case #147 was written for: a copy whose tags claim exactly the wanted
// length while the audio runs fourteen seconds longer. The claim used to be the
// number compared, so the pair agreed and the audio was left carrying it; the
// measurement contradicts, and a contradiction voids the pair whatever else
// agrees.
func TestATagClaimingTheRightLengthCannotRescueAudioOfAnother(t *testing.T) {
	store, inbox, libraryPath, requestID := fetchedCandidate(t, 3)
	wanted := 252_632
	importer := NewImporter(store, inbox, libraryPath, zerolog.Nop()).
		WithAcousticIdentifier(&fakeIdentifier{
			configured:      true,
			durationSeconds: 267,
			clusters: []acoustid.Cluster{
				cluster(0.99, uuid.MustParse(wantedRecording)),
			},
		}).
		WithVerifier(identity.NewResolver(stubRecordings{
			uuid.MustParse(wantedRecording): {
				ID: uuid.MustParse(wantedRecording), Title: "Get Your Wish (Sewerslvt remix)",
				ArtistCredit: "Anetha", DurationMS: &wanted,
			},
		}))
	importer.inspect = func(string) library.AudioMetadata {
		return library.AudioMetadata{
			Artist: "Anetha", Title: "Get Your Wish (Sewerslvt remix)", DurationMS: 252_000,
		}
	}

	if err := importer.Import(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}
	if store.accepted != nil {
		t.Fatalf("accepted = %+v, want the measured length to void the pair", store.accepted)
	}
	if store.settled == nil || store.settled.Verdict != db.AcquiredFileDiscardedTags {
		t.Fatalf("verdict = %+v, want a copy contradicted by its own length", store.settled)
	}
}

// The release group the catalogue holds an album for. It is what both import
// paths file that album's music under.
const catalogueReleaseGroup = "f3c445e4-1111-4111-8111-111111111111"

// wantOffACataloguedAlbum is one file fetched for a want whose release group the
// catalogue answers for. That is the ordinary case: a want is resolved through a
// release, and that release is an album row with one artist on it.
//
// albumArtist is the name on that album row. credit is what the recording itself
// is credited to, which is a different claim: it names the guests on this one
// track, and MusicBrainz spells it as the release printed it.
func wantOffACataloguedAlbum(
	t *testing.T, albumArtist, credit string,
) (*fakeImportStore, string, string, uuid.UUID, identity.Verification) {
	t.Helper()
	store, inbox, libraryPath, requestID := fetchedCandidate(t, 3)
	store.targetRow.MusicBrainzReleaseGroupID = uuid.NullUUID{
		UUID: uuid.MustParse(catalogueReleaseGroup), Valid: true,
	}
	store.targetRow.AlbumArtist = albumArtist
	store.targetRow.AlbumTitle = "WAVE"
	proven := provenByAudio()
	// The audio agrees that this is the release the want was resolved through,
	// which is the ordinary case: a want names a recording on that release, and
	// a copy of it is identified there.
	proven.Identity.ReleaseGroupID = uuid.MustParse(catalogueReleaseGroup)
	proven.Identity.ArtistName = credit
	proven.Identity.ReleaseTitle = "WAVE"
	return store, inbox, libraryPath, requestID, proven
}

// A guest on one track does not make a second album. "WAVE" is an album by
// Ufo361, and one of its tracks is credited "Ufo361 feat. KC Rebell". Filing by
// that credit gave the album one artist folder per guest — four folders called
// WAVE on the live collection, all carrying the same release in the name.
func TestAGuestCreditIsFiledUnderTheAlbumsOwnArtist(t *testing.T) {
	store, inbox, libraryPath, requestID, proven := wantOffACataloguedAlbum(
		t, "Ufo361", "Ufo361 feat. KC Rebell")
	importer := candidateImporter(
		store, inbox, libraryPath, nil, &fakeVerifier{verification: proven})

	if err := importer.Import(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}
	local := store.localPaths[`Music\Anetha\03.flac`].LocalPath
	if local == "" {
		t.Fatalf("the copy was not imported: %+v", store.settled)
	}
	want := filepath.Join("Ufo361", "WAVE [f3c445e4]")
	if !strings.HasSuffix(filepath.Dir(local), want) {
		t.Fatalf("filed at %q, want %q", local, want)
	}
}

// One artist, spelled two ways. A credit is written per recording, so
// MusicBrainz prints "RIN" on one release and "Rin" on another, while the album
// row holds one name. Filing by the credit split one artist's music into two
// folders that differ only in case — nine such pairs on the live collection.
func TestTheAlbumsSpellingIsTheFolderHoweverATrackIsCredited(t *testing.T) {
	store, inbox, libraryPath, requestID, proven := wantOffACataloguedAlbum(t, "RIN", "Rin")
	importer := candidateImporter(
		store, inbox, libraryPath, nil, &fakeVerifier{verification: proven})

	if err := importer.Import(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}
	local := store.localPaths[`Music\Anetha\03.flac`].LocalPath
	if local == "" {
		t.Fatalf("the copy was not imported: %+v", store.settled)
	}
	if artist := filepath.Base(filepath.Dir(filepath.Dir(local))); artist != "RIN" {
		t.Fatalf("filed under %q, want the album's own spelling \"RIN\"", artist)
	}
}

// The property both paths have to share, pinned as one test so that neither can
// drift from the other: a whole release chosen by hand and one track of it
// fetched for a want are the same album, and one album is one folder.
func TestBothImportPathsFileOneAlbumInOneFolder(t *testing.T) {
	inbox, libraryPath := t.TempDir(), t.TempDir()
	folder := filepath.Join(inbox, "Album")
	if err := os.Mkdir(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, "01.flac"), []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	requestID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	whole := &fakeImportStore{row: db.DownloadImportRow{
		RequestID: requestID, AlbumID: fixtureAlbum,
		ReleaseGroupID: uuid.NullUUID{
			UUID: uuid.MustParse(catalogueReleaseGroup), Valid: true,
		},
		ArtistName: "Ufo361", AlbumTitle: "WAVE",
		SourceDirectory: `Remote\Album`,
		Files: []db.DownloadRequestFile{
			{Path: `Music\Album\01.flac`, Name: "01.flac", SizeBytes: 3},
		},
		Tracks: []db.ImportTrack{
			{Title: "First", DiscNumber: 1, TrackNumber: 1, DurationMS: 100_000},
		},
	}}
	importer := NewImporter(whole, inbox, libraryPath, zerolog.Nop())
	importer.inspect = func(string) library.AudioMetadata {
		return library.AudioMetadata{
			Artist: "Ufo361", Album: "WAVE", Title: "First",
			DiscNumber: 1, TrackNumber: 1, DurationMS: 101_000,
		}
	}
	if err := importer.Import(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}

	// The same album again, one track of it fetched for a want and credited on
	// that track to a guest.
	single, singleInbox, singleLibrary, singleRequest, proven := wantOffACataloguedAlbum(
		t, "Ufo361", "Ufo361 feat. KC Rebell")
	singleImporter := candidateImporter(
		single, singleInbox, singleLibrary, nil, &fakeVerifier{verification: proven})
	if err := singleImporter.Import(context.Background(), singleRequest); err != nil {
		t.Fatal(err)
	}
	fetched := single.localPaths[`Music\Anetha\03.flac`].LocalPath
	if fetched == "" {
		t.Fatalf("the copy was not imported: %+v", single.settled)
	}

	// Each path ran against a temporary library of its own, so what is compared
	// is the folder each built under its own root.
	byRelease := strings.TrimPrefix(whole.destination, libraryPath)
	byWant := strings.TrimPrefix(filepath.Dir(fetched), singleLibrary)
	if byRelease != byWant {
		t.Fatalf("a whole release filed at %q and one of its tracks at %q, want one folder",
			byRelease, byWant)
	}
}

// A copy the audio placed on another release is filed as that release, not as
// the one the want was resolved through. A want asks for a recording through the
// release Schall resolved it on; the file that arrives may be a copy from a
// different release that carries the same recording, and the audio is what
// knows. The catalogue's album here describes a release this file is not on, so
// it does not name the folder — and the layout migration reads the release the
// same way round, so the two agree.
func TestACopyIdentifiedOnAnotherReleaseIsFiledAsThatRelease(t *testing.T) {
	store, inbox, libraryPath, requestID, proven := wantOffACataloguedAlbum(
		t, "Ufo361", "Ufo361 feat. KC Rebell")
	elsewhere := uuid.MustParse("bbbbbbbb-2222-4222-8222-222222222222")
	proven.Identity.ReleaseGroupID = elsewhere
	proven.Identity.ReleaseTitle = "Ich bin ein Berliner"
	importer := candidateImporter(
		store, inbox, libraryPath, nil, &fakeVerifier{verification: proven})

	if err := importer.Import(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}
	local := store.localPaths[`Music\Anetha\03.flac`].LocalPath
	if local == "" {
		t.Fatalf("the copy was not imported: %+v", store.settled)
	}
	want := filepath.Join("Ufo361 feat. KC Rebell", "Ich bin ein Berliner [bbbbbbbb]")
	if !strings.HasSuffix(filepath.Dir(local), want) {
		t.Fatalf("filed at %q, want the release the audio named, %q", local, want)
	}
}

// A copy a person accepted is filed in the album folder too. Nothing proved
// what it is, so nothing contradicts the release the want was resolved through,
// and the catalogue's album for that release is the best account of it there
// is. This path used to name the folder after the entry's artist and the
// entry's track title, which gave one album a folder per hand-accepted track.
func TestACopyAcceptedByHandLandsInTheAlbumFolder(t *testing.T) {
	store, inbox, libraryPath, requestID, _ := wantOffACataloguedAlbum(
		t, "Ufo361", "Ufo361 feat. KC Rebell")
	store.acceptedByHand = true
	// No verifier and no identifier: a decision already made is not judged
	// again, and a nil verifier would panic if anything tried.
	importer := candidateImporter(store, inbox, libraryPath, nil, nil)

	if err := importer.Import(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}
	local := store.localPaths[`Music\Anetha\03.flac`].LocalPath
	if local == "" {
		t.Fatalf("the copy was not imported: %+v", store.settled)
	}
	want := filepath.Join("Ufo361", "WAVE [f3c445e4]")
	if !strings.HasSuffix(filepath.Dir(local), want) {
		t.Fatalf("filed at %q, want %q", local, want)
	}
}
