package db

import (
	"context"
	"testing"
	"time"

	"github.com/pxldi/schall/internal/dbtest"
)

// The weekly playlist puts a song on trial: it grants a lease, watches whether
// the player's star ever lands on it, and deletes it if none does. Every part
// of that reads the MusicBrainz recording — the suppression of songs the
// library already holds, and the read of the star back against the recording.
//
// A file Schall holds by its address on another service has no recording, so
// none of that can be done honestly. It is passed over entirely: a lease is the
// only licence to delete music (ADR 0021), and the machinery never puts a file
// it cannot reason about on trial.
func TestTheWeeklyPlaylistPassesOverAFileNamedFromAnotherService(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	queries := New(dbtest.Setup(t))
	fixture := seedWeeklySong(ctx, t, queries, "Illegal (Abo Edit)")
	fixture.acquire(ctx, t, queries)

	arrivals, err := queries.WeeklyPlaylistArrivals(ctx, fixture.playlistID)
	if err != nil {
		t.Fatal(err)
	}
	if len(arrivals) != 1 {
		t.Fatalf("the obtained song was not offered as an arrival: %+v", arrivals)
	}

	// Somebody says the file is a SoundCloud track. Whatever it was acquired
	// for, what Schall now holds it as is an address, and the weekly playlist
	// stops offering it.
	if _, err := queries.db.Exec(ctx, `
		DELETE FROM library_file_identities WHERE library_file_id = $1
	`, fixture.fileID); err != nil {
		t.Fatal(err)
	}
	if _, err := queries.db.Exec(ctx, `
		INSERT INTO library_file_identities (
		    library_file_id, kind, provider, source, external_id, external_url,
		    method, is_manual
		) VALUES ($1, 'source', 'soundcloud', 'soundcloud', 'soundcloud:293',
		          'https://soundcloud.com/abo/illegal', 'source_url', true)
	`, fixture.fileID); err != nil {
		t.Fatal(err)
	}

	arrivals, err = queries.WeeklyPlaylistArrivals(ctx, fixture.playlistID)
	if err != nil {
		t.Fatal(err)
	}
	if len(arrivals) != 0 {
		t.Fatalf("a file Schall holds by address was put on trial: %+v", arrivals)
	}
}
