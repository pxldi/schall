package uploads

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/filecopy"
	"github.com/pxldi/schall/internal/library"
	"github.com/pxldi/schall/internal/transcode"
	"github.com/rs/zerolog"
)

// ImportStore is the one thing an upload writes to the database, and it writes
// it once: where the upload landed, that the managed library is a music folder,
// and that a scan is owed. Nothing here records what a file is — that is the
// resolver's answer, and it is given after the scan, from the audio.
type ImportStore interface {
	CompleteUploadImport(ctx context.Context, uploadID uuid.UUID, rootPath, importPath string) error
}

// Importer takes a staged upload into the library.
//
// What it validates is deliberately narrow. A downloaded release is checked
// against the catalogue edition somebody chose for it, because a stranger's
// folder is a claim; an upload is not a claim about anything, so there is
// nothing for a catalogue to disagree with and nothing for AcoustID to object
// to. Both would be asking whether the user owns their own music.
//
// So the only refusal is about the bytes: a file that is not audio the library
// can hold did not arrive, whatever the browser reported. That is the same
// question the folder-import path asks first, through the same parser, so a
// file cannot pass here and then be indexed as unreadable by the scan that
// follows.
type Importer struct {
	staging     *Staging
	libraryPath string
	store       ImportStore
	inspect     func(string) library.AudioMetadata
	// layout is the folder shape the operator chose, read per import so that
	// changing it takes effect without a restart. Without one, an upload is
	// filed where an untouched installation has always filed it.
	layout func(context.Context) (library.Template, error)
	logger zerolog.Logger
}

func NewImporter(
	staging *Staging, libraryPath string, store ImportStore, logger zerolog.Logger,
) *Importer {
	return &Importer{
		staging: staging, libraryPath: filepath.Clean(libraryPath), store: store,
		inspect: library.InspectAudio, logger: logger,
	}
}

// WithLayout registers where the folder shape is read from. It is the same
// setting the download import renders against and the same one the layout
// migration moves a library to, so all three are one answer rather than three:
// an upload arriving after a migration lands where the migration would have put
// it instead of rebuilding the layout it replaced.
func (importer *Importer) WithLayout(
	layout func(context.Context) (library.Template, error),
) *Importer {
	importer.layout = layout
	return importer
}

// validated is one staged file that may be copied, together with what its tags
// said. The tags name the folder it is filed under and nothing else: a path is
// where bytes live, never evidence, and what the file is gets decided later.
type validated struct {
	file   StagedFile
	source string
	tags   library.AudioMetadata
}

// Import validates a staged upload and copies it into the managed library. It
// returns where each file landed, in the order the upload's own files are in,
// which is how a caller with a SoundCloud address to write finds the one file
// it names without asking the layout template a second time.
//
// It is all or nothing, which is the posture the folder import already takes: a
// release half in the library and half in staging is worse than one still
// waiting, because the half that landed is now indistinguishable from music
// somebody put there on purpose. Every reason a file was refused is collected
// first, so one broken track reports itself as one broken track rather than as
// the point validation stopped at.
//
// Repeating it is safe. The copy is renamed into place, so a destination that
// already exists is an import that already happened, and re-running finishes
// the steps that come after it.
func (importer *Importer) Import(ctx context.Context, uploadID uuid.UUID) ([]string, error) {
	upload, err := importer.staging.Open(uploadID)
	if err != nil {
		return nil, err
	}
	if len(upload.Files) == 0 {
		return nil, fmt.Errorf("upload %s: %w", uploadID, ErrNoFiles)
	}

	files := make([]validated, 0, len(upload.Files))
	problems := make([]string, 0)
	for _, staged := range upload.Files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		source := filepath.Join(importer.staging.Path(uploadID), staged.Name)
		if !library.CanHold(staged.Name) {
			problems = append(problems,
				fmt.Sprintf("%q is not a file type the library can hold", staged.Name))
			continue
		}
		if staged.SizeBytes == 0 {
			problems = append(problems, fmt.Sprintf("%q is empty", staged.Name))
			continue
		}
		metadata := importer.inspect(source)
		// A file carrying no tags at all is silence rather than damage — a WAV
		// off a disc says nothing about itself and is still the recording it is,
		// and the audio is what identifies it anyway.
		if metadata.Error != "" && !metadata.TagsAbsent {
			problems = append(problems,
				fmt.Sprintf("%q cannot be read as audio: %s", staged.Name, metadata.Error))
			continue
		}
		files = append(files, validated{file: staged, source: source, tags: metadata})
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("%s: %w", strings.Join(problems, "; "), ErrUnreadable)
	}

	destination := importer.destination(ctx, uploadID, files)
	if err := importer.copy(ctx, destination, files); err != nil {
		return nil, err
	}
	if err := importer.store.CompleteUploadImport(
		ctx, uploadID, importer.libraryPath, destination,
	); err != nil {
		return nil, err
	}
	importer.logger.Info().Str("upload_id", uploadID.String()).
		Str("path", destination).Int("files", len(files)).Msg("upload imported")

	// Last, and never fatal: the managed copy is committed, so an upload left in
	// staging costs disc space and nothing else. Failing the job here would put
	// music that is already in the library back in the queue.
	if err := importer.staging.Discard(uploadID); err != nil {
		importer.logger.Error().Err(err).Str("upload_id", uploadID.String()).
			Msg("clear the staging folder of an imported upload")
	}
	landed := make([]string, len(files))
	for index, file := range files {
		landed[index] = filepath.Join(destination, file.file.Name)
	}
	return landed, nil
}

// destination is where an upload is filed: the layout the operator chose,
// rendered against what the upload's own files agree they are.
//
// Where they agree on an artist and an album, those name the folder, because a
// person browsing the volume should find their purchase where they would look
// for it. Where they do not, that field is silent and the template says what a
// folder with nothing to call it is called — the same answer a download with an
// untagged artist gets, rather than a second convention only uploads use. The
// upload's own identifier is what keeps two of them apart, which is all the
// folder name has ever needed it for.
//
// A template that cannot be read files the upload the way an untouched
// installation always has rather than refusing it: the bytes are staged and
// validated, and a settings row nobody can read is not a reason to lose them.
//
// None of this decides anything: the layout is where bytes live, and no part of
// matching or identity reads a path.
func (importer *Importer) destination(
	ctx context.Context, uploadID uuid.UUID, files []validated,
) string {
	artist, album := agreed(files)
	fields := library.Fields{Artist: artist, Album: album, ID: uploadID.String()[:8]}
	template, err := library.ParseTemplate(library.DefaultTemplate)
	if importer.layout != nil {
		chosen, layoutErr := importer.layout(ctx)
		if layoutErr != nil {
			importer.logger.Warn().Err(layoutErr).Str("upload_id", uploadID.String()).
				Msg("read the library layout; filing this upload the default way")
		} else {
			template, err = chosen, nil
		}
	}
	if err != nil {
		// Only reachable if the default template itself stopped parsing, which is
		// a constant with tests of its own. The download path answers this by
		// building the default shape by hand; here that would be a second renderer
		// of the layout, which is the thing this change exists to remove. A folder
		// named after the upload needs no renderer, is inside the library, and
		// belongs to nothing else.
		return filepath.Join(importer.libraryPath, "Uploads", uploadID.String()[:8])
	}
	return filepath.Join(importer.libraryPath, template.Render(fields))
}

// agreed is the artist and album every file names, or empty where any of them
// is silent or says something else. One disagreement is enough: a folder named
// after the majority would be filing music under a title no one file claims.
func agreed(files []validated) (string, string) {
	artist := strings.TrimSpace(files[0].tags.Artist)
	album := strings.TrimSpace(files[0].tags.Album)
	for _, file := range files[1:] {
		if strings.TrimSpace(file.tags.Artist) != artist {
			artist = ""
		}
		if strings.TrimSpace(file.tags.Album) != album {
			album = ""
		}
	}
	return artist, album
}

// copy writes the whole upload beside the destination and renames it into
// place, so that no scan ever sees a folder that is still being filled.
//
// It never re-encodes anything. An upload has no verdict of its own — nothing
// about it is checked against a catalogue edition — so the file the resolver
// and the acoustic check read after the scan is the file that arrived; every
// identification Schall can make about a file it did not verify at import
// time depends on that. The transcoding setting still reaches this music, but
// only once it has a resolved identity: the library sweep
// (internal/library/transcode.go) re-encodes proven files already on disc,
// uploads included, in the same order every other file already in the
// library is re-encoded in.
func (importer *Importer) copy(ctx context.Context, destination string, files []validated) error {
	placed := importer.plan(files)

	if info, err := os.Stat(destination); err == nil && info.IsDir() {
		return importer.fileInto(ctx, destination, placed)
	} else if err == nil {
		return fmt.Errorf("import destination %q is not a directory", destination)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	parent := filepath.Dir(destination)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	// The name must be unique so two Schall processes cannot fill one staging folder.
	staging, err := os.MkdirTemp(parent, ".schall-upload-")
	if err != nil {
		return err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(staging)
		}
	}()
	for _, placement := range placed {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := filecopy.Copy(placement.Source, filepath.Join(staging, placement.Name)); err != nil {
			return err
		}
	}
	if err := os.Rename(staging, destination); err != nil {
		return err
	}
	cleanup = false
	return nil
}

// plan says what each staged file is written as: itself, under the name it
// arrived with. transcode.Placement is reused as the shape rather than
// invented again here, because copy and fileInto already speak it and an
// untouched file is the same shape a re-encode would have been, minus the
// re-encode.
func (importer *Importer) plan(files []validated) []transcode.Placement {
	placed := make([]transcode.Placement, len(files))
	for index, file := range files {
		placed[index] = transcode.Placement{
			Source: file.source, Name: file.file.Name, SizeBytes: file.file.SizeBytes,
		}
	}
	return placed
}

// fileInto writes an upload into a folder that is already there.
//
// The folder used to be the upload's alone — its identifier was in the name —
// so finding it meant the rename had already happened and there was nothing
// left to copy. A template is free to name a folder that says nothing about
// which upload it holds, and then a second upload of the same album finds the
// first one's folder; returning here would record an import that copied
// nothing.
//
// So each file is asked about by itself, and by its bytes rather than its
// size. One already there that is this file is this upload arriving twice, and
// the copy that landed first is the copy. One there holding anything else is
// another file wearing the name — two uploads of one album name their tracks
// the same way, and two encodes of one track can be the same length — and which
// one the library keeps is a question for a person rather than something a copy
// settles by overwriting or by skipping.
func (importer *Importer) fileInto(
	ctx context.Context, destination string, placed []transcode.Placement,
) error {
	toPlace := make([]transcode.Placement, 0, len(placed))
	for _, placement := range placed {
		if err := ctx.Err(); err != nil {
			return err
		}
		localPath := filepath.Join(destination, placement.Name)
		info, err := os.Lstat(localPath)
		switch {
		case errors.Is(err, os.ErrNotExist):
			toPlace = append(toPlace, placement)
			continue
		case err != nil:
			return err
		case !info.Mode().IsRegular() || info.Size() != placement.SizeBytes:
			return fmt.Errorf("%q is already in the library as a different file", localPath)
		}
		same, err := filecopy.SameBytes(placement.Source, localPath)
		if err != nil {
			return err
		}
		if !same {
			return fmt.Errorf("%q is already in the library as a different file", localPath)
		}
	}

	placedPaths := make([]string, 0, len(toPlace))
	rollback := func() {
		for _, path := range placedPaths {
			_ = os.Remove(path)
		}
	}
	for _, placement := range toPlace {
		if err := ctx.Err(); err != nil {
			rollback()
			return err
		}
		localPath := filepath.Join(destination, placement.Name)
		if err := filecopy.Place(placement.Source, localPath, ".schall-upload-"); err != nil {
			rollback()
			return err
		}
		placedPaths = append(placedPaths, localPath)
	}
	return nil
}
