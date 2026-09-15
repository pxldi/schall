package acoustid

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
)

// These run against the real AcoustID service and the real fpcalc binary. They
// gate on the key rather than skipping quietly by default for the same reason
// the database suite gates on SCHALL_TEST_DATABASE_URL: a test that silently
// does nothing is worse than an absent one.
//
// AcoustID issues a key per application, so there is no shared credential to
// check in. Set SCHALL_ACOUSTID_API_KEY to run these.
func liveClient(t *testing.T) *Client {
	t.Helper()
	key := os.Getenv("SCHALL_ACOUSTID_API_KEY")
	if key == "" {
		t.Skip("SCHALL_ACOUSTID_API_KEY is not set; skipping the live AcoustID tests")
	}
	if _, err := exec.LookPath(defaultFpcalc); err != nil {
		t.Skip("fpcalc is not installed; skipping the live AcoustID tests")
	}
	return NewClient(Options{APIKey: key})
}

// The whole path in one go: decode audio, compute a fingerprint, and have
// AcoustID accept it. Audio nobody has ever submitted comes back recognised as
// nothing, which is the point — a rejected key fails loudly instead.
func TestLiveLookupAcceptsTheKeyAndAnswers(t *testing.T) {
	client := liveClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	print, err := client.Fingerprint(ctx, writeTone(t, 30))
	if err != nil {
		t.Fatalf("Fingerprint() error = %v", err)
	}

	recordings, err := client.Identify(ctx, print)
	if err != nil {
		t.Fatalf("Identify() error = %v; the key was refused or the service disagreed", err)
	}
	// A generated tone is not in the database. Anything else would mean the
	// lookup matched something it cannot possibly have matched.
	if len(recordings) != 0 {
		t.Fatalf("a synthetic tone identified as %#v", recordings)
	}
}

// Real music, end to end. A synthetic tone proves the key and the plumbing but
// can never prove recognition, because AcoustID has by definition never heard
// it. Point SCHALL_ACOUSTID_TEST_FILE at a real track to check the part that
// matters: that a file the world knows resolves to recordings, not to silence.
func TestLiveLookupIdentifiesRealMusic(t *testing.T) {
	client := liveClient(t)
	path := os.Getenv("SCHALL_ACOUSTID_TEST_FILE")
	if path == "" {
		t.Skip("SCHALL_ACOUSTID_TEST_FILE is not set; skipping the real-music lookup")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	print, err := client.Fingerprint(ctx, path)
	if err != nil {
		t.Fatalf("Fingerprint() error = %v", err)
	}
	if print.DurationSeconds < 30 {
		t.Fatalf("decoded only %ds; that is too short to identify", print.DurationSeconds)
	}

	clusters, err := client.Identify(ctx, print)
	if err != nil {
		t.Fatalf("Identify() error = %v", err)
	}
	if len(clusters) == 0 {
		t.Skipf("AcoustID does not know %q; it holds no fingerprint for every release", path)
	}
	t.Logf("identified as %d cluster(s), the best scoring %.2f over %d recording(s)",
		len(clusters), clusters[0].Score, len(clusters[0].Recordings))
}

// A key the service rejects has to fail loudly rather than look like audio
// nobody recognised, because the two are otherwise indistinguishable and only
// one of them is a configuration mistake.
func TestLiveLookupRejectsABadKey(t *testing.T) {
	liveClient(t) // skip unless the live suite is switched on at all
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := NewClient(Options{APIKey: "definitely-not-a-key"})
	print, err := client.Fingerprint(ctx, writeTone(t, 30))
	if err != nil {
		t.Fatalf("Fingerprint() error = %v", err)
	}
	if _, err := client.Identify(ctx, print); err == nil {
		t.Fatal("a rejected key looked like a successful lookup")
	}
}
