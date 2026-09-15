package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/dbtest"
	"github.com/rs/zerolog"
)

// The four things docs/app-plan.md says proxy mode has to do, against a real
// table: no credential is refused, a token minted in Settings is served, a
// token that was removed is refused afterwards, and a token cannot mint
// another one.
func TestAppTokensAgainstTheDatabase(t *testing.T) {
	pool := dbtest.Setup(t)
	store := db.New(pool)

	handler := NewAPI(store, pool, &fakeArtistSearcher{}, zerolog.Nop(), WithAuth(AuthSettings{
		Mode:     authProxy,
		ProxyKey: testProxyKey,
	}))
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	browser := map[string]string{
		proxyKeyHeader: testProxyKey,
		usernameHeader: "leo",
		"Content-Type": "application/json",
	}

	response := ask(t, server, http.MethodGet, "/api/v1/me", nil, "")
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no credential got %d, want 401", response.StatusCode)
	}

	response = ask(t, server, http.MethodPost, "/api/v1/settings/phones", browser, `{"name":"Pixel"}`)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("minting got %d, want 201", response.StatusCode)
	}
	var minted mintedPhoneResponse
	if err := json.NewDecoder(response.Body).Decode(&minted); err != nil {
		t.Fatalf("decode the minted phone: %v", err)
	}
	if !strings.HasPrefix(minted.Token, tokenPrefix) {
		t.Fatalf("token = %q, want the %s prefix", minted.Token, tokenPrefix)
	}
	if minted.CreatedBy != "leo" {
		t.Fatalf("created_by = %q, want the browser identity", minted.CreatedBy)
	}

	phone := map[string]string{
		"Authorization": "Bearer " + minted.Token,
		"Content-Type":  "application/json",
	}

	response = ask(t, server, http.MethodGet, "/api/v1/me", phone, "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("a fresh token got %d, want 200", response.StatusCode)
	}
	if said := whoAmISays(t, response); said.Actor != "Pixel" || said.Auth != authKindToken {
		t.Fatalf("/me said %+v, want the phone's name and token auth", said)
	}

	response = ask(t, server, http.MethodPost, "/api/v1/settings/phones", phone, `{"name":"Another"}`)
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("a phone minting a phone got %d, want 403", response.StatusCode)
	}

	response = ask(t, server, http.MethodDelete, "/api/v1/settings/phones/"+minted.ID, browser, "")
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("removing the phone got %d, want 204", response.StatusCode)
	}

	response = ask(t, server, http.MethodGet, "/api/v1/me", phone, "")
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a removed token got %d, want 401", response.StatusCode)
	}

	// And the list Settings reads no longer offers it.
	response = ask(t, server, http.MethodGet, "/api/v1/settings/phones", browser, "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("listing phones got %d, want 200", response.StatusCode)
	}
	var listed struct {
		Items []phoneResponse `json:"items"`
	}
	if err := json.NewDecoder(response.Body).Decode(&listed); err != nil {
		t.Fatalf("decode the phone list: %v", err)
	}
	if len(listed.Items) != 0 {
		t.Fatalf("the list holds %d phones, want the removed one gone", len(listed.Items))
	}
}
