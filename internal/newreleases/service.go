// Package newreleases keeps the new-releases playlist: the listening surface of
// the follow feed.
//
// Schall lets a person follow an artist or a label. A sweep then asks
// MusicBrainz what they have published since, wants every recording on it the
// library does not hold, and the acquisition loop obtains them. All of that
// happens unattended, and until now none of it ever reached the player as a
// statement — the music simply appeared somewhere in a library of thousands of
// files.
//
// This package is that statement. One playlist, holding the tracks the library
// owns from releases the feed surfaced inside a window of days, newest release
// first, pushed to the player by the ordinary Navidrome playlist sync. A refresh
// runs after the feed sweep and after a scan brings new files in.
//
// It rotates playlist membership and nothing else. A release leaving the window
// leaves a playlist: the files stay, the wants stay wanted, and every decision
// Schall has made stands. Nothing here can delete music, which is why it shares
// no machinery with the weekly playlist, where a lease is the licence to delete
// and every line is written around that.
package newreleases

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"

	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/events"
)

// PlaylistName is what the list is called in Schall and in the player.
const PlaylistName = "New releases"

const (
	// defaultWindowDays is how far back a release may have been published and
	// still be on the list, before anybody has changed it. It matches the
	// column default in migration 00075.
	defaultWindowDays = 60

	// minWindowDays and maxWindowDays are the bounds the database holds, said
	// again here so that a person who types something outside them is told what
	// is wrong rather than shown a failed save.
	minWindowDays = 1
	maxWindowDays = 365
)

// ErrWindowOutOfRange is returned when the window somebody asked for is not a
// window. It reads as a sentence because it is shown as one.
var ErrWindowOutOfRange = errors.New("the window must be between 1 and 365 days")

// Store is everything this package reads and writes.
type Store interface {
	NewReleasesPlaylistSettings(ctx context.Context) (db.NewReleasesPlaylistSettingsRow, error)
	SaveNewReleasesPlaylistSettings(ctx context.Context, params db.SaveNewReleasesPlaylistSettingsParams) (db.NewReleasesPlaylistSettingsRow, error)

	EnsureNewReleasesPlaylist(ctx context.Context, name string) (db.PlaylistRow, error)
	NewReleasesPlaylist(ctx context.Context) (db.PlaylistRow, error)

	NewReleaseCandidates(ctx context.Context, releasedOnOrAfter time.Time) ([]db.NewReleaseCandidateRow, error)
	NewReleasePlaylistMembers(ctx context.Context) ([]db.NewReleasePlaylistMemberRow, error)
	WithdrawRemovedNewReleaseMembers(ctx context.Context) (int64, error)
	AddNewReleasePlaylistMember(ctx context.Context, params db.AddNewReleasePlaylistMemberParams) error
	DropNewReleasePlaylistMember(ctx context.Context, libraryFileID uuid.UUID) error
	ForgetNewReleasePlaylistMembers(ctx context.Context) (int64, error)
	OrderNewReleasePlaylistEntries(ctx context.Context, playlistID uuid.UUID) error

	QueueNavidromePlaylistSync(ctx context.Context, playlistID uuid.UUID) error
	QueueNewReleasesPlaylistRefresh(ctx context.Context, runAfter time.Time) error
}

// Service keeps the list.
type Service struct {
	store   Store
	logger  zerolog.Logger
	notices *events.Hub
	now     func() time.Time
}

// NewService builds the service. The event hub is registered separately, in the
// shape the weekly playlist uses: an installation with no interface attached
// still keeps the list.
func NewService(store Store, logger zerolog.Logger) *Service {
	return &Service{store: store, logger: logger, now: time.Now}
}

// WithEvents registers what tells connected interfaces the playlists changed.
func (service *Service) WithEvents(hub *events.Hub) *Service {
	service.notices = hub
	return service
}

// WithClock replaces the clock. Tests use it; nothing else does.
func (service *Service) WithClock(now func() time.Time) *Service {
	service.now = now
	return service
}

// Settings returns how the list is kept. A person who has never opened the
// setting gets the defaults, switched on: this feature adds and removes playlist
// rows and can never delete a file, so there is nothing here to consent to
// before it happens.
func (service *Service) Settings(ctx context.Context) (db.NewReleasesPlaylistSettingsRow, error) {
	settings, err := service.store.NewReleasesPlaylistSettings(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.NewReleasesPlaylistSettingsRow{Enabled: true, WindowDays: defaultWindowDays}, nil
	}
	if err != nil {
		return db.NewReleasesPlaylistSettingsRow{}, fmt.Errorf("read new releases playlist settings: %w", err)
	}
	return settings, nil
}

// SaveSettings writes what a person set, and refuses a window that is not one.
//
// It then asks for a refresh, because the settings are the list: switching the
// feature on, or widening the window from two months to six, changes what
// belongs on the playlist and a person who pressed Save meant that to happen.
// Without this the press did nothing visible until the follow feed next ran or
// a scan brought a new file in — days, on a library that has settled — and the
// only thing to do about it was wait.
func (service *Service) SaveSettings(
	ctx context.Context, params db.SaveNewReleasesPlaylistSettingsParams,
) (db.NewReleasesPlaylistSettingsRow, error) {
	if params.WindowDays < minWindowDays || params.WindowDays > maxWindowDays {
		return db.NewReleasesPlaylistSettingsRow{}, ErrWindowOutOfRange
	}
	settings, err := service.store.SaveNewReleasesPlaylistSettings(ctx, params)
	if err != nil {
		return db.NewReleasesPlaylistSettingsRow{}, fmt.Errorf("save new releases playlist settings: %w", err)
	}
	// A failure to queue is logged rather than returned: the setting is saved,
	// which is what was asked for, and a refresh that did not get asked for
	// happens at the next feed pass anyway.
	if err := service.store.QueueNewReleasesPlaylistRefresh(ctx, service.now()); err != nil {
		service.logger.Error().Err(err).Msg("queue new releases playlist refresh")
	}
	service.announce()
	return settings, nil
}

// RefreshResult is what one pass did.
type RefreshResult struct {
	// Skipped is a pass that had nothing to keep: the feature is switched off,
	// and Reason says so.
	Skipped bool
	Reason  string
	// PlaylistID is the list this pass wrote, invalid when it wrote none.
	PlaylistID uuid.NullUUID
	// Added and Dropped are the tracks that joined and left the list.
	Added   int
	Dropped int
	// Withdrawn is the tracks somebody took out of the list in their player
	// since the last pass. They stay out.
	Withdrawn int
	// Total is what the list holds now.
	Total int
}

// Refresh brings the list up to date with what the library owns.
//
// The order of the four steps matters and is the whole correctness of this
// package. A person's own removal is read first, from the entry the sync
// deleted; only then does the pass remove anything of its own, so nothing it
// does can be mistaken for something somebody did.
func (service *Service) Refresh(ctx context.Context) (RefreshResult, error) {
	var result RefreshResult
	settings, err := service.Settings(ctx)
	if err != nil {
		return result, err
	}

	playlist, listed, err := service.list(ctx)
	if err != nil {
		return result, err
	}

	if !settings.Enabled {
		if !listed {
			return RefreshResult{Skipped: true, Reason: "the new-releases playlist is switched off"}, nil
		}
		return service.empty(ctx, playlist)
	}

	if !listed {
		playlist, err = service.store.EnsureNewReleasesPlaylist(ctx, PlaylistName)
		if err != nil {
			return result, fmt.Errorf("make the new releases playlist: %w", err)
		}
	}
	result.PlaylistID = uuid.NullUUID{UUID: playlist.ID, Valid: true}

	withdrawn, err := service.store.WithdrawRemovedNewReleaseMembers(ctx)
	if err != nil {
		return result, err
	}
	result.Withdrawn = int(withdrawn)

	members, err := service.store.NewReleasePlaylistMembers(ctx)
	if err != nil {
		return result, fmt.Errorf("read the new releases playlist members: %w", err)
	}
	cutoff := service.now().UTC().AddDate(0, 0, -int(settings.WindowDays))
	candidates, err := service.store.NewReleaseCandidates(ctx, cutoff)
	if err != nil {
		return result, fmt.Errorf("read what the follow feed has brought in: %w", err)
	}

	wanted := make(map[uuid.UUID]struct{}, len(candidates))
	for _, candidate := range candidates {
		wanted[candidate.LibraryFileID] = struct{}{}
	}

	held := make(map[uuid.UUID]struct{}, len(members))
	for _, member := range members {
		// A withdrawal is permanent. The row is neither dropped nor counted as
		// held, so the track is never put back and never taken into account
		// again.
		if member.WithdrawnAt.Valid {
			held[member.LibraryFileID] = struct{}{}
			continue
		}
		if _, still := wanted[member.LibraryFileID]; still {
			held[member.LibraryFileID] = struct{}{}
			result.Total++
			continue
		}
		if err := service.store.DropNewReleasePlaylistMember(ctx, member.LibraryFileID); err != nil {
			return result, fmt.Errorf("take file %s off the new releases playlist: %w", member.LibraryFileID, err)
		}
		result.Dropped++
	}

	for _, candidate := range candidates {
		if _, already := held[candidate.LibraryFileID]; already {
			continue
		}
		if err := service.store.AddNewReleasePlaylistMember(ctx, db.AddNewReleasePlaylistMemberParams{
			PlaylistID:    playlist.ID,
			LibraryFileID: candidate.LibraryFileID,
			AlbumID:       candidate.AlbumID,
			ReleasedOn:    candidate.ReleasedOn,
			Title:         candidate.Title,
		}); err != nil {
			return result, fmt.Errorf("put file %s on the new releases playlist: %w", candidate.LibraryFileID, err)
		}
		result.Added++
		result.Total++
	}

	if err := service.store.OrderNewReleasePlaylistEntries(ctx, playlist.ID); err != nil {
		return result, err
	}
	service.publish(ctx, playlist.ID)
	service.announce()
	service.logger.Debug().
		Str("playlist_id", playlist.ID.String()).
		Int("added", result.Added).Int("dropped", result.Dropped).
		Int("withdrawn", result.Withdrawn).Int("total", result.Total).
		Msg("new releases playlist refreshed")
	return result, nil
}

// list reads the playlist, and clears the membership when it finds the list has
// been deleted.
//
// Deleting the playlist deleted its entries with it, so every member row is left
// pointing at nothing — which is exactly what a track somebody took out in their
// player looks like. Forgetting them here is what stops one deleted list pinning
// every track out of the next one.
func (service *Service) list(ctx context.Context) (db.PlaylistRow, bool, error) {
	playlist, err := service.store.NewReleasesPlaylist(ctx)
	if err == nil {
		return playlist, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return db.PlaylistRow{}, false, fmt.Errorf("read the new releases playlist: %w", err)
	}
	forgotten, err := service.store.ForgetNewReleasePlaylistMembers(ctx)
	if err != nil {
		return db.PlaylistRow{}, false, err
	}
	if forgotten > 0 {
		service.logger.Info().Int64("members", forgotten).
			Msg("the new releases playlist was deleted, so its membership is forgotten")
	}
	return db.PlaylistRow{}, false, nil
}

// empty takes every track off the list, which is what switching the feature off
// means. The list itself stays, holding nothing, and the next sync empties it in
// the player through the ordinary removal path — there is no music to delete
// here and none is deleted.
func (service *Service) empty(ctx context.Context, playlist db.PlaylistRow) (RefreshResult, error) {
	result := RefreshResult{
		Skipped:    true,
		Reason:     "the new-releases playlist is switched off",
		PlaylistID: uuid.NullUUID{UUID: playlist.ID, Valid: true},
	}
	members, err := service.store.NewReleasePlaylistMembers(ctx)
	if err != nil {
		return result, fmt.Errorf("read the new releases playlist members: %w", err)
	}
	for _, member := range members {
		if member.WithdrawnAt.Valid {
			continue
		}
		if err := service.store.DropNewReleasePlaylistMember(ctx, member.LibraryFileID); err != nil {
			return result, fmt.Errorf("take file %s off the new releases playlist: %w", member.LibraryFileID, err)
		}
		result.Dropped++
	}
	if result.Dropped > 0 {
		service.publish(ctx, playlist.ID)
		service.announce()
	}
	return result, nil
}

// publish asks for the list to be pushed to the player. It is queued rather than
// pushed here: the sync is one job at a time per list, and a refresh that waited
// for a player which is slow or switched off would be a refresh that fails for a
// reason nothing on this list depends on.
func (service *Service) publish(ctx context.Context, playlistID uuid.UUID) {
	if err := service.store.QueueNavidromePlaylistSync(ctx, playlistID); err != nil {
		service.logger.Warn().Err(err).Str("playlist_id", playlistID.String()).
			Msg("queue the sync of the new releases playlist")
	}
}

func (service *Service) announce() {
	if service.notices != nil {
		service.notices.Publish(events.TopicPlaylists)
	}
}

// Playlist is the whole screen in one read: how the list is configured, and the
// list itself when there is one.
type Playlist struct {
	Settings db.NewReleasesPlaylistSettingsRow
	List     db.PlaylistRow
	Exists   bool
}

// Overview reads the settings and the list for the interface.
func (service *Service) Overview(ctx context.Context) (Playlist, error) {
	settings, err := service.Settings(ctx)
	if err != nil {
		return Playlist{}, err
	}
	list, err := service.store.NewReleasesPlaylist(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return Playlist{Settings: settings}, nil
	}
	if err != nil {
		return Playlist{}, fmt.Errorf("read the new releases playlist: %w", err)
	}
	return Playlist{Settings: settings, List: list, Exists: true}, nil
}
