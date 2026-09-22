package identity

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/pxldi/schall/internal/musicbrainz"
)

// A source identity says a file is a track published at an address on another
// service, and it is a decision somebody made by hand. Resolution must never
// touch such a file again: asking MusicBrainz about it can only produce a
// second answer that contradicts the first, and the first one is the one a
// person made.
//
// The skip is the `decided` branch of Resolve, which reads is_manual and the
// source kind. This test is what stops a later change to how a source identity
// is written from quietly handing the file back to the resolver.
func TestResolutionLeavesAFileNamedFromAnotherServiceAlone(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := setupResolution(t, ctx)

	if _, err := fixture.pool.Exec(ctx, `
		INSERT INTO library_file_identities (
		    library_file_id, kind, provider, source, external_id, external_url,
		    artist_name, track_title, method, confidence, is_manual, summary
		)
		VALUES ($1, 'source', 'soundcloud', 'soundcloud', 'soundcloud:293',
		        'https://soundcloud.com/abo/illegal', 'Abo',
		        'PinkPantheress - Illegal (Abo Edit)', 'source_url', 1, true,
		        'Recorded by hand as a SoundCloud track.')
	`, fixture.fileID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `
		UPDATE library_files SET resolution_status = 'source' WHERE id = $1
	`, fixture.fileID); err != nil {
		t.Fatal(err)
	}

	// A provider that would happily name the file something else if it were
	// asked. It must not be asked.
	length := 302000
	provider := &stubProvider{results: []musicbrainz.Recording{{
		ID: uuid.New(), Title: "Illegal", ArtistCredit: "PinkPantheress",
		DurationMS: &length,
	}}}
	service := NewService(fixture.pool, provider, zerolog.Nop())
	if err := service.Resolve(ctx, fixture.fileID); err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	var kind, externalID, status string
	if err := fixture.pool.QueryRow(ctx, `
		SELECT library_file_identities.kind,
		       coalesce(library_file_identities.external_id, ''),
		       library_files.resolution_status
		FROM library_file_identities
		JOIN library_files ON library_files.id = library_file_identities.library_file_id
		WHERE library_file_identities.library_file_id = $1
	`, fixture.fileID).Scan(&kind, &externalID, &status); err != nil {
		t.Fatalf("read the identity back: %v", err)
	}
	if kind != "source" || externalID != "soundcloud:293" {
		t.Fatalf("identity = %q/%q; resolution overwrote a decision somebody made",
			kind, externalID)
	}
	if status != StatusSource {
		t.Fatalf("resolution status = %q, want %q", status, StatusSource)
	}
}

// The queue never picks the file up either. Its status is not one of the two
// the queue asks for, so the job is not made in the first place — the skip in
// Resolve above is the second of two guards, not the only one.
func TestTheResolutionQueueDoesNotAskAboutAFileNamedFromAnotherService(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := setupResolution(t, ctx)

	if _, err := fixture.pool.Exec(ctx, `
		UPDATE library_files SET resolution_status = 'source' WHERE id = $1
	`, fixture.fileID); err != nil {
		t.Fatal(err)
	}

	var queued int
	if err := fixture.pool.QueryRow(ctx, `
		WITH unasked AS (
			SELECT id FROM library_files
			WHERE missing_at IS NULL
			  AND resolution_status IN ('pending', 'failed')
		)
		SELECT count(*) FROM unasked WHERE id = $1
	`, fixture.fileID).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 0 {
		t.Fatal("the resolution queue picked up a file somebody already named")
	}
}

// A source identity the want's anchor proved is written with is_manual false
// (ADR 0037 §4). The resolver leaves it alone all the same: its first act is
// to delete every identity nobody wrote by hand, and this one was proven by
// the audio, not by the tags the resolver would read.
func TestResolutionLeavesAFileTheAnchorProvedAlone(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := setupResolution(t, ctx)

	if _, err := fixture.pool.Exec(ctx, `
		INSERT INTO library_file_identities (
		    library_file_id, kind, provider, source, external_id, external_url,
		    method, confidence, is_manual, summary
		)
		VALUES ($1, 'source', 'deezer', 'deezer', 'deezer:2178654',
		        'https://www.deezer.com/track/2178654', 'published-sample', 1, false,
		        'Proven by the sample Deezer publishes for this track.')
	`, fixture.fileID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `
		UPDATE library_files SET resolution_status = 'pending' WHERE id = $1
	`, fixture.fileID); err != nil {
		t.Fatal(err)
	}

	length := 302000
	provider := &stubProvider{results: []musicbrainz.Recording{{
		ID: uuid.New(), Title: "Illegal", ArtistCredit: "PinkPantheress",
		DurationMS: &length,
	}}}
	service := NewService(fixture.pool, provider, zerolog.Nop())
	if err := service.Resolve(ctx, fixture.fileID); err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	var kind, externalID string
	if err := fixture.pool.QueryRow(ctx, `
		SELECT kind, coalesce(external_id, '')
		FROM library_file_identities WHERE library_file_id = $1
	`, fixture.fileID).Scan(&kind, &externalID); err != nil {
		t.Fatalf("read the identity back: %v", err)
	}
	if kind != "source" || externalID != "deezer:2178654" {
		t.Fatalf("identity = %q/%q, want the anchor's proof kept", kind, externalID)
	}
}
