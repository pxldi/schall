package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/listenbrainz"
	"github.com/pxldi/schall/internal/recommendations"
	"github.com/rs/zerolog"
)

type fakeSourceStore struct {
	*fakeStore
	settings   db.ListenBrainzSettingsRow
	configured bool
	saved      db.SaveListenBrainzSettingsParams
	// sweepQueuedFor is when the save asked for a recommendation pass; zero when
	// it asked for none.
	sweepQueuedFor time.Time
	// Each of these breaks one query on its own, so a store with nothing stored
	// and one that could not be read are different tests.
	settingsErr error
	saveErr     error
	sweepErr    error
}

func (store *fakeSourceStore) QueueRecommendationSweep(_ context.Context, at time.Time) error {
	if store.sweepErr != nil {
		return store.sweepErr
	}
	store.sweepQueuedFor = at
	return nil
}

func (store *fakeSourceStore) ListenBrainzSettings(context.Context) (db.ListenBrainzSettingsRow, error) {
	if store.settingsErr != nil {
		return db.ListenBrainzSettingsRow{}, store.settingsErr
	}
	if !store.configured {
		return db.ListenBrainzSettingsRow{}, pgx.ErrNoRows
	}
	return store.settings, nil
}

func (store *fakeSourceStore) SaveListenBrainzSettings(
	_ context.Context, params db.SaveListenBrainzSettingsParams,
) (db.ListenBrainzSettingsRow, error) {
	if store.saveErr != nil {
		return db.ListenBrainzSettingsRow{}, store.saveErr
	}
	store.saved = params

	token := store.settings.UserToken
	switch {
	case params.ClearUserToken:
		token = pgtype.Text{}
	case params.UserToken != "":
		token = pgtype.Text{String: params.UserToken, Valid: true}
	}

	store.configured = true
	// The addresses are not the save's to move: they stay whatever the row
	// already held, as the column defaults and the ON CONFLICT clause leave them.
	store.settings = db.ListenBrainzSettingsRow{
		BaseURL: listenbrainz.DefaultBaseURL, LabsURL: listenbrainz.DefaultLabsURL,
		Username: params.Username, UserToken: token,
		SimilarityAlgorithm: params.SimilarityAlgorithm,
		Enabled:             params.Enabled, ConnectionStatus: "unknown",
	}
	return store.settings, nil
}

func configuredSource() db.ListenBrainzSettingsRow {
	return db.ListenBrainzSettingsRow{
		BaseURL:             listenbrainz.DefaultBaseURL,
		LabsURL:             listenbrainz.DefaultLabsURL,
		Username:            "listener",
		UserToken:           pgtype.Text{String: "source-token", Valid: true},
		SimilarityAlgorithm: "session_based",
		Enabled:             true,
		ConnectionStatus:    "unknown",
	}
}

// fakeRecommendationSource stands in for the service layer. The handler must
// not build a client of its own, so this is the only way the route can answer.
type fakeRecommendationSource struct {
	row db.ListenBrainzSettingsRow
	err error
}

func (source *fakeRecommendationSource) CheckConnection(context.Context) (db.ListenBrainzSettingsRow, error) {
	if source.err != nil {
		return db.ListenBrainzSettingsRow{}, source.err
	}
	return source.row, nil
}

func TestTheSourceTokenIsNeverReturnedToThePage(t *testing.T) {
	store := &fakeSourceStore{fakeStore: &fakeStore{}, configured: true, settings: configuredSource()}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/settings/listenbrainz", nil))

	if strings.Contains(response.Body.String(), "source-token") {
		t.Fatalf("response leaked the token: %s", response.Body)
	}
	var payload listenBrainzSettingsResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if !payload.Configured || !payload.UserTokenSet || payload.Username != "listener" {
		t.Fatalf("payload = %#v", payload)
	}
}

// The service is hosted, so an installation that has never opened this section
// is shown the addresses already filled in rather than two empty boxes.
func TestAnUnconfiguredSourceAnswersWithTheHostedDefaults(t *testing.T) {
	store := &fakeSourceStore{fakeStore: &fakeStore{}}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/settings/listenbrainz", nil))

	var payload listenBrainzSettingsResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Configured {
		t.Fatal("an installation with no settings row reported itself configured")
	}
	if payload.BaseURL != listenbrainz.DefaultBaseURL || payload.LabsURL != listenbrainz.DefaultLabsURL {
		t.Fatalf("payload = %#v, want the hosted addresses", payload)
	}
	if payload.SimilarityAlgorithm == "" {
		t.Error("no similarity algorithm was suggested")
	}
}

// The addresses have a right answer, so a save that names only the account is
// complete rather than half-filled.
func TestSavingTheSourceNeedsOnlyTheAccount(t *testing.T) {
	store := &fakeSourceStore{fakeStore: &fakeStore{}}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodPut, "/api/v1/settings/listenbrainz",
		strings.NewReader(`{"username":"listener","enabled":true}`)))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if store.saved.Username != "listener" {
		t.Errorf("saved username = %q", store.saved.Username)
	}
	if store.saved.SimilarityAlgorithm == "" {
		t.Error("saved an empty similarity algorithm")
	}
}

// Connecting the account is the moment there is something to ask, and the sweep
// cannot schedule its own first pass. Without this, an installation that had
// just been told which account to read stayed silent until the process bounced.
func TestConnectingTheSourceAsksForARecommendationPass(t *testing.T) {
	store := &fakeSourceStore{fakeStore: &fakeStore{}}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodPut, "/api/v1/settings/listenbrainz",
		strings.NewReader(`{"username":"listener","enabled":true}`)))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if store.sweepQueuedFor.IsZero() {
		t.Fatal("saving an enabled account queued no recommendation pass")
	}
}

// A source that is switched off is not asked. The pass would refuse anyway, and
// it would refuse permanently — leaving a failed job to say nothing more than
// that nobody wanted one.
func TestSavingTheSourceSwitchedOffAsksForNothing(t *testing.T) {
	store := &fakeSourceStore{fakeStore: &fakeStore{}}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodPut, "/api/v1/settings/listenbrainz",
		strings.NewReader(`{"username":"listener","enabled":false}`)))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if !store.sweepQueuedFor.IsZero() {
		t.Fatalf("queued a pass for a disabled source at %s", store.sweepQueuedFor)
	}
}

// The account is stored either way. A queue that would not take the pass is a
// line in the log, not a save the user has to make twice.
func TestAnAccountIsSavedEvenWhenThePassCannotBeQueued(t *testing.T) {
	store := &fakeSourceStore{fakeStore: &fakeStore{}, sweepErr: errors.New("queue is down")}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodPut, "/api/v1/settings/listenbrainz",
		strings.NewReader(`{"username":"listener","enabled":true}`)))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if store.saved.Username != "listener" {
		t.Fatalf("saved = %#v, want the account stored", store.saved)
	}
}

// The regression this exists for: a caller that could name the host could point
// it at one of its own, omit the token so the save keeps the stored one, and
// read the credential off the Authorization header the connection check sends.
// The write path does not accept an address, so the attempt changes nothing.
func TestASaveCannotMoveTheHostTheTokenIsSentTo(t *testing.T) {
	store := &fakeSourceStore{fakeStore: &fakeStore{}, configured: true, settings: configuredSource()}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodPut, "/api/v1/settings/listenbrainz",
		strings.NewReader(`{"baseUrl":"http://attacker.example","labsUrl":"http://attacker.example",`+
			`"username":"listener","enabled":true}`)))

	// The field is not on the request at all, and the decoder refuses what it
	// does not recognise — so the attempt is turned away rather than quietly
	// dropped, which is what makes it visible to whoever tried it.
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", response.Code, response.Body)
	}
	if store.settings.BaseURL != listenbrainz.DefaultBaseURL {
		t.Fatalf("base URL = %q, want the stored host untouched", store.settings.BaseURL)
	}
	if store.settings.LabsURL != listenbrainz.DefaultLabsURL {
		t.Fatalf("labs URL = %q, want the stored host untouched", store.settings.LabsURL)
	}
}

// The account is the one thing that has no right answer, so it is the one thing
// that is required.
func TestSavingTheSourceWithoutAnAccountIsRefused(t *testing.T) {
	store := &fakeSourceStore{fakeStore: &fakeStore{}}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodPut, "/api/v1/settings/listenbrainz",
		strings.NewReader(`{"username":"  ","enabled":true}`)))

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body = %s", response.Code, response.Body)
	}
}

// The page is never shown the token, so an empty one means "leave it alone"
// rather than "erase it".
func TestSavingTheSourceWithAnEmptyTokenLeavesTheStoredOneAlone(t *testing.T) {
	store := &fakeSourceStore{fakeStore: &fakeStore{}, configured: true, settings: configuredSource()}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodPut, "/api/v1/settings/listenbrainz",
		strings.NewReader(`{"username":"listener","userToken":"","enabled":true}`)))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if store.saved.UserToken != "" || store.saved.ClearUserToken {
		t.Fatalf("saved = %#v, want the stored token untouched", store.saved)
	}
	if !store.settings.UserToken.Valid {
		t.Error("the stored token was removed by a save that never mentioned it")
	}
}

// The token is optional, so a stored one has to be removable. Asking for it
// explicitly is the only spelling that is not also "leave it alone".
func TestClearingTheSourceTokenRemovesIt(t *testing.T) {
	store := &fakeSourceStore{fakeStore: &fakeStore{}, configured: true, settings: configuredSource()}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodPut, "/api/v1/settings/listenbrainz",
		strings.NewReader(`{"username":"listener","clearUserToken":true,"enabled":true}`)))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if !store.saved.ClearUserToken {
		t.Fatal("the request to remove the token did not reach the store")
	}
	if store.settings.UserToken.Valid {
		t.Error("the token survived a request to remove it")
	}
}

// A verdict about an account that is no longer configured is not shown as
// though it were about the one that is. The caller is told to test again.
func TestACheckOvertakenByASaveIsReportedRatherThanShown(t *testing.T) {
	store := &fakeSourceStore{fakeStore: &fakeStore{}, configured: true, settings: configuredSource()}
	source := &fakeRecommendationSource{err: recommendations.ErrCheckSuperseded}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithRecommendationSource(source))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, "/api/v1/settings/listenbrainz/test", nil))

	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body = %s", response.Code, response.Body)
	}
}

func TestTestingTheSourceReportsWhatTheCheckFound(t *testing.T) {
	checked := configuredSource()
	checked.ConnectionStatus = "ok"
	checked.ConnectionDetail = pgtype.Text{String: "1000 recommendations for listener", Valid: true}
	store := &fakeSourceStore{fakeStore: &fakeStore{}, configured: true, settings: configuredSource()}
	source := &fakeRecommendationSource{row: checked}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithRecommendationSource(source))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, "/api/v1/settings/listenbrainz/test", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	var payload listenBrainzSettingsResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.ConnectionStatus != "ok" || payload.ConnectionDetail == nil {
		t.Fatalf("payload = %#v", payload)
	}
}

// Testing a source nobody configured is the caller's mistake, not a fault.
func TestTestingASourceNobodyConfiguredIsRefused(t *testing.T) {
	store := &fakeSourceStore{fakeStore: &fakeStore{}}
	source := &fakeRecommendationSource{err: recommendations.ErrNotConfigured}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithRecommendationSource(source))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, "/api/v1/settings/listenbrainz/test", nil))

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body = %s", response.Code, response.Body)
	}
}

// The settings can be read and saved on an installation that cannot check them.
// Reporting the check unavailable is a different thing from reporting a failure.
func TestTestingWithoutASourceServiceReportsItselfUnavailable(t *testing.T) {
	store := &fakeSourceStore{fakeStore: &fakeStore{}, configured: true, settings: configuredSource()}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, "/api/v1/settings/listenbrainz/test", nil))

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body = %s", response.Code, response.Body)
	}
}

// A store that could not be read is a fault, and is not the same answer as a
// store with nothing in it.
func TestASettingsReadThatFailedIsNotAnUnconfiguredSource(t *testing.T) {
	store := &fakeSourceStore{fakeStore: &fakeStore{}, settingsErr: errors.New("connection refused")}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/settings/listenbrainz", nil))

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body = %s", response.Code, response.Body)
	}
}
