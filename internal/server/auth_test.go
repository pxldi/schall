package server

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pxldi/schall/internal/db"
	"github.com/rs/zerolog"
)

// The credential check, at the door rather than in the database. What is proved
// here is that proxy mode refuses a request nothing vouched for, that it
// believes the identity headers only beside the proxy key or a trusted peer,
// that a token is looked up by hash, and that open mode is what it always was.

const testProxyKey = "a-key-only-traefik-has"

type fakeTokenStore struct {
	*fakeStore
	byHash  map[string]db.AppTokenByHashRow
	lookups int
	minted  db.CreateAppTokenRow
	created db.CreateAppTokenParams
	listed  []db.ListAppTokensRow
	touched []uuid.UUID
	revoked []uuid.UUID
	revoke  error
}

func newTokenStore() *fakeTokenStore {
	return &fakeTokenStore{fakeStore: &fakeStore{}, byHash: map[string]db.AppTokenByHashRow{}}
}

// hold puts a token in the store the way minting would: by its hash.
func (store *fakeTokenStore) hold(token string, row db.AppTokenByHashRow) {
	digest := sha256.Sum256([]byte(token))
	store.byHash[string(digest[:])] = row
}

func (store *fakeTokenStore) AppTokenByHash(_ context.Context, hash []byte) (db.AppTokenByHashRow, error) {
	store.lookups++
	row, ok := store.byHash[string(hash)]
	if !ok {
		return db.AppTokenByHashRow{}, pgx.ErrNoRows
	}
	return row, nil
}

func (store *fakeTokenStore) CreateAppToken(
	_ context.Context, params db.CreateAppTokenParams,
) (db.CreateAppTokenRow, error) {
	store.created = params
	return store.minted, nil
}

func (store *fakeTokenStore) ListAppTokens(context.Context) ([]db.ListAppTokensRow, error) {
	return store.listed, nil
}

func (store *fakeTokenStore) RevokeAppToken(_ context.Context, id uuid.UUID) (uuid.UUID, error) {
	if store.revoke != nil {
		return uuid.Nil, store.revoke
	}
	store.revoked = append(store.revoked, id)
	return id, nil
}

func (store *fakeTokenStore) TouchAppToken(_ context.Context, id uuid.UUID) error {
	store.touched = append(store.touched, id)
	return nil
}

// proxyServer is Schall as it runs in production: proxy mode with the key
// Traefik adds.
func proxyServer(t *testing.T, store *fakeTokenStore, settings AuthSettings) *httptest.Server {
	t.Helper()
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(), WithAuth(settings))
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server
}

func ask(t *testing.T, server *httptest.Server, method, path string, headers map[string]string, body string) *http.Response {
	t.Helper()
	var reader *strings.Reader = strings.NewReader(body)
	request, err := http.NewRequest(method, server.URL+path, reader)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("send request: %v", err)
	}
	t.Cleanup(func() { response.Body.Close() })
	return response
}

func whoAmISays(t *testing.T, response *http.Response) meResponse {
	t.Helper()
	var body meResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode /me: %v", err)
	}
	return body
}

func TestOpenModeServesEveryRequest(t *testing.T) {
	server := proxyServer(t, newTokenStore(), AuthSettings{Mode: authOpen})

	response := ask(t, server, http.MethodGet, "/api/v1/me", nil, "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}
	said := whoAmISays(t, response)
	if said.Actor != localActor || said.Auth != authKindOpen {
		t.Fatalf("/me said %+v, want the local actor with open auth", said)
	}
}

func TestProxyModeRefusesARequestWithNoCredential(t *testing.T) {
	server := proxyServer(t, newTokenStore(), AuthSettings{Mode: authProxy, ProxyKey: testProxyKey})

	response := ask(t, server, http.MethodGet, "/api/v1/me", nil, "")
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", response.StatusCode)
	}
}

func TestProxyModeTakesTheProxyKeyAndTheUsername(t *testing.T) {
	server := proxyServer(t, newTokenStore(), AuthSettings{Mode: authProxy, ProxyKey: testProxyKey})

	response := ask(t, server, http.MethodGet, "/api/v1/me", map[string]string{
		proxyKeyHeader: testProxyKey,
		usernameHeader: "leo",
	}, "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}
	said := whoAmISays(t, response)
	if said.Actor != "leo" || said.Auth != authKindBrowser {
		t.Fatalf("/me said %+v, want leo authenticated as the browser", said)
	}
}

func TestProxyModeRefusesTheWrongProxyKey(t *testing.T) {
	server := proxyServer(t, newTokenStore(), AuthSettings{Mode: authProxy, ProxyKey: testProxyKey})

	// The username header is the one anything reaching the pod can write. It is
	// worth nothing on its own, which is the whole point of the key.
	response := ask(t, server, http.MethodGet, "/api/v1/me", map[string]string{
		proxyKeyHeader: "guessed",
		usernameHeader: "leo",
	}, "")
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", response.StatusCode)
	}
}

func TestProxyModeRefusesTheUsernameAlone(t *testing.T) {
	server := proxyServer(t, newTokenStore(), AuthSettings{Mode: authProxy, ProxyKey: testProxyKey})

	response := ask(t, server, http.MethodGet, "/api/v1/me", map[string]string{
		usernameHeader: "leo",
	}, "")
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", response.StatusCode)
	}
}

func TestProxyModeTrustsAPeerOnANamedNetwork(t *testing.T) {
	// httptest serves on loopback, so loopback stands in for the pod network.
	loopback := netip.MustParsePrefix("127.0.0.0/8")
	server := proxyServer(t, newTokenStore(), AuthSettings{
		Mode:           authProxy,
		TrustedProxies: []netip.Prefix{loopback},
	})

	response := ask(t, server, http.MethodGet, "/api/v1/me", map[string]string{
		usernameHeader: "leo",
		// The address middleware.RealIP would have believed. The check reads
		// the TCP peer instead, which is why the middleware runs before it.
		"X-Forwarded-For": "203.0.113.9",
	}, "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}
	if said := whoAmISays(t, response); said.Actor != "leo" {
		t.Fatalf("/me said %+v, want leo", said)
	}
}

func TestABearerHeaderNeverFallsBackToTheIdentityHeaders(t *testing.T) {
	// Traefik lets any request with a Bearer header past forward-auth, so its
	// X-authentik-* headers are whatever the client wrote. On a trusted network
	// the fallback would have signed that client in as anybody.
	loopback := netip.MustParsePrefix("127.0.0.0/8")
	store := newTokenStore()
	server := proxyServer(t, store, AuthSettings{
		Mode:           authProxy,
		TrustedProxies: []netip.Prefix{loopback},
	})

	response := ask(t, server, http.MethodGet, "/api/v1/me", map[string]string{
		"Authorization": "Bearer not-a-schall-token",
		usernameHeader:  "leo",
	}, "")
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", response.StatusCode)
	}
	if store.lookups != 0 {
		t.Fatalf("lookups = %d, want no lookup for a token without the prefix", store.lookups)
	}
}

func TestProxyModeRefusesAnUnknownToken(t *testing.T) {
	store := newTokenStore()
	server := proxyServer(t, store, AuthSettings{Mode: authProxy, ProxyKey: testProxyKey})

	response := ask(t, server, http.MethodGet, "/api/v1/me", map[string]string{
		"Authorization": "Bearer schall_nothing-was-ever-minted-for-this",
	}, "")
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", response.StatusCode)
	}
	if store.lookups != 1 {
		t.Fatalf("lookups = %d, want the token looked up once by hash", store.lookups)
	}
}

func TestProxyModeTakesAToken(t *testing.T) {
	store := newTokenStore()
	id := uuid.New()
	store.hold("schall_a-minted-token", db.AppTokenByHashRow{ID: id, Name: "Pixel"})
	server := proxyServer(t, store, AuthSettings{Mode: authProxy, ProxyKey: testProxyKey})

	header := map[string]string{"Authorization": "Bearer schall_a-minted-token"}
	response := ask(t, server, http.MethodGet, "/api/v1/me", header, "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}
	said := whoAmISays(t, response)
	if said.Actor != "Pixel" || said.Auth != authKindToken {
		t.Fatalf("/me said %+v, want the phone's name and token auth", said)
	}

	// The second request inside the minute records nothing: the column says
	// which day a phone was last used, and one UPDATE per poll is the cost of
	// saying it to the second.
	ask(t, server, http.MethodGet, "/api/v1/me", header, "")
	if len(store.touched) != 1 {
		t.Fatalf("last_used_at written %d times, want once a minute", len(store.touched))
	}
}

func TestProxyModeRefusesARevokedToken(t *testing.T) {
	store := newTokenStore()
	store.hold("schall_a-removed-phone", db.AppTokenByHashRow{
		ID:        uuid.New(),
		Name:      "Old Pixel",
		RevokedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
	})
	server := proxyServer(t, store, AuthSettings{Mode: authProxy, ProxyKey: testProxyKey})

	response := ask(t, server, http.MethodGet, "/api/v1/me", map[string]string{
		"Authorization": "Bearer schall_a-removed-phone",
	}, "")
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", response.StatusCode)
	}
}

func TestAPhoneCannotAddAPhone(t *testing.T) {
	store := newTokenStore()
	store.hold("schall_a-minted-token", db.AppTokenByHashRow{ID: uuid.New(), Name: "Pixel"})
	server := proxyServer(t, store, AuthSettings{Mode: authProxy, ProxyKey: testProxyKey})

	response := ask(t, server, http.MethodPost, "/api/v1/settings/phones", map[string]string{
		"Authorization": "Bearer schall_a-minted-token",
		"Content-Type":  "application/json",
	}, `{"name":"Another"}`)
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", response.StatusCode)
	}
	if store.created.Name != "" {
		t.Fatalf("minted %q, want nothing minted", store.created.Name)
	}
}

func TestTheBrowserAddsAPhoneAndIsToldTheToken(t *testing.T) {
	store := newTokenStore()
	store.minted = db.CreateAppTokenRow{
		ID:        uuid.New(),
		Name:      "Pixel",
		CreatedBy: "leo",
		CreatedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
	}
	server := proxyServer(t, store, AuthSettings{Mode: authProxy, ProxyKey: testProxyKey})

	response := ask(t, server, http.MethodPost, "/api/v1/settings/phones", map[string]string{
		proxyKeyHeader: testProxyKey,
		usernameHeader: "leo",
		"Content-Type": "application/json",
	}, `{"name":"Pixel"}`)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", response.StatusCode)
	}

	var body mintedPhoneResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode the minted phone: %v", err)
	}
	if !strings.HasPrefix(body.Token, tokenPrefix) {
		t.Fatalf("token = %q, want the %s prefix", body.Token, tokenPrefix)
	}
	if body.Server != server.URL {
		t.Fatalf("server = %q, want the address the request arrived on (%s)", body.Server, server.URL)
	}
	if store.created.CreatedBy != "leo" {
		t.Fatalf("created_by = %q, want the browser identity", store.created.CreatedBy)
	}
	// Only the digest is handed to the database, and it is the digest of the
	// token the browser was given.
	digest := sha256.Sum256([]byte(body.Token))
	if string(store.created.TokenHash) != string(digest[:]) {
		t.Fatal("the stored hash is not the hash of the token that was handed out")
	}
}

func TestTheHealthRouteNeedsNoCredential(t *testing.T) {
	// The kubelet's liveness probe sends nothing. In proxy mode a 401 here
	// would have the pod restarted every thirty seconds.
	server := proxyServer(t, newTokenStore(), AuthSettings{Mode: authProxy, ProxyKey: testProxyKey})

	response := ask(t, server, http.MethodGet, "/api/v1/health", nil, "")
	if response.StatusCode == http.StatusUnauthorized {
		t.Fatal("the health route asked for a credential")
	}
}

func TestTheFrontendIsServedWithoutACredential(t *testing.T) {
	// The plan covers /api/ and nothing else: in production Traefik's
	// forward-auth stands in front of every request that is not a Bearer call
	// to /api/, and the shell holds no collection data.
	server := proxyServer(t, newTokenStore(), AuthSettings{Mode: authProxy, ProxyKey: testProxyKey})

	response := ask(t, server, http.MethodGet, "/", nil, "")
	if response.StatusCode == http.StatusUnauthorized {
		t.Fatal("the frontend asked for a credential; only /api/ is covered")
	}
}
