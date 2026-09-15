package anchor

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/chromaprint"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/deezer"
	"github.com/pxldi/schall/internal/identity"
	"github.com/rs/zerolog"
)

// Two fingerprints fpcalc 1.6.1 actually printed, so an anchor in these tests
// is a value the comparison could read rather than a plausible-looking string.
// Both are of generated audio — three tones moving against each other, the same
// shape internal/chromaprint's contract test uses — so nothing here depends on a
// file or on anybody's music.
//
// The first stands for thirty seconds, the length a preview is. The second
// stands for four, which is the case that must be refused: it decodes, and it is
// far too little audio for a bit error rate over it to mean anything.
const (
	previewFingerprint = "AQAA3dGkaEzEJPjyo1r0oEmaD2fQ5jd-aE_xoeGJPidyNai4o3lMnIbVIN7xaQukKieaqCwYdxfO4C" +
		"EPJle6wPqhiRGaHz10Im1U4_nxkaguo0kqT8jxxNB99Dg9NEceHQ-_ItSPps7xD91HFWLooMEXxuhZwRf0HE5WGq" +
		"EuPDzOBz_ewN9E_MPxoxKD5GyO8FlONNdxwpt2ozf6BM3xI34gKraS4P-QK8ZHw1OmhPjm4Avyo4mjEl10BeoSGz" +
		"_8CLeCfRb-4EoYaKlyVBGq_-hDEblT4cmJP2h_hPmx5_izgj9EpgfzCz6chsGzHT6PWh2LJwvMKNCVHM-VFU2ONj" +
		"t-HNp7uGglnFdQkT4aQfp4kBxKVIvO4Az0w5uNL7hjiFeCB_G44KaC_ricPPCP8BdOnwJrpcero4lzDYrOHCGZXT" +
		"geXI6D5uXwHb-CZFGaBKGEB3t0CX8Exj6ah8dNqThyHWoHX4d0PpAchUdzHk8PHxfRo6aCJk4CuaxwOit8_Dhha8" +
		"G_whfxEq-po4WoNYezKA_uU7gIqjr6SEazzuihC09aCc6FH3mT48sO3_h5ZFUTPDGcI78z4YWr4GbQwx3xHD9aJs" +
		"ctJdAio8eX41oa1MYPv2gDYgBiVgAQiDoIEIWYQAoIRgiBChBpGICAIOIYE8QA5YgBgBBBjBFEEIOUEoBBIZAQAh" +
		"gMlAAIAECIM8hJp5gFBFFBiCACGeMYEsAKAwRhTBiIgARCIQCAqEoh4QgyQCFCGDIEIIAIQYQAAIRARBhFFRFGAc" +
		"GAUAgBBBDgAgCkAFRKKAGYUsw4wAFwyAhFhXCAGmcMcQoJwCQRAhCkHDGAA6UAFgA"
	shortFingerprint = "AQAAC9GkaEzEJPjyo1r0oEmaD2fQ5jd-aE_xoeGJPicAYgBiVgAQiDoIAA"
)

type fakeStore struct {
	due      []db.AnchorDueRow
	recorded []db.AnchorParams
	absent   map[uuid.UUID]string
	deferred map[uuid.UUID]time.Time
	reasons  map[uuid.UUID]string
	// judging is the wants whose held copies were asked to be measured against
	// the anchor that had just landed.
	judging []uuid.UUID
}

func newStore(due ...db.AnchorDueRow) *fakeStore {
	return &fakeStore{
		due:      due,
		absent:   map[uuid.UUID]string{},
		deferred: map[uuid.UUID]time.Time{},
		reasons:  map[uuid.UUID]string{},
	}
}

func (store *fakeStore) DueAnchors(
	_ context.Context, _ time.Time, limit int32,
) ([]db.AnchorDueRow, error) {
	if int(limit) < len(store.due) {
		return store.due[:limit], nil
	}
	return store.due, nil
}

func (store *fakeStore) RecordAnchor(_ context.Context, params db.AnchorParams) error {
	store.recorded = append(store.recorded, params)
	return nil
}

func (store *fakeStore) RecordAnchorAbsent(
	_ context.Context, targetID uuid.UUID, reason string,
) error {
	store.absent[targetID] = reason
	return nil
}

func (store *fakeStore) DeferAnchor(
	_ context.Context, targetID uuid.UUID, at time.Time, reason string,
) error {
	store.deferred[targetID] = at
	store.reasons[targetID] = reason
	return nil
}

func (store *fakeStore) QueueWantCopyJudging(
	_ context.Context, targetID uuid.UUID, _ time.Time,
) error {
	store.judging = append(store.judging, targetID)
	return nil
}

// fakeFinder stands in for the upload search. uploads is what a search returns,
// excerpt the bytes taken out of the one that was chosen, and asked records the
// queries and the ids so a test can read what the rule actually sent.
type fakeFinder struct {
	uploads        []Upload
	searchErr      error
	excerpt        []byte
	excerptErr     error
	available      bool
	asked          []string
	fetched        []string
	excerptSeconds int
}

func (finder *fakeFinder) Available() bool { return finder.available }

func (finder *fakeFinder) Search(
	_ context.Context, query string, _ int,
) ([]Upload, error) {
	finder.asked = append(finder.asked, query)
	return finder.uploads, finder.searchErr
}

func (finder *fakeFinder) Excerpt(_ context.Context, id string, durationSeconds int) ([]byte, error) {
	finder.fetched = append(finder.fetched, id)
	finder.excerptSeconds = durationSeconds
	return finder.excerpt, finder.excerptErr
}

type fakeDistributor struct {
	track    deezer.Track
	trackErr error
	audio    []byte
	audioErr error
	asked    []string
}

func (distributor *fakeDistributor) TrackByISRC(
	_ context.Context, isrc string,
) (deezer.Track, error) {
	distributor.asked = append(distributor.asked, isrc)
	return distributor.track, distributor.trackErr
}

func (distributor *fakeDistributor) FetchPreview(_ context.Context, _ string) ([]byte, error) {
	if distributor.audioErr != nil {
		return nil, distributor.audioErr
	}
	return distributor.audio, nil
}

type fakePrints struct {
	value     string
	seconds   int
	err       error
	available bool
	lengths   []int
}

func (prints *fakePrints) Available() bool { return prints.available }

func (prints *fakePrints) Compute(
	_ context.Context, _ string, lengthSeconds int,
) (string, int, error) {
	prints.lengths = append(prints.lengths, lengthSeconds)
	return prints.value, prints.seconds, prints.err
}

func want(isrc string) db.AnchorDueRow {
	return db.AnchorDueRow{
		ID: uuid.New(), ISRC: isrc, Artist: "Vela Nine", Title: "Watchfire",
		DurationMS: 305000,
	}
}

func newService(
	store Store, distributor Distributor, prints Fingerprinter,
) *Service {
	return newServiceFinding(store, distributor, nil, prints)
}

func newServiceFinding(
	store Store, distributor Distributor, finder Finder, prints Fingerprinter,
) *Service {
	service := NewService(store, distributor, finder, prints, zerolog.Nop())
	// Nothing here waits on a clock: the pause between requests is the pace the
	// distributor is asked at, and a test that served it would spend its run
	// asleep.
	service.pause = func(context.Context, time.Duration) {}
	return service
}

func workingParts() (*fakeDistributor, *fakePrints) {
	distributor := &fakeDistributor{
		track: deezer.Track{ID: 4166953962, Title: "Watchfire", DurationSeconds: 305,
			PreviewURL: "https://cdn.deezer.example/preview.mp3"},
		audio: []byte("audio"),
	}
	prints := &fakePrints{value: previewFingerprint, seconds: 30, available: true}
	return distributor, prints
}

func TestAPreviewIsKeptAsTheWantsAnchor(t *testing.T) {
	store := newStore(want("QZTAT2400123"))
	distributor, prints := workingParts()

	if _, err := newService(store, distributor, prints).Sweep(context.Background()); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	if len(store.recorded) != 1 {
		t.Fatalf("recorded %d anchors, wanted 1", len(store.recorded))
	}
	anchor := store.recorded[0]
	if anchor.Fingerprint != previewFingerprint {
		t.Errorf("kept fingerprint %q", anchor.Fingerprint)
	}
	if anchor.Source != "deezer" || anchor.Reference != "4166953962" {
		t.Errorf("kept %q/%q, which cannot be traced back to the published track",
			anchor.Source, anchor.Reference)
	}
	if anchor.Seconds != 30 {
		t.Errorf("kept %d seconds of audio, wanted the 30 the decoder measured", anchor.Seconds)
	}
}

func TestThePreviewIsFingerprintedAtTheReferenceLength(t *testing.T) {
	store := newStore(want("QZTAT2400123"))
	distributor, prints := workingParts()

	if _, err := newService(store, distributor, prints).Sweep(context.Background()); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	if len(prints.lengths) != 1 || prints.lengths[0] != chromaprint.ReferenceLengthSeconds {
		t.Fatalf("fingerprinted at %v seconds, wanted %d",
			prints.lengths, chromaprint.ReferenceLengthSeconds)
	}
}

// A want the distributor has no preview for is silence only once the upload
// search has looked too. The preview sentence is still written, because why the
// keyed reference is missing and why the searched one is are two facts.
func TestARecordingWithNoPreviewIsSilenceAndNotAskedAboutAgain(t *testing.T) {
	one := want("QZTAT2400123")
	store := newStore(one)
	distributor, prints := workingParts()
	distributor.trackErr = deezer.ErrNoPreview
	finder := &fakeFinder{available: true}

	service := newServiceFinding(store, distributor, finder, prints)
	if _, err := service.Sweep(context.Background()); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	if store.absent[one.ID] != noPreviewPublished+" "+noUploadMatched {
		t.Errorf("recorded %q", store.absent[one.ID])
	}
	if _, deferred := store.deferred[one.ID]; deferred {
		t.Error("a recording neither the distributor nor an upload carries was asked about again")
	}
	if len(store.recorded) != 0 {
		t.Error("an anchor was kept for a recording with no preview")
	}
}

// The distinction this test holds is the whole reason the two are separate
// calls. Deezer reports "we hold nothing under that key" and "you have asked too
// often" the same way — inside a body sent with HTTP 200 — and reading the second
// as the first would turn a busy afternoon into a permanent fact about the music.
func TestRateLimitingIsNeverRecordedAsAMissingPreview(t *testing.T) {
	one := want("QZTAT2400123")
	store := newStore(one)
	distributor, prints := workingParts()
	distributor.trackErr = deezer.ErrRateLimited

	if _, err := newService(store, distributor, prints).Sweep(context.Background()); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	if _, absent := store.absent[one.ID]; absent {
		t.Fatal("a rate-limited minute was written down as the recording having no preview")
	}
	if _, deferred := store.deferred[one.ID]; !deferred {
		t.Fatal("a rate-limited want was not asked about again")
	}
}

func TestAPassStopsWhenTheDistributorIsRateLimiting(t *testing.T) {
	store := newStore(want("QZTAT2400123"), want("QZTAT2400124"), want("QZTAT2400125"))
	distributor, prints := workingParts()
	distributor.trackErr = deezer.ErrRateLimited

	more, err := newService(store, distributor, prints).Sweep(context.Background())
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if more {
		t.Error("a pass that stopped on rate limiting asked to come straight back")
	}
	if len(distributor.asked) != 1 {
		t.Fatalf("asked about %d wants after being refused; wanted 1",
			len(distributor.asked))
	}
}

// The distributor is still only ever asked by code. A want with no code goes to
// the upload search instead, which is the one place a text query is allowed
// (docs/decisions/0029).
func TestAWantWithNoISRCIsNeverAskedTheDistributorByName(t *testing.T) {
	one := want("")
	store := newStore(one)
	distributor, prints := workingParts()
	finder := &fakeFinder{available: true}

	service := newServiceFinding(store, distributor, finder, prints)
	if _, err := service.Sweep(context.Background()); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	if len(distributor.asked) != 0 {
		t.Fatalf("asked the distributor %v for a want with no ISRC", distributor.asked)
	}
	if len(finder.asked) != 1 || finder.asked[0] != "Vela Nine Watchfire" {
		t.Fatalf("searched for %v, wanted the want's artist and title", finder.asked)
	}
	if store.absent[one.ID] != noISRCToAskBy+" "+noUploadMatched {
		t.Errorf("recorded %q", store.absent[one.ID])
	}
}

// A want with no code and a want with a broken one are fixed by different
// things, so they are not told apart by the reader having to guess.
func TestAMalformedISRCSaysSoInItsOwnWords(t *testing.T) {
	one := want("not-a-code")
	store := newStore(one)
	distributor, prints := workingParts()
	distributor.trackErr = deezer.ErrNotAnISRC

	service := newServiceFinding(store, distributor, &fakeFinder{available: true}, prints)
	if _, err := service.Sweep(context.Background()); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	if store.absent[one.ID] != unusableISRC+" "+noUploadMatched {
		t.Errorf("recorded %q", store.absent[one.ID])
	}
}

func TestAPreviewTooShortToCompareIsRefused(t *testing.T) {
	one := want("QZTAT2400123")
	store := newStore(one)
	distributor, prints := workingParts()
	prints.value = shortFingerprint

	if _, err := newService(store, distributor, prints).Sweep(context.Background()); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	if len(store.recorded) != 0 {
		t.Fatal("a scrap of audio was kept as a reference")
	}
	if store.absent[one.ID] != unreadablePreview {
		t.Errorf("recorded %q", store.absent[one.ID])
	}
}

func TestNothingIsFetchedWithoutTheProgramThatMeasuresIt(t *testing.T) {
	store := newStore(want("QZTAT2400123"))
	distributor, prints := workingParts()
	prints.available = false

	_, err := newService(store, distributor, prints).Sweep(context.Background())
	if !errors.Is(err, chromaprint.ErrNoFpcalc) {
		t.Fatalf("sweep reported %v, wanted the missing program", err)
	}
	if len(distributor.asked) != 0 {
		t.Error("audio nothing could read was fetched from the distributor anyway")
	}
}

func TestAFullBatchAsksToComeStraightBack(t *testing.T) {
	due := make([]db.AnchorDueRow, sweepBatch)
	for index := range due {
		due[index] = want(fmt.Sprintf("QZTAT24001%02d", index))
	}
	store := newStore(due...)
	distributor, prints := workingParts()

	more, err := newService(store, distributor, prints).Sweep(context.Background())
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if !more {
		t.Fatal("a pass that filled its batch said there was nothing left")
	}
}

func TestTheWaitGrowsWithEveryFailedAttempt(t *testing.T) {
	one := want("QZTAT2400123")
	one.Attempts = 3
	store := newStore(one)
	distributor, prints := workingParts()
	distributor.trackErr = errors.New("could not reach Deezer")

	service := newService(store, distributor, prints)
	at := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return at }

	if _, err := service.Sweep(context.Background()); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	if got := store.deferred[one.ID]; !got.Equal(at.Add(retryLadder[3])) {
		t.Fatalf("asked again at %s, wanted %s", got, at.Add(retryLadder[3]))
	}
}

// The wants docs/decisions/0029 exists for: no ISRC anywhere, so the preview
// path has nothing and the upload search is the only thing that can speak.

func TestAWantWithNoISRCIsAnchoredToTheUploadItNames(t *testing.T) {
	one := want("")
	store := newStore(one)
	distributor, prints := workingParts()
	finder := &fakeFinder{
		available: true,
		uploads: []Upload{
			{ID: "wrong", Title: "Vela Nine - Watchfire (Kessler Remix)",
				Channel: "Kessler", Seconds: 305, Views: 40_000_000},
			{ID: "right", Title: "Vela Nine - Watchfire (Official Audio)",
				Channel: " Vela Nine - Topic ", Seconds: 306, Views: 1_200_000},
		},
		excerpt: []byte("audio"),
	}

	service := newServiceFinding(store, distributor, finder, prints)
	if _, err := service.Sweep(context.Background()); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	if len(store.recorded) != 1 {
		t.Fatalf("recorded %d anchors, wanted 1", len(store.recorded))
	}
	kept := store.recorded[0]
	if kept.Source != identity.AnchorByNameTopic || kept.Reference != "right" {
		t.Errorf("anchored to %s/%s", kept.Source, kept.Reference)
	}
	if kept.Label != "Vela Nine - Watchfire (Official Audio) — Vela Nine - Topic" {
		t.Errorf("kept the label %q", kept.Label)
	}
	if kept.Views != 1_200_000 {
		t.Errorf("kept %d views", kept.Views)
	}
	if finder.excerptSeconds != 306 {
		t.Errorf("excerpt duration = %d seconds, want upload duration 306", finder.excerptSeconds)
	}
	if len(finder.fetched) != 1 || finder.fetched[0] != "right" {
		t.Errorf("took an excerpt from %v", finder.fetched)
	}
	// An anchor landing is a witness the want's held copies were judged without.
	if len(store.judging) != 1 || store.judging[0] != one.ID {
		t.Errorf("asked to judge %v, wanted the want that was just anchored", store.judging)
	}
}

// The one thing that must not happen before the search is wired up: a want
// written off as having no anchor when nothing has looked for one.
func TestAWantWaitsWhereThereIsNoUploadSearch(t *testing.T) {
	for _, test := range []struct {
		name   string
		finder Finder
	}{
		{name: "no search configured at all", finder: nil},
		{name: "a search that cannot run", finder: &fakeFinder{available: false}},
	} {
		t.Run(test.name, func(t *testing.T) {
			one := want("")
			store := newStore(one)
			distributor, prints := workingParts()

			service := newServiceFinding(store, distributor, test.finder, prints)
			if _, err := service.Sweep(context.Background()); err != nil {
				t.Fatalf("sweep: %v", err)
			}

			if reason, absent := store.absent[one.ID]; absent {
				t.Fatalf("recorded %q for a want nothing looked for", reason)
			}
			if _, deferred := store.deferred[one.ID]; !deferred {
				t.Fatal("a want nothing looked for was not asked about again")
			}
		})
	}
}

// A want with no length cannot be keyed by the one rule that chooses an upload,
// and that is silence rather than a title match on its own.
func TestAWantWithNoLengthIsRecordedAsHavingNoUploadToKeyBy(t *testing.T) {
	one := want("")
	one.DurationMS = 0
	store := newStore(one)
	distributor, prints := workingParts()
	finder := &fakeFinder{
		available: true,
		uploads: []Upload{
			{ID: "exact", Title: "Vela Nine - Watchfire", Seconds: 305, Views: 900_000},
		},
	}

	service := newServiceFinding(store, distributor, finder, prints)
	if _, err := service.Sweep(context.Background()); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	if store.absent[one.ID] != noISRCToAskBy+" "+noLengthToKeyBy {
		t.Errorf("recorded %q", store.absent[one.ID])
	}
	if len(finder.asked) != 0 {
		t.Errorf("searched %v for a want with no length", finder.asked)
	}
	if len(store.recorded) != 0 {
		t.Error("a want with no length was anchored")
	}
}

// A search that could not run says nothing about the music, and a pass stops
// rather than spending every want's attempt on the same refusal.
func TestAPassStopsWhenTheUploadSearchIsRateLimiting(t *testing.T) {
	store := newStore(want(""), want(""), want(""))
	distributor, prints := workingParts()
	finder := &fakeFinder{available: true, searchErr: ErrFinderRateLimited}

	more, err := newServiceFinding(store, distributor, finder, prints).Sweep(context.Background())
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if more {
		t.Error("a pass that stopped on rate limiting asked to come straight back")
	}
	if len(finder.asked) != 1 {
		t.Fatalf("searched %d times after being refused; wanted 1", len(finder.asked))
	}
	if len(store.absent) != 0 {
		t.Error("a refused minute was written down as the want having no upload")
	}
}
