package library

import "testing"

func TestBetterPrefersLosslessOverLossy(t *testing.T) {
	old := upgradeCandidate{path: "/music/track.mp3", bitRateKbps: 320}
	fresh := upgradeCandidate{path: "/music/track.flac", bitRateKbps: 0}
	if !better(old, fresh, 0) {
		t.Fatal("a lossless copy of the same recording was not judged better than a lossy one")
	}
}

func TestBetterRefusesLossyOverLossless(t *testing.T) {
	old := upgradeCandidate{path: "/music/track.flac", bitRateKbps: 0}
	fresh := upgradeCandidate{path: "/music/track.mp3", bitRateKbps: 320}
	if better(old, fresh, 0) {
		t.Fatal("a lossy copy was judged better than a lossless file already on disc")
	}
}

func TestBetterAcceptsAHigherBitRate(t *testing.T) {
	old := upgradeCandidate{path: "/music/track.mp3", bitRateKbps: 128}
	fresh := upgradeCandidate{path: "/music/track.mp3", bitRateKbps: 320}
	if !better(old, fresh, 0) {
		t.Fatal("a higher bitrate copy of the same format was not judged better")
	}
}

func TestBetterRefusesALowerBitRate(t *testing.T) {
	old := upgradeCandidate{path: "/music/track.mp3", bitRateKbps: 320}
	fresh := upgradeCandidate{path: "/music/track.mp3", bitRateKbps: 128}
	if better(old, fresh, 0) {
		t.Fatal("a lower bitrate copy was judged an upgrade")
	}
}

func TestBetterRefusesTheSameFormatAndBitRate(t *testing.T) {
	old := upgradeCandidate{path: "/music/track.mp3", bitRateKbps: 192}
	fresh := upgradeCandidate{path: "/other/track.mp3", bitRateKbps: 192}
	if better(old, fresh, 0) {
		t.Fatal("an identical format and bitrate was judged an upgrade")
	}
}

// A damaged 320 kbps MP3 has to be replaceable by a clean 320 kbps MP3.
// Before the decode check the same format and bit rate was never better, so
// the clean copy was discarded and the damaged file kept.
func TestBetterAcceptsACleanCopyOfTheSameQualityOverADamagedOne(t *testing.T) {
	old := upgradeCandidate{path: "/music/track.mp3", bitRateKbps: 320, damaged: true}
	fresh := upgradeCandidate{path: "/other/track.mp3", bitRateKbps: 320}
	if !better(old, fresh, 0) {
		t.Fatal("a clean copy of the same quality was not judged better than a damaged one")
	}
}

func TestBetterKeepsADamagedFileOverALowerQualityCopy(t *testing.T) {
	for name, test := range map[string]struct{ old, fresh upgradeCandidate }{
		"lossy over lossless": {
			upgradeCandidate{path: "/music/track.flac", damaged: true},
			upgradeCandidate{path: "/other/track.mp3", bitRateKbps: 320},
		},
		"lower bit rate": {
			upgradeCandidate{path: "/music/track.mp3", bitRateKbps: 320, damaged: true},
			upgradeCandidate{path: "/other/track.mp3", bitRateKbps: 192},
		},
		"damaged too": {
			upgradeCandidate{path: "/music/track.mp3", bitRateKbps: 128, damaged: true},
			upgradeCandidate{path: "/other/track.flac", damaged: true},
		},
	} {
		if better(test.old, test.fresh, 0) {
			t.Errorf("%s: judged an upgrade over a damaged file", name)
		}
	}
}

// With re-encoding on, every lossless copy fetched for a damaged FLAC arrives in
// the library as an MP3. Refusing it discarded each one, and the want fetched
// the next for ever.
func TestBetterAcceptsACopyWrittenSmallerFromLosslessOverADamagedLosslessFile(t *testing.T) {
	old := upgradeCandidate{path: "/music/track.flac", damaged: true}
	fresh := upgradeCandidate{path: "/music/track.mp3", bitRateKbps: 320, fromLossless: true}
	if !better(old, fresh, 0) {
		t.Fatal("a clean copy written from a lossless file was not judged better than a damaged lossless one")
	}
}

// With re-encoding on, a clean lossy copy at the re-encode rate replaces a
// damaged lossless file; one below it does not.
func TestBetterAcceptsALossyCopyAtTheReencodeRateOverADamagedLosslessFile(t *testing.T) {
	old := upgradeCandidate{path: "/music/track.flac", damaged: true}
	if !better(old, upgradeCandidate{path: "/music/track.mp3", bitRateKbps: 320}, 320) {
		t.Fatal("a 320 kbps copy was not judged better than a damaged FLAC with re-encoding to 320 on")
	}
	if better(old, upgradeCandidate{path: "/music/track.mp3", bitRateKbps: 256}, 320) {
		t.Fatal("a 256 kbps copy was judged better than a damaged FLAC with re-encoding to 320 on")
	}
}
