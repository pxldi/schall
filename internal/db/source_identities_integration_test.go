package db

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pxldi/schall/internal/dbtest"
)

func aFile(t *testing.T, ctx context.Context, pool *pgxpool.Pool, path string) uuid.UUID {
	t.Helper()
	fileID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_files (id, path, size_bytes, modified_at)
		VALUES ($1, $2, 1024, now())
	`, fileID, path); err != nil {
		t.Fatal(err)
	}
	return fileID
}

// A source identity names a file by an address on another service. The three
// things it must never be are all refused by the database itself, so no code
// path anywhere can store one of them.
func TestTheDatabaseRefusesASourceIdentityThatIdentifiesNothing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	good := aFile(t, ctx, pool, "/music/uploads/illegal.flac")
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_file_identities (
		    library_file_id, kind, source, external_id, external_url, method,
		    is_manual
		) VALUES ($1, 'source', 'soundcloud', 'soundcloud:293',
		          'https://soundcloud.com/abo/illegal', 'source_url', true)
	`, good); err != nil {
		t.Fatalf("a whole source identity was refused: %v", err)
	}

	refusals := []struct {
		name      string
		statement string
		args      []any
	}{
		{
			// No identifier on the service means the row names nothing.
			name: "a source identity with no external id",
			statement: `INSERT INTO library_file_identities
			    (library_file_id, kind, source, method)
			    VALUES ($1, 'source', 'soundcloud', 'source_url')`,
		},
		{
			name: "a source identity with an empty external id",
			statement: `INSERT INTO library_file_identities
			    (library_file_id, kind, source, external_id, method)
			    VALUES ($1, 'source', 'soundcloud', '  ', 'source_url')`,
		},
		{
			// A row carrying a recording is claiming the other kind of key.
			name: "a source identity that also claims a recording",
			statement: `INSERT INTO library_file_identities
			    (library_file_id, kind, source, external_id, musicbrainz_recording_id, method)
			    VALUES ($1, 'source', 'soundcloud', 'soundcloud:293',
			            '11111111-1111-4111-8111-111111111111', 'source_url')`,
		},
		{
			// The other kinds have nothing to say about a service.
			name: "a local-only decision carrying a service",
			statement: `INSERT INTO library_file_identities
			    (library_file_id, kind, source, external_id, method)
			    VALUES ($1, 'local_only', 'soundcloud', 'soundcloud:293', 'manual')`,
		},
		{
			name: "an identity of a kind nothing recognises",
			statement: `INSERT INTO library_file_identities
			    (library_file_id, kind, method)
			    VALUES ($1, 'guessed', 'manual')`,
		},
	}

	for _, refusal := range refusals {
		fileID := aFile(t, ctx, pool, "/music/uploads/"+refusal.name+".flac")
		if _, err := pool.Exec(ctx, refusal.statement, fileID); err == nil {
			t.Errorf("%s was stored", refusal.name)
		}
	}
}

// 'source' joins the states a file's resolution can be in, so the library list
// can be narrowed to the tracks Schall holds by address. Nothing else joins it.
func TestASourceIsAResolutionStateAndAGuessIsNot(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	fileID := aFile(t, ctx, pool, "/music/uploads/illegal.flac")
	if _, err := pool.Exec(ctx,
		`UPDATE library_files SET resolution_status = 'source' WHERE id = $1`,
		fileID); err != nil {
		t.Fatalf("'source' was refused as a resolution state: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE library_files SET resolution_status = 'probably' WHERE id = $1`,
		fileID); err == nil {
		t.Fatal("a resolution state nothing recognises was stored")
	}
}

// One artist row per account on a service. The key is the service and its own
// identifier there, never the name: an uploader called "Abo" and a MusicBrainz
// artist called "Abo" are two people until somebody says otherwise.
func TestAnArtistIsKeyedByTheirAccountAndNotByTheirName(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	insert := `INSERT INTO artists (name, sort_name, followed_at, catalogue_summary, source, external_id)
	           VALUES ('Abo', 'Abo', NULL, 'held', 'soundcloud', $1)`
	if _, err := pool.Exec(ctx, insert, "soundcloud:https://soundcloud.com/abo"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, insert, "soundcloud:https://soundcloud.com/abo"); err == nil {
		t.Fatal("the same account was stored as two artists")
	}
	// A different account of the same name is a different artist.
	if _, err := pool.Exec(ctx, insert, "soundcloud:https://soundcloud.com/abo-music"); err != nil {
		t.Fatalf("a second account of the same name was refused: %v", err)
	}
	// Every artist Schall already holds has no account at all, and any number
	// of them can carry no key without colliding.
	for range 2 {
		if _, err := pool.Exec(ctx, `
			INSERT INTO artists (musicbrainz_id, name, sort_name, followed_at)
			VALUES ($1, 'Abo', 'Abo', now())
		`, uuid.New()); err != nil {
			t.Fatalf("an artist with no account was refused: %v", err)
		}
	}
}
