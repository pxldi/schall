package soundcloud

import (
	"testing"

	"go.senan.xyz/taglib"
)

func readTags(t *testing.T, path string) map[string][]string {
	t.Helper()
	tags, err := taglib.ReadTags(path)
	if err != nil {
		t.Fatalf("read the tags on %q: %v", path, err)
	}
	return tags
}
