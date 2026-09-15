package jobs

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"syscall"

	"github.com/pxldi/schall/internal/musicbrainz"
	"github.com/pxldi/schall/internal/sources"
)

// A job is one piece of work the queue runs on its own: refreshing a release,
// importing a transfer, asking what a file is. A job that could not be finished
// records the Go error it stopped on, and that text is exact, complete and
// unreadable — the failure that stopped seventy-one jobs in August ended in a
// URL-encoded query string and the address of a DNS resolver.
//
// Classify reads one of those failures as a cause: the single fact behind it.
// Seventy-one jobs that could not resolve the name musicbrainz.org are one
// outage, and a screen listing them should say so once rather than seventy-one
// times.
//
// The classes are few and none of them is a guess: a server name that could not
// be resolved, a connection that was refused, a request that ran out of time, a
// connection that closed part way through, a service that refused the request, a
// service that could not answer, and everything else — which claims nothing and
// keeps its own text.

// FailureCause is the class of thing that stopped a job.
type FailureCause string

const (
	// CauseUnknownHost is a server whose name could not be turned into an
	// address. Nothing was sent and nothing was refused: the machine running
	// Schall could not find out where to send it.
	CauseUnknownHost FailureCause = "unknown_host"
	// CauseRefused is an address that answered by closing the door. Something
	// resolved, nothing was listening.
	CauseRefused FailureCause = "refused"
	// CauseTimedOut is a request that was sent and never answered inside the
	// time the caller was prepared to wait.
	CauseTimedOut FailureCause = "timed_out"
	// CauseDropped is a connection that was open and then was not. The request
	// was sent, the far side closed the connection, and no answer came back —
	// which Go reports as an end of file in the middle of a reply.
	CauseDropped FailureCause = "dropped"
	// CauseRejected is a service that answered, and answered no: an HTTP status
	// in the 400s. What Schall asked for is wrong or not allowed, and asking
	// again asks the same wrong question.
	CauseRejected FailureCause = "rejected"
	// CauseUnanswered is a service that answered with a fault of its own: an
	// HTTP status in the 500s. The question was fine; the far side was not.
	CauseUnanswered FailureCause = "service_failing"
	// CauseOther is everything Schall cannot put in one of the classes above.
	// It keeps the recorded text and says nothing about it.
	CauseOther FailureCause = "other"
)

// otherKey groups every failure Schall could not classify. It is one bucket
// rather than one row per distinct sentence: those sentences carry file names
// and identifiers, so grouping them by their text would be the unreadable list
// again under a new heading.
const otherKey = "other"

// Failure is what stopped a job, read as a cause.
//
// Key is what groups two failures into one row. It names the class and the
// service, and the status where there is one, so a MusicBrainz outage and an
// slskd outage stay two problems while seventy-one jobs stopped by the first
// stay one.
//
// Title and Detail are written for somebody who knows nothing about the
// internals: the title says what happened, and the detail says what the named
// thing is before it says what is wrong with it.
//
// Permanent says another attempt would meet the same answer. Unreachable says no
// answer came back at all — the service was not found, would not take the
// connection, did not reply in time, or closed the line part way through. That
// is the failure worth waiting longer before repeating, and it is what chooses
// the slower of the two ladders of waits between attempts.
type Failure struct {
	Cause       FailureCause
	Service     string
	Status      int
	Key         string
	Title       string
	Detail      string
	Permanent   bool
	Unreachable bool
}

// Temporary reports that the cause is one that goes away on its own: a service
// that could not be reached, one that answered with a fault of its own, or one
// that asked to be asked less often. A failure Schall could not classify is
// never temporary — the bucket that holds a wrong setting and an unreadable file
// is not one to run again on a timer.
func (failure Failure) Temporary() bool {
	return failure.Cause != CauseOther && failure.Cause != "" && !failure.Permanent
}

// Classify reads a live error. It is the recorded text plus the two things only
// a live error can say: the exact type of a network failure, and the sentinels
// that mean "not now" whatever words carried them.
func Classify(err error) Failure {
	if err == nil {
		return Failure{}
	}
	failure := ClassifyMessage(err.Error())
	if failure.Cause == CauseOther {
		failure = classifyNetworkError(err, failure)
	}
	// A provider asking to be left alone is never permanent, whatever status it
	// said it with. slskd answers 403 when it is not logged in to Soulseek and
	// MusicBrainz answers 429 when it is being asked too fast; both are "come
	// back", and spending a job's attempts is the wrong reading of them.
	if askedToWait(err) {
		failure.Permanent = false
	}
	return failure
}

// ClassifyMessage reads a failure that has already been recorded, where the text
// is all there is. It is what the failed-jobs screen groups by.
func ClassifyMessage(message string) Failure {
	trimmed := strings.TrimSpace(message)
	switch {
	case looksLikeNameFailure(trimmed):
		return unknownHost(hostInMessage(trimmed))
	case strings.Contains(trimmed, "connection refused"):
		return refused(addressInMessage(trimmed))
	case looksLikeTimeout(trimmed):
		return timedOut(hostInMessage(trimmed))
	case looksLikeDroppedConnection(trimmed):
		return dropped(hostInMessage(trimmed))
	default:
		if name, status, ok := statusInMessage(trimmed); ok {
			return answeredWith(name, status)
		}
		return somethingElse()
	}
}

// looksLikeNameFailure recognises a name that could not be resolved. Go writes
// those as a lookup inside a dial, and the tail of the sentence — "no such
// host", "server misbehaving" — is the resolver's own words and varies. The
// recorded text is cut at two thousand characters, so the tail may not even be
// there; the lookup at the front always is.
func looksLikeNameFailure(message string) bool {
	if !strings.Contains(message, "lookup ") {
		return false
	}
	for _, marker := range []string{
		"dial tcp: lookup ", ": lookup ", "no such host",
		"server misbehaving", "name resolution",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func looksLikeTimeout(message string) bool {
	for _, marker := range []string{
		"context deadline exceeded", "i/o timeout", "Client.Timeout exceeded",
		"TLS handshake timeout", "connection timed out", "awaiting headers",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

// looksLikeDroppedConnection recognises a connection that closed in the middle
// of a request. Go writes the far side hanging up as an end of file — ": EOF"
// at the end of the chain, or "unexpected EOF" when it happened while a body
// was being read — and a connection the far side reset as "connection reset by
// peer". The bare word EOF is not enough on its own: it is a common word in a
// recorded sentence, so the colon Go separates its clauses with is required.
func looksLikeDroppedConnection(message string) bool {
	for _, marker := range []string{": EOF", "unexpected EOF", "connection reset by peer"} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

// classifyNetworkError asks the error itself what kind of network failure it
// was, for the cases the text did not settle. A live *net.DNSError names the
// host it could not resolve without anybody parsing a sentence for it.
func classifyNetworkError(err error, unclassified Failure) Failure {
	var nameErr *net.DNSError
	if errors.As(err, &nameErr) {
		return unknownHost(nameErr.Name)
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return refused(endpointOf(err))
	}
	if errors.Is(err, syscall.ECONNRESET) {
		return dropped(endpointOf(err))
	}
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
		return timedOut(endpointOf(err))
	}
	return unclassified
}

// askedToWait reports that the far side said "not now". These are the sentinels
// the provider clients raise for backpressure, and they are temporary by
// definition however they are worded.
func askedToWait(err error) bool {
	return errors.Is(err, sources.ErrRateLimited) ||
		errors.Is(err, sources.ErrProviderUnavailable) ||
		errors.Is(err, musicbrainz.ErrThrottled)
}

func unknownHost(host string) Failure {
	known := serviceAt(host)
	return Failure{
		Cause:   CauseUnknownHost,
		Service: known.name,
		Key:     string(CauseUnknownHost) + ":" + known.key,
		Title:   fmt.Sprintf("Schall could not reach %s.", known.name),
		Detail: known.does + " The name of that server could not be turned into " +
			"an address from this machine, so no request was sent.",
		Unreachable: true,
	}
}

func refused(address string) Failure {
	known := serviceAt(address)
	return Failure{
		Cause:   CauseRefused,
		Service: known.name,
		Key:     string(CauseRefused) + ":" + known.key,
		Title:   fmt.Sprintf("Schall could not reach %s.", known.name),
		Detail: known.does + " The address answered by refusing the connection, " +
			"which is what an address with nothing listening on it does.",
		Unreachable: true,
	}
}

func timedOut(host string) Failure {
	known := serviceAt(host)
	return Failure{
		Cause:   CauseTimedOut,
		Service: known.name,
		Key:     string(CauseTimedOut) + ":" + known.key,
		Title:   fmt.Sprintf("%s did not answer in time.", sentenceStart(known.name)),
		Detail: known.does + " The request was sent and no answer had arrived by " +
			"the time Schall stopped waiting for one.",
		Unreachable: true,
	}
}

// dropped is a service that took the request and then closed the connection.
//
// It is not permanent — a connection closed part way through is the same passing
// fault as a timeout, and the jobs it stopped are worth running again on their
// own. It is marked unreachable for the same reason, which is what puts it on
// the slow ladder of waits: nothing came back, so there is nothing to say the
// far side is ready to answer five seconds later. On the ordinary ladder a job
// spends all six attempts inside two and a half minutes, which is shorter than
// most outages and turns one into a job a person has to come and press.
func dropped(host string) Failure {
	known := serviceAt(host)
	return Failure{
		Cause:   CauseDropped,
		Service: known.name,
		Key:     string(CauseDropped) + ":" + known.key,
		Title:   fmt.Sprintf("%s dropped the connection.", sentenceStart(known.name)),
		Detail: known.does + " The request was sent and the connection closed " +
			"before any answer arrived.",
		Unreachable: true,
	}
}

// answeredWith reads a service that answered. A status in the 400s is about
// what was asked — it will be the same answer next time — and a status in the
// 500s is a fault at the far end, which is worth meeting again later.
func answeredWith(name string, status int) Failure {
	known := serviceNamed(name)
	if status >= 500 {
		return Failure{
			Cause:   CauseUnanswered,
			Service: known.name,
			Status:  status,
			Key:     fmt.Sprintf("%s:%s:%d", CauseUnanswered, known.key, status),
			Title:   fmt.Sprintf("%s could not answer (HTTP %d).", sentenceStart(known.name), status),
			Detail: known.does + fmt.Sprintf(" It answered %d, which is a fault on its "+
				"side rather than anything wrong with what Schall asked for.", status),
		}
	}
	permanent := status != 408 && status != 429
	detail := known.does + fmt.Sprintf(" It answered %d, which means %s.", status, meaningOf(status))
	if permanent {
		detail += " Another attempt is met by the same answer until something is corrected."
	}
	return Failure{
		Cause:     CauseRejected,
		Service:   known.name,
		Status:    status,
		Key:       fmt.Sprintf("%s:%s:%d", CauseRejected, known.key, status),
		Title:     fmt.Sprintf("%s refused the request (HTTP %d).", sentenceStart(known.name), status),
		Detail:    detail,
		Permanent: permanent,
	}
}

// meaningOf says what a status code means in words. Only the ones Schall meets
// are named; anything else is left as the number it is, because inventing a
// meaning for a status nobody has seen would be this file guessing.
func meaningOf(status int) string {
	switch status {
	case 400:
		return "the service did not understand the request"
	case 401:
		return "Schall is not signed in to it"
	case 403:
		return "the account Schall uses is not allowed to do this"
	case 404:
		return "what Schall asked for is not there"
	case 408:
		return "the service stopped waiting for the request to arrive"
	case 429:
		return "Schall is asking more often than the service allows"
	default:
		return "the service refused the request"
	}
}

func somethingElse() Failure {
	return Failure{
		Cause: CauseOther,
		Key:   otherKey,
		Title: "Something else stopped this work.",
		Detail: "Schall could not put these failures into one class. What each job " +
			"recorded is below, word for word, with nothing taken out.",
	}
}

// service is one program or website Schall talks to, and what it talks to it
// for. The description is what makes a failure readable to somebody who has
// never heard of the thing that failed.
type service struct {
	// key is what the group is keyed on, so that two failures against the same
	// service group together whether the host or the client's own name named it.
	key  string
	name string
	does string
}

// known are the services Schall is built to talk to. Each is matched two ways,
// because a failure names them two ways: a network failure carries the host it
// was dialling, and a service that answered names itself in the error its
// client raises ("MusicBrainz returned HTTP 404").
var known = []struct {
	host string
	word string
	service
}{
	{
		host: "musicbrainz.org", word: "musicbrainz",
		service: service{key: "musicbrainz", name: "MusicBrainz",
			does: "Schall looks up artists, releases and recordings at musicbrainz.org."},
	},
	{
		host: "acoustid.org", word: "acoustid",
		service: service{key: "acoustid", name: "AcoustID",
			does: "Schall identifies audio by its fingerprint at acoustid.org."},
	},
	{
		host: "listenbrainz.org", word: "listenbrainz",
		service: service{key: "listenbrainz", name: "ListenBrainz",
			does: "Schall asks listenbrainz.org which recordings to suggest."},
	},
	{
		host: "coverartarchive.org", word: "coverartarchive",
		service: service{key: "coverart", name: "the Cover Art Archive",
			does: "Schall fetches the picture of a release from coverartarchive.org."},
	},
	{
		host: "spotify.com", word: "spotify",
		service: service{key: "spotify", name: "Spotify",
			does: "Schall reads followed playlists from spotify.com."},
	},
	{
		word: "slskd",
		service: service{key: "slskd", name: "slskd",
			does: "slskd is the Soulseek client Schall searches and downloads through. " +
				"It runs beside Schall, at the address given in Settings."},
	},
	{
		word: "navidrome",
		service: service{key: "navidrome", name: "Navidrome",
			does: "Navidrome is the music player Schall keeps its playlists in step with."},
	},
}

// serviceAt names the service at a host or address. A host Schall does not know
// is still an answer — it is named as itself, and the description says only what
// is true, which is that Schall was talking to it.
func serviceAt(endpoint string) service {
	host := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(endpoint)), ".")
	if host == "" {
		return service{
			key:  "unnamed",
			name: "the service it needed",
			does: "Schall was talking to another program or website, and the recorded " +
				"failure does not say which.",
		}
	}
	bare := host
	if trimmed, _, found := strings.Cut(host, ":"); found {
		bare = trimmed
	}
	for _, candidate := range known {
		if candidate.host != "" && (bare == candidate.host || strings.HasSuffix(bare, "."+candidate.host)) {
			return candidate.service
		}
	}
	return service{
		key:  host,
		name: host,
		does: fmt.Sprintf("Schall was talking to %s.", host),
	}
}

// serviceNamed names the service from the word its own client puts in front of
// a status. Every client writes that sentence the same way, which is what makes
// one rule enough.
func serviceNamed(word string) service {
	lowered := strings.ToLower(strings.TrimSpace(word))
	for _, candidate := range known {
		if lowered == candidate.word {
			return candidate.service
		}
	}
	if lowered == "" {
		return serviceAt("")
	}
	return service{
		key:  lowered,
		name: word,
		does: fmt.Sprintf("Schall was talking to %s.", word),
	}
}

// sentenceStart capitalises a name that starts a sentence, and leaves alone one
// that is already written the way its owner writes it. "slskd" is lowercase on
// purpose and "the Cover Art Archive" starts with an article.
func sentenceStart(name string) string {
	if name == "" {
		return name
	}
	if strings.HasPrefix(name, "the ") {
		return "The " + strings.TrimPrefix(name, "the ")
	}
	return name
}

// statusInMessage reads the one sentence every provider client writes when a
// service answered and the answer was not a success: "<service> returned HTTP
// <status>".
func statusInMessage(message string) (string, int, bool) {
	const marker = " returned HTTP "
	at := strings.LastIndex(message, marker)
	if at < 0 {
		return "", 0, false
	}
	name := lastWord(message[:at])
	digits := message[at+len(marker):]
	end := 0
	for end < len(digits) && digits[end] >= '0' && digits[end] <= '9' {
		end++
	}
	status, err := strconv.Atoi(digits[:end])
	if err != nil || status < 100 || status > 599 {
		return "", 0, false
	}
	return name, status, true
}

func lastWord(text string) string {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return ""
	}
	return strings.Trim(fields[len(fields)-1], `"':,`)
}

// endpointOf reads the host a live error was talking to. An HTTP client wraps
// every failure in a *url.Error, which carries the URL that was being fetched.
func endpointOf(err error) string {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		if parsed, parseErr := url.Parse(urlErr.URL); parseErr == nil && parsed.Host != "" {
			return parsed.Host
		}
	}
	return hostInMessage(err.Error())
}

// hostInMessage reads the host out of a recorded failure, which is all a stored
// message leaves to read. Three shapes carry one, in the order they are worth
// trusting: the URL an HTTP failure quotes, the name a resolver was asked for,
// and the address a dial was made to.
func hostInMessage(message string) string {
	if host := hostInQuotedURL(message); host != "" {
		return host
	}
	if host := wordAfter(message, "lookup "); host != "" {
		return host
	}
	return addressInMessage(message)
}

// addressInMessage reads the address a connection was attempted to. Go writes
// it after the network name, as "dial tcp 10.43.0.7:5030: connect: connection
// refused".
func addressInMessage(message string) string {
	for _, marker := range []string{"dial tcp ", "dial udp "} {
		if address := wordAfter(message, marker); address != "" {
			return address
		}
	}
	return hostInQuotedURL(message)
}

// hostInQuotedURL reads the host of the URL an HTTP failure quotes, as in
// Get "https://musicbrainz.org/ws/2/recording/...". The URL is taken as far as
// the closing quote, so a query string full of escapes cannot end it early.
func hostInQuotedURL(message string) string {
	at := strings.Index(message, `"http`)
	if at < 0 {
		return ""
	}
	rest := message[at+1:]
	end := strings.Index(rest, `"`)
	if end < 0 {
		end = len(rest)
	}
	parsed, err := url.Parse(rest[:end])
	if err != nil {
		return ""
	}
	return parsed.Host
}

// wordAfter reads the word that follows a marker, stopping at whitespace or at
// the colon Go's network errors separate clauses with.
func wordAfter(message, marker string) string {
	at := strings.Index(message, marker)
	if at < 0 {
		return ""
	}
	rest := message[at+len(marker):]
	end := strings.IndexAny(rest, " \t")
	if end >= 0 {
		rest = rest[:end]
	}
	return strings.TrimSuffix(strings.TrimSpace(rest), ":")
}
