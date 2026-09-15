package sources

import (
	"errors"
	"testing"
)

// ErrNotLoggedIn is backpressure, the same as the other refusals a caller only
// checking for ErrProviderUnavailable already knows how to wait out.
func TestErrNotLoggedInIsProviderUnavailable(t *testing.T) {
	if !errors.Is(ErrNotLoggedIn, ErrProviderUnavailable) {
		t.Error("ErrNotLoggedIn does not satisfy errors.Is(err, ErrProviderUnavailable)")
	}
}

// ErrSignedOut is the same fact as ErrNotLoggedIn — the account is signed out
// — learned earlier, before a search was ever made. It has to stay backpressure
// like every other refusal, and it has to stay a distinct sentinel: acquisition
// gives ErrNotLoggedIn the account-wide bannedRest wait, and folding the two
// together would hand the earlier check that same wait instead of the shorter
// one it has always gotten (#495).
func TestErrSignedOutIsProviderUnavailableButNotErrNotLoggedIn(t *testing.T) {
	if !errors.Is(ErrSignedOut, ErrProviderUnavailable) {
		t.Error("ErrSignedOut does not satisfy errors.Is(err, ErrProviderUnavailable)")
	}
	if errors.Is(ErrSignedOut, ErrNotLoggedIn) {
		t.Error("ErrSignedOut satisfies errors.Is(err, ErrNotLoggedIn), want the two kept apart")
	}
}

// A transfer has settled when it has stopped moving, either way. Reading a
// transfer that is still going as settled would have Schall look for bytes that
// are not all there yet; reading a finished one as unsettled would wait forever.
func TestATransferIsSettledOnceItHasStoppedMoving(t *testing.T) {
	for _, state := range []string{TransferCompleted, TransferFailed, TransferCancelled} {
		t.Run(state, func(t *testing.T) {
			if !(Transfer{State: state}).Settled() {
				t.Fatalf("a %s transfer is not settled", state)
			}
		})
	}
}

func TestATransferStillMovingIsNotSettled(t *testing.T) {
	for _, state := range []string{TransferQueued, TransferDownloading, "", "something new"} {
		t.Run(state, func(t *testing.T) {
			if (Transfer{State: state}).Settled() {
				t.Fatalf("a %q transfer was read as settled", state)
			}
		})
	}
}
