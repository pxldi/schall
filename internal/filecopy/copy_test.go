package filecopy

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, path string, content []byte) string {
	t.Helper()
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write %q: %v", path, err)
	}
	return path
}

func read(t *testing.T, path string) []byte {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %q: %v", path, err)
	}
	return content
}

// The reason the bytes are read at all. Two encodes of one track can be the
// same length, so a size that agrees says nothing about whether the file
// already in the library is this one.
func TestTwoFilesOfOneLengthHoldingDifferentAudioAreNotTheSameFile(t *testing.T) {
	dir := t.TempDir()
	left := write(t, filepath.Join(dir, "left.flac"), []byte("the first encode!!"))
	right := write(t, filepath.Join(dir, "right.flac"), []byte("a different encode"))

	if len(read(t, left)) != len(read(t, right)) {
		t.Fatalf("the fixtures must be the same length to test anything")
	}

	same, err := SameBytes(left, right)
	if err != nil {
		t.Fatalf("compare: %v", err)
	}
	if same {
		t.Fatal("two files of one length holding different audio were called the same file")
	}
}

func TestTwoCopiesOfOneFileAreTheSameFile(t *testing.T) {
	dir := t.TempDir()
	left := write(t, filepath.Join(dir, "left.flac"), []byte("the same audio twice"))
	right := write(t, filepath.Join(dir, "right.flac"), []byte("the same audio twice"))

	same, err := SameBytes(left, right)
	if err != nil {
		t.Fatalf("compare: %v", err)
	}
	if !same {
		t.Fatal("two copies of one file were called different files")
	}
}

// A track is bigger than the buffer, so a difference the first read cannot see
// is the ordinary case rather than an edge one.
func TestFilesThatDifferOnlyPastTheFirstReadAreNotTheSameFile(t *testing.T) {
	dir := t.TempDir()
	shared := bytes.Repeat([]byte("a"), 200*1024)
	left := write(t, filepath.Join(dir, "left.flac"), append(append([]byte{}, shared...), 'x'))
	right := write(t, filepath.Join(dir, "right.flac"), append(append([]byte{}, shared...), 'y'))

	same, err := SameBytes(left, right)
	if err != nil {
		t.Fatalf("compare: %v", err)
	}
	if same {
		t.Fatal("files differing only past the first read were called the same file")
	}
}

func TestAFileThatIsTheStartOfAnotherIsNotTheSameFile(t *testing.T) {
	dir := t.TempDir()
	short := write(t, filepath.Join(dir, "short.flac"), []byte("the whole track"))
	long := write(t, filepath.Join(dir, "long.flac"), []byte("the whole track and more of it"))

	same, err := SameBytes(short, long)
	if err != nil {
		t.Fatalf("compare: %v", err)
	}
	if same {
		t.Fatal("a truncated file was called the same file as the whole one")
	}
}

func TestTwoEmptyFilesAreTheSameFile(t *testing.T) {
	dir := t.TempDir()
	left := write(t, filepath.Join(dir, "left.flac"), nil)
	right := write(t, filepath.Join(dir, "right.flac"), nil)

	same, err := SameBytes(left, right)
	if err != nil {
		t.Fatalf("compare: %v", err)
	}
	if !same {
		t.Fatal("two empty files were called different files")
	}
}

// A file that could not be read is a failure to report, not evidence that the
// two are different: answering false would let the caller refuse an import for
// a reason it never established.
func TestAFileThatCannotBeReadIsReportedRatherThanCalledDifferent(t *testing.T) {
	dir := t.TempDir()
	present := write(t, filepath.Join(dir, "present.flac"), []byte("audio"))

	if _, err := SameBytes(present, filepath.Join(dir, "absent.flac")); err == nil {
		t.Fatal("a missing file was compared without an error")
	}
	if _, err := SameBytes(filepath.Join(dir, "absent.flac"), present); err == nil {
		t.Fatal("a missing file was compared without an error")
	}
}

func TestAPlacedFileHoldsWhatWasCopiedIntoIt(t *testing.T) {
	dir := t.TempDir()
	source := write(t, filepath.Join(dir, "source.flac"), []byte("the audio"))
	localPath := filepath.Join(dir, "01 Track.flac")

	if err := Place(source, localPath, ".schall-import-"); err != nil {
		t.Fatalf("place: %v", err)
	}
	if got := read(t, localPath); !bytes.Equal(got, []byte("the audio")) {
		t.Fatalf("placed file holds %q", got)
	}
	if leftovers := partials(t, dir); len(leftovers) != 0 {
		t.Fatalf("place left %v behind", leftovers)
	}
}

// The name is only ever renamed on once the copy finished, so a copy that
// failed leaves nothing a scan could index — neither under the name it was
// placing at nor under the partial name it wrote through.
func TestACopyThatFailedPlacesNoFileAtAll(t *testing.T) {
	dir := t.TempDir()
	unreadable := filepath.Join(dir, "source.flac")
	if err := os.Mkdir(unreadable, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	localPath := filepath.Join(dir, "01 Track.flac")

	if err := Place(unreadable, localPath, ".schall-import-"); err == nil {
		t.Fatal("a copy that could not be read was placed anyway")
	}
	if _, err := os.Lstat(localPath); !os.IsNotExist(err) {
		t.Fatalf("a failed copy left a file at %q (%v)", localPath, err)
	}
	if leftovers := partials(t, dir); len(leftovers) != 0 {
		t.Fatalf("a failed copy left %v behind", leftovers)
	}
}

// The copy can finish and the rename still fail. What was written is a whole
// file by then, but it is a whole file under a name nothing asked for, so it
// goes the same way as a half-written one.
func TestACopyThatCouldNotBeRenamedOnLeavesNoPartialBehind(t *testing.T) {
	dir := t.TempDir()
	source := write(t, filepath.Join(dir, "source.flac"), []byte("the audio"))
	localPath := filepath.Join(dir, "01 Track.flac")
	if err := os.Mkdir(localPath, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if err := Place(source, localPath, ".schall-import-"); err == nil {
		t.Fatal("a copy that could not be renamed on was reported as placed")
	}
	if leftovers := partials(t, dir); len(leftovers) != 0 {
		t.Fatalf("a failed place left %v behind", leftovers)
	}
}

// Whatever an interrupted attempt wrote is discarded rather than renamed on:
// half a track under the name a scan indexes would be indexed as the track.
func TestAPartialFromAnEarlierAttemptIsNeverPlacedAsMusic(t *testing.T) {
	dir := t.TempDir()
	source := write(t, filepath.Join(dir, "source.flac"), []byte("the whole audio"))
	localPath := filepath.Join(dir, "01 Track.flac")
	staging := write(t, filepath.Join(dir, ".schall-import-01 Track.flac.part"), []byte("half"))

	if err := Place(source, localPath, ".schall-import-"); err != nil {
		t.Fatalf("place: %v", err)
	}
	if got := read(t, localPath); !bytes.Equal(got, []byte("the whole audio")) {
		t.Fatalf("placed file holds %q", got)
	}
	if _, err := os.Lstat(staging); !os.IsNotExist(err) {
		t.Fatalf("the earlier partial is still at %q (%v)", staging, err)
	}
}

// Two writers filling one folder each name their partials for themselves, so
// one of them clearing its own leftovers never takes the other's work.
func TestPlacingLeavesAnotherWritersPartialAlone(t *testing.T) {
	dir := t.TempDir()
	source := write(t, filepath.Join(dir, "source.flac"), []byte("the audio"))
	localPath := filepath.Join(dir, "01 Track.flac")
	theirs := write(t, filepath.Join(dir, ".schall-upload-01 Track.flac.part"), []byte("theirs"))

	if err := Place(source, localPath, ".schall-import-"); err != nil {
		t.Fatalf("place: %v", err)
	}
	if got := read(t, theirs); !bytes.Equal(got, []byte("theirs")) {
		t.Fatalf("the other writer's partial holds %q", got)
	}
}

// The name in a staging folder is claimed by whoever writes it first. Copying
// over one already there would make one import's file another's.
func TestCopyingOntoANameAlreadyTakenIsRefused(t *testing.T) {
	dir := t.TempDir()
	source := write(t, filepath.Join(dir, "source.flac"), []byte("the new audio"))
	taken := write(t, filepath.Join(dir, "taken.flac"), []byte("the audio already there"))

	if err := Copy(source, taken); err == nil {
		t.Fatal("a name already taken was copied over")
	}
	if got := read(t, taken); !bytes.Equal(got, []byte("the audio already there")) {
		t.Fatalf("the file already there now holds %q", got)
	}
}

func TestASourceThatIsNotThereCreatesNoDestination(t *testing.T) {
	dir := t.TempDir()
	destination := filepath.Join(dir, "destination.flac")

	if err := Copy(filepath.Join(dir, "absent.flac"), destination); err == nil {
		t.Fatal("a missing source was copied without an error")
	}
	if _, err := os.Lstat(destination); !os.IsNotExist(err) {
		t.Fatalf("a missing source created %q (%v)", destination, err)
	}
}

// partials lists what a scan would ignore but a person would call litter.
func partials(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %q: %v", dir, err)
	}
	var found []string
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".part") {
			found = append(found, entry.Name())
		}
	}
	return found
}
