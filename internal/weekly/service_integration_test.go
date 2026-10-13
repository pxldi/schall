package weekly

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/dbtest"
)

// A lease is a licence to delete (ADR 0021), and these tests run three refreshes
// against the real store: one to put the song on trial, one to announce it, and
// one to carry it out. The player answers that nobody starred anything, so the
// only thing between the song and the remover is who asked for it.

// absentStars is a player that answers for every song: nobody starred it.
type absentStars struct{}

func (absentStars) Stars(_ context.Context, songs []Song) (map[string]SignalRead, error) {
	answers := make(map[string]SignalRead, len(songs))
	for _, song := range songs {
		answers[song.Path] = SignalRead{Reading: Absent}
	}
	return answers, nil
}

type weeklySong struct {
	playlistID  uuid.UUID
	targetID    uuid.UUID
	fileID      uuid.UUID
	recordingID uuid.UUID
}

// wantedByTheWeekly records one song the weekly playlist asked for, the way a
// refresh asks for it.
func wantedByTheWeekly(ctx context.Context, t *testing.T, pool *pgxpool.Pool) weeklySong {
	t.Helper()
	queries := db.New(pool)
	if _, err := queries.SaveWeeklyPlaylistSettings(ctx, db.SaveWeeklyPlaylistSettingsParams{
		Enabled: true, SongsPerWeek: 20, Mode: ModeRemove,
	}); err != nil {
		t.Fatal(err)
	}
	playlist, err := queries.EnsureWeeklyPlaylist(ctx, PlaylistName)
	if err != nil {
		t.Fatal(err)
	}
	song := weeklySong{playlistID: playlist.ID, fileID: uuid.New(), recordingID: uuid.New()}
	entry, err := queries.AppendWeeklyPlaylistEntry(ctx, db.AppendWeeklyPlaylistEntryParams{
		PlaylistID: playlist.ID, MusicBrainzRecordingID: song.recordingID,
		EntryArtist: "Portishead", EntryTitle: "Glory Box",
	})
	if err != nil {
		t.Fatal(err)
	}
	song.targetID, err = queries.CreatePlaylistEntryTarget(ctx, entry.ID, db.CreateAcquisitionTargetParams{
		Origin: "recommendation", EntryArtist: "Portishead", EntryTitle: "Glory Box",
		MusicBrainzRecordingID: uuid.NullUUID{UUID: song.recordingID, Valid: true},
		ResolutionMethod:       pgtype.Text{String: "manual", Valid: true},
		Summary:                "wanted for the weekly playlist",
		WeeklyOnly:             true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return song
}

// arrive records the file the want delivered.
func (song weeklySong) arrive(ctx context.Context, t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_files (id, path, size_bytes, modified_at)
		VALUES ($1, '/music/Weekly/Portishead - Glory Box.flac', 1024, now())
	`, song.fileID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE acquisition_targets
		SET status = 'acquired', acquired_at = now(), acquired_library_file_id = $2,
		    next_attempt_at = NULL
		WHERE id = $1
	`, song.targetID, song.fileID); err != nil {
		t.Fatal(err)
	}
}

// onAPlaylistOfTheirOwn joins the want to an entry of a person's own playlist,
// as an import of that playlist does when it finds the want already there.
func (song weeklySong) onAPlaylistOfTheirOwn(ctx context.Context, t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	var playlistID, entryID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO playlists (source, name) VALUES ('manual', 'Evening') RETURNING id
	`).Scan(&playlistID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO playlist_entries (playlist_id, position, entry_artist, entry_title)
		VALUES ($1, 1, 'Portishead', 'Glory Box')
		RETURNING id
	`, playlistID).Scan(&entryID); err != nil {
		t.Fatal(err)
	}
	if err := db.New(pool).LinkPlaylistEntryTarget(ctx, entryID, song.targetID); err != nil {
		t.Fatal(err)
	}
}

// threeWeeks runs the refreshes that take a song from arrival to removal, and
// calls between after the first, when the song is on trial.
func threeWeeks(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool, remover *fakeRemover, between func(),
) {
	t.Helper()
	now := time.Now()
	service := NewService(db.New(pool), zerolog.Nop()).
		WithStars(absentStars{}).
		WithRemover(remover).
		WithClock(func() time.Time { return now })
	for week := range 3 {
		if _, err := service.Refresh(ctx); err != nil {
			t.Fatalf("refresh %d: %v", week, err)
		}
		if week == 0 && between != nil {
			between()
		}
		now = now.Add(trialWeek + time.Hour)
	}
}

// The control: a song only the weekly playlist asked for is removed when nobody
// keeps it, so the tests below fail on who asked and nothing else.
func TestASongOnlyTheWeeklyAskedForIsRemovedWhenNobodyKeepsIt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	song := wantedByTheWeekly(ctx, t, pool)
	song.arrive(ctx, t, pool)

	remover := &fakeRemover{}
	threeWeeks(ctx, t, pool, remover, nil)

	if len(remover.removed) != 1 || remover.removed[0] != song.fileID {
		t.Fatalf("removed = %v, want the unkept song removed", remover.removed)
	}
}

// A song a person's own playlist also wants is theirs, and a weekly lease must
// not delete it.
func TestASongOnAPersonsPlaylistIsNeverRemovedByTheWeekly(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	song := wantedByTheWeekly(ctx, t, pool)
	song.onAPlaylistOfTheirOwn(ctx, t, pool)
	song.arrive(ctx, t, pool)

	remover := &fakeRemover{}
	threeWeeks(ctx, t, pool, remover, nil)

	if len(remover.removed) != 0 {
		t.Fatalf("removed = %v, want the song a person's playlist wants left alone", remover.removed)
	}
}

// A person can ask for the song while it is on trial. The removal asks again
// right before it deletes.
func TestASongAskedForWhileOnTrialIsNeverRemovedByTheWeekly(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	song := wantedByTheWeekly(ctx, t, pool)
	song.arrive(ctx, t, pool)

	remover := &fakeRemover{}
	threeWeeks(ctx, t, pool, remover, func() {
		song.onAPlaylistOfTheirOwn(ctx, t, pool)
	})

	if len(remover.removed) != 0 {
		t.Fatalf("removed = %v, want the song asked for during its trial left alone", remover.removed)
	}
}

// Wanting the recording by hand returns the weekly's want rather than a second
// one, and that ask is recorded on it.
func TestASongWantedByHandWhileOnTrialIsNeverRemovedByTheWeekly(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	song := wantedByTheWeekly(ctx, t, pool)
	song.arrive(ctx, t, pool)

	remover := &fakeRemover{}
	threeWeeks(ctx, t, pool, remover, func() {
		id, created, err := db.New(pool).CreateAcquisitionTarget(ctx, db.CreateAcquisitionTargetParams{
			Origin: "manual", EntryArtist: "Portishead", EntryTitle: "Glory Box",
			MusicBrainzRecordingID: uuid.NullUUID{UUID: song.recordingID, Valid: true},
			Summary:                "Wanted.",
		})
		if err != nil || created || id != song.targetID {
			t.Fatalf("CreateAcquisitionTarget() = %v, %v, %v; want the weekly's want back", id, created, err)
		}
	})

	if len(remover.removed) != 0 {
		t.Fatalf("removed = %v, want the song a person asked for left alone", remover.removed)
	}
}
