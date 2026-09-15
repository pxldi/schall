package downloads

import (
	"errors"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// errUnsafeFolderName reports a provider folder whose name cannot be turned
// into a local one. It is a durable fact about the request rather than a
// filesystem failure, so validation records it and does not retry it.
var errUnsafeFolderName = errors.New("the provider folder has no safe local name")

// inboxFolder is where slskd put the files of one provider folder. It is the
// last component of the remote directory, under the configured inbox.
//
// Every path in the inbox is derived here and nowhere else. A second derivation
// that disagreed by one component would look in a folder that is not there, and
// read every file it could not find as bytes that never arrived.
func inboxFolder(inboxPath, sourceDirectory string) (string, error) {
	folderName := path.Base(strings.ReplaceAll(strings.TrimSpace(sourceDirectory), `\`, "/"))
	if folderName == "" || folderName == "." || folderName == "/" {
		return "", errUnsafeFolderName
	}
	return containedDirectory(inboxPath, filepath.Join(inboxPath, folderName))
}

// inboxFile is where one downloaded file is, and whether its bytes are still
// there.
func inboxFile(inboxPath, sourceDirectory, fileName string) (string, bool) {
	folder, err := inboxFolder(inboxPath, sourceDirectory)
	if err != nil {
		return "", false
	}
	return regularFile(folder, fileName)
}

// regularFile joins a file name that names nothing but itself to a folder
// already resolved, and reports whether ordinary bytes are there.
func regularFile(folder, fileName string) (string, bool) {
	if fileName == "" || filepath.Base(fileName) != fileName {
		return "", false
	}
	source := filepath.Join(folder, fileName)
	info, err := os.Lstat(source)
	if err != nil || !info.Mode().IsRegular() {
		return "", false
	}
	return source, true
}
