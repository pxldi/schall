package library

import "testing"

func TestBetterPrefersLosslessOverLossy(t *testing.T) {
	old := upgradeCandidate{path: "/music/track.mp3", bitRateKbps: 320}
	fresh := upgradeCandidate{path: "/music/track.flac", bitRateKbps: 0}
	if !better(old, fresh) {
		t.Fatal("a lossless copy of the same recording was not judged better than a lossy one")
	}
}

func TestBetterRefusesLossyOverLossless(t *testing.T) {
	old := upgradeCandidate{path: "/music/track.flac", bitRateKbps: 0}
	fresh := upgradeCandidate{path: "/music/track.mp3", bitRateKbps: 320}
	if better(old, fresh) {
		t.Fatal("a lossy copy was judged better than a lossless file already on disc")
	}
}

func TestBetterAcceptsAHigherBitRate(t *testing.T) {
	old := upgradeCandidate{path: "/music/track.mp3", bitRateKbps: 128}
	fresh := upgradeCandidate{path: "/music/track.mp3", bitRateKbps: 320}
	if !better(old, fresh) {
		t.Fatal("a higher bitrate copy of the same format was not judged better")
	}
}

func TestBetterRefusesALowerBitRate(t *testing.T) {
	old := upgradeCandidate{path: "/music/track.mp3", bitRateKbps: 320}
	fresh := upgradeCandidate{path: "/music/track.mp3", bitRateKbps: 128}
	if better(old, fresh) {
		t.Fatal("a lower bitrate copy was judged an upgrade")
	}
}

func TestBetterRefusesTheSameFormatAndBitRate(t *testing.T) {
	old := upgradeCandidate{path: "/music/track.mp3", bitRateKbps: 192}
	fresh := upgradeCandidate{path: "/other/track.mp3", bitRateKbps: 192}
	if better(old, fresh) {
		t.Fatal("an identical format and bitrate was judged an upgrade")
	}
}
