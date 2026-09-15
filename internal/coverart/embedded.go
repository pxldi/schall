package coverart

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png" // registered so a PNG cover decodes like a JPEG one
	"os"

	"github.com/dhowden/tag"
	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/db"
	"golang.org/x/image/draw"
)

const (
	// NameEmbedded says the picture came out of a file in this library rather
	// than off the internet.
	NameEmbedded = "embedded"
	// coverWidth is the size everything here is stored at. The archives are asked
	// for 500 pixels; a picture out of a file arrives at whatever size somebody
	// packed it at, which is a few megabytes often enough to matter when it is
	// going in a database and beside a name.
	coverWidth = 500
	// coverQuality is the JPEG quality a re-encoded cover is stored at. It is a
	// thumbnail on a page, not a print.
	coverQuality = 85
	// maxEmbeddedFiles bounds how many of a release's files are opened looking
	// for a picture. Almost every release answers on the first one, and a box set
	// is not worth reading end to end for a thumbnail.
	maxEmbeddedFiles = 3
)

// Embedded reads the picture packed into a release's own files.
//
// It is the last thing asked, and it is asked about a release the archives had no
// picture of. There is no guessing in it: the files it reads are the ones a track
// mapping already tied to this release, which is a match this project proved
// before it would record it, so the picture inside is a picture of this release.
// A loose image lying in a folder is a different thing and is not read — nobody
// proved anything about that.
//
// Nothing here reaches matching or identity. A picture is display, and a file's
// artwork says nothing about what the file is.
func Embedded(paths []string) (Artwork, error) {
	for index, path := range paths {
		if index >= maxEmbeddedFiles {
			break
		}
		artwork, err := embeddedIn(path)
		if err == nil {
			return artwork, nil
		}
		if !errors.Is(err, ErrNoArtwork) {
			return Artwork{}, err
		}
	}
	return Artwork{}, ErrNoArtwork
}

// Files is the part of the catalogue that says which local files are this
// release's. It is deliberately this narrow: a picture decides nothing, so
// nothing that decides anything is reachable from here.
type Files interface {
	ReleaseMappedFilePaths(context.Context, db.ReleaseMappedFilePathsParams) ([]string, error)
}

// Library answers with the picture a release's own files are carrying.
type Library struct{ files Files }

func NewLibrary(files Files) *Library { return &Library{files: files} }

func (library *Library) Cover(ctx context.Context, albumID uuid.UUID) (Artwork, error) {
	paths, err := library.files.ReleaseMappedFilePaths(ctx, db.ReleaseMappedFilePathsParams{
		AlbumID: albumID, Limit: maxEmbeddedFiles,
	})
	if err != nil {
		return Artwork{}, fmt.Errorf("list the files of release %s: %w", albumID, err)
	}
	if len(paths) == 0 {
		return Artwork{}, ErrNoArtwork
	}
	return Embedded(paths)
}

func embeddedIn(path string) (Artwork, error) {
	file, err := os.Open(path)
	if err != nil {
		// A file the library knows about and the disk does not is this release's
		// problem, not the sweep's: the next file is tried.
		return Artwork{}, ErrNoArtwork
	}
	defer func() { _ = file.Close() }()

	metadata, err := tag.ReadFrom(file)
	if err != nil {
		return Artwork{}, ErrNoArtwork
	}
	picture := metadata.Picture()
	if picture == nil || len(picture.Data) == 0 {
		return Artwork{}, ErrNoArtwork
	}
	return shrink(picture.Data)
}

// shrink re-encodes a picture at the size everything else is stored at. A cover
// packed into a file is whatever the person who packed it felt like — four
// megabytes of it is common — and this is a thumbnail beside a name.
func shrink(data []byte) (Artwork, error) {
	if len(data) > maxImageBytes {
		return Artwork{}, ErrNoArtwork
	}
	source, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		// Something that will not decode is not a picture, whatever it claims.
		return Artwork{}, ErrNoArtwork
	}
	bounds := source.Bounds()
	if bounds.Dx() == 0 || bounds.Dy() == 0 {
		return Artwork{}, ErrNoArtwork
	}

	target := bounds
	if bounds.Dx() > coverWidth {
		height := bounds.Dy() * coverWidth / bounds.Dx()
		if height < 1 {
			height = 1
		}
		target = image.Rect(0, 0, coverWidth, height)
	} else {
		target = image.Rect(0, 0, bounds.Dx(), bounds.Dy())
	}

	scaled := image.NewRGBA(target)
	draw.CatmullRom.Scale(scaled, target, source, bounds, draw.Src, nil)

	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, scaled, &jpeg.Options{Quality: coverQuality}); err != nil {
		return Artwork{}, fmt.Errorf("re-encode an embedded cover: %w", err)
	}
	return Artwork{
		Image:       encoded.Bytes(),
		ContentType: "image/jpeg",
		Source:      NameEmbedded,
	}, nil
}
