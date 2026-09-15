// Package playlists imports want lists from Spotify. A playlist is a list
// with a source; the wants it creates live their own lives as acquisition
// targets (ADR 0008), and nothing in this package matches anything — an entry
// either is provably owned by identifier agreement, or becomes a target for
// the resolver to grade.
package playlists

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pxldi/schall/internal/acquisition"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/events"
	"github.com/pxldi/schall/internal/spotify"
	"github.com/rs/zerolog"
)

// ErrNotConfigured means nobody has entered Spotify credentials yet.
var ErrNotConfigured = errors.New("Spotify is not configured")

// ErrNotConnected means credentials exist but no account has authorized them.
var ErrNotConnected = errors.New("no Spotify account is connected")

// ErrEmptyFile means the uploaded file was read and held no songs.
var ErrEmptyFile = errors.New("this file holds no songs")

// ErrNotSpotify refuses an import for a list that has no remote source.
var ErrNotSpotify = errors.New("only a Spotify playlist can be imported")

// ErrEntryNotFound means the entry does not exist, or exists on a different
// playlist than the one asked.
var ErrEntryNotFound = errors.New("playlist entry not found")

// Store is the persistence playlist ingestion needs.
type Store interface {
	SpotifySettings(context.Context) (db.SpotifySettingsRow, error)
	SaveSpotifyAuthorization(ctx context.Context, refreshToken, accountName string) (db.SpotifySettingsRow, error)
	RecordSpotifyConnection(ctx context.Context, status, failure string) (db.SpotifySettingsRow, error)

	UpsertSpotifyPlaylist(ctx context.Context, sourceID, name string) (db.PlaylistRow, error)
	UpsertFilePlaylist(ctx context.Context, sourceID, name string) (db.PlaylistRow, error)
	Playlist(context.Context, uuid.UUID) (db.PlaylistRow, error)
	Playlists(context.Context) ([]db.PlaylistRow, error)
	DeletePlaylist(context.Context, uuid.UUID) error
	CompletePlaylistImport(ctx context.Context, id uuid.UUID, revision, name, description, ownerName string, trackCount int32) (db.PlaylistRow, error)
	CompleteFilePlaylistImport(ctx context.Context, id uuid.UUID, name string, trackCount int32) (db.PlaylistRow, error)
	PlaylistEntries(context.Context, uuid.UUID) ([]db.PlaylistEntryRow, error)
	PlaylistEntry(context.Context, uuid.UUID) (db.PlaylistEntryRow, error)
	UpsertPlaylistEntry(context.Context, db.UpsertPlaylistEntryParams) (db.PlaylistEntryRow, error)
	PrunePlaylistEntries(ctx context.Context, playlistID uuid.UUID, keepTrackIDs []string) (int64, error)
	MarkPlaylistEntryOwned(ctx context.Context, entryID uuid.UUID, fileID uuid.NullUUID) error
	CreatePlaylistEntryTarget(ctx context.Context, entryID uuid.UUID, params db.CreateAcquisitionTargetParams) (uuid.UUID, error)
	PlaylistEntryTargetID(ctx context.Context, entryID uuid.UUID) (uuid.NullUUID, error)
	OwnedFileForISRC(ctx context.Context, isrc string) (uuid.NullUUID, error)
	LibraryFileByPath(ctx context.Context, path string) (db.LibraryFileRefRow, error)
	QueuePlaylistImport(ctx context.Context, playlistID uuid.UUID) error
	QueueAcquisitionSweep(context.Context, time.Time) error
	QueueNavidromePlaylistSync(ctx context.Context, playlistID uuid.UUID) error
}

type Service struct {
	store      Store
	httpClient *http.Client
	notices    *events.Hub
	logger     zerolog.Logger

	// apiURL and accountsURL are test seams, empty in production.
	apiURL      string
	accountsURL string

	// One access token is cached for the process: imports run one at a time
	// and every one starts with a refresh round-trip otherwise.
	mu     sync.Mutex
	access spotify.Token
}

func NewService(store Store, logger zerolog.Logger) *Service {
	return &Service{
		store:      store,
		httpClient: &http.Client{Timeout: 30 * time.Second},
		logger:     logger,
	}
}

func (service *Service) WithEvents(hub *events.Hub) *Service {
	service.notices = hub
	return service
}

// WithEndpoints points the service at test servers instead of Spotify.
func (service *Service) WithEndpoints(apiURL, accountsURL string, client *http.Client) *Service {
	service.apiURL, service.accountsURL = apiURL, accountsURL
	if client != nil {
		service.httpClient = client
	}
	return service
}

func (service *Service) announce() {
	if service.notices != nil {
		service.notices.Publish(events.TopicPlaylists)
	}
}

// client builds a Spotify client from the stored credentials.
func (service *Service) client(ctx context.Context) (*spotify.Client, db.SpotifySettingsRow, error) {
	settings, err := service.store.SpotifySettings(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, db.SpotifySettingsRow{}, ErrNotConfigured
	}
	if err != nil {
		return nil, db.SpotifySettingsRow{}, fmt.Errorf("read Spotify settings: %w", err)
	}
	client, err := spotify.NewClient(spotify.Options{
		APIURL:       service.apiURL,
		AccountsURL:  service.accountsURL,
		ClientID:     settings.ClientID,
		ClientSecret: settings.ClientSecret,
		HTTPClient:   service.httpClient,
	})
	if err != nil {
		return nil, db.SpotifySettingsRow{}, fmt.Errorf("build Spotify client: %w", err)
	}
	return client, settings, nil
}

// accessToken returns a live access token, refreshing through the stored
// refresh token when the cached one has expired.
func (service *Service) accessToken(ctx context.Context, client *spotify.Client, settings db.SpotifySettingsRow) (string, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.access.AccessToken != "" && time.Now().Before(service.access.ExpiresAt.Add(-time.Minute)) {
		return service.access.AccessToken, nil
	}
	if !settings.RefreshToken.Valid || strings.TrimSpace(settings.RefreshToken.String) == "" {
		return "", ErrNotConnected
	}
	token, err := client.Refresh(ctx, settings.RefreshToken.String)
	if errors.Is(err, spotify.ErrUnauthorized) {
		if _, recordErr := service.store.RecordSpotifyConnection(ctx, "failed", "Spotify no longer accepts the stored authorization; connect the account again."); recordErr != nil {
			service.logger.Error().Err(recordErr).Msg("record spotify connection")
		}
		return "", fmt.Errorf("refresh Spotify access: %w", err)
	}
	if err != nil {
		return "", fmt.Errorf("refresh Spotify access: %w", err)
	}
	// Spotify may rotate the refresh token; a rotated one replaces the stored
	// one or the next refresh fails for good.
	if token.RefreshToken != "" && token.RefreshToken != settings.RefreshToken.String {
		if _, err := service.store.SaveSpotifyAuthorization(ctx, token.RefreshToken, ""); err != nil {
			return "", fmt.Errorf("store rotated Spotify refresh token: %w", err)
		}
	}
	service.access = token
	return token.AccessToken, nil
}

// Connect finishes the OAuth round-trip: it exchanges the callback's code,
// learns whose account authorized, and stores the refresh token.
func (service *Service) Connect(ctx context.Context, code, redirectURI string) (db.SpotifySettingsRow, error) {
	client, _, err := service.client(ctx)
	if err != nil {
		return db.SpotifySettingsRow{}, err
	}
	token, err := client.Exchange(ctx, code, redirectURI)
	if err != nil {
		return db.SpotifySettingsRow{}, fmt.Errorf("exchange Spotify authorization code: %w", err)
	}
	if token.RefreshToken == "" {
		return db.SpotifySettingsRow{}, errors.New("Spotify returned no refresh token")
	}
	account, err := client.Me(ctx, token.AccessToken)
	if err != nil {
		return db.SpotifySettingsRow{}, fmt.Errorf("read Spotify account: %w", err)
	}
	name := account.DisplayName
	if strings.TrimSpace(name) == "" {
		name = account.ID
	}
	service.mu.Lock()
	service.access = token
	service.mu.Unlock()
	settings, err := service.store.SaveSpotifyAuthorization(ctx, token.RefreshToken, name)
	if err != nil {
		return db.SpotifySettingsRow{}, fmt.Errorf("store Spotify authorization: %w", err)
	}
	service.announce()
	return settings, nil
}

// AuthorizeURL is where the browser goes to approve access.
func (service *Service) AuthorizeURL(ctx context.Context, redirectURI, state string) (string, error) {
	client, _, err := service.client(ctx)
	if err != nil {
		return "", err
	}
	return client.AuthorizeURL(redirectURI, state), nil
}

// Follow records a Spotify playlist as a want list and queues its first
// import. Following a list that is already followed re-queues an import and
// answers with the list, not a refusal.
func (service *Service) Follow(ctx context.Context, link string) (db.PlaylistRow, error) {
	playlistID, err := spotify.PlaylistID(link)
	if err != nil {
		return db.PlaylistRow{}, err
	}
	client, settings, err := service.client(ctx)
	if err != nil {
		return db.PlaylistRow{}, err
	}
	token, err := service.accessToken(ctx, client, settings)
	if err != nil {
		return db.PlaylistRow{}, err
	}
	name, _, err := client.PlaylistRevision(ctx, token, playlistID)
	if err != nil {
		return db.PlaylistRow{}, fmt.Errorf("read Spotify playlist %s: %w", playlistID, err)
	}
	if strings.TrimSpace(name) == "" {
		name = playlistID
	}
	playlist, err := service.store.UpsertSpotifyPlaylist(ctx, playlistID, name)
	if err != nil {
		return db.PlaylistRow{}, fmt.Errorf("record playlist %s: %w", playlistID, err)
	}
	if err := service.store.QueuePlaylistImport(ctx, playlist.ID); err != nil {
		return db.PlaylistRow{}, fmt.Errorf("queue playlist import: %w", err)
	}
	service.announce()
	return playlist, nil
}

// QueueImport asks for a re-import of a followed list.
func (service *Service) QueueImport(ctx context.Context, id uuid.UUID) (db.PlaylistRow, error) {
	playlist, err := service.store.Playlist(ctx, id)
	if err != nil {
		return db.PlaylistRow{}, err
	}
	if playlist.Source != "spotify" || !playlist.SourceID.Valid {
		return db.PlaylistRow{}, ErrNotSpotify
	}
	if err := service.store.QueuePlaylistImport(ctx, playlist.ID); err != nil {
		return db.PlaylistRow{}, fmt.Errorf("queue playlist import: %w", err)
	}
	service.announce()
	return playlist, nil
}

// List and Get pass the stored lists through.
func (service *Service) List(ctx context.Context) ([]db.PlaylistRow, error) {
	return service.store.Playlists(ctx)
}

func (service *Service) Get(ctx context.Context, id uuid.UUID) (db.PlaylistRow, []db.PlaylistEntryRow, error) {
	playlist, err := service.store.Playlist(ctx, id)
	if err != nil {
		return db.PlaylistRow{}, nil, err
	}
	entries, err := service.store.PlaylistEntries(ctx, id)
	if err != nil {
		return db.PlaylistRow{}, nil, fmt.Errorf("read playlist entries: %w", err)
	}
	return playlist, entries, nil
}

// WantEntry creates a want for one entry by hand: the door for a list whose
// import never asks on its own, which today is every list adopted as a file
// rather than reconciled against a live source. Asking for an entry that
// already has a target, or one the library already answers, changes nothing —
// the same idempotence reconcileEntry gives an import.
func (service *Service) WantEntry(ctx context.Context, playlistID, entryID uuid.UUID) error {
	entry, err := service.store.PlaylistEntry(ctx, entryID)
	if err != nil {
		return err
	}
	if entry.PlaylistID != playlistID {
		return ErrEntryNotFound
	}
	if entry.AnsweringFileID.Valid || entry.TargetID.Valid {
		return nil
	}
	targetParams := db.CreateAcquisitionTargetParams{
		Origin:          "playlist",
		EntryArtist:     entry.EntryArtist,
		EntryTitle:      entry.EntryTitle,
		EntryAlbum:      entry.EntryAlbum,
		EntryDurationMS: entry.EntryDurationMS,
		EntryISRC:       entry.EntryISRC,
		Summary:         acquisition.UnresolvedSummary,
	}
	if _, err := service.store.CreatePlaylistEntryTarget(ctx, entry.ID, targetParams); err != nil {
		return fmt.Errorf("create target for entry %s: %w", entryID, err)
	}
	service.announce()
	return nil
}

// Delete forgets a list. Its targets are left standing on purpose.
func (service *Service) Delete(ctx context.Context, id uuid.UUID) error {
	if err := service.store.DeletePlaylist(ctx, id); err != nil {
		return err
	}
	service.announce()
	return nil
}

// Import is the job body: it reconciles one followed list against what Spotify
// serves now and against what the library holds now.
//
// Both halves matter, and only one of them is Spotify's to answer. An import
// re-asks ownership of every entry — a file that arrived since the last import
// answers its entry, one that went missing un-answers it — so the library
// changing is reason enough to run, whatever Spotify says about the list.
//
// The revision is still read and still recorded, and it is worth logging when
// it has not moved, because it says which of the two things changed. It is not
// a reason to stop: every import is a person pressing a button — following a
// list, or asking for it again — and nothing else ever queues one. A no-op
// hidden behind the only lever somebody has for "look again" is a button that
// does nothing.
//
// The revision is stored only after every entry landed, so a crash re-does
// work rather than skipping it.
func (service *Service) Import(ctx context.Context, id uuid.UUID) error {
	playlist, err := service.store.Playlist(ctx, id)
	if err != nil {
		return fmt.Errorf("read playlist %s: %w", id, err)
	}
	if playlist.Source != "spotify" || !playlist.SourceID.Valid {
		return ErrNotSpotify
	}
	client, settings, err := service.client(ctx)
	if err != nil {
		return err
	}
	token, err := service.accessToken(ctx, client, settings)
	if err != nil {
		return err
	}

	_, revision, err := client.PlaylistRevision(ctx, token, playlist.SourceID.String)
	if err != nil {
		return service.recordFailure(ctx, fmt.Errorf("read Spotify playlist revision: %w", err))
	}
	if playlist.SourceRevision.Valid && playlist.SourceRevision.String == revision && playlist.ImportedAt.Valid {
		service.logger.Info().
			Str("playlist_id", playlist.ID.String()).
			Str("source_revision", revision).
			Msg("playlist unchanged at the source; reconciling it against the library anyway")
	}

	remote, err := client.Playlist(ctx, token, playlist.SourceID.String)
	if err != nil {
		return service.recordFailure(ctx, fmt.Errorf("read Spotify playlist: %w", err))
	}

	kept := make([]string, 0, len(remote.Entries))
	seen := make(map[string]bool, len(remote.Entries))
	position := int32(0)
	created, owned, skipped := 0, 0, 0
	for _, entry := range remote.Entries {
		// A local file or an episode has no source identity to reconcile by
		// and nothing a resolver could be asked; it is counted rather than
		// silently dropped. A track appearing twice is one entry at its first
		// position — the want is one want.
		if entry.Unusable || seen[entry.TrackID] {
			skipped++
			continue
		}
		if entry.Title == "" && entry.ISRC == "" {
			skipped++
			continue
		}
		seen[entry.TrackID] = true
		kept = append(kept, entry.TrackID)
		position++

		madeTarget, isOwned, err := service.reconcileEntry(ctx, playlist.ID, position, sourceEntry{
			Key: entry.TrackID,
			// Spotify's own comma-joined display form for several credits.
			Artist:     strings.Join(entry.Artists, ", "),
			Title:      entry.Title,
			Album:      entry.Album,
			DurationMS: entry.DurationMS,
			ISRC:       entry.ISRC,
			Explicit:   entry.Explicit,
		})
		if err != nil {
			return service.recordFailure(ctx, err)
		}
		if madeTarget {
			created++
		}
		if isOwned {
			owned++
		}
	}

	pruned, err := service.store.PrunePlaylistEntries(ctx, playlist.ID, kept)
	if err != nil {
		return service.recordFailure(ctx, fmt.Errorf("prune departed playlist entries: %w", err))
	}

	if _, err := service.store.CompletePlaylistImport(
		ctx, playlist.ID, remote.SnapshotID, remote.Name, remote.Description,
		remote.OwnerName, int32(len(kept)),
	); err != nil {
		return service.recordFailure(ctx, fmt.Errorf("complete playlist import: %w", err))
	}
	if _, err := service.store.RecordSpotifyConnection(ctx, "ok", ""); err != nil {
		service.logger.Error().Err(err).Msg("record spotify connection")
	}

	// New wants are unresolved, and only a sweep resolves them. Asking for one
	// now is what turns an import into targets being looked up within minutes
	// rather than at the next restart. A failure costs a delay, never a want.
	if created > 0 {
		if err := service.store.QueueAcquisitionSweep(ctx, time.Now()); err != nil {
			service.logger.Error().Err(err).Msg("queue acquisition sweep")
		}
		if service.notices != nil {
			service.notices.Publish(events.TopicAcquisitions)
		}
	}

	// What the player is told is what this list holds now, and this is the
	// moment that changed. Queued rather than done here: a pass talks to the
	// player, and an import that waited on that conversation would fail for a
	// reason that has nothing to do with the music. Asking for a list nothing
	// can be paired for costs one refusal and writes nothing.
	if err := service.store.QueueNavidromePlaylistSync(ctx, playlist.ID); err != nil {
		service.logger.Error().Err(err).
			Str("playlist_id", playlist.ID.String()).
			Msg("queue navidrome playlist sync")
	}

	service.logger.Info().
		Str("playlist_id", playlist.ID.String()).
		Str("source_revision", remote.SnapshotID).
		Int("entries", len(kept)).
		Int("targets_created", created).
		Int("owned", owned).
		Int("skipped", skipped).
		Int64("pruned", pruned).
		Msg("playlist imported")
	service.announce()
	return nil
}

// ImportFile makes a want list out of a file somebody uploaded: a CSV another
// tool exported, or an M3U a player wrote.
//
// The file is read here, from its bytes, and never from anything a browser
// says about it. A preview shows what the reading found before this is called,
// and the person confirms it; if that preview were sent back as the thing to
// import, the entries would be whatever the browser handed over rather than
// what the file says, and an entry is evidence.
//
// Everything after the reading is the walk Import already does. A row becomes
// an entry, an entry that the library already answers is owned, and an entry
// nothing answers becomes a want for the sweep to resolve. No rule about what
// proves a match is touched or added here.
//
// Importing the same file twice lands on the same list — it is found by the
// file's name — and on the same entries, because a row's identity is its ISRC
// or its own words. Nothing is created a second time.
func (service *Service) ImportFile(
	ctx context.Context, filename, name string, content []byte,
) (db.PlaylistRow, error) {
	parsed, err := ParseFile(filename, content)
	if err != nil {
		return db.PlaylistRow{}, err
	}
	if len(parsed.Rows) == 0 {
		return db.PlaylistRow{}, ErrEmptyFile
	}
	listName := strings.TrimSpace(name)
	if listName == "" {
		listName = parsed.Name
	}

	playlist, err := service.store.UpsertFilePlaylist(ctx, fileIdentity(filename), listName)
	if err != nil {
		return db.PlaylistRow{}, fmt.Errorf("record playlist from %s: %w", filename, err)
	}

	kept := make([]string, 0, len(parsed.Rows))
	seen := make(map[string]bool, len(parsed.Rows))
	position := int32(0)
	created, owned, repeated := 0, 0, 0
	for _, row := range parsed.Rows {
		// A row that says exactly what an earlier row said is the same song
		// asked for twice, and the want is one want — the rule the Spotify
		// import applies to a track listed twice.
		if seen[row.Key] {
			repeated++
			continue
		}
		seen[row.Key] = true
		kept = append(kept, row.Key)
		position++

		entry := sourceEntry{
			Key:        row.Key,
			Artist:     row.Artist,
			Title:      row.Title,
			Album:      row.Album,
			DurationMS: row.DurationMS,
			ISRC:       row.ISRC,
		}
		if row.LibraryPath != "" {
			entry.KnownFile, err = service.fileAtPath(ctx, row.LibraryPath)
			if err != nil {
				return db.PlaylistRow{}, err
			}
		}
		madeTarget, isOwned, err := service.reconcileEntry(ctx, playlist.ID, position, entry)
		if err != nil {
			return db.PlaylistRow{}, err
		}
		if madeTarget {
			created++
		}
		if isOwned {
			owned++
		}
	}

	pruned, err := service.store.PrunePlaylistEntries(ctx, playlist.ID, kept)
	if err != nil {
		return db.PlaylistRow{}, fmt.Errorf("prune departed playlist entries: %w", err)
	}

	playlist, err = service.store.CompleteFilePlaylistImport(
		ctx, playlist.ID, listName, int32(len(kept)))
	if err != nil {
		return db.PlaylistRow{}, fmt.Errorf("complete playlist import: %w", err)
	}

	// The tail of Import, for the same reasons: new wants are unresolved until
	// a sweep looks at them, and the player is told what this list holds now.
	if created > 0 {
		if err := service.store.QueueAcquisitionSweep(ctx, time.Now()); err != nil {
			service.logger.Error().Err(err).Msg("queue acquisition sweep")
		}
		if service.notices != nil {
			service.notices.Publish(events.TopicAcquisitions)
		}
	}
	if err := service.store.QueueNavidromePlaylistSync(ctx, playlist.ID); err != nil {
		service.logger.Error().Err(err).
			Str("playlist_id", playlist.ID.String()).
			Msg("queue navidrome playlist sync")
	}

	service.logger.Info().
		Str("playlist_id", playlist.ID.String()).
		Str("file", parsed.Format).
		Int("entries", len(kept)).
		Int("targets_created", created).
		Int("owned", owned).
		Int("repeated_rows", repeated).
		Int("unreadable_rows", len(parsed.Skipped)).
		Int64("pruned", pruned).
		Msg("playlist imported from a file")
	service.announce()
	return playlist, nil
}

// fileIdentity is the list's identity for a second import: the file's own
// name, directories and all removed, because that is what a browser sends and
// the only thing about an upload that is stable between two of them.
func fileIdentity(filename string) string {
	base := filepath.Base(strings.ReplaceAll(strings.TrimSpace(filename), `\`, "/"))
	if base == "" || base == "." || base == "/" {
		return "playlist"
	}
	return base
}

// fileAtPath answers which library file a path line names, and nothing where
// it names none.
//
// Exact path equality is the whole test. A path that no present file sits at
// is not evidence about any file — the row's words go to the resolver like any
// other row's, and the path is not read as a description of music.
func (service *Service) fileAtPath(ctx context.Context, path string) (uuid.NullUUID, error) {
	file, err := service.store.LibraryFileByPath(ctx, path)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.NullUUID{}, nil
	}
	if err != nil {
		return uuid.NullUUID{}, fmt.Errorf("look for the file at %s: %w", path, err)
	}
	return uuid.NullUUID{UUID: file.ID, Valid: true}, nil
}

// sourceEntry is one row of a want list as its source described it, whichever
// source that was: a Spotify track, or a line of a file somebody imported.
//
// The two doors differ only in what they can say; what happens after them is
// one walk, so that a file row is graded by exactly the rules a Spotify row is
// and by no others.
type sourceEntry struct {
	// Key is the identity a re-import reconciles by: Spotify's track id, or
	// the identity a file row was given (rowKey). It says which stored entry
	// this row is, and never anything about the music.
	Key string

	Artist     string
	Title      string
	Album      string
	DurationMS int
	ISRC       string
	// Explicit is the source saying this track is the explicit recording. Only
	// Spotify says it; every other source leaves it false, which is silence.
	Explicit bool

	// KnownFile is a library file the source named outright, rather than
	// described. Only a file import fills it, from a path line that is the
	// path of a file the library holds; nothing is inferred from a path that
	// is not.
	KnownFile uuid.NullUUID
}

// reconcileEntry lands one row: refresh the entry, answer ownership by
// identifier agreement, and make sure an unowned entry has a want.
func (service *Service) reconcileEntry(
	ctx context.Context, playlistID uuid.UUID, position int32, remote sourceEntry,
) (madeTarget, isOwned bool, err error) {
	params := db.UpsertPlaylistEntryParams{
		PlaylistID:    playlistID,
		Position:      position,
		SourceTrackID: remote.Key,
		// Verbatim: the entry is evidence, and the resolver decides what to
		// offer the grader.
		EntryArtist: remote.Artist,
		EntryTitle:  remote.Title,
		EntryAlbum:  remote.Album,
	}
	if remote.DurationMS > 0 {
		params.EntryDurationMS = pgtype.Int4{Int32: int32(remote.DurationMS), Valid: true}
	}
	if remote.ISRC != "" {
		params.EntryISRC = pgtype.Text{String: remote.ISRC, Valid: true}
	}
	// Only a source that said so writes it. False here is a source that said
	// nothing, and recording that as "not explicit" would be a claim nobody made.
	if remote.Explicit {
		params.EntryExplicit = pgtype.Bool{Bool: true, Valid: true}
	}
	entry, err := service.store.UpsertPlaylistEntry(ctx, params)
	if err != nil {
		return false, false, fmt.Errorf("reconcile playlist entry %s: %w", remote.Key, err)
	}

	// Ownership is an identifier question and is re-asked every import: a
	// file that arrived since the last one answers the entry, and one that
	// went missing un-answers it.
	ownedFile := remote.KnownFile
	if !ownedFile.Valid && remote.ISRC != "" {
		ownedFile, err = service.store.OwnedFileForISRC(ctx, remote.ISRC)
		if err != nil {
			return false, false, fmt.Errorf("look for an owned copy of %s: %w", remote.ISRC, err)
		}
	}
	if ownedFile != entry.OwnedFileID {
		if err := service.store.MarkPlaylistEntryOwned(ctx, entry.ID, ownedFile); err != nil {
			return false, false, fmt.Errorf("record ownership of entry %s: %w", entry.ID, err)
		}
	}
	if ownedFile.Valid {
		return false, true, nil
	}

	// One want per entry, asked once: an entry that already made its target
	// keeps it through every re-import, whatever state it has reached since.
	// Creation and the join land in one transaction, so a crashed import can
	// never leave a target no entry admits to having asked for.
	existing, err := service.store.PlaylistEntryTargetID(ctx, entry.ID)
	if err != nil {
		return false, false, fmt.Errorf("read entry target: %w", err)
	}
	if existing.Valid {
		return false, false, nil
	}
	targetParams := db.CreateAcquisitionTargetParams{
		Origin:          "playlist",
		EntryArtist:     params.EntryArtist,
		EntryTitle:      remote.Title,
		EntryAlbum:      remote.Album,
		EntryDurationMS: params.EntryDurationMS,
		EntryExplicit:   params.EntryExplicit,
		Summary:         acquisition.UnresolvedSummary,
	}
	if remote.ISRC != "" {
		targetParams.EntryISRC = pgtype.Text{String: remote.ISRC, Valid: true}
	}
	if _, err := service.store.CreatePlaylistEntryTarget(ctx, entry.ID, targetParams); err != nil {
		return false, false, fmt.Errorf("create target for entry %s: %w", entry.ID, err)
	}
	return true, false, nil
}

// recordFailure writes the failure to the settings row so the pages can say
// why the last import stopped, and hands the error back for the job to retry.
func (service *Service) recordFailure(ctx context.Context, cause error) error {
	if _, err := service.store.RecordSpotifyConnection(ctx, "failed", cause.Error()); err != nil {
		service.logger.Error().Err(err).Msg("record spotify connection")
	}
	return cause
}
