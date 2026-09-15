package activity

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/dbtest"
)

// The feed reads records other parts of Schall already wrote. This seeds two of
// them the way those parts write them, and reads the story back.
func TestTheFeedReadsWhatOtherPartsAlreadyWroteDown(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	reader := New(pool)

	targetID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO acquisition_targets (
		    id, origin, entry_artist, entry_title, status,
		    musicbrainz_recording_id, resolution_method, resolved_at
		)
		VALUES ($1, 'manual', 'Talk Talk', 'Ascension Day', 'pending', $2, 'isrc', now())
	`, targetID, uuid.New()); err != nil {
		t.Fatal(err)
	}
	for _, offer := range []struct {
		verdict string
		summary string
	}{
		{"accepted", "Proven by its fingerprint and imported."},
		{"discarded_audio", "The audio is a different recording."},
	} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO acquisition_target_files (
			    acquisition_target_id, provider, source_username, remote_path,
			    verdict, summary
			) VALUES ($1, 'slskd', 'somebody', $2, $3, $4)
		`, targetID, "music/"+offer.verdict+".flac", offer.verdict, offer.summary); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_file_removals (
		    library_file_id, library_path, musicbrainz_recording_id, kept_path,
		    removed_by, reason
		) VALUES ($1, '/music/gone.flac', $2, '/music/kept.mp3',
		          'the shrink-the-library sweep', 're-encoded to mp3 at 320')
	`, uuid.New(), uuid.New()); err != nil {
		t.Fatal(err)
	}

	events, err := reader.Since(ctx, time.Now().Add(-Window))
	if err != nil {
		t.Fatalf("Since() error = %v", err)
	}

	kinds := map[Kind]Event{}
	for _, event := range events {
		kinds[event.Kind] = event
	}
	if len(kinds) != 3 {
		t.Fatalf("kinds = %v, want an arrival, a refusal and a removal", kinds)
	}
	// The sentence is the one the record already carried. The feed passes it on
	// and never writes one of its own.
	if kinds[Refused].Detail != "The audio is a different recording." {
		t.Errorf("refusal read %q", kinds[Refused].Detail)
	}
	if kinds[Removed].Detail != "re-encoded to mp3 at 320" {
		t.Errorf("removal read %q", kinds[Removed].Detail)
	}
	// The library row is gone by the time a licence is written, so the file's
	// own name is the only thing left that a person recognises. Without it the
	// row read "Deleted · A file".
	if kinds[Removed].Title != "gone.flac" {
		t.Errorf("the removal named %q", kinds[Removed].Title)
	}
	if kinds[Arrived].Title != "Ascension Day" || kinds[Arrived].Artist != "Talk Talk" {
		t.Errorf("arrival named %q by %q", kinds[Arrived].Title, kinds[Arrived].Artist)
	}
}

// Nothing older than the window. A feed that answers "what happened while I was
// away" with last month's work is answering a different question.
func TestTheFeedLeavesOutWhatIsOlderThanItsWindow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	if _, err := pool.Exec(ctx, `
		INSERT INTO library_file_removals (
		    library_file_id, library_path, musicbrainz_recording_id, kept_path,
		    removed_by, reason, removed_at
		) VALUES ($1, '/music/old.flac', $2, '/music/kept.mp3',
		          'the person at the duplicates screen', 'a better copy was kept',
		          now() - interval '3 days')
	`, uuid.New(), uuid.New()); err != nil {
		t.Fatal(err)
	}

	events, err := New(pool).Since(ctx, time.Now().Add(-Window))
	if err != nil {
		t.Fatalf("Since() error = %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("events = %+v, want none", events)
	}
}
