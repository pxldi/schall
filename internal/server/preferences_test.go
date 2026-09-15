package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pxldi/schall/internal/db"
	"github.com/rs/zerolog"
)

// preferenceStore is the ordinary store with the one thing these tests are
// about: what the format preferences were saved as.
type preferenceStore struct {
	fakeStore
	saved db.SourcePreferencesRow
}

func (store *preferenceStore) SourcePreferences(context.Context) (db.SourcePreferencesRow, error) {
	return store.saved, nil
}

func (store *preferenceStore) SaveSourcePreferences(
	_ context.Context, saving db.SourcePreferencesRow,
) (db.SourcePreferencesRow, error) {
	store.saved = saving
	return saving, nil
}

// saveFormats sends one attempt at the settings and hands back what the server
// answered.
func saveFormats(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	handler := NewAPI(&preferenceStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPut,
		"/api/v1/settings/source-preferences", strings.NewReader(body)))
	return response
}

// A format cannot be both preferred and refused. That is a setting that cannot
// do what it says, and guessing which half was meant would be worse than saying
// so.
func TestAFormatCannotBePreferredAndRefusedAtOnce(t *testing.T) {
	response := saveFormats(t, `{"preferred":["mp3"],"unacceptable":["mp3"]}`)

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body = %s", response.Code, response.Body)
	}
	if !strings.Contains(response.Body.String(), "both preferred and refused") {
		t.Fatalf("body = %s, want the contradiction named", response.Body)
	}
}

// A preference naming a format no peer ever shares is a setting that silently
// does nothing, which is worse than being told the name was not recognised.
func TestAFormatNobodyKnowsIsRefusedRatherThanStored(t *testing.T) {
	response := saveFormats(t, `{"preferred":["mp4"]}`)

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body = %s", response.Code, response.Body)
	}
	if !strings.Contains(response.Body.String(), "mp4") {
		t.Fatalf("body = %s, want the unknown format named", response.Body)
	}
}

// The floor is in kbps and the table will not hold an absurd one, so the
// refusal reads as a sentence rather than as a database error.
func TestABitrateFloorOutsideWhatCanBeStoredIsRefused(t *testing.T) {
	response := saveFormats(t, `{"minimumBitRate":9000}`)

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body = %s", response.Code, response.Body)
	}
}

// The general floor is held to the same lower bound as a per-format one: below
// 32 kbps there is no music left to keep out, so a smaller number is a typing
// mistake rather than a preference.
func TestTheGeneralFloorBelowWhatAnyMusicUsesIsRefused(t *testing.T) {
	response := saveFormats(t, `{"minimumBitRate":8}`)

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body = %s", response.Code, response.Body)
	}
}

// The same format written two ways is one format, and case and a leading dot
// are the two ways it arrives written differently.
func TestAFormatWrittenTwoWaysIsStoredOnce(t *testing.T) {
	store := &preferenceStore{}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPut,
		"/api/v1/settings/source-preferences",
		strings.NewReader(`{"preferred":[".FLAC","flac","  Flac  "]}`)))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if len(store.saved.Preferred) != 1 || store.saved.Preferred[0] != "flac" {
		t.Fatalf("stored %v, want one lower-case flac", store.saved.Preferred)
	}
}

// A floor above what an encoder reaches refuses every copy that format can ever
// have. That is "never fetch MP3" written so it does not look like it, and the
// unacceptable list says it plainly instead.
func TestAnMP3FloorAboveWhatMP3ReachesIsRefused(t *testing.T) {
	response := saveFormats(t, `{"formatMinimumBitRate":{"mp3":400}}`)

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body = %s", response.Code, response.Body)
	}
	if !strings.Contains(response.Body.String(), "320 kbps") {
		t.Fatalf("body = %s, want the MP3 range named", response.Body)
	}
}

// Below 32 kbps there is no music left to keep out, so a smaller number is a
// typing mistake rather than a preference.
func TestAFloorBelowWhatAnyMusicUsesIsRefused(t *testing.T) {
	response := saveFormats(t, `{"formatMinimumBitRate":{"mp3":8}}`)

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body = %s", response.Code, response.Body)
	}
}

// A lossless format has no bitrate, so a floor on one would never apply. A
// setting that does nothing is worse than being told why it cannot be set.
func TestAFloorOnALosslessFormatIsRefused(t *testing.T) {
	response := saveFormats(t, `{"formatMinimumBitRate":{"flac":320}}`)

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body = %s", response.Code, response.Body)
	}
	if !strings.Contains(response.Body.String(), "no bit rate") {
		t.Fatalf("body = %s, want the reason named", response.Body)
	}
}

// A floor on a format no peer ever shares is a setting that silently does
// nothing, exactly as a preference naming one is.
func TestAFloorOnAFormatNobodyKnowsIsRefused(t *testing.T) {
	response := saveFormats(t, `{"formatMinimumBitRate":{"mp4":320}}`)

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body = %s", response.Code, response.Body)
	}
}

// Zero is how the screen clears a field, so it is stored as no floor rather than
// as a floor of zero.
func TestAFloorSetToZeroIsStoredAsNoFloor(t *testing.T) {
	store := &preferenceStore{}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPut,
		"/api/v1/settings/source-preferences",
		strings.NewReader(`{"formatMinimumBitRate":{"mp3":0}}`)))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if len(store.saved.FormatMinimumBitRate) != 0 {
		t.Fatalf("stored %v, want no floor at all", store.saved.FormatMinimumBitRate)
	}
}

// The floor the owner asked for, stored as it was written.
func TestAnMP3FloorOf320IsStored(t *testing.T) {
	store := &preferenceStore{}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPut,
		"/api/v1/settings/source-preferences",
		strings.NewReader(`{"formatMinimumBitRate":{"MP3":320}}`)))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if store.saved.FormatMinimumBitRate["mp3"] != 320 {
		t.Fatalf("stored %v, want mp3 at 320", store.saved.FormatMinimumBitRate)
	}
}

// The screen draws a floor field only for formats that have a bitrate, and it
// reads which those are from here rather than keeping a second list of its own.
func TestTheSettingsAnswerNamesWhichFormatsCanTakeAFloor(t *testing.T) {
	handler := NewAPI(&preferenceStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/settings/source-preferences", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	body := response.Body.String()
	if !strings.Contains(body, `"mp3":320`) {
		t.Fatalf("body = %s, want MP3 offered a floor up to 320", body)
	}
	if strings.Contains(body, `"flac":`) {
		t.Fatalf("body = %s, want no floor offered on a lossless format", body)
	}
}
