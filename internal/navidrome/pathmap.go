package navidrome

import (
	"fmt"
	"path/filepath"
	"strings"
)

// PathMap translates between the path Schall knows a file by and the path
// Navidrome reports for the same file.
//
// The two are not always the same string. Schall and Navidrome read the same
// directory through their own mounts — the deployment this was written for has
// the library at /music in Schall and /music-schall in Navidrome — so an
// absolute path compared across the boundary would agree with nothing, ever,
// and 0010's real-path check would refuse every pass for a reason that has
// nothing to do with real paths.
//
// It changes nothing about the rule. The comparison is still exact string
// equality (0012); it is made after both sides have been put in the same terms,
// so that they are talking about the same file rather than the same string.
// Nothing here infers, and a path under no configured prefix passes through
// untouched rather than being matched more loosely.
//
// The zero value is the identity mapping, which is right for every installation
// whose two mounts already agree.
type PathMap struct {
	pairs []pathPair
}

// pathPair is one mount seen from both sides. Neither end carries a trailing
// separator, so that the boundary check below is the only thing deciding where
// a prefix ends.
type pathPair struct {
	local  string
	remote string
}

// ParsePathMap reads a comma-separated list of local=remote pairs, such as
// "/music=/music-schall".
//
// An empty specification is the identity mapping. Anything half-written is an
// error rather than a pair that silently translates nothing: a mapping that is
// quietly not applied looks exactly like a Navidrome that reports display
// paths, and both present as a sync that pairs nothing.
func ParsePathMap(specification string) (PathMap, error) {
	var pathMap PathMap
	for _, entry := range strings.Split(specification, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		local, remote, found := strings.Cut(entry, "=")
		local, remote = strings.TrimSpace(local), strings.TrimSpace(remote)
		if !found || local == "" || remote == "" {
			return PathMap{}, fmt.Errorf("path mapping %q must read local=remote", entry)
		}
		if !filepath.IsAbs(local) || !filepath.IsAbs(remote) {
			return PathMap{}, fmt.Errorf("path mapping %q must name two absolute paths", entry)
		}
		local, remote = trimPathEnd(local), trimPathEnd(remote)
		for _, existing := range pathMap.pairs {
			if existing.local == local {
				return PathMap{}, fmt.Errorf("path mapping %q maps %s twice", entry, local)
			}
			if existing.remote == remote {
				return PathMap{}, fmt.Errorf("path mapping %q maps two folders onto %s", entry, remote)
			}
		}
		pathMap.pairs = append(pathMap.pairs, pathPair{local: local, remote: remote})
	}
	return pathMap, nil
}

// ToRemote puts a Schall path in Navidrome's terms, for asking the server about
// a file and for comparing what it answers with.
func (pathMap PathMap) ToRemote(path string) string {
	return pathMap.translate(path, func(pair pathPair) (string, string) {
		return pair.local, pair.remote
	})
}

// ToLocal puts a path Navidrome reported in Schall's terms, for looking the
// file up in the library.
func (pathMap PathMap) ToLocal(path string) string {
	return pathMap.translate(path, func(pair pathPair) (string, string) {
		return pair.remote, pair.local
	})
}

// translate applies the longest configured prefix that matches, and only where
// the prefix ends on a separator: /music must never claim a file under
// /music-extra, which is a different folder that happens to start the same way.
func (pathMap PathMap) translate(path string, sides func(pathPair) (string, string)) string {
	best := ""
	translated := path
	for _, pair := range pathMap.pairs {
		from, to := sides(pair)
		if !underPrefix(path, from) || len(from) <= len(best) {
			continue
		}
		best = from
		if path == from {
			translated = to
			continue
		}
		// The separator is left on the remainder rather than taken from the
		// prefix, so that a mount configured as the root maps like any other.
		translated = to + path[len(strings.TrimSuffix(from, string(filepath.Separator))):]
	}
	return translated
}

// underPrefix reports whether a path is the prefix itself or something inside
// it.
func underPrefix(path, prefix string) bool {
	if path == prefix {
		return true
	}
	separator := string(filepath.Separator)
	return strings.HasPrefix(path, strings.TrimSuffix(prefix, separator)+separator)
}

// trimPathEnd drops a trailing separator so that "/music/" and "/music" are the
// same mount. The root itself keeps its separator, being nothing else.
func trimPathEnd(path string) string {
	separator := string(filepath.Separator)
	if path == separator {
		return path
	}
	return strings.TrimSuffix(path, separator)
}
