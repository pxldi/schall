package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pxldi/schall/internal/identity"
	"github.com/rs/zerolog"
)

type fakeIdentityResolver struct {
	resolution  identity.FileResolution
	accepted    uuid.UUID
	localOnly   uuid.UUID
	cleared     uuid.UUID
	acceptError error
	clearError  error
	// fileError answers the lookup alone, so a file that is not there and a
	// decision that could not be recorded are different tests.
	fileError error
}

func (resolver *fakeIdentityResolver) File(context.Context, uuid.UUID) (identity.FileResolution, error) {
	return resolver.resolution, resolver.fileError
}

func (resolver *fakeIdentityResolver) Accept(_ context.Context, _ uuid.UUID, recordingID uuid.UUID) error {
	resolver.accepted = recordingID
	return resolver.acceptError
}

func (resolver *fakeIdentityResolver) MarkLocalOnly(_ context.Context, fileID uuid.UUID) error {
	resolver.localOnly = fileID
	return nil
}

func (resolver *fakeIdentityResolver) Clear(_ context.Context, fileID uuid.UUID) error {
	resolver.cleared = fileID
	return resolver.clearError
}

func TestFileIdentityIsReadBackWithItsCandidates(t *testing.T) {
	fileID, recordingID := uuid.New(), uuid.New()
	resolver := &fakeIdentityResolver{resolution: identity.FileResolution{
		FileID: fileID, Status: identity.StatusNeedsReview, MatchStatus: "unmatched",
		Summary: "2 recordings could be this file.",
		Candidates: []identity.Candidate{{
			RecordingID: recordingID, ArtistName: "Portishead", TrackTitle: "Roads",
			Rank: 1, Agrees: []string{"title"}, Differs: []string{"duration"},
			Summary: "Its title agrees, but its duration disagrees.",
		}},
	}}
	handler := NewAPI(
		&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithIdentityResolver(resolver),
	)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet, "/api/v1/library/files/"+fileID.String()+"/identity", nil,
	))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	var payload fileIdentityResponse
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.Status != identity.StatusNeedsReview || len(payload.Candidates) != 1 {
		t.Fatalf("payload = %+v", payload)
	}
	if payload.Candidates[0].RecordingID != recordingID || payload.Identity != nil {
		t.Errorf("candidate = %+v identity = %v", payload.Candidates[0], payload.Identity)
	}
}

func TestAcceptingACandidateRecordsTheDecision(t *testing.T) {
	fileID, recordingID := uuid.New(), uuid.New()
	resolver := &fakeIdentityResolver{}
	handler := NewAPI(
		&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithIdentityResolver(resolver),
	)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, "/api/v1/library/files/"+fileID.String()+"/identity",
		strings.NewReader(`{"recordingId":"`+recordingID.String()+`"}`),
	))
	if response.Code != http.StatusOK || resolver.accepted != recordingID {
		t.Fatalf("status = %d accepted = %s; body = %s", response.Code, resolver.accepted, response.Body)
	}
}

func TestLocalOnlyIsADecisionOfItsOwn(t *testing.T) {
	fileID := uuid.New()
	resolver := &fakeIdentityResolver{}
	handler := NewAPI(
		&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithIdentityResolver(resolver),
	)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, "/api/v1/library/files/"+fileID.String()+"/identity",
		strings.NewReader(`{"localOnly":true}`),
	))
	if response.Code != http.StatusOK || resolver.localOnly != fileID {
		t.Fatalf("status = %d local only = %s", response.Code, resolver.localOnly)
	}
}

// Asking for both answers at once, or for neither, is not an answer.
func TestAnIdentityDecisionMustSayWhichAnswer(t *testing.T) {
	fileID := uuid.New()
	handler := NewAPI(
		&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithIdentityResolver(&fakeIdentityResolver{}),
	)
	for _, body := range []string{`{}`, `{"recordingId":"` + uuid.New().String() + `","localOnly":true}`} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(
			http.MethodPost, "/api/v1/library/files/"+fileID.String()+"/identity",
			strings.NewReader(body),
		))
		if response.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: status = %d, want 422", body, response.Code)
		}
	}
}

func TestAcceptingAnUnofferedRecordingIsRefused(t *testing.T) {
	fileID := uuid.New()
	handler := NewAPI(
		&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithIdentityResolver(&fakeIdentityResolver{acceptError: identity.ErrUnknownCandidate}),
	)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, "/api/v1/library/files/"+fileID.String()+"/identity",
		strings.NewReader(`{"recordingId":"`+uuid.New().String()+`"}`),
	))
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
}

// Withdrawing a decision leaves a file nobody has answered for, so the question
// goes straight back to the providers.
func TestWithdrawingADecisionAsksAgain(t *testing.T) {
	fileID := uuid.New()
	store := &fakeStore{}
	resolver := &fakeIdentityResolver{}
	handler := NewAPI(
		store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithIdentityResolver(resolver),
	)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodDelete, "/api/v1/library/files/"+fileID.String()+"/identity", nil,
	))
	if response.Code != http.StatusOK || resolver.cleared != fileID {
		t.Fatalf("status = %d cleared = %s", response.Code, resolver.cleared)
	}
	if store.resolvedFileID != fileID {
		t.Errorf("queued file = %s, want the file to be asked about again", store.resolvedFileID)
	}
}

func TestWithdrawingNothingIsNotFound(t *testing.T) {
	handler := NewAPI(
		&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithIdentityResolver(&fakeIdentityResolver{clearError: identity.ErrNoDecision}),
	)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodDelete, "/api/v1/library/files/"+uuid.New().String()+"/identity", nil,
	))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d", response.Code)
	}
}

func TestResolvingOnRequestQueuesTheFile(t *testing.T) {
	fileID := uuid.New()
	store := &fakeStore{}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, "/api/v1/library/files/"+fileID.String()+"/resolve", nil,
	))
	if response.Code != http.StatusAccepted || store.resolvedFileID != fileID {
		t.Fatalf("status = %d queued = %s; body = %s", response.Code, store.resolvedFileID, response.Body)
	}
}

// Without a resolver configured the library still works; it simply cannot
// answer what a file is.
func TestIdentityEndpointsReportBeingUnavailable(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())
	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/api/v1/library/files/"+uuid.NewString()+"/identity", nil),
		httptest.NewRequest(http.MethodPost, "/api/v1/library/files/"+uuid.NewString()+"/identity",
			strings.NewReader(`{}`)),
		httptest.NewRequest(http.MethodDelete, "/api/v1/library/files/"+uuid.NewString()+"/identity", nil),
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s: status = %d, want 503", request.Method, request.URL.Path, response.Code)
		}
	}
}

// The decision a person made is carried back with the evidence behind it,
// because that is what makes it reviewable rather than merely recorded.
func TestARecordedIdentityIsReadBackWithItsEvidence(t *testing.T) {
	fileID, recordingID := uuid.New(), uuid.New()
	resolver := &fakeIdentityResolver{resolution: identity.FileResolution{
		FileID: fileID, Status: identity.StatusResolved, MatchStatus: "matched",
		Identity: &identity.StoredIdentity{
			Identity: identity.Identity{
				RecordingID: recordingID, ArtistName: "Portishead", TrackTitle: "Roads",
				Method: "recording_id", Confidence: 1,
				Summary:  "Its MusicBrainz recording ID names this recording.",
				Evidence: []string{"recording id"},
			},
			Kind: "recording", IsManual: true,
		},
	}}
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithIdentityResolver(resolver))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/library/files/"+fileID.String()+"/identity", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	var payload fileIdentityResponse
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Identity == nil || payload.Identity.RecordingID == nil ||
		*payload.Identity.RecordingID != recordingID {
		t.Fatalf("identity = %+v", payload.Identity)
	}
	if !payload.Identity.Manual || len(payload.Identity.Evidence) != 1 {
		t.Fatalf("identity = %+v, want the manual decision and its evidence", payload.Identity)
	}
}

func TestReadingAnIdentityForAFileThatIsNotThereIsNotFound(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithIdentityResolver(&fakeIdentityResolver{fileError: pgx.ErrNoRows}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/library/files/"+uuid.NewString()+"/identity", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
}

func TestReadingAnIdentityReportsAStoreThatFailed(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithIdentityResolver(&fakeIdentityResolver{
			fileError: errors.New("the database is unreachable"),
		}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/library/files/"+uuid.NewString()+"/identity", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

func TestAnIdentityDecisionRejectsABodyThatIsNotJSON(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithIdentityResolver(&fakeIdentityResolver{}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/library/files/"+uuid.NewString()+"/identity", strings.NewReader(`not json`)))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.Code)
	}
}

func TestDecidingAboutAFileThatIsNotThereIsNotFound(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithIdentityResolver(&fakeIdentityResolver{acceptError: pgx.ErrNoRows}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/library/files/"+uuid.NewString()+"/identity",
		strings.NewReader(`{"recordingId":"`+uuid.NewString()+`"}`)))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
}

func TestRecordingADecisionReportsAStoreThatFailed(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithIdentityResolver(&fakeIdentityResolver{
			acceptError: errors.New("the database is unreachable"),
		}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/library/files/"+uuid.NewString()+"/identity",
		strings.NewReader(`{"recordingId":"`+uuid.NewString()+`"}`)))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

func TestWithdrawingADecisionForAFileThatIsNotThereIsNotFound(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithIdentityResolver(&fakeIdentityResolver{clearError: pgx.ErrNoRows}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodDelete,
		"/api/v1/library/files/"+uuid.NewString()+"/identity", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
}

func TestWithdrawingADecisionReportsAStoreThatFailed(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithIdentityResolver(&fakeIdentityResolver{
			clearError: errors.New("the database is unreachable"),
		}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodDelete,
		"/api/v1/library/files/"+uuid.NewString()+"/identity", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

// Withdrawing puts the question back to the providers, so a resolution that
// could not be queued is reported rather than answered as though it had been.
func TestWithdrawingReportsAResolutionThatCouldNotBeQueued(t *testing.T) {
	store := &fakeStore{err: errors.New("the database is unreachable")}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithIdentityResolver(&fakeIdentityResolver{}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodDelete,
		"/api/v1/library/files/"+uuid.NewString()+"/identity", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

// Reading the decision back is what the response is; a read that failed is
// reported rather than answered with an empty resolution.
func TestReadingBackADecisionReportsAStoreThatFailed(t *testing.T) {
	resolver := &readBackFailure{}
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithIdentityResolver(resolver))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/library/files/"+uuid.NewString()+"/identity",
		strings.NewReader(`{"localOnly":true}`)))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

// readBackFailure records a decision and then cannot read it back, which is the
// one ordering the read-back path exists for.
type readBackFailure struct {
	fakeIdentityResolver
	decided bool
}

func (resolver *readBackFailure) File(context.Context, uuid.UUID) (identity.FileResolution, error) {
	if resolver.decided {
		return identity.FileResolution{}, errors.New("the database is unreachable")
	}
	return identity.FileResolution{}, nil
}

func (resolver *readBackFailure) MarkLocalOnly(_ context.Context, fileID uuid.UUID) error {
	resolver.localOnly, resolver.decided = fileID, true
	return nil
}

// The interface shows a person what identified their file, so the answer has to
// carry the words. It keeps the short name as well, which is what code reads.
func TestAResolvedIdentityIsReadBackInWords(t *testing.T) {
	fileID := uuid.New()
	resolver := &fakeIdentityResolver{resolution: identity.FileResolution{
		FileID: fileID, Status: identity.StatusResolved, MatchStatus: "matched",
		Identity: &identity.StoredIdentity{
			Identity: identity.Identity{
				RecordingID: uuid.New(), ArtistName: "Portishead", TrackTitle: "Roads",
				Method: "fingerprint-and-identifier", Confidence: 1, Evidence: []string{"audio"},
			},
			Kind: "external",
		},
	}}
	handler := NewAPI(
		&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithIdentityResolver(resolver),
	)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet, "/api/v1/library/files/"+fileID.String()+"/identity", nil,
	))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	var payload fileIdentityResponse
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.Identity == nil {
		t.Fatalf("payload = %+v, want the identity read back", payload)
	}
	if payload.Identity.Method != "fingerprint-and-identifier" {
		t.Errorf("method = %q, want the name code reads", payload.Identity.Method)
	}
	if payload.Identity.MethodText != identity.MethodSentence("fingerprint-and-identifier") {
		t.Errorf("methodText = %q, want the sentence a person reads", payload.Identity.MethodText)
	}
}
