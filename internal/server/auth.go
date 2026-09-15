package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pxldi/schall/internal/db"
)

// The two modes SCHALL_AUTH selects, and what each of them believes.
//
// open serves every request and calls the actor local. It is what Schall did
// before it could authenticate anybody, and it is what `make dev`, the browser
// tests and the seed run under.
//
// proxy serves an /api/ request on one of two credentials: identity headers a
// reverse proxy vouched for, or an app token minted in Settings. Anything else
// is 401. A mode this does not recognise is treated as open, and cannot be
// reached from a deployment: config.Load refuses any other value at startup.
const (
	authOpen  = "open"
	authProxy = "proxy"
)

// How an actor was recognised, as /api/v1/me reports it.
const (
	authKindOpen    = "open"
	authKindBrowser = "browser"
	authKindToken   = "token"
)

// localActor is who made the request when Schall is not asking. It is not a
// username and is not meant to look like one.
const localActor = "local"

// tokenPrefix marks an app token. It lets the middleware take the token path
// without a database lookup, and lets a secret scanner recognise one that has
// leaked into a screenshot or a repository.
const tokenPrefix = "schall_"

// healthPath is served without a credential in every mode. The kubelet's
// probes carry none, and the route says only whether the database answers.
const healthPath = "/api/v1/health"

// proxyKeyHeader is what the reverse proxy adds to say the identity headers
// beside it came through it.
const proxyKeyHeader = "X-Schall-Proxy-Key"

// usernameHeader is the header Authentik's outpost adds to a request it has
// checked. It is an ordinary request header — anything that can reach the pod
// can write it — so it is read only beside a proxy key or a trusted peer.
const usernameHeader = "X-authentik-username"

// tokenUseInterval is how often one token's use is written down. A phone
// polling every fifteen seconds would otherwise cost an UPDATE per request for
// a column nobody reads more precisely than "today".
const tokenUseInterval = time.Minute

// AuthSettings is what the middleware works from: the mode, and the two ways a
// request can show it came through the reverse proxy.
type AuthSettings struct {
	Mode           string
	ProxyKey       string
	TrustedProxies []netip.Prefix
}

// AppTokenStore holds the tokens phones authenticate with.
type AppTokenStore interface {
	AppTokenByHash(context.Context, []byte) (db.AppTokenByHashRow, error)
	CreateAppToken(context.Context, db.CreateAppTokenParams) (db.CreateAppTokenRow, error)
	ListAppTokens(context.Context) ([]db.ListAppTokensRow, error)
	RevokeAppToken(context.Context, uuid.UUID) (uuid.UUID, error)
	TouchAppToken(context.Context, uuid.UUID) error
}

// WithAuth says who Schall believes. Without it the mode is open, which is
// what every installation had before there was a mode.
func WithAuth(settings AuthSettings) Option {
	return func(api *API) {
		api.auth = settings
	}
}

// Actor is who made a request and how Schall knows. Name is an Authentik
// username, the name of a phone, or "local".
type Actor struct {
	Name string
	Auth string
}

type actorKey struct{}

// ActorOf returns who the request was authenticated as. A request that never
// passed the middleware has no actor, and gets the zero one.
func ActorOf(ctx context.Context) Actor {
	holder, _ := ctx.Value(actorKey{}).(*Actor)
	if holder == nil {
		return Actor{}
	}
	return *holder
}

// carryActor puts a holder for the actor on the request, so that a middleware
// further in can name the actor and one further out can read the name it gave.
// The request log line is written after the handler returns and needs the
// actor the middleware inside it found, which a value in a context cannot
// carry outwards.
func carryActor(request *http.Request) (*http.Request, *Actor) {
	if holder, ok := request.Context().Value(actorKey{}).(*Actor); ok {
		return request, holder
	}
	holder := &Actor{}
	return request.WithContext(context.WithValue(request.Context(), actorKey{}, holder)), holder
}

// tokenUses remembers when each token's use was last written down, so the
// write happens about once a minute per token rather than once per request.
// Losing it on restart costs one extra UPDATE per token.
type tokenUses struct {
	mutex sync.Mutex
	last  map[uuid.UUID]time.Time
}

func (uses *tokenUses) due(id uuid.UUID, now time.Time) bool {
	uses.mutex.Lock()
	defer uses.mutex.Unlock()
	if last, ok := uses.last[id]; ok && now.Sub(last) < tokenUseInterval {
		return false
	}
	if uses.last == nil {
		uses.last = make(map[uuid.UUID]time.Time)
	}
	uses.last[id] = now
	return true
}

// authenticate decides who is asking, before middleware.RealIP runs: the
// trusted-network check reads the TCP peer address, and RealIP replaces it
// with whatever X-Forwarded-For said.
//
// Only /api/ is covered, which is what docs/app-plan.md asks for. The frontend
// files are served without a credential: in production Traefik's forward-auth
// stands in front of everything that is not a Bearer request to /api/, so the
// pages are already behind Authentik, and the shell holds no collection data —
// every reading of one goes back through /api/.
func (api *API) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		request, actor := carryActor(request)

		if api.auth.Mode != authProxy {
			*actor = Actor{Name: localActor, Auth: authKindOpen}
			next.ServeHTTP(response, request)
			return
		}
		if !strings.HasPrefix(request.URL.Path, "/api/") || request.URL.Path == healthPath {
			next.ServeHTTP(response, request)
			return
		}

		recognised, ok := api.identify(response, request)
		if !ok {
			return
		}
		*actor = recognised
		next.ServeHTTP(response, request)
	})
}

// identify names the actor, or answers the request itself and returns false.
func (api *API) identify(response http.ResponseWriter, request *http.Request) (Actor, bool) {
	if token, ok := bearerToken(request); ok {
		return api.identifyToken(response, request, token)
	}
	username := strings.TrimSpace(request.Header.Get(usernameHeader))
	if username != "" && api.vouchedFor(request) {
		return Actor{Name: username, Auth: authKindBrowser}, true
	}
	api.unauthorized(response)
	return Actor{}, false
}

func (api *API) identifyToken(
	response http.ResponseWriter, request *http.Request, token string,
) (Actor, bool) {
	if api.tokens == nil || !strings.HasPrefix(token, tokenPrefix) {
		api.unauthorized(response)
		return Actor{}, false
	}
	// The plain token is hashed here and never leaves this function: what the
	// lookup compares, and all the database holds, is the digest.
	digest := sha256.Sum256([]byte(token))
	row, err := api.tokens.AppTokenByHash(request.Context(), digest[:])
	if errors.Is(err, pgx.ErrNoRows) {
		api.unauthorized(response)
		return Actor{}, false
	}
	if err != nil {
		api.internalError(response, request, err)
		return Actor{}, false
	}
	if row.RevokedAt.Valid {
		api.logger.Info().Str("phone", row.Name).Msg("a removed phone asked")
		api.unauthorized(response)
		return Actor{}, false
	}
	api.noteTokenUse(request.Context(), row.ID)
	return Actor{Name: row.Name, Auth: authKindToken}, true
}

// vouchedFor says whether this request came through the reverse proxy: either
// it carries the proxy key, or its peer address is on a network the operator
// named as one.
func (api *API) vouchedFor(request *http.Request) bool {
	if api.auth.ProxyKey != "" {
		presented := request.Header.Get(proxyKeyHeader)
		// Constant time, because a key compared byte by byte can be guessed
		// one byte at a time by whoever can measure the answer.
		if subtle.ConstantTimeCompare([]byte(presented), []byte(api.auth.ProxyKey)) == 1 {
			return true
		}
	}
	return api.auth.trustedPeer(request.RemoteAddr)
}

func (settings AuthSettings) trustedPeer(remote string) bool {
	if len(settings.TrustedProxies) == 0 {
		return false
	}
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	peer = peer.Unmap()
	for _, prefix := range settings.TrustedProxies {
		if prefix.Contains(peer) {
			return true
		}
	}
	return false
}

// noteTokenUse writes down that a token was used, about once a minute. The
// write is detached from the request's own context: an app that walks out of
// range cancels its requests, and those are uses worth recording.
func (api *API) noteTokenUse(ctx context.Context, id uuid.UUID) {
	if !api.tokenUses.due(id, time.Now()) {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := api.tokens.TouchAppToken(ctx, id); err != nil {
		api.logger.Warn().Err(err).Msg("record app token use")
	}
}

// bearerToken returns the Bearer credential when the request carries one. Any
// Bearer header takes the token path, whatever it holds: in production Traefik
// lets a request with one past forward-auth, so its identity headers were
// written by the client and must not be read.
func bearerToken(request *http.Request) (string, bool) {
	const scheme = "Bearer "
	header := request.Header.Get("Authorization")
	if len(header) < len(scheme) || !strings.EqualFold(header[:len(scheme)], scheme) {
		return "", false
	}
	return strings.TrimSpace(header[len(scheme):]), true
}

func (api *API) unauthorized(response http.ResponseWriter) {
	api.problem(response, http.StatusUnauthorized, "not signed in",
		[]string{"Sign in through the browser, or send a token minted in Settings → Phone."})
}

// mintToken makes one app token: the prefix, then 32 random bytes in
// unpadded base64url.
func mintToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return tokenPrefix + base64.RawURLEncoding.EncodeToString(raw), nil
}

type meResponse struct {
	Actor string `json:"actor"`
	Auth  string `json:"auth"`
}

// whoAmI says who Schall took this request to be. The app calls it after a QR
// scan to confirm the token works and to show the name it was given.
func (api *API) whoAmI(response http.ResponseWriter, request *http.Request) {
	actor := ActorOf(request.Context())
	api.writeJSON(response, http.StatusOK, meResponse{Actor: actor.Name, Auth: actor.Auth})
}

type phoneResponse struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	CreatedBy  string     `json:"createdBy"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastUsedAt *time.Time `json:"lastUsedAt"`
}

// mintedPhoneResponse is the one answer that carries the plain token. Nothing
// else ever can: only the hash is stored.
type mintedPhoneResponse struct {
	phoneResponse
	Token string `json:"token"`
	// Server is the address this request arrived on, so the QR code the
	// browser draws can carry somewhere the phone can reach.
	Server string `json:"server"`
}

func (api *API) listPhones(response http.ResponseWriter, request *http.Request) {
	if api.tokens == nil {
		api.problem(response, http.StatusServiceUnavailable, "phones are unavailable",
			[]string{"This installation cannot mint tokens for a phone."})
		return
	}
	rows, err := api.tokens.ListAppTokens(request.Context())
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	phones := make([]phoneResponse, 0, len(rows))
	for _, row := range rows {
		phones = append(phones, phoneResponse{
			ID:         row.ID.String(),
			Name:       row.Name,
			CreatedBy:  row.CreatedBy,
			CreatedAt:  row.CreatedAt.Time,
			LastUsedAt: nullableTime(row.LastUsedAt.Valid, row.LastUsedAt.Time),
		})
	}
	api.writeJSON(response, http.StatusOK, map[string]any{"items": phones})
}

func (api *API) addPhone(response http.ResponseWriter, request *http.Request) {
	if api.tokens == nil {
		api.problem(response, http.StatusServiceUnavailable, "phones are unavailable",
			[]string{"This installation cannot mint tokens for a phone."})
		return
	}
	actor := ActorOf(request.Context())
	if !actor.mayManagePhones() {
		api.phoneCannotDecide(response, "a phone cannot add phones")
		return
	}

	var body struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(response, request, &body); err != nil {
		api.problem(response, http.StatusBadRequest, "invalid request body", []string{err.Error()})
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		api.problem(response, http.StatusBadRequest, "the phone needs a name",
			[]string{"Type a name such as Pixel and press Add phone."})
		return
	}

	token, err := mintToken()
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	digest := sha256.Sum256([]byte(token))
	row, err := api.tokens.CreateAppToken(request.Context(), db.CreateAppTokenParams{
		Name:      name,
		TokenHash: digest[:],
		CreatedBy: actor.Name,
	})
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	api.logger.Info().Str("phone", name).Str("actor", actor.Name).Msg("a phone was added")
	api.writeJSON(response, http.StatusCreated, mintedPhoneResponse{
		phoneResponse: phoneResponse{
			ID:         row.ID.String(),
			Name:       row.Name,
			CreatedBy:  row.CreatedBy,
			CreatedAt:  row.CreatedAt.Time,
			LastUsedAt: nullableTime(row.LastUsedAt.Valid, row.LastUsedAt.Time),
		},
		Token:  token,
		Server: requestOrigin(request),
	})
}

func (api *API) removePhone(response http.ResponseWriter, request *http.Request) {
	if api.tokens == nil {
		api.problem(response, http.StatusServiceUnavailable, "phones are unavailable",
			[]string{"This installation cannot mint tokens for a phone."})
		return
	}
	actor := ActorOf(request.Context())
	if !actor.mayManagePhones() {
		api.phoneCannotDecide(response, "a phone cannot remove phones")
		return
	}
	id, err := uuid.Parse(chi.URLParam(request, "tokenID"))
	if err != nil {
		api.problem(response, http.StatusBadRequest, "invalid phone id", nil)
		return
	}
	if _, err := api.tokens.RevokeAppToken(request.Context(), id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			api.problem(response, http.StatusNotFound, "that phone was already removed", nil)
			return
		}
		api.internalError(response, request, err)
		return
	}
	api.logger.Info().Str("actor", actor.Name).Msg("a phone was removed")
	response.WriteHeader(http.StatusNoContent)
}

// mayManagePhones is the rule ADR 0033 turns on: a stolen phone cannot add
// itself a second credential or take the owner's away.
func (actor Actor) mayManagePhones() bool { return actor.Auth != authKindToken }

func (api *API) phoneCannotDecide(response http.ResponseWriter, title string) {
	api.problem(response, http.StatusForbidden, title,
		[]string{"Open Settings → Phone in a browser."})
}

// requestOrigin is the address this request arrived on, as the phone would
// have to type it. Behind a proxy that is what the proxy was asked for rather
// than what it asked Schall for.
func requestOrigin(request *http.Request) string {
	scheme := "http"
	if request.TLS != nil {
		scheme = "https"
	}
	if forwarded := firstForwarded(request.Header.Get("X-Forwarded-Proto")); forwarded != "" {
		scheme = strings.ToLower(forwarded)
	}
	host := request.Host
	if forwarded := firstForwarded(request.Header.Get("X-Forwarded-Host")); forwarded != "" {
		host = forwarded
	}
	return scheme + "://" + host
}

// firstForwarded takes the first entry of a comma-separated forwarding header.
// A chain of proxies appends to these, and the first entry is the one nearest
// the browser.
func firstForwarded(value string) string {
	first, _, _ := strings.Cut(value, ",")
	return strings.TrimSpace(first)
}
