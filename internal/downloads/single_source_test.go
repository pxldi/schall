package downloads

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/identity"
	"github.com/pxldi/schall/internal/library"
)

// A want MusicBrainz has no recording for is searched on its anchor, and its
// copies are judged on that anchor alone (ADR 0037). These tests are about what
// the importer does with such a copy; the rule is pinned in internal/identity.

// recordingless is one file fetched for an unresolved want anchored by source.
// The verifier would prove anything it is asked about, so a test that sees a
// copy admitted through it has found the recording path taken by mistake.
func recordingless(
	t *testing.T, source, reference, fingerprint string,
) (*fakeImportStore, *Importer, *fakeVerifier, uuid.UUID) {
	t.Helper()
	store, inbox, libraryPath, requestID := fetchedCandidate(t, 3)
	store.targetRow.MusicBrainzRecordingID = uuid.Nil
	store.targetRow.AnchorFingerprint = anchorFingerprint
	store.targetRow.AnchorSource = source
	store.targetRow.AnchorReference = reference
	store.targetRow.AnchorSeconds = 30
	verifier := &fakeVerifier{verification: provenByAudio()}
	importer := candidateImporter(store, inbox, libraryPath, nil, verifier).
		WithFingerprinter(&fakeFingerprinter{value: fingerprint, available: true})
	return store, importer, verifier, requestID
}

// The case the rule exists for: the tags say exactly what the entry says, and
// the audio is something else. Tags that agree never admit, and a Deezer
// preview the copy does not reproduce refuses it.
func TestACopyWhoseTagsAgreeButWhoseAudioIsNotTheDeezerPreviewIsNeverAdmitted(t *testing.T) {
	store, importer, verifier, requestID := recordingless(
		t, "deezer", "2178654", otherAudioFingerprint)

	if err := importer.ImportOne(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}

	if store.accepted != nil {
		t.Fatalf("accepted = %+v, want a copy of other audio kept out", store.accepted)
	}
	if len(verifier.asked) != 0 {
		t.Fatalf("asked the recording verifier %d times, want a want with no recording "+
			"judged on its anchor alone", len(verifier.asked))
	}
	if store.settled == nil || store.settled.Verdict != db.AcquiredFileDiscardedAudio {
		t.Fatalf("settled = %+v, want the copy refused on the preview", store.settled)
	}
	if got := store.settled.Evidence.Wanted; got == nil || got.MusicBrainzRecordingID != "" {
		t.Errorf("wanted = %+v, want the entry with no recording named", got)
	}
}

// A Topic upload is found by name, so other audio does not refuse the copy
// (ADR 0029 §3). It does not admit it either.
func TestACopyThatIsNotTheTopicUploadIsHeldForAWantWithNoRecording(t *testing.T) {
	store, importer, _, requestID := recordingless(
		t, "youtube-topic", "dQw4w9WgXcQ", otherAudioFingerprint)

	if err := importer.ImportOne(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}

	if store.accepted != nil {
		t.Fatalf("accepted = %+v, want a copy of other audio kept out", store.accepted)
	}
	if store.settled == nil || store.settled.Verdict != db.AcquiredFileHeld {
		t.Fatalf("settled = %+v, want the copy held", store.settled)
	}
}

// Same audio admits, and the copy is recorded as the track at the anchor's
// address: the key completing it writes as a source identity.
func TestACopyThatReproducesTheAnchorIsAdmittedAsTheAnchorsTrack(t *testing.T) {
	for _, test := range []struct {
		source, reference string
		want              db.ImportSource
	}{
		{"deezer", "2178654", db.ImportSource{
			Source: "deezer", ExternalID: "deezer:2178654",
			ExternalURL: "https://www.deezer.com/track/2178654",
		}},
		{"youtube-topic", "dQw4w9WgXcQ", db.ImportSource{
			Source: "youtube", ExternalID: "youtube:dQw4w9WgXcQ",
			ExternalURL: "https://www.youtube.com/watch?v=dQw4w9WgXcQ",
		}},
	} {
		t.Run(test.source, func(t *testing.T) {
			store, importer, verifier, requestID := recordingless(
				t, test.source, test.reference, anchorFingerprint)

			if err := importer.ImportOne(context.Background(), requestID); err != nil {
				t.Fatal(err)
			}

			if len(verifier.asked) != 0 {
				t.Fatal("asked the recording verifier about a want with no recording")
			}
			if store.accepted == nil || store.accepted.Verdict != db.AcquiredFileAccepted {
				t.Fatalf("accepted = %+v, settled = %+v, want the copy admitted",
					store.accepted, store.settled)
			}
			evidence := store.accepted.Evidence
			if evidence.Source == nil || *evidence.Source != test.want {
				t.Errorf("source = %+v, want %+v", evidence.Source, test.want)
			}
			if evidence.Method != identity.MethodPublishedSample || evidence.Confidence != 1 {
				t.Errorf("method = %q (%v), want the published sample", evidence.Method,
					evidence.Confidence)
			}
			if evidence.Anchor == nil || evidence.Anchor.Agrees == nil || !*evidence.Anchor.Agrees {
				t.Errorf("anchor = %+v, want the comparison recorded as agreeing", evidence.Anchor)
			}
		})
	}
}

// A contradiction holds a copy whatever the audio says (ADR 0037 §3).
func TestACopyWhoseTagsContradictTheEntryIsHeldWhateverItsAudio(t *testing.T) {
	store, importer, _, requestID := recordingless(t, "deezer", "2178654", anchorFingerprint)
	importer.inspect = func(string) library.AudioMetadata {
		return library.AudioMetadata{Artist: "Somebody Else", Title: "Candy from Strangers"}
	}

	if err := importer.ImportOne(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}

	if store.accepted != nil {
		t.Fatalf("accepted = %+v, want a contradicted copy held", store.accepted)
	}
	if store.settled == nil || store.settled.Verdict != db.AcquiredFileHeld {
		t.Fatalf("settled = %+v, want the copy held", store.settled)
	}
}

// A MusicBrainz recording ID on the copy has nothing to agree with, and it
// means MusicBrainz knows the file: resolution has not caught up yet.
func TestACopyCarryingARecordingIDIsHeldForAWantWithNoRecording(t *testing.T) {
	store, importer, _, requestID := recordingless(t, "deezer", "2178654", anchorFingerprint)
	tagged := uuid.New()
	importer.inspect = func(string) library.AudioMetadata {
		return library.AudioMetadata{
			Artist: "Anetha", Title: "Candy from Strangers", MusicBrainzRecordingID: &tagged,
		}
	}

	if err := importer.ImportOne(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}

	if store.accepted != nil {
		t.Fatalf("accepted = %+v, want the copy held", store.accepted)
	}
	if store.settled == nil || store.settled.Verdict != db.AcquiredFileHeld {
		t.Fatalf("settled = %+v, want the copy held", store.settled)
	}
	if store.settled.Summary[:len(unresolvedRecordingSummary)] != unresolvedRecordingSummary {
		t.Errorf("summary = %q, want it to say MusicBrainz knows the file", store.settled.Summary)
	}
}

// A person accepting a held copy writes the same key, as their decision
// (ADR 0037 §5).
func TestACopyAcceptedByHandForAWantWithNoRecordingCarriesTheAnchorsKey(t *testing.T) {
	store, importer, verifier, requestID := recordingless(
		t, "deezer", "2178654", otherAudioFingerprint)
	store.acceptedByHand = true

	if err := importer.ImportOne(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}

	if len(verifier.asked) != 0 {
		t.Fatal("asked the verifier about a copy a person already decided")
	}
	if store.accepted == nil {
		t.Fatalf("settled = %+v, want the person's copy imported", store.settled)
	}
	evidence := store.accepted.Evidence
	if evidence.Method != "manual" || evidence.Source == nil ||
		evidence.Source.ExternalID != "deezer:2178654" {
		t.Errorf("evidence = %+v, want a manual decision keyed by the anchor", evidence)
	}
}
