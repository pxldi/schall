package coverart

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/db"
	"github.com/rs/zerolog"
)

// What is written, and what stops a picture being written twice.
//
// Navidrome is the playback server that reads the same music from the same disc
// and answers the phone. It finds a release's cover by looking in the release's
// folder, in this order: cover.*, folder.*, front.*, then a picture embedded in
// one of the audio files, then an external service. It finds an artist's picture
// by looking for artist.* in the artist's folder and then in the artist's album
// folders. Those are its defaults and they are what these names follow.
//
// The names are also the record. A folder that already holds any of the three
// cover names is left exactly as it is, and so is an artist folder that already
// holds an artist picture — which covers both cases at once: a picture the user
// put there themselves is never overridden, and a picture Schall wrote last week
// is never fetched again. Nothing about this needs a column.
const (
	coverName  = "cover"
	artistName = "artist"
)

// coverNames are the filenames Navidrome reads a release's cover from, in its
// own order of preference. Any of them present means somebody — the user, an
// earlier pass, another tool — has already answered this folder.
var coverNames = []string{"cover", "folder", "front"}

// handPlacedNames are the cover names Schall never writes. This package writes
// cover.* and only cover.*, so a folder.jpg or a front.png in a folder was put
// there by a person or by another tool, and nothing here replaces one.
var handPlacedNames = []string{"folder", "front"}

// imageExtensions are the names an image file goes by. The list is what gets
// looked for, not what gets written: Schall writes the extension that matches
// the bytes it holds.
var imageExtensions = []string{".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp"}

// CoverIn returns the path of the release cover this folder holds, if it holds
// one. It looks for the same names this package writes and reads, so what it
// finds is a picture Schall can vouch for rather than any image in the folder.
//
// It exists for the layout migration. When the audio of a release moves to a
// new folder, the cover written beside it has to go too — otherwise the new
// folder has no picture until a sweep notices, and the old folder is left
// standing because it is not empty. See internal/library/moves.go.
func CoverIn(folder string) (string, bool) {
	entries, err := os.ReadDir(folder)
	if err != nil {
		return "", false
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		lowered := strings.ToLower(entry.Name())
		extension := filepath.Ext(lowered)
		if !contains(imageExtensions, extension) {
			continue
		}
		if contains(coverNames, strings.TrimSuffix(lowered, extension)) {
			return filepath.Join(folder, entry.Name()), true
		}
	}
	return "", false
}

// picturesPerPass bounds how many pictures one pass writes.
//
// Writing is cheap; fetching the archive's full-size copy of one is not, and
// every one of those is a request to somebody else's server. So a pass writes a
// handful and says there is more to do, which is the same shape as the sweep
// that fills the cache — a library fills over minutes rather than in a burst.
const picturesPerPass = 25

// DiskStore is what the writer reads: where the music is, and which releases and
// artists a picture is already held for.
type DiskStore interface {
	ReleaseFoldersToPicture(context.Context) ([]db.ReleaseFoldersToPictureRow, error)
	ReleaseCoverArt(context.Context, uuid.UUID) (db.ReleaseCoverArtRow, error)
	ArtistFoldersToPicture(context.Context) ([]db.ArtistFoldersToPictureRow, error)
	ArtistImage(context.Context, uuid.UUID) (db.ArtistImageRow, error)
	ListLibraryRoots(context.Context) ([]db.LibraryRootRow, error)
	SourceFilesToPicture(context.Context) ([]db.SourceFileToPictureRow, error)
	FileCoverArt(context.Context, uuid.UUID) (db.FileCoverArtRow, error)
}

// FullFronts fetches the archive's own copy of a release's front cover, at the
// size the archive holds it rather than the thumbnail the cache keeps.
type FullFronts interface {
	FullFront(ctx context.Context, releaseID, releaseGroupID uuid.NullUUID) (Artwork, error)
}

// DiskWriter puts the pictures Schall already trusts where the player can see
// them.
//
// Schall caches every cover and artist picture it fetches, keyed by an
// identifier that identity resolution already settled, and draws its own pages
// from that cache. Navidrome cannot read a Postgres table. Left alone it shows
// whatever a peer happened to embed in the audio — junk quality, or nothing at
// all for a file acquired on its own — while Schall holds the correct picture
// for the same release a few tables away.
//
// This is display and never evidence, exactly as the cache is. Nothing written
// here is read by matching, by identity, or by any import grader, and no audio
// file is opened, moved or changed: an image is a new file beside the music,
// and the music's bytes are the same before and after.
type DiskWriter struct {
	store   DiskStore
	archive FullFronts
	logger  zerolog.Logger

	// budget is picturesPerPass, held rather than read so a test can set one it
	// can reach.
	budget int
}

func NewDiskWriter(store DiskStore, archive FullFronts, logger zerolog.Logger) *DiskWriter {
	return &DiskWriter{store: store, archive: archive, logger: logger, budget: picturesPerPass}
}

// Write puts covers and artist pictures on disc, and reports whether more are
// waiting.
//
// A folder that cannot be written to costs that folder and nothing else. The
// library is somebody's mounted volume and a release filed on a read-only path
// is an ordinary thing to meet; a pass that stopped at the first one would leave
// the rest of the collection unpictured for as long as it stayed there.
func (writer *DiskWriter) Write(ctx context.Context) (bool, error) {
	roots, err := writer.roots(ctx)
	if err != nil {
		return false, err
	}
	written, more := writer.covers(ctx)
	if written < writer.budget {
		fileWritten, fileMore := writer.sourceFiles(ctx, writer.budget-written)
		written += fileWritten
		more = more || fileMore
	}
	if written < writer.budget {
		artistsWritten, artistsMore := writer.artists(ctx, roots, writer.budget-written)
		written += artistsWritten
		more = more || artistsMore
	} else {
		// The budget went on covers. There may or may not be artists waiting;
		// saying there are costs one more pass and never a missed picture.
		more = true
	}
	if written > 0 {
		writer.logger.Info().Int("written", written).Bool("more", more).
			Msg("pictures written into the library")
	}
	return more, nil
}

// covers writes one front cover into every release folder that has none.
func (writer *DiskWriter) covers(ctx context.Context) (int, bool) {
	releases, err := writer.store.ReleaseFoldersToPicture(ctx)
	if err != nil {
		writer.logger.Error().Err(err).Msg("read release folders to picture")
		return 0, false
	}
	written := 0
	for _, release := range releases {
		if ctx.Err() != nil {
			return written, true
		}
		if written >= writer.budget {
			return written, true
		}
		if !usable(release.Folder) {
			continue
		}
		pictured := alreadyPictured(release.Folder, coverNames)
		if pictured && !replaceable(release) {
			continue
		}
		artwork, found := writer.cover(ctx, release)
		if !found {
			continue
		}
		name := coverName + extensionFor(artwork.ContentType)
		destination := filepath.Join(release.Folder, name)
		if pictured && same(destination, artwork.Image) {
			// The supplied cover is already the file in the folder. Writing it
			// again would be the same bytes every pass, forever.
			continue
		}
		if err := place(destination, artwork.Image); err != nil {
			writer.logger.Warn().Err(err).Str("album_id", release.ID.String()).
				Str("folder", release.Folder).Msg("write cover into the library")
			continue
		}
		if pictured {
			writer.tidyCovers(release.Folder, destination)
		}
		written++
	}
	return written, false
}

// replaceable reports whether the picture Schall holds for a release may take
// the place of the cover already in its folder.
//
// Almost never. Every picture an archive gave is answered by the name being
// there: the file on disc is what stops the archive being asked a second time,
// and a pass that rewrote it would ask somebody's server every hour for a
// picture already sitting in the folder.
//
// A cover the person supplied is the exception, because it is not an answer
// Schall went looking for. Somebody looked at the record, said this is the
// picture, and the folder holding an older one is exactly the situation they
// were correcting.
//
// Even then, only a file Schall itself wrote is replaced. cover.* is the one
// name this package ever writes, so a folder.jpg or a front.png is a file
// somebody else put there and the folder is left alone.
func replaceable(release db.ReleaseFoldersToPictureRow) bool {
	if release.Source != NameUser {
		return false
	}
	return !alreadyPictured(release.Folder, handPlacedNames)
}

// same reports whether the file at this path already holds exactly these bytes.
//
// It is what stops a supplied cover being written on every pass. For every
// other picture the presence of the name is the record that the work is done; a
// supplied one replaces what is there, so the only way to know it has already
// been written is to look at what is written.
//
// The length is compared first, so the ordinary answer — a folder holding a
// picture of a different size — costs one stat and no reading at all.
func same(path string, image []byte) bool {
	info, err := os.Stat(path)
	if err != nil || info.Size() != int64(len(image)) {
		return false
	}
	existing, err := os.ReadFile(path)
	return err == nil && bytes.Equal(existing, image)
}

// tidyCovers removes the cover files a folder is left holding after a new one
// went in under a different extension.
//
// A folder with cover.jpg beside cover.png is a folder whose picture is decided
// by whichever the player lists first, and that is not a decision anybody made.
// Only the name this package writes is removed, and never the file just placed:
// folder.* and front.* are somebody else's and are not touched here or anywhere.
func (writer *DiskWriter) tidyCovers(folder, keep string) {
	entries, err := os.ReadDir(folder)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		path := filepath.Join(folder, entry.Name())
		if path == keep {
			continue
		}
		lowered := strings.ToLower(entry.Name())
		extension := filepath.Ext(lowered)
		if !contains(imageExtensions, extension) {
			continue
		}
		if strings.TrimSuffix(lowered, extension) != coverName {
			continue
		}
		if err := os.Remove(path); err != nil {
			writer.logger.Warn().Err(err).Str("path", path).
				Msg("remove the cover a new one replaced")
		}
	}
}

// cover is the picture to write beside one release's music.
//
// The Cover Art Archive is asked for its own full-size copy first, and only for
// a cover the archive gave. The file a phone opens full-screen is the one place
// the five-hundred-pixel copy in the cache looks soft, so the bigger one is
// worth a request — but a picture found by name at iTunes, or read out of one of
// the release's own files, exists nowhere except that cache, and the archive has
// nothing to say about it.
//
// Everywhere the archive does not answer, the cached picture is written instead.
// Schall is already showing it on its own pages, and a player showing nothing
// beside music Schall has pictured is the whole reason this file exists: what
// the pages show, the player gets.
//
// A fetch that failed for some other reason writes nothing at all. The file on
// disc is what stops this being asked again, so a thumbnail written during an
// outage would be the copy the player keeps for good; the next pass asks again.
//
// Both answers have to be a picture, for the same reason. A page an archive
// labelled image/jpeg would be written as cover.jpg, and the folder would then
// hold a cover nobody can open that nothing will ever replace.
func (writer *DiskWriter) cover(
	ctx context.Context, release db.ReleaseFoldersToPictureRow,
) (Artwork, bool) {
	if release.Source == Name {
		artwork, err := writer.archive.FullFront(ctx,
			release.MusicbrainzReleaseID, release.MusicbrainzReleaseGroupID)
		if err == nil && pictured(artwork) {
			return artwork, true
		}
		if err != nil && !errors.Is(err, ErrNoArtwork) {
			writer.logger.Warn().Err(err).Str("album_id", release.ID.String()).
				Msg("fetch full-size cover for the library")
			return Artwork{}, false
		}
	}
	cached, err := writer.store.ReleaseCoverArt(ctx, release.ID)
	if err != nil {
		writer.logger.Warn().Err(err).Str("album_id", release.ID.String()).
			Msg("read the cached cover for the library")
		return Artwork{}, false
	}
	artwork := Artwork{
		Image: cached.Image, ContentType: cached.ContentType, Source: cached.Source,
	}
	if !pictured(artwork) {
		return Artwork{}, false
	}
	return artwork, true
}

// sourceFiles writes the picture of a file Schall holds by its address on
// another service into the folder that file is in.
//
// Such a file belongs to no release, so the release pass above cannot reach it:
// there is no album row and therefore no release folder. The folder here is
// simply the one the file sits in.
//
// One folder holds one cover.jpg. An upload is a flat set of files, so two
// SoundCloud singles can land in the same folder, and the second one finds the
// first one's picture already there and writes nothing. That is accepted rather
// than worked around: the picture that always travels with the right music is
// the one embedded in the file, which internal/soundcloud writes, and this is
// the copy a player that reads folders gets.
func (writer *DiskWriter) sourceFiles(ctx context.Context, budget int) (int, bool) {
	files, err := writer.store.SourceFilesToPicture(ctx)
	if err != nil {
		writer.logger.Error().Err(err).Msg("read the files to picture")
		return 0, false
	}
	written := 0
	for _, file := range files {
		if ctx.Err() != nil {
			return written, true
		}
		if written >= budget {
			return written, true
		}
		if !usable(file.Folder) || alreadyPictured(file.Folder, coverNames) {
			continue
		}
		cached, err := writer.store.FileCoverArt(ctx, file.ID)
		if err != nil {
			writer.logger.Warn().Err(err).Str("file_id", file.ID.String()).
				Msg("read the cached picture for a file")
			continue
		}
		artwork := Artwork{
			Image: cached.Image, ContentType: cached.ContentType, Source: cached.Source,
		}
		if !pictured(artwork) {
			continue
		}
		name := coverName + extensionFor(artwork.ContentType)
		if err := place(filepath.Join(file.Folder, name), artwork.Image); err != nil {
			writer.logger.Warn().Err(err).Str("file_id", file.ID.String()).
				Str("folder", file.Folder).Msg("write a file's picture into the library")
			continue
		}
		written++
	}
	return written, false
}

// artists writes one picture into every artist folder that has none.
//
// The artist folder is worked out from the release folders under it rather than
// from the layout template, because the template can be edited and the folders
// are where the music actually is. It is the longest path every one of the
// artist's release folders sits inside — for the layout Schall ships, that is
// exactly the artist's own folder.
//
// An artist whose releases share no folder above them has no artist folder, and
// none is invented: writing the picture into each release folder instead would
// scatter the same image through the collection, and Navidrome reads it from
// there anyway if it is ever put there by hand.
func (writer *DiskWriter) artists(ctx context.Context, roots []string, budget int) (int, bool) {
	rows, err := writer.store.ArtistFoldersToPicture(ctx)
	if err != nil {
		writer.logger.Error().Err(err).Msg("read artist folders to picture")
		return 0, false
	}
	folders := map[uuid.UUID][]string{}
	order := []uuid.UUID{}
	for _, row := range rows {
		if _, seen := folders[row.ID]; !seen {
			order = append(order, row.ID)
		}
		folders[row.ID] = append(folders[row.ID], row.Folder)
	}

	written := 0
	for _, artistID := range order {
		if ctx.Err() != nil {
			return written, true
		}
		if written >= budget {
			return written, true
		}
		folder, ok := artistFolder(folders[artistID], roots)
		if !ok || alreadyPictured(folder, []string{artistName}) {
			continue
		}
		image, err := writer.store.ArtistImage(ctx, artistID)
		if err != nil || len(image.Image) == 0 {
			continue
		}
		name := artistName + extensionFor(image.ContentType)
		if err := place(filepath.Join(folder, name), image.Image); err != nil {
			writer.logger.Warn().Err(err).Str("artist_id", artistID.String()).
				Str("folder", folder).Msg("write artist picture into the library")
			continue
		}
		written++
	}
	return written, false
}

// roots reads the folders the library is allowed to occupy, cleaned to compare
// against.
func (writer *DiskWriter) roots(ctx context.Context) ([]string, error) {
	rows, err := writer.store.ListLibraryRoots(ctx)
	if err != nil {
		return nil, fmt.Errorf("read library roots: %w", err)
	}
	roots := make([]string, 0, len(rows))
	for _, row := range rows {
		if path := filepath.Clean(strings.TrimSpace(row.Path)); usable(path) {
			roots = append(roots, path)
		}
	}
	return roots, nil
}

// artistFolder is the folder that holds all of one artist's releases, if there
// is one and it is inside the library rather than being the library.
//
// A library root is not an artist folder. Writing artist.jpg at the top of
// somebody's music volume would claim the whole collection belongs to whichever
// artist happened to be written first, and that is a mistake nothing on the
// screen would explain.
func artistFolder(releaseFolders, roots []string) (string, bool) {
	if len(releaseFolders) == 0 || len(roots) == 0 {
		return "", false
	}
	shared := filepath.Clean(releaseFolders[0])
	for _, folder := range releaseFolders[1:] {
		shared = commonFolder(shared, filepath.Clean(folder))
		if shared == "" {
			return "", false
		}
	}
	// One release is filed in its own folder, and the artist's folder is the one
	// above it.
	if len(releaseFolders) == 1 {
		shared = filepath.Dir(shared)
	}
	for _, root := range roots {
		if shared == root {
			return "", false
		}
		if within(root, shared) {
			return shared, true
		}
	}
	return "", false
}

// commonFolder is the longest folder both paths sit inside, or empty when they
// share nothing above the filesystem root.
func commonFolder(left, right string) string {
	leftParts := strings.Split(left, string(filepath.Separator))
	rightParts := strings.Split(right, string(filepath.Separator))
	shared := []string{}
	for index := 0; index < len(leftParts) && index < len(rightParts); index++ {
		if leftParts[index] != rightParts[index] {
			break
		}
		shared = append(shared, leftParts[index])
	}
	if len(shared) <= 1 {
		return ""
	}
	return strings.Join(shared, string(filepath.Separator))
}

// within reports whether path sits strictly inside root.
func within(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return relative != "." && !strings.HasPrefix(relative, "..")
}

// usable rejects a folder no picture may be written into: an empty one, and a
// relative one, which would be resolved against wherever Schall happens to be
// running rather than against the library.
func usable(folder string) bool {
	return folder != "" && filepath.IsAbs(folder)
}

// alreadyPictured reports whether the folder already holds an image under any of
// these names. Case is ignored, because a library that came from Windows is full
// of Cover.jpg and Folder.JPG.
func alreadyPictured(folder string, names []string) bool {
	entries, err := os.ReadDir(folder)
	if err != nil {
		// A folder that cannot be read cannot be written to either, and this
		// answer stops the caller trying.
		return true
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		lowered := strings.ToLower(entry.Name())
		extension := filepath.Ext(lowered)
		if !contains(imageExtensions, extension) {
			continue
		}
		if contains(names, strings.TrimSuffix(lowered, extension)) {
			return true
		}
	}
	return false
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

// extensionFor names the bytes. The archive says what it sent and is believed,
// because a picture with the wrong extension is a picture some players will not
// open.
func extensionFor(contentType string) string {
	switch strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0])) {
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/bmp":
		return ".bmp"
	default:
		return ".jpg"
	}
}

// place writes one image beside the music, and never half of one.
//
// The bytes go to a temporary file in the same folder and are renamed into
// place, which on one filesystem is a single step no reader can catch halfway.
// A scan walking the folder at the wrong moment would not index the file either
// way — a scan reads audio extensions and nothing else — but Navidrome does read
// it, and a truncated cover is a broken frame on somebody's phone.
func place(destination string, image []byte) error {
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".schall-picture-*")
	if err != nil {
		return fmt.Errorf("open a place for %s: %w", destination, err)
	}
	name := temporary.Name()
	defer func() { _ = os.Remove(name) }()

	if _, err := temporary.Write(image); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write %s: %w", destination, err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("write %s: %w", destination, err)
	}
	// Readable by whatever reads the music, which is a different process under a
	// different account as often as not.
	if err := os.Chmod(name, 0o644); err != nil {
		return fmt.Errorf("set the mode of %s: %w", destination, err)
	}
	if err := os.Rename(name, destination); err != nil {
		return fmt.Errorf("put %s in place: %w", destination, err)
	}
	return nil
}
