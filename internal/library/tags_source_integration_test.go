package library

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/pxldi/schall/internal/dbtest"
	"github.com/pxldi/schall/internal/tagging"
	"go.senan.xyz/taglib"
)

// writeSourceTags puts on the file exactly what internal/soundcloud puts on
// one: the track's title, the uploader, and the address in the comment.
func writeSourceTags(path string) error {
	return tagging.Write(path, tagging.Tags{
		Title:       "PinkPantheress - Illegal (Abo Edit)",
		Album:       "PinkPantheress - Illegal (Abo Edit)",
		Artist:      "Abo",
		AlbumArtist: "Abo",
		Comment:     "https://soundcloud.com/abo/illegal",
	})
}

// A file Schall holds by its address on another service was never a catalogue
// track's, so it cannot have stopped being one.
//
// The pass that gives a file back the tags it arrived with looks for a file
// Schall tagged that no mapping accounts for. That describes every SoundCloud
// track in the library, permanently: its tags came from what SoundCloud
// published, and having no mapping is its ordinary state. Left unguarded, the
// first time anything asked about the file it would lose the track's name and
// get the uploaded file's name back.
func TestAFileNamedFromAnotherServiceIsNotGivenBackTheTagsItArrivedWith(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)
	path := filepath.Join(rootPath, "illegal.flac")
	fileID := peersCopy(t, pool, rootID, path)

	// The file says what SoundCloud published, exactly as internal/soundcloud
	// writes it, and Schall is marked as the one who wrote it.
	if err := writeSourceTags(path); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_file_identities (
		    library_file_id, kind, provider, source, external_id, external_url,
		    artist_name, track_title, method, is_manual
		) VALUES ($1, 'source', 'soundcloud', 'soundcloud', 'soundcloud:293',
		          'https://soundcloud.com/abo/illegal', 'Abo',
		          'PinkPantheress - Illegal (Abo Edit)', 'source_url', true)
	`, fileID); err != nil {
		t.Fatal(err)
	}

	written, err := newTagger(pool).TagFile(ctx, fileID)
	if err != nil {
		t.Fatalf("write the tags of one file: %v", err)
	}
	if written {
		t.Fatal("a file Schall holds by address was written; it is nothing to do")
	}

	tags, err := taglib.ReadTags(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := tags["TITLE"]; len(got) != 1 || got[0] != "PinkPantheress - Illegal (Abo Edit)" {
		t.Fatalf("TITLE = %q, want the track's name still on the file", got)
	}
	if got := tags["COMMENT"]; len(got) != 1 || got[0] != "https://soundcloud.com/abo/illegal" {
		t.Fatalf("COMMENT = %q, want the address still on the file", got)
	}
}

// A file resolved to 'source' should never carry a track_mappings row —
// internal/matching's ReconcileFile refuses to create one. But a mapping can
// still exist if a scan ran between somebody previewing a SoundCloud track and
// confirming it (internal/soundcloud.Adopt now refuses that too), and if one
// ever does, the tagging pass must not act on it: writing the catalogue's tags
// over a file a person named by hand would take the track's own name off it.
func TestASourceIdentifiedFileIsNeverRetaggedFromTheCatalogue(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)
	path := filepath.Join(rootPath, "illegal.flac")
	fileID := peersCopy(t, pool, rootID, path)

	if err := writeSourceTags(path); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_file_identities (
		    library_file_id, kind, provider, source, external_id, external_url,
		    artist_name, track_title, method, is_manual
		) VALUES ($1, 'source', 'soundcloud', 'soundcloud', 'soundcloud:293',
		          'https://soundcloud.com/abo/illegal', 'Abo',
		          'PinkPantheress - Illegal (Abo Edit)', 'source_url', true)
	`, fileID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE library_files SET resolution_status = 'source' WHERE id = $1
	`, fileID); err != nil {
		t.Fatal(err)
	}
	release := catalogueRelease(t, pool, "Unrelated Artist", "Unrelated Album", "Unrelated Track")
	mapFileToTrack(t, pool, fileID, release.trackID)

	written, err := newTagger(pool).TagFile(ctx, fileID)
	if err != nil {
		t.Fatalf("write the tags of one file: %v", err)
	}
	if written {
		t.Fatal("a file named by hand from SoundCloud was written from the catalogue")
	}

	tags, err := taglib.ReadTags(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := tags["TITLE"]; len(got) != 1 || got[0] != "PinkPantheress - Illegal (Abo Edit)" {
		t.Fatalf("TITLE = %q, want the SoundCloud track's name still on the file", got)
	}
	if got := tags["ARTIST"]; len(got) != 1 || got[0] != "Abo" {
		t.Fatalf("ARTIST = %q, want the uploader still on the file", got)
	}
}
