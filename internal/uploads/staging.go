// Package uploads takes music the user already owns — a ripped disc, a
// Bandcamp purchase — from a browser into the library.
//
// It is the second of the two acquisition routes PRODUCT.md names, and the one
// whose provenance is the user's own word rather than a stranger's. That is the
// whole difference from the acquisition path: nothing here has to prove what a
// file is before it may be kept, because nobody is claiming it is anything.
// What a file turns out to be is decided afterwards, by the same resolution
// every other file on the volume goes through.
//
// Uploading is therefore two questions and no more: is this something the
// library can hold, and did all of it arrive. Both are answered in a staging
// folder that no scan ever walks, because a scan that caught a half-written
// upload would index a truncated file as music the library holds.
package uploads

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/library"
	"github.com/rs/zerolog"
)

// Refusals the upload itself is the answer to. Every one of them is durable:
// a file that is not audio does not become audio on a second attempt, and a
// name with a path in it will not sanitise itself. They are separated from
// filesystem and database failures by Refused, which is what tells the worker
// whether asking again could change anything.
var (
	// ErrNoFiles is a request that carried no file parts at all — a form
	// submitted with nothing chosen.
	ErrNoFiles = errors.New("the upload contains no files")
	// ErrUnsafeName is a filename carrying a path. Nothing is sanitised: a name
	// is either the plain name of a file or it is refused, because guessing at
	// what a caller meant by "../" is how a staging folder stops being one.
	ErrUnsafeName = errors.New("an uploaded file must be named without any path")
	// ErrUnsupported is a file of a type no scan would ever index. Copying it
	// into the library would be filing something the library cannot hold.
	ErrUnsupported = errors.New("the library cannot hold a file of this type")
	// ErrDuplicateName is one upload naming the same file twice, which cannot be
	// staged into one flat folder without one copy silently replacing the other.
	ErrDuplicateName = errors.New("the upload names the same file twice")
	// ErrNoHeadroom is the staging volume having no room for what is offered.
	// Asked before a byte is accepted, because a volume discovering it is full
	// halfway through a 400 MB upload has already wasted the whole transfer.
	ErrNoHeadroom = errors.New("the staging volume has no room for this upload")
	// ErrNotStaged is an upload identifier with no folder behind it.
	ErrNotStaged = errors.New("that upload is not in the staging folder")
	// ErrUnreadable is a staged file whose bytes are not audio. It is the one
	// thing validation refuses on, and it means the upload arrived damaged.
	ErrUnreadable = errors.New("the upload holds a file that cannot be read as audio")
)

// headroomReserve is what the staging volume must have left over after an
// upload, on top of the upload itself. A volume filled to its last byte by an
// upload has nothing left for the copy into the library that follows it, nor
// for anything else sharing the disc.
const headroomReserve = 1 << 30

// StagedFile is one file as it sits in staging: the name the browser gave it,
// and how much of it is on disk.
type StagedFile struct {
	Name      string
	SizeBytes int64
}

// Address is a SoundCloud address and the naming a person confirmed for it,
// sent as plain form fields beside a one-file upload's bytes rather than as
// files of their own.
//
// It rides the request and nothing else: none of it reaches the staging
// folder, so a restart between accepting the upload and queuing its import
// loses it — the file lands unnamed and is named from its own row afterwards,
// exactly as an upload with no address always has.
type Address struct {
	URL     string
	Artist  string
	Title   string
	Remixer string
}

// Sent reports whether an address was sent at all. An empty struct is what
// every upload got before this existed, and behaves exactly the same way.
func (address Address) Sent() bool {
	return strings.TrimSpace(address.URL) != ""
}

// Upload is one staged folder. It is read from the filesystem every time rather
// than recorded anywhere, because a table of in-flight uploads would be a
// second thing that can disagree with the folder it describes — and the folder
// is the one holding the bytes.
type Upload struct {
	ID       uuid.UUID
	Files    []StagedFile
	StagedAt time.Time
}

// TotalBytes is how much of the volume this upload is holding.
func (upload Upload) TotalBytes() int64 {
	var total int64
	for _, file := range upload.Files {
		total += file.SizeBytes
	}
	return total
}

// Names is the staged filenames, in the order they are listed.
func (upload Upload) Names() []string {
	names := make([]string, 0, len(upload.Files))
	for _, file := range upload.Files {
		names = append(names, file.Name)
	}
	return names
}

// Staging is the folder uploads land in. One instance owns one directory and
// everything under it; nothing else writes there.
type Staging struct {
	root   string
	logger zerolog.Logger
	// free reports the bytes available on the volume a path is on. It is a field
	// so a test can decide what the volume has left without needing a volume
	// that has that much left.
	free func(string) (int64, error)
}

// NewStaging prepares the staging folder, creating it when it is not there.
// Unlike the download inbox — somebody else's mount, which Schall only reads —
// this directory is Schall's own scratch space, so a fresh installation is not
// asked to create it by hand.
func NewStaging(root string, logger zerolog.Logger) (*Staging, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, errors.New("an upload staging folder is required")
	}
	if !filepath.IsAbs(root) {
		return nil, errors.New("the upload staging folder must be an absolute path")
	}
	root = filepath.Clean(root)
	// MkdirAll is also the check that the path is a directory: it succeeds on one
	// that already exists and refuses anything else that is in the way.
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("create upload staging folder %s: %w", root, err)
	}
	// Resolved the way a music folder is, so that the two can be compared at all:
	// a staging path reached through a symlink into the library would otherwise
	// pass a containment check it plainly fails.
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("resolve upload staging folder %s: %w", root, err)
	}
	root = resolved
	// Writability is proven by writing rather than by reading permission bits: a
	// read-only bind mount grants the bits and refuses the write anyway, and an
	// installation should learn that at startup rather than from the first
	// upload somebody spends ten minutes on.
	probe, err := os.CreateTemp(root, ".schall-staging-")
	if err != nil {
		return nil, fmt.Errorf("the upload staging folder %s is not writable: %w", root, err)
	}
	name := probe.Name()
	_ = probe.Close()
	if err := os.Remove(name); err != nil {
		return nil, fmt.Errorf("clear the upload staging probe: %w", err)
	}
	return &Staging{root: root, logger: logger, free: volumeFree}, nil
}

// Root is the staging directory itself, which the interface shows so that an
// operator can see where the bytes are waiting.
func (staging *Staging) Root() string { return staging.root }

// EnsureOutsideLibrary refuses a staging folder that any scan could walk into,
// or that could swallow a music folder.
//
// This is checked against the folders that exist rather than assumed from
// configuration, because the cost of being wrong is a truncated file indexed as
// music the library holds — a half-written upload is a real file of the right
// name and the wrong length, and matching would be comparing against bytes that
// were still arriving.
// Every music folder is compared both as it was written and as it resolves,
// because the staging root arrives here already resolved and comparing the two
// forms would answer about a path neither of them is. `/music` symlinked to
// `/data/library` is an ordinary container layout, and a staging folder at
// `/data/library/incoming` is inside it however the configuration spells it.
func EnsureOutsideLibrary(root string, libraryPaths []string) error {
	root = filepath.Clean(root)
	for _, configured := range libraryPaths {
		configured = strings.TrimSpace(configured)
		if configured == "" {
			continue
		}
		for _, candidate := range bothForms(configured) {
			if contains(candidate, root) {
				return fmt.Errorf(
					"the upload staging folder %s is inside the music folder %s", root, configured)
			}
			if contains(root, candidate) {
				return fmt.Errorf(
					"the upload staging folder %s contains the music folder %s", root, configured)
			}
		}
	}
	return nil
}

// bothForms is a path as written and as it resolves. A folder that is not there
// yet cannot be resolved, and is compared as written rather than passed over: an
// allowed root nobody has created is still a place a music folder may appear.
func bothForms(path string) []string {
	cleaned := filepath.Clean(path)
	resolved, err := filepath.EvalSymlinks(cleaned)
	if err != nil || resolved == cleaned {
		return []string{cleaned}
	}
	return []string{cleaned, resolved}
}

// contains reports whether candidate is parent or lies beneath it.
func contains(parent, candidate string) bool {
	relative, err := filepath.Rel(parent, candidate)
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// CheckHeadroom refuses an upload the volume has no room for, before a byte of
// it is accepted. An expectation of zero or less is a request that did not say
// how large it is, and only the reserve is required of it; the copy itself
// still fails honestly if the volume fills up under it.
func (staging *Staging) CheckHeadroom(expected int64) error {
	available, err := staging.free(staging.root)
	if err != nil {
		return err
	}
	if expected < 0 {
		expected = 0
	}
	if available < expected+headroomReserve {
		return fmt.Errorf("%s has %d bytes free and this upload needs %d: %w",
			staging.root, available, expected+headroomReserve, ErrNoHeadroom)
	}
	return nil
}

// Accept streams one multipart request into a folder of its own and returns
// what landed there, together with the SoundCloud address sent beside it.
//
// Every file part is written straight to disk: a release is 300–500 MB and
// holding one in memory to inspect it would be the same mistake as holding it
// in memory to serve it. Nothing partial survives — a refusal, a broken
// connection, or a volume that filled up all remove the folder on the way out,
// because a folder half of an album deep looks exactly like one that finished.
func (staging *Staging) Accept(
	ctx context.Context, parts *multipart.Reader,
) (Upload, Address, error) {
	upload := Upload{ID: uuid.New(), StagedAt: time.Now().UTC()}
	var address Address
	directory := filepath.Join(staging.root, upload.ID.String())
	if err := os.Mkdir(directory, 0o755); err != nil {
		return Upload{}, Address{}, fmt.Errorf("create staging folder for upload %s: %w", upload.ID, err)
	}
	kept := false
	defer func() {
		if kept {
			return
		}
		if err := os.RemoveAll(directory); err != nil {
			staging.logger.Error().Err(err).Str("upload_id", upload.ID.String()).
				Msg("clear the staging folder of an upload that was not accepted")
		}
	}()

	seen := make(map[string]bool)
	for {
		if err := ctx.Err(); err != nil {
			return Upload{}, Address{}, err
		}
		part, err := parts.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Upload{}, Address{}, fmt.Errorf("read upload %s: %w", upload.ID, err)
		}
		field, name, isFile := partIdentity(part)
		if !isFile {
			// The address and the naming confirmed for it ride here, as plain
			// fields beside the files. Anything else sent this way is a browser
			// being entitled to send fields nothing here reads.
			readAddressField(&address, field, part)
			_ = part.Close()
			continue
		}
		file, err := staging.write(ctx, directory, name, seen, part)
		_ = part.Close()
		if err != nil {
			return Upload{}, Address{}, err
		}
		seen[file.Name] = true
		upload.Files = append(upload.Files, file)
	}
	if len(upload.Files) == 0 {
		return Upload{}, Address{}, ErrNoFiles
	}
	sort.Slice(upload.Files, func(left, right int) bool {
		return upload.Files[left].Name < upload.Files[right].Name
	})
	kept = true
	return upload, address, nil
}

// partIdentity is the field name a part was sent under, the filename it
// carries if it is one, and whether it is a file at all.
//
// Part.FileName strips any directory it finds, which is safe and silent —
// and silence is the wrong answer here. A browser asked for a folder sends
// every path relative to it, so two discs' opening tracks would arrive as one
// name, and one of them would be reported as a duplicate of the other rather
// than as the folder upload Schall does not take. The raw parameter is read so
// that the refusal can say what actually happened.
func partIdentity(part *multipart.Part) (field, filename string, isFile bool) {
	_, params, err := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
	if err != nil {
		return "", "", false
	}
	filename, isFile = params["filename"]
	return params["name"], filename, isFile
}

// addressFieldLimit bounds how much of one address field is read: a URL, an
// artist name, a track title, a remixer credit. None of these are bytes worth
// streaming, and a part is a reader, not a value — reading it without a bound
// would let a caller that ignores the contract stage an unbounded read as a
// form field.
const addressFieldLimit = 4 << 10

// readAddressField fills in whichever of the address fields this part is, or
// does nothing where it is a field nothing here reads.
func readAddressField(address *Address, field string, part io.Reader) {
	value, err := io.ReadAll(io.LimitReader(part, addressFieldLimit))
	if err != nil {
		return
	}
	text := strings.TrimSpace(string(value))
	switch field {
	case "sourceUrl":
		address.URL = text
	case "artist":
		address.Artist = text
	case "title":
		address.Title = text
	case "remixer":
		address.Remixer = text
	}
}

// write puts one part on disk under the name it was sent as, having first
// established that the name is one.
func (staging *Staging) write(
	ctx context.Context, directory, name string, seen map[string]bool, part io.Reader,
) (StagedFile, error) {
	if err := ctx.Err(); err != nil {
		return StagedFile{}, err
	}
	if !safeName(name) {
		return StagedFile{}, fmt.Errorf("%q: %w", name, ErrUnsafeName)
	}
	// Asked of the name rather than of the bytes, because this is the same
	// question a scan asks of every file it walks past: a type no scan indexes
	// is not music the library can hold, however good the copy is.
	if !library.CanHold(name) {
		return StagedFile{}, fmt.Errorf("%q: %w", name, ErrUnsupported)
	}
	if seen[name] {
		return StagedFile{}, fmt.Errorf("%q: %w", name, ErrDuplicateName)
	}

	target := filepath.Join(directory, name)
	handle, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return StagedFile{}, fmt.Errorf("stage uploaded file %q: %w", name, err)
	}
	written, err := io.Copy(handle, contextReader{ctx: ctx, reader: part})
	if err == nil {
		err = handle.Sync()
	}
	if closeErr := handle.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(target)
		return StagedFile{}, fmt.Errorf("stage uploaded file %q: %w", name, err)
	}
	return StagedFile{Name: name, SizeBytes: written}, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader contextReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	read, err := reader.reader.Read(buffer)
	if contextErr := reader.ctx.Err(); contextErr != nil {
		return read, contextErr
	}
	return read, err
}

// safeName reports whether a filename is a filename: a plain name, with no
// path in it and nothing a shell or a filesystem would read as an instruction.
// Browsers send a relative path here when a directory was chosen, so this is a
// refusal a legitimate caller can meet — and refusing is still right, because
// the alternative is deciding on the user's behalf which part of their folder
// structure to throw away.
func safeName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	if strings.ContainsAny(name, `/\`) || filepath.Base(name) != name {
		return false
	}
	// A leading dot hides a file from the folder listing an operator would look
	// at, and every hidden file a browser sends is metadata rather than music.
	if strings.HasPrefix(name, ".") {
		return false
	}
	for _, symbol := range name {
		if unicode.IsControl(symbol) {
			return false
		}
	}
	return true
}

// Open reads one staged upload back off the filesystem.
func (staging *Staging) Open(id uuid.UUID) (Upload, error) {
	directory := filepath.Join(staging.root, id.String())
	info, err := os.Stat(directory)
	if errors.Is(err, os.ErrNotExist) {
		return Upload{}, fmt.Errorf("upload %s: %w", id, ErrNotStaged)
	}
	if err != nil {
		return Upload{}, fmt.Errorf("inspect staged upload %s: %w", id, err)
	}
	if !info.IsDir() {
		return Upload{}, fmt.Errorf("upload %s: %w", id, ErrNotStaged)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return Upload{}, fmt.Errorf("read staged upload %s: %w", id, err)
	}
	upload := Upload{ID: id, StagedAt: info.ModTime().UTC()}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		details, err := entry.Info()
		if err != nil {
			return Upload{}, fmt.Errorf("inspect staged file %q: %w", entry.Name(), err)
		}
		upload.Files = append(upload.Files, StagedFile{
			Name: entry.Name(), SizeBytes: details.Size(),
		})
	}
	sort.Slice(upload.Files, func(left, right int) bool {
		return upload.Files[left].Name < upload.Files[right].Name
	})
	return upload, nil
}

// Path is where one staged upload's files are.
func (staging *Staging) Path(id uuid.UUID) string {
	return filepath.Join(staging.root, id.String())
}

// List is every upload currently staged, newest first. Anything in the folder
// that is not a directory named by a UUID was not put there by Schall and is
// passed over rather than reported as an upload.
func (staging *Staging) List() ([]Upload, error) {
	entries, err := os.ReadDir(staging.root)
	if err != nil {
		return nil, fmt.Errorf("read the upload staging folder: %w", err)
	}
	uploads := make([]Upload, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		id, err := uuid.Parse(entry.Name())
		if err != nil {
			continue
		}
		upload, err := staging.Open(id)
		if errors.Is(err, ErrNotStaged) {
			continue
		}
		if err != nil {
			return nil, err
		}
		uploads = append(uploads, upload)
	}
	sort.Slice(uploads, func(left, right int) bool {
		return uploads[left].StagedAt.After(uploads[right].StagedAt)
	})
	return uploads, nil
}

// Discard removes a staged upload and everything in it. An upload that is not
// there is not an error: the caller wanted it gone.
func (staging *Staging) Discard(id uuid.UUID) error {
	if err := os.RemoveAll(staging.Path(id)); err != nil {
		return fmt.Errorf("discard staged upload %s: %w", id, err)
	}
	return nil
}

// Refused reports whether an error is the upload's own answer rather than
// something that could go differently next time. It is what keeps a worker from
// spending three attempts rediscovering that a file is not audio, and from
// giving up on a database that was briefly unreachable.
func Refused(err error) bool {
	for _, refusal := range []error{
		ErrNoFiles, ErrUnsafeName, ErrUnsupported, ErrDuplicateName,
		ErrNoHeadroom, ErrNotStaged, ErrUnreadable,
	} {
		if errors.Is(err, refusal) {
			return true
		}
	}
	return false
}

// volumeFree reports the bytes an unprivileged writer has left on the volume a
// path is on. Bavail rather than Bfree: the blocks reserved for root are not
// room this process has.
func volumeFree(path string) (int64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, fmt.Errorf("read the free space on %s: %w", path, err)
	}
	return int64(stat.Bavail) * int64(stat.Bsize), nil
}
