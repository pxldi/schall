package tracksource

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/tagging"
	"github.com/rs/zerolog"
)

// NameStore is what naming a file from its address reads and writes.
type NameStore interface {
	KeyedFileToName(ctx context.Context, targetID uuid.UUID) (db.SourceFileToName, error)
	NameSourceFile(ctx context.Context, params db.NameSourceFileParams) (uuid.UUID, error)
}

// Artwork fetches a picture and refuses anything that is not one.
// internal/soundcloud's client is the implementation: its fetch is a plain
// image GET, whichever service the address is on.
type Artwork interface {
	Artwork(ctx context.Context, artworkURL string) ([]byte, string, error)
}

// Namer names the file a keyed want was acquired with, the way ADR 0026 §4-§6
// names a file a person adopted: the service's title and artist as tags, ALBUM
// repeating the title, COMMENT holding the address, the artwork embedded and
// held for cover.jpg, and the uploader as an artist row keyed by service and
// account (ADR 0038 §5).
type Namer struct {
	store   NameStore
	artwork Artwork
	logger  zerolog.Logger
}

func NewNamer(store NameStore, artwork Artwork, logger zerolog.Logger) *Namer {
	return &Namer{store: store, artwork: artwork, logger: logger}
}

// NameKeyedFile names one want's file. The rows are written before the tags,
// and a tag write that fails is logged: a volume mounted read-only must not
// undo what the person confirmed.
func (namer *Namer) NameKeyedFile(ctx context.Context, targetID uuid.UUID) error {
	file, err := namer.store.KeyedFileToName(ctx, targetID)
	if err != nil {
		return err
	}
	log := namer.logger.With().Str("acquisition_target_id", targetID.String()).
		Str("library_file_id", file.LibraryFileID.String()).Str("path", file.Path).Logger()

	title := strings.TrimSpace(file.Lookup.Title)
	artist := strings.TrimSpace(file.Lookup.Artist)
	if artist == "" {
		artist = strings.TrimSpace(file.Lookup.Uploader)
	}

	var image []byte
	var contentType string
	if namer.artwork != nil && strings.TrimSpace(file.Lookup.ArtworkURL) != "" {
		image, contentType, err = namer.artwork.Artwork(ctx, file.Lookup.ArtworkURL)
		if err != nil {
			log.Warn().Err(err).Str("artwork_url", file.Lookup.ArtworkURL).
				Msg("fetch the artwork of a keyed want's track")
			image, contentType = nil, ""
		}
	}

	if _, err := namer.store.NameSourceFile(ctx, db.NameSourceFileParams{
		LibraryFileID: file.LibraryFileID, Source: file.Source, ExternalID: file.ExternalID,
		Title: title, Artist: artist, Uploader: file.Lookup.Uploader, AccountID: file.Lookup.AccountID,
		UploaderSummary: uploaderSummary(file.Source),
		Image:           image, ContentType: contentType,
	}); err != nil {
		return fmt.Errorf("name the file of want %s: %w", targetID, err)
	}

	if title == "" {
		return nil
	}
	if err := tagging.Write(file.Path, tagging.Tags{
		Title: title, Album: title, Artist: artist, AlbumArtist: artist, Comment: file.ExternalURL,
	}); err != nil {
		log.Warn().Err(err).Msg("write the keyed address's tags into the file")
		return nil
	}
	if len(image) == 0 || ctx.Err() != nil {
		return nil
	}
	if err := tagging.WriteArtwork(file.Path, image); err != nil {
		log.Warn().Err(err).Msg("write the keyed address's artwork into the file")
	}
	return nil
}

// uploaderSummary is the one sentence an uploader's artist row carries about
// why the library holds them.
func uploaderSummary(source string) string {
	service := map[string]string{
		SoundCloud: "SoundCloud", YouTube: "YouTube", Bandcamp: "Bandcamp",
	}[source]
	if service == "" {
		service = source
	}
	return "In the library because a track they uploaded to " + service + " is."
}
