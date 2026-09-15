package navidrome

import "testing"

func TestAnUnsetMapLeavesEveryPathAsItIs(t *testing.T) {
	pathMap, err := ParsePathMap("")
	if err != nil {
		t.Fatalf("ParsePathMap() error = %v", err)
	}
	if got := pathMap.ToRemote("/music/artist/track.flac"); got != "/music/artist/track.flac" {
		t.Errorf("ToRemote() = %q, want the path untouched", got)
	}
	if got := pathMap.ToLocal("/music/artist/track.flac"); got != "/music/artist/track.flac" {
		t.Errorf("ToLocal() = %q, want the path untouched", got)
	}
}

func TestAMappedPathIsTranslatedBothWays(t *testing.T) {
	pathMap, err := ParsePathMap("/music=/music-schall")
	if err != nil {
		t.Fatalf("ParsePathMap() error = %v", err)
	}
	if got := pathMap.ToRemote("/music/artist/track.flac"); got != "/music-schall/artist/track.flac" {
		t.Errorf("ToRemote() = %q", got)
	}
	if got := pathMap.ToLocal("/music-schall/artist/track.flac"); got != "/music/artist/track.flac" {
		t.Errorf("ToLocal() = %q", got)
	}
}

// The prefix has to end where a folder ends. /music-extra is a different
// folder that happens to start with the same letters.
func TestAPrefixOnlyMatchesOnAFolderBoundary(t *testing.T) {
	pathMap, err := ParsePathMap("/music=/music-schall")
	if err != nil {
		t.Fatalf("ParsePathMap() error = %v", err)
	}
	if got := pathMap.ToRemote("/music-extra/track.flac"); got != "/music-extra/track.flac" {
		t.Errorf("ToRemote() = %q, want a neighbouring folder left alone", got)
	}
}

func TestTheLongestMatchingMountWins(t *testing.T) {
	pathMap, err := ParsePathMap("/music=/nd-music, /music/live=/nd-live")
	if err != nil {
		t.Fatalf("ParsePathMap() error = %v", err)
	}
	if got := pathMap.ToRemote("/music/live/set.flac"); got != "/nd-live/set.flac" {
		t.Errorf("ToRemote() = %q, want the more specific mount", got)
	}
	if got := pathMap.ToRemote("/music/other/track.flac"); got != "/nd-music/other/track.flac" {
		t.Errorf("ToRemote() = %q, want the general mount", got)
	}
}

func TestAPathUnderNoConfiguredMountPassesThrough(t *testing.T) {
	pathMap, err := ParsePathMap("/music=/music-schall")
	if err != nil {
		t.Fatalf("ParsePathMap() error = %v", err)
	}
	if got := pathMap.ToRemote("/elsewhere/track.flac"); got != "/elsewhere/track.flac" {
		t.Errorf("ToRemote() = %q, want the path untouched", got)
	}
}

func TestATrailingSeparatorIsTheSameMount(t *testing.T) {
	pathMap, err := ParsePathMap("/music/=/music-schall/")
	if err != nil {
		t.Fatalf("ParsePathMap() error = %v", err)
	}
	if got := pathMap.ToRemote("/music/track.flac"); got != "/music-schall/track.flac" {
		t.Errorf("ToRemote() = %q", got)
	}
}

func TestAHalfWrittenMappingIsRefused(t *testing.T) {
	for _, specification := range []string{"/music", "/music=", "=/music-schall"} {
		if _, err := ParsePathMap(specification); err == nil {
			t.Errorf("ParsePathMap(%q) was accepted", specification)
		}
	}
}

func TestARelativeMappingIsRefused(t *testing.T) {
	if _, err := ParsePathMap("music=/music-schall"); err == nil {
		t.Error("a relative local path was accepted")
	}
	if _, err := ParsePathMap("/music=music-schall"); err == nil {
		t.Error("a relative remote path was accepted")
	}
}

// One folder mapped twice has no answer, and picking one would be a guess.
func TestAFolderMappedTwiceIsRefused(t *testing.T) {
	if _, err := ParsePathMap("/music=/a,/music=/b"); err == nil {
		t.Error("the same local folder was accepted twice")
	}
	if _, err := ParsePathMap("/a=/music,/b=/music"); err == nil {
		t.Error("two local folders were accepted onto one remote folder")
	}
}
