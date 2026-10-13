package downloads

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/identity"
)

// A copy is one file Schall fetched for a want, and what validation decided
// about it is normally permanent. A second pass exists because two rules changed
// underneath copies already decided: wants gained an audio anchor, and an
// identification naming another MusicBrainz row of one registered track stopped
// being read as a refusal.
//
// These tests are about what the pass offers and what it refuses to touch. What
// it decides is decided by the same ImportOne every fresh copy goes through, and
// is pinned where that lives.

// offered is one copy in the pass's list, pointing at the file the shared
// fixture puts in the inbox.
func offered(store *fakeImportStore, requestID uuid.UUID) db.JudgeAgainRow {
	return db.JudgeAgainRow{
		CopyID:              uuid.New(),
		AcquisitionTargetID: store.targetRow.AcquisitionTargetID,
		RequestID:           requestID,
		SourceDirectory:     `Music\Anetha`,
		FileName:            "03.flac",
		Verdict:             "held",
	}
}

// The copy is put back where validation claims from, and then judged. Nothing
// here decides anything: it is the same call a copy gets the moment it arrives.
func TestACopyOfferedAgainGoesThroughValidation(t *testing.T) {
	store, inbox, libraryPath, requestID := fetchedCandidate(t, 3)
	store.judgeAgain = []db.JudgeAgainRow{offered(store, requestID)}
	importer := candidateImporter(store, inbox, libraryPath, nil, &fakeVerifier{
		verification: identity.Verification{
			Summary: "nothing could decide", Agrees: []string{}, Differs: []string{},
		},
	})

	judged, err := importer.judgeAgain(context.Background())
	if err != nil {
		t.Fatalf("judgeAgain() error = %v", err)
	}
	if judged.Judged != 1 {
		t.Fatalf("judged = %+v, want the copy judged", judged)
	}
	if len(store.reopened) != 1 || store.reopened[0] != requestID {
		t.Errorf("reopened = %v, want the copy's own request", store.reopened)
	}
	if store.settled == nil {
		t.Error("nothing was recorded, want the copy decided again")
	}
}

// A want whose music has already arrived is left alone. Validation imports
// whatever it proves and never asks whether the want was answered a moment ago,
// so a want with fifteen copies would otherwise take fifteen of them.
func TestAWantThatAlreadyHasItsMusicIsNotOfferedMore(t *testing.T) {
	store, inbox, libraryPath, requestID := fetchedCandidate(t, 3)
	store.judgeAgain = []db.JudgeAgainRow{offered(store, requestID)}
	store.answeredWants = map[uuid.UUID]bool{store.targetRow.AcquisitionTargetID: true}
	importer := candidateImporter(store, inbox, libraryPath, nil,
		&fakeVerifier{verification: identity.Verification{}})

	judged, err := importer.judgeAgain(context.Background())
	if err != nil {
		t.Fatalf("judgeAgain() error = %v", err)
	}
	if judged.Judged != 0 || judged.Answered != 1 {
		t.Fatalf("judged = %+v, want the copy left alone", judged)
	}
	if len(store.reopened) != 0 {
		t.Errorf("reopened = %v, want nothing put back into validation", store.reopened)
	}
}

// A copy whose bytes have gone from the inbox keeps the verdict it has.
//
// This is the question the pass could destroy. Validation reads a missing file
// as bytes that never arrived and records it as such, so a copy somebody was
// going to answer would be overwritten with a wrong account of why nobody can.
func TestACopyWhoseFileIsGoneIsLeftExactlyAsItIs(t *testing.T) {
	store, inbox, libraryPath, requestID := fetchedCandidate(t, 3)
	store.judgeAgain = []db.JudgeAgainRow{offered(store, requestID)}
	if err := os.Remove(filepath.Join(inbox, "Anetha", "03.flac")); err != nil {
		t.Fatal(err)
	}
	importer := candidateImporter(store, inbox, libraryPath, nil,
		&fakeVerifier{verification: identity.Verification{}})

	judged, err := importer.judgeAgain(context.Background())
	if err != nil {
		t.Fatalf("judgeAgain() error = %v", err)
	}
	if judged.Missing != 1 || judged.Judged != 0 {
		t.Fatalf("judged = %+v, want the copy counted as missing and not touched", judged)
	}
	if len(store.reopened) != 0 {
		t.Errorf("reopened = %v, want nothing put back into validation", store.reopened)
	}
	if store.settled != nil {
		t.Errorf("settled = %+v, want the recorded verdict left alone", store.settled)
	}
}

// A copy something else took first is passed over rather than fought for. Its
// verdict is being decided by whoever took it, and two passes deciding one copy
// is the one thing worth avoiding here.
func TestACopyTakenBySomethingElseIsPassedOver(t *testing.T) {
	store, inbox, libraryPath, requestID := fetchedCandidate(t, 3)
	store.judgeAgain = []db.JudgeAgainRow{offered(store, requestID)}
	store.reopenErr = db.ErrCopyNotReopened
	importer := candidateImporter(store, inbox, libraryPath, nil,
		&fakeVerifier{verification: identity.Verification{}})

	judged, err := importer.judgeAgain(context.Background())
	if err != nil {
		t.Fatalf("judgeAgain() error = %v", err)
	}
	if judged.Judged != 0 {
		t.Fatalf("judged = %+v, want the copy passed over", judged)
	}
	if store.settled != nil {
		t.Errorf("settled = %+v, want the recorded verdict left alone", store.settled)
	}
}

// One copy that could not be judged does not end the pass. The rest of the
// collection is waiting on it, and the copy is left where validation will find
// it again.
func TestOneCopyThatCouldNotBeJudgedDoesNotStopThePass(t *testing.T) {
	store, inbox, libraryPath, requestID := fetchedCandidate(t, 3)
	first := offered(store, requestID)
	second := offered(store, requestID)
	second.AcquisitionTargetID = uuid.New()
	store.judgeAgain = []db.JudgeAgainRow{first, second}
	importer := candidateImporter(store, inbox, libraryPath, nil, &fakeVerifier{
		verification: identity.Verification{
			Summary: "nothing could decide", Agrees: []string{}, Differs: []string{},
		},
	})
	store.targetClaimErr = errors.New("the request went away")

	judged, err := importer.judgeAgain(context.Background())
	if err != nil {
		t.Fatalf("judgeAgain() error = %v, want a pass that carries on", err)
	}
	if judged.Offered != 2 || judged.Judged != 0 {
		t.Fatalf("judged = %+v, want both offered and neither judged", judged)
	}
	if len(store.reopened) != 2 {
		t.Errorf("reopened = %v, want the second copy still tried", store.reopened)
	}
	// The claim each failed judgement took is given back. Left at 'validating',
	// the copy would vanish from the queue and the want could not fetch another.
	if len(store.released) != 2 {
		t.Errorf("released = %v, want both copies given back after their judgement failed", store.released)
	}
}

// The pass over the copies refused on the artist tag reads its own list. The two
// passes offer different sets, and a copy in one is not offered by the other.
func TestTheCreditRefusalPassReadsItsOwnOffers(t *testing.T) {
	store, inbox, libraryPath, requestID := fetchedCandidate(t, 3)
	store.creditRefusals = []db.JudgeAgainRow{offered(store, requestID)}
	importer := candidateImporter(store, inbox, libraryPath, nil, &fakeVerifier{
		verification: identity.Verification{
			Summary: "nothing could decide", Agrees: []string{}, Differs: []string{},
		},
	})

	judged, err := importer.judgeCreditRefusalsAgain(context.Background())
	if err != nil {
		t.Fatalf("judgeCreditRefusalsAgain() error = %v", err)
	}
	if judged.Judged != 1 {
		t.Fatalf("judged = %+v, want the copy judged", judged)
	}
	if len(store.reopened) != 1 || store.reopened[0] != requestID {
		t.Errorf("reopened = %v, want the copy's own request", store.reopened)
	}
}

// An anchor landing on a want is a witness its copies were judged without, so
// that want's copies go back through the same validation without anybody asking
// (ADR 0029 §5). It reaches that want's copies and no others.
func TestANewlyAnchoredWantsCopiesAreJudgedAgain(t *testing.T) {
	store, inbox, libraryPath, requestID := fetchedCandidate(t, 3)
	elsewhere := offered(store, uuid.New())
	elsewhere.AcquisitionTargetID = uuid.New()
	store.judgeAgain = []db.JudgeAgainRow{offered(store, requestID), elsewhere}
	importer := candidateImporter(store, inbox, libraryPath, nil, &fakeVerifier{
		verification: identity.Verification{
			Summary: "nothing could decide", Agrees: []string{}, Differs: []string{},
		},
	})

	err := importer.JudgeWantCopies(context.Background(), store.targetRow.AcquisitionTargetID, time.Time{})
	if err != nil {
		t.Fatalf("JudgeWantCopies() error = %v", err)
	}
	if len(store.reopened) != 1 || store.reopened[0] != requestID {
		t.Errorf("reopened = %v, want this want's copy alone", store.reopened)
	}
}

// The copy's bytes have left the inbox, so there is nothing to put back through
// validation and nothing to import either. What is left is the fingerprint kept
// on the copy, and measuring that against the new anchor is worth doing: it
// tells the person reading the queue that this copy was the right recording and
// that the want has to be fetched again. The verdict is not touched.
func TestACopyWithNoBytesIsMeasuredFromItsStoredFingerprint(t *testing.T) {
	store, inbox, libraryPath, _ := fetchedCandidate(t, 3)
	copied := offered(store, uuid.New())
	copied.FileName = "gone.flac"
	copied.AudioFingerprint = anchorFingerprint
	copied.AnchorFingerprint = anchorFingerprint
	copied.AnchorSource = identity.AnchorByNameTopic
	copied.AnchorReference = "kQw4o8Uv"
	copied.AnchorLabel = "Vela Nine - Watchfire (Official Audio) — Vela Nine"
	copied.AnchorViews = 1_200_000
	copied.AnchorSeconds = 30
	store.judgeAgain = []db.JudgeAgainRow{copied}
	importer := candidateImporter(store, inbox, libraryPath, nil,
		&fakeVerifier{verification: identity.Verification{}})

	err := importer.JudgeWantCopies(context.Background(), store.targetRow.AcquisitionTargetID, time.Time{})
	if err != nil {
		t.Fatalf("JudgeWantCopies() error = %v", err)
	}
	if len(store.reopened) != 0 {
		t.Fatalf("reopened = %v, want a copy with no bytes left out of validation", store.reopened)
	}
	if store.settled != nil {
		t.Fatalf("verdict = %+v, want a copy with no bytes left as it was", store.settled)
	}
	reading, measured := store.anchorReadings[copied.CopyID]
	if !measured {
		t.Fatal("nothing was measured, want the stored fingerprint read against the anchor")
	}
	if !reading.Measured || reading.Agrees == nil || !*reading.Agrees {
		t.Errorf("anchor = %+v, want a fingerprint measured against itself to agree", reading)
	}
	if reading.Label != copied.AnchorLabel || reading.Views != 1_200_000 {
		t.Errorf("anchor = %+v, want the upload named on the copy", reading)
	}
	if store.anchorSummaries[copied.CopyID] != storedProvenSummary {
		t.Errorf("summary = %q, want the sentence for a copy whose file has gone",
			store.anchorSummaries[copied.CopyID])
	}
}

// The same copy against an upload found by name, not the artist's Topic
// channel: the person is told the audio matched, and nothing is concluded.
func TestACopyWithNoBytesIsMeasuredFromItsStoredFingerprintByNameStaysAQuestion(t *testing.T) {
	store, inbox, libraryPath, _ := fetchedCandidate(t, 3)
	copied := offered(store, uuid.New())
	copied.FileName = "gone.flac"
	copied.AudioFingerprint = anchorFingerprint
	copied.AnchorFingerprint = anchorFingerprint
	copied.AnchorSource = identity.AnchorByName
	copied.AnchorReference = "kQw4o8Uv"
	copied.AnchorLabel = "Vela Nine - Watchfire (Official Audio) — Vela Nine"
	copied.AnchorViews = 1_200_000
	copied.AnchorSeconds = 30
	store.judgeAgain = []db.JudgeAgainRow{copied}
	importer := candidateImporter(store, inbox, libraryPath, nil,
		&fakeVerifier{verification: identity.Verification{}})

	err := importer.JudgeWantCopies(context.Background(), store.targetRow.AcquisitionTargetID, time.Time{})
	if err != nil {
		t.Fatalf("JudgeWantCopies() error = %v", err)
	}
	if len(store.reopened) != 0 {
		t.Fatalf("reopened = %v, want a copy with no bytes left out of validation", store.reopened)
	}
	if store.settled != nil {
		t.Fatalf("verdict = %+v, want a copy with no bytes left as it was", store.settled)
	}
	reading, measured := store.anchorReadings[copied.CopyID]
	if !measured {
		t.Fatal("nothing was measured, want the stored fingerprint read against the anchor")
	}
	if !reading.Measured || reading.Agrees == nil || !*reading.Agrees {
		t.Errorf("anchor = %+v, want a fingerprint measured against itself to agree", reading)
	}
	if reading.Label != copied.AnchorLabel || reading.Views != 1_200_000 {
		t.Errorf("anchor = %+v, want the upload named on the copy", reading)
	}
	if store.anchorSummaries[copied.CopyID] != storedUndecidedSummary {
		t.Errorf("summary = %q, want an upload found by name to leave the copy undecided",
			store.anchorSummaries[copied.CopyID])
	}
}

// The same copy against an upload it does not reproduce concludes nothing, and
// nothing is what is written down. An upload found by name never refuses.
func TestACopyWithNoBytesIsNotRefusedByAnUpload(t *testing.T) {
	store, inbox, libraryPath, _ := fetchedCandidate(t, 3)
	copied := offered(store, uuid.New())
	copied.FileName = "gone.flac"
	copied.AudioFingerprint = otherAudioFingerprint
	copied.AnchorFingerprint = anchorFingerprint
	copied.AnchorSource = identity.AnchorByName
	copied.AnchorSeconds = 30
	store.judgeAgain = []db.JudgeAgainRow{copied}
	importer := candidateImporter(store, inbox, libraryPath, nil,
		&fakeVerifier{verification: identity.Verification{}})

	err := importer.JudgeWantCopies(context.Background(), store.targetRow.AcquisitionTargetID, time.Time{})
	if err != nil {
		t.Fatalf("JudgeWantCopies() error = %v", err)
	}
	reading := store.anchorReadings[copied.CopyID]
	if reading.Agrees != nil {
		t.Errorf("agrees = %v, want an upload found by name to conclude nothing", *reading.Agrees)
	}
	if store.anchorSummaries[copied.CopyID] != storedUndecidedSummary {
		t.Errorf("summary = %q, want the sentence for a copy nothing decided about",
			store.anchorSummaries[copied.CopyID])
	}
}

// A want that stopped fetching because it held a copy nobody had answered has
// something to answer it with now. The pass puts the copies to the anchor and
// then asks for the want's next look back, so a want whose copies the anchor
// could not settle is not left where nothing would ever look at it again.
func TestANewlyAnchoredWantIsAskedToLookAgain(t *testing.T) {
	store, inbox, libraryPath, requestID := fetchedCandidate(t, 3)
	store.judgeAgain = []db.JudgeAgainRow{offered(store, requestID)}
	store.rearms = true
	importer := candidateImporter(store, inbox, libraryPath, nil, &fakeVerifier{
		verification: identity.Verification{
			Summary: "nothing could decide", Agrees: []string{}, Differs: []string{},
		},
	})

	targetID := store.targetRow.AcquisitionTargetID
	if err := importer.JudgeWantCopies(context.Background(), targetID, time.Time{}); err != nil {
		t.Fatalf("JudgeWantCopies() error = %v", err)
	}
	if len(store.rearmed) != 1 || store.rearmed[0] != targetID {
		t.Fatalf("rearmed = %v, want the judged want asked to look again", store.rearmed)
	}
}

// A pass is worked in parts of judgeAgainWants wants. A part that leaves wants
// behind names the last want it took, and the next part starts after it, so a
// restart between parts does not judge the first wants again.
func TestAPartOfThePassStopsAfterItsWantsAndTheNextCarriesOn(t *testing.T) {
	store, inbox, libraryPath, _ := fetchedCandidate(t, 3)
	store.answeredWants = map[uuid.UUID]bool{}
	for range judgeAgainWants + 1 {
		row := offered(store, uuid.New())
		row.AcquisitionTargetID = uuid.New()
		store.answeredWants[row.AcquisitionTargetID] = true
		store.judgeAgain = append(store.judgeAgain, row)
	}
	importer := candidateImporter(store, inbox, libraryPath, nil,
		&fakeVerifier{verification: identity.Verification{}})

	first, next, more, err := importer.judgeAgainPart(
		context.Background(), db.JudgeAgainPlace{}, time.Time{}, nil)
	if err != nil {
		t.Fatalf("judgeAgainPart() error = %v", err)
	}
	if first.Answered != judgeAgainWants || !more {
		t.Fatalf("first part = %+v, more = %v, want %d wants and one left",
			first, more, judgeAgainWants)
	}
	second, next, more, err := importer.judgeAgainPart(context.Background(), next, time.Time{}, nil)
	if err != nil {
		t.Fatalf("judgeAgainPart() error = %v", err)
	}
	if second.Answered != 1 || !more || next != (db.JudgeAgainPlace{Rest: true}) {
		t.Fatalf("second part = %+v, next %+v, more = %v, want the one want left and the second round next",
			second, next, more)
	}
	third, _, more, err := importer.judgeAgainPart(context.Background(), next, time.Time{}, nil)
	if err != nil {
		t.Fatalf("judgeAgainPart() error = %v", err)
	}
	if third.Offered != 0 || more {
		t.Fatalf("third part = %+v, more = %v, want nothing in the second round and the pass over",
			third, more)
	}
}

// Every copy of a want lands in the same part, because the judges are serial
// per want and a want split across parts could take two copies.
func TestAPartNeverSplitsAWantsCopies(t *testing.T) {
	first, second := uuid.New(), uuid.New()
	copies := []db.JudgeAgainRow{
		{AcquisitionTargetID: first}, {AcquisitionTargetID: first},
		{AcquisitionTargetID: second}, {AcquisitionTargetID: second},
	}

	part, more := firstWants(copies, 1)

	if len(part) != 2 || !more {
		t.Fatalf("part = %d copies, more = %v, want both copies of the first want", len(part), more)
	}
}

// A part keeps its place one want at a time. A restart in the middle of a part
// then resumes at the want it was on, not at the part's first want.
func TestAPartKeepsItsPlaceAfterEachWant(t *testing.T) {
	store, inbox, libraryPath, _ := fetchedCandidate(t, 3)
	store.answeredWants = map[uuid.UUID]bool{}
	for range 3 {
		row := offered(store, uuid.New())
		row.AcquisitionTargetID = uuid.New()
		store.answeredWants[row.AcquisitionTargetID] = true
		store.judgeAgain = append(store.judgeAgain, row)
	}
	importer := candidateImporter(store, inbox, libraryPath, nil,
		&fakeVerifier{verification: identity.Verification{}})

	var kept []uuid.UUID
	_, _, err := importer.JudgeAgain(context.Background(), db.JudgeAgainPlace{}, time.Time{},
		func(_ context.Context, want uuid.UUID) error {
			kept = append(kept, want)
			return nil
		})
	if err != nil {
		t.Fatalf("JudgeAgain() error = %v", err)
	}
	wants := make([]uuid.UUID, 0, len(store.judgeAgain))
	for _, row := range store.judgeAgain {
		wants = append(wants, row.AcquisitionTargetID)
	}
	slices.SortFunc(wants, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
	if !slices.Equal(kept, wants) {
		t.Fatalf("places kept = %v, want each want in order %v", kept, wants)
	}
}

// hangingVerifier never answers until the caller gives up, like a request that
// is waiting on a connection nothing will ever write to.
type hangingVerifier struct{}

func (hangingVerifier) Verify(
	ctx context.Context, _ identity.Evidence, _ uuid.UUID,
) (identity.Verification, error) {
	<-ctx.Done()
	return identity.Verification{}, ctx.Err()
}

// One copy that hangs is cut off, given back, and the pass goes on to the
// next want. Before, the part waited on it for as long as it hung.
func TestACopyThatHangsIsCutOffAndThePassGoesOn(t *testing.T) {
	limit := judgeCopyLimit
	judgeCopyLimit = 50 * time.Millisecond
	t.Cleanup(func() { judgeCopyLimit = limit })

	store, inbox, libraryPath, requestID := fetchedCandidate(t, 3)
	hung := offered(store, requestID)
	next := offered(store, uuid.New())
	next.AcquisitionTargetID = uuid.New()
	store.answeredWants = map[uuid.UUID]bool{next.AcquisitionTargetID: true}
	store.judgeAgain = []db.JudgeAgainRow{hung, next}
	importer := candidateImporter(store, inbox, libraryPath, nil, hangingVerifier{})

	done := make(chan JudgedAgain, 1)
	go func() {
		result, _, _, err := importer.judgeAgainPart(
			context.Background(), db.JudgeAgainPlace{}, time.Time{}, nil)
		if err != nil {
			t.Errorf("judgeAgainPart() error = %v", err)
		}
		done <- result
	}()
	select {
	case result := <-done:
		if result.Judged != 0 || result.Answered != 1 {
			t.Fatalf("result = %+v, want the hung copy unjudged and the next want reached", result)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the pass is still waiting on a copy that hangs")
	}
	if !slices.Contains(store.released, requestID) {
		t.Fatalf("released = %v, want the hung copy given back", store.released)
	}
}

// sleepyVerifier takes a moment over every copy, like a want whose copies are
// each a few decodes and paced requests.
type sleepyVerifier struct{ asked int }

func (verifier *sleepyVerifier) Verify(
	_ context.Context, _ identity.Evidence, _ uuid.UUID,
) (identity.Verification, error) {
	verifier.asked++
	time.Sleep(30 * time.Millisecond)
	return identity.Verification{Summary: "nothing could decide"}, nil
}

// A want held in many copies gets a share of the pass, not all of it. Before,
// a want with dozens of copies each under the slow mark held the pass with
// nothing in the log to say so.
func TestAWantsCopiesStopWhenItsTimeRunsOutAndThePassGoesOn(t *testing.T) {
	limit := judgeWantLimit
	judgeWantLimit = 10 * time.Millisecond
	t.Cleanup(func() { judgeWantLimit = limit })

	store, inbox, libraryPath, requestID := fetchedCandidate(t, 3)
	many := []db.JudgeAgainRow{offered(store, requestID)}
	for range 3 {
		many = append(many, offered(store, uuid.New()))
	}
	next := offered(store, uuid.New())
	next.AcquisitionTargetID = uuid.New()
	store.answeredWants = map[uuid.UUID]bool{next.AcquisitionTargetID: true}
	store.judgeAgain = append(many, next)
	verifier := &sleepyVerifier{}
	importer := candidateImporter(store, inbox, libraryPath, nil, verifier)

	var kept []uuid.UUID
	result, _, _, err := importer.judgeAgainPart(context.Background(), db.JudgeAgainPlace{}, time.Time{},
		func(_ context.Context, want uuid.UUID) error {
			kept = append(kept, want)
			return nil
		})
	if err != nil {
		t.Fatalf("judgeAgainPart() error = %v", err)
	}
	if verifier.asked != 1 || result.Deferred != 3 {
		t.Fatalf("asked %d, result %+v, want one copy judged and three left", verifier.asked, result)
	}
	if result.Answered != 1 || len(kept) != 2 {
		t.Fatalf("result %+v, places %v, want the next want reached and both places kept",
			result, kept)
	}
}
