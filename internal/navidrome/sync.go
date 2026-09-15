package navidrome

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pxldi/schall/internal/db"
	"github.com/rs/zerolog"
)

// syncTimeout bounds one pass. A pass asks the server once per unlisted track
// and once per acquired one, so a long playlist is a run of small requests
// rather than one large wait; this is generous for that and short enough that a
// wedged player cannot hold the general job lane open indefinitely.
const syncTimeout = 5 * time.Minute

// SyncStore is what a sync pass reads and writes: the player's connection, the
// want list and its acquired subset, the snapshot of what was pushed, and the
// two edits a pass is allowed to make to a playlist's entries.
type SyncStore interface {
	NavidromeSettings(context.Context) (db.NavidromeSettingsRow, error)

	Playlist(context.Context, uuid.UUID) (db.PlaylistRow, error)
	Playlists(context.Context) ([]db.PlaylistRow, error)
	AcquiredPlaylistTracks(context.Context, uuid.UUID) ([]db.AcquiredPlaylistTrackRow, error)
	AppendPlaylistEntry(context.Context, db.AppendPlaylistEntryParams) (db.PlaylistEntryRow, error)
	DeletePlaylistEntry(context.Context, uuid.UUID) error
	UpsertNavidromePlaylist(ctx context.Context, sourceID, name string) (db.PlaylistRow, error)
	LibraryFileByPath(context.Context, string) (db.LibraryFileRefRow, error)

	NavidromePlaylistSnapshot(context.Context, uuid.UUID) (db.NavidromePlaylistSnapshotRow, error)
	NavidromePlaylistSnapshotTracks(context.Context, uuid.UUID) ([]db.NavidromePlaylistSnapshotTrackRow, error)
	SnapshotByNavidromeID(context.Context, string) (db.NavidromePlaylistSnapshotRow, error)
	ReplaceNavidromePlaylistSnapshot(
		context.Context, db.NavidromePlaylistSnapshotParams, []db.NavidromePlaylistSnapshotTrackParams,
	) error
	RepairNavidromeSnapshotTrack(ctx context.Context, playlistID uuid.UUID, position int32, songID string) error
	DeleteNavidromePlaylistSnapshot(context.Context, uuid.UUID) error

	QueueNavidromePlaylistSync(context.Context, uuid.UUID) error
}

// playlistClient is the part of a client a sync pass uses.
type playlistClient interface {
	Playlists(context.Context) ([]Playlist, error)
	Playlist(ctx context.Context, id string) (Playlist, error)
	CreatePlaylist(ctx context.Context, name string, songIDs []string) (string, error)
	UpdatePlaylist(context.Context, PlaylistUpdate) error
	Song(ctx context.Context, id string) (Song, error)
	FindSongByPath(context.Context, SongLookup) (SongMatch, error)
}

// ErrNothingPaired is the refusal at the heart of the pass: Schall holds files
// for this list and Navidrome recognised none of them.
//
// It is a refusal rather than a first push of nothing, because every reason it
// can happen is a reason the comparison cannot be trusted, and pushing anyway
// would write a snapshot claiming a state Navidrome was never in. The message
// names all three causes, since from here they are indistinguishable.
var ErrNothingPaired = errors.New(
	"navidrome recognised none of the files Schall holds for this playlist: " +
		"either Subsonic.DefaultReportRealPath was not on when the schall-sync client first called, " +
		"or the library has not been scanned yet, " +
		"or SCHALL_NAVIDROME_PATH_MAP does not describe how the two mounts differ")

// SyncResult is what one pass did. Nothing in it is a failure: a track nobody
// could pair, a break that could not be repaired and a song the library does
// not hold are all answers, and all of them are reported rather than acted on.
type SyncResult struct {
	PlaylistID uuid.UUID
	// Configured is false when there is no player, or it is switched off. The
	// pass then did nothing at all and said so.
	Configured bool
	// Created is a list Navidrome did not hold before this pass.
	Created bool
	// RemoteDeleted is the playlist having been deleted in Navidrome. The
	// snapshot is dropped and nothing is recreated in this pass.
	RemoteDeleted    bool
	RemotePlaylistID string
	Pushed           int
	RemovalsActed    int
	BreaksRepaired   int
	BreaksLeft       []string
	AdditionsAdopted int
	// RemovalsWithoutEntry are snapshot tracks the user removed which no longer
	// name a playlist entry. There is nothing left to remove.
	RemovalsWithoutEntry int
	// Unpaired are the acquired files Navidrome could not be asked about, and
	// UnknownAdditions the songs it holds at paths the library does not.
	Unpaired         []UnpairedTrack
	UnknownAdditions []string
}

// UnpairedTrack is one acquired file the player did not answer for, with what
// it was asked and what came back.
//
// It carries the evidence rather than a count because the count cannot be acted
// on. Twenty-one files nobody can name is a number to worry about; twenty-one
// files that share a naming shape, or a folder, or a reason, is a fault with a
// direction. Nothing here is stored: it is what this pass saw, and the next
// pass sees it again if it is still true.
type UnpairedTrack struct {
	EntryID       uuid.UUID
	Position      int32
	LibraryFileID uuid.UUID
	// LibraryPath is the path Schall holds the file at; RemotePath is the path
	// the player was asked about, which is the same path through the path map.
	LibraryPath string
	RemotePath  string
	Reason      MissReason
	Attempts    []LookupAttempt
}

// unpairedSample bounds how many unpaired files one log line carries. It is a
// sample and not a list: enough to see a shape, few enough that a healthy pass
// and a broken one still print at the same size.
const unpairedSample = 6

// UnpairedNote is one unpaired file as a log line carries it. The path is
// escaped, because the question being asked of a log like this is why two
// strings that print identically were not equal, and a log that prints them
// identically cannot answer it.
type UnpairedNote struct {
	Path   string     `json:"path"`
	Reason MissReason `json:"reason"`
	// Searched is what the player was asked, Candidates how many songs it
	// answered with in total, and Offered a few of the paths they were at.
	Searched   []string `json:"searched,omitempty"`
	Candidates int      `json:"candidates"`
	Offered    []string `json:"offered,omitempty"`
}

// LogPairing writes onto one log line what a count of unpaired files cannot
// say: how the refusals divide by reason, and a few of the files themselves.
//
// The two reasons are the two competing explanations for a file the player will
// not answer for — a search that reached nothing, and a search that reached
// other files — and which of them a deployment has is not something anybody can
// infer from a number. A pass with nothing unpaired adds nothing.
func (result SyncResult) LogPairing(event *zerolog.Event) {
	if len(result.Unpaired) == 0 {
		return
	}
	reasons := make(map[string]int, 4)
	for _, track := range result.Unpaired {
		reasons[string(track.Reason)]++
	}
	event.Interface("unpaired_reasons", reasons)
	event.Interface("unpaired_sample", sampleUnpaired(result.Unpaired, unpairedSample))
}

// sampleUnpaired chooses which unpaired files to show.
//
// Not the first few: taken from the front, a sample of six answers a question
// nobody asked, because the front of a playlist is one arbitrary corner of it.
// One file per reason and folder comes first instead, so that six lines out of
// twenty-one say whether this is one folder, one naming shape or the whole
// list; whatever room is left is filled in playlist order.
func sampleUnpaired(unpaired []UnpairedTrack, limit int) []UnpairedNote {
	notes := make([]UnpairedNote, 0, limit)
	taken := make(map[int]struct{}, limit)
	seen := make(map[string]struct{}, limit)
	for index, track := range unpaired {
		if len(notes) >= limit {
			break
		}
		shape := string(track.Reason) + "\x00" + filepath.Dir(track.RemotePath)
		if _, repeat := seen[shape]; repeat {
			continue
		}
		seen[shape] = struct{}{}
		taken[index] = struct{}{}
		notes = append(notes, unpairedNote(track))
	}
	for index, track := range unpaired {
		if len(notes) >= limit {
			break
		}
		if _, already := taken[index]; already {
			continue
		}
		notes = append(notes, unpairedNote(track))
	}
	return notes
}

func unpairedNote(track UnpairedTrack) UnpairedNote {
	note := UnpairedNote{Path: Escaped(track.RemotePath), Reason: track.Reason}
	for _, attempt := range track.Attempts {
		note.Searched = append(note.Searched, attempt.Term)
		note.Candidates += attempt.Candidates
		note.Offered = append(note.Offered, attempt.Evidence...)
	}
	return note
}

// SweepResult is what one look at the whole player did.
type SweepResult struct {
	Configured bool
	Adopted    []uuid.UUID
	// Skipped are the remote lists another account owns, or that a Schall list
	// is already joined to.
	Skipped          int
	Queued           int
	UnknownAdditions []string
}

// Syncer joins the want lists to the player's playlists.
//
// Like the Notifier it builds a client per pass rather than holding one: the
// settings are the operator's to change while Schall is running. The client is
// built under SyncClientName and never the default, because this is the half
// that reads paths back and needs a player row created after
// Subsonic.DefaultReportRealPath was turned on (docs/decisions/0010).
type Syncer struct {
	store  SyncStore
	paths  PathMap
	logger zerolog.Logger
	// newClient exists so a test can answer for the player without a network.
	newClient func(Options) (playlistClient, error)
}

func NewSyncer(store SyncStore, paths PathMap, logger zerolog.Logger) *Syncer {
	return &Syncer{
		store:  store,
		paths:  paths,
		logger: logger,
		newClient: func(options Options) (playlistClient, error) {
			return NewClient(options)
		},
	}
}

// SyncPlaylist brings one want list and its playlist in Navidrome into
// agreement, through the snapshot of what was last pushed.
//
// An installation with no player, or one whose player has been switched off, is
// not a failure: there is nothing to sync and nothing wrong, so this reports a
// result that did nothing, exactly as NoticeLibraryChange does.
func (syncer *Syncer) SyncPlaylist(ctx context.Context, playlistID uuid.UUID) (SyncResult, error) {
	result := SyncResult{PlaylistID: playlistID}
	client, _, configured, err := syncer.connect(ctx)
	if err != nil || !configured {
		return result, err
	}
	result.Configured = true

	bounded, cancel := context.WithTimeout(ctx, syncTimeout)
	defer cancel()
	return syncer.syncOne(bounded, client, result)
}

// pairingTimeout bounds one report. It asks the player once per acquired file,
// the same run of small requests a push makes, and it is answered while
// somebody waits — so it is shorter than a pass and shorter than the API's own
// limit on a request, and a list too long to finish inside it is reported as
// far as it got rather than as a failure.
const pairingTimeout = 20 * time.Second

// PairingReport is what a push would find if it ran now: how much of a list
// Schall holds a file for, how much of that the player answers for, and which
// files it does not.
//
// AcquiredCount and the owned count on the playlist are now the same number,
// asked in two places: both read the entry's ownership record and the copy its
// want was acquired on (db.answeringFile). They used to disagree, because
// ownership was only written down when a list was imported and a want acquired
// since then was invisible to it. AcquiredCount is still reported here because
// it is what Checked and PairedCount are counted against — how much of what
// Schall holds this reading got through, and how much of that the player knew.
type PairingReport struct {
	PlaylistID uuid.UUID
	// Configured is false when there is no player, or it is switched off.
	Configured    bool
	EntryCount    int64
	AcquiredCount int
	// Checked is how many acquired files the player was asked about before the
	// budget for one request ran out. Below AcquiredCount, the rest is unknown
	// rather than paired or unpaired.
	Checked     int
	PairedCount int
	Unpaired    []UnpairedTrack
	// Pushed and LastPushedAt are what the snapshot says was last sent, and are
	// the one part of this that is a record rather than a reading.
	Pushed           int
	LastPushedAt     time.Time
	RemotePlaylistID string
}

// PlaylistPairing answers the question a push leaves behind: which files did
// the player not know about?
//
// It asks the player rather than reading a stored answer, because there is no
// stored answer that would still be true — a file the player learned about an
// hour after the push would sit in it forever, and the list would age into a
// complaint about a problem that fixed itself. Nothing here writes anything:
// no snapshot, no entry, no push. It is a reading, and reading it twice is the
// same as reading it once.
func (syncer *Syncer) PlaylistPairing(
	ctx context.Context, playlistID uuid.UUID,
) (PairingReport, error) {
	report := PairingReport{PlaylistID: playlistID}
	playlist, err := syncer.store.Playlist(ctx, playlistID)
	if err != nil {
		return report, fmt.Errorf("read playlist %s: %w", playlistID, err)
	}
	report.EntryCount = playlist.EntryCount

	snapshot, snapshotTracks, pushed, err := syncer.readSnapshot(ctx, playlistID)
	if err != nil {
		return report, err
	}
	if pushed {
		report.RemotePlaylistID = snapshot.NavidromePlaylistID
		report.Pushed = len(snapshotTracks)
		if snapshot.PushedAt.Valid {
			report.LastPushedAt = snapshot.PushedAt.Time
		}
	}

	acquired, err := syncer.store.AcquiredPlaylistTracks(ctx, playlistID)
	if err != nil {
		return report, fmt.Errorf("read the acquired tracks of playlist %s: %w", playlistID, err)
	}
	report.AcquiredCount = len(acquired)

	client, _, configured, err := syncer.connect(ctx)
	if err != nil || !configured {
		return report, err
	}
	report.Configured = true

	bounded, cancel := context.WithTimeout(ctx, pairingTimeout)
	defer cancel()
	paired, unpaired, err := syncer.lookup(bounded, client, acquired)
	// A report is a picture and never a push, so the refusal a pass makes when
	// nothing pairs is not made here: nothing paired is exactly the state
	// somebody is looking at this to understand. Running out of time is
	// answered the same way — with what was checked, and how much that was.
	if err != nil && !(errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil) {
		return report, err
	}
	report.PairedCount = len(paired)
	report.Unpaired = unpaired
	report.Checked = len(paired) + len(unpaired)
	return report, nil
}

// Sweep adopts the playlists the configured account owns and Schall does not
// know yet, then asks for a pass over every want list.
func (syncer *Syncer) Sweep(ctx context.Context) (SweepResult, error) {
	var result SweepResult
	client, settings, configured, err := syncer.connect(ctx)
	if err != nil || !configured {
		return result, err
	}
	result.Configured = true

	bounded, cancel := context.WithTimeout(ctx, syncTimeout)
	defer cancel()

	remote, err := client.Playlists(bounded)
	if err != nil {
		return result, fmt.Errorf("read the navidrome playlists: %w", err)
	}
	for _, listed := range remote {
		// Only what this account owns. A list somebody else shared with it is
		// theirs, and adopting it would make Schall push into another person's
		// playlist on the strength of being able to see it.
		if strings.TrimSpace(listed.Owner) != strings.TrimSpace(settings.Username) {
			result.Skipped++
			continue
		}
		_, err := syncer.store.SnapshotByNavidromeID(bounded, listed.ID)
		if err == nil {
			result.Skipped++
			continue
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return result, fmt.Errorf("read the snapshot for navidrome playlist %s: %w", listed.ID, err)
		}
		adopted, unknown, err := syncer.adopt(bounded, client, listed)
		if err != nil {
			return result, err
		}
		result.Adopted = append(result.Adopted, adopted)
		result.UnknownAdditions = append(result.UnknownAdditions, unknown...)
	}

	// Every want list, not only the adopted ones: the sweep is how a list that
	// nobody pressed anything for gets pushed at all.
	lists, err := syncer.store.Playlists(bounded)
	if err != nil {
		return result, fmt.Errorf("read the playlists: %w", err)
	}
	for _, list := range lists {
		if err := syncer.store.QueueNavidromePlaylistSync(bounded, list.ID); err != nil {
			return result, fmt.Errorf("queue the sync of playlist %s: %w", list.ID, err)
		}
		result.Queued++
	}
	return result, nil
}

// connect builds the client for one pass from the settings as they are now.
// The third value is false when there is nothing configured to talk to.
func (syncer *Syncer) connect(
	ctx context.Context,
) (playlistClient, db.NavidromeSettingsRow, bool, error) {
	row, err := syncer.store.NavidromeSettings(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, row, false, nil
	}
	if err != nil {
		return nil, row, false, fmt.Errorf("read navidrome settings: %w", err)
	}
	if !row.Enabled {
		return nil, row, false, nil
	}
	client, err := syncer.newClient(Options{
		BaseURL:    row.BaseURL,
		Username:   row.Username,
		Password:   row.Password,
		ClientName: SyncClientName,
	})
	if err != nil {
		return nil, row, false, err
	}
	return client, row, true, nil
}

// pairedTrack is one acquired file and the song Navidrome holds for it.
type pairedTrack struct {
	track db.AcquiredPlaylistTrackRow
	song  Song
}

// songSets are the ids one pass reasons with.
//
// known is every song this list is not surprised by: what the snapshot holds,
// what a repair re-pointed a pushed track at, and what a Navidrome-side
// addition has just been taken in as. Anything outside it is something the user
// put there.
//
// pushed is the narrower set Schall itself sent, and it is the only set a
// removal may be drawn from. A song Schall never pushed is never taken out of
// the player's playlist, whatever the want list says about it — including a
// song adopted moments ago in this same pass, which the user added and Schall
// has no business removing because it could not pair it back.
type songSets struct {
	known  map[string]struct{}
	pushed map[string]struct{}
}

func newSongSets(snapshotTracks []db.NavidromePlaylistSnapshotTrackRow) songSets {
	sets := songSets{
		known:  make(map[string]struct{}, len(snapshotTracks)),
		pushed: make(map[string]struct{}, len(snapshotTracks)),
	}
	for _, track := range snapshotTracks {
		sets.known[track.NavidromeSongID] = struct{}{}
		sets.pushed[track.NavidromeSongID] = struct{}{}
	}
	return sets
}

// syncOne is the pass over one playlist, in the order docs/decisions/0004,
// 0010 and 0012 put it: pair what is acquired, tell a removal from a break,
// repair what broke, take in what the user added, push, and write down what was
// pushed.
func (syncer *Syncer) syncOne(
	ctx context.Context, client playlistClient, result SyncResult,
) (SyncResult, error) {
	playlistID := result.PlaylistID
	playlist, err := syncer.store.Playlist(ctx, playlistID)
	if err != nil {
		return result, fmt.Errorf("read playlist %s: %w", playlistID, err)
	}
	acquired, err := syncer.store.AcquiredPlaylistTracks(ctx, playlistID)
	if err != nil {
		return result, fmt.Errorf("read the acquired tracks of playlist %s: %w", playlistID, err)
	}
	snapshot, snapshotTracks, pushed, err := syncer.readSnapshot(ctx, playlistID)
	if err != nil {
		return result, err
	}

	paired, unpaired, err := syncer.pair(ctx, client, acquired)
	if err != nil {
		return result, err
	}
	result.Unpaired = unpaired

	if !pushed {
		return syncer.createRemote(ctx, client, playlist, paired, result)
	}
	result.RemotePlaylistID = snapshot.NavidromePlaylistID

	remote, err := client.Playlist(ctx, snapshot.NavidromePlaylistID)
	if errors.Is(err, ErrNotFound) {
		// The list was deleted in Navidrome. Forgetting the snapshot is the
		// whole answer: what was pushed is gone, so nothing in it can be read as
		// a removal, and the want list is untouched because leaving Navidrome is
		// not the statement "stop wanting this music". A later pass finds a list
		// with no snapshot and creates it again — see 0012.
		if err := syncer.store.DeleteNavidromePlaylistSnapshot(ctx, playlistID); err != nil {
			return result, fmt.Errorf("forget the snapshot of playlist %s: %w", playlistID, err)
		}
		result.RemoteDeleted = true
		return result, nil
	}
	if err != nil {
		return result, fmt.Errorf("read navidrome playlist %s: %w", snapshot.NavidromePlaylistID, err)
	}

	songs := newSongSets(snapshotTracks)
	result, err = syncer.reconcileAbsent(ctx, client, remote, snapshotTracks, songs, result)
	if err != nil {
		return result, err
	}
	result, err = syncer.adoptAdditions(ctx, playlistID, remote, songs, result)
	if err != nil {
		return result, err
	}

	// Read again: entries have just been deleted and added, and what is pushed
	// is what is wanted now rather than what was wanted when the pass started.
	acquired, err = syncer.store.AcquiredPlaylistTracks(ctx, playlistID)
	if err != nil {
		return result, fmt.Errorf("read the acquired tracks of playlist %s: %w", playlistID, err)
	}
	paired, unpaired, err = syncer.pair(ctx, client, acquired)
	if err != nil {
		return result, err
	}
	result.Unpaired = unpaired

	if err := syncer.push(ctx, client, remote, paired, songs); err != nil {
		return result, err
	}
	if err := syncer.writeSnapshot(ctx, playlistID, remote.ID, snapshot.Adopted, paired); err != nil {
		return result, err
	}
	result.Pushed = len(paired)
	return result, nil
}

// readSnapshot reads what this list was last told, if it has ever been told
// anything. Never having been pushed is the ordinary first state, not an error.
func (syncer *Syncer) readSnapshot(ctx context.Context, playlistID uuid.UUID) (
	db.NavidromePlaylistSnapshotRow, []db.NavidromePlaylistSnapshotTrackRow, bool, error,
) {
	snapshot, err := syncer.store.NavidromePlaylistSnapshot(ctx, playlistID)
	if errors.Is(err, pgx.ErrNoRows) {
		return snapshot, nil, false, nil
	}
	if err != nil {
		return snapshot, nil, false, fmt.Errorf("read the snapshot of playlist %s: %w", playlistID, err)
	}
	tracks, err := syncer.store.NavidromePlaylistSnapshotTracks(ctx, playlistID)
	if err != nil {
		return snapshot, nil, false, fmt.Errorf("read the pushed tracks of playlist %s: %w", playlistID, err)
	}
	return snapshot, tracks, true, nil
}

// pair asks the player which song each acquired file is, by its path and
// nothing else.
//
// A file the server does not report at that path is unpaired and carried on
// from: it is a want the player cannot be told about yet, which is not an
// error. But an acquired subset where nothing at all paired is refused, because
// every cause of it — display paths, an unscanned library, a path map that does
// not describe the deployment — makes the whole comparison meaningless, and the
// pass that followed would read the player's listing as the user's doing.
func (syncer *Syncer) pair(
	ctx context.Context, client playlistClient, acquired []db.AcquiredPlaylistTrackRow,
) ([]pairedTrack, []UnpairedTrack, error) {
	paired, unpaired, err := syncer.lookup(ctx, client, acquired)
	if err != nil {
		return nil, nil, err
	}
	if len(acquired) > 0 && len(paired) == 0 {
		return nil, nil, ErrNothingPaired
	}
	return paired, unpaired, nil
}

// lookup asks the player about each acquired file and sorts the answers into
// the two piles, with the reason on every file in the second one.
//
// It reports what it managed before a failure alongside the failure, so that a
// caller which asked for a picture rather than a push — PlaylistPairing — can
// show the part of the list it got through. The sync pass ignores the partial
// answer and acts on the error, because half a comparison is not something to
// push from.
func (syncer *Syncer) lookup(
	ctx context.Context, client playlistClient, acquired []db.AcquiredPlaylistTrackRow,
) ([]pairedTrack, []UnpairedTrack, error) {
	paired := make([]pairedTrack, 0, len(acquired))
	var unpaired []UnpairedTrack
	for _, track := range acquired {
		remotePath := syncer.paths.ToRemote(track.LibraryPath)
		match, err := client.FindSongByPath(ctx, SongLookup{
			Path:   remotePath,
			Artist: track.Artist,
			Title:  track.Title,
		})
		if errors.Is(err, ErrNotFound) {
			unpaired = append(unpaired, unpairedTrack(track, remotePath, err))
			continue
		}
		if err != nil {
			return paired, unpaired, fmt.Errorf("ask navidrome about %s: %w", track.LibraryPath, err)
		}
		paired = append(paired, pairedTrack{track: track, song: match.Song})
	}
	return paired, unpaired, nil
}

// unpairedTrack turns one refusal into the record of it. A refusal that carries
// no detail — nothing else answers ErrNotFound here, but the type does not
// promise that — is still an unpaired file, with no reason to show for it.
func unpairedTrack(
	track db.AcquiredPlaylistTrackRow, remotePath string, err error,
) UnpairedTrack {
	unpaired := UnpairedTrack{
		EntryID:       track.EntryID,
		Position:      track.Position,
		LibraryFileID: track.LibraryFileID,
		LibraryPath:   track.LibraryPath,
		RemotePath:    remotePath,
	}
	var miss *LookupMiss
	if errors.As(err, &miss) {
		unpaired.Reason = miss.Reason
		unpaired.Attempts = miss.Attempts
	}
	return unpaired
}

// createRemote makes the playlist in Navidrome for the first time and writes
// down what it was made with.
//
// A list nothing could be paired for is not created at all: an empty playlist
// would be a statement that Navidrome holds none of this music, and the next
// pass would have a snapshot saying so.
func (syncer *Syncer) createRemote(
	ctx context.Context, client playlistClient,
	playlist db.PlaylistRow, paired []pairedTrack, result SyncResult,
) (SyncResult, error) {
	if len(paired) == 0 {
		return result, nil
	}
	songIDs := make([]string, 0, len(paired))
	for _, pair := range paired {
		songIDs = append(songIDs, pair.song.ID)
	}
	remoteID, err := client.CreatePlaylist(ctx, playlist.Name, songIDs)
	if err != nil {
		return result, fmt.Errorf("create navidrome playlist %q: %w", playlist.Name, err)
	}
	if err := syncer.writeSnapshot(ctx, playlist.ID, remoteID, false, paired); err != nil {
		return result, err
	}
	result.Created = true
	result.RemotePlaylistID = remoteID
	result.Pushed = len(paired)
	return result, nil
}

// reconcileAbsent asks, of every pushed track the listing no longer shows, the
// one question that separates the two reasons it could be missing (0010): does
// the id it was pushed under still mean the file it was pushed for?
func (syncer *Syncer) reconcileAbsent(
	ctx context.Context, client playlistClient, remote Playlist,
	snapshotTracks []db.NavidromePlaylistSnapshotTrackRow,
	songs songSets, result SyncResult,
) (SyncResult, error) {
	listed := make(map[string]struct{}, len(remote.Entries))
	for _, entry := range remote.Entries {
		listed[entry.ID] = struct{}{}
	}
	for _, track := range snapshotTracks {
		if _, present := listed[track.NavidromeSongID]; present {
			continue
		}
		pushedPath := syncer.paths.ToRemote(track.LibraryPath)
		song, err := client.Song(ctx, track.NavidromeSongID)
		switch {
		case errors.Is(err, ErrNotFound):
			// The id resolves to nothing. Not a removal — an identity that broke.
			result, err = syncer.repair(ctx, client, track, songs, result)
			if err != nil {
				return result, err
			}
		case err != nil:
			return result, fmt.Errorf("ask navidrome about song %s: %w", track.NavidromeSongID, err)
		case song.Path == pushedPath:
			// The id still means the file it was pushed for, so its absence from
			// the listing is the user having taken it out.
			result, err = syncer.removeEntry(ctx, track, result)
			if err != nil {
				return result, err
			}
		default:
			// The id resolves to some other file. Also not a removal.
			result, err = syncer.repair(ctx, client, track, songs, result)
			if err != nil {
				return result, err
			}
		}
	}
	return result, nil
}

// repair re-points a broken pairing at the id the same file answers to now, or
// leaves it alone (0012). Nothing is removed either way.
func (syncer *Syncer) repair(
	ctx context.Context, client playlistClient,
	track db.NavidromePlaylistSnapshotTrackRow, songs songSets, result SyncResult,
) (SyncResult, error) {
	// Asked by path and file name alone: the snapshot is the record of what was
	// pushed and holds no words for the file, and reading them from the library
	// here would be a repair looking up something it was not told.
	match, err := client.FindSongByPath(ctx, SongLookup{Path: syncer.paths.ToRemote(track.LibraryPath)})
	if errors.Is(err, ErrNotFound) {
		result.BreaksLeft = append(result.BreaksLeft, track.LibraryPath)
		return result, nil
	}
	if err != nil {
		return result, fmt.Errorf("look for the file at %s: %w", track.LibraryPath, err)
	}
	song := match.Song
	if err := syncer.store.RepairNavidromeSnapshotTrack(
		ctx, track.PlaylistID, track.Position, song.ID,
	); err != nil {
		return result, fmt.Errorf("repair the pushed track at %s: %w", track.LibraryPath, err)
	}
	// The id the file answers to now is the same pushed track under another
	// name: known, so that a user who put the file back by hand is not read as
	// having added something Schall never sent, and pushed, because it is still
	// a track Schall put in this playlist.
	songs.known[song.ID] = struct{}{}
	songs.pushed[song.ID] = struct{}{}
	result.BreaksRepaired++
	return result, nil
}

// removeEntry acts on a confirmed removal by taking the track out of the want
// list. A pushed track that no longer names an entry is reported instead: there
// is nothing left to remove.
func (syncer *Syncer) removeEntry(
	ctx context.Context, track db.NavidromePlaylistSnapshotTrackRow, result SyncResult,
) (SyncResult, error) {
	if !track.PlaylistEntryID.Valid {
		result.RemovalsWithoutEntry++
		return result, nil
	}
	if err := syncer.store.DeletePlaylistEntry(ctx, track.PlaylistEntryID.UUID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			result.RemovalsWithoutEntry++
			return result, nil
		}
		return result, fmt.Errorf("remove playlist entry %s: %w", track.PlaylistEntryID.UUID, err)
	}
	result.RemovalsActed++
	return result, nil
}

// adoptAdditions takes what the user added in Navidrome into the want list.
//
// A song the library does not hold is reported and nothing else: Schall can
// only add an entry for music it has, and inventing a want from a path would be
// guessing at what the file is.
func (syncer *Syncer) adoptAdditions(
	ctx context.Context, playlistID uuid.UUID, remote Playlist,
	songs songSets, result SyncResult,
) (SyncResult, error) {
	for _, song := range remote.Entries {
		if _, seen := songs.known[song.ID]; seen {
			continue
		}
		file, err := syncer.store.LibraryFileByPath(ctx, syncer.paths.ToLocal(song.Path))
		if errors.Is(err, pgx.ErrNoRows) {
			result.UnknownAdditions = append(result.UnknownAdditions, song.Path)
			continue
		}
		if err != nil {
			return result, fmt.Errorf("look up the library file at %s: %w", song.Path, err)
		}
		if _, err := syncer.store.AppendPlaylistEntry(ctx, db.AppendPlaylistEntryParams{
			PlaylistID:    playlistID,
			EntryTitle:    entryTitle(song, file),
			LibraryFileID: file.ID,
		}); err != nil {
			return result, fmt.Errorf("add %s to playlist %s: %w", file.Path, playlistID, err)
		}
		songs.known[song.ID] = struct{}{}
		result.AdditionsAdopted++
	}
	return result, nil
}

// entryTitle is what the new entry is called. The song's own title is the
// player's reading of the file's tags; the file name is the fallback, because
// an entry has to carry a title or an ISRC to exist at all and this entry is
// answered by a file rather than searched for by its words.
func entryTitle(song Song, file db.LibraryFileRefRow) string {
	if title := strings.TrimSpace(song.Title); title != "" {
		return title
	}
	return filepath.Base(file.Path)
}

// push brings the remote list to the acquired subset in one update.
//
// Order beyond appending is not maintained: Subsonic adds at the end, and
// reordering a playlist somebody may have arranged themselves would be Schall
// overwriting a decision it was never asked to make.
//
// What is removed is only ever something Schall pushed and no longer wants. A
// song in the listing that Schall never pushed and cannot hold is left where it
// is — it was reported as an addition nobody could take in, and removing it
// would be acting on exactly the thing that was refused.
func (syncer *Syncer) push(
	ctx context.Context, client playlistClient, remote Playlist,
	paired []pairedTrack, songs songSets,
) error {
	wanted := make(map[string]struct{}, len(paired))
	for _, pair := range paired {
		wanted[pair.song.ID] = struct{}{}
	}

	var removeIndexes []int
	listed := make(map[string]struct{}, len(remote.Entries))
	for index, entry := range remote.Entries {
		listed[entry.ID] = struct{}{}
		if _, want := wanted[entry.ID]; want {
			continue
		}
		if _, ours := songs.pushed[entry.ID]; !ours {
			continue
		}
		// Positions in the one listing read at the start of this pass. The
		// server reads them all against that listing, so they are not adjusted
		// for each other.
		removeIndexes = append(removeIndexes, index)
	}

	var addSongIDs []string
	added := make(map[string]struct{}, len(paired))
	for _, pair := range paired {
		if _, present := listed[pair.song.ID]; present {
			continue
		}
		if _, twice := added[pair.song.ID]; twice {
			continue
		}
		added[pair.song.ID] = struct{}{}
		addSongIDs = append(addSongIDs, pair.song.ID)
	}

	if len(removeIndexes) == 0 && len(addSongIDs) == 0 {
		return nil
	}
	if err := client.UpdatePlaylist(ctx, PlaylistUpdate{
		PlaylistID:    remote.ID,
		AddSongIDs:    addSongIDs,
		RemoveIndexes: removeIndexes,
	}); err != nil {
		return fmt.Errorf("update navidrome playlist %s: %w", remote.ID, err)
	}
	return nil
}

// writeSnapshot records what is now pushed, in full.
//
// The path written down is the Schall path — the one Schall wrote and owns —
// and never the path Navidrome reported for the same file. The mapping between
// them is applied when a comparison is made, so that a stored path does not
// silently become wrong the day a mount is renamed; the snapshot is the record
// of what Schall pushed, in Schall's own terms.
func (syncer *Syncer) writeSnapshot(
	ctx context.Context, playlistID uuid.UUID, remoteID string, adopted bool, paired []pairedTrack,
) error {
	tracks := make([]db.NavidromePlaylistSnapshotTrackParams, 0, len(paired))
	for index, pair := range paired {
		tracks = append(tracks, db.NavidromePlaylistSnapshotTrackParams{
			Position:        int32(index + 1),
			NavidromeSongID: pair.song.ID,
			LibraryPath:     pair.track.LibraryPath,
			LibraryFileID:   uuid.NullUUID{UUID: pair.track.LibraryFileID, Valid: true},
			PlaylistEntryID: uuid.NullUUID{UUID: pair.track.EntryID, Valid: true},
		})
	}
	if err := syncer.store.ReplaceNavidromePlaylistSnapshot(ctx,
		db.NavidromePlaylistSnapshotParams{
			PlaylistID:          playlistID,
			NavidromePlaylistID: remoteID,
			Adopted:             adopted,
		}, tracks,
	); err != nil {
		return fmt.Errorf("record what was pushed to playlist %s: %w", playlistID, err)
	}
	return nil
}

// adopt takes a playlist created in Navidrome into Schall as a want list of its
// own, with an entry for every song the library already holds.
func (syncer *Syncer) adopt(
	ctx context.Context, client playlistClient, listed Playlist,
) (uuid.UUID, []string, error) {
	playlist, err := syncer.store.UpsertNavidromePlaylist(ctx, listed.ID, listed.Name)
	if err != nil {
		return uuid.Nil, nil, fmt.Errorf("adopt navidrome playlist %s: %w", listed.ID, err)
	}
	detail, err := client.Playlist(ctx, listed.ID)
	if err != nil {
		return uuid.Nil, nil, fmt.Errorf("read navidrome playlist %s: %w", listed.ID, err)
	}

	var unknown []string
	tracks := make([]db.NavidromePlaylistSnapshotTrackParams, 0, len(detail.Entries))
	for _, song := range detail.Entries {
		file, err := syncer.store.LibraryFileByPath(ctx, syncer.paths.ToLocal(song.Path))
		if errors.Is(err, pgx.ErrNoRows) {
			unknown = append(unknown, song.Path)
			continue
		}
		if err != nil {
			return uuid.Nil, nil, fmt.Errorf("look up the library file at %s: %w", song.Path, err)
		}
		entry, err := syncer.store.AppendPlaylistEntry(ctx, db.AppendPlaylistEntryParams{
			PlaylistID:    playlist.ID,
			EntryTitle:    entryTitle(song, file),
			LibraryFileID: file.ID,
		})
		if err != nil {
			return uuid.Nil, nil, fmt.Errorf("add %s to playlist %s: %w", file.Path, playlist.ID, err)
		}
		tracks = append(tracks, db.NavidromePlaylistSnapshotTrackParams{
			Position:        int32(len(tracks) + 1),
			NavidromeSongID: song.ID,
			LibraryPath:     file.Path,
			LibraryFileID:   uuid.NullUUID{UUID: file.ID, Valid: true},
			PlaylistEntryID: uuid.NullUUID{UUID: entry.ID, Valid: true},
		})
	}
	if err := syncer.store.ReplaceNavidromePlaylistSnapshot(ctx,
		db.NavidromePlaylistSnapshotParams{
			PlaylistID:          playlist.ID,
			NavidromePlaylistID: listed.ID,
			Adopted:             true,
		}, tracks,
	); err != nil {
		return uuid.Nil, nil, fmt.Errorf("record the adopted playlist %s: %w", listed.ID, err)
	}
	syncer.logger.Info().
		Str("playlist_id", playlist.ID.String()).
		Str("navidrome_playlist_id", listed.ID).
		Int("tracks", len(tracks)).
		Msg("adopted a playlist created in navidrome")
	return playlist.ID, unknown, nil
}
