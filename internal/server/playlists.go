package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pxldi/schall/internal/acquisition"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/navidrome"
	"github.com/pxldi/schall/internal/playlists"
)

// PlaylistService is what the handlers ask of playlist ingestion.
type PlaylistService interface {
	List(context.Context) ([]db.PlaylistRow, error)
	Get(context.Context, uuid.UUID) (db.PlaylistRow, []db.PlaylistEntryRow, error)
	Follow(ctx context.Context, link string) (db.PlaylistRow, error)
	QueueImport(context.Context, uuid.UUID) (db.PlaylistRow, error)
	WantEntry(ctx context.Context, playlistID, entryID uuid.UUID) error
	ImportFile(ctx context.Context, filename, name string, content []byte) (db.PlaylistRow, error)
	Delete(context.Context, uuid.UUID) error
	Connect(ctx context.Context, code, redirectURI string) (db.SpotifySettingsRow, error)
	AuthorizeURL(ctx context.Context, redirectURI, state string) (string, error)
}

// NavidromePlaylistSyncStore queues sync passes. Queueing is all the API does
// with sync: a pass talks to the player and edits playlists, which is work for
// the job lane and not for a request somebody is waiting on.
type NavidromePlaylistSyncStore interface {
	QueueNavidromePlaylistSync(context.Context, uuid.UUID) error
	QueueNavidromePlaylistSweep(context.Context, time.Time) error
}

// PlayerPairing answers what the player holds for one list. It talks to the
// player inside the request, which is why it is the one thing about sync the
// API does not hand to the job lane: it writes nothing, and somebody is waiting
// on the answer rather than on the work.
type PlayerPairing interface {
	PlaylistPairing(context.Context, uuid.UUID) (navidrome.PairingReport, error)
}

// SpotifySettingsStore is the settings persistence the handlers read and write.
type SpotifySettingsStore interface {
	SpotifySettings(context.Context) (db.SpotifySettingsRow, error)
	SaveSpotifySettings(ctx context.Context, clientID, clientSecret string) (db.SpotifySettingsRow, error)
}

// oauthStates holds the CSRF tokens handed to browsers that were sent off to
// authorize. In memory on purpose: a state outliving a restart would mean the
// approval took longer than anybody sits on a consent page.
type oauthStates struct {
	mu     sync.Mutex
	issued map[string]time.Time
}

const oauthStateLifetime = 15 * time.Minute

func (states *oauthStates) issue() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("issue OAuth state: %w", err)
	}
	state := hex.EncodeToString(raw)
	states.mu.Lock()
	defer states.mu.Unlock()
	if states.issued == nil {
		states.issued = make(map[string]time.Time)
	}
	for token, issuedAt := range states.issued {
		if time.Since(issuedAt) > oauthStateLifetime {
			delete(states.issued, token)
		}
	}
	states.issued[state] = time.Now()
	return state, nil
}

func (states *oauthStates) redeem(state string) bool {
	states.mu.Lock()
	defer states.mu.Unlock()
	issuedAt, found := states.issued[state]
	if found {
		delete(states.issued, state)
	}
	return found && time.Since(issuedAt) <= oauthStateLifetime
}

func (api *API) playlistsAvailable(response http.ResponseWriter) bool {
	if api.playlists == nil || api.spotify == nil {
		api.problem(response, http.StatusServiceUnavailable, "playlists are not configured", nil)
		return false
	}
	return true
}

type spotifySettingsResponse struct {
	Configured       bool       `json:"configured"`
	ClientID         string     `json:"clientId"`
	ClientSecretSet  bool       `json:"clientSecretSet"`
	Connected        bool       `json:"connected"`
	AccountName      *string    `json:"accountName,omitempty"`
	ConnectionStatus string     `json:"connectionStatus"`
	ConnectionError  *string    `json:"connectionError,omitempty"`
	LastCheckedAt    *time.Time `json:"lastCheckedAt"`
	// RedirectURI is what must be registered on the Spotify application for
	// the connect flow to come back, shown so nobody has to derive it by hand.
	RedirectURI string `json:"redirectUri"`
}

// spotifyResponse never includes the secret or any token; callers learn only
// that they are stored.
func spotifyResponse(row db.SpotifySettingsRow, redirectURI string) spotifySettingsResponse {
	return spotifySettingsResponse{
		Configured:       true,
		ClientID:         row.ClientID,
		ClientSecretSet:  strings.TrimSpace(row.ClientSecret) != "",
		Connected:        row.RefreshToken.Valid && strings.TrimSpace(row.RefreshToken.String) != "",
		AccountName:      textPointer(row.AccountName),
		ConnectionStatus: row.ConnectionStatus,
		ConnectionError:  textPointer(row.ConnectionError),
		LastCheckedAt:    nullableTime(row.LastCheckedAt.Valid, row.LastCheckedAt.Time),
		RedirectURI:      redirectURI,
	}
}

// spotifyRedirectURI is where Spotify sends the browser back: this API's own
// callback, addressed as the browser addressed us, because that is the address
// the operator can register on the application.
func spotifyRedirectURI(request *http.Request) string {
	scheme := "http"
	if request.TLS != nil || strings.EqualFold(request.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return scheme + "://" + request.Host + "/api/v1/spotify/callback"
}

func (api *API) getSpotifySettings(response http.ResponseWriter, request *http.Request) {
	if !api.playlistsAvailable(response) {
		return
	}
	row, err := api.spotify.SpotifySettings(request.Context())
	if errors.Is(err, pgx.ErrNoRows) {
		api.writeJSON(response, http.StatusOK, spotifySettingsResponse{
			ConnectionStatus: "unknown",
			RedirectURI:      spotifyRedirectURI(request),
		})
		return
	}
	if err != nil {
		api.problem(response, http.StatusInternalServerError, "read Spotify settings", nil)
		return
	}
	api.writeJSON(response, http.StatusOK, spotifyResponse(row, spotifyRedirectURI(request)))
}

func (api *API) saveSpotifySettings(response http.ResponseWriter, request *http.Request) {
	if !api.playlistsAvailable(response) {
		return
	}
	var input struct {
		ClientID     string `json:"clientId"`
		ClientSecret string `json:"clientSecret"`
	}
	if err := decodeJSON(response, request, &input); err != nil {
		api.problem(response, http.StatusBadRequest, "invalid request body", []string{err.Error()})
		return
	}
	input.ClientID = strings.TrimSpace(input.ClientID)
	input.ClientSecret = strings.TrimSpace(input.ClientSecret)
	if input.ClientID == "" {
		api.problem(response, http.StatusUnprocessableEntity, "invalid Spotify settings",
			[]string{"clientId is required"})
		return
	}
	if input.ClientSecret == "" {
		// An omitted secret keeps the stored one — but only if one is stored.
		if _, err := api.spotify.SpotifySettings(request.Context()); errors.Is(err, pgx.ErrNoRows) {
			api.problem(response, http.StatusUnprocessableEntity, "invalid Spotify settings",
				[]string{"clientSecret is required"})
			return
		}
	}
	row, err := api.spotify.SaveSpotifySettings(request.Context(), input.ClientID, input.ClientSecret)
	if err != nil {
		api.problem(response, http.StatusInternalServerError, "save Spotify settings", nil)
		return
	}
	api.writeJSON(response, http.StatusOK, spotifyResponse(row, spotifyRedirectURI(request)))
}

// authorizeSpotify hands the browser the URL to approve access at, with a
// state this server will insist on seeing again.
func (api *API) authorizeSpotify(response http.ResponseWriter, request *http.Request) {
	if !api.playlistsAvailable(response) {
		return
	}
	state, err := api.oauthStates.issue()
	if err != nil {
		api.problem(response, http.StatusInternalServerError, "prepare authorization", nil)
		return
	}
	authorizeURL, err := api.playlists.AuthorizeURL(request.Context(), spotifyRedirectURI(request), state)
	if errors.Is(err, playlists.ErrNotConfigured) {
		api.problem(response, http.StatusConflict, "enter the Spotify client credentials first", nil)
		return
	}
	if err != nil {
		api.problem(response, http.StatusInternalServerError, "prepare authorization", nil)
		return
	}
	api.writeJSON(response, http.StatusOK, map[string]string{"url": authorizeURL})
}

// spotifyCallback is where Spotify sends the browser back. It is a browser
// navigation, not an API call, so both outcomes end at the settings page.
func (api *API) spotifyCallback(response http.ResponseWriter, request *http.Request) {
	if api.playlists == nil {
		http.Error(response, "playlists are not configured", http.StatusServiceUnavailable)
		return
	}
	query := request.URL.Query()
	// Spotify sends the browser back here, and this is where it goes next. The
	// settings screen is five addresses now, and the account this callback
	// settles is on Sources — which is also where the notice below is read, so
	// landing anywhere else would report nothing at all.
	settingsPage := func(result string) string { return "/settings/sources?spotify=" + result }
	if !api.oauthStates.redeem(query.Get("state")) {
		http.Redirect(response, request, settingsPage("state-mismatch"), http.StatusFound)
		return
	}
	if query.Get("error") != "" || query.Get("code") == "" {
		http.Redirect(response, request, settingsPage("denied"), http.StatusFound)
		return
	}
	if _, err := api.playlists.Connect(request.Context(), query.Get("code"), spotifyRedirectURI(request)); err != nil {
		api.logger.Error().Err(err).Msg("connect spotify account")
		http.Redirect(response, request, settingsPage("failed"), http.StatusFound)
		return
	}
	http.Redirect(response, request, settingsPage("connected"), http.StatusFound)
}

type playlistResponse struct {
	ID             uuid.UUID  `json:"id"`
	Source         string     `json:"source"`
	SourceID       *string    `json:"sourceId,omitempty"`
	SourceRevision *string    `json:"sourceRevision,omitempty"`
	Name           string     `json:"name"`
	Description    string     `json:"description"`
	OwnerName      string     `json:"ownerName"`
	TrackCount     int32      `json:"trackCount"`
	EntryCount     int64      `json:"entryCount"`
	OwnedCount     int64      `json:"ownedCount"`
	ImportedAt     *time.Time `json:"importedAt"`
	CreatedAt      *time.Time `json:"createdAt"`
}

func toPlaylistResponse(row db.PlaylistRow) playlistResponse {
	return playlistResponse{
		ID:             row.ID,
		Source:         row.Source,
		SourceID:       textPointer(row.SourceID),
		SourceRevision: textPointer(row.SourceRevision),
		Name:           row.Name,
		Description:    row.Description,
		OwnerName:      row.OwnerName,
		TrackCount:     row.TrackCount,
		EntryCount:     row.EntryCount,
		OwnedCount:     row.OwnedCount,
		ImportedAt:     nullableTime(row.ImportedAt.Valid, row.ImportedAt.Time),
		CreatedAt:      nullableTime(row.CreatedAt.Valid, row.CreatedAt.Time),
	}
}

type playlistEntryResponse struct {
	ID            uuid.UUID  `json:"id"`
	Position      int32      `json:"position"`
	Artist        string     `json:"artist"`
	Title         string     `json:"title"`
	Album         string     `json:"album"`
	DurationMS    *int32     `json:"durationMs,omitempty"`
	ISRC          *string    `json:"isrc,omitempty"`
	OwnedFileID   *uuid.UUID `json:"ownedFileId,omitempty"`
	TargetID      *uuid.UUID `json:"targetId,omitempty"`
	TargetStatus  *string    `json:"targetStatus,omitempty"`
	TargetSummary *string    `json:"targetSummary,omitempty"`
	// MusicBrainzSeed is the release editor filled in for this entry, and it is
	// here only for the entries MusicBrainz has had nothing for. Its presence is
	// the whole signal: a reader cannot tell an entry waiting for its first
	// attempt from one that has reached a dead end, both being 'unresolved', and
	// the interface must never offer this where it is not the thing that helps.
	MusicBrainzSeed *musicbrainzSeedResponse `json:"musicbrainzSeed,omitempty"`
}

// The release editor as a form somebody can submit: where it goes and every
// field it carries. It is sent as data rather than as an address because the
// editor only reads a seed out of a request body, and because nothing about this
// is Schall talking to MusicBrainz — the browser posts it when the person says
// so, and never before.
type musicbrainzSeedResponse struct {
	URL    string              `json:"url"`
	Fields []seedFieldResponse `json:"fields"`
}

type seedFieldResponse struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

func (api *API) listPlaylists(response http.ResponseWriter, request *http.Request) {
	if !api.playlistsAvailable(response) {
		return
	}
	rows, err := api.playlists.List(request.Context())
	if err != nil {
		api.problem(response, http.StatusInternalServerError, "list playlists", nil)
		return
	}
	items := make([]playlistResponse, 0, len(rows))
	for _, row := range rows {
		items = append(items, toPlaylistResponse(row))
	}
	api.writeJSON(response, http.StatusOK, map[string]any{"items": items})
}

func (api *API) followPlaylist(response http.ResponseWriter, request *http.Request) {
	if !api.playlistsAvailable(response) {
		return
	}
	var input struct {
		URL string `json:"url"`
	}
	if err := decodeJSON(response, request, &input); err != nil {
		api.problem(response, http.StatusBadRequest, "invalid request body", []string{err.Error()})
		return
	}
	playlist, err := api.playlists.Follow(request.Context(), input.URL)
	switch {
	case errors.Is(err, playlists.ErrNotConfigured):
		api.problem(response, http.StatusConflict, "enter the Spotify client credentials first", nil)
	case errors.Is(err, playlists.ErrNotConnected):
		api.problem(response, http.StatusConflict, "connect a Spotify account first", nil)
	case err != nil:
		api.problem(response, http.StatusUnprocessableEntity, "follow playlist", []string{err.Error()})
	default:
		api.writeJSON(response, http.StatusCreated, toPlaylistResponse(playlist))
	}
}

func (api *API) getPlaylist(response http.ResponseWriter, request *http.Request) {
	if !api.playlistsAvailable(response) {
		return
	}
	id, err := uuid.Parse(chi.URLParam(request, "playlistID"))
	if err != nil {
		api.problem(response, http.StatusBadRequest, "invalid playlist id", nil)
		return
	}
	playlist, entries, err := api.playlists.Get(request.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		api.problem(response, http.StatusNotFound, "playlist not found", nil)
		return
	}
	if err != nil {
		api.problem(response, http.StatusInternalServerError, "read playlist", nil)
		return
	}
	artistIDs := api.artistIDsForSeeding(request.Context(), id, entries)
	items := make([]playlistEntryResponse, 0, len(entries))
	for _, entry := range entries {
		item := playlistEntryResponse{
			ID:       entry.ID,
			Position: entry.Position,
			Artist:   entry.EntryArtist,
			Title:    entry.EntryTitle,
			Album:    entry.EntryAlbum,
			ISRC:     textPointer(entry.EntryISRC),
		}
		if entry.EntryDurationMS.Valid {
			duration := entry.EntryDurationMS.Int32
			item.DurationMS = &duration
		}
		// The answering file, not the stored ownership record: a want Schall
		// fetched and imported is music the user has, whether or not a list
		// import has run since it landed. This is the same reading the owned
		// count on the playlist is made of, so a page cannot say one thing in
		// its header and another on its rows.
		if entry.AnsweringFileID.Valid {
			owned := entry.AnsweringFileID.UUID
			item.OwnedFileID = &owned
		}
		if entry.TargetID.Valid {
			target := entry.TargetID.UUID
			item.TargetID = &target
			item.TargetStatus = textPointer(entry.TargetStatus)
			item.TargetSummary = textPointer(entry.TargetSummary)
			item.MusicBrainzSeed = seedFor(entry, playlist.Source, artistIDs)
		}
		items = append(items, item)
	}
	api.writeJSON(response, http.StatusOK, map[string]any{
		"playlist": toPlaylistResponse(playlist),
		"entries":  items,
	})
}

// artistIDsForSeeding asks the catalogue which of the credited artists it already
// holds a MusicBrainz identifier for.
//
// Only the entries at a dead end are asked about, because they are the only ones
// offered a form. A failure is logged and nothing else: the form still opens, and
// without an identifier it asks the person to search for the artist, which is
// where a credit line alone would have left it anyway.
func (api *API) artistIDsForSeeding(
	ctx context.Context, playlistID uuid.UUID, entries []db.PlaylistEntryRow,
) map[string]uuid.UUID {
	var names []string
	for _, entry := range entries {
		if acquisition.MusicBrainzHasNothing(entry.TargetStatus.String, entry.TargetSummary.String) {
			names = append(names, acquisition.CreditNames(entry.EntryArtist)...)
		}
	}
	if len(names) == 0 {
		return nil
	}
	identified, err := api.store.ArtistMusicBrainzIDsByName(ctx, names)
	if err != nil {
		api.logger.Warn().Err(err).Str("playlist_id", playlistID.String()).
			Msg("read artist identifiers for a release seed")
		return nil
	}
	return identified
}

// seedFor fills MusicBrainz's release editor in for an entry MusicBrainz has
// nothing for, and answers with nothing for every other entry.
//
// This is the one action that changes such an entry's outcome. Schall cannot
// acquire what MusicBrainz holds no recording for — the chain that admits a copy
// ends at a recording identifier — so asking again is not what is missing;
// somebody adding the release is.
func seedFor(
	entry db.PlaylistEntryRow, source string, artistIDs map[string]uuid.UUID,
) *musicbrainzSeedResponse {
	if !acquisition.MusicBrainzHasNothing(entry.TargetStatus.String, entry.TargetSummary.String) {
		return nil
	}
	return seededRelease(acquisition.SeedEntry{
		Artist:     entry.EntryArtist,
		Title:      entry.EntryTitle,
		Album:      entry.EntryAlbum,
		DurationMS: int(entry.EntryDurationMS.Int32),
		ISRC:       entry.EntryISRC.String,
		Source:     listingSource(source),
	}, artistIDs)
}

// seededRelease is the release editor filled in, as something a browser can
// post. It is offered on the list's own page, beside the entry it is about,
// which is where somebody browsing what a list did and did not find already is.
//
// Nothing comes back where the entry cannot name a release, which is the one
// field MusicBrainz insists on.
func seededRelease(
	entry acquisition.SeedEntry, artistIDs map[string]uuid.UUID,
) *musicbrainzSeedResponse {
	seed, ok := acquisition.SeedRelease(entry, artistIDs)
	if !ok {
		return nil
	}
	fields := make([]seedFieldResponse, 0, len(seed.Fields))
	for _, field := range seed.Fields {
		fields = append(fields, seedFieldResponse{Name: field.Name, Value: field.Value})
	}
	return &musicbrainzSeedResponse{URL: seed.URL, Fields: fields}
}

// listingSource names where a list's metadata came from, for an edit note a
// MusicBrainz editor will read. A source Schall has not learnt to name yet says
// only that it was a playlist, which is true of all of them.
func listingSource(source string) string {
	switch source {
	case "spotify":
		return "a Spotify playlist"
	case "navidrome":
		return "a Navidrome playlist"
	}
	return "a playlist"
}

func (api *API) importPlaylist(response http.ResponseWriter, request *http.Request) {
	if !api.playlistsAvailable(response) {
		return
	}
	id, err := uuid.Parse(chi.URLParam(request, "playlistID"))
	if err != nil {
		api.problem(response, http.StatusBadRequest, "invalid playlist id", nil)
		return
	}
	playlist, err := api.playlists.QueueImport(request.Context(), id)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		api.problem(response, http.StatusNotFound, "playlist not found", nil)
	case errors.Is(err, playlists.ErrNotSpotify):
		api.problem(response, http.StatusConflict, "only a Spotify playlist can be imported", nil)
	case err != nil:
		api.problem(response, http.StatusInternalServerError, "queue playlist import", nil)
	default:
		api.writeJSON(response, http.StatusAccepted, toPlaylistResponse(playlist))
	}
}

// wantPlaylistEntry creates a want for one entry by hand. An import creates a
// target for every unowned entry on its own, but a list adopted as a file is
// never re-reconciled against a live source, so its entries have no other way
// to gain one.
func (api *API) wantPlaylistEntry(response http.ResponseWriter, request *http.Request) {
	if !api.playlistsAvailable(response) {
		return
	}
	playlistID, err := uuid.Parse(chi.URLParam(request, "playlistID"))
	if err != nil {
		api.problem(response, http.StatusBadRequest, "invalid playlist id", nil)
		return
	}
	entryID, err := uuid.Parse(chi.URLParam(request, "entryID"))
	if err != nil {
		api.problem(response, http.StatusBadRequest, "invalid entry id", nil)
		return
	}
	err = api.playlists.WantEntry(request.Context(), playlistID, entryID)
	switch {
	case errors.Is(err, pgx.ErrNoRows), errors.Is(err, playlists.ErrEntryNotFound):
		api.problem(response, http.StatusNotFound, "playlist entry not found", nil)
	case err != nil:
		api.problem(response, http.StatusInternalServerError, "want playlist entry", nil)
	default:
		api.writeJSON(response, http.StatusOK, map[string]any{"status": "wanted"})
	}
}

// A playlist can also arrive as a file. Two requests do it: one reads the file
// and answers with what it says, and one imports it. Both are given the file
// itself — the second never trusts rows the first sent to a browser, because an
// entry is evidence and evidence comes from the file.
//
// maxPlaylistFileBytes is what a request carrying such a file may weigh. A
// playlist of ten thousand songs written as text is far under this; anything
// above it is not a playlist.
const maxPlaylistFileBytes = 5 << 20

// playlistFileRowResponse is one row as the file described it, for the preview
// to show before anything is created.
type playlistFileRowResponse struct {
	Position   int    `json:"position"`
	Artist     string `json:"artist"`
	Title      string `json:"title"`
	Album      string `json:"album"`
	DurationMS int    `json:"durationMs"`
	ISRC       string `json:"isrc"`
	// Path is a file the row named. Whether the library holds it is not asked
	// here; the import asks.
	Path string `json:"path"`
}

type playlistFileSkippedResponse struct {
	Line   int    `json:"line"`
	Reason string `json:"reason"`
}

// playlistFilePreviewResponse is one file read and nothing done: the name the
// list would take, the shape it was read as, the fields it was found to carry,
// and every row.
type playlistFilePreviewResponse struct {
	Name     string                        `json:"name"`
	Format   string                        `json:"format"`
	Columns  []string                      `json:"columns"`
	RowCount int                           `json:"rowCount"`
	Rows     []playlistFileRowResponse     `json:"rows"`
	Skipped  []playlistFileSkippedResponse `json:"skipped"`
}

// readPlaylistFile takes the file out of the request, or answers the caller
// and reports that it did.
func (api *API) readPlaylistFile(
	response http.ResponseWriter, request *http.Request,
) (filename string, content []byte, listName string, ok bool) {
	if request.ContentLength > maxPlaylistFileBytes {
		api.problem(response, http.StatusRequestEntityTooLarge, "this file is too large",
			[]string{"A playlist file may be up to 5 MB."})
		return "", nil, "", false
	}
	request.Body = http.MaxBytesReader(response, request.Body, maxPlaylistFileBytes)
	if err := request.ParseMultipartForm(maxPlaylistFileBytes); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			api.problem(response, http.StatusRequestEntityTooLarge, "this file is too large",
				[]string{"A playlist file may be up to 5 MB."})
			return "", nil, "", false
		}
		api.problem(response, http.StatusBadRequest,
			"send the playlist file as multipart/form-data", []string{err.Error()})
		return "", nil, "", false
	}
	file, header, err := request.FormFile("file")
	if err != nil {
		api.problem(response, http.StatusBadRequest, "choose a playlist file to import",
			[]string{err.Error()})
		return "", nil, "", false
	}
	defer file.Close()
	body, err := io.ReadAll(file)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			api.problem(response, http.StatusRequestEntityTooLarge, "this file is too large",
				[]string{"A playlist file may be up to 5 MB."})
			return "", nil, "", false
		}
		api.problem(response, http.StatusBadRequest, "read the playlist file", []string{err.Error()})
		return "", nil, "", false
	}
	return header.Filename, body, strings.TrimSpace(request.FormValue("name")), true
}

// refusePlaylistFile turns what the reading refused into a status and words
// somebody can act on.
func (api *API) refusePlaylistFile(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, playlists.ErrTooManyRows):
		api.problem(response, http.StatusUnprocessableEntity, "this playlist is too long",
			[]string{fmt.Sprintf("A playlist file may hold up to %d songs. Split it and import each part.",
				playlists.MaxFileRows)})
	case errors.Is(err, playlists.ErrEmptyFile):
		api.problem(response, http.StatusUnprocessableEntity, "this file holds no songs",
			[]string{"Every row was empty. Check the file and choose it again."})
	case errors.Is(err, playlists.ErrUnreadableFile):
		api.problem(response, http.StatusUnprocessableEntity, "this file cannot be read as a playlist",
			[]string{"Import a CSV with named columns, a two-column artist and title list, or an M3U.",
				err.Error()})
	default:
		api.problem(response, http.StatusInternalServerError, "import the playlist file", nil)
	}
}

// previewPlaylistFile reads an uploaded file and says what is in it. It writes
// nothing: this is the step somebody looks at before deciding.
func (api *API) previewPlaylistFile(response http.ResponseWriter, request *http.Request) {
	if !api.playlistsAvailable(response) {
		return
	}
	filename, content, _, ok := api.readPlaylistFile(response, request)
	if !ok {
		return
	}
	parsed, err := playlists.ParseFile(filename, content)
	if err != nil {
		api.refusePlaylistFile(response, err)
		return
	}
	body := playlistFilePreviewResponse{
		Name:     parsed.Name,
		Format:   parsed.Format,
		Columns:  parsed.Columns,
		RowCount: len(parsed.Rows),
		Rows:     make([]playlistFileRowResponse, 0, len(parsed.Rows)),
		Skipped:  make([]playlistFileSkippedResponse, 0, len(parsed.Skipped)),
	}
	if body.Columns == nil {
		body.Columns = []string{}
	}
	for _, row := range parsed.Rows {
		body.Rows = append(body.Rows, playlistFileRowResponse{
			Position: row.Position, Artist: row.Artist, Title: row.Title,
			Album: row.Album, DurationMS: row.DurationMS, ISRC: row.ISRC,
			Path: row.LibraryPath,
		})
	}
	for _, skipped := range parsed.Skipped {
		body.Skipped = append(body.Skipped, playlistFileSkippedResponse{
			Line: skipped.Line, Reason: skipped.Reason,
		})
	}
	api.writeJSON(response, http.StatusOK, body)
}

// importPlaylistFile makes the list. It reads the file again rather than
// taking the preview back from the browser, and importing the same file twice
// lands on the same list with the same entries.
func (api *API) importPlaylistFile(response http.ResponseWriter, request *http.Request) {
	if !api.playlistsAvailable(response) {
		return
	}
	filename, content, listName, ok := api.readPlaylistFile(response, request)
	if !ok {
		return
	}
	playlist, err := api.playlists.ImportFile(request.Context(), filename, listName, content)
	if err != nil {
		api.logger.Warn().Err(err).Str("file", filename).Msg("import a playlist file")
		api.refusePlaylistFile(response, err)
		return
	}
	api.writeJSON(response, http.StatusCreated, toPlaylistResponse(playlist))
}

// syncPlaylistToNavidrome asks for one pass over one list. It answers 202 the
// moment the pass is queued: what it will do depends on what the player says,
// and nobody is kept waiting on a conversation with it.
func (api *API) syncPlaylistToNavidrome(response http.ResponseWriter, request *http.Request) {
	if api.playerSync == nil {
		api.problem(response, http.StatusServiceUnavailable, "playlist sync is not configured", nil)
		return
	}
	id, err := uuid.Parse(chi.URLParam(request, "playlistID"))
	if err != nil {
		api.problem(response, http.StatusBadRequest, "invalid playlist id", nil)
		return
	}
	err = api.playerSync.QueueNavidromePlaylistSync(request.Context(), id)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		api.problem(response, http.StatusNotFound, "playlist not found", nil)
	case err != nil:
		api.problem(response, http.StatusInternalServerError, "queue navidrome playlist sync", nil)
	default:
		api.writeJSON(response, http.StatusAccepted, map[string]string{"status": "queued"})
	}
}

// playerPairingResponse is what the player was found to hold for one list.
//
// The three counts are deliberately all here. A page that shows only the last
// one leaves a reader working out why eight of thirty-eight tracks reached the
// player when fourteen are owned, and the answer is that those are three
// different questions: how long the list is, how much of it Schall has a file
// for — which includes copies acquired since the last import and so counted as
// owned nowhere yet — and how much of that the player answered for.
type playerPairingResponse struct {
	Configured    bool  `json:"configured"`
	EntryCount    int64 `json:"entryCount"`
	AcquiredCount int   `json:"acquiredCount"`
	Checked       int   `json:"checked"`
	PairedCount   int   `json:"pairedCount"`
	// Pushed and LastPushedAt are the last push as it was recorded, which is
	// the one part of this that is not being read live.
	Pushed           int                    `json:"pushed"`
	LastPushedAt     *time.Time             `json:"lastPushedAt"`
	RemotePlaylistID string                 `json:"remotePlaylistId,omitempty"`
	Unpaired         []unpairedFileResponse `json:"unpaired"`
}

// unpairedFileResponse is one file the player did not answer for. It carries
// the search behind the refusal as well as the file, because "Navidrome does
// not have this" and "Schall asked for it in words Navidrome does not file it
// under" look the same from the outside and are not the same fault.
type unpairedFileResponse struct {
	EntryID uuid.UUID `json:"entryId"`
	// Position places it in the playlist, so the row can be read against the
	// entry the page is already showing.
	Position int32  `json:"position"`
	Path     string `json:"path"`
	// PathEscaped is the same path with its non-ASCII runes escaped and its
	// length in bytes, so a difference that does not survive being displayed is
	// still visible somewhere.
	PathEscaped string   `json:"pathEscaped"`
	Reason      string   `json:"reason"`
	Searched    []string `json:"searched"`
	Candidates  int      `json:"candidates"`
	Offered     []string `json:"offered"`
}

// playlistPlayerPairing reports which of a list's files the player answers for,
// by asking it now rather than by remembering the last answer.
func (api *API) playlistPlayerPairing(response http.ResponseWriter, request *http.Request) {
	if api.playerPairing == nil {
		api.problem(response, http.StatusServiceUnavailable, "playlist sync is not configured", nil)
		return
	}
	id, err := uuid.Parse(chi.URLParam(request, "playlistID"))
	if err != nil {
		api.problem(response, http.StatusBadRequest, "invalid playlist id", nil)
		return
	}
	report, err := api.playerPairing.PlaylistPairing(request.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		api.problem(response, http.StatusNotFound, "playlist not found", nil)
		return
	}
	if err != nil {
		// The player's own words: this is the page somebody opens because the
		// player is not behaving, so what it said belongs in front of them.
		api.logger.Warn().Err(err).Str("playlist_id", id.String()).Msg("read the player pairing")
		api.problem(response, http.StatusBadGateway, "ask the player about this playlist",
			[]string{err.Error()})
		return
	}
	api.writeJSON(response, http.StatusOK, toPlayerPairingResponse(report))
}

func toPlayerPairingResponse(report navidrome.PairingReport) playerPairingResponse {
	body := playerPairingResponse{
		Configured:       report.Configured,
		EntryCount:       report.EntryCount,
		AcquiredCount:    report.AcquiredCount,
		Checked:          report.Checked,
		PairedCount:      report.PairedCount,
		Pushed:           report.Pushed,
		LastPushedAt:     nullableTime(!report.LastPushedAt.IsZero(), report.LastPushedAt),
		RemotePlaylistID: report.RemotePlaylistID,
		Unpaired:         make([]unpairedFileResponse, 0, len(report.Unpaired)),
	}
	for _, track := range report.Unpaired {
		file := unpairedFileResponse{
			EntryID:     track.EntryID,
			Position:    track.Position,
			Path:        track.RemotePath,
			PathEscaped: navidrome.Escaped(track.RemotePath),
			Reason:      string(track.Reason),
			Searched:    make([]string, 0, len(track.Attempts)),
			Offered:     []string{},
		}
		for _, attempt := range track.Attempts {
			file.Searched = append(file.Searched, attempt.Term)
			file.Candidates += attempt.Candidates
			file.Offered = append(file.Offered, attempt.Evidence...)
		}
		body.Unpaired = append(body.Unpaired, file)
	}
	return body
}

// syncNavidromePlaylists asks for a look at the whole player: the lists it
// holds that Schall has not joined, and a pass over every want list after that.
func (api *API) syncNavidromePlaylists(response http.ResponseWriter, request *http.Request) {
	if api.playerSync == nil {
		api.problem(response, http.StatusServiceUnavailable, "playlist sync is not configured", nil)
		return
	}
	// Now, not on the sweep's own schedule: a pass waiting six hours out is not
	// an answer to somebody pressing this.
	if err := api.playerSync.QueueNavidromePlaylistSweep(request.Context(), time.Now()); err != nil {
		api.problem(response, http.StatusInternalServerError, "queue navidrome playlist sync", nil)
		return
	}
	api.writeJSON(response, http.StatusAccepted, map[string]string{"status": "queued"})
}

func (api *API) deletePlaylist(response http.ResponseWriter, request *http.Request) {
	if !api.playlistsAvailable(response) {
		return
	}
	id, err := uuid.Parse(chi.URLParam(request, "playlistID"))
	if err != nil {
		api.problem(response, http.StatusBadRequest, "invalid playlist id", nil)
		return
	}
	err = api.playlists.Delete(request.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		api.problem(response, http.StatusNotFound, "playlist not found", nil)
		return
	}
	if err != nil {
		api.problem(response, http.StatusInternalServerError, "delete playlist", nil)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}
