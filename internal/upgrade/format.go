package upgrade

import (
	"path/filepath"
	"strings"
)

// format is a file's bare lower-case extension, the same word the scanner
// stores nothing else about and the format floor is keyed by. library_files
// carries no format column of its own — the extension on the path is what
// every part of Schall that asks "what format is this" already reads.
func format(path string) string {
	return strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")
}
