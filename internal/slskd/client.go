// Package slskd implements a search-only slskd provider. It inspects what
// peers are offering and never starts, queues, or imports a transfer.
package slskd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/sources"
	"github.com/rs/zerolog"
)

const (
	// Name identifies this provider in API responses and logs.
	Name = "slskd"

	apiPrefix         = "/api/v0"
	maxResponseBody   = 8 << 20
	maxCandidates     = 50
	maxFilesPerFolder = 200

	defaultSearchTimeout = 20 * time.Second
	defaultPollInterval  = time.Second
	defaultHTTPTimeout   = 10 * time.Second

	// defaultDequeueBudget is how long Search waits for slskd to take a search
	// out of its own queue and put it on the network, before the reply window
	// that defaultSearchTimeout and the plateau govern is allowed to start.
	//
	// slskd gives a search "Requested" or "Queued" while the search is still
	// waiting inside slskd's own client, and only marks it "InProgress" once the
	// request has actually reached the Soulseek server (see neverAsked). Read
	// live from slskd's own API: 29 searches Schall created were sitting in
	// "Queued" with endedAt: null, and the 14 most recently created were all of
	// them still queued. None of that is a Soulseek ban — slskd reported
	// "Connected, LoggedIn" the whole time — it is slskd's queue running behind
	// the rate Schall was creating searches at.
	//
	// Across 600 searches in slskd's history, the wall-clock from startedAt to
	// endedAt had a median of 162 seconds and a p90 of 338 seconds. That is the
	// whole search, dequeue wait and reply collection together, not the dequeue
	// wait alone — slskd does not report the two separately. But dequeue time
	// can never exceed total completion time for the same search, so a budget of
	// 338 seconds is provably enough to cover at least the p90 of dequeue waits
	// too, where 162 seconds would only be guaranteed to cover half of them.
	// Reproducing the bug this replaces at a slower timescale, for the searches
	// slowest to dequeue, is the failure mode a smaller number risks.
	//
	// A search still queued when this runs out is real backpressure and still
	// reported as sources.ErrNobodyWasAsked (see confirmAsked); nothing here
	// changes what may be read as an answer, only how long Schall waits before
	// deciding there wasn't one.
	defaultDequeueBudget = 338 * time.Second

	// defaultReplyPlateau is how long a search that peers have already answered
	// waits for one more reply before its window is closed.
	//
	// A search window is the time Schall spends watching one Soulseek search
	// collect replies. Until now it was one fixed number for every search: the
	// budget above, twenty seconds, spent whether peers were still answering or
	// had all finished in the first three.
	//
	// The windows were measured before they were shortened. Over 72 hours of
	// ordinary use, 2,578 windows on the 20-second budget: the median window ran
	// 18.0 seconds, the median last reply landed at 3.0 seconds, and the median
	// window therefore spent 15.0 seconds after its last reply hearing nothing.
	// Of 11.59 hours of window time in total, 8.44 hours came after the last
	// reply.
	//
	// Replaying those same reply curves, closing a window this many seconds
	// after its last new reply saves 2.24 of those hours, and 61 of the 2,578
	// windows — 2.4% — lose a reply that would have arrived later. None of them
	// lose every reply. Six seconds saves more and costs 4.5% of windows a late
	// reply; ten seconds costs 1.2% and saves a third less. Eight is where that
	// trade was settled.
	//
	// The plateau only starts once a peer has answered. A search nobody replied
	// to has no plateau to reach and waits out its whole budget, which is what
	// the 705 windows with no reply at all did in the measurement and still do:
	// silence is never read as an answer here.
	//
	// Eight seconds is the floor and the measured answer for a twenty-second
	// window. It is not the answer for every window. "Search timeout" on the
	// settings page is how long to wait for peers, and somebody who raises it to
	// two minutes is saying peers on this connection answer late — a fixed eight
	// seconds would close their windows at the same early moment and throw away
	// what they lengthened the window to collect. So the plateau is held at the
	// same share of the window the measurement settled on: eight seconds of
	// twenty, which is two fifths, applied to whatever width the setting names.
	// See plateauFor.
	defaultReplyPlateau = 8 * time.Second

	// plateauNumerator and plateauDenominator are that share, kept as two
	// integers so the arithmetic stays in time.Duration and nothing rounds
	// through a float.
	plateauNumerator   = 2
	plateauDenominator = 5

	// defaultSearchConcurrency is how many searches may be in flight at once. A
	// search is twenty seconds of almost pure waiting, so running several is close
	// to free in wall-clock terms. Being in flight is not the same as being
	// created: slskd admits exactly one creation at a time (see Gate.creation),
	// and it is creations, not reply windows, that earn the 429.
	//
	// Four was the binding constraint. Measured on the live instance over 145
	// minutes on 2026-08-30: 922 searches, 3.48 of the four slots occupied on
	// average, 6.37 searches a minute against the 12 a minute the creation
	// interval below already allows. Half of every slot was dead time — a
	// search closing on its reply budget had sat 25 seconds median inside
	// slskd's own queue before it went out, and the slot was held for all of
	// it.
	//
	// Eight doubles what may be waiting without changing how fast anything is
	// asked. The creation interval is untouched, so the rate the Soulseek
	// server sees is the same rate it saw before; what changes is that the
	// interval, rather than the slots, is what the rate runs into.
	defaultSearchConcurrency = 8

	// defaultCreationInterval is the least time between two search creations,
	// across every lane at once.
	//
	// The Soulseek server — not slskd — bans this account for thirty minutes when
	// it decides searches are arriving too fast, and "do not quickly repeat a
	// search" is its own wording for what earns it. The concurrency bounds never
	// touched that: four searches in flight can be four creations in the same
	// second, and one want walks a ladder of four to six near-identical phrases
	// with nothing between the rungs.
	//
	// The server's tolerance is undocumented, so this is a starting point rather
	// than a measurement. It is long enough to turn a burst into a trickle, and
	// short enough to cost a ladder almost nothing: a rung that spent its reply
	// window has already waited far longer than this, so the wait falls only on
	// searches that came back at once.
	//
	// Ten seconds since 2026-09-05. That night a fresh pod walked its backlog of
	// 694 wants at 8.5 creations a minute under a five-second interval, and the
	// server stopped answering after about 50 searches in six and a half
	// minutes. The 145-minute run of 2026-08-30 at 6.37 a minute earned no ban.
	// Ten seconds caps the rate at six a minute, the one rate known to have
	// survived. One search per want for that backlog takes about two hours.
	defaultCreationInterval = 10 * time.Second

	// defaultRepeatWindow and repeatAllowance are the bound on asking the same
	// thing rather than on asking quickly, which is the offence the ban names
	// first: "do not repeat the same text several times in a row".
	//
	// Nothing before this counted repetition. The interval turns a burst into a
	// trickle, but a trickle of one phrase is still that phrase over and over,
	// and that is what a real afternoon looked like: "souly teufel fallen" went
	// out fifteen times in six minutes, twice inside the same second, and the
	// ban followed. Two wants from one release ask the release rung in the same
	// words; a search that fails after its budget has gone brings the whole want
	// back to say them again.
	//
	// A phrase is allowed twice rather than once because the second ask is
	// deliberate and load-bearing: a want whose ladder came back empty asks its
	// broadest rung once more (internal/downloads, emptyRetryDelay), and that
	// echo is how an unreliable network's silence is told from an answer.
	// Refusing it would leave such a want deferred forever instead of settling,
	// which is more traffic than it saves. The gate's interval already keeps the
	// two asks at least ten seconds apart, so the allowance never permits the
	// same second twice.
	//
	// The window is a guess, as the interval is: the server's tolerance is
	// undocumented and only an experiment on a live account would refine it.
	// What it is fitted to is what surrounds it. It is shorter than the five
	// minutes a refused want stands back for (internal/acquisition,
	// throttleDelay), so a want refused here finds its phrases askable when it
	// returns rather than being refused a second time; and far shorter than the
	// fifteen minutes before a want's own next attempt (attemptDelays), so no
	// want's ordinary cadence ever trips it.
	//
	// What it costs is one want's turn, and no more than that. Acquisition reads
	// this refusal as being about the want's own words rather than about the
	// source, so the want that said them stands back five minutes and every
	// other want in the sweep is asked as usual — they have words of their own,
	// and the source is answering. The bill used to fall wider: any refusal was
	// read as the source refusing to search, so one held repeat stood the whole
	// pass back, and ten wants sharing a release took five passes and something
	// like twenty-five minutes before any of them was asked, with wants for
	// other music queued behind them waiting through it.
	//
	// Nothing is lost by the refusal either way: it spends no attempt and
	// records no absence, so no want is worse off than delayed, and each pass
	// drains the allowance again. That it drains rather than circles is the
	// ladder's doing more than this window's — a want that was asked settles and
	// leaves on attemptDelays, which climbs, so a noisy release thins out
	// instead of coming back in step with itself.
	defaultRepeatWindow = 4 * time.Minute
	repeatAllowance     = 2

	// silentWindowsBeforePause is how many searches in a row may reach the
	// Soulseek network and collect no reply at all before Schall stops asking.
	//
	// It reads the one refusal confirmAsked said it could not see. The Soulseek
	// server serves some of its thirty-minute bans while leaving the connection
	// up: slskd goes on accepting searches, /server goes on reporting
	// "Connected, LoggedIn", and every search comes back empty. On 2026-09-04
	// the replies stopped at 09:52, the server's own ban notice arrived at
	// 10:03:14, and the connection check refused its first search at 10:05:40.
	// In between, 84 searches ran and 72 collected nothing — each one an
	// absence recorded and an attempt spent on a want, about music peers were
	// sharing the whole time.
	//
	// Eight separates the two states cleanly in that day's log. Inside the
	// episode the longest run of consecutive empty windows was 63. Across the
	// other 671 windows of the same day the longest was 4, once, then 2 ten
	// times and 1 fifty-two times. So eight is twice the longest run ordinary
	// quiet has ever produced here, and it would have fired about a minute
	// after the replies stopped rather than thirteen.
	//
	// Only a window that reached the network is counted, and only one nothing
	// else explains. A search slskd never sent, or gave up on, or ran while the
	// account was signed out, says nothing about whether Soulseek is
	// distributing this account's queries — and confirmAsked already reports
	// each of those as backpressure of its own.
	silentWindowsBeforePause = 8

	// silencedRest is how long searching stops once that run is reached, timed
	// from the last search that went unanswered.
	//
	// It is the length of the ban in the server's own words: "you will be able
	// to reconnect to the service in 30 minutes". Timed from the last window
	// rather than the first because the run is what dates the episode, and the
	// ban may well have started before Schall could see it.
	//
	// Waiting it out costs a want nothing that matters. No attempt is spent and
	// no absence recorded (see sources.ErrSearchSilenced), so every want comes
	// back exactly where it was — and the pause lifts early if a peer answers
	// or the account is seen signing back in.
	silencedRest = 30 * time.Minute

	// banNotice is the phrase the Soulseek server's own message carries, in the
	// conversation slskd keeps with it. It confirms a pause that has already
	// begun and never starts one: the notice arrived eleven minutes after the
	// replies stopped on 2026-09-04, so waiting for it would be waiting out most
	// of the ban.
	banNotice = "you have been banned"

	// connectionMemory is how long a look at slskd's Soulseek connection is
	// believed before it is worth another look.
	//
	// It is what keeps reading the connection before every search from costing a
	// request per search: at the gate's interval, thirty seconds spans several
	// creations, and while the connection is down every one of them is refused
	// without touching the network at all. Short, because the ban it is most
	// likely to be reading lasts thirty minutes and the account comes back
	// without anybody being told.
	connectionMemory = 30 * time.Second

	// searchQueueMemory is how long one reading of slskd's outgoing search
	// queue is shared by the searches behind it. Listing searches is local to
	// slskd but returns its retained history as well as the live ones, so doing
	// it before every creation would turn backpressure into needless work.
	searchQueueMemory = 30 * time.Second

	// maxQueuedSearches is the backlog Schall will tolerate before it stops
	// creating more work for slskd. It matches the number of searches Schall can
	// hold in flight itself: one full set may be waiting inside slskd, but a
	// second set is refused until the first has left that queue.
	maxQueuedSearches = defaultSearchConcurrency

	// reapBudget is how long the cleanup of a search Schall stopped watching
	// waits for slskd to be done with it, asking at the same interval the search
	// itself was watched at. It is generous on purpose: these are calls to
	// slskd's own API, which cost the Soulseek network nothing, and what they
	// buy is not deleting a search that is still running.
	reapBudget = 2 * time.Minute

	// defaultCompletionGrace is how long a search Schall has stopped reading is
	// given to finish at slskd before Schall gives up on its replies.
	//
	// slskd stores a search's responses only once the search reaches a completed
	// state. While it is still running, GET /searches/{id}/responses answers an
	// empty list however many replies its own responseCount has counted, and
	// Schall was reading that list as the answer. Measured over 2h22m on
	// 2026-09-04: 491 of 759 windows closed with slskd still running the search,
	// they had collected 29,204 replies between them, and not one of them
	// produced a single candidate.
	//
	// Two minutes is what the measurement supports. Every one of those 491
	// searches had been deleted from slskd afterwards, and removeOnceFinished
	// only deletes a search it saw complete inside reapBudget, so every one of
	// them completed within two minutes of being abandoned. After the stop
	// settleWindow sends first, it should take a poll.
	defaultCompletionGrace = 2 * time.Minute

	// creationAttempts and creationRetryDelay absorb a creation that collided
	// with one made outside this process — somebody searching in slskd's own
	// interface at the same moment. One short retry is enough: our own
	// creations are already serialized, so a second refusal means slskd is
	// genuinely busy and the layers above should hear it.
	creationAttempts   = 2
	creationRetryDelay = time.Second

	// maxCurveSeconds bounds the reply-arrival curve one search records. The
	// window is a setting, so this is only here to keep a mis-set one from
	// growing a log line without limit.
	maxCurveSeconds = 120
)

// Options configures a client. BaseURL and APIKey are required.
type Options struct {
	BaseURL       string
	APIKey        string
	HTTPClient    *http.Client
	SearchTimeout time.Duration
	PollInterval  time.Duration
	// Gate bounds how many searches are in flight and how closely together they
	// are created. Nil gives this client one of its own, which bounds it alone;
	// callers that build a client per search share one instead.
	Gate *Gate
	// Logger receives one line per finished search describing when its replies
	// arrived. The zero value discards, so a caller with nothing to say about a
	// search need not supply one.
	Logger zerolog.Logger
}

// Gate admits one search per slot and makes the rest wait.
//
// It is a value the caller holds rather than state inside a client, because a
// client is built afresh from the stored settings for each search: a bound each
// client kept to itself would be no bound at all.
//
// creation is the second, stricter bound: slskd's searches controller holds a
// static SemaphoreSlim(1, 1) and answers 429 to any creation arriving while
// another is still being started — one concurrent creation, no queueing,
// however many searches are collecting replies. Slots bound the reply windows,
// which slskd runs happily in parallel; creation serializes the knocks on the
// one door that refuses a crowd.
//
// interval is the third bound, and the first that is about rate rather than
// concurrency. Both of the others are satisfied by creations arriving one after
// another as fast as slskd will take them, which is exactly what the Soulseek
// server bans this account for. It is kept here, on the one thing every lane
// shares, because it has to hold across release runs, wants and manual searches
// together — and because the rungs of one want's phrase ladder, the tightest
// burst of the lot, pass through here like everything else.
//
// repeat is the fourth, and bounds asking the same thing rather than asking
// quickly. See defaultRepeatWindow for why the two are not the same bound and
// why neither covers the other.
//
// The run of searches Soulseek did not answer is here for the same reason, and
// with more of a claim on it than anything else: one empty search says nothing,
// and a client that remembers one search cannot count them. See
// silentWindowsBeforePause.
//
// What the gate knows about the Soulseek connection is here for the same reason
// every bound is: a client is built for each search and remembers nothing, so a
// connection read once would be read again by the next search and every one
// after it. It is not a bound, but it is shared, and it is what lets a search be
// refused before it is created rather than explained after it came back empty.
// Settings that change under a running process point the same gate at a
// different slskd; a reading that goes stale that way is thirty seconds old at
// most and costs the searches inside it, no more.
//
// A reading that is wrong for longer is a real cost and worth stating plainly:
// an slskd whose /server never says "LoggedIn" while it is perfectly well
// connected would have every search refused, over and over, for as long as that
// lasted. It is the same sentence CheckConnection reads, so such a build already
// reports itself unusable on the settings page rather than searching quietly —
// but this is where it would stop searching, and that is what to look at first
// if searching stops without slskd complaining.
type Gate struct {
	slots    chan struct{}
	creation chan struct{}
	interval time.Duration
	repeat   time.Duration
	// mu guards everything below it, which is read and written by whichever
	// search reaches it at the time.
	mu   sync.Mutex
	last time.Time
	// asked is what has been put to Soulseek lately, by the words it was put in.
	// forget keeps it to the window, and the window times the interval is what
	// bounds it: at the defaults no more than four minutes of creations five
	// seconds apart, which is forty-eight phrases.
	asked map[string]askedText
	seen  connectionSeen
	queue searchQueueSeen
	// silence is the run of searches that reached Soulseek and heard nobody,
	// and the pause it earned. See silentWindowsBeforePause.
	silence silenceSeen
	// reconnects counts the times slskd has been seen signing back in to
	// Soulseek. The pause reads it and nothing else does: a run of unanswered
	// searches is about the connection they ran over, and a connection that
	// went down and came back is not that one.
	reconnects int
	// now is where the pause reads the time from. Only a test replaces it, the
	// way a test shrinks the client's dequeueBudget: the pause is half an hour
	// and the rest of the gate's clocks are seconds, so this is the one place
	// waiting the real duration out is not an option.
	now func() time.Time
}

// askedText is one phrase and what has become of it inside the window: when it
// was first put to Soulseek, and how many times since. Counting from the first
// ask rather than the last is what stops a phrase asked at the allowance from
// renewing its own window and being askable forever.
type askedText struct {
	first time.Time
	asks  int
}

// reservation is one search's place in a phrase's window: which phrase, and
// which window of it, because a phrase outlives its windows. A search can sit
// waiting for a slot longer than a window lasts, and a give-back that named the
// phrase alone would then take back an ask belonging to a search that really
// did go out — leaving the gate believing nobody had asked, which is the state
// this whole window exists to prevent.
//
// A repeated reservation is nobody's place and carries no window: it was
// refused, so there is nothing of it to hand back.
type reservation struct {
	text   string
	window time.Time
	// askable is how long until the phrase may be put to Soulseek again, and is
	// only meaningful when repeated is set.
	askable  time.Duration
	repeated bool
}

// connectionSeen is the last thing slskd said about its connection to the
// Soulseek network, and when it said it.
type connectionSeen struct {
	at       time.Time
	loggedIn bool
	reported string
}

// searchQueueSeen is the last count of searches slskd had accepted but not yet
// put on the Soulseek network, and when that count was read.
type searchQueueSeen struct {
	at     time.Time
	queued int
}

// silenceSeen is the run of searches that reached the Soulseek network and
// heard nobody, one after another, and the pause that run has earned.
//
// queries are the phrases the run was made of, so the line reporting the pause
// says what went unanswered rather than only how often. It is bounded by the
// run that starts the pause: nothing longer is worth reading.
//
// until is when searching resumes, and zero while it is going ahead. reconnects
// is the gate's count of sign-ins at the moment the pause began — a later one
// means this is a different connection and the run said nothing about it.
type silenceSeen struct {
	run        int
	queries    []string
	until      time.Time
	reconnects int
}

// NewGate returns a gate admitting concurrency searches at once, no two
// creations closer together than interval, and no phrase repeated inside
// repeat. Zero or less takes the default, any of them.
func NewGate(concurrency int, interval, repeat time.Duration) *Gate {
	if concurrency <= 0 {
		concurrency = defaultSearchConcurrency
	}
	if interval <= 0 {
		interval = defaultCreationInterval
	}
	if repeat <= 0 {
		repeat = defaultRepeatWindow
	}
	return &Gate{
		slots:    make(chan struct{}, concurrency),
		creation: make(chan struct{}, 1),
		interval: interval,
		repeat:   repeat,
		asked:    make(map[string]askedText),
		now:      time.Now,
	}
}

// reserve records that a phrase is about to be put to Soulseek, or refuses it
// as a repeat and says how long until it can be asked again.
//
// It is taken here rather than after slskd accepted the creation so that the
// refusal is free: a caller hears it before it queues for anything, which is
// what a want deep in a sweep needs and what keeps a phrase in its window from
// occupying a slot to be turned away in. What it costs is that a search which
// never reached the network has been counted as one that did, and unreserve is
// how that is given back.
func (gate *Gate) reserve(text string) reservation {
	now := time.Now()
	gate.mu.Lock()
	defer gate.mu.Unlock()
	gate.forget(now)

	asked := gate.asked[text]
	if asked.asks >= repeatAllowance {
		askable := gate.repeat - now.Sub(asked.first)
		if askable < 0 {
			askable = 0
		}
		return reservation{text: text, askable: askable, repeated: true}
	}
	if asked.asks == 0 {
		asked.first = now
	}
	asked.asks++
	gate.asked[text] = asked
	return reservation{text: text, window: asked.first}
}

// unreserve hands back an ask by a search that never became one. A phrase the
// network never heard is not a phrase this process repeated, and counting it as
// one would spend the allowance the deliberate second ask needs.
//
// Only the window the ask was taken from is given back to. A search can wait for
// a slot longer than a window lasts, and by the time it gives up the phrase may
// have been asked again by somebody else in a window of its own; that ask is
// real and is not this one's to withdraw.
//
// The window is left where it is when an ask remains. It was opened by a search
// that did go out, and moving it would let a phrase renew its own window by
// being given up on.
func (gate *Gate) unreserve(held reservation) {
	if held.repeated || held.window.IsZero() {
		return
	}
	gate.mu.Lock()
	defer gate.mu.Unlock()
	asked, seen := gate.asked[held.text]
	if !seen || !asked.first.Equal(held.window) {
		return
	}
	if asked.asks--; asked.asks <= 0 {
		delete(gate.asked, held.text)
		return
	}
	gate.asked[held.text] = asked
}

// forget drops the phrases whose window has passed, which is the whole of what
// keeps the memory of what was asked from growing: nothing is remembered longer
// than it is refused for, and a phrase is only remembered by having been
// admitted, which the interval already paces.
func (gate *Gate) forget(now time.Time) {
	for text, asked := range gate.asked {
		if now.Sub(asked.first) >= gate.repeat {
			delete(gate.asked, text)
		}
	}
}

// connection reports the last look at slskd's Soulseek connection, and whether
// it is recent enough to act on.
func (gate *Gate) connection() (connectionSeen, bool) {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if gate.seen.at.IsZero() || time.Since(gate.seen.at) >= connectionMemory {
		return connectionSeen{}, false
	}
	return gate.seen, true
}

// observeConnection writes down what was just learned about the connection, so
// the searches behind this one need not ask again.
func (gate *Gate) observeConnection(loggedIn bool, reported string) connectionSeen {
	seen := connectionSeen{at: time.Now(), loggedIn: loggedIn, reported: reported}
	gate.mu.Lock()
	defer gate.mu.Unlock()
	// An account that was signed out and is signed in again is a new connection
	// to Soulseek, and the Soulseek server hands a banned account a fresh half
	// hour rather than the remainder of the old one. So a sign-in is counted,
	// and the pause reads the count rather than this flag: what matters is that
	// the connection changed since the run of unanswered searches, not what it
	// happens to be right now.
	if !gate.seen.at.IsZero() && !gate.seen.loggedIn && loggedIn {
		gate.reconnects++
	}
	gate.seen = seen
	return seen
}

// observeSilence records one search that reached the Soulseek network, heard
// nobody, and had nothing else to explain it. It reports the run so far, and
// whether this search is the one that started the pause.
//
// The pause is put back to a full silencedRest by every silent search, so one
// still in flight when the pause began puts it back rather than being counted
// and forgotten. Only the search that started it is reported as starting it, so
// the line about it is written once instead of once per search behind it.
func (gate *Gate) observeSilence(text string) (silenceSeen, bool) {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	now := gate.now()
	gate.silence.run++
	if len(gate.silence.queries) < silentWindowsBeforePause {
		gate.silence.queries = append(gate.silence.queries, text)
	}
	if gate.silence.run < silentWindowsBeforePause {
		return gate.silence, false
	}
	started := gate.silence.until.IsZero()
	if started {
		gate.silence.reconnects = gate.reconnects
	}
	gate.silence.until = now.Add(silencedRest)
	return gate.silence, started
}

// breakSilence ends the run, and any pause it earned, because a peer answered a
// search. It reports what it ended, and whether that included a pause.
//
// A reply is the Soulseek server distributing this account's queries, which is
// the only thing the run ever stood in for. It can arrive during a pause: a
// search created before the pause began collects its replies afterwards.
func (gate *Gate) breakSilence() (silenceSeen, bool) {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	ended := gate.silence
	gate.silence = silenceSeen{}
	return ended, !ended.until.IsZero()
}

// silenced reports the pause searching is under: how long is left of it, and,
// where it has just finished, in which of its two ways.
//
// A pause that is over is ended here rather than by a clock of its own. Nothing
// runs while searching is paused, so the searches asking whether they may go
// are the only thing that ever looks at it.
func (gate *Gate) silenced() (held silenceSeen, remaining time.Duration, ended string) {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if gate.silence.until.IsZero() {
		return silenceSeen{}, 0, ""
	}
	held = gate.silence
	switch {
	case gate.reconnects != held.reconnects:
		ended = "slskd signed back in to Soulseek"
	case !gate.now().Before(held.until):
		ended = "the half hour passed"
	default:
		return held, held.until.Sub(gate.now()), ""
	}
	gate.silence = silenceSeen{}
	return held, 0, ended
}

// SilencedUntil reports when searching resumes, for a status page that wants to
// say why nothing is being looked for. It reads the pause without ending one:
// a caller that is not a search decides nothing about it.
func (gate *Gate) SilencedUntil() (time.Time, bool) {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if gate.silence.until.IsZero() || gate.reconnects != gate.silence.reconnects {
		return time.Time{}, false
	}
	if !gate.now().Before(gate.silence.until) {
		return time.Time{}, false
	}
	return gate.silence.until, true
}

// searchQueue reports the last look at slskd's outgoing search queue, and
// whether it is recent enough to make an admission decision from.
func (gate *Gate) searchQueue() (searchQueueSeen, bool) {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if gate.queue.at.IsZero() || time.Since(gate.queue.at) >= searchQueueMemory {
		return searchQueueSeen{}, false
	}
	return gate.queue, true
}

// observeSearchQueue shares one queue reading with the searches behind it.
func (gate *Gate) observeSearchQueue(queued int) searchQueueSeen {
	seen := searchQueueSeen{at: time.Now(), queued: queued}
	gate.mu.Lock()
	defer gate.mu.Unlock()
	gate.queue = seen
	return seen
}

// enter waits for a slot, or for the caller to give up.
func (gate *Gate) enter(ctx context.Context) error {
	select {
	case gate.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (gate *Gate) leave() { <-gate.slots }

// beginCreation takes the one creation slot and waits out whatever is left of
// the interval since the last creation left it, or lets the caller give up.
//
// The wait happens inside the slot rather than before it, so what is paced is
// the sequence of creations slskd actually sees. A caller that gives up during
// it hands the slot back: a search nobody is waiting for any more must not hold
// the door for the ones that are.
func (gate *Gate) beginCreation(ctx context.Context) error {
	select {
	case gate.creation <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := gate.pace(ctx); err != nil {
		<-gate.creation
		return err
	}
	return nil
}

// endCreation notes when this creation finished and hands the slot on. The next
// one is paced from here rather than from when this one started, so a creation
// slskd was slow to accept is not also asked to wait for it afterwards.
func (gate *Gate) endCreation() {
	gate.mu.Lock()
	gate.last = time.Now()
	gate.mu.Unlock()
	<-gate.creation
}

// cancelCreation hands back admission that never created anything. Unlike
// endCreation it does not advance the pacing clock: no request reached slskd.
func (gate *Gate) cancelCreation() { <-gate.creation }

func (gate *Gate) pace(ctx context.Context) error {
	gate.mu.Lock()
	last := gate.last
	gate.mu.Unlock()
	if last.IsZero() {
		return nil
	}
	wait := gate.interval - time.Since(last)
	if wait <= 0 {
		return nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func canFinish(ctx context.Context, budget time.Duration) bool {
	deadline, bounded := ctx.Deadline()
	return !bounded || time.Until(deadline) >= budget
}

type Client struct {
	baseURL       string
	apiKey        string
	httpClient    *http.Client
	searchTimeout time.Duration
	pollInterval  time.Duration
	// replyPlateau is what plateauFor makes of the configured search timeout,
	// held on the client so the tests can scale it down with the poll interval
	// and the budget rather than waiting out real seconds. Nothing outside this
	// package sets it.
	replyPlateau time.Duration
	// dequeueBudget is defaultDequeueBudget, held on the client the same way
	// replyPlateau is: so a test can shrink it rather than wait out real
	// seconds. It is not read from Options — it is about how long slskd's own
	// queue runs, not the peer reply window "Search timeout" configures, so it
	// does not scale with searchTimeout the way replyPlateau does.
	dequeueBudget time.Duration
	// completionGrace is defaultCompletionGrace, held on the client for the same
	// reason: a test shrinks it rather than waiting out two real minutes.
	completionGrace time.Duration
	gate            *Gate
	logger          zerolog.Logger
}

func NewClient(options Options) (*Client, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(options.BaseURL), "/")
	if baseURL == "" {
		return nil, errors.New("slskd base URL is required")
	}
	parsed, err := url.ParseRequestURI(baseURL)
	if err != nil {
		return nil, fmt.Errorf("parse slskd base URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("slskd base URL must use http or https")
	}
	if strings.TrimSpace(options.APIKey) == "" {
		return nil, errors.New("slskd API key is required")
	}

	client := &Client{
		baseURL:       baseURL,
		apiKey:        strings.TrimSpace(options.APIKey),
		httpClient:    options.HTTPClient,
		searchTimeout: options.SearchTimeout,
		pollInterval:  options.PollInterval,
		gate:          options.Gate,
		logger:        options.Logger,
	}
	if client.httpClient == nil {
		client.httpClient = &http.Client{Timeout: defaultHTTPTimeout}
	}
	if client.searchTimeout <= 0 {
		client.searchTimeout = defaultSearchTimeout
	}
	if client.pollInterval <= 0 {
		client.pollInterval = defaultPollInterval
	}
	// After the fallback, so a client built without a timeout gets the plateau
	// the default window was measured for.
	client.replyPlateau = plateauFor(client.searchTimeout)
	client.dequeueBudget = defaultDequeueBudget
	client.completionGrace = defaultCompletionGrace
	if client.gate == nil {
		client.gate = NewGate(defaultSearchConcurrency, defaultCreationInterval, defaultRepeatWindow)
	}
	return client, nil
}

func (client *Client) Name() string { return Name }

// SearchBudget is the whole context Search gives one phrase after it enters the
// shared gate: the wait for slskd to take the search out of its own queue, the
// Soulseek reply window, the wait for slskd to finish with a search whose window
// closed early, and the HTTP allowance for opening and reading the search.
// internal/downloads sizes a whole ladder walk off this, so a change here
// changes how long a want's search is allowed to run for, not only how long one
// rung's search is.
func (client *Client) SearchBudget() time.Duration {
	return client.dequeueBudget + client.searchTimeout + client.completionGrace + defaultHTTPTimeout
}

// plateauFor is how long a search that peers have answered waits for one more
// reply, for a reply window of this width.
//
// The measurement behind defaultReplyPlateau was taken on twenty-second
// windows, and eight seconds is what it settled on there. The share is what
// carries to other widths, not the eight seconds: a window twice as wide is a
// user saying peers here answer late, so closing it at the same early moment
// would discard exactly what widening it was for. Two fifths of the window
// reproduces the measured number at twenty seconds and stretches with the
// setting above it.
//
// Eight seconds is also the floor, so narrowing the window never makes Schall
// quicker to call a reply the last one. A five-second window keeps the whole
// eight, which simply means it has no plateau to reach and waits itself out —
// the same thing a search nobody answered does.
func plateauFor(searchTimeout time.Duration) time.Duration {
	return max(defaultReplyPlateau, searchTimeout*plateauNumerator/plateauDenominator)
}

// CheckConnection reports whether slskd is reachable, authenticated, and
// logged in to the Soulseek network. Failures carry an actionable message
// rather than a transport error.
func (client *Client) CheckConnection(ctx context.Context) (sources.Status, error) {
	var payload applicationResponse
	if err := client.get(ctx, "/application", nil, &payload); err != nil {
		return sources.Status{}, client.describe(ctx, err)
	}

	// The detail is read on the settings page, so it is a sentence in plain
	// words rather than the provider's own vocabulary. slskd reports its
	// version as "0.26.0 (0.26.0+e42a525d)" — the build repeated with its hash —
	// and its state as a flag list like "Connected, LoggedIn"; shown raw, both
	// read as a log line. The state still travels verbatim in the error below,
	// because a person debugging a refusal wants slskd's own words.
	version := payload.Version.Value
	if short, _, cut := strings.Cut(version, " ("); cut {
		version = short
	}
	if version == "" {
		version = "unknown version"
	}
	state := strings.TrimSpace(payload.Server.State)
	if state == "" {
		state = "unknown"
	}
	detail := "slskd " + version
	if !loggedInState(state) {
		// A state slskd never reported is silence, not a refusal: the detail
		// admits the unknown rather than claiming a sign-out nobody saw.
		if state == "unknown" {
			detail += " — reachable, Soulseek connection unknown"
		} else {
			detail += " — reachable, not signed in to Soulseek"
		}
		return sources.Status{Detail: detail}, fmt.Errorf(
			"slskd is reachable but not logged in to Soulseek (state %q); check the Soulseek credentials in slskd", state)
	}
	detail += " — connected to Soulseek"
	if username := strings.TrimSpace(payload.Server.Username); username != "" {
		detail += " as " + username
	}
	return sources.Status{Detail: detail}, nil
}

// Search runs one bounded slskd search and returns ranked candidates. The
// search is removed from slskd once slskd has finished with it, so repeated
// inspection does not accumulate state.
func (client *Client) Search(ctx context.Context, query sources.Query) ([]sources.Candidate, error) {
	// Normalized here as well as by the caller, because punctuation reaching
	// Soulseek silently returns nothing rather than failing.
	text := sources.NormalizeQueryText(query.Text)
	if text == "" {
		return nil, errors.New("search text is required")
	}

	// Asked before the phrase is reserved, because a search that is not going to
	// be made must not spend the phrase's allowance finding that out.
	if err := client.confirmNotSilenced(ctx); err != nil {
		return nil, err
	}

	// Asked before anything is waited for, because a phrase that may not be
	// repeated may not be repeated in five minutes' time either, and the caller
	// is owed the refusal now. It is backpressure and not an answer, for the
	// reason every refusal here is: nothing was learned about the music, so no
	// attempt may be spent on it and no absence recorded from it.
	held := client.gate.reserve(text)
	if held.repeated {
		client.logger.Warn().Str("query", text).Dur("askable_in", held.askable).
			Msg("a search was not repeated, because Soulseek bans this account for repeating one")
		return nil, fmt.Errorf(
			"%w: %q has been searched for %d times in the last %s and Soulseek bans this account "+
				"for repeating a search, so it will be asked again in %s",
			sources.ErrPhraseHeldBack, text, repeatAllowance,
			client.gate.repeat.Round(time.Second), held.askable.Round(time.Second))
	}

	// Both shared gate stages are entered before affordability is checked again.
	// Their wait cannot be predicted: other searches may already hold every slot
	// or the one creation door. Charging a guessed duration would recreate the
	// fixed-budget defect one layer down, so the wait spends the caller's
	// headroom and the full search allowance is checked once the gate is ours.
	if err := client.gate.enter(ctx); err != nil {
		client.gate.unreserve(held)
		return nil, err
	}
	// Handed back once, wherever the search gets to. settleWindow gives it up
	// before its wait rather than at the end, so waiting on slskd does not keep
	// the next search out of a slot.
	slot := sync.OnceFunc(client.gate.leave)
	defer slot()
	if err := client.gate.beginCreation(ctx); err != nil {
		client.gate.unreserve(held)
		return nil, err
	}
	if !canFinish(ctx, client.SearchBudget()) {
		client.gate.cancelCreation()
		client.gate.unreserve(held)
		return nil, sources.ErrSearchBudgetExhausted
	}

	// The caller's own deadline, kept aside from the search budget below: asking
	// whether there was anybody to answer this search is not part of the search,
	// and must not be asked with what is left of a budget the search has spent.
	caller := ctx
	ctx, cancel := context.WithTimeout(ctx, client.SearchBudget())
	defer cancel()

	searchID := uuid.New().String()
	err := func() error {
		defer client.gate.endCreation()
		return client.startSearch(ctx, searchID, text)
	}()
	if err != nil {
		if !reachedSoulseek(err) {
			client.gate.unreserve(held)
		}
		return nil, err
	}
	// Whether slskd is done with the search is what decides its removal, and the
	// answer is not known until the polling below has run — so the flag is set
	// where it is learned and read where the cleanup happens. See removeSearch
	// for what deleting a running one costs.
	finished := false
	defer func() { client.removeSearch(searchID, finished) }()

	window, err := client.awaitCompletion(ctx, searchID)
	if err != nil {
		finished = isComplete(window.state)
		return nil, err
	}

	// A caller that gave up gets its own error. Everything below reads the
	// window as an answer of some kind, and a window nobody finished watching is
	// not one.
	if err := caller.Err(); err != nil {
		return nil, err
	}

	// A window Schall closed itself leaves slskd still running the search, and
	// slskd hands over a search's replies only once it has finished with it. So
	// a window that peers answered is asked to finish and waited out. A window
	// nobody answered is left exactly where it was: it has no replies to lose,
	// and stopping it would rewrite the state confirmAsked reads below.
	if !isComplete(window.state) && window.replied() {
		slot()
		client.settleWindow(ctx, searchID, &window)
	}
	finished = isComplete(window.state)

	var responses []searchResponse
	switch {
	case isComplete(window.state):
		responses, err = client.readResponses(ctx, searchID, &window)
		if err != nil {
			return nil, client.describe(ctx, err)
		}
	case window.replied():
		// Peers answered and slskd is still holding what they said. Asking for
		// the replies now would get the empty list slskd answers a running
		// search with, and reading that as nobody sharing the music is the false
		// absence this whole path exists to stop. The window is reported anyway.
		window.report(client.logger, searchID, text, client.searchTimeout, 0)
		if err := caller.Err(); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf(
			"%w: %d peers answered, and slskd was still running the search (%s) %s after it was "+
				"asked to finish, so it had not stored what they said",
			sources.ErrRepliesUnread, window.replies, window.state, client.completionGrace)
	}
	// Ranked before it is capped, never after. A popular release answers with
	// far more folders than anyone can read, and cutting them in the order
	// slskd happened to return them threw away candidates that had not been
	// corroborated or scored yet — including, on a busy search, the best one.
	ranked := sources.Rank(client.toCandidates(responses), query)
	if len(ranked) > maxCandidates {
		ranked = ranked[:maxCandidates]
	}
	window.report(client.logger, searchID, text, client.searchTimeout, len(ranked))

	// A search nobody replied to is only an answer if there was somebody there to
	// reply. One peer answering settles that on its own, so this is asked only of
	// the searches that came back empty — and it is asked with the caller's own
	// deadline, because the search budget is gone by now.
	if len(responses) == 0 {
		if window.replied() {
			client.gate.observeConnection(true, "peers answered a search")
			if ended, paused := client.gate.breakSilence(); paused {
				client.logger.Info().Int("silent_searches", ended.run).
					Strs("silent_queries", ended.queries).
					Str("ended_by", "a peer answered a search").
					Msg("searching resumed after the Soulseek network stopped answering this account")
			}
			client.logger.Warn().Str("search_id", searchID).
				Str("query", text).
				Int("replies", window.replies).
				Str("state", window.state).
				Msg("peers answered, but slskd stored none of their replies")
			return nil, fmt.Errorf(
				"%w: %d peers answered, but slskd stored none of their replies",
				sources.ErrRepliesUnread, window.replies)
		}
		if err := client.confirmAsked(caller, searchID, text, window.state, window.cancelAsked); err != nil {
			return nil, err
		}
		client.noteSilence(caller, searchID, text, window.state, window.cancelAsked)
		return ranked, nil
	}
	// A peer answering is the connection being there, known for free and better
	// than /server could say it: the searches behind this one need not spend a
	// request finding out what this one just proved.
	client.gate.observeConnection(true, "peers answered a search")
	if ended, paused := client.gate.breakSilence(); paused {
		client.logger.Info().Int("silent_searches", ended.run).
			Strs("silent_queries", ended.queries).
			Str("ended_by", "a peer answered a search").
			Msg("searching resumed after the Soulseek network stopped answering this account")
	}
	return ranked, nil
}

// noteSilence counts one search that reached the Soulseek network and came back
// with nothing, and stops searching once enough of them have happened in a row.
//
// This is the ban confirmAsked cannot see: the connection is up, slskd is
// accepting searches, and the Soulseek server is dropping this account's
// queries. One such search is the ordinary quiet search Soulseek is full of, and
// nothing here changes what one of them means. What is read is the run — see
// silentWindowsBeforePause for the day it was measured on and the margin
// between it and ordinary quiet.
//
// The two states that reached nobody are checked again rather than assumed.
// confirmAsked has already refused both by the time this runs, so neither
// branch is reachable today; the rule that only a search Soulseek actually
// heard may count towards a ban belongs here, where the counting is, and not in
// the order two functions happen to be called in.
func (client *Client) noteSilence(
	ctx context.Context, searchID, text, state string, cancelAsked bool,
) {
	if _, gaveUp := abandoned(state, cancelAsked); gaveUp || neverAsked(state) {
		return
	}
	held, started := client.gate.observeSilence(text)
	if !started {
		return
	}
	client.logger.Warn().Str("search_id", searchID).
		Int("silent_searches", held.run).
		Strs("silent_queries", held.queries).
		Dur("paused_for", silencedRest).
		Str("notice", client.readBanNotice(ctx)).
		Msg("searching paused, because the Soulseek network stopped answering this account")
}

// confirmNotSilenced refuses a search while the Soulseek network is answering
// this account with silence, before there is a search to refuse.
//
// It is the same reading noteSilence took, put where it costs nothing: a search
// made during a ban collects nothing, and the empty list it comes back with is
// recorded as an absence about music that is there. The refusal spends no
// attempt and records nothing, so a want stands back half an hour and comes
// back exactly where it was.
//
// Two things end the pause early, and both are the network answering rather
// than a clock: a peer replying to a search created before the pause began (see
// breakSilence), and the account signing back in. The second is why the
// connection is read here at all — while every search is being refused, nothing
// else is looking at /server to notice a reconnect. It costs no request per
// search: what slskd said last is shared on the gate for connectionMemory,
// exactly as it is on the creation path.
func (client *Client) confirmNotSilenced(ctx context.Context) error {
	held, remaining, ended := client.gate.silenced()
	if remaining > 0 {
		if _, fresh := client.gate.connection(); !fresh {
			client.readConnection(ctx)
			held, remaining, ended = client.gate.silenced()
		}
	}
	if ended != "" {
		client.logger.Info().Int("silent_searches", held.run).
			Strs("silent_queries", held.queries).
			Str("ended_by", ended).
			Msg("searching resumed after the Soulseek network stopped answering this account")
	}
	if remaining <= 0 {
		return nil
	}
	client.logger.Debug().Int("silent_searches", held.run).
		Dur("askable_in", remaining).
		Msg("a search was not made, because the Soulseek network is not answering this account")
	return fmt.Errorf(
		"%w: %d searches in a row reached the Soulseek network and collected no reply, "+
			"which is how a thirty-minute ban looks from here; searching resumes in %s",
		sources.ErrSearchSilenced, held.run, remaining.Round(time.Second))
}

// readBanNotice reads slskd's conversation with the Soulseek server for the
// message that would explain the silence, and says in one phrase what it found.
//
// It decides nothing. The pause is already on by the time this runs, and stays
// on whatever this says: the notice arrived eleven minutes after the replies
// stopped on 2026-09-04, so a pause that waited for it would be waiting out most
// of the ban. What it buys is a log line that says whether the server said why,
// which is the difference between a ban and an slskd that has stopped
// distributing searches for some other reason.
//
// It is read once, when the pause starts. A build that does not serve the
// endpoint, or a conversation slskd has never held, is silence and reported as
// such.
func (client *Client) readBanNotice(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, defaultHTTPTimeout)
	defer cancel()
	var conversation conversationResponse
	if err := client.get(ctx, "/conversations/server", nil, &conversation); err != nil {
		client.logger.Debug().Err(err).
			Msg("could not read what the Soulseek server had said to slskd")
		return "slskd would not say what the Soulseek server had told it"
	}
	for _, message := range conversation.Messages {
		if !strings.Contains(strings.ToLower(message.Message), banNotice) {
			continue
		}
		// Only a notice inside the pause it would explain. The conversation
		// keeps every ban this account has ever been served, and the one from
		// February explains nothing about this afternoon.
		if since := time.Since(message.Timestamp); since >= 0 && since <= silencedRest {
			return "the Soulseek server said it had banned this account"
		}
	}
	return "the Soulseek server said nothing about a ban"
}

// confirmAsked reports an empty search as backpressure when it can be shown
// that nobody was asked, and says nothing when it cannot.
//
// A search the network never distributed and a search nobody was sharing the
// music for look identical from the results alone: slskd accepts the creation,
// opens the reply window, and hands back an empty list either way. The Soulseek
// server stops distributing our queries when it bans this account for thirty
// minutes — for asking too quickly, which is what the gate's interval is there
// to stop earning — and reading that silence as "nobody has it" records a false
// negative about music plenty of peers are sharing.
//
// Three things can show that nobody was asked, and they are not the same kind
// of nobody. slskd may have given up on the search itself, which it reports in
// the search's own finishing state; the search may never have gone out at all,
// which the same state reports in a different word (see neverAsked) — that one
// is slskd's own queue holding this search back, no wider than the search
// itself; and the connection the search would have gone out over may not have
// been there, which it reports at /server — that one is the account being
// signed out, and every search running at the same time fails the same way.
// Each is still backpressure for the reason the 409 on creation is: nothing was
// learned about the music, so no attempt may be spent and no answer recorded.
//
// It can only object, never admit. A slskd that would not answer the question,
// a build that does not serve /server, a state nobody reported: none of those
// are evidence of anything, and an empty search stays the ordinary empty search
// it has always been. Nor does this see every refusal. A ban the Soulseek server
// serves while leaving the connection up is invisible to every question asked
// here, because slskd answers all of them the way it answers them on a good day;
// that one is read from a run of empty searches rather than from any one of
// them, by noteSilence.
func (client *Client) confirmAsked(
	ctx context.Context, searchID, text, state string, cancelAsked bool,
) error {
	if gaveUp, ended := abandoned(state, cancelAsked); ended {
		client.logger.Warn().Str("search_id", searchID).Str("query", text).
			Str("state", state).
			Msg("a search that was abandoned collected nothing, which is not an answer")
		return fmt.Errorf("%w: slskd %s the search before anybody answered it",
			sources.ErrProviderUnavailable, gaveUp)
	}

	if neverAsked(state) {
		client.logger.Warn().Str("search_id", searchID).Str("query", text).
			Str("state", state).
			Msg("a search that never went out collected nothing, which is not an answer")
		return fmt.Errorf(
			"%w: slskd was still waiting to send the search (%s) when its window closed",
			sources.ErrNobodyWasAsked, state)
	}

	server, read := client.readConnection(ctx)
	if !read {
		client.logger.Debug().Str("search_id", searchID).
			Msg("could not read what the Soulseek connection was doing behind an empty search")
		return nil
	}
	if server.loggedIn {
		return nil
	}
	client.logger.Warn().Str("search_id", searchID).Str("query", text).
		Str("server_state", server.reported).
		Msg("a search collected nothing while slskd was not logged in to Soulseek")
	return fmt.Errorf("%w while the search ran (%s), so nobody was asked",
		sources.ErrNotLoggedIn, server.reported)
}

// confirmConnected refuses a search slskd has no connection to put it on, before
// there is a search to refuse.
//
// The same reading as confirmAsked, moved to the only side of the search where
// it costs nothing. Read afterwards it saves an answer from being believed;
// read beforehand it saves the search from being made at all, which is what
// matters while the Soulseek server is refusing this account: the afternoon that
// prompted this had slskd log eleven connection attempts in one minute and
// dozens of searches thrown straight back as "the server connection must be
// connected and logged in to perform a search", every one of them a knock on a
// door that had just been shut in our face.
//
// It costs no request per search. What slskd said last is kept on the gate for
// connectionMemory, so a run of searches asks once between them, and while the
// connection is down every search after the first is refused without touching
// the network at all.
//
// Like confirmAsked it may only ever object. A /server that cannot be read, a
// build that does not serve it, a state nobody reported: none of those are
// evidence, and the search goes ahead exactly as it did before this existed.
func (client *Client) confirmConnected(ctx context.Context) error {
	seen, fresh := client.gate.connection()
	if !fresh {
		var read bool
		if seen, read = client.readConnection(ctx); !read {
			return nil
		}
	}
	if seen.loggedIn {
		return nil
	}
	client.logger.Warn().Str("server_state", seen.reported).
		Msg("a search was not created, because slskd is not logged in to Soulseek")
	return fmt.Errorf(
		"%w: slskd is not logged in to the Soulseek network (%s), so a search would reach nobody; "+
			"it will be asked again once slskd is logged back in",
		sources.ErrSignedOut, seen.reported)
}

// readConnection asks slskd what its Soulseek connection is doing and writes the
// answer down for the searches behind this one. A question slskd would not
// answer is reported as unanswered rather than as bad news, and nothing is
// remembered from it: silence is not a no here any more than anywhere else.
func (client *Client) readConnection(ctx context.Context) (connectionSeen, bool) {
	ctx, cancel := context.WithTimeout(ctx, defaultHTTPTimeout)
	defer cancel()
	var server serverStateResponse
	if err := client.get(ctx, "/server", nil, &server); err != nil {
		client.logger.Debug().Err(err).
			Msg("could not read what slskd's Soulseek connection was doing")
		return connectionSeen{}, false
	}
	return client.gate.observeConnection(server.loggedIn(), server.reported()), true
}

// startSearch creates the search after Search has taken the gate's creation
// admission. A creation that is refused anyway — one made from slskd's own
// interface holds the same one-slot semaphore — is retried once after a short
// wait; a second refusal is reported as the backpressure it is.
//
// The search keeps its id across the retry: a 429 is returned before slskd
// creates anything, so there is nothing for the same id to collide with.
func (client *Client) startSearch(ctx context.Context, searchID, text string) error {
	// Asked inside the creation slot, which is where one question at a time is
	// already the rule: the searches queued behind this one neither ask it
	// again nor race it to the answer, and the one that does ask writes down
	// what it heard for all of them.
	if err := client.confirmConnected(ctx); err != nil {
		return err
	}
	if err := client.confirmSearchCapacity(ctx); err != nil {
		return err
	}

	for attempt := 1; ; attempt++ {
		err := client.describe(ctx, client.post(ctx, "/searches", searchRequest{
			ID: searchID, SearchText: text,
			SearchTimeout: client.searchTimeout.Milliseconds(),
		}, nil))
		if err == nil || !errors.Is(err, sources.ErrRateLimited) || attempt >= creationAttempts {
			return err
		}
		timer := time.NewTimer(creationRetryDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// confirmSearchCapacity refuses to add another search while slskd is already
// holding a full Schall-sized set in its outgoing queue.
//
// This is deliberately a preflight rather than another, longer dequeue wait.
// Before it existed Schall admitted one search every five seconds while slskd
// completed about six a minute, growing 172 queued searches and leaving every
// acquisition worker waiting behind work whose caller had already moved on.
// The dequeue wait still gives an admitted search its own fair window; this
// check prevents new searches from making an existing queue longer.
//
// Like the connection checks, this may only object to a state slskd actually
// reported. An older build that cannot list searches, or a temporary failure
// reading the list, leaves creation exactly as it was.
func (client *Client) confirmSearchCapacity(ctx context.Context) error {
	seen, fresh := client.gate.searchQueue()
	if !fresh {
		ctx, cancel := context.WithTimeout(ctx, defaultHTTPTimeout)
		defer cancel()

		var searches []searchStateResponse
		if err := client.get(ctx, "/searches", nil, &searches); err != nil {
			client.logger.Debug().Err(err).
				Msg("could not read slskd's outgoing search queue")
			return nil
		}
		queued := 0
		for _, search := range searches {
			if neverAsked(search.State) {
				queued++
			}
		}
		seen = client.gate.observeSearchQueue(queued)
	}
	if seen.queued < maxQueuedSearches {
		return nil
	}
	client.logger.Warn().Int("queued_searches", seen.queued).
		Int("queue_limit", maxQueuedSearches).
		Msg("a search was not created, because slskd's outgoing queue is full")
	return fmt.Errorf(
		"%w: slskd already has %d searches waiting to reach the Soulseek network; "+
			"Schall will ask again after that queue drains",
		sources.ErrProviderUnavailable, seen.queued)
}

// reachedSoulseek reads a failed creation for whether the phrase went out over
// the network in spite of it, which is what decides whether the phrase counts
// as asked.
//
// Three failures are known not to have asked anybody. A caller that gave up
// never got as far as the door; slskd's 429 is refused before it creates
// anything; and its 409, like the connection check above it, is the network
// being unreachable from slskd's own side. Everything else is a failure of
// unknown shape, and an unknown counts as asked: the cost of that is one phrase
// waiting out a window it need not have, and the cost of the other way round is
// a repeat that believed it was the first.
func reachedSoulseek(err error) bool {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return false
	case errors.Is(err, sources.ErrRateLimited), errors.Is(err, sources.ErrProviderUnavailable):
		return false
	}
	return true
}

// awaitCompletion polls until slskd reports the search finished, the replies
// stop arriving, or the search budget expires. A search that ran out of time
// still has usable responses, so neither ending is an error.
//
// It runs in two phases, and the second does not start until the first is
// over. The first waits out neverAsked(state): slskd holding the search in its
// own queue, not yet put to the Soulseek network. defaultDequeueBudget bounds
// that phase alone, because it measures something different from the reply
// window below it — how long slskd's queue runs, not how long peers take to
// answer — and counting the two against one budget is the defect this
// replaces: a search still queued when the old fixed window closed was read as
// "nobody was asked" when nobody had been asked the question yet.
//
// Four things close the window, and the one that did is recorded so the next
// measurement can be read off the logs:
//
//   - "completed", which is slskd saying it is done with the search;
//   - "queued", which is the dequeue phase running out with the search still
//     queued — real backpressure from slskd's own queue, and confirmAsked
//     still reads it as sources.ErrNobodyWasAsked because neverAsked(state) is
//     still true;
//   - "plateau", which is at least one peer having answered and no new reply
//     having arrived for plateauFor(searchTimeout) — a share of this
//     installation's own reply window, never a fixed number of seconds; see
//     there for the measurement it rests on;
//   - "budget", which is the whole reply window having been spent once the
//     search left the queue.
//
// Completion is read before either budget, because a search slskd has finished
// with is finished whatever its replies or its queue were doing. A search
// cannot plateau while still queued: the plateau needs a peer to have
// answered, and nobody can answer a search that has not gone out yet.
//
// A plateau, or the dequeue phase running out, leaves the search exactly where
// the reply budget leaves it: slskd is still running it — its state is not a
// completed one — so Search hands it to the reaper that waits for it to end
// and removes it then. Deleting a running search is what removeSearch exists
// not to do.
//
// Every poll also says how many replies the search has accepted so far, so the
// requests that spend the window measure it as well. That measurement is what
// the plateau above was argued from, and it keeps running, so the next
// argument has numbers too.
func (client *Client) awaitCompletion(ctx context.Context, searchID string) (searchWindow, error) {
	var window searchWindow
	started := time.Now()
	dequeueDeadline := started.Add(client.dequeueBudget)
	queued := true
	var replyDeadline time.Time
	for {
		var payload searchStateResponse
		if err := client.get(ctx, "/searches/"+searchID, nil, &payload); err != nil {
			return window, client.describe(ctx, err)
		}
		window.observe(payload.ResponseCount, payload.State, time.Since(started))

		if queued && !neverAsked(payload.State) {
			// Left the queue just now. The reply window starts here, not at
			// search creation, so a search that sat queued for a while still
			// gets the full searchTimeout to collect replies afterwards.
			queued = false
			replyDeadline = time.Now().Add(client.searchTimeout)
		}

		switch {
		case isComplete(payload.State):
			window.closedBy = "completed"
			return window, nil
		case queued && !time.Now().Before(dequeueDeadline):
			window.closedBy = "queued"
			return window, nil
		case !queued && !time.Now().Before(replyDeadline):
			window.closedBy = "budget"
			return window, nil
		case !queued && window.replied() && window.quietFor() >= client.replyPlateau:
			window.closedBy = "plateau"
			return window, nil
		}
		timer := time.NewTimer(client.pollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			window.closedBy = "caller"
			return window, nil
		case <-timer.C:
		}
	}
}

// settleWindow asks slskd to finish a search Schall has stopped reading, and
// waits until slskd says it has.
//
// It exists because of where slskd keeps a search's replies. Until the search
// reaches a completed state, GET /searches/{id}/responses answers an empty list
// — measured on slskd 0.26.0 against three live searches whose responseCount
// was 218, 246 and 248. So the replies behind a window closed on a plateau, or
// on the reply budget, cannot be read until slskd is done with the search.
//
// PUT /searches/{id} stops a running search, which is what makes this wait
// short: slskd finishes the search and stores what it collected. A stop slskd
// would not take is not a failure here — the fallback is the wait that was
// going to happen anyway, and every one of the 491 searches Schall abandoned in
// the 2h22m sample finished on its own within two minutes.
//
// The polls here are left out of the window's reply curve. The curve measures
// how peers answered inside the window, which is what the plateau was argued
// from, and this wait is after the window closed.
func (client *Client) settleWindow(ctx context.Context, searchID string, window *searchWindow) {
	started := time.Now()
	defer func() { window.waitedToComplete = time.Since(started) }()

	window.cancelAsked = true
	window.stopped = client.stopSearch(ctx, searchID)
	deadline := started.Add(client.completionGrace)
	for {
		var payload searchStateResponse
		if err := client.get(ctx, "/searches/"+searchID, nil, &payload); err != nil {
			client.logger.Debug().Err(err).Str("search_id", searchID).
				Msg("slskd stopped saying what a search it had been asked to finish was doing")
			return
		}
		window.state = payload.State
		if isComplete(payload.State) || !time.Now().Before(deadline) {
			return
		}
		timer := time.NewTimer(client.pollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// stopSearch asks slskd to finish a search now and reports whether slskd took
// the request. A refusal is written down and nothing more: the caller waits for
// the search to end either way, and a slskd that will not stop a search says
// nothing about the music.
func (client *Client) stopSearch(ctx context.Context, searchID string) bool {
	ctx, cancel := context.WithTimeout(ctx, defaultHTTPTimeout)
	defer cancel()
	if err := client.put(ctx, "/searches/"+searchID); err != nil {
		client.logger.Debug().Err(err).Str("search_id", searchID).
			Msg("slskd would not stop a search Schall had finished reading")
		return false
	}
	return true
}

// searchWindow is what one search's polls saw: how many replies had arrived by
// each second of the window, when the last new one landed, and the state slskd
// finished in. Its resolution is one poll interval, which is the price of it
// costing no requests of its own.
//
// A reply is a peer answering, not a copy worth having: the curve says when the
// window stopped being useful, not which reply was eventually accepted.
type searchWindow struct {
	replies   int
	polls     int
	lastReply time.Duration
	elapsed   time.Duration
	state     string
	bySecond  []int
	// closedBy is what ended the window: "completed", "queued", "plateau",
	// "budget" or "caller". It is written in the log line so a later
	// measurement can count the plateaus without replaying the curves.
	closedBy string
	// cancelAsked is whether settleWindow asked slskd to finish the search.
	// stopped is whether slskd took the request, and waitedToComplete is how long
	// the wait for it to finish ran afterwards. Both stay zero for a window
	// slskd closed itself. responsesAfter is how long a counted but empty first
	// response read took to produce a non-empty list or reach the grace limit.
	cancelAsked      bool
	stopped          bool
	waitedToComplete time.Duration
	responsesAfter   time.Duration
}

// replied reports whether any peer answered this search at all. The plateau
// asks this first: a search nobody replied to has no last reply to be quiet
// since, and waits out its whole budget.
func (window searchWindow) replied() bool { return window.replies > 0 }

// quietFor is how long the window has heard nothing new since its last reply.
// Its resolution is one poll interval, the same as the curve's, because both
// are read from the polls the window already makes.
func (window searchWindow) quietFor() time.Duration {
	return window.elapsed - window.lastReply
}

// observe records one poll. bySecond[i] is how many replies were known to have
// arrived by second i of the window, carried forward across seconds no poll
// landed in, so the curve reads the same however the polls were spaced.
func (window *searchWindow) observe(replies int, state string, at time.Duration) {
	window.polls++
	window.elapsed = at
	window.state = state
	if replies > window.replies {
		window.replies = replies
		window.lastReply = at
	}

	second := int(at / time.Second)
	if second >= maxCurveSeconds {
		second = maxCurveSeconds - 1
	}
	for len(window.bySecond) <= second {
		carried := 0
		if filled := len(window.bySecond); filled > 0 {
			carried = window.bySecond[filled-1]
		}
		window.bySecond = append(window.bySecond, carried)
	}
	window.bySecond[second] = window.replies
}

// report writes the curve where it can be read without a shell on the database.
// A state that is not a completed one means the search was still running when
// Schall stopped watching, and closed_by says which of the two reasons stopped
// it: the replies had plateaued, or the budget ran out.
func (window searchWindow) report(
	logger zerolog.Logger, searchID, text string, budget time.Duration, candidates int,
) {
	logger.Info().
		Str("search_id", searchID).
		Str("query", text).
		Str("slskd_state", window.state).
		Str("closed_by", window.closedBy).
		Bool("stopped", window.stopped).
		Int64("wait_for_complete_ms", window.waitedToComplete.Milliseconds()).
		Int64("responses_after_ms", window.responsesAfter.Milliseconds()).
		Int("replies", window.replies).
		Int("candidates", candidates).
		Int("polls", window.polls).
		Dur("budget", budget).
		Dur("elapsed", window.elapsed).
		Dur("last_reply_at", window.lastReply).
		Ints("replies_by_second", window.bySecond).
		Msg("a search window closed")
}

func isComplete(state string) bool {
	state = strings.ToLower(state)
	return strings.Contains(state, "completed") || strings.Contains(state, "cancelled") ||
		strings.Contains(state, "errored")
}

// abandoned reports whether slskd stopped the search rather than finishing it,
// and in which of its own words. The states are flags and arrive combined —
// "Completed, Errored" is one string — so they are read as substrings, the way
// completion itself is.
//
// A cancellation Schall requested while settling a window is not slskd giving
// up. A cancellation with no such request remains an abandoned search.
//
// A search that timed out is not abandoned: the reply window is bounded on
// purpose, and reaching the end of it having heard nobody is the ordinary quiet
// search this must not be confused with.
func abandoned(state string, cancelAsked bool) (string, bool) {
	state = strings.ToLower(state)
	switch {
	case strings.Contains(state, "errored"):
		return "failed", true
	case strings.Contains(state, "cancelled"):
		if cancelAsked {
			return "", false
		}
		return "cancelled", true
	}
	return "", false
}

// neverAsked reports whether the search was still waiting to go out when its
// window closed, in slskd's own word for it.
//
// slskd is built on Soulseek.NET, and that library gives a search a named state
// at each step. "Requested" is set when the search is made, "Queued" while it
// waits for a search slot inside the client, and "InProgress" only after the
// request has been written to the Soulseek server. So a search still requested
// or queued at the end of its window was put to nobody: it never left this
// machine, and the empty list beside it is not an answer about who is sharing
// the music.
//
// Why a search sits there is not asked here and cannot be answered from here.
// The Soulseek server ignoring this account for thirty minutes looks like this,
// and so does slskd having more searches open than it will run at once. Both
// mean the same thing about the music, which is nothing.
//
// The states are flags and arrive combined, so they are read as substrings the
// way completion is. A search that completed is never this, whatever else its
// state says: it finished, and how it finished is abandoned's question.
//
// Only these two words are read, and nothing is inferred from the rest. A state
// slskd never reported, and a word this does not know, are silence rather than
// evidence, and an empty search stays the ordinary empty search it has always
// been.
//
// A window closed early on a plateau is never read here, and cannot be. The
// plateau needs a peer to have answered (searchWindow.replied), and confirmAsked
// is only reached when nobody did, so the two conditions exclude each other. A
// search nobody replied to waits out its whole budget, which is what leaves the
// state below the state it was still stuck in when the budget ran out.
func neverAsked(state string) bool {
	if isComplete(state) {
		return false
	}
	state = strings.ToLower(state)
	return strings.Contains(state, "queued") || strings.Contains(state, "requested")
}

// loggedInState reads slskd's compound connection state, which names the flags
// it is in: "Connected, LoggedIn" when it can search, "Disconnected" or
// "Connecting" when it cannot.
func loggedInState(state string) bool {
	return strings.Contains(
		strings.ToLower(strings.ReplaceAll(state, " ", "")), "loggedin")
}

// toCandidates groups each peer's files by containing folder, because a folder
// is the unit a user actually inspects and would eventually acquire.
func (client *Client) toCandidates(responses []searchResponse) []sources.Candidate {
	grouped := make(map[string]*sources.Candidate)
	order := make([]string, 0, len(responses))

	for _, response := range responses {
		username := strings.TrimSpace(response.Username)
		if username == "" {
			continue
		}
		for _, file := range response.Files {
			extension := sources.NormalizeExtension(file.Extension)
			if extension == "" {
				extension = sources.NormalizeExtension(file.Filename)
			}
			if !sources.AudioExtensions[extension] {
				continue
			}
			directory, name := splitRemotePath(file.Filename)
			key := username + "\x00" + directory
			candidate, ok := grouped[key]
			if !ok {
				candidate = &sources.Candidate{
					Provider:       Name,
					Username:       username,
					Directory:      directory,
					FreeUploadSlot: response.HasFreeUploadSlot,
					QueueLength:    response.QueueLength,
					UploadSpeed:    response.UploadSpeed,
				}
				grouped[key] = candidate
				order = append(order, key)
			}
			if len(candidate.Files) >= maxFilesPerFolder {
				continue
			}
			candidate.Files = append(candidate.Files, sources.File{
				Path:            file.Filename,
				Name:            name,
				Extension:       extension,
				SizeBytes:       file.Size,
				BitRate:         file.BitRate,
				DurationSeconds: file.Length,
				VariableBitRate: file.IsVariableBitRate,
			})
		}
	}

	candidates := make([]sources.Candidate, 0, len(order))
	for _, key := range order {
		candidate := grouped[key]
		candidate.Summarize()
		candidates = append(candidates, *candidate)
	}
	return candidates
}

// splitRemotePath separates a Soulseek path, which uses Windows separators far
// more often than not, into its folder and file name.
func splitRemotePath(value string) (string, string) {
	value = strings.TrimSpace(value)
	normalized := strings.ReplaceAll(value, "\\", "/")
	directory, name := path.Split(normalized)
	directory = strings.TrimSuffix(directory, "/")
	if name == "" {
		name = normalized
	}
	return directory, name
}

// removeSearch takes a finished search out of slskd, and hands one slskd has
// not finished with to a reaper that waits for it instead.
//
// The removal is housekeeping — it exists so that inspecting searches does not
// accumulate state — and housekeeping must never abort work. Deleting a search
// that is still running does exactly that: slskd's own writes then find the row
// gone, and it logs "Failed to execute search" and "Failed to finalize search"
// with a DbUpdateConcurrencyException, hundreds of times in an afternoon. The
// search dies mid-flight, Schall gets nothing back, and the want comes round to
// ask the same phrase again — which is how a cleanup turned into the repetition
// the Soulseek server bans this account for.
//
// It was fired on every return path, including the two where the search is
// still very much alive: a search that outran the reply-window budget, and a
// caller that gave up while it ran. Both are ordinary.
//
// A leaked search record is the far smaller problem, and slskd ages them out.
// So a search nobody could confirm was over is left exactly where it is. DELETE
// is not the way to end one: the production logs are the evidence that it
// removes the record without stopping the search behind it. Stopping a search
// cleanly is what settleWindow's PUT is for, and a search that took it reaches
// here finished.
func (client *Client) removeSearch(searchID string, finished bool) {
	if finished {
		client.deleteSearch(searchID)
		return
	}
	// Off this caller's time and outside the gate: the slot this search held is
	// handed back the moment it returns, and waiting for slskd to finish with a
	// search nobody is reading any more must not keep the next one out.
	go client.removeOnceFinished(searchID)
}

// removeOnceFinished waits for slskd to be done with a search and then removes
// it, giving up on the wait rather than on the search. A slskd that stops
// answering, a state that never becomes a finished one, a wait that outlasts
// its budget: each of them leaves the record behind, which is the outcome this
// is willing to have.
func (client *Client) removeOnceFinished(searchID string) {
	ctx, cancel := context.WithTimeout(context.Background(), reapBudget)
	defer cancel()
	for {
		var payload searchStateResponse
		if err := client.get(ctx, "/searches/"+searchID, nil, &payload); err != nil {
			client.logger.Debug().Err(err).Str("search_id", searchID).
				Msg("a search was left in slskd, which stopped saying what it was doing")
			return
		}
		if isComplete(payload.State) {
			client.deleteSearch(searchID)
			return
		}
		timer := time.NewTimer(client.pollInterval)
		select {
		case <-ctx.Done():
			client.logger.Debug().Str("search_id", searchID).Str("state", payload.State).
				Dur("waited", reapBudget).
				Msg("a search was left in slskd, which had not finished with it")
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// deleteSearch is the only way a finished search ever leaves slskd, and the
// error path here is silent to everything but the log: a failed DELETE grows
// the 961-row leak the live instance was found carrying, and logging the
// search ID and the error is the only way to watch it grow.
func (client *Client) deleteSearch(searchID string) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultHTTPTimeout)
	defer cancel()
	request, err := client.newRequest(ctx, http.MethodDelete, "/searches/"+searchID, nil, nil)
	if err != nil {
		client.logger.Warn().Err(err).Str("search_id", searchID).
			Msg("could not build the request to delete a finished search")
		return
	}
	response, err := client.httpClient.Do(request)
	if err != nil {
		client.logger.Warn().Err(err).Str("search_id", searchID).
			Msg("could not delete a finished search")
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	_ = response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		client.logger.Warn().Str("search_id", searchID).Int("status", response.StatusCode).
			Msg("slskd refused to delete a finished search")
	}
}

func (client *Client) get(ctx context.Context, endpoint string, parameters url.Values, destination any) error {
	request, err := client.newRequest(ctx, http.MethodGet, endpoint, parameters, nil)
	if err != nil {
		return err
	}
	return client.do(request, destination)
}

func (client *Client) post(ctx context.Context, endpoint string, body any, destination any) error {
	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode slskd request: %w", err)
	}
	request, err := client.newRequest(ctx, http.MethodPost, endpoint, nil, encoded)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	return client.do(request, destination)
}

// put sends a body-less PUT, which is the shape of the one verb slskd has for
// stopping a search it is running.
func (client *Client) put(ctx context.Context, endpoint string) error {
	request, err := client.newRequest(ctx, http.MethodPut, endpoint, nil, nil)
	if err != nil {
		return err
	}
	return client.do(request, nil)
}

func (client *Client) newRequest(ctx context.Context, method, endpoint string, parameters url.Values, body []byte) (*http.Request, error) {
	requestURL, err := url.Parse(client.baseURL + apiPrefix + endpoint)
	if err != nil {
		return nil, fmt.Errorf("build slskd URL: %w", err)
	}
	if len(parameters) > 0 {
		requestURL.RawQuery = parameters.Encode()
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, requestURL.String(), reader)
	if err != nil {
		return nil, fmt.Errorf("create slskd request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("X-API-Key", client.apiKey)
	return request, nil
}

func (client *Client) do(request *http.Request, destination any) error {
	response, err := client.httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return &HTTPError{StatusCode: response.StatusCode}
	}
	if destination == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBody))
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxResponseBody)).Decode(destination); err != nil {
		return fmt.Errorf("decode slskd response: %w", err)
	}
	return nil
}

// HTTPError is a non-success response from slskd.
type HTTPError struct {
	StatusCode int
}

func (err *HTTPError) Error() string {
	return fmt.Sprintf("slskd returned HTTP %d", err.StatusCode)
}

// describe turns a transport or status failure into a message a user can act
// on without reading the server log. A caller that gave up is propagated
// unchanged; a slskd that ran out of time is reported as such, because the
// two are indistinguishable from the error alone.
func (client *Client) describe(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return err
	}
	if errors.Is(err, context.DeadlineExceeded) || isTimeout(err) {
		return errors.New("slskd did not respond in time; check the base URL and that slskd is running")
	}
	var httpError *HTTPError
	if errors.As(err, &httpError) {
		switch httpError.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			return errors.New("slskd rejected the API key; check the key configured in slskd")
		case http.StatusNotFound:
			return errors.New("slskd did not recognise the API path; check the base URL and that slskd exposes /api/v0")
		case http.StatusTooManyRequests:
			// Concurrent searches are enough to trip this. It says nothing
			// about the release, so it must never settle a search.
			return fmt.Errorf("%w: slskd is refusing further searches for the moment",
				sources.ErrRateLimited)
		case http.StatusConflict:
			// slskd answers 409 when its own search call throws, and the one
			// thing that throws it is a Soulseek client that is not connected
			// or logged in. A search text it is already running answers 400,
			// so this is never a collision with another question — it is the
			// network being unreachable from slskd's side, which says nothing
			// whatever about the music.
			return fmt.Errorf("%w: slskd is running but is not connected to the Soulseek network",
				sources.ErrProviderUnavailable)
		case http.StatusServiceUnavailable, http.StatusBadGateway, http.StatusGatewayTimeout:
			return fmt.Errorf("%w: slskd is reachable but not ready; try again once it has started",
				sources.ErrProviderUnavailable)
		default:
			return err
		}
	}
	return fmt.Errorf("could not reach slskd: %w", err)
}

func isTimeout(err error) bool {
	var timeout interface{ Timeout() bool }
	return errors.As(err, &timeout) && timeout.Timeout()
}

type applicationResponse struct {
	Version applicationVersion `json:"version"`
	Server  struct {
		State    string `json:"state"`
		Address  string `json:"address"`
		Username string `json:"username"`
	} `json:"server"`
}

// applicationVersion tolerates both shapes slskd has used for the version
// field: a bare string, and the object real builds return, which carries the
// resolved version under full or current.
type applicationVersion struct {
	Value string
}

func (version *applicationVersion) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		version.Value = strings.TrimSpace(text)
		return nil
	}
	var object struct {
		Full    string `json:"full"`
		Current string `json:"current"`
	}
	if err := json.Unmarshal(data, &object); err != nil {
		return fmt.Errorf("decode slskd version: %w", err)
	}
	version.Value = strings.TrimSpace(object.Full)
	if version.Value == "" {
		version.Value = strings.TrimSpace(object.Current)
	}
	return nil
}

// serverStateResponse is slskd's view of its own connection to the Soulseek
// network, which is what a search would have gone out over.
//
// The flags are pointers because a build that does not send them must not read
// as false. An absent field is silence here as everywhere else, and silence
// taken for "not logged in" would refuse every empty search on installations
// that are perfectly well connected.
type serverStateResponse struct {
	Address     string `json:"address"`
	Username    string `json:"username"`
	State       string `json:"state"`
	IsConnected *bool  `json:"isConnected"`
	IsLoggedIn  *bool  `json:"isLoggedIn"`
}

// loggedIn reports whether slskd was in a state to put a query to the network.
// A state nobody reported and no flags to read leaves the question unanswered,
// which counts as logged in: this may only ever object.
func (server serverStateResponse) loggedIn() bool {
	if strings.TrimSpace(server.State) != "" {
		return loggedInState(server.State)
	}
	if server.IsLoggedIn != nil {
		return *server.IsLoggedIn
	}
	if server.IsConnected != nil {
		return *server.IsConnected
	}
	return true
}

// reported is what slskd called the state, for the sentence the want explains
// itself with.
func (server serverStateResponse) reported() string {
	if state := strings.TrimSpace(server.State); state != "" {
		return fmt.Sprintf("slskd reports %q", state)
	}
	return "slskd reports it is not logged in"
}

// conversationResponse is one chat conversation slskd holds. The Soulseek
// server's own messages arrive in the one named "server", which is where its ban
// notices land.
type conversationResponse struct {
	Username                   string                `json:"username"`
	UnAcknowledgedMessageCount int                   `json:"unAcknowledgedMessageCount"`
	Messages                   []conversationMessage `json:"messages"`
}

// conversationMessage is one message in that conversation.
//
// A ban notice reads the timestamp and the words alone: a notice this account
// sent to itself is not a shape the Soulseek server produces, so the direction
// settles nothing the phrase has not already settled. A peer's human check
// reads the rest — the identifier names the message to mark read, and the
// acknowledgement is what tells a new question from one already answered.
type conversationMessage struct {
	ID             int64     `json:"id"`
	Timestamp      time.Time `json:"timestamp"`
	Direction      string    `json:"direction"`
	Message        string    `json:"message"`
	IsAcknowledged bool      `json:"isAcknowledged"`
}

// searchStateResponse is slskd's view of a running search. responseCount is the
// number of replies it has accepted so far — slskd writes it as they arrive,
// rate limited to a few times a second, so polling it is a reply-arrival curve
// for no requests beyond the ones awaitCompletion already makes.
// searchRequest is the body of POST /searches.
//
// SearchTimeout is milliseconds, and it is the reply window Schall itself
// watches for. Sending it is what makes slskd finish the search while Schall is
// still there to read it: slskd's own default is longer than Schall's window,
// and a search slskd has not finished has no responses to hand over. The rest
// of the fields slskd accepts — responseLimit, fileLimit, the peer filters —
// are left at slskd's own settings, because each of them decides which replies
// are kept and none of them is Schall's decision to make here.
type searchRequest struct {
	ID            string `json:"id"`
	SearchText    string `json:"searchText"`
	SearchTimeout int64  `json:"searchTimeout"`
}

type searchStateResponse struct {
	ID            string `json:"id"`
	State         string `json:"state"`
	ResponseCount int    `json:"responseCount"`
}

type searchResponse struct {
	Username          string `json:"username"`
	HasFreeUploadSlot bool   `json:"hasFreeUploadSlot"`
	QueueLength       int    `json:"queueLength"`
	UploadSpeed       int64  `json:"uploadSpeed"`
	Files             []struct {
		Filename          string `json:"filename"`
		Extension         string `json:"extension"`
		Size              int64  `json:"size"`
		BitRate           int    `json:"bitRate"`
		Length            int    `json:"length"`
		IsVariableBitRate bool   `json:"isVariableBitRate"`
	} `json:"files"`
}

// readResponses reads a completed search and gives slskd a short chance to
// finish storing replies it counted before the first read. An empty search has
// no replies to wait for, so it is read once and left for confirmAsked.
func (client *Client) readResponses(
	ctx context.Context, searchID string, window *searchWindow,
) ([]searchResponse, error) {
	var responses []searchResponse
	if err := client.get(ctx, "/searches/"+searchID+"/responses", nil, &responses); err != nil {
		return nil, err
	}
	if len(responses) > 0 || !window.replied() {
		return responses, nil
	}

	started := time.Now()
	defer func() { window.responsesAfter = time.Since(started) }()
	deadline := started.Add(client.completionGrace)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return responses, nil
		}
		wait := client.pollInterval
		if remaining < wait {
			wait = remaining
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return responses, ctx.Err()
		case <-timer.C:
		}

		responses = nil
		if err := client.get(ctx, "/searches/"+searchID+"/responses", nil, &responses); err != nil {
			return nil, err
		}
		if len(responses) > 0 {
			return responses, nil
		}
	}
}
