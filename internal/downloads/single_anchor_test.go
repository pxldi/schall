package downloads

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/chromaprint"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/identity"
)

// A want may carry an anchor: the short sample its recording's distributor
// publishes, fetched by the recording's own registration code and kept on the
// want. A copy a peer sent is measured against it, and these tests are about
// what the importer does with the measurement — the rule itself is pinned in
// internal/identity.
//
// Both fingerprints below are real. They were computed by fpcalc 1.6.1 over two
// pieces of generated audio, thirty seconds each and deliberately unalike, and
// they measure 0.47 apart: the range the calibration in docs/decisions/0024
// found for audio that is not the same audio.

const anchorFingerprint = "AQAA3dGkaEzEJPjyo1r0oEmaD2fQ5jd-aE_xoeGJPidyNai4o3lMnIbVIN7xaQukKieaqCwYdxfO4C" +
	"EPJle6wPqhiRGaHz10Im1U4_nxkaguo0kqT8jxxNB99Dg9NEceHQ-_ItSPps7xD91HFWLooMEXxuhZ" +
	"wRf0HE5WGqEuPDzOBz_ewN9E_MPxoxKD5GyO8FlONNdxwpt2ozf6BM3xI34gKraS4P-" +
	"QK8ZHw1OmhPjm4Avyo4mjEl10BeoSGz_8CLeCfRb-4EoYaKlyVBGq_-" +
	"hDEblT4cmJP2h_hPmx5_izgj9EpgfzCz6chsGzHT6PWh2LJwvMKNCVHM-VFU2ONjt-HNp7uGglnFdQ" +
	"kT4aQfp4kBxKVIvO4Az0w5uNL7hjiFeCB_G44KaC_ricPPCP8BdOnwJrpcero4lzDYrOHCGZXTgeXI" +
	"6D5uXwHb-CZFGaBKGEB3t0CX8Exj6ah8dNqThyHWoHX4d0PpAchUdzHk8PHxfRo6aCJk4CuaxwOit8" +
	"_Dhha8G_whfxEq-po4WoNYezKA_uU7gIqjr6SEazzuihC09aCc6FH3mT48sO3_h5ZFUTPDGcI78z4Y" +
	"Wr4GbQwx3xHD9aJsctJdAio8eX41oa1MYPv2gDYgBiVgAQiDoIEIWYQAoIRgiBChBpGICAIOIYE8QA" +
	"5YgBgBBBjBFEEIOUEoBBIZAQAhgMlAAIAECIM8hJp5gFBFFBiCACGeMYEsAKAwRhTBiIgARCIQCAqE" +
	"oh4QgyQCFCGDIEIIAIQYQAAIRARBhFFRFGAcGAUAgBBBDgAgCkAFRKKAGYUsw4wAFwyAhFhXCAGmcM" +
	"cQoJwCQRAhCkHDGAA6UAFgA"

const otherAudioFingerprint = "AQAA3UmWKdmUEj-Oo2Ev9Md3XBE-9IcpgpfQh8cBXw-e4Qn-wwwPoEdTDu8Bysfz4EZ_ND_-" +
	"Bw9zwcuE86jMoPlQNOMDuqjVoxETXGge9DlOVId_-MFpwceDo8E3BswzWOXxwx1RaPBxg3DWgT3c5d" +
	"DBXUGj6jjxRBmyI0RzPHhr_EHhKjjxFj8OyIcP8T1-_Njh4yP8oTr-oFeOpsJ7XPBpYEcPJuyDM2g0" +
	"3NIBCx908HAmH5KX4cPxBP-BX4flocdV9IHzw4eP4gZ_1KC-wDmc4MShbxfEE1-PZ1NwE8eB44cTow" +
	"_qo3mCws_RZ9A6iD-qReHRSziJ40d5PMZ7HH7x4vh49MNx3Dj84ZSM44e5Ht_w3fBxXMUPHz_wSSuO" +
	"wz--CCfI9jhcDsR1fIGoCj_MBZcOH4d-fMeDO3hgHSeOKzsMPIee4cYBCkARyCCjjEFAAAqIkYIApJ" +
	"RQQiCBDEFACASYQEICwJABSDjEEBDECCUIkgARooETBDDhFLIACACocAIJKwghxjChBGFCOMQEowoB" +
	"AAjgCACmjCIMAOMIAUYAwBwCREGAjEBGCCIFIIYYBggzwCAprACQEoGMMBQBYIRBlCqCqGBOOMQYUU" +
	"4AZhhiwllEqECAAg"

// fakeFingerprinter stands in for fpcalc. value is what it computes for whatever
// file it is handed, so a test decides what the copy sounds like by naming a
// fingerprint rather than by supplying audio.
type fakeFingerprinter struct {
	value     string
	err       error
	available bool
	lengths   []int
	// seconds overrides what Compute reports as measured. Zero means the
	// ordinary 30s a copy fixture is measured at; tests about the length gate
	// itself set this to something else — the CandidateLengthSeconds cap, or a
	// length that disagrees with the settled file it is compared against.
	seconds int
}

func (prints *fakeFingerprinter) Available() bool { return prints.available }

func (prints *fakeFingerprinter) Compute(
	_ context.Context, _ string, lengthSeconds int,
) (string, int, error) {
	prints.lengths = append(prints.lengths, lengthSeconds)
	if prints.err != nil {
		return "", 0, prints.err
	}
	seconds := prints.seconds
	if seconds == 0 {
		seconds = 30
	}
	return prints.value, seconds, nil
}

// anchoredCandidate is one file fetched for a want that carries an anchor.
func anchoredCandidate(t *testing.T) (*fakeImportStore, string, string, uuid.UUID) {
	t.Helper()
	store, inbox, libraryPath, requestID := fetchedCandidate(t, 3)
	store.targetRow.AnchorFingerprint = anchorFingerprint
	store.targetRow.AnchorSource = "deezer"
	store.targetRow.AnchorReference = "2178654"
	store.targetRow.AnchorSeconds = 30
	return store, inbox, libraryPath, requestID
}

// The copy is other audio. It is refused, and the refusal names the sample
// rather than AcoustID, because AcoustID had nothing to say about this music at
// all — which is the whole reason the want has a sample.
func TestACopyThatIsNotTheSampleIsRefused(t *testing.T) {
	store, inbox, libraryPath, requestID := anchoredCandidate(t)
	verifier := &fakeVerifier{verification: identity.Verification{
		AnchorSaysOtherwise: true, Contradicted: true,
		Summary: "the audio is not the published sample",
		Agrees:  []string{}, Differs: []string{"audio (not the published sample of this recording)"},
	}}
	importer := candidateImporter(store, inbox, libraryPath, nil, verifier).
		WithFingerprinter(&fakeFingerprinter{value: otherAudioFingerprint, available: true})

	if err := importer.ImportOne(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}

	if len(verifier.asked) != 1 || verifier.asked[0].Anchor == nil {
		t.Fatalf("evidence = %+v, want the measurement handed to the grader", verifier.asked)
	}
	if rate := verifier.asked[0].Anchor.Rate; rate < chromaprint.DiscardAbove {
		t.Errorf("rate = %v, want two unlike fingerprints to measure above %v",
			rate, chromaprint.DiscardAbove)
	}
	if store.settled == nil || store.settled.Verdict != db.AcquiredFileDiscardedAudio {
		t.Fatalf("verdict = %+v, want the copy discarded on its audio", store.settled)
	}
	if !strings.HasPrefix(store.settled.Summary, anchorRefusedSummary) {
		t.Errorf("summary = %q, want the sample's own sentence", store.settled.Summary)
	}
	if store.settled.Evidence == nil || store.settled.Evidence.Anchor == nil {
		t.Fatal("no anchor evidence was recorded")
	}
	anchor := store.settled.Evidence.Anchor
	if !anchor.Measured || anchor.Agrees == nil || *anchor.Agrees {
		t.Errorf("anchor = %+v, want a comparison that ran and disagreed", anchor)
	}
	if anchor.Source != "deezer" || anchor.Reference != "2178654" || anchor.Seconds != 30 {
		t.Errorf("anchor = %+v, want the sample's provenance kept with it", anchor)
	}
}

// The copy reproduces the sample, and that is what admits it. Nothing else
// proved it: the sample is the audio proof for music AcoustID has never heard,
// and audio is the only thing that may admit a file a stranger sent (ADR 0002).
func TestACopyThatMatchesTheSampleIsImported(t *testing.T) {
	store, inbox, libraryPath, requestID := anchoredCandidate(t)
	proven := provenByAudio()
	proven.Identity.Method = "published-sample"
	proven.AnchorAgrees = true
	verifier := &fakeVerifier{verification: proven}
	importer := candidateImporter(store, inbox, libraryPath, nil, verifier).
		WithFingerprinter(&fakeFingerprinter{value: anchorFingerprint, available: true})

	if err := importer.ImportOne(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}

	if rate := verifier.asked[0].Anchor.Rate; rate != 0 {
		t.Errorf("rate = %v, want a fingerprint measured against itself to be 0", rate)
	}
	if store.accepted == nil || store.accepted.Verdict != db.AcquiredFileAccepted {
		t.Fatalf("verdict = %+v, want the copy imported", store.accepted)
	}
	if store.accepted.Evidence.Method != "published-sample" {
		t.Errorf("method = %q, want the sample recorded as what proved it",
			store.accepted.Evidence.Method)
	}
	anchor := store.accepted.Evidence.Anchor
	if anchor.Agrees == nil || !*anchor.Agrees {
		t.Errorf("anchor = %+v, want the agreement written down", anchor)
	}
}

// The measurement reaches the grader naming the recording the sample was
// published for. Without that name one number would answer for whatever
// recording it was set beside, and a copy weighed against a list of candidates
// would come back proven for all of them.
func TestTheSampleReachesTheGraderNamingItsRecording(t *testing.T) {
	store, inbox, libraryPath, requestID := anchoredCandidate(t)
	verifier := &fakeVerifier{verification: identity.Verification{
		Summary: "nothing could decide", Agrees: []string{}, Differs: []string{},
	}}
	importer := candidateImporter(store, inbox, libraryPath, nil, verifier).
		WithFingerprinter(&fakeFingerprinter{value: anchorFingerprint, available: true})

	if err := importer.ImportOne(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}

	anchor := verifier.asked[0].Anchor
	if anchor == nil || anchor.RecordingID != store.targetRow.MusicBrainzRecordingID {
		t.Fatalf("anchor = %+v, want it naming the want's own recording %s",
			anchor, store.targetRow.MusicBrainzRecordingID)
	}
}

// The copy is read far enough in to hold the sample. A distributor cuts its
// excerpt from wherever in the song it likes, so a copy read only as far as
// AcoustID reads would fail to contain a sample taken from the middle — and a
// correct copy would then measure like a stranger.
func TestTheCopyIsReadFarEnoughInToHoldTheSample(t *testing.T) {
	store, inbox, libraryPath, requestID := anchoredCandidate(t)
	prints := &fakeFingerprinter{value: anchorFingerprint, available: true}
	importer := candidateImporter(store, inbox, libraryPath, nil,
		&fakeVerifier{verification: identity.Verification{}}).WithFingerprinter(prints)

	if err := importer.ImportOne(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}

	if len(prints.lengths) != 1 || prints.lengths[0] != chromaprint.CandidateLengthSeconds {
		t.Errorf("lengths = %v, want the copy read at %d seconds",
			prints.lengths, chromaprint.CandidateLengthSeconds)
	}
}

// A comparison that could not run. Each of these is a check that did not happen,
// never a verdict about the music: the copy is judged exactly as it was before
// anchors existed, and why nothing was measured is written down.
func TestAComparisonThatCouldNotRunRefusesNothing(t *testing.T) {
	for _, test := range []struct {
		name   string
		prints Fingerprinter
		anchor string
	}{
		{"fpcalc is not installed", &fakeFingerprinter{available: false}, anchorFingerprint},
		{"nothing is registered to fingerprint", nil, anchorFingerprint},
		{
			"the copy would not decode",
			&fakeFingerprinter{available: true, err: errors.New("no audio streams")},
			anchorFingerprint,
		},
		{
			// The copy holds less audio than the sample, so there is no position
			// the sample sits wholly inside it and there is nothing to measure.
			"the copy is shorter than the sample",
			&fakeFingerprinter{available: true, value: shortCopyFingerprint},
			anchorFingerprint,
		},
		{
			"the want's stored sample is not a fingerprint",
			&fakeFingerprinter{available: true, value: anchorFingerprint},
			"not a fingerprint at all",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, inbox, libraryPath, requestID := anchoredCandidate(t)
			store.targetRow.AnchorFingerprint = test.anchor
			verifier := &fakeVerifier{verification: identity.Verification{
				Summary: "nothing could decide", Agrees: []string{}, Differs: []string{},
			}}
			importer := candidateImporter(store, inbox, libraryPath, nil, verifier)
			if test.prints != nil {
				importer = importer.WithFingerprinter(test.prints)
			}

			if err := importer.ImportOne(context.Background(), requestID); err != nil {
				t.Fatal(err)
			}

			if verifier.asked[0].Anchor != nil {
				t.Errorf("anchor = %+v, want the grader told nothing was measured",
					verifier.asked[0].Anchor)
			}
			if store.settled == nil || store.settled.Verdict != db.AcquiredFileHeld {
				t.Fatalf("verdict = %+v, want the copy held", store.settled)
			}
			anchor := store.settled.Evidence.Anchor
			if anchor == nil || anchor.Measured || anchor.Unavailable == "" {
				t.Errorf("anchor = %+v, want a reason no comparison was made", anchor)
			}
		})
	}
}

// A want with no sample. Most music has none, and nothing about judging a copy
// for it changes.
func TestAWantWithNoSampleIsJudgedAsBefore(t *testing.T) {
	store, inbox, libraryPath, requestID := fetchedCandidate(t, 3)
	verifier := &fakeVerifier{verification: identity.Verification{
		Summary: "nothing could decide", Agrees: []string{}, Differs: []string{},
	}}
	importer := candidateImporter(store, inbox, libraryPath, nil, verifier).
		WithFingerprinter(&fakeFingerprinter{value: anchorFingerprint, available: true})

	if err := importer.ImportOne(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}

	if verifier.asked[0].Anchor != nil {
		t.Errorf("anchor = %+v, want nothing measured", verifier.asked[0].Anchor)
	}
	if store.settled.Evidence.Anchor != nil {
		t.Errorf("anchor evidence = %+v, want none recorded", store.settled.Evidence.Anchor)
	}
	if store.settled.Verdict != db.AcquiredFileHeld {
		t.Errorf("verdict = %q, want the copy held", store.settled.Verdict)
	}
}

// Four seconds of audio: less than the sample, so BitErrorRate has no alignment
// to read. Real, from the same fpcalc run as the others.
const shortCopyFingerprint = "AQAAC9GkaEzEJPjyo1r0oEmaD2fQ5jd-aE_xoeGJPicAYgBiVgAQiDoIAA"

// The gate minus refusal, as the importer sees it (docs/decisions/0029 §3).
//
// The grader concludes nothing from an upload found by name that reads as other
// audio, so the copy arrives here with nothing decided about it. It must be held
// with the sentence that says so, rather than with the sentence for a copy
// nothing spoke about at all: a reader told only "nothing could decide" would
// never learn that the audio and the upload are different, and a reader told the
// audio is different would read it as a discard.
func TestACopyThatIsNotTheUploadIsHeldAndNotRefused(t *testing.T) {
	store, inbox, libraryPath, requestID := anchoredCandidate(t)
	store.targetRow.AnchorSource = identity.AnchorByName
	store.targetRow.AnchorReference = "kQw4o8Uv"
	store.targetRow.AnchorLabel = "Vela Nine - Watchfire (Official Audio) — Vela Nine"
	store.targetRow.AnchorViews = 1_200_000
	verifier := &fakeVerifier{verification: identity.Verification{
		Summary: "nothing could decide", Agrees: []string{}, Differs: []string{},
	}}
	importer := candidateImporter(store, inbox, libraryPath, nil, verifier).
		WithFingerprinter(&fakeFingerprinter{value: otherAudioFingerprint, available: true})

	if err := importer.ImportOne(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}

	if store.settled == nil || store.settled.Verdict != db.AcquiredFileHeld {
		t.Fatalf("verdict = %+v, want the copy held rather than discarded", store.settled)
	}
	if !strings.HasPrefix(store.settled.Summary, uploadUnmatchedSummary) {
		t.Errorf("summary = %q, want the sentence that says this refuses nothing",
			store.settled.Summary)
	}
	anchor := store.settled.Evidence.Anchor
	if anchor == nil || !anchor.Measured {
		t.Fatalf("anchor = %+v, want the measurement recorded", anchor)
	}
	if anchor.Agrees != nil {
		t.Errorf("agrees = %v, want nothing concluded", *anchor.Agrees)
	}
	if anchor.Label != store.targetRow.AnchorLabel || anchor.Views != 1_200_000 {
		t.Errorf("anchor = %+v, want the upload named on the copy", anchor)
	}
}

// The same measurement against a distributor's own sample still refuses. The two
// witnesses differ in exactly one direction, and this is the other side of it.
func TestACopyThatIsNotTheDistributorsSampleIsStillRefused(t *testing.T) {
	store, inbox, libraryPath, requestID := anchoredCandidate(t)
	verifier := &fakeVerifier{verification: identity.Verification{
		AnchorSaysOtherwise: true, Contradicted: true,
		Summary: "the audio is not the published sample",
		Agrees:  []string{}, Differs: []string{"audio (not the published sample of this recording)"},
	}}
	importer := candidateImporter(store, inbox, libraryPath, nil, verifier).
		WithFingerprinter(&fakeFingerprinter{value: otherAudioFingerprint, available: true})

	if err := importer.ImportOne(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}

	if store.settled == nil || store.settled.Verdict != db.AcquiredFileDiscardedAudio {
		t.Fatalf("verdict = %+v, want the copy discarded on its audio", store.settled)
	}
}
