// Package filecopy puts one file where another copy of it may already be.
//
// It exists because Schall writes into the library from two places — the files
// a download produced, and the files a person uploaded themselves — and those
// two must not drift apart in how carefully they write. A copy that stops
// half way must not appear as music in either of them, and a name already
// taken must be decided by the bytes under it in either of them. A rule
// loosened for one is loosened for both, on purpose.
//
// Nothing here knows what a file is. Identity is decided elsewhere; this only
// moves bytes and reports whether two files hold the same ones.
package filecopy

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
)

// Place copies one file beside the name it will have and renames it on,
// because a folder a scan may already be walking cannot be filled atomically as
// a whole. The half-written copy is named for a type no scan indexes, so the
// only thing that ever appears as music is the finished file.
//
// stagingPrefix names that half-written copy and belongs to the caller: two
// writers filling one folder must not pick the same partial name for the same
// track.
func Place(source, localPath, stagingPrefix string) error {
	staging := filepath.Join(filepath.Dir(localPath),
		stagingPrefix+filepath.Base(localPath)+".part")
	if err := os.Remove(staging); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := Copy(source, staging); err != nil {
		_ = os.Remove(staging)
		return err
	}
	if err := os.Rename(staging, localPath); err != nil {
		_ = os.Remove(staging)
		return err
	}
	return nil
}

// SameBytes reports whether two files hold the same audio. It is asked only
// when a name in the library is already taken, so the reading it costs is paid
// on a collision rather than on every import.
func SameBytes(left, right string) (bool, error) {
	first, err := os.Open(left)
	if err != nil {
		return false, err
	}
	defer first.Close()
	second, err := os.Open(right)
	if err != nil {
		return false, err
	}
	defer second.Close()

	firstChunk, secondChunk := make([]byte, 64*1024), make([]byte, 64*1024)
	for {
		read, firstErr := io.ReadFull(first, firstChunk)
		matched, secondErr := io.ReadFull(second, secondChunk)
		// Asked before the bytes are compared: a read that stopped short because
		// the disc gave up is a failure to report, and comparing what did arrive
		// would report it as two different files instead.
		for _, err := range []error{firstErr, secondErr} {
			if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
				return false, err
			}
		}
		if read != matched || !bytes.Equal(firstChunk[:read], secondChunk[:matched]) {
			return false, nil
		}
		// Equal reads and one of them short means both files ended here.
		if firstErr != nil || secondErr != nil {
			return true, nil
		}
	}
}

// Copy writes source to destination, which must not exist yet.
func Copy(source, destination string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err = io.Copy(output, input); err == nil {
		err = output.Sync()
	}
	closeErr := output.Close()
	if err != nil {
		return err
	}
	return closeErr
}
