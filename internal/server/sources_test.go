package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/events"
	"github.com/pxldi/schall/internal/sources"
	"github.com/rs/zerolog"
)

// albumUUID is any well-formed release identifier; the fake store ignores it.
const albumUUID = "0f6d4a4c-0f4e-4a1e-9b8f-2c1a6f0d5e77"

type fakeSettingsStore struct {
	*fakeStore
	settings       db.SlskdSettingsRow
	configured     bool
	saved          db.SaveSlskdSettingsParams
	recordedStatus string
	recordedDetail string
	recordedError  string
	imports        db.ImportSettingsRow
	// Each of these breaks one settings query on its own, so a store that
	// cannot be read and one that has nothing stored are different tests.
	settingsErr    error
	saveSlskdErr   error
	recordErr      error
	importsErr     error
	saveImportsErr error
}

func (store *fakeSettingsStore) SlskdSettings(context.Context) (db.SlskdSettingsRow, error) {
	if store.settingsErr != nil {
		return db.SlskdSettingsRow{}, store.settingsErr
	}
	if !store.configured {
		return db.SlskdSettingsRow{}, pgx.ErrNoRows
	}
	return store.settings, nil
}

func (store *fakeSettingsStore) SaveSlskdSettings(_ context.Context, params db.SaveSlskdSettingsParams) (db.SlskdSettingsRow, error) {
	if store.saveSlskdErr != nil {
		return db.SlskdSettingsRow{}, store.saveSlskdErr
	}
	store.saved = params
	store.configured = true
	store.settings = db.SlskdSettingsRow{
		BaseURL: params.BaseURL, APIKey: params.APIKey, Enabled: params.Enabled,
		SearchTimeoutSeconds: params.SearchTimeoutSeconds, ConnectionStatus: "unknown",
	}
	return store.settings, nil
}

func (store *fakeSettingsStore) RecordSlskdConnection(_ context.Context, status, detail, failure string) (db.SlskdSettingsRow, error) {
	if store.recordErr != nil {
		return db.SlskdSettingsRow{}, store.recordErr
	}
	store.recordedStatus, store.recordedDetail, store.recordedError = status, detail, failure
	store.settings.ConnectionStatus = status
	store.settings.ConnectionDetail = pgtype.Text{String: detail, Valid: detail != ""}
	store.settings.ConnectionError = pgtype.Text{String: failure, Valid: failure != ""}
	return store.settings, nil
}

func (store *fakeSettingsStore) ImportSettings(context.Context) (db.ImportSettingsRow, error) {
	if store.importsErr != nil {
		return db.ImportSettingsRow{}, store.importsErr
	}
	if store.imports.SourceRetention == "" {
		return db.DefaultImportSettings(), nil
	}
	return store.imports, nil
}

func (store *fakeSettingsStore) SaveImportSettings(_ context.Context, params db.SaveImportSettingsParams) (db.ImportSettingsRow, error) {
	if store.saveImportsErr != nil {
		return db.ImportSettingsRow{}, store.saveImportsErr
	}
	key := params.AcoustIDAPIKey
	if key == "" {
		key = store.imports.AcoustIDAPIKey
	}
	// Mirrors the real store's coalesce: nil (or, for the strings, empty)
	// keeps whatever is already stored, falling back to the column defaults
	// when nothing is stored yet, exactly the way SaveImportSettings's own SQL
	// does.
	defaults := db.DefaultImportSettings()
	transcodeEnabled := store.imports.TranscodeEnabled
	if params.TranscodeEnabled != nil {
		transcodeEnabled = *params.TranscodeEnabled
	}
	target := store.imports.TranscodeTarget
	if target == "" {
		target = defaults.TranscodeTarget
	}
	if params.TranscodeTarget != "" {
		target = params.TranscodeTarget
	}
	bitrate := store.imports.TranscodeBitrate
	if bitrate == "" {
		bitrate = defaults.TranscodeBitrate
	}
	if params.TranscodeBitrate != "" {
		bitrate = params.TranscodeBitrate
	}
	when := store.imports.TranscodeWhen
	if when == "" {
		when = defaults.TranscodeWhen
	}
	if params.TranscodeWhen != "" {
		when = params.TranscodeWhen
	}
	store.imports = db.ImportSettingsRow{
		SourceRetention:  params.SourceRetention,
		AcoustIDAPIKey:   key,
		AcoustIDEnabled:  params.AcoustIDEnabled,
		TranscodeEnabled: transcodeEnabled,
		TranscodeTarget:  target,
		TranscodeBitrate: bitrate,
		TranscodeWhen:    when,
	}
	return store.imports, nil
}

type fakeProvider struct {
	settings   sources.Settings
	candidates []sources.Candidate
	query      sources.Query
	queries    []sources.Query
	results    map[string][]sources.Candidate
	status     sources.Status
	checkErr   error
	searchErr  error
	buildErr   error
	// firstSearchErr, when set, is returned by the first search only, so a test
	// can show a refusal that asking again gets past.
	firstSearchErr error
}

func (provider *fakeProvider) Build(settings sources.Settings) (sources.Provider, error) {
	provider.settings = settings
	if provider.buildErr != nil {
		return nil, provider.buildErr
	}
	return provider, nil
}

func (provider *fakeProvider) Name() string { return "fake" }

func (provider *fakeProvider) CheckConnection(context.Context) (sources.Status, error) {
	return provider.status, provider.checkErr
}

func (provider *fakeProvider) SearchBudget() time.Duration { return 30 * time.Second }

func (provider *fakeProvider) Search(_ context.Context, query sources.Query) ([]sources.Candidate, error) {
	provider.query = query
	provider.queries = append(provider.queries, query)
	if provider.firstSearchErr != nil && len(provider.queries) == 1 {
		return nil, provider.firstSearchErr
	}
	if provider.results != nil {
		return provider.results[query.Text], provider.searchErr
	}
	return provider.candidates, provider.searchErr
}

func configuredSettings() db.SlskdSettingsRow {
	return db.SlskdSettingsRow{
		BaseURL: "http://slskd:5030", APIKey: "secret-key", Enabled: true,
		SearchTimeoutSeconds: 30, ConnectionStatus: "ok",
	}
}

func TestGetSlskdSettingsReportsUnconfiguredState(t *testing.T) {
	store := &fakeSettingsStore{fakeStore: &fakeStore{}}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/settings/slskd", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}

	var payload slskdSettingsResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Configured || payload.APIKeySet || payload.ConnectionStatus != "unknown" {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestGetSlskdSettingsNeverReturnsTheAPIKey(t *testing.T) {
	store := &fakeSettingsStore{fakeStore: &fakeStore{}, configured: true, settings: configuredSettings()}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/settings/slskd", nil))
	if strings.Contains(response.Body.String(), "secret-key") {
		t.Fatalf("response leaked the API key: %s", response.Body)
	}

	var payload slskdSettingsResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if !payload.Configured || !payload.APIKeySet || payload.BaseURL != "http://slskd:5030" {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestSaveSlskdSettingsValidatesInput(t *testing.T) {
	store := &fakeSettingsStore{fakeStore: &fakeStore{}}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	for name, body := range map[string]string{
		"missing base URL": `{"baseUrl":"","apiKey":"k","enabled":true,"searchTimeoutSeconds":20}`,
		"relative URL":     `{"baseUrl":"slskd:5030","apiKey":"k","enabled":true,"searchTimeoutSeconds":20}`,
		"wrong scheme":     `{"baseUrl":"ftp://slskd","apiKey":"k","enabled":true,"searchTimeoutSeconds":20}`,
		"timeout too low":  `{"baseUrl":"http://slskd:5030","apiKey":"k","enabled":true,"searchTimeoutSeconds":1}`,
		"missing key":      `{"baseUrl":"http://slskd:5030","apiKey":"","enabled":true,"searchTimeoutSeconds":20}`,
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(
			http.MethodPut, "/api/v1/settings/slskd", strings.NewReader(body)))
		if response.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s: status = %d; body = %s", name, response.Code, response.Body)
		}
	}
}

func TestSaveSlskdSettingsKeepsStoredKeyWhenOmitted(t *testing.T) {
	store := &fakeSettingsStore{fakeStore: &fakeStore{}, configured: true, settings: configuredSettings()}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/api/v1/settings/slskd", strings.NewReader(
		`{"baseUrl":"http://slskd:5030/","apiKey":"","enabled":false,"searchTimeoutSeconds":45}`)))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if store.saved.APIKey != "" {
		t.Fatalf("saved key = %q; an omitted key must stay untouched in the store", store.saved.APIKey)
	}
	if store.saved.BaseURL != "http://slskd:5030" {
		t.Fatalf("saved base URL = %q, want the trailing slash trimmed", store.saved.BaseURL)
	}
	if store.saved.Enabled || store.saved.SearchTimeoutSeconds != 45 {
		t.Fatalf("saved = %#v", store.saved)
	}
}

func TestTestSlskdConnectionRecordsSuccess(t *testing.T) {
	store := &fakeSettingsStore{fakeStore: &fakeStore{}, configured: true, settings: configuredSettings()}
	provider := &fakeProvider{status: sources.Status{Detail: "slskd 0.22.1, Soulseek Connected, LoggedIn"}}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(), WithSourceProvider(provider))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/settings/slskd/test", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if store.recordedStatus != "ok" || store.recordedError != "" {
		t.Fatalf("recorded %q / %q", store.recordedStatus, store.recordedError)
	}
	if provider.settings.APIKey != "secret-key" || provider.settings.SearchTimeout.Seconds() != 30 {
		t.Fatalf("provider built from %#v", provider.settings)
	}
}

func TestTestSlskdConnectionRecordsActionableFailure(t *testing.T) {
	store := &fakeSettingsStore{fakeStore: &fakeStore{}, configured: true, settings: configuredSettings()}
	provider := &fakeProvider{checkErr: errors.New("slskd rejected the API key")}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(), WithSourceProvider(provider))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/settings/slskd/test", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if store.recordedStatus != "failed" || !strings.Contains(store.recordedError, "API key") {
		t.Fatalf("recorded %q / %q", store.recordedStatus, store.recordedError)
	}
}

func TestTestSlskdConnectionRequiresConfiguration(t *testing.T) {
	store := &fakeSettingsStore{fakeStore: &fakeStore{}}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(), WithSourceProvider(&fakeProvider{}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/settings/slskd/test", nil))
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
}

func TestListAlbumSourcesRanksCandidatesFromTheCatalogueQuery(t *testing.T) {
	album := db.GetAlbumRow{ArtistName: "Portishead", Title: "Dummy", TrackCount: 11}
	store := &fakeSettingsStore{
		fakeStore:  &fakeStore{album: album},
		configured: true, settings: configuredSettings(),
	}
	candidate := sources.Candidate{
		Provider: "fake", Username: "peer", Directory: "Music/Dummy",
		Files:          []sources.File{{Name: "01.flac", Extension: "flac", SizeBytes: 1 << 20}},
		Format:         "flac",
		FreeUploadSlot: true, Score: 0.9, Reasons: []string{"Lossless FLAC"},
	}
	provider := &fakeProvider{candidates: []sources.Candidate{candidate}}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(), WithSourceProvider(provider))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet, "/api/v1/albums/"+albumUUID+"/sources", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if provider.query.Text != "Portishead Dummy" || provider.query.ExpectedTrackCount != 11 {
		t.Fatalf("query = %#v", provider.query)
	}

	var payload struct {
		Query string                    `json:"query"`
		Items []sourceCandidateResponse `json:"items"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Items) != 1 || payload.Items[0].TrackCount != 1 {
		t.Fatalf("items = %#v", payload.Items)
	}
	if payload.Items[0].Reasons[0] != "Lossless FLAC" || payload.Items[0].Files[0].Name != "01.flac" {
		t.Fatalf("candidate = %#v", payload.Items[0])
	}
}

// Without the catalogue's own tracks a search can only report how good a copy
// is, never whether it holds the release that was asked for.
func TestListAlbumSourcesSendsTheCatalogueTracksToBeCorroboratedAgainst(t *testing.T) {
	store := &fakeSettingsStore{
		fakeStore: &fakeStore{
			album: db.GetAlbumRow{ArtistName: "Portishead", Title: "Dummy", TrackCount: 2},
			tracks: []db.ListAlbumTracksRow{
				{Title: "Mysterons", DurationMs: pgtype.Int4{Int32: 205_000, Valid: true}},
				{Title: "Sour Times", DurationMs: pgtype.Int4{Int32: 254_000, Valid: true}},
			},
		},
		configured: true, settings: configuredSettings(),
	}
	provider := &fakeProvider{}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(), WithSourceProvider(provider))

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(
		http.MethodGet, "/api/v1/albums/"+albumUUID+"/sources", nil))

	if len(provider.query.Tracks) != 2 {
		t.Fatalf("query carried %d tracks, want both catalogue tracks", len(provider.query.Tracks))
	}
	if provider.query.Tracks[0].Title != "Mysterons" ||
		provider.query.Tracks[0].DurationSeconds != 205 {
		t.Fatalf("first track = %#v, want the title and length in seconds", provider.query.Tracks[0])
	}
}

func TestListAlbumSourcesReportsWhatCorroboratedACandidate(t *testing.T) {
	store := &fakeSettingsStore{
		fakeStore:  &fakeStore{album: db.GetAlbumRow{ArtistName: "Portishead", Title: "Dummy", TrackCount: 1}},
		configured: true, settings: configuredSettings(),
	}
	provider := &fakeProvider{candidates: []sources.Candidate{{
		Provider: "fake", Username: "peer", Directory: "Music/Dummy",
		Files:  []sources.File{{Name: "01 Mysterons.flac", Extension: "flac", SizeBytes: 1 << 20}},
		Format: "flac", Score: 0.9,
		Match: sources.Match{Expected: 1, Confirmed: 1},
	}}}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(), WithSourceProvider(provider))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet, "/api/v1/albums/"+albumUUID+"/sources", nil))

	var payload struct {
		Items []sourceCandidateResponse `json:"items"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Items) != 1 {
		t.Fatalf("items = %#v", payload.Items)
	}
	match := payload.Items[0].Match
	if !match.Checked || !match.Complete || match.Confirmed != 1 {
		t.Fatalf("match = %#v, want a fully corroborated candidate", match)
	}
	if match.Summary == "" {
		t.Fatal("match carries no summary for the interface to show")
	}
}

// noRateLimitPause removes the wait before an interactive search asks again.
// The wait is there for a real provider; a test waiting it out proves nothing.
func noRateLimitPause(t *testing.T) {
	t.Helper()
	original := rateLimitPause
	rateLimitPause = 0
	t.Cleanup(func() { rateLimitPause = original })
}

// Being asked to slow down is not a failed search: three concurrent searches
// were enough to trip slskd's limit, and reporting it as a failure told the
// user to do exactly what the handler could have done itself.
func TestListAlbumSourcesAsksAgainWhenTheSourceIsRateLimiting(t *testing.T) {
	noRateLimitPause(t)
	store := &fakeSettingsStore{
		fakeStore:  &fakeStore{album: db.GetAlbumRow{ArtistName: "Portishead", Title: "Dummy", TrackCount: 1}},
		configured: true, settings: configuredSettings(),
	}
	provider := &fakeProvider{
		firstSearchErr: fmt.Errorf("%w: slskd is busy", sources.ErrRateLimited),
		candidates: []sources.Candidate{{
			Provider: "fake", Username: "peer", Directory: "Music/Dummy", Format: "flac",
			Files: []sources.File{{Name: "01.flac", Extension: "flac", SizeBytes: 1 << 20}},
		}},
	}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(), WithSourceProvider(provider))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet, "/api/v1/albums/"+albumUUID+"/sources", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want the retry to succeed; body = %s", response.Code, response.Body)
	}
	if len(provider.queries) != 2 {
		t.Fatalf("searched %d times, want a second attempt after the refusal", len(provider.queries))
	}
}

// A provider still refusing after the retry is busy, not broken. Reporting it
// as a bad gateway blamed the wrong thing and gave the user nothing to do.
func TestListAlbumSourcesReportsAPersistentRateLimitAsBusy(t *testing.T) {
	noRateLimitPause(t)
	store := &fakeSettingsStore{
		fakeStore:  &fakeStore{album: db.GetAlbumRow{ArtistName: "Portishead", Title: "Dummy", TrackCount: 1}},
		configured: true, settings: configuredSettings(),
	}
	provider := &fakeProvider{searchErr: fmt.Errorf("%w: slskd is busy", sources.ErrRateLimited)}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(), WithSourceProvider(provider))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet, "/api/v1/albums/"+albumUUID+"/sources", nil))

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body = %s", response.Code, response.Body)
	}
}

// A source that cannot search is neither busy nor broken, and the person
// looking at the screen is the one who can go and fix it — so the answer
// carries what the provider actually said rather than a generic failure.
func TestListAlbumSourcesReportsASourceThatCannotSearch(t *testing.T) {
	noRateLimitPause(t)
	store := &fakeSettingsStore{
		fakeStore:  &fakeStore{album: db.GetAlbumRow{ArtistName: "Portishead", Title: "Dummy", TrackCount: 1}},
		configured: true, settings: configuredSettings(),
	}
	provider := &fakeProvider{searchErr: fmt.Errorf(
		"%w: slskd is running but is not connected to the Soulseek network",
		sources.ErrProviderUnavailable)}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(), WithSourceProvider(provider))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet, "/api/v1/albums/"+albumUUID+"/sources", nil))

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body = %s", response.Code, response.Body)
	}
	if !strings.Contains(response.Body.String(), "Soulseek network") {
		t.Errorf("body = %s, want it to name what is not connected", response.Body)
	}
}

func TestListAlbumSourcesAcceptsAQueryOverride(t *testing.T) {
	store := &fakeSettingsStore{
		fakeStore:  &fakeStore{album: db.GetAlbumRow{ArtistName: "Portishead", Title: "Dummy", TrackCount: 11}},
		configured: true, settings: configuredSettings(),
	}
	provider := &fakeProvider{}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(), WithSourceProvider(provider))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet, "/api/v1/albums/"+albumUUID+"/sources?q=dummy%20flac", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if provider.query.Text != "dummy flac" || provider.query.ExpectedTrackCount != 11 {
		t.Fatalf("query = %#v", provider.query)
	}

	short := httptest.NewRecorder()
	handler.ServeHTTP(short, httptest.NewRequest(
		http.MethodGet, "/api/v1/albums/"+albumUUID+"/sources?q=a", nil))
	if short.Code != http.StatusUnprocessableEntity {
		t.Fatalf("short query status = %d", short.Code)
	}
}

func TestListAlbumSourcesRetriesWithoutTrailingReleaseKind(t *testing.T) {
	store := &fakeSettingsStore{
		fakeStore: &fakeStore{album: db.GetAlbumRow{
			ArtistName: "Sewerslvt", Title: "Suffering From Melancholia - EP", TrackCount: 4,
		}},
		configured: true, settings: configuredSettings(),
	}
	fallback := "Sewerslvt Suffering From Melancholia"
	provider := &fakeProvider{results: map[string][]sources.Candidate{
		fallback: {{
			Provider: "fake", Username: "peer", Directory: "Music/Suffering From Melancholia",
			Files: []sources.File{{Name: "01.flac", Path: `Music\Suffering\01.flac`}},
		}},
	}}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(), WithSourceProvider(provider))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet, "/api/v1/albums/"+albumUUID+"/sources", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if len(provider.queries) != 2 ||
		provider.queries[0].Text != "Sewerslvt Suffering From Melancholia EP" ||
		provider.queries[1].Text != fallback {
		t.Fatalf("queries = %#v", provider.queries)
	}
	var payload struct {
		Query string                    `json:"query"`
		Items []sourceCandidateResponse `json:"items"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Query != fallback || len(payload.Items) != 1 {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestListAlbumSourcesDoesNotBroadenAnOverride(t *testing.T) {
	store := &fakeSettingsStore{
		fakeStore: &fakeStore{album: db.GetAlbumRow{
			ArtistName: "Sewerslvt", Title: "Suffering From Melancholia - EP", TrackCount: 4,
		}},
		configured: true, settings: configuredSettings(),
	}
	provider := &fakeProvider{results: map[string][]sources.Candidate{}}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(), WithSourceProvider(provider))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet, "/api/v1/albums/"+albumUUID+"/sources?q=custom%20EP", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if len(provider.queries) != 1 || provider.queries[0].Text != "custom EP" {
		t.Fatalf("queries = %#v", provider.queries)
	}
}

func TestListAlbumSourcesRequiresAnEnabledProvider(t *testing.T) {
	settings := configuredSettings()
	settings.Enabled = false
	store := &fakeSettingsStore{
		fakeStore:  &fakeStore{album: db.GetAlbumRow{ArtistName: "Portishead", Title: "Dummy"}},
		configured: true, settings: settings,
	}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(), WithSourceProvider(&fakeProvider{}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet, "/api/v1/albums/"+albumUUID+"/sources", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
}

func TestListAlbumSourcesReportsProviderFailure(t *testing.T) {
	store := &fakeSettingsStore{
		fakeStore:  &fakeStore{album: db.GetAlbumRow{ArtistName: "Portishead", Title: "Dummy"}},
		configured: true, settings: configuredSettings(),
	}
	provider := &fakeProvider{searchErr: errors.New("could not reach slskd")}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(), WithSourceProvider(provider))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet, "/api/v1/albums/"+albumUUID+"/sources", nil))
	if response.Code != http.StatusBadGateway {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if !strings.Contains(response.Body.String(), "could not reach slskd") {
		t.Fatalf("body = %s", response.Body)
	}
}

// fakeSearchStore is a settings store that also records bulk searches, because
// creating a run needs both: the provider has to be configured before fifty
// releases are queued against it.
type fakeSearchStore struct {
	*fakeSettingsStore
	requested   []uuid.UUID
	autoRequest bool
	rows        []db.SourceSearchRow
	createErr   error
	readErr     error
	stopped     int
	cancelErr   error
}

func (store *fakeSearchStore) CreateSourceSearchRun(
	_ context.Context, albumIDs []uuid.UUID, autoRequest bool,
) (uuid.UUID, int, error) {
	store.requested, store.autoRequest = albumIDs, autoRequest
	if store.createErr != nil {
		return uuid.Nil, 0, store.createErr
	}
	return uuid.New(), len(albumIDs), nil
}

func (store *fakeSearchStore) SourceSearchRun(
	context.Context, uuid.UUID,
) ([]db.SourceSearchRow, error) {
	return store.rows, store.readErr
}

func (store *fakeSearchStore) CancelSourceSearchRun(
	_ context.Context, _ uuid.UUID,
) ([]db.SourceSearchRow, int, error) {
	if store.cancelErr != nil {
		return nil, 0, store.cancelErr
	}
	return store.rows, store.stopped, nil
}

func searchStore() *fakeSearchStore {
	return &fakeSearchStore{
		fakeSettingsStore: &fakeSettingsStore{
			fakeStore:  &fakeStore{album: db.GetAlbumRow{ArtistName: "Portishead", Title: "Dummy"}},
			configured: true, settings: configuredSettings(),
		},
		rows: []db.SourceSearchRow{{
			ID: uuid.New(), AlbumID: uuid.New(), AlbumTitle: "Dummy",
			ArtistName: "Portishead", TrackCount: 11, Status: "queued",
		}},
		stopped: 1,
	}
}

func postSearch(t *testing.T, handler http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/source-searches", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestCreateSourceSearchQueuesEveryRelease(t *testing.T) {
	store := searchStore()
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithSourceProvider(&fakeProvider{}))

	first, second := uuid.New(), uuid.New()
	response := postSearch(t, handler,
		`{"albumIds":["`+first.String()+`","`+second.String()+`"]}`)
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if len(store.requested) != 2 || store.requested[0] != first || store.requested[1] != second {
		t.Fatalf("requested = %#v", store.requested)
	}

	var run struct {
		ID      string `json:"id"`
		Pending int    `json:"pending"`
		Total   int    `json:"total"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &run); err != nil {
		t.Fatalf("decode run: %v", err)
	}
	if run.ID == "" || run.Total != 1 || run.Pending != 1 {
		t.Fatalf("run = %#v", run)
	}
}

// Permission to record downloads without asking is given per run, so it has to
// travel with the run rather than be inferred later.
func TestCreateSourceSearchCarriesThePermissionToRequestAutomatically(t *testing.T) {
	store := searchStore()
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithSourceProvider(&fakeProvider{}))

	response := postSearch(t, handler,
		`{"albumIds":["`+uuid.New().String()+`"],"autoRequest":true}`)
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if !store.autoRequest {
		t.Fatal("the run was created without the permission the request gave it")
	}
}

// Silence is not permission. A run that does not ask for it must never get it.
func TestCreateSourceSearchWithholdsAutomaticRequestsByDefault(t *testing.T) {
	store := searchStore()
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithSourceProvider(&fakeProvider{}))

	postSearch(t, handler, `{"albumIds":["`+uuid.New().String()+`"]}`)
	if store.autoRequest {
		t.Fatal("a run that never asked to act on its own was given permission to")
	}
}

// An unconfigured provider is a thing to fix, not a run to wait out: fifty
// releases must not be queued against a search that cannot happen.
func TestCreateSourceSearchRefusesWithoutAnEnabledProvider(t *testing.T) {
	store := searchStore()
	store.settings.Enabled = false
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithSourceProvider(&fakeProvider{}))

	response := postSearch(t, handler, `{"albumIds":["`+uuid.New().String()+`"]}`)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if store.requested != nil {
		t.Fatalf("queued %#v against a provider that cannot search", store.requested)
	}
}

func TestCreateSourceSearchRefusesAnEmptySelection(t *testing.T) {
	handler := NewAPI(searchStore(), fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithSourceProvider(&fakeProvider{}))

	response := postSearch(t, handler, `{"albumIds":[]}`)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
}

// Every release is a separate provider search the worker runs one after
// another, so a run has to be bounded rather than merely discouraged.
func TestCreateSourceSearchRefusesMoreReleasesThanARunAllows(t *testing.T) {
	handler := NewAPI(searchStore(), fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithSourceProvider(&fakeProvider{}))

	ids := make([]string, 0, maxSearchRunAlbums+1)
	for range maxSearchRunAlbums + 1 {
		ids = append(ids, `"`+uuid.New().String()+`"`)
	}
	response := postSearch(t, handler, `{"albumIds":[`+strings.Join(ids, ",")+`]}`)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if !strings.Contains(response.Body.String(), "50 releases or fewer") {
		t.Fatalf("body = %s", response.Body)
	}
}

func TestGetSourceSearchReportsWhatEachReleaseCameTo(t *testing.T) {
	store := searchStore()
	requested := uuid.New()
	store.rows = []db.SourceSearchRow{
		{
			AlbumID: uuid.New(), AlbumTitle: "Dummy", ArtistName: "Portishead",
			TrackCount: 11, Status: "found", Query: "portishead dummy",
			Candidates: []db.SourceSearchCandidate{{
				Provider: "slskd", Username: "peer", Format: "flac", Score: 0.93,
				Files: []db.SourceSearchFile{{Path: `p\01.flac`, Name: "01.flac"}},
			}},
		},
		{AlbumID: uuid.New(), AlbumTitle: "Third", ArtistName: "Portishead", Status: "none"},
		{
			AlbumID: uuid.New(), AlbumTitle: "Roseland", ArtistName: "Portishead",
			Status: "failed", ErrorMessage: pgtype.Text{String: "slskd is unreachable", Valid: true},
		},
		{
			AlbumID: uuid.New(), AlbumTitle: "Hex", ArtistName: "Bark Psychosis",
			Status: "queued", RequestedDownloadID: uuid.NullUUID{UUID: requested, Valid: true},
		},
	}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithSourceProvider(&fakeProvider{}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet, "/api/v1/source-searches/"+uuid.New().String(), nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}

	var run struct {
		Pending     int `json:"pending"`
		Total       int `json:"total"`
		FoundCount  int `json:"foundCount"`
		NoneCount   int `json:"noneCount"`
		FailedCount int `json:"failedCount"`
		Items       []struct {
			Status              string  `json:"status"`
			Error               *string `json:"error"`
			RequestedDownloadID *string `json:"requestedDownloadId"`
			Candidates          []struct {
				Username   string `json:"username"`
				TrackCount int    `json:"trackCount"`
			} `json:"candidates"`
		} `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &run); err != nil {
		t.Fatalf("decode run: %v", err)
	}
	if run.Total != 4 || run.Pending != 1 || run.FoundCount != 1 ||
		run.NoneCount != 1 || run.FailedCount != 1 {
		t.Fatalf("run tallies = %#v", run)
	}
	// Finding nothing and failing to look must never render the same.
	if run.Items[1].Error != nil {
		t.Errorf("an empty result carried an error: %v", *run.Items[1].Error)
	}
	if run.Items[2].Error == nil || *run.Items[2].Error != "slskd is unreachable" {
		t.Errorf("failed result error = %v", run.Items[2].Error)
	}
	if len(run.Items[0].Candidates) != 1 || run.Items[0].Candidates[0].TrackCount != 1 {
		t.Fatalf("candidates = %#v", run.Items[0].Candidates)
	}
	if run.Items[3].RequestedDownloadID == nil || *run.Items[3].RequestedDownloadID != requested.String() {
		t.Errorf("requested download = %v", run.Items[3].RequestedDownloadID)
	}
}

func TestGetSourceSearchReportsAnUnknownRunAsMissing(t *testing.T) {
	store := searchStore()
	store.readErr = pgx.ErrNoRows
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithSourceProvider(&fakeProvider{}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet, "/api/v1/source-searches/"+uuid.New().String(), nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
}

// A cancelled release is not waiting for anything, so the interface must stop
// waiting for it. Counting it as pending is what would leave a stopped run
// polling for an answer that is never coming.
func TestGetSourceSearchCountsACancelledReleaseAsSettled(t *testing.T) {
	store := searchStore()
	store.rows = []db.SourceSearchRow{
		{AlbumID: uuid.New(), AlbumTitle: "Dummy", ArtistName: "Portishead", Status: "found"},
		{AlbumID: uuid.New(), AlbumTitle: "Third", ArtistName: "Portishead", Status: "cancelled"},
	}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithSourceProvider(&fakeProvider{}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet, "/api/v1/source-searches/"+uuid.New().String(), nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}

	var run struct {
		Pending        int `json:"pending"`
		Total          int `json:"total"`
		FoundCount     int `json:"foundCount"`
		CancelledCount int `json:"cancelledCount"`
		FailedCount    int `json:"failedCount"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &run); err != nil {
		t.Fatalf("decode run: %v", err)
	}
	if run.Pending != 0 || run.Total != 2 || run.CancelledCount != 1 || run.FoundCount != 1 {
		t.Fatalf("run tallies = %#v", run)
	}
	// Nobody looked, which is not the same as looking and getting nowhere.
	if run.FailedCount != 0 {
		t.Errorf("a cancelled release was counted as a failure")
	}
}

func postCancel(t *testing.T, handler http.Handler, runID string) *httptest.ResponseRecorder {
	t.Helper()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, "/api/v1/source-searches/"+runID+"/cancel", nil))
	return response
}

func TestCancelSourceSearchReturnsTheRunAsItNowStands(t *testing.T) {
	store := searchStore()
	store.rows = []db.SourceSearchRow{
		{AlbumID: uuid.New(), AlbumTitle: "Dummy", ArtistName: "Portishead", Status: "found"},
		{AlbumID: uuid.New(), AlbumTitle: "Third", ArtistName: "Portishead", Status: "cancelled"},
	}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithSourceProvider(&fakeProvider{}))

	response := postCancel(t, handler, uuid.New().String())
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}

	var run struct {
		Pending        int `json:"pending"`
		CancelledCount int `json:"cancelledCount"`
		Items          []struct {
			Status string `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &run); err != nil {
		t.Fatalf("decode run: %v", err)
	}
	if run.Pending != 0 || run.CancelledCount != 1 {
		t.Fatalf("run tallies = %#v", run)
	}
	// A release already searched is a result somebody may act on, and stopping
	// the rest of the run is not a reason to withdraw it.
	if len(run.Items) != 2 || run.Items[0].Status != "found" {
		t.Fatalf("items = %#v", run.Items)
	}
}

// A run is a shared thing: a second tab open on it goes on counting stopped
// releases as waiting until it asks again. Cancelling is announced so it learns
// at once, the same way each settled release is.
func TestCancelSourceSearchAnnouncesIt(t *testing.T) {
	hub := events.NewHub()
	notices, unsubscribe := hub.Subscribe()
	defer unsubscribe()
	store := searchStore()
	store.rows = []db.SourceSearchRow{
		{AlbumID: uuid.New(), AlbumTitle: "Third", ArtistName: "Portishead", Status: "cancelled"},
	}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithSourceProvider(&fakeProvider{}), WithEventHub(hub))

	if response := postCancel(t, handler, uuid.New().String()); response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}

	select {
	case topic := <-notices:
		if topic != events.TopicSourceSearches {
			t.Fatalf("notice = %q, want the source-searches topic", topic)
		}
	default:
		t.Fatal("cancelling a run published nothing")
	}
}

// Nothing was stopped, so nothing went stale. A notice here would send every
// open interface to refetch a run that reads exactly as it did before.
func TestCancelSourceSearchAnnouncesNothingWhenItRefuses(t *testing.T) {
	hub := events.NewHub()
	notices, unsubscribe := hub.Subscribe()
	defer unsubscribe()
	store := searchStore()
	store.cancelErr = db.ErrNothingToCancel
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithSourceProvider(&fakeProvider{}), WithEventHub(hub))

	if response := postCancel(t, handler, uuid.New().String()); response.Code != http.StatusConflict {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}

	select {
	case topic := <-notices:
		t.Fatalf("a refused cancellation published %q", topic)
	default:
	}
}

// Refused rather than reported as a cancellation that cancelled nothing: the
// answers are all there, and saying the run was stopped would suggest they are
// not.
func TestCancelSourceSearchRefusesARunWithNothingLeftWaiting(t *testing.T) {
	store := searchStore()
	store.cancelErr = db.ErrNothingToCancel
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithSourceProvider(&fakeProvider{}))

	response := postCancel(t, handler, uuid.New().String())
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
}

func TestCancelSourceSearchReportsAnUnknownRunAsMissing(t *testing.T) {
	store := searchStore()
	store.cancelErr = pgx.ErrNoRows
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithSourceProvider(&fakeProvider{}))

	response := postCancel(t, handler, uuid.New().String())
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
}

// Without the settings queries there is nowhere to read or write a source
// configuration, and the routes say so rather than reporting nothing stored.
func TestTheSettingsRoutesReportBeingUnavailable(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())
	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/api/v1/settings/slskd", nil),
		httptest.NewRequest(http.MethodPut, "/api/v1/settings/slskd", strings.NewReader(`{}`)),
		httptest.NewRequest(http.MethodPost, "/api/v1/settings/slskd/test", nil),
		httptest.NewRequest(http.MethodGet, "/api/v1/settings/imports", nil),
		httptest.NewRequest(http.MethodPut, "/api/v1/settings/imports", strings.NewReader(`{}`)),
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s: status = %d, want 503", request.Method, request.URL.Path, response.Code)
		}
	}
}

func TestReadingSlskdSettingsReportsAStoreThatFailed(t *testing.T) {
	store := &fakeSettingsStore{
		fakeStore: &fakeStore{}, settingsErr: errors.New("the database is unreachable"),
	}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithSourceProvider(&fakeProvider{}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/settings/slskd", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

func TestSavingSlskdSettingsRejectsABodyThatIsNotJSON(t *testing.T) {
	store := &fakeSettingsStore{fakeStore: &fakeStore{}}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithSourceProvider(&fakeProvider{}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/api/v1/settings/slskd",
		strings.NewReader(`not json`)))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.Code)
	}
}

// Every bound on the stored settings is refused rather than corrected, because
// a base URL nothing can be reached at is worth saying out loud.
func TestSlskdSettingsRefuseWhatCannotBeStored(t *testing.T) {
	store := &fakeSettingsStore{fakeStore: &fakeStore{}, configured: true, settings: configuredSettings()}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithSourceProvider(&fakeProvider{}))
	for name, body := range map[string]string{
		"not a URL":     `{"baseUrl":"h ttp://slskd","apiKey":"k"}`,
		"not http":      `{"baseUrl":"ftp://slskd:5030","apiKey":"k"}`,
		"no host":       `{"baseUrl":"http://","apiKey":"k"}`,
		"long base URL": `{"baseUrl":"http://` + strings.Repeat("h", 500) + `","apiKey":"k"}`,
		"long API key":  `{"baseUrl":"http://slskd:5030","apiKey":"` + strings.Repeat("k", 501) + `"}`,
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/api/v1/settings/slskd",
			strings.NewReader(body)))
		if response.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: status = %d, want 422; body = %s", name, response.Code, response.Body)
		}
	}
}

// A timeout nobody gave takes the default, which is what makes the settings
// form safe to submit with the field left alone.
func TestSlskdSettingsWithoutATimeoutTakeTheDefault(t *testing.T) {
	store := &fakeSettingsStore{fakeStore: &fakeStore{}}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithSourceProvider(&fakeProvider{}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/api/v1/settings/slskd",
		strings.NewReader(`{"baseUrl":"http://slskd:5030","apiKey":"secret"}`)))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if store.saved.SearchTimeoutSeconds != 20 {
		t.Fatalf("timeout = %d, want the default of 20", store.saved.SearchTimeoutSeconds)
	}
}

// A stored key is only kept if there is one to keep, and a lookup that failed
// is not the same as nothing stored.
func TestKeepingAStoredKeyReportsAStoreThatFailed(t *testing.T) {
	store := &fakeSettingsStore{
		fakeStore: &fakeStore{}, settingsErr: errors.New("the database is unreachable"),
	}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithSourceProvider(&fakeProvider{}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/api/v1/settings/slskd",
		strings.NewReader(`{"baseUrl":"http://slskd:5030"}`)))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

func TestSavingSlskdSettingsReportsAStoreThatFailed(t *testing.T) {
	store := &fakeSettingsStore{
		fakeStore: &fakeStore{}, saveSlskdErr: errors.New("the database is unreachable"),
	}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithSourceProvider(&fakeProvider{}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/api/v1/settings/slskd",
		strings.NewReader(`{"baseUrl":"http://slskd:5030","apiKey":"secret"}`)))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

// The stored status is what the settings page shows between checks, so a check
// whose outcome could not be recorded is reported rather than answered with a
// status nothing wrote.
func TestATestedConnectionReportsAStoreThatFailed(t *testing.T) {
	store := &fakeSettingsStore{
		fakeStore: &fakeStore{}, configured: true, settings: configuredSettings(),
		recordErr: errors.New("the database is unreachable"),
	}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithSourceProvider(&fakeProvider{}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/settings/slskd/test", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

// A provider that could not be built from the stored settings is this
// installation's fault rather than a configuration nobody entered.
func TestATestedConnectionReportsAProviderThatCouldNotBeBuilt(t *testing.T) {
	store := &fakeSettingsStore{
		fakeStore: &fakeStore{}, configured: true, settings: configuredSettings(),
	}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithSourceProvider(&fakeProvider{buildErr: errors.New("the base URL cannot be parsed")}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/settings/slskd/test", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

func TestSearchingSourcesForAReleaseThatIsNotThereIsNotFound(t *testing.T) {
	store := &fakeSettingsStore{
		fakeStore: &fakeStore{err: pgx.ErrNoRows}, configured: true, settings: configuredSettings(),
	}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithSourceProvider(&fakeProvider{}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/albums/"+albumUUID+"/sources", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
}

func TestSearchingSourcesReportsAStoreThatFailed(t *testing.T) {
	store := &fakeSettingsStore{
		fakeStore:  &fakeStore{err: errors.New("the database is unreachable")},
		configured: true, settings: configuredSettings(),
	}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithSourceProvider(&fakeProvider{}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/albums/"+albumUUID+"/sources", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

// A release named with nothing a search engine could match is not a search to
// run, and saying so is better than broadcasting an empty phrase.
func TestSearchingSourcesRefusesAReleaseWithNothingToSearchFor(t *testing.T) {
	store := &fakeSettingsStore{
		fakeStore:  &fakeStore{album: db.GetAlbumRow{ArtistName: "...", Title: "!!!"}},
		configured: true, settings: configuredSettings(),
	}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithSourceProvider(&fakeProvider{}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/albums/"+albumUUID+"/sources", nil))
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body = %s", response.Code, response.Body)
	}
}

// A title of nothing but symbols leaves the artist's name as the whole phrase,
// which asks for everything that peer is sharing by them. The release has no
// ladder at all now, and the endpoint refuses rather than searching for one.
func TestSearchingSourcesRefusesAReleaseWhoseTitleLeavesOnlyTheArtist(t *testing.T) {
	store := &fakeSettingsStore{
		fakeStore:  &fakeStore{album: db.GetAlbumRow{ArtistName: "Cynthoni", Title: "｡ﾟ･ (>﹏<) ･ﾟ｡"}},
		configured: true, settings: configuredSettings(),
	}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithSourceProvider(&fakeProvider{}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/albums/"+albumUUID+"/sources", nil))
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body = %s", response.Code, response.Body)
	}
}

// Nobody is left to read the answer once the client has gone.
func TestACancelledSourceSearchIsNotAnswered(t *testing.T) {
	store := &fakeSettingsStore{
		fakeStore:  &fakeStore{album: db.GetAlbumRow{ArtistName: "Portishead", Title: "Dummy"}},
		configured: true, settings: configuredSettings(),
	}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithSourceProvider(&fakeProvider{searchErr: context.Canceled}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/albums/"+albumUUID+"/sources", nil))
	if response.Body.Len() != 0 {
		t.Fatalf("body = %s, want nothing written", response.Body)
	}
}

// A client that gives up during the rate-limit pause is not waited out and is
// not answered.
func TestASourceSearchAbandonedDuringTheRateLimitPauseIsNotAnswered(t *testing.T) {
	previous := rateLimitPause
	rateLimitPause = time.Hour
	t.Cleanup(func() { rateLimitPause = previous })

	store := &fakeSettingsStore{
		fakeStore:  &fakeStore{album: db.GetAlbumRow{ArtistName: "Portishead", Title: "Dummy"}},
		configured: true, settings: configuredSettings(),
	}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithSourceProvider(&fakeProvider{searchErr: sources.ErrRateLimited}))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request := httptest.NewRequest(http.MethodGet,
		"/api/v1/albums/"+albumUUID+"/sources", nil).WithContext(ctx)

	done := make(chan struct{})
	response := httptest.NewRecorder()
	go func() {
		defer close(done)
		handler.ServeHTTP(response, request)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the search waited out a pause nobody was left for")
	}
	if response.Body.Len() != 0 {
		t.Fatalf("body = %s, want nothing written", response.Body)
	}
}

func TestCreatingASourceSearchRejectsABodyThatIsNotJSON(t *testing.T) {
	handler := NewAPI(searchStore(), fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithSourceProvider(&fakeProvider{}))

	response := postSearch(t, handler, `not json`)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.Code)
	}
}

// A provider that could not be built is not the same as one nobody configured,
// and a run must not be queued against either.
func TestCreatingASourceSearchReportsAProviderThatCouldNotBeBuilt(t *testing.T) {
	store := searchStore()
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithSourceProvider(&fakeProvider{buildErr: errors.New("the base URL cannot be parsed")}))

	response := postSearch(t, handler, `{"albumIds":["`+uuid.New().String()+`"]}`)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
	if store.requested != nil {
		t.Fatalf("requested = %v, want nothing queued", store.requested)
	}
}

// Releases that do not exist cannot be searched for, and a run of none of them
// is refused rather than created empty.
func TestCreatingASourceSearchForReleasesThatDoNotExistIsRefused(t *testing.T) {
	store := searchStore()
	store.createErr = pgx.ErrNoRows
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithSourceProvider(&fakeProvider{}))

	response := postSearch(t, handler, `{"albumIds":["`+uuid.New().String()+`"]}`)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", response.Code)
	}
}

func TestCreatingASourceSearchReportsAStoreThatFailed(t *testing.T) {
	store := searchStore()
	store.createErr = errors.New("the database is unreachable")
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithSourceProvider(&fakeProvider{}))

	response := postSearch(t, handler, `{"albumIds":["`+uuid.New().String()+`"]}`)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

// The run that was queued is read back so the caller sees what it created; a
// read that failed is reported rather than answered with an empty run.
func TestReadingBackAQueuedSearchReportsAStoreThatFailed(t *testing.T) {
	store := searchStore()
	store.readErr = errors.New("the database is unreachable")
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithSourceProvider(&fakeProvider{}))

	response := postSearch(t, handler, `{"albumIds":["`+uuid.New().String()+`"]}`)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

func TestReadingASourceSearchReportsAStoreThatFailed(t *testing.T) {
	store := searchStore()
	store.readErr = errors.New("the database is unreachable")
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithSourceProvider(&fakeProvider{}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/source-searches/"+uuid.New().String(), nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

func TestCancellingASourceSearchReportsAStoreThatFailed(t *testing.T) {
	store := searchStore()
	store.cancelErr = errors.New("the database is unreachable")
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithSourceProvider(&fakeProvider{}))

	response := postCancel(t, handler, uuid.New().String())
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

// A run id that is not a UUID names no run.
func TestAMalformedRunIDNamesNoSearch(t *testing.T) {
	handler := NewAPI(searchStore(), fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithSourceProvider(&fakeProvider{}))
	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/api/v1/source-searches/not-a-uuid", nil),
		httptest.NewRequest(http.MethodPost, "/api/v1/source-searches/not-a-uuid/cancel", nil),
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusNotFound {
			t.Errorf("%s %s: status = %d, want 404", request.Method, request.URL.Path, response.Code)
		}
	}
}

// A build with no provider wired in has nothing to test the connection with,
// and that is a configuration to complete rather than a server fault.
func TestTestingTheConnectionWithoutAProviderIsRefused(t *testing.T) {
	store := &fakeSettingsStore{
		fakeStore: &fakeStore{}, configured: true, settings: configuredSettings(),
	}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/settings/slskd/test", nil))
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", response.Code)
	}
}

func TestSearchingSourcesReportsSettingsThatCouldNotBeRead(t *testing.T) {
	store := &fakeSettingsStore{
		fakeStore:   &fakeStore{album: db.GetAlbumRow{ArtistName: "Portishead", Title: "Dummy"}},
		settingsErr: errors.New("the database is unreachable"),
	}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithSourceProvider(&fakeProvider{}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/albums/"+albumUUID+"/sources", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

func TestSearchingSourcesReportsAProviderThatCouldNotBeBuilt(t *testing.T) {
	store := &fakeSettingsStore{
		fakeStore:  &fakeStore{album: db.GetAlbumRow{ArtistName: "Portishead", Title: "Dummy"}},
		configured: true, settings: configuredSettings(),
	}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithSourceProvider(&fakeProvider{buildErr: errors.New("the base URL cannot be parsed")}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/albums/"+albumUUID+"/sources", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

// The catalogue's tracks are what a candidate is corroborated against, and
// failing to load them costs the evidence rather than the search.
func TestASearchStillRunsWhenTheCatalogueTracksCannotBeRead(t *testing.T) {
	store := &fakeSettingsStore{
		fakeStore: &fakeStore{
			album:     db.GetAlbumRow{ArtistName: "Portishead", Title: "Dummy", TrackCount: 11},
			tracksErr: errors.New("the database is unreachable"),
		},
		configured: true, settings: configuredSettings(),
	}
	provider := &fakeProvider{}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithSourceProvider(provider))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/albums/"+albumUUID+"/sources", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if len(provider.queries) == 0 {
		t.Fatal("nothing was searched for")
	}
}

func TestAMalformedReleaseIDNamesNothingToSearchSourcesFor(t *testing.T) {
	handler := NewAPI(searchStore(), fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithSourceProvider(&fakeProvider{}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/albums/not-a-uuid/sources", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
}

// Without the source-search queries the routes say so, rather than looking like
// a run nobody has ever started.
func TestTheSourceSearchRoutesReportBeingUnavailable(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())
	runID := uuid.NewString()
	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodPost, "/api/v1/source-searches", strings.NewReader(`{}`)),
		httptest.NewRequest(http.MethodGet, "/api/v1/source-searches/"+runID, nil),
		httptest.NewRequest(http.MethodPost, "/api/v1/source-searches/"+runID+"/cancel", nil),
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s: status = %d, want 503", request.Method, request.URL.Path, response.Code)
		}
	}
}

// A release whose every offer was under the user's bitrate floor found nothing,
// and that is a different answer from nobody sharing it. The run says how many
// copies the floor turned away and why, so the person can lower it.
func TestASearchReportsHowManyCopiesTheBitRateFloorRefused(t *testing.T) {
	store := searchStore()
	store.rows = []db.SourceSearchRow{{
		AlbumID: uuid.New(), AlbumTitle: "Dummy", ArtistName: "Portishead",
		Status: "none",
		Refused: []db.SourceSearchRefusal{
			{
				Username: "peer one", Directory: "Dummy [MP3]", Format: "mp3",
				Reason: "MP3 at 128 kbps is below the 320 kbps you asked for",
				Kind:   sources.RefusedBitRate,
			},
			{
				Username: "peer two", Directory: "Dummy [MP3 V2]", Format: "mp3",
				Reason: "MP3 at 192 kbps is below the 320 kbps you asked for",
				Kind:   sources.RefusedBitRate,
			},
			{
				Username: "peer three", Directory: "Dummy [DSF]", Format: "dsf",
				Reason: "DSF is a format you do not accept",
				Kind:   sources.RefusedFormat,
			},
		},
	}}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithSourceProvider(&fakeProvider{}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet, "/api/v1/source-searches/"+uuid.New().String(), nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}

	var run struct {
		Items []struct {
			RefusedBelowBitRate int `json:"refusedBelowBitRate"`
			Refused             []struct {
				Username string `json:"username"`
				Reason   string `json:"reason"`
				Kind     string `json:"kind"`
			} `json:"refused"`
		} `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &run); err != nil {
		t.Fatalf("decode run: %v", err)
	}
	if len(run.Items) != 1 {
		t.Fatalf("items = %d, want one", len(run.Items))
	}
	item := run.Items[0]
	if item.RefusedBelowBitRate != 2 {
		t.Fatalf("refusedBelowBitRate = %d, want the two under the floor",
			item.RefusedBelowBitRate)
	}
	if len(item.Refused) != 3 {
		t.Fatalf("refused = %#v, want all three reported", item.Refused)
	}
	if item.Refused[0].Reason == "" || item.Refused[0].Username != "peer one" {
		t.Fatalf("first refusal = %#v, want the peer and the reason", item.Refused[0])
	}
}
