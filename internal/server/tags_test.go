package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/library"
	"github.com/rs/zerolog"
)

// fakeTagger stands in for the pass over the library's tags. What it decides is
// pinned in internal/library; here it is what the two routes ask.
type fakeTagger struct {
	status library.TagStatus
	queued int
	err    error
}

func (tagger *fakeTagger) QueueTagging(context.Context) (library.TagJobRow, error) {
	if tagger.err != nil {
		return library.TagJobRow{}, tagger.err
	}
	tagger.queued++
	return library.TagJobRow{ID: uuid.New(), Status: "queued"}, nil
}

func (tagger *fakeTagger) LatestTagging(context.Context) (library.TagStatus, error) {
	if tagger.err != nil {
		return library.TagStatus{}, tagger.err
	}
	return tagger.status, nil
}

func tagsHandler(options ...Option) http.Handler {
	return NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(), options...)
}

func TestTheLastPassOverTheTagsIsReadable(t *testing.T) {
	tagger := &fakeTagger{status: library.TagStatus{
		Status:   "completed",
		Run:      library.TagRun{Written: 12, Unchanged: 3, Skipped: 1},
		Eligible: 16,
	}}
	response := httptest.NewRecorder()

	tagsHandler(WithLibraryTagger(tagger)).ServeHTTP(
		response, httptest.NewRequest(http.MethodGet, "/api/v1/library/tags", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	var decoded libraryTagsResponse
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode %s: %v", response.Body, err)
	}
	if decoded.Written != 12 || decoded.Unchanged != 3 || decoded.Skipped != 1 ||
		decoded.Eligible != 16 || decoded.Status != "completed" {
		t.Fatalf("the pass reads as %+v", decoded)
	}
}

// A pass reports why it left files alone, not only how many, because "left
// alone" covers two silences and the page names the one that happened.
func TestThePassSaysWhyItLeftFilesAlone(t *testing.T) {
	tagger := &fakeTagger{status: library.TagStatus{
		Status: "completed",
		Run: library.TagRun{
			Skipped: 3, SkippedAmbiguous: 2, SkippedUnproved: 1,
		},
	}}
	response := httptest.NewRecorder()

	tagsHandler(WithLibraryTagger(tagger)).ServeHTTP(
		response, httptest.NewRequest(http.MethodGet, "/api/v1/library/tags", nil))

	var decoded libraryTagsResponse
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode %s: %v", response.Body, err)
	}
	if decoded.SkippedAmbiguous != 2 || decoded.SkippedUnproved != 1 {
		t.Fatalf("the pass reads as %+v", decoded)
	}
}

// The work is a job, so the answer is that it was accepted rather than that it
// is done: a library is tens of thousands of files and each one is copied to be
// written.
func TestAskingForAPassQueuesItAndAnswersAtOnce(t *testing.T) {
	tagger := &fakeTagger{}
	response := httptest.NewRecorder()

	tagsHandler(WithLibraryTagger(tagger)).ServeHTTP(
		response, httptest.NewRequest(http.MethodPost, "/api/v1/library/tags", nil))

	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if tagger.queued != 1 {
		t.Fatalf("%d passes were queued", tagger.queued)
	}
}

// An installation without a tagger says so rather than half-working.
func TestWritingTheTagsSaysSoWhenItIsNotConfigured(t *testing.T) {
	response := httptest.NewRecorder()

	tagsHandler().ServeHTTP(
		response, httptest.NewRequest(http.MethodPost, "/api/v1/library/tags", nil))

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
}

func TestAPassThatCouldNotBeReadIsAnError(t *testing.T) {
	response := httptest.NewRecorder()

	tagsHandler(WithLibraryTagger(&fakeTagger{err: errors.New("the database is gone")})).ServeHTTP(
		response, httptest.NewRequest(http.MethodGet, "/api/v1/library/tags", nil))

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
}
