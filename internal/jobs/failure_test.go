package jobs

import (
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/pxldi/schall/internal/sources"
)

// outage is the failure that stopped seventy-one jobs on 12 August 2026, as the
// queue recorded it: the whole chain of what was being attempted, ending in a
// URL-encoded query string and the address of a DNS resolver, and cut off at the
// length the column holds.
const outage = `import download: verify "10-ufo361-gewinn.flac" against recording ` +
	`c75fe756-1e6e-4a5b-9c7c-4b0b2f7e1f10: look up MusicBrainz recording: ` +
	`Get "https://musicbrainz.org/ws/2/recording/c75fe756-1e6e-4a5b-9c7c-4b0b2f7e1f10` +
	`?fmt=json&inc=artist-credits%2Bisrcs%2Breleases%2Brelease-groups": ` +
	`dial tcp: lookup musicbrainz.org on 10.43.0.10:53: serv`

func TestAServerNameThatCouldNotBeResolvedReadsAsThatServiceBeingUnreachable(t *testing.T) {
	failure := ClassifyMessage(outage)

	if failure.Cause != CauseUnknownHost {
		t.Fatalf("cause = %q, want %q", failure.Cause, CauseUnknownHost)
	}
	if failure.Service != "MusicBrainz" {
		t.Fatalf("service = %q, want MusicBrainz", failure.Service)
	}
}

func TestTwoJobsStoppedByOneOutageShareOneCause(t *testing.T) {
	first := ClassifyMessage(outage)
	second := ClassifyMessage(`resolve library file: look up MusicBrainz recording: ` +
		`Get "https://musicbrainz.org/ws/2/recording/9f1?fmt=json": ` +
		`dial tcp: lookup musicbrainz.org on 10.43.0.10:53: no such host`)

	if first.Key != second.Key {
		t.Fatalf("keys = %q and %q, want one cause for one outage", first.Key, second.Key)
	}
}

func TestTwoServicesThatCouldNotBeReachedStayTwoCauses(t *testing.T) {
	musicBrainz := ClassifyMessage(outage)
	acoustID := ClassifyMessage(
		`identify audio: Get "https://api.acoustid.org/v2/lookup": ` +
			`dial tcp: lookup api.acoustid.org on 10.43.0.10:53: no such host`)

	if musicBrainz.Key == acoustID.Key {
		t.Fatalf("both keys are %q, want one cause each", musicBrainz.Key)
	}
}

func TestACauseIsWrittenWithoutTheRecordedErrorInIt(t *testing.T) {
	failure := ClassifyMessage(outage)

	for _, unwanted := range []string{"%2B", "fmt=json", "dial tcp", "10.43.0.10:53"} {
		if strings.Contains(failure.Title+failure.Detail, unwanted) {
			t.Fatalf("the cause quotes %q; it is written for a reader, not copied from the log", unwanted)
		}
	}
}

func TestACauseSaysWhatTheServiceIsBeforeWhatWentWrongWithIt(t *testing.T) {
	failure := ClassifyMessage(outage)

	if !strings.HasPrefix(failure.Detail, "Schall looks up artists, releases and recordings at musicbrainz.org.") {
		t.Fatalf("detail = %q, want it to name what MusicBrainz is first", failure.Detail)
	}
}

func TestARefusedConnectionReadsAsNothingListeningAtThatAddress(t *testing.T) {
	failure := ClassifyMessage(
		"search album sources: dial tcp 10.43.0.7:5030: connect: connection refused")

	if failure.Cause != CauseRefused {
		t.Fatalf("cause = %q, want %q", failure.Cause, CauseRefused)
	}
	if !strings.Contains(failure.Title, "10.43.0.7:5030") {
		t.Fatalf("title = %q, want the address that refused named in it", failure.Title)
	}
}

func TestARequestThatRanOutOfTimeReadsAsATimeout(t *testing.T) {
	failure := ClassifyMessage(
		`fetch artist catalogue: Get "https://musicbrainz.org/ws/2/artist/9f1": ` +
			`context deadline exceeded (Client.Timeout exceeded while awaiting headers)`)

	if failure.Cause != CauseTimedOut {
		t.Fatalf("cause = %q, want %q", failure.Cause, CauseTimedOut)
	}
	if failure.Service != "MusicBrainz" {
		t.Fatalf("service = %q, want MusicBrainz", failure.Service)
	}
}

func TestAServiceThatCouldNotBeReachedIsMarkedUnreachable(t *testing.T) {
	if !ClassifyMessage(outage).Unreachable {
		t.Fatal("a name that could not be resolved reached nobody, and must say so")
	}
}

func TestARefusalFromANamedServiceIsPermanent(t *testing.T) {
	failure := ClassifyMessage("fetch release group: MusicBrainz returned HTTP 404")

	if failure.Cause != CauseRejected {
		t.Fatalf("cause = %q, want %q", failure.Cause, CauseRejected)
	}
	if !failure.Permanent {
		t.Fatal("a 404 is the same answer next time and must not be tried again")
	}
}

func TestAFaultAtTheFarEndIsNotPermanent(t *testing.T) {
	failure := ClassifyMessage("search album sources: slskd returned HTTP 500")

	if failure.Cause != CauseUnanswered {
		t.Fatalf("cause = %q, want %q", failure.Cause, CauseUnanswered)
	}
	if failure.Permanent {
		t.Fatal("a fault on the other side is worth meeting again later")
	}
}

func TestBeingAskedToSlowDownIsNotPermanent(t *testing.T) {
	if ClassifyMessage("slskd returned HTTP 429").Permanent {
		t.Fatal("a service asking to be asked less often has not refused for good")
	}
}

func TestTwoStatusesFromOneServiceStayTwoCauses(t *testing.T) {
	missing := ClassifyMessage("MusicBrainz returned HTTP 404")
	unauthorised := ClassifyMessage("MusicBrainz returned HTTP 401")

	if missing.Key == unauthorised.Key {
		t.Fatalf("both keys are %q, want the two answers told apart", missing.Key)
	}
}

func TestAFailureThatFitsNoClassKeepsItsOwnTextAndClaimsNothing(t *testing.T) {
	failure := ClassifyMessage("apply layout migration 9f1: rename file: permission denied")

	if failure.Cause != CauseOther {
		t.Fatalf("cause = %q, want %q", failure.Cause, CauseOther)
	}
	if failure.Permanent || failure.Unreachable {
		t.Fatal("an unclassified failure must claim nothing about itself")
	}
}

func TestEveryFailureThatFitsNoClassIsOneGroup(t *testing.T) {
	first := ClassifyMessage("import upload 9f1: the folder is no longer staged")
	second := ClassifyMessage("apply layout migration 4c2: rename file: permission denied")

	if first.Key != second.Key {
		t.Fatalf("keys = %q and %q, want one bucket for what Schall cannot name",
			first.Key, second.Key)
	}
}

func TestAnUnclassifiedFailureIsNeverTriedAgainOnATimer(t *testing.T) {
	if ClassifyMessage("import upload 9f1: the folder is no longer staged").Temporary() {
		t.Fatal("a failure nobody has classified must wait for a person to read it")
	}
}

func TestAnOutageIsTriedAgainOnATimer(t *testing.T) {
	if !ClassifyMessage(outage).Temporary() {
		t.Fatal("a service that could not be reached comes back on its own")
	}
}

// A provider that asks to be left alone says so with a sentinel of its own, and
// the status it carried must not turn that into a permanent refusal.
func TestAProviderAskingToBeLeftAloneIsNeverPermanent(t *testing.T) {
	err := fmt.Errorf("search album sources: %w: slskd returned HTTP 403",
		sources.ErrProviderUnavailable)

	if Classify(err).Permanent {
		t.Fatal("a provider that cannot search right now is a reason to come back")
	}
}

func TestALiveNameFailureIsReadFromTheErrorItself(t *testing.T) {
	err := fmt.Errorf("look up recording: %w",
		&net.DNSError{Err: "server misbehaving", Name: "musicbrainz.org", IsTemporary: true})

	if failure := Classify(err); failure.Service != "MusicBrainz" {
		t.Fatalf("service = %q, want MusicBrainz read from the error's own host", failure.Service)
	}
}

func TestNoErrorIsNoFailure(t *testing.T) {
	if failure := Classify(nil); failure.Cause != "" {
		t.Fatalf("cause = %q, want nothing said about nothing", failure.Cause)
	}
	if Classify(nil).Temporary() {
		t.Fatal("no failure is not a temporary failure")
	}
}

// dropped is the failure that stopped sixty-four jobs on 26 August 2026, as the
// queue recorded it. MusicBrainz took the request and closed the connection
// before it answered, which Go writes as an end of file at the end of the chain.
const droppedLine = `import download: verify "03-ufo361-stay.flac" against recording ` +
	`3f0b1c22-9d31-4a0f-8f21-6b2a0d5c7e44: look up MusicBrainz recording: ` +
	`Get "https://musicbrainz.org/ws/2/recording/3f0b1c22-9d31-4a0f-8f21-6b2a0d5c7e44` +
	`?fmt=json&inc=artist-credits%2Bisrcs%2Breleases%2Brelease-groups": EOF`

func TestAConnectionThatClosedPartWayThroughReadsAsThatServiceDroppingIt(t *testing.T) {
	failure := ClassifyMessage(droppedLine)

	if failure.Cause != CauseDropped {
		t.Fatalf("cause = %q, want %q", failure.Cause, CauseDropped)
	}
	if failure.Service != "MusicBrainz" {
		t.Fatalf("service = %q, want MusicBrainz", failure.Service)
	}
	if failure.Key != "dropped:musicbrainz" {
		t.Fatalf("key = %q, want dropped:musicbrainz", failure.Key)
	}
}

// The far side hanging up comes back on its own, exactly as a timeout does. The
// pass that runs an outage's jobs again reads this class, and sixty-four jobs
// that stopped on it must not wait for somebody to press Retry.
func TestADroppedConnectionIsTriedAgainOnATimer(t *testing.T) {
	if !ClassifyMessage(droppedLine).Temporary() {
		t.Fatal("a connection the far side closed is worth making again")
	}
}

// A dropped connection waits as long between attempts as a timeout does. Both
// end with no answer, and five seconds later there is still nothing to say the
// far side is ready — six attempts on the short ladder are gone in two and a
// half minutes, which is shorter than the outage that stopped them.
func TestADroppedConnectionWaitsAsLongAsATimeoutBetweenAttempts(t *testing.T) {
	dropped := retryDelay(ClassifyMessage(droppedLine), 2)
	timedOut := retryDelay(ClassifyMessage(
		`look up MusicBrainz recording: Get "https://musicbrainz.org/ws/2/recording/9f1`+
			`?fmt=json": context deadline exceeded`), 2)

	if dropped != timedOut {
		t.Fatalf("dropped waits %s and a timeout waits %s, want one ladder for both",
			dropped, timedOut)
	}
}

// The same fault written the two other ways Go writes it: a body that stopped
// part way through, and a connection the far side reset.
func TestTheOtherWordsForADroppedConnectionReadTheSameWay(t *testing.T) {
	body := ClassifyMessage(`refresh artist metadata: look up MusicBrainz artist: ` +
		`Get "https://musicbrainz.org/ws/2/artist/9f1?fmt=json": unexpected EOF`)
	reset := ClassifyMessage(`refresh artist metadata: look up MusicBrainz artist: ` +
		`Get "https://musicbrainz.org/ws/2/artist/4c2?fmt=json": read tcp 10.42.0.9:41022->` +
		`151.101.1.100:443: read: connection reset by peer`)

	if body.Cause != CauseDropped || reset.Cause != CauseDropped {
		t.Fatalf("causes = %q and %q, want %q", body.Cause, reset.Cause, CauseDropped)
	}
}

func TestTwoServicesThatDroppedTheConnectionStayTwoCauses(t *testing.T) {
	musicBrainz := ClassifyMessage(droppedLine)
	acoustID := ClassifyMessage(
		`identify audio: Get "https://api.acoustid.org/v2/lookup": EOF`)

	if musicBrainz.Key == acoustID.Key {
		t.Fatalf("both keys are %q, want one cause each", musicBrainz.Key)
	}
}

// A name that could not be resolved is written by Go as a dial that ends in the
// resolver's own words, and those sentences carry colons of their own. The
// class that reads an end of file must not take one of those.
func TestANameThatCouldNotBeResolvedIsStillReadAsAName(t *testing.T) {
	if failure := ClassifyMessage(outage); failure.Cause != CauseUnknownHost {
		t.Fatalf("cause = %q, want %q", failure.Cause, CauseUnknownHost)
	}
}
