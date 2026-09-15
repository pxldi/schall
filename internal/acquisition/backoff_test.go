package acquisition

import (
	"testing"
	"time"

	"github.com/pxldi/schall/internal/sources"
	"github.com/rs/zerolog"
)

// The wait starts in minutes, because an empty answer is usually about the
// network at that moment rather than about the music; it climbs, because a
// copy truly absent this morning is often absent tonight; and it stops
// climbing, because a want does not expire: a target asked about daily forever
// costs almost nothing and eventually finds its copy.
func TestTheWaitBetweenAttemptsClimbsAndThenStops(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	service := NewService(nil, zerolog.Nop())
	service.now = func() time.Time { return now }

	waits := map[int32]time.Duration{
		0:  15 * time.Minute,
		1:  time.Hour,
		2:  3 * time.Hour,
		3:  6 * time.Hour,
		4:  12 * time.Hour,
		5:  24 * time.Hour,
		40: 24 * time.Hour,
	}
	for attempts, want := range waits {
		if got := service.nextAttempt(attempts).Sub(now); got != want {
			t.Errorf("after %d attempts the next one waits %s, want %s", attempts, got, want)
		}
	}
}

// The other empty answer waits far longer. A want with a recording and no copy
// is asking who is sharing what, which changes hour by hour. A want MusicBrainz
// holds no recording for is asking whether somebody has written that recording
// in, which happens over weeks and for most entries never. One imported playlist
// of 314 entries left 253 wants here, and on the hourly ladder that was 253
// lookups a day for recordings nobody is going to add.
func TestAWantMusicBrainzHasNothingForWaitsTheSlowerLadder(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	service := NewService(nil, zerolog.Nop())
	service.now = func() time.Time { return now }

	waits := map[int32]time.Duration{
		0:  24 * time.Hour,
		1:  72 * time.Hour,
		2:  7 * 24 * time.Hour,
		3:  30 * 24 * time.Hour,
		40: 30 * 24 * time.Hour,
	}
	for attempts, want := range waits {
		if got := service.nextUnfoundAttempt(attempts).Sub(now); got != want {
			t.Errorf("after %d attempts the next one waits %s, want %s", attempts, got, want)
		}
	}
}

// The slower ladder never asks sooner than the ordinary one. It is the same
// question asked less often, and a rung that dipped below would make a dead end
// the busiest thing in the queue.
func TestTheSlowerLadderNeverAsksSoonerThanTheOrdinaryOne(t *testing.T) {
	for attempts := int32(0); attempts < 40; attempts++ {
		ordinary := rungOf(attemptDelays, attempts)
		unfound := rungOf(unfoundDelays, attempts)
		if unfound < ordinary {
			t.Errorf("after %d attempts a dead end waits %s and a missing copy waits %s",
				attempts, unfound, ordinary)
		}
	}
}

// A want whose attempt count makes no sense waits the first delay rather than
// reading off the end of the ladder. The column cannot hold a negative today, and
// the point is that the schedule never depends on that staying true.
func TestAnImpossibleAttemptCountWaitsTheFirstDelay(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	service := NewService(nil, zerolog.Nop())
	service.now = func() time.Time { return now }

	if got := service.nextAttempt(-3).Sub(now); got != attemptDelays[0] {
		t.Fatalf("the next attempt waits %s, want the first delay %s", got, attemptDelays[0])
	}
}

// A pass can hear two different refusals, and they do not ask for the same
// wait. Being rate limited is five minutes; being signed out of Soulseek gets
// the same half hour a ban gets, because it is just as account-wide. A pass
// that kept whichever it heard first would come back in five minutes and ask
// straight into a source that still cannot search.
func TestAPassKeepsTheLongestStandBackItHeard(t *testing.T) {
	limited, ok := refusalFor(sources.ErrRateLimited)
	if !ok {
		t.Fatal("a rate limit is not read as a refusal")
	}
	banned, ok := refusalFor(sources.ErrNotLoggedIn)
	if !ok {
		t.Fatal("being signed out of Soulseek is not read as a refusal")
	}

	for _, testCase := range []struct {
		name  string
		heard []refusal
	}{
		{"the long one first", []refusal{banned, limited}},
		{"the short one first", []refusal{limited, banned}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			var pass passRefusal
			for _, one := range testCase.heard {
				pass.record(one)
			}

			standing, refused := pass.refused()
			if !refused {
				t.Fatal("the pass remembered nothing, want the refusals it heard")
			}
			if standing.delay != banned.delay {
				t.Errorf("stand-back = %v, want the longest heard (%v)",
					standing.delay, banned.delay)
			}
			if standing.summary != banned.summary {
				t.Errorf("summary = %q, want the refusal the wait belongs to (%q)",
					standing.summary, banned.summary)
			}
		})
	}
}

// Two refusals asking for the same wait are one refusal as far as the pass is
// concerned, and the first is kept: it is the one that was actually heard from
// the provider, and a later want's copy says the same with less claim to it.
func TestAPassKeepsTheFirstOfTwoEqualRefusals(t *testing.T) {
	var pass passRefusal
	first, _ := refusalFor(sources.ErrRateLimited)
	second, _ := refusalFor(sources.ErrProviderUnavailable)
	if first.delay != second.delay {
		t.Fatalf("the two refusals ask for %v and %v, want this test to be about equal ones",
			first.delay, second.delay)
	}

	pass.record(first)
	pass.record(second)

	standing, refused := pass.refused()
	if !refused {
		t.Fatal("the pass remembered nothing, want the refusals it heard")
	}
	if standing.summary != first.summary {
		t.Errorf("summary = %q, want the one that was actually heard (%q)",
			standing.summary, first.summary)
	}
}

// A refusal about one want's own words is never recorded on the pass at all,
// which is what lets its siblings carry on being asked.
func TestAPhraseRefusalIsNotAPassRefusal(t *testing.T) {
	held, ok := refusalFor(sources.ErrPhraseHeldBack)
	if !ok {
		t.Fatal("a held phrase is not read as a refusal")
	}
	if !held.aboutOnePhrase {
		t.Error("a held phrase is read as being about the source, want it about the want")
	}
	if held.delay != throttleDelay {
		t.Errorf("stand-back = %v, want the ordinary %v", held.delay, throttleDelay)
	}
}

// A search slskd's own queue held back is a fact about that one search, not the
// account, so it is never recorded on the pass either.
func TestANeverAskedRefusalIsNotAPassRefusal(t *testing.T) {
	neverAsked, ok := refusalFor(sources.ErrNobodyWasAsked)
	if !ok {
		t.Fatal("a search nobody received is not read as a refusal")
	}
	if !neverAsked.aboutOnePhrase {
		t.Error("a search nobody received is read as being about the source, want it about the one search")
	}
}

// A search whose replies the source had not stored is the search worth making
// again, not an absence. It must never spend an attempt as "nobody is sharing
// this": peers answered it, and the empty list was the source's own bookkeeping.
func TestUnreadRepliesAreARefusalAboutTheOneSearch(t *testing.T) {
	unread, ok := refusalFor(sources.ErrRepliesUnread)
	if !ok {
		t.Fatal("replies nobody could read are not read as a refusal, so an attempt is spent on them")
	}
	if !unread.aboutOnePhrase {
		t.Error("replies nobody could read are read as being about the source, want it about the one search")
	}
	if unread.delay != throttleDelay {
		t.Errorf("stand-back = %v, want the ordinary %v", unread.delay, throttleDelay)
	}
	if unread.summary != repliesUnreadSummary {
		t.Errorf("summary = %q, want %q", unread.summary, repliesUnreadSummary)
	}
}

// Being signed out of Soulseek is the opposite of a search slskd's own queue
// held back: every search running at the same time fails the same way, so it
// is recorded on the pass and stands the whole pass back.
func TestALoggedOutRefusalIsAPassRefusal(t *testing.T) {
	loggedOut, ok := refusalFor(sources.ErrNotLoggedIn)
	if !ok {
		t.Fatal("being signed out of Soulseek is not read as a refusal")
	}
	if loggedOut.aboutOnePhrase {
		t.Error("being signed out of Soulseek is read as being about one want, want it about the source")
	}
	if loggedOut.delay != bannedRest {
		t.Errorf("stand-back = %v, want the %v the general refusal above gets", loggedOut.delay, bannedRest)
	}
}

// ErrSignedOut is the same account-wide fact as ErrNotLoggedIn, caught earlier
// by a cheaper check that runs before a search is even made (see ErrSignedOut's
// own comment). It wraps ErrProviderUnavailable rather than ErrNotLoggedIn, so
// before this it fell through refusalFor's narrow-before-wide switch to the
// general case and got throttleDelay's five minutes instead of bannedRest's
// half hour — the wait depending on which of the two checks got there first,
// exactly what the comment says must not happen.
func TestASignedOutRefusalGetsTheSameWaitAsALoggedOutOne(t *testing.T) {
	signedOut, ok := refusalFor(sources.ErrSignedOut)
	if !ok {
		t.Fatal("a source signed out before a search was made is not read as a refusal")
	}
	loggedOut, _ := refusalFor(sources.ErrNotLoggedIn)
	if signedOut.delay != loggedOut.delay {
		t.Errorf("stand-back = %v, want the same %v ErrNotLoggedIn gets, not throttleDelay's %v",
			signedOut.delay, loggedOut.delay, throttleDelay)
	}
	if signedOut.summary != loggedOut.summary {
		t.Errorf("summary = %q, want the same %q ErrNotLoggedIn gets", signedOut.summary, loggedOut.summary)
	}
	if signedOut.aboutOnePhrase {
		t.Error("a signed-out source is read as being about one want, want it about the source")
	}
}

// Soulseek answering this account's searches with silence is account-wide the
// way being signed out is: no want in the pass can be asked about while it
// lasts. It has to be one of the named refusals, or recordSearchFailure settles
// the want instead of deferring it — which is an attempt spent and an absence
// recorded on every want in the pass, for a question nobody was asked.
func TestASilencedSourceIsAPassRefusalAndSpendsNoAttempt(t *testing.T) {
	silenced, ok := refusalFor(sources.ErrSearchSilenced)
	if !ok {
		t.Fatal("a source Soulseek has stopped answering is not read as a refusal, so a want spends an attempt on it")
	}
	if silenced.aboutOnePhrase {
		t.Error("it is read as being about one want's words, want it about the source")
	}
	if silenced.delay != bannedRest {
		t.Errorf("stand-back = %v, want the %v the ban behind it lasts", silenced.delay, bannedRest)
	}
	if silenced.summary != silencedSummary {
		t.Errorf("summary = %q, want %q", silenced.summary, silencedSummary)
	}
}
