package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pxldi/schall/internal/db"
	"github.com/rs/zerolog"
)

func putRetention(t *testing.T, handler http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPut, "/api/v1/settings/imports", strings.NewReader(body))
	request.Header.Set("content-type", "application/json")
	handler.ServeHTTP(response, request)
	return response
}

func TestImportSettingsDefaultToKeepingTheProviderCopy(t *testing.T) {
	store := &fakeSettingsStore{fakeStore: &fakeStore{}}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithDownloadInbox(t.TempDir()))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/settings/imports", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}

	var payload importSettingsResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.SourceRetention != db.SourceRetentionKeep || !payload.InboxWritable {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestSaveImportSettingsRejectsAnUnknownPolicy(t *testing.T) {
	store := &fakeSettingsStore{fakeStore: &fakeStore{}}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithDownloadInbox(t.TempDir()))

	response := putRetention(t, handler, `{"sourceRetention":"archive"}`)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if store.imports.SourceRetention != "" {
		t.Fatalf("an unknown policy was stored: %q", store.imports.SourceRetention)
	}
}

// Removal is refused rather than stored when Schall could not carry it out.
// slskd's completed-download folder is normally mounted read-only, and a policy
// accepted there would report a refusal once per release instead of once.
func TestSaveImportSettingsRefusesDeletionIntoAReadOnlyInbox(t *testing.T) {
	inbox := filepath.Join(t.TempDir(), "downloads")
	if err := os.Mkdir(inbox, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(inbox, 0o755) })
	if os.Geteuid() == 0 {
		t.Skip("root ignores the directory mode this test relies on")
	}
	store := &fakeSettingsStore{fakeStore: &fakeStore{}}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithDownloadInbox(inbox))

	response := putRetention(t, handler, `{"sourceRetention":"delete"}`)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if store.imports.SourceRetention != "" {
		t.Fatalf("a policy Schall cannot apply was stored: %q", store.imports.SourceRetention)
	}
	// Keeping stays available: an inbox nobody may write to is only a reason to
	// refuse deletion, never a reason to refuse the default.
	if response := putRetention(t, handler, `{"sourceRetention":"keep"}`); response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
}

func TestSaveImportSettingsStoresDeletionForAWritableInbox(t *testing.T) {
	inbox := t.TempDir()
	store := &fakeSettingsStore{fakeStore: &fakeStore{}}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithDownloadInbox(inbox))

	response := putRetention(t, handler, `{"sourceRetention":"delete"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if store.imports.SourceRetention != db.SourceRetentionDelete {
		t.Fatalf("stored = %q", store.imports.SourceRetention)
	}
	// The probe is the only thing Schall writes to the provider folder, and it
	// does not outlive the request.
	entries, err := os.ReadDir(inbox)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("the writability probe was left behind: %v", entries)
	}
}

func TestSaveImportSettingsRefusesDeletionWithoutAConfiguredInbox(t *testing.T) {
	store := &fakeSettingsStore{fakeStore: &fakeStore{}}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := putRetention(t, handler, `{"sourceRetention":"delete"}`)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
}

func TestReadingImportSettingsReportsAStoreThatFailed(t *testing.T) {
	store := &fakeSettingsStore{
		fakeStore: &fakeStore{}, importsErr: errors.New("the database is unreachable"),
	}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithDownloadInbox(t.TempDir()))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/settings/imports", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

func TestSavingImportSettingsRejectsABodyThatIsNotJSON(t *testing.T) {
	store := &fakeSettingsStore{fakeStore: &fakeStore{}}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithDownloadInbox(t.TempDir()))

	response := putRetention(t, handler, `not json`)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.Code)
	}
}

// Enabling the acoustic check without a key would look like a verification that
// silently verifies nothing, so whether one is stored has to be read — and a
// read that failed is not the same as nothing stored.
func TestEnablingTheAcousticCheckReportsAStoreThatFailed(t *testing.T) {
	store := &fakeSettingsStore{
		fakeStore: &fakeStore{}, importsErr: errors.New("the database is unreachable"),
	}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithDownloadInbox(t.TempDir()))

	response := putRetention(t, handler, `{"sourceRetention":"keep","acoustidEnabled":true}`)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

// An acoustic check with no key anywhere verifies nothing, which is worse than
// an honestly absent one.
func TestEnablingTheAcousticCheckWithoutAKeyIsRefused(t *testing.T) {
	store := &fakeSettingsStore{fakeStore: &fakeStore{}}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithDownloadInbox(t.TempDir()))

	response := putRetention(t, handler, `{"sourceRetention":"keep","acoustidEnabled":true}`)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body = %s", response.Code, response.Body)
	}
}

// A key already stored is the key: the settings form never echoes it back, so
// enabling the check without retyping it has to keep working.
func TestEnablingTheAcousticCheckAgainstAStoredKeyIsAllowed(t *testing.T) {
	store := &fakeSettingsStore{
		fakeStore: &fakeStore{},
		imports: db.ImportSettingsRow{
			SourceRetention: db.SourceRetentionKeep, AcoustIDAPIKey: "stored-key",
		},
	}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithDownloadInbox(t.TempDir()))

	response := putRetention(t, handler, `{"sourceRetention":"keep","acoustidEnabled":true}`)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if store.imports.AcoustIDAPIKey != "stored-key" {
		t.Fatalf("stored key = %q, want the one already there", store.imports.AcoustIDAPIKey)
	}
}

func TestSavingImportSettingsReportsAStoreThatFailed(t *testing.T) {
	store := &fakeSettingsStore{
		fakeStore: &fakeStore{}, saveImportsErr: errors.New("the database is unreachable"),
	}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithDownloadInbox(t.TempDir()))

	response := putRetention(t, handler, `{"sourceRetention":"keep"}`)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

// anEncoder is a file standing in for ffmpeg. Whether re-encoding can be turned
// on is answered by looking for the binary, not by running it, so a path that
// exists is enough and no audio is involved.
func anEncoder(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ffmpeg")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestImportSettingsDefaultToLeavingMusicAsItArrived(t *testing.T) {
	store := &fakeSettingsStore{fakeStore: &fakeStore{}}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithDownloadInbox(t.TempDir()), WithEncoder(anEncoder(t)))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/settings/imports", nil))

	var payload importSettingsResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.TranscodeEnabled {
		t.Fatal("music is re-encoded on an installation that never asked for it")
	}
	if payload.TranscodeTarget != "mp3" || payload.TranscodeBitrate != "320" ||
		payload.TranscodeWhen != "lossless" {
		t.Fatalf("payload = %#v", payload)
	}
	if !payload.TranscoderPresent {
		t.Fatalf("the encoder at %q was not found", payload.TranscoderDetail)
	}
}

func TestSaveImportSettingsStoresTheReEncodingPolicy(t *testing.T) {
	store := &fakeSettingsStore{fakeStore: &fakeStore{}}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithDownloadInbox(t.TempDir()), WithEncoder(anEncoder(t)))

	response := putRetention(t, handler, `{"sourceRetention":"keep","transcodeEnabled":true,`+
		`"transcodeTarget":"mp3","transcodeBitrate":"V0","transcodeWhen":"above_target"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if !store.imports.TranscodeEnabled || store.imports.TranscodeBitrate != "V0" ||
		store.imports.TranscodeWhen != "above_target" {
		t.Fatalf("stored = %#v", store.imports)
	}
}

// The whole reason TranscodeEnabled and its siblings are pointers: a PUT that
// only means to change the source retention sends no opinion about
// re-encoding at all, and that must not silently turn a stored policy off.
func TestSavingOnlyTheRetentionKeepsTheReEncodingPolicyEnabled(t *testing.T) {
	store := &fakeSettingsStore{fakeStore: &fakeStore{}}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithDownloadInbox(t.TempDir()), WithEncoder(anEncoder(t)))

	enabled := putRetention(t, handler, `{"sourceRetention":"keep","transcodeEnabled":true,`+
		`"transcodeTarget":"mp3","transcodeBitrate":"256","transcodeWhen":"lossless"}`)
	if enabled.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", enabled.Code, enabled.Body)
	}
	if !store.imports.TranscodeEnabled {
		t.Fatal("re-encoding was not stored as enabled")
	}

	// This request has an opinion about the retention alone.
	after := putRetention(t, handler, `{"sourceRetention":"delete"}`)
	if after.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", after.Code, after.Body)
	}
	if store.imports.SourceRetention != db.SourceRetentionDelete {
		t.Fatalf("sourceRetention = %q, want %q", store.imports.SourceRetention, db.SourceRetentionDelete)
	}
	if !store.imports.TranscodeEnabled || store.imports.TranscodeBitrate != "256" ||
		store.imports.TranscodeWhen != "lossless" {
		t.Fatalf("stored = %#v, want the re-encoding policy unchanged", store.imports)
	}

	var payload importSettingsResponse
	if err := json.NewDecoder(after.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if !payload.TranscodeEnabled {
		t.Fatal("the response reports re-encoding as off after a request that never mentioned it")
	}
}

func TestSaveImportSettingsRejectsABitrateNobodyCanStore(t *testing.T) {
	store := &fakeSettingsStore{fakeStore: &fakeStore{}}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithDownloadInbox(t.TempDir()), WithEncoder(anEncoder(t)))

	response := putRetention(t, handler, `{"sourceRetention":"keep","transcodeEnabled":true,`+
		`"transcodeTarget":"mp3","transcodeBitrate":"999","transcodeWhen":"lossless"}`)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if store.imports.TranscodeEnabled {
		t.Fatal("a policy the database would refuse was stored")
	}
}

func TestSaveImportSettingsRejectsAnUnknownRuleForWhichFilesAreReEncoded(t *testing.T) {
	store := &fakeSettingsStore{fakeStore: &fakeStore{}}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithDownloadInbox(t.TempDir()), WithEncoder(anEncoder(t)))

	response := putRetention(t, handler, `{"sourceRetention":"keep","transcodeEnabled":true,`+
		`"transcodeTarget":"mp3","transcodeBitrate":"320","transcodeWhen":"sometimes"}`)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
}

// A setting that is on and does nothing is worse than one that is off: the
// operator believes their disc is filling slower than it is.
func TestSaveImportSettingsRefusesReEncodingWithNoEncoderInstalled(t *testing.T) {
	store := &fakeSettingsStore{fakeStore: &fakeStore{}}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithDownloadInbox(t.TempDir()),
		WithEncoder(filepath.Join(t.TempDir(), "no-such-encoder")))

	response := putRetention(t, handler, `{"sourceRetention":"keep","transcodeEnabled":true,`+
		`"transcodeTarget":"mp3","transcodeBitrate":"320","transcodeWhen":"lossless"}`)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if store.imports.TranscodeEnabled {
		t.Fatal("re-encoding was turned on with nothing to re-encode with")
	}
	if !strings.Contains(response.Body.String(), "re-encoded on this machine") {
		t.Fatalf("body = %s", response.Body)
	}
}

// The existing settings keep working untouched: a caller that says nothing
// about re-encoding is not turning it on, and is not storing an empty policy
// the database would refuse either.
func TestSaveImportSettingsWithoutAReEncodingPolicyStoresTheDefault(t *testing.T) {
	store := &fakeSettingsStore{fakeStore: &fakeStore{}}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithDownloadInbox(t.TempDir()), WithEncoder(anEncoder(t)))

	response := putRetention(t, handler, `{"sourceRetention":"keep"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if store.imports.TranscodeEnabled || store.imports.TranscodeTarget != "mp3" ||
		store.imports.TranscodeBitrate != "320" || store.imports.TranscodeWhen != "lossless" {
		t.Fatalf("stored = %#v", store.imports)
	}
}
