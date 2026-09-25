// Package recommendations reads the external source Schall takes suggestions
// from, as the stored settings describe it. It also runs Schall's own engine
// over the listens already copied (own.go, ADR 0039).
//
// It is the only thing in the tree that builds a ListenBrainz client, and it
// builds one per use from the settings row rather than holding one: an account
// changed in the interface takes effect on the next call rather than on the next
// restart. What comes back is identifiers and the material a reason is written
// from — nothing here decides what may be shown, which is the suppression
// engine's job against Schall's own tables (ADR 0005).
package recommendations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/listenbrainz"
	"github.com/pxldi/schall/internal/musicbrainz"
	"github.com/rs/zerolog"
)

// ErrNotConfigured means nobody has named a ListenBrainz account yet. It is
// what every path through this service refuses with when there is no settings
// row, so that "the source was never set up" is never reported as "the source
// had nothing to say" (ADR 0002).
var ErrNotConfigured = errors.New("ListenBrainz is not configured")

// ErrDisabled means the account remains stored but automatic recommendation
// reads are switched off. Connection checks deliberately ignore this setting;
// sweeps do not.
var ErrDisabled = errors.New("ListenBrainz recommendations are disabled")

// ErrCheckSuperseded means the settings row moved while the check was running,
// so the verdict is about an account that is no longer the configured one.
//
// It is reported rather than swallowed: somebody who changed the account while a
// check was in flight is owed "that check no longer applies", not a status line
// that silently stayed as it was.
var ErrCheckSuperseded = errors.New("the ListenBrainz account changed while the check was running")

// Store is the persistence the recommendation source and suppression engine
// need. Provider answers, user decisions and impression fatigue remain separate
// tables even though this service composes them for one list (ADR 0005).
type Store interface {
	ListenBrainzSettings(context.Context) (db.ListenBrainzSettingsRow, error)
	RecordListenBrainzConnection(context.Context, db.RecordListenBrainzConnectionParams) (db.ListenBrainzSettingsRow, error)
	ReplaceRecommendationSnapshot(context.Context, db.UpsertRecommendationSnapshotParams, []db.InsertRecommendationCandidateParams) error
	PublishRecommendationSnapshot(ctx context.Context, staged, published string) error
	DiscardRecommendationSnapshot(context.Context, string) error
	RecommendationCandidates(context.Context, string) ([]db.RecommendationCandidate, error)
	RecommendationCandidateCount(context.Context, string) (int, error)
	ListRecommendationCandidateFacts(context.Context, db.ListRecommendationCandidateFactsParams) ([]db.ListRecommendationCandidateFactsRow, error)
	UpsertRecommendationDismissal(context.Context, db.UpsertRecommendationDismissalParams) (db.RecommendationDismissal, error)
	RecordRecommendationFeedback(context.Context, db.RecordRecommendationFeedbackParams) (db.RecommendationFeedback, error)
	ListRecommendationFeedback(context.Context, string) ([]db.RecommendationFeedback, error)
	DeleteRecommendationFeedback(context.Context, string) error
	OwnRecommendationPool(context.Context, db.OwnRecommendationPoolParams) ([]db.OwnRecommendationPoolRow, error)
	RecordRecommendationImpression(context.Context, db.RecordRecommendationImpressionParams) (db.RecommendationImpression, error)
	ListedRecommendationRecordings(context.Context, []uuid.UUID) ([]uuid.UUID, error)
}

type Service struct {
	store      Store
	recordings RecordingProvider
	httpClient *http.Client
	userAgent  string
	logger     zerolog.Logger

	// apiURL and labsURL are test seams, empty in production.
	apiURL  string
	labsURL string
	now     func() time.Time
}

// RecordingProvider expands the recording identifiers ListenBrainz returns
// into the release-group and complete artist-credit identifiers required by
// the hard read-time suppression rules.
type RecordingProvider interface {
	Recording(context.Context, uuid.UUID) (musicbrainz.Recording, error)
}

// NewService builds the service. userAgent is the identity MetaBrainz asks
// every caller for, and is the same one MusicBrainz is asked under: it is one
// installation talking to one organisation.
func NewService(store Store, userAgent string, logger zerolog.Logger) *Service {
	return &Service{
		store:      store,
		httpClient: &http.Client{Timeout: 15 * time.Second},
		userAgent:  userAgent,
		logger:     logger,
		now:        time.Now,
	}
}

// WithEndpoints points the service at test servers instead of ListenBrainz.
func (service *Service) WithEndpoints(apiURL, labsURL string, client *http.Client) *Service {
	service.apiURL, service.labsURL = apiURL, labsURL
	if client != nil {
		service.httpClient = client
	}
	return service
}

// WithRecordingProvider registers MusicBrainz expansion for recommendation
// sweeps. It is a seam rather than a second HTTP path: internal/musicbrainz
// remains the only client boundary for this metadata.
func (service *Service) WithRecordingProvider(provider RecordingProvider) *Service {
	service.recordings = provider
	return service
}

// client builds a ListenBrainz client from the stored settings.
//
// It reads the row on every call rather than caching it, which is what makes a
// changed username or a cleared token take effect immediately. The stored
// address wins over the seam only in production: a test that has pointed the
// service somewhere is pointed there whatever the row says, because a test that
// can reach the real service by getting a fixture wrong is worse than no test.
func (service *Service) client(ctx context.Context) (*listenbrainz.Client, db.ListenBrainzSettingsRow, error) {
	settings, err := service.store.ListenBrainzSettings(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, db.ListenBrainzSettingsRow{}, ErrNotConfigured
	}
	if err != nil {
		return nil, db.ListenBrainzSettingsRow{}, fmt.Errorf("read ListenBrainz settings: %w", err)
	}

	baseURL, labsURL := settings.BaseURL, settings.LabsURL
	if service.apiURL != "" {
		baseURL = service.apiURL
	}
	if service.labsURL != "" {
		labsURL = service.labsURL
	}

	client, err := listenbrainz.NewClient(listenbrainz.Options{
		BaseURL:    baseURL,
		LabsURL:    labsURL,
		UserAgent:  service.userAgent,
		UserToken:  settings.UserToken.String,
		HTTPClient: service.httpClient,
	})
	if err != nil {
		return nil, db.ListenBrainzSettingsRow{}, fmt.Errorf("build ListenBrainz client: %w", err)
	}
	return client, settings, nil
}

// connectionCheckTimeout bounds the check so a wedged or very slow service
// cannot hold a request open indefinitely.
const connectionCheckTimeout = 10 * time.Second

// CheckConnection asks ListenBrainz whether it is there and knows the stored
// account, and records the outcome so the stored status stays meaningful
// between checks. It reads; it stores nothing about anybody's taste.
//
// A source that is switched off is still checked. `enabled` says whether Schall
// should go asking for recommendations on its own, and somebody who has turned
// that off is still entitled to find out whether the account they typed is the
// right one.
func (service *Service) CheckConnection(ctx context.Context) (db.ListenBrainzSettingsRow, error) {
	client, settings, err := service.client(ctx)
	if err != nil {
		return db.ListenBrainzSettingsRow{}, err
	}

	bounded, cancel := context.WithTimeout(ctx, connectionCheckTimeout)
	defer cancel()

	status, checkErr := client.CheckConnection(bounded, settings.Username)
	statusName, failure := "ok", ""
	if checkErr != nil {
		statusName, failure = "failed", checkErr.Error()
		service.logger.Warn().Err(checkErr).Msg("listenbrainz connection check failed")
	}

	// Recorded against the caller's context rather than the bounded one, so a
	// check that timed out still writes down that it did.
	//
	// The row this check read is named in the write, and the write finds nothing
	// if a save moved it meanwhile. A check is a statement about the account it
	// asked; landing it on whatever the singleton holds by the time it finishes
	// would show one account's verdict under another's name, and the settings
	// page would be reporting a success nobody ever obtained for that account.
	saved, err := service.store.RecordListenBrainzConnection(ctx, db.RecordListenBrainzConnectionParams{
		ExpectedUsername:  settings.Username,
		ExpectedUpdatedAt: settings.UpdatedAt,
		Status:            statusName,
		Detail:            status.Detail,
		Failure:           failure,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		service.logger.Info().Str("username", settings.Username).
			Msg("listenbrainz connection check superseded before it was recorded")
		return db.ListenBrainzSettingsRow{}, ErrCheckSuperseded
	}
	if err != nil {
		return db.ListenBrainzSettingsRow{}, fmt.Errorf("record ListenBrainz connection: %w", err)
	}
	return saved, nil
}

const (
	recommendationPageSize     = 1000
	maximumSeedRecordings      = 25
	maximumCandidateExpansions = 25
	seedStatisticsRange        = "all_time"
)

// maximumSweepPasses bounds one chain of continuations, however the chain goes.
//
// Walking a full provider page takes recommendationPageSize divided by
// maximumCandidateExpansions passes — forty today — and the cap sits above that
// so an ordinary sweep is never cut short by it. What it stops is the chain that
// does not converge. A pass that has more to do queues its successor to run
// immediately, so a chain which stopped making progress would ask ListenBrainz
// and MusicBrainz again every few seconds for as long as it kept not making it.
// A pass takes its work from what the snapshot does not yet hold, so the walk is
// meant to converge on its own; this stays as the backstop for the case nobody
// predicted.
//
// A chain now also spends a pass on each service outage it waits through,
// because an outage carries the chain on rather than ending it. The cap has room
// for both: fifty-two passes walked the live account's 1298 candidates, and the
// passes above that are how many outages one chain may wait through before it
// stops and leaves the day to the next sweep.
const maximumSweepPasses = 128

// stagedSuffix names the snapshot a chain is still building, beside the one
// people read.
//
// Both recommendation tables are keyed by source, so a second source name is a
// second snapshot in the same tables and needs no schema change. Every pass of a
// chain writes the whole of what it has under the staged name, and the chain
// renames it over the published name in one transaction once it has walked the
// list. Only the running chain reads the staged name, to carry its own earlier
// passes forward; nothing that shows recommendations to anybody looks at it.
//
// Before this, each pass replaced the published snapshot. The first pass of a
// chain carries nothing forward, so once a day the list somebody reads was cut
// to the 25 records that pass had expanded, and grew back over the next half
// hour (issue #305).
const stagedSuffix = ":building"

func stagedSource(source string) string { return source + stagedSuffix }

// SweepPosition is the durable point between two bounded expansion passes.
//
// It carries the answer itself. Naming the records a sweep must look up costs
// ListenBrainz 27 requests: one for the recommendation page, and one for each of
// the 25 top recordings the similarity path uses as a seed. Looking one record up
// in MusicBrainz costs a request too, and a pass may spend only
// maximumCandidateExpansions of those, so a full account is walked over about
// fifty passes. Every pass used to name the records again before it looked any
// of them up. On the live account that was about 1350 ListenBrainz requests to do
// 1298 look-ups, and a sweep of 37 minutes where the work in it is 22 (issue
// #302). Now the first pass of a chain asks, and every pass after it works from
// what this carries.
//
// Candidates is what is left to look up, in the order the walk takes them. Each
// pass drops the records it asked MusicBrainz about — whether or not a row came
// of the answer, because a recording MusicBrainz does not know would otherwise be
// offered first for ever — and hands the rest to its successor. The list is
// largest in the first payload, roughly 200 KB of JSON for the live account's
// 1298 records, and is empty by the last.
//
// A pass hands its position on only while records are left, so Candidates is
// never empty in a position any pass wrote. SweepPage reads an empty one on a
// later pass as an answer that was lost and fetches again.
//
// SnapshotID, SnapshotAt and Notes are the rest of that one fetch: what
// ListenBrainz called the model behind the page, when it last rebuilt it, and
// what the fetch could not do. Notes travel to the end of the chain because the
// snapshot the last pass publishes has to say the similarity service was quiet,
// and no pass after the first can find that out for itself.
//
// One fetch per chain also settles two things earlier passes had to defend
// against. The list can no longer change size under the walk, which used to
// restart it (issue #303). And a service that goes quiet after the first pass no
// longer takes away the records it named: the chain holds them and finishes,
// where before it could only wait for the quiet service to come back and gave up
// after maximumSweepPasses if it did not.
//
// HasCandidates says whether the partial snapshot already holds rows this pass
// must carry forward. Passes counts the whole chain and bounds how much work one
// sweep may spend. Offered is how many records the one fetch named, so every pass
// of a chain reports the same total.
//
// Username and Algorithm are the account the one fetch asked about, carried so
// that a later pass can tell whether it is still the account settings name.
type SweepPosition struct {
	Candidates    []SweepCandidate `json:"candidates,omitempty"`
	SnapshotID    string           `json:"snapshotId,omitempty"`
	SnapshotAt    *time.Time       `json:"snapshotAt,omitempty"`
	Notes         []string         `json:"notes,omitempty"`
	Username      string           `json:"username,omitempty"`
	Algorithm     string           `json:"algorithm,omitempty"`
	HasCandidates bool             `json:"hasCandidates,omitempty"`
	Passes        int              `json:"passes,omitempty"`
	Offered       int              `json:"offered,omitempty"`
}

// forAccount reports whether this position was built for the account the
// settings row names now. Both fields are part of the account: the username says
// whose listening was read, and the similarity algorithm says which answer the
// labs service was asked for.
func (position SweepPosition) forAccount(settings db.ListenBrainzSettingsRow) bool {
	return position.Username == settings.Username &&
		position.Algorithm == settings.SimilarityAlgorithm
}

// SweepCandidate is one record ListenBrainz named, in the form the job payload
// carries it from one pass of a chain to the next.
//
// It holds everything the stored row will need except what MusicBrainz has not
// been asked yet: which recording to look up, the score ListenBrainz gave it,
// the codes saying why it is in the answer, the context a reason is written from,
// and the seed recordings a similarity answer came back from.
type SweepCandidate struct {
	RecordingID uuid.UUID      `json:"id"`
	Score       *float64       `json:"score,omitempty"`
	Reasons     []string       `json:"reasons,omitempty"`
	Context     map[string]any `json:"context,omitempty"`
	SimilarTo   []string       `json:"similarTo,omitempty"`
}

// SweepResult tells the worker whether the chain goes on and, if it does, where
// the next bounded pass starts. Report is what the pass did, in numbers.
//
// More means there is work waiting and the next pass should run at once. A pass
// MusicBrainz answered about a record sets it whether or not a look-up failed at
// the end of the pass, and whether or not a row came of the answer, because a
// service that answered is a service to go back to. Resume means the chain is
// unfinished and what it has built is kept, so the next job must carry Position
// forward rather than start a new chain. Every pass that sets More sets Resume
// too; a pass MusicBrainz would not answer at all sets Resume alone, because the
// wait before the next attempt is longer.
//
// A chain that finished the list, a chain that spent its passes, and a chain
// told of no records at all set neither: each of the three is over, and the next
// sweep starts a new one.
type SweepResult struct {
	More     bool
	Resume   bool
	Position SweepPosition
	Report   SweepReport
}

// SweepReport is one pass written down, so the worker can say in the log what
// happened and choose when to come back.
//
// The sweep talks to two services and can take minutes, and until this existed
// it said nothing at all. Somebody who had just connected a ListenBrainz account
// had no way to tell a pass that found two hundred records from a pass that
// found none, or from one that never ran.
//
// Offered is how many recordings ListenBrainz named. Asked is how many of them
// this pass looked up in MusicBrainz; the sweep is bounded, so a large answer is
// walked over several passes. Stored is how many rows the snapshot holds when
// the pass ends, which is smaller than Asked whenever MusicBrainz knows a
// recording but not the release or the artist behind it.
//
// ProviderFailed separates the two reasons a pass can end early. A pass that
// reached a bound Schall gave it is working as intended. A pass that stopped
// because ListenBrainz or MusicBrainz would not answer is an outage, and the two
// want different waits before the next pass.
//
// Published says this pass ended the chain and put what the chain built in front
// of readers. Only the pass that walks the last candidate does that, so an
// ordinary sweep publishes once and the passes before it hold their work back.
//
// KeptPublished is the other end of that. ListenBrainz named no records at all,
// a list is already in front of readers, and this pass left it there rather than
// replace it with nothing (issue #317). Like a pass that reached nobody, it is a
// reason to come back sooner than the usual day.
type SweepReport struct {
	SnapshotID     string
	Offered        int
	Asked          int
	Stored         int
	Status         string
	Detail         string
	ProviderFailed bool
	Published      bool
	KeptPublished  bool
}

// Sweep fetches one usable ListenBrainz snapshot, expands its identifiers
// through MusicBrainz, and atomically replaces the stored provider facts once
// the whole answer has been walked. Local ownership, requests, follows,
// dismissals and impressions are not read here: the hard rules are evaluated
// when the list is read.
//
// The collaborative-filtered list is the primary answer. The account's top
// recordings add the ADR's one-hop similarity path. Once that fan-out starts,
// a provider failure stops the remaining calls and records an explained partial
// snapshot rather than substituting weaker data. A failure before any usable
// answer, or before any raw candidate can be expanded, writes nothing so the
// previous snapshot remains available.
func (service *Service) Sweep(ctx context.Context) (bool, error) {
	result, err := service.SweepPage(ctx, SweepPosition{})
	return result.More, err
}

// SweepPage performs one bounded part of a sweep. The job payload carries its
// returned position, so a process restart resumes instead of asking
// MusicBrainz about the first candidates again.
//
// Only the first pass of a chain asks ListenBrainz what the answer is. The
// passes after it read that answer out of the position they were given and spend
// their requests on MusicBrainz alone.
//
// Every pass writes into the staged snapshot and only the pass that finishes the
// list publishes it, so the list people read is a whole answer at every moment
// of a sweep.
func (service *Service) SweepPage(ctx context.Context, position SweepPosition) (SweepResult, error) {
	client, settings, err := service.client(ctx)
	if err != nil {
		return SweepResult{}, err
	}
	staged := stagedSource(listenbrainz.Name)
	if !settings.Enabled {
		// Switching recommendations off ends whatever chain was running, and no
		// later pass will come to tidy up after it, so the half-built snapshot goes
		// now rather than sitting in the database until somebody switches them on.
		if err := service.store.DiscardRecommendationSnapshot(ctx, staged); err != nil {
			return SweepResult{}, fmt.Errorf("discard the snapshot a stopped sweep was building: %w", err)
		}
		return SweepResult{}, ErrDisabled
	}
	if service.recordings == nil {
		return SweepResult{}, errors.New("MusicBrainz recording expansion is not configured")
	}

	// A chain belongs to the account it was started for. Saving a different
	// username, or a different similarity algorithm, queues a sweep — and that ask
	// moves the pass already on the books forward rather than replacing it, so the
	// continuation still carries the records the previous account's fetch named.
	// Walking those to the end would publish one person's suggestions under
	// another person's name, and every "Not interested" pressed against them is a
	// permanent decision about somebody else's music (issue #328).
	//
	// So the chain is dropped and this pass starts a new one: it is the first pass
	// again, which is what makes it fetch, and the half-built snapshot goes with
	// the branch below.
	//
	// A position written by a build that carried no account reads as a different
	// account. That costs one fetch nobody wanted on the first pass after an
	// upgrade, and it is the safe way round.
	if position.Passes > 0 && !position.forAccount(settings) {
		position = SweepPosition{}
	}

	answer := position.offer()
	offered := position.Offered
	if position.Passes == 0 {
		// A chain that never reached its end leaves a staged snapshot behind: a
		// process that stopped, or an outage that outlasted the job's retries.
		// Nobody can read it, but this chain must not carry another chain's work
		// forward as though it were its own.
		if err := service.store.DiscardRecommendationSnapshot(ctx, staged); err != nil {
			return SweepResult{}, fmt.Errorf("discard an abandoned recommendation snapshot: %w", err)
		}
	}
	// A pass fetches when it is the first of its chain, and when the position it
	// was handed carries no records.
	//
	// An empty list means the answer was lost, never that the work is done. A pass
	// hands its position on only while records are left, and it puts those records
	// in it, so no position a pass writes is ever both later than the first and
	// empty. One arrives anyway when the payload was written by a build that did
	// not carry records: encoding/json drops the fields it does not know, and what
	// is left decodes to a later pass with nothing to walk. Reading that as a
	// finished chain would publish whatever the staged snapshot happened to hold —
	// a part of the list, over the whole list people are reading, called complete
	// (issue #305).
	//
	// So the pass asks again. That costs one fetch nobody wanted and recovers
	// everything the position lost: the records, what the answer is called, when it
	// was built, and what the fetch could not do all come back together, because
	// one fetch is where all four come from. The chain then walks what the staged
	// snapshot does not already hold and finishes with a whole answer.
	if position.Passes == 0 || len(answer.candidates) == 0 {
		if answer, err = service.fetchOffer(ctx, client, settings); err != nil {
			return SweepResult{}, err
		}
		offered = len(answer.candidates)
	}
	passes := position.Passes + 1
	partial := append(make([]string, 0, len(answer.notes)+2), answer.notes...)
	// providerFailed is set only where a service Schall asked did not answer. The
	// other things that end a pass early are bounds this code chose, and a bound
	// is not a reason to come back sooner than usual. A pass that asked nobody but
	// MusicBrainz can only report on MusicBrainz.
	providerFailed := answer.failed

	inputs := make([]CandidateInput, 0, len(answer.candidates))
	if position.HasCandidates {
		stored, err := service.store.RecommendationCandidates(ctx, staged)
		if err != nil {
			return SweepResult{}, fmt.Errorf("read partial recommendation snapshot: %w", err)
		}
		inputs, err = candidateInputs(stored)
		if err != nil {
			return SweepResult{}, err
		}
	}

	// A record the staged snapshot already holds is finished with, whatever the
	// carried list still says. Two things put one there: a pass delivered twice,
	// which is what the job queue promises rather than exactly once, and a look-up
	// MusicBrainz answered about the recording the asked-for one was merged into.
	held := make(map[uuid.UUID]struct{}, len(inputs))
	for _, input := range inputs {
		held[input.MusicBrainzRecordingID] = struct{}{}
	}
	remaining := make([]*sourceCandidate, 0, len(answer.candidates))
	for _, candidate := range answer.candidates {
		if _, done := held[candidate.recordingID]; done {
			continue
		}
		remaining = append(remaining, candidate)
	}
	pending := remaining
	if len(pending) > maximumCandidateExpansions {
		pending = pending[:maximumCandidateExpansions]
	}

	run, err := service.expandCandidates(ctx, pending, len(inputs) > 0)
	if err != nil {
		return SweepResult{}, err
	}
	inputs = append(inputs, run.stored...)
	if run.partial != "" {
		partial = append(partial, run.partial)
		providerFailed = true
	}

	// left is the work the chain still has: a record ListenBrainz named that
	// nobody has finished with. None left is a finished chain, and a finished
	// chain is the only one that publishes. done is the other side of the same
	// count, for the log line and the explanations.
	left := candidatesLeft(remaining, run.asked, inputs)
	done := offered - len(left)

	// answered says MusicBrainz was there for this pass. run.partial is the one
	// look-up that failed part way through it, and on its own that is not an
	// outage: a pass that MusicBrainz answered several times and then kept waiting
	// on was talking to a service that is up and merely slow. Reading that as an
	// outage cost the chain thirty minutes for one slow request, once per pass,
	// which is longer than the sweep itself (issue #310).
	//
	// What counts as an answer is every recording MusicBrainz replied about,
	// stored or not. It knows some recordings but not the release group or the
	// artist behind them, and a pass whose every reply was one of those stored
	// nothing while the service answered it each time. Waiting half an hour on
	// that is waiting on an outage nobody had (issue #314). A recording it replied
	// "I do not know that one" about counts too: that is an answer as much as any
	// other, and it is the only kind the pass keeps nothing from.
	//
	// A pass whose very first look-up failed has been answered about nothing, and
	// that one waits. A degraded MusicBrainz that replies fast and uselessly is
	// what the fast path is now open to, and the one-request-per-second limiter in
	// the client is what bounds how fast the chain can then loop.
	answered := run.partial == "" || len(run.asked) > 0
	unfinished := len(left) > 0
	more := answered && unfinished
	spent := unfinished && passes >= maximumSweepPasses
	switch {
	case spent:
		// The chain has spent what one sweep may spend. Half a list is not an
		// answer, so what it built is thrown away rather than published, and the
		// next scheduled sweep starts a fresh chain.
		more = false
		partial = append(partial, fmt.Sprintf(
			"the sweep stopped after %d passes: it was shown %d candidates and finished with %d",
			passes, offered, done,
		))
	case more:
		partial = append(partial, fmt.Sprintf(
			"MusicBrainz expansion deferred %d of %d candidates", len(left), offered,
		))
	}
	report := SweepReport{
		SnapshotID:     answer.snapshotID,
		Offered:        offered,
		Asked:          len(run.asked),
		ProviderFailed: providerFailed,
	}
	next := SweepPosition{
		Candidates: carriedCandidates(left),
		SnapshotID: answer.snapshotID,
		SnapshotAt: answer.snapshotAt,
		Notes:      answer.notes,
		Username:   settings.Username,
		Algorithm:  settings.SimilarityAlgorithm,
		Passes:     passes,
		Offered:    offered,
	}
	if spent {
		report.Status, report.Detail = "partial", strings.Join(partial, "; ")
		if err := service.store.DiscardRecommendationSnapshot(ctx, staged); err != nil {
			return SweepResult{}, fmt.Errorf("discard a recommendation snapshot nobody finished: %w", err)
		}
		return SweepResult{Report: report}, nil
	}
	// A pass holding no record at all never puts an empty list where a list is.
	// That is the one rule here, and the four answers below are the four ways a
	// pass arrives holding nothing. The first is not an end at all. The two after
	// it are faults: they fail the pass, and a failed pass writes nothing. The last
	// is an answer of nothing that nobody called a failure, and it keeps whatever
	// is published. An empty snapshot is published where nothing is published yet,
	// and nowhere else.
	if len(inputs) == 0 {
		// Not an end at all. Records are still waiting and this pass happens to have
		// stored none of them, so its successor carries the chain on.
		if more {
			report.Status = "partial"
			report.Detail = strings.Join(partial, "; ")
			return SweepResult{More: true, Resume: true, Report: report, Position: next}, nil
		}
		// ListenBrainz named records and not one of them could be stored, which is
		// MusicBrainz answering thinly rather than the account having nothing. That
		// is a fault, so the pass fails and the job is retried. Nothing is written,
		// so whatever is published stays published.
		if offered > 0 {
			return SweepResult{}, fmt.Errorf(
				"ListenBrainz returned %d candidates but none had usable MusicBrainz metadata",
				offered,
			)
		}
		// The fetch said what it could not do. Same again: a fault, reported, with
		// nothing written.
		if len(partial) > 0 {
			return SweepResult{}, errors.New(strings.Join(partial, "; "))
		}
		// Both ListenBrainz services named no records, and neither called that a
		// failure. An account with no model computed for it yet gets that answer,
		// and so does an account whose two services were both quiet this morning.
		// The two answers are the same answer: nothing in either of them says which
		// one this is.
		//
		// Publishing replaces, so publishing this would delete the list somebody
		// read yesterday and tell them the account has nothing to suggest, which is
		// the one thing nobody here knows (issue #317). Where a list is already
		// published, an answer of no records is not believed. The published list
		// stays, this pass publishes nothing, and the sweep comes back in the time a
		// pass that reached nobody comes back in.
		//
		// This asks what is published rather than what was offered, so it holds
		// however the pass came to be empty — including a payload that reached this
		// pass without the count of what the fetch named (issue #313).
		//
		// An account that truly has nothing keeps what it published before until
		// some later answer names a record. That is a known limitation and it is
		// accepted: telling the two apart needs a distinction the answers do not
		// carry, and inventing one would guess.
		published, err := service.store.RecommendationCandidateCount(ctx, listenbrainz.Name)
		if err != nil {
			return SweepResult{}, fmt.Errorf("count the published recommendations: %w", err)
		}
		if published > 0 {
			report.Status = "partial"
			report.Detail = fmt.Sprintf(
				"ListenBrainz named no recordings, so the %d already published are kept", published)
			report.KeptPublished = true
			return SweepResult{Report: report}, nil
		}
	}

	status, detail := "complete", ""
	if len(partial) > 0 {
		status, detail = "partial", strings.Join(partial, "; ")
	}
	feedback, err := service.store.ListRecommendationFeedback(ctx, listenbrainz.Name)
	if err != nil {
		return SweepResult{}, fmt.Errorf("read recommendation feedback: %w", err)
	}
	if err := service.replaceSnapshot(ctx, Snapshot{
		Source:           staged,
		SourceSnapshotID: answer.snapshotID,
		SourceSnapshotAt: answer.snapshotAt,
		Status:           status,
		Detail:           detail,
		Candidates:       inputs,
	}, feedback); err != nil {
		return SweepResult{}, err
	}
	report.Stored = len(inputs)
	report.Status = status
	report.Detail = detail
	if unfinished {
		// The chain goes on, and what it has stays where no reader looks. A pass
		// with candidates waiting comes straight back; a pass that reached nobody
		// waits, and either way the next one carries this position.
		next.HasCandidates = true
		return SweepResult{More: more, Resume: true, Report: report, Position: next}, nil
	}
	if err := service.store.PublishRecommendationSnapshot(ctx, staged, listenbrainz.Name); err != nil {
		return SweepResult{}, fmt.Errorf("publish recommendation snapshot: %w", err)
	}
	report.Published = true
	return SweepResult{Report: report}, nil
}

// offer is one ListenBrainz answer: every record it named, in the order the walk
// takes them, beside what the answer is called and what the fetch could not do.
// A chain fetches one of these on its first pass and carries it to the end.
type offer struct {
	snapshotID string
	snapshotAt *time.Time
	candidates []*sourceCandidate
	notes      []string
	// failed says a service Schall asked did not answer, which is a reason to
	// come back sooner than usual.
	failed bool
}

// offer reads the answer a chain is walking back out of the job payload that
// carried it. Only the first pass of a chain has no answer to read, and it
// fetches one.
func (position SweepPosition) offer() offer {
	answer := offer{
		snapshotID: position.SnapshotID,
		snapshotAt: position.SnapshotAt,
		candidates: make([]*sourceCandidate, 0, len(position.Candidates)),
		notes:      position.Notes,
	}
	for _, carried := range position.Candidates {
		candidate := &sourceCandidate{
			recordingID:   carried.RecordingID,
			score:         carried.Score,
			reasonCodes:   make(map[string]struct{}, len(carried.Reasons)),
			reasonContext: make(map[string]any, len(carried.Context)),
			similarTo:     make(map[string]struct{}, len(carried.SimilarTo)),
		}
		for _, reason := range carried.Reasons {
			candidate.reasonCodes[reason] = struct{}{}
		}
		for key, value := range carried.Context {
			candidate.reasonContext[key] = value
		}
		for _, seed := range carried.SimilarTo {
			candidate.similarTo[seed] = struct{}{}
		}
		answer.candidates = append(answer.candidates, candidate)
	}
	return answer
}

// carriedCandidates writes the records a pass did not reach into the form the
// job payload takes them on in. The sets become sorted lists so the same
// remainder is always the same bytes.
func carriedCandidates(candidates []*sourceCandidate) []SweepCandidate {
	carried := make([]SweepCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		reasons := make([]string, 0, len(candidate.reasonCodes))
		for reason := range candidate.reasonCodes {
			reasons = append(reasons, reason)
		}
		sort.Strings(reasons)
		seeds := make([]string, 0, len(candidate.similarTo))
		for seed := range candidate.similarTo {
			seeds = append(seeds, seed)
		}
		sort.Strings(seeds)
		carried = append(carried, SweepCandidate{
			RecordingID: candidate.recordingID,
			Score:       candidate.score,
			Reasons:     reasons,
			Context:     candidate.reasonContext,
			SimilarTo:   seeds,
		})
	}
	return carried
}

// fetchOffer asks ListenBrainz what this account is offered. It costs 27
// requests, so a chain does it once and every pass after the first works from
// what this returned.
//
// The collaborative-filtered page is the primary answer. The account's top
// recordings add the ADR's one-hop similarity path. A failure in that fan-out
// stops the remaining calls and is written down rather than substituted for: the
// chain walks the shorter answer, and the snapshot it publishes says the
// similarity service was quiet. A failure of the page itself is returned, so the
// sweep writes nothing and the previous snapshot remains available.
func (service *Service) fetchOffer(
	ctx context.Context, client *listenbrainz.Client, settings db.ListenBrainzSettingsRow,
) (offer, error) {

	raw := make(map[uuid.UUID]*sourceCandidate)
	answer := offer{notes: make([]string, 0, 2)}

	batch, err := client.Recommendations(ctx, settings.Username, recommendationPageSize, 0)
	switch {
	case err == nil:
		answer.snapshotID = batch.ModelID
		if !batch.LastUpdated.IsZero() {
			at := batch.LastUpdated.UTC()
			answer.snapshotAt = &at
		}
		for _, recommendation := range batch.Recordings {
			recordingID, parseErr := uuid.Parse(recommendation.RecordingMBID)
			if parseErr != nil {
				continue
			}
			candidate := sourceCandidateFor(raw, recordingID)
			candidate.addScore(recommendation.Score)
			candidate.reasonCodes["cf_raw"] = struct{}{}
			if batch.ModelID != "" {
				candidate.reasonContext["model_id"] = batch.ModelID
			}
			if !recommendation.LatestListenedAt.IsZero() {
				candidate.reasonContext["latest_listened_at"] =
					recommendation.LatestListenedAt.UTC().Format(time.RFC3339)
			}
		}
	case errors.Is(err, listenbrainz.ErrNoRecommendations):
		// The account exists but has no CF model yet. Its top recordings may
		// still seed a usable similarity snapshot.
	default:
		return offer{}, fmt.Errorf("fetch ListenBrainz recommendations: %w", err)
	}

	seeds, seedErr := client.TopRecordings(ctx, settings.Username, seedStatisticsRange)
	switch {
	case seedErr == nil:
		if len(seeds) > maximumSeedRecordings {
			answer.notes = append(answer.notes, fmt.Sprintf(
				"seed request budget used %d of %d top recordings",
				maximumSeedRecordings, len(seeds),
			))
			seeds = seeds[:maximumSeedRecordings]
		}
		for _, seed := range seeds {
			seedID, parseErr := uuid.Parse(seed.RecordingMBID)
			if parseErr != nil {
				continue
			}
			similar, similarErr := client.SimilarRecordings(
				ctx, []string{seedID.String()}, settings.SimilarityAlgorithm,
			)
			if errors.Is(similarErr, listenbrainz.ErrNoSimilarRecordings) {
				continue
			}
			if similarErr != nil {
				answer.notes = append(answer.notes, fmt.Sprintf(
					"similarity fan-out stopped at seed %s: %v", seed.RecordingMBID, similarErr,
				))
				answer.failed = true
				break
			}
			for _, recommendation := range similar {
				recordingID, parseErr := uuid.Parse(recommendation.RecordingMBID)
				if parseErr != nil {
					continue
				}
				referenceID, parseErr := uuid.Parse(recommendation.ReferenceMBID)
				if parseErr != nil {
					continue
				}
				candidate := sourceCandidateFor(raw, recordingID)
				candidate.addScore(recommendation.Score)
				candidate.reasonCodes["similar_to"] = struct{}{}
				candidate.similarTo[referenceID.String()] = struct{}{}
			}
		}
	case errors.Is(seedErr, listenbrainz.ErrNoRecommendations):
		// No computed statistics is a usable empty secondary answer.
	default:
		answer.notes = append(answer.notes, "top-recording seeds unavailable: "+seedErr.Error())
		answer.failed = true
	}

	answer.candidates = orderedSourceCandidates(raw)
	return answer, nil
}

// candidatesLeft is what a chain still has to look up: the records it had left
// at the start of this pass, less the ones the pass finished with.
//
// A record this pass asked MusicBrainz about is finished with whether or not a
// row came of it. MusicBrainz not knowing the recording, or knowing it but not
// the release group or the artist behind it, is an answer, and a pass that kept
// such a record would offer it first to every pass after it for ever. A look-up
// that failed is not an answer and stays on the list.
//
// The stored rows are read as well as the asked-about identifiers, because
// MusicBrainz can be asked about one recording and reply about the recording
// that one was merged into. The row is then stored under an identifier this pass
// never asked for, and the merged-into record must not be looked up again when
// the walk reaches it.
func candidatesLeft(
	remaining []*sourceCandidate, asked []uuid.UUID, inputs []CandidateInput,
) []*sourceCandidate {

	settled := make(map[uuid.UUID]struct{}, len(asked)+len(inputs))
	for _, recordingID := range asked {
		settled[recordingID] = struct{}{}
	}
	for _, input := range inputs {
		settled[input.MusicBrainzRecordingID] = struct{}{}
	}
	left := make([]*sourceCandidate, 0, len(remaining))
	for _, candidate := range remaining {
		if _, done := settled[candidate.recordingID]; done {
			continue
		}
		left = append(left, candidate)
	}
	return left
}

type sourceCandidate struct {
	recordingID   uuid.UUID
	score         *float64
	reasonCodes   map[string]struct{}
	reasonContext map[string]any
	similarTo     map[string]struct{}
}

func sourceCandidateFor(candidates map[uuid.UUID]*sourceCandidate, recordingID uuid.UUID) *sourceCandidate {
	if candidate, ok := candidates[recordingID]; ok {
		return candidate
	}
	candidate := &sourceCandidate{
		recordingID: recordingID, reasonCodes: make(map[string]struct{}),
		reasonContext: make(map[string]any), similarTo: make(map[string]struct{}),
	}
	candidates[recordingID] = candidate
	return candidate
}

func (candidate *sourceCandidate) addScore(score float64) {
	if math.IsNaN(score) || math.IsInf(score, 0) {
		return
	}
	if candidate.score == nil || score > *candidate.score {
		candidate.score = &score
	}
}

func orderedSourceCandidates(raw map[uuid.UUID]*sourceCandidate) []*sourceCandidate {
	candidates := make([]*sourceCandidate, 0, len(raw))
	for _, candidate := range raw {
		candidates = append(candidates, candidate)
	}
	sort.Slice(candidates, func(left, right int) bool {
		leftScore, rightScore := math.Inf(-1), math.Inf(-1)
		if candidates[left].score != nil {
			leftScore = *candidates[left].score
		}
		if candidates[right].score != nil {
			rightScore = *candidates[right].score
		}
		if leftScore != rightScore {
			return leftScore > rightScore
		}
		return candidates[left].recordingID.String() < candidates[right].recordingID.String()
	})
	return candidates
}

func candidateInputs(stored []db.RecommendationCandidate) ([]CandidateInput, error) {
	result := make([]CandidateInput, 0, len(stored))
	for _, candidate := range stored {
		context := make(map[string]any)
		if len(candidate.ReasonContext) > 0 {
			if err := json.Unmarshal(candidate.ReasonContext, &context); err != nil {
				return nil, fmt.Errorf(
					"decode stored recommendation candidate %s context: %w",
					candidate.MusicbrainzRecordingID, err,
				)
			}
		}
		var score *float64
		if candidate.SourceScore.Valid {
			value := candidate.SourceScore.Float64
			score = &value
		}
		result = append(result, CandidateInput{
			MusicBrainzRecordingID:    candidate.MusicbrainzRecordingID,
			MusicBrainzReleaseGroupID: candidate.MusicbrainzReleaseGroupID,
			MusicBrainzArtistIDs:      append([]uuid.UUID(nil), candidate.MusicbrainzArtistIds...),
			RecordingTitle:            candidate.RecordingTitle, ReleaseTitle: candidate.ReleaseTitle,
			ArtistName: candidate.ArtistName, SourceScore: score,
			ReasonCodes: append([]string(nil), candidate.ReasonCodes...), ReasonContext: context,
		})
	}
	return result, nil
}

// expansion is one bounded run of MusicBrainz look-ups written down: the
// candidates it turned into rows, every candidate it did ask about whether or
// not a row came of it, and the explanation if a failure ended the run early.
//
// asked is separate from stored because the cursor needs both. A recording
// MusicBrainz does not know was asked about and produced nothing, and a pass
// that recorded only what it stored would offer that same recording to every
// following pass.
type expansion struct {
	stored  []CandidateInput
	asked   []uuid.UUID
	partial string
}

// carried says the chain already holds records from its earlier passes. Without
// them a run that stores nothing has nothing to report, and the pass fails so
// that the published snapshot is left alone. With them the run is a partial one:
// the chain keeps what it has and its successor asks again.
func (service *Service) expandCandidates(
	ctx context.Context, candidates []*sourceCandidate, carried bool,
) (expansion, error) {

	run := expansion{
		stored: make([]CandidateInput, 0, len(candidates)),
		asked:  make([]uuid.UUID, 0, len(candidates)),
	}
	for _, candidate := range candidates {
		recording, usable, err := service.expandRecording(ctx, candidate.recordingID)
		if err != nil {
			// Not counted as asked. MusicBrainz refusing to answer says nothing
			// about the recording, so the next pass has to put the question again.
			message := fmt.Sprintf("MusicBrainz expansion stopped at %s: %v", candidate.recordingID, err)
			if len(run.stored) == 0 && !carried {
				return expansion{}, errors.New(message)
			}
			run.partial = message
			return run, nil
		}
		run.asked = append(run.asked, candidate.recordingID)
		if !usable {
			continue
		}

		reasons := make([]string, 0, len(candidate.reasonCodes))
		for reason := range candidate.reasonCodes {
			reasons = append(reasons, reason)
		}
		sort.Strings(reasons)
		if len(candidate.similarTo) > 0 {
			seeds := make([]string, 0, len(candidate.similarTo))
			for seed := range candidate.similarTo {
				seeds = append(seeds, seed)
			}
			sort.Strings(seeds)
			candidate.reasonContext["seed_recording_ids"] = seeds
		}
		run.stored = append(run.stored, recording.input(candidate.score, reasons, candidate.reasonContext))
	}
	return run, nil
}

// recordingExpansion is what MusicBrainz says about one recording, in the shape
// a stored candidate needs: the release group and the complete artist credit
// the hard read-time rules compare against.
type recordingExpansion struct {
	recordingID    uuid.UUID
	releaseGroupID uuid.UUID
	artistIDs      []uuid.UUID
	title          string
	releaseTitle   string
	artistName     string
}

// expandRecording asks MusicBrainz about one recording. Both sweeps use it, so
// they spend the same client and its one-request-per-second limit.
//
// It answers false with no error when MusicBrainz answered and the answer
// cannot be stored: it does not know the recording, or knows it without a
// release group or a credited artist. An error means MusicBrainz did not
// answer, which says nothing about the recording.
func (service *Service) expandRecording(
	ctx context.Context, recordingID uuid.UUID,
) (recordingExpansion, bool, error) {
	recording, err := service.recordings.Recording(ctx, recordingID)
	if errors.Is(err, musicbrainz.ErrNotFound) {
		return recordingExpansion{}, false, nil
	}
	if err != nil {
		return recordingExpansion{}, false, err
	}

	var release musicbrainz.RecordingRelease
	for _, item := range recording.Releases {
		if item.ReleaseGroupID != uuid.Nil {
			release = item
			break
		}
	}
	artistIDs := make([]uuid.UUID, 0, len(recording.Credits))
	seenArtists := make(map[uuid.UUID]struct{}, len(recording.Credits))
	for _, credit := range recording.Credits {
		if credit.ArtistID == uuid.Nil {
			continue
		}
		if _, seen := seenArtists[credit.ArtistID]; seen {
			continue
		}
		seenArtists[credit.ArtistID] = struct{}{}
		artistIDs = append(artistIDs, credit.ArtistID)
	}
	if release.ReleaseGroupID == uuid.Nil || len(artistIDs) == 0 ||
		strings.TrimSpace(recording.Title) == "" || strings.TrimSpace(recording.ArtistCredit) == "" {
		return recordingExpansion{}, false, nil
	}
	return recordingExpansion{
		recordingID: recording.ID, releaseGroupID: release.ReleaseGroupID,
		artistIDs: artistIDs, title: recording.Title,
		releaseTitle: release.Title, artistName: recording.ArtistCredit,
	}, true, nil
}

// input is the stored candidate this expansion becomes under one source's
// score and reasons.
func (recording recordingExpansion) input(
	score *float64, reasons []string, context map[string]any,
) CandidateInput {
	return CandidateInput{
		MusicBrainzRecordingID: recording.recordingID, MusicBrainzReleaseGroupID: recording.releaseGroupID,
		MusicBrainzArtistIDs: append([]uuid.UUID(nil), recording.artistIDs...), RecordingTitle: recording.title,
		ReleaseTitle: recording.releaseTitle, ArtistName: recording.artistName,
		SourceScore: score, ReasonCodes: reasons, ReasonContext: context,
	}
}

// SuppressionRule is why a candidate is not renderable. Empty means it may be
// shown. The order is the PRODUCT.md order and is stable when several rules
// happen to apply at once.
//
// There are eight internal rules and six product entries: the acquisition entry
// has two answers, and the follow entry is asked once each about an artist and
// a label.
type SuppressionRule string

const (
	SuppressedOwned     SuppressionRule = "owned"
	SuppressedRequested SuppressionRule = "requested"
	// SuppressedNotWanted is a want somebody stopped. It sits beside
	// SuppressedRequested because both are the same question — has this reader
	// already answered about acquiring this recording — and apart from it
	// because the answers are opposite and a reader is owed the difference.
	SuppressedNotWanted      SuppressionRule = "not_wanted"
	SuppressedFollowedArtist SuppressionRule = "followed_artist"
	// SuppressedFollowedLabel is the same rule as SuppressedFollowedArtist,
	// about a label instead: the follow feed already covers this release, so
	// suggesting it here would offer to fetch it a second time.
	SuppressedFollowedLabel SuppressionRule = "followed_label"
	SuppressedDismissed     SuppressionRule = "dismissed"
	// SuppressedUnkept is a recording the weekly playlist obtained, the reader
	// had for a week, and nobody kept, so it was removed again (ADR 0021). It
	// sits after the dismissal because it is weaker: the reader never said
	// "not interested", they were merely busy. It sits before the impression
	// fatigue because it is stronger: the song was on their disc, not just on
	// their screen. The window expires on its own.
	SuppressedUnkept            SuppressionRule = "unkept"
	SuppressedImpressionFatigue SuppressionRule = "impression_fatigue"
)

// SuppressionFacts are the independent facts read from current local state for
// one stored provider candidate, one per reason.
type SuppressionFacts struct {
	Owned             bool
	Requested         bool
	NotWanted         bool
	FollowedArtist    bool
	FollowedLabel     bool
	Dismissed         bool
	Unkept            bool
	ImpressionFatigue bool
}

// SuppressionRuleFor is the pure suppression engine: no I/O, clock or stored
// state is hidden inside it. Callers can therefore prove both the rule selected
// and the absence of one from the exact facts the database returned.
func SuppressionRuleFor(facts SuppressionFacts) SuppressionRule {
	switch {
	case facts.Owned:
		return SuppressedOwned
	case facts.Requested:
		return SuppressedRequested
	case facts.NotWanted:
		return SuppressedNotWanted
	case facts.FollowedArtist:
		return SuppressedFollowedArtist
	case facts.FollowedLabel:
		return SuppressedFollowedLabel
	case facts.Dismissed:
		return SuppressedDismissed
	case facts.Unkept:
		return SuppressedUnkept
	case facts.ImpressionFatigue:
		return SuppressedImpressionFatigue
	default:
		return ""
	}
}

// Candidate is one provider answer with the source provenance and explanation
// material the renderer needs. All identifiers are MusicBrainz identifiers.
type Candidate struct {
	Source                    string
	MusicBrainzRecordingID    uuid.UUID
	MusicBrainzReleaseGroupID uuid.UUID
	MusicBrainzArtistIDs      []uuid.UUID
	RecordingTitle            string
	ReleaseTitle              string
	ArtistName                string
	Rank                      int32
	SourceScore               *float64
	ReasonCodes               []string
	ReasonContext             json.RawMessage
	FetchedAt                 time.Time
	SourceSnapshotID          *string
	SourceSnapshotAt          *time.Time
	SnapshotStatus            string
	SnapshotDetail            string
	SnapshotFetchedAt         time.Time
}

// EvaluatedCandidate keeps the suppression verdict beside the candidate it was
// made about. SuppressedBy is empty exactly when Candidate may be rendered.
type EvaluatedCandidate struct {
	Candidate
	SuppressedBy SuppressionRule
}

// Evaluate reads the current hard facts for a source and applies the pure
// engine to each candidate. It includes suppressed rows so diagnostics can say
// which rule won without changing the stored provider snapshot.
func (service *Service) Evaluate(ctx context.Context, source string) ([]EvaluatedCandidate, error) {
	rows, err := service.store.ListRecommendationCandidateFacts(ctx, db.ListRecommendationCandidateFactsParams{
		ObservedAt: pgtype.Timestamptz{Time: service.now().UTC(), Valid: true},
		Source:     source,
	})
	if err != nil {
		return nil, fmt.Errorf("read recommendation candidates: %w", err)
	}

	result := make([]EvaluatedCandidate, 0, len(rows))
	for _, row := range rows {
		result = append(result, EvaluatedCandidate{
			Candidate:    candidateFromRow(row),
			SuppressedBy: SuppressionRuleFor(factsFromRow(row)),
		})
	}
	return result, nil
}

// List returns only candidates that may be rendered now, in their stored rank
// order. Suppression is evaluated on every call, so a follow, request,
// dismissal, ownership change or expired fatigue window takes effect without a
// provider refresh.
func (service *Service) List(ctx context.Context, source string) ([]Candidate, error) {
	evaluated, err := service.Evaluate(ctx, source)
	if err != nil {
		return nil, err
	}
	result := make([]Candidate, 0, len(evaluated))
	for _, candidate := range evaluated {
		if candidate.SuppressedBy == "" {
			result = append(result, candidate.Candidate)
		}
	}
	return result, nil
}

func factsFromRow(row db.ListRecommendationCandidateFactsRow) SuppressionFacts {
	return SuppressionFacts{
		Owned:             row.Owned,
		Requested:         row.Requested,
		NotWanted:         row.NotWanted,
		FollowedArtist:    row.FollowedArtist,
		FollowedLabel:     row.FollowedLabel,
		Dismissed:         row.Dismissed,
		Unkept:            row.Unkept,
		ImpressionFatigue: row.ImpressionFatigue,
	}
}

func candidateFromRow(row db.ListRecommendationCandidateFactsRow) Candidate {
	result := Candidate{
		Source:                    row.Source,
		MusicBrainzRecordingID:    row.MusicbrainzRecordingID,
		MusicBrainzReleaseGroupID: row.MusicbrainzReleaseGroupID,
		MusicBrainzArtistIDs:      row.MusicbrainzArtistIds,
		RecordingTitle:            row.RecordingTitle,
		ReleaseTitle:              row.ReleaseTitle,
		ArtistName:                row.ArtistName,
		Rank:                      row.Rank,
		ReasonCodes:               row.ReasonCodes,
		ReasonContext:             row.ReasonContext,
		FetchedAt:                 row.FetchedAt.Time,
		SnapshotStatus:            row.SnapshotStatus,
		SnapshotDetail:            row.SnapshotDetail,
		SnapshotFetchedAt:         row.SnapshotFetchedAt.Time,
	}
	if row.SourceScore.Valid {
		result.SourceScore = &row.SourceScore.Float64
	}
	if row.SourceSnapshotID.Valid {
		result.SourceSnapshotID = &row.SourceSnapshotID.String
	}
	if row.SourceSnapshotAt.Valid {
		result.SourceSnapshotAt = &row.SourceSnapshotAt.Time
	}
	return result
}

// DismissalSubject is the exact level at which a permanent dismissal applies.
type DismissalSubject string

const (
	DismissRecording DismissalSubject = "recording"
	DismissRelease   DismissalSubject = "release_group"
	DismissArtist    DismissalSubject = "artist"
)

// Dismiss records one permanent recommendation decision. Repeating it keeps
// the original timestamp because it is the same fact, not a new dismissal.
func (service *Service) Dismiss(
	ctx context.Context, subject DismissalSubject, musicBrainzID uuid.UUID,
) (db.RecommendationDismissal, error) {
	if subject != DismissRecording && subject != DismissRelease && subject != DismissArtist {
		return db.RecommendationDismissal{}, fmt.Errorf("unknown recommendation dismissal subject %q", subject)
	}
	if musicBrainzID == uuid.Nil {
		return db.RecommendationDismissal{}, errors.New("recommendation dismissal MusicBrainz ID is required")
	}
	row, err := service.store.UpsertRecommendationDismissal(ctx, db.UpsertRecommendationDismissalParams{
		SubjectType:   string(subject),
		MusicbrainzID: musicBrainzID,
		DismissedAt:   pgtype.Timestamptz{Time: service.now().UTC(), Valid: true},
	})
	if err != nil {
		return db.RecommendationDismissal{}, fmt.Errorf("record recommendation dismissal: %w", err)
	}
	return row, nil
}

// FeedbackSignal is the soft recommendation opinion recorded by a press.
type FeedbackSignal string

const (
	FeedbackMoreLikeThis FeedbackSignal = "more_like_this"
	FeedbackLessLikeThis FeedbackSignal = "less_like_this"
)

// RecordFeedback copies the source's current candidate into one append-only
// event. The database statement reads and writes it together, so a replaced
// candidate produces no partial event. Each sweep reads only its own source's
// events (ADR 0039 §5).
func (service *Service) RecordFeedback(
	ctx context.Context, source string, recordingID uuid.UUID, signal FeedbackSignal,
) (db.RecommendationFeedback, error) {
	if strings.TrimSpace(source) == "" {
		return db.RecommendationFeedback{}, errors.New("recommendation feedback source is required")
	}
	if recordingID == uuid.Nil {
		return db.RecommendationFeedback{}, errors.New("recommendation feedback recording ID is required")
	}
	if signal != FeedbackMoreLikeThis && signal != FeedbackLessLikeThis {
		return db.RecommendationFeedback{}, fmt.Errorf("unknown recommendation feedback signal %q", signal)
	}
	row, err := service.store.RecordRecommendationFeedback(ctx, db.RecordRecommendationFeedbackParams{
		Signal:                 string(signal),
		Source:                 source,
		MusicbrainzRecordingID: recordingID,
	})
	if err != nil {
		return db.RecommendationFeedback{}, fmt.Errorf("record recommendation feedback: %w", err)
	}
	return row, nil
}

// ClearFeedback removes one source's feedback events. The other source's
// events and every other recommendation decision remain in their stores.
func (service *Service) ClearFeedback(ctx context.Context, source string) error {
	if strings.TrimSpace(source) == "" {
		return errors.New("recommendation feedback source is required")
	}
	if err := service.store.DeleteRecommendationFeedback(ctx, source); err != nil {
		return fmt.Errorf("clear recommendation feedback: %w", err)
	}
	return nil
}

const ignoredImpressionThreshold int32 = 3

const (
	minimumImpressionGap  = 24 * time.Hour
	impressionSuppression = 90 * 24 * time.Hour
)

// RecordImpression counts one eligible candidate actually shown. The database
// transition is atomic; repeated reads inside 24 hours do not count, and the
// first showing after a 90-day suppression starts a new cycle at one.
func (service *Service) RecordImpression(
	ctx context.Context, musicBrainzRecordingID uuid.UUID,
) (db.RecommendationImpression, error) {
	if musicBrainzRecordingID == uuid.Nil {
		return db.RecommendationImpression{}, errors.New("recommendation impression MusicBrainz recording ID is required")
	}
	row, err := service.store.RecordRecommendationImpression(ctx, db.RecordRecommendationImpressionParams{
		MusicbrainzRecordingID:  musicBrainzRecordingID,
		ObservedAt:              pgtype.Timestamptz{Time: service.now().UTC(), Valid: true},
		Threshold:               ignoredImpressionThreshold,
		SuppressionMicroseconds: impressionSuppression.Microseconds(),
		MinimumGapMicroseconds:  minimumImpressionGap.Microseconds(),
	})
	if err != nil {
		return db.RecommendationImpression{}, fmt.Errorf("record recommendation impression: %w", err)
	}
	return row, nil
}

// RecordImpressions counts a showing for each of these recordings that the
// stored list actually offers, and answers how many that was.
//
// The count is what the fifth rule holds a recording back on, so it may only be
// written about a row that existed to be shown. The route that reaches this
// takes any UUID a caller sends, and the list it was read from can be replaced
// by a sweep while somebody is looking at it. A recording the stored list does
// not name is therefore dropped and not refused: it is either a caller with no
// business writing here, or a reader whose page got old, and neither is a
// fault the reader can do anything about.
func (service *Service) RecordImpressions(
	ctx context.Context, musicBrainzRecordingIDs []uuid.UUID,
) (int, error) {
	asked := make([]uuid.UUID, 0, len(musicBrainzRecordingIDs))
	for _, recordingID := range musicBrainzRecordingIDs {
		if recordingID != uuid.Nil {
			asked = append(asked, recordingID)
		}
	}
	if len(asked) == 0 {
		return 0, nil
	}
	listed, err := service.store.ListedRecommendationRecordings(ctx, asked)
	if err != nil {
		return 0, fmt.Errorf("read listed recommendation recordings: %w", err)
	}
	offered := make(map[uuid.UUID]struct{}, len(listed))
	for _, recordingID := range listed {
		offered[recordingID] = struct{}{}
	}

	recorded := 0
	for _, recordingID := range asked {
		if _, known := offered[recordingID]; !known {
			continue
		}
		if _, err := service.RecordImpression(ctx, recordingID); err != nil {
			return recorded, err
		}
		recorded++
	}
	return recorded, nil
}

// Snapshot is one current usable sweep from a recommendation source.
type Snapshot struct {
	Source           string
	SourceSnapshotID string
	SourceSnapshotAt *time.Time
	Status           string
	Detail           string
	FetchedAt        time.Time
	Candidates       []CandidateInput
}

// CandidateInput is one expanded provider answer before source-local duplicate
// rows have been merged and ranked.
type CandidateInput struct {
	MusicBrainzRecordingID    uuid.UUID
	MusicBrainzReleaseGroupID uuid.UUID
	MusicBrainzArtistIDs      []uuid.UUID
	RecordingTitle            string
	ReleaseTitle              string
	ArtistName                string
	SourceScore               *float64
	ReasonCodes               []string
	ReasonContext             map[string]any
}

// ReplaceSnapshot prepares and atomically replaces one source. A successful
// empty sweep clears the old candidates; any validation or database failure
// preserves the previous snapshot in full.
func (service *Service) ReplaceSnapshot(ctx context.Context, snapshot Snapshot) error {
	return service.replaceSnapshot(ctx, snapshot)
}

func (service *Service) replaceSnapshot(
	ctx context.Context, snapshot Snapshot, feedback ...[]db.RecommendationFeedback,
) error {
	snapshot.Source = strings.TrimSpace(snapshot.Source)
	if snapshot.Source == "" {
		return errors.New("recommendation snapshot source is required")
	}
	if snapshot.Status != "complete" && snapshot.Status != "partial" {
		return fmt.Errorf("unknown recommendation snapshot status %q", snapshot.Status)
	}
	if snapshot.Status == "partial" && strings.TrimSpace(snapshot.Detail) == "" {
		return errors.New("partial recommendation snapshot detail is required")
	}
	if snapshot.FetchedAt.IsZero() {
		snapshot.FetchedAt = service.now().UTC()
	} else {
		snapshot.FetchedAt = snapshot.FetchedAt.UTC()
	}

	candidates, err := prepareCandidates(snapshot.Candidates, feedback...)
	if err != nil {
		return err
	}
	header := db.UpsertRecommendationSnapshotParams{
		Source:           snapshot.Source,
		SourceSnapshotID: pgtype.Text{String: strings.TrimSpace(snapshot.SourceSnapshotID), Valid: strings.TrimSpace(snapshot.SourceSnapshotID) != ""},
		Status:           snapshot.Status,
		Detail:           snapshot.Detail,
		FetchedAt:        pgtype.Timestamptz{Time: snapshot.FetchedAt, Valid: true},
	}
	if snapshot.SourceSnapshotAt != nil {
		header.SourceSnapshotAt = pgtype.Timestamptz{Time: snapshot.SourceSnapshotAt.UTC(), Valid: true}
	}
	for index := range candidates {
		candidates[index].Source = snapshot.Source
		candidates[index].FetchedAt = header.FetchedAt
	}
	if err := service.store.ReplaceRecommendationSnapshot(ctx, header, candidates); err != nil {
		return fmt.Errorf("replace recommendation snapshot: %w", err)
	}
	return nil
}

type preparedCandidate struct {
	recordingID    uuid.UUID
	releaseGroupID uuid.UUID
	artistIDs      map[uuid.UUID]struct{}
	recordingTitle string
	releaseTitle   string
	artistName     string
	score          *float64
	effectiveScore *float64
	reasonCodes    map[string]struct{}
	reasonContext  map[string]json.RawMessage
}

const (
	feedbackWeight       = 0.10
	feedbackMinimumDelta = -0.30
	feedbackMaximumDelta = 0.30
)

type feedbackNeighbourhood struct {
	recordingID    uuid.UUID
	releaseGroupID uuid.UUID
	artistIDs      map[uuid.UUID]struct{}
	seedIDs        map[uuid.UUID]struct{}
	sign           float64
}

func prepareCandidates(
	inputs []CandidateInput, feedbackRows ...[]db.RecommendationFeedback,
) ([]db.InsertRecommendationCandidateParams, error) {
	feedback := []feedbackNeighbourhood(nil)
	if len(feedbackRows) > 0 {
		var err error
		feedback, err = prepareFeedback(feedbackRows[0])
		if err != nil {
			return nil, err
		}
	}
	merged := make(map[uuid.UUID]*preparedCandidate, len(inputs))
	for _, input := range inputs {
		if input.MusicBrainzRecordingID == uuid.Nil || input.MusicBrainzReleaseGroupID == uuid.Nil {
			continue
		}
		artists := make(map[uuid.UUID]struct{}, len(input.MusicBrainzArtistIDs))
		for _, artistID := range input.MusicBrainzArtistIDs {
			if artistID != uuid.Nil {
				artists[artistID] = struct{}{}
			}
		}
		recordingTitle := strings.TrimSpace(input.RecordingTitle)
		artistName := strings.TrimSpace(input.ArtistName)
		if len(artists) == 0 || recordingTitle == "" || artistName == "" {
			continue
		}
		if input.SourceScore != nil && (math.IsNaN(*input.SourceScore) || math.IsInf(*input.SourceScore, 0)) {
			return nil, fmt.Errorf("candidate %s has a non-finite source score", input.MusicBrainzRecordingID)
		}

		candidate, exists := merged[input.MusicBrainzRecordingID]
		if !exists {
			candidate = &preparedCandidate{
				recordingID:    input.MusicBrainzRecordingID,
				releaseGroupID: input.MusicBrainzReleaseGroupID,
				artistIDs:      make(map[uuid.UUID]struct{}, len(artists)),
				recordingTitle: recordingTitle,
				releaseTitle:   strings.TrimSpace(input.ReleaseTitle),
				artistName:     artistName,
				reasonCodes:    make(map[string]struct{}),
				reasonContext:  make(map[string]json.RawMessage),
			}
			merged[input.MusicBrainzRecordingID] = candidate
		} else if candidate.releaseGroupID != input.MusicBrainzReleaseGroupID ||
			candidate.recordingTitle != recordingTitle ||
			candidate.releaseTitle != strings.TrimSpace(input.ReleaseTitle) ||
			candidate.artistName != artistName {
			return nil, fmt.Errorf("candidate %s has conflicting expanded metadata", input.MusicBrainzRecordingID)
		}

		for artistID := range artists {
			candidate.artistIDs[artistID] = struct{}{}
		}
		if input.SourceScore != nil && (candidate.score == nil || *input.SourceScore > *candidate.score) {
			score := *input.SourceScore
			candidate.score = &score
		}
		for _, reason := range input.ReasonCodes {
			reason = strings.TrimSpace(reason)
			if reason == "" {
				return nil, fmt.Errorf("candidate %s has an empty reason code", input.MusicBrainzRecordingID)
			}
			candidate.reasonCodes[reason] = struct{}{}
		}
		for key, value := range input.ReasonContext {
			key = strings.TrimSpace(key)
			if key == "" {
				return nil, fmt.Errorf("candidate %s has an empty reason context key", input.MusicBrainzRecordingID)
			}
			encoded, err := json.Marshal(value)
			if err != nil {
				return nil, fmt.Errorf("encode candidate %s reason context %q: %w", input.MusicBrainzRecordingID, key, err)
			}
			if prior, exists := candidate.reasonContext[key]; exists && !bytes.Equal(prior, encoded) {
				return nil, fmt.Errorf("candidate %s has conflicting reason context %q", input.MusicBrainzRecordingID, key)
			}
			candidate.reasonContext[key] = encoded
		}
	}

	prepared := make([]*preparedCandidate, 0, len(merged))
	for _, candidate := range merged {
		if len(candidate.reasonCodes) == 0 {
			return nil, fmt.Errorf("candidate %s has no reason codes", candidate.recordingID)
		}
		if len(feedbackRows) > 0 {
			delete(candidate.reasonCodes, "feedback_more_like_this")
			delete(candidate.reasonCodes, "feedback_less_like_this")
			delta := feedbackDelta(candidate, feedback)
			if delta > 0 {
				candidate.reasonCodes["feedback_more_like_this"] = struct{}{}
			} else if delta < 0 {
				candidate.reasonCodes["feedback_less_like_this"] = struct{}{}
			}
			if candidate.score != nil {
				effective := *candidate.score + delta
				candidate.effectiveScore = &effective
			}
		}
		if candidate.effectiveScore == nil {
			candidate.effectiveScore = candidate.score
		}
		prepared = append(prepared, candidate)
	}
	sort.Slice(prepared, func(left, right int) bool {
		leftScore, rightScore := prepared[left].effectiveScore, prepared[right].effectiveScore
		if leftScore != nil && rightScore != nil && *leftScore != *rightScore {
			return *leftScore > *rightScore
		}
		if leftScore != nil && rightScore == nil {
			return true
		}
		if leftScore == nil && rightScore != nil {
			return false
		}
		return prepared[left].recordingID.String() < prepared[right].recordingID.String()
	})

	result := make([]db.InsertRecommendationCandidateParams, 0, len(prepared))
	for index, candidate := range prepared {
		artistIDs := make([]uuid.UUID, 0, len(candidate.artistIDs))
		for artistID := range candidate.artistIDs {
			artistIDs = append(artistIDs, artistID)
		}
		sort.Slice(artistIDs, func(left, right int) bool {
			return artistIDs[left].String() < artistIDs[right].String()
		})
		reasonCodes := make([]string, 0, len(candidate.reasonCodes))
		for reason := range candidate.reasonCodes {
			reasonCodes = append(reasonCodes, reason)
		}
		sort.Strings(reasonCodes)
		reasonContext, err := json.Marshal(candidate.reasonContext)
		if err != nil {
			return nil, fmt.Errorf("encode candidate %s reason context: %w", candidate.recordingID, err)
		}
		row := db.InsertRecommendationCandidateParams{
			MusicbrainzRecordingID:    candidate.recordingID,
			MusicbrainzReleaseGroupID: candidate.releaseGroupID,
			MusicbrainzArtistIds:      artistIDs,
			RecordingTitle:            candidate.recordingTitle,
			ReleaseTitle:              candidate.releaseTitle,
			ArtistName:                candidate.artistName,
			Rank:                      int32(index + 1),
			ReasonCodes:               reasonCodes,
			ReasonContext:             reasonContext,
		}
		if candidate.score != nil {
			row.SourceScore = pgtype.Float8{Float64: *candidate.score, Valid: true}
		}
		result = append(result, row)
	}
	return result, nil
}

func prepareFeedback(rows []db.RecommendationFeedback) ([]feedbackNeighbourhood, error) {
	prepared := make([]feedbackNeighbourhood, 0, len(rows))
	for _, row := range rows {
		context := make(map[string]json.RawMessage)
		if len(row.ReasonContext) > 0 {
			if err := json.Unmarshal(row.ReasonContext, &context); err != nil {
				return nil, fmt.Errorf("decode recommendation feedback %s context: %w", row.ID, err)
			}
		}
		sign := 0.0
		switch row.Signal {
		case string(FeedbackMoreLikeThis):
			sign = 1
		case string(FeedbackLessLikeThis):
			sign = -1
		default:
			return nil, fmt.Errorf("unknown recommendation feedback signal %q", row.Signal)
		}
		prepared = append(prepared, feedbackNeighbourhood{
			recordingID:    row.MusicbrainzRecordingID,
			releaseGroupID: row.MusicbrainzReleaseGroupID,
			artistIDs:      uuidSet(row.MusicbrainzArtistIds),
			seedIDs:        contextSeedIDs(context),
			sign:           sign,
		})
	}
	return prepared, nil
}

func uuidSet(ids []uuid.UUID) map[uuid.UUID]struct{} {
	result := make(map[uuid.UUID]struct{}, len(ids))
	for _, id := range ids {
		if id != uuid.Nil {
			result[id] = struct{}{}
		}
	}
	return result
}

func contextSeedIDs(context map[string]json.RawMessage) map[uuid.UUID]struct{} {
	var values []string
	if raw, ok := context["seed_recording_ids"]; ok {
		if err := json.Unmarshal(raw, &values); err != nil {
			return nil
		}
	}
	result := make(map[uuid.UUID]struct{}, len(values))
	for _, value := range values {
		id, err := uuid.Parse(value)
		if err == nil {
			result[id] = struct{}{}
		}
	}
	return result
}

func feedbackDelta(candidate *preparedCandidate, feedback []feedbackNeighbourhood) float64 {
	delta := 0.0
	candidateArtists := candidate.artistIDs
	candidateSeeds := contextSeedIDs(candidate.reasonContext)
	for _, row := range feedback {
		if row.recordingID == candidate.recordingID {
			continue
		}
		dimensions := 0
		if sharesUUID(candidateSeeds, row.seedIDs) {
			dimensions++
		}
		if candidate.releaseGroupID == row.releaseGroupID {
			dimensions++
		}
		if sharesUUID(candidateArtists, row.artistIDs) {
			dimensions++
		}
		delta += row.sign * feedbackWeight * float64(dimensions)
	}
	return min(max(delta, feedbackMinimumDelta), feedbackMaximumDelta)
}

func sharesUUID(left, right map[uuid.UUID]struct{}) bool {
	for id := range left {
		if _, ok := right[id]; ok {
			return true
		}
	}
	return false
}
