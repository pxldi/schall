package acquisition

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/dbtest"
	"github.com/pxldi/schall/internal/identity"
	"github.com/pxldi/schall/internal/musicbrainz"
)

// stubProvider stands where MusicBrainz does. Everything above it — the resolver
// and the grading rules that decide what may be concluded — is the real thing, so
// these tests exercise what a real answer does to a target rather than what a
// hand-written answer does.
//
// It records the queries it was asked, because those are the contract at this
// boundary: the requests are rate limited and shared with the interactive search,
// so an entry already answered must not cost one.
type stubProvider struct {
	results  []musicbrainz.Recording
	err      error
	searches []musicbrainz.RecordingQuery
	lookups  []uuid.UUID
}

func (provider *stubProvider) Recording(
	_ context.Context, id uuid.UUID,
) (musicbrainz.Recording, error) {
	provider.lookups = append(provider.lookups, id)
	return musicbrainz.Recording{}, musicbrainz.ErrNotFound
}

func (provider *stubProvider) ResolveRecordingID(
	_ context.Context, id uuid.UUID,
) (uuid.UUID, error) {
	provider.lookups = append(provider.lookups, id)
	return uuid.Nil, musicbrainz.ErrNotFound
}

func (provider *stubProvider) SearchRecordings(
	_ context.Context, query musicbrainz.RecordingQuery, _ int,
) ([]musicbrainz.Recording, error) {
	provider.searches = append(provider.searches, query)
	return provider.results, provider.err
}

// recording is one MusicBrainz recording as the provider would return it.
func recording(id uuid.UUID, title, artist string, durationMS int, isrcs ...string) musicbrainz.Recording {
	return musicbrainz.Recording{
		ID: id, Title: title, ArtistCredit: artist, DurationMS: &durationMS, ISRCs: isrcs,
		Releases: []musicbrainz.RecordingRelease{
			{ID: uuid.New(), ReleaseGroupID: uuid.New(), Title: "Dummy", Status: "Official"},
		},
	}
}

// withProvider gives the service the real resolver over a stubbed provider.
func withProvider(service *Service, provider *stubProvider) *Service {
	return service.WithResolver(identity.NewResolver(provider))
}

// The answer B exists to produce. One recording, written into the want, which
// becomes actionable immediately rather than after a wait.
func TestAResolvedEntryBecomesAWantWithARecording(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	recordingID := uuid.New()
	found := recording(recordingID, "Glory Box", "Portishead", 301000, "GBAAA9400123")
	provider := &stubProvider{results: []musicbrainz.Recording{found}}
	service := withProvider(newTestService(pool), provider)
	target := createEntry(ctx, t, service)

	// Resolution alone, so the state it leaves behind can be read before the
	// acquisition pass that follows it in a full sweep touches the counters.
	if err := service.resolveDue(ctx); err != nil {
		t.Fatalf("resolveDue() error = %v", err)
	}

	after, err := service.Target(ctx, target.ID)
	if err != nil {
		t.Fatalf("Target() error = %v", err)
	}
	if after.Status != "pending" {
		t.Fatalf("status = %s, want pending", after.Status)
	}
	if !after.MusicBrainzRecordingID.Valid || after.MusicBrainzRecordingID.UUID != recordingID {
		t.Fatalf("recording = %v, want %s", after.MusicBrainzRecordingID, recordingID)
	}
	if after.MusicBrainzReleaseGroupID.UUID != found.Releases[0].ReleaseGroupID {
		t.Fatalf("release group = %v, want the release the resolution ran through",
			after.MusicBrainzReleaseGroupID)
	}
	if after.ResolutionMethod.String != "title-artist-duration" {
		t.Fatalf("method = %q, want the evidence that concluded it", after.ResolutionMethod.String)
	}
	// The ladder measures how long a want has gone unmet by peers, and no peer
	// has been asked yet.
	if after.Attempts != 0 {
		t.Fatalf("attempts = %d, want the acquisition ladder to start unclimbed", after.Attempts)
	}
	if !after.NextAttemptAt.Valid {
		t.Fatal("a resolved want is not scheduled to be looked for")
	}
	// The attempt that resolved it is kept, numbered as it stood before the reset.
	if outcome := lastAttempt(ctx, t, pool, target.ID); outcome != "resolved" {
		t.Fatalf("attempt outcome = %q, want resolved", outcome)
	}
}

// A want that has just been resolved is due immediately, so the acquisition pass
// of the same sweep looks for it. An entry asked for now can be a file in the
// library without waiting out a ladder rung it never earned.
func TestAResolvedEntryIsLookedForInTheSameSweep(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	recordingID := uuid.New()
	service := withProvider(newTestService(pool), &stubProvider{
		results: []musicbrainz.Recording{
			recording(recordingID, "Glory Box", "Portishead", 301000),
		},
	})
	fileID := seedIdentifiedFile(ctx, t, pool, "/music/glory-box.flac", recordingID)
	target := createEntry(ctx, t, service)

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	after, err := service.Target(ctx, target.ID)
	if err != nil {
		t.Fatalf("Target() error = %v", err)
	}
	if after.Status != "acquired" {
		t.Fatalf("status = %s, want the copy the library already holds to satisfy it", after.Status)
	}
	if after.AcquiredLibraryFileID.UUID != fileID {
		t.Fatalf("acquired file = %v, want %s", after.AcquiredLibraryFileID, fileID)
	}
}

// The refusal this whole item is built around: several plausible recordings are a
// question for a person, stored with what stood for and against each, and nothing
// is written into the target.
func TestAnInconclusiveEntryBecomesAQuestionWithItsEvidence(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	// A remaster the same length as the original, which is the ordinary case here
	// rather than a rare one. Both reach the weakest conclusive grade, so neither
	// may be taken.
	first, second := uuid.New(), uuid.New()
	service := withProvider(newTestService(pool), &stubProvider{
		results: []musicbrainz.Recording{
			recording(first, "Glory Box", "Portishead", 301000),
			recording(second, "Glory Box", "Portishead", 301500),
		},
	})
	target := createEntry(ctx, t, service)

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	after, err := service.Target(ctx, target.ID)
	if err != nil {
		t.Fatalf("Target() error = %v", err)
	}
	if after.Status != "awaiting_review" {
		t.Fatalf("status = %s, want awaiting_review", after.Status)
	}
	if after.ReviewReason.String != "resolution" {
		t.Fatalf("review reason = %q, want the question to name itself", after.ReviewReason.String)
	}
	if after.MusicBrainzRecordingID.Valid {
		t.Fatalf("recording = %v, want nothing written while the answer is unknown",
			after.MusicBrainzRecordingID)
	}
	// A question waiting on a person is not on a timer. Re-asking would replace
	// the candidates they are reading with a fresh copy of the same ones.
	if after.NextAttemptAt.Valid {
		t.Fatalf("next attempt = %v, want a question to wait for its answer", after.NextAttemptAt)
	}

	candidates, err := service.Candidates(ctx, target.ID)
	if err != nil {
		t.Fatalf("Candidates() error = %v", err)
	}
	if len(candidates) != 2 {
		t.Fatalf("candidates = %d, want both plausible recordings", len(candidates))
	}
	offered := map[uuid.UUID]bool{}
	for _, candidate := range candidates {
		offered[candidate.MusicBrainzRecordingID] = true
		if candidate.Rank < 1 {
			t.Fatalf("rank = %d, want the order the attempt ranked them", candidate.Rank)
		}
		if len(candidate.Agrees) == 0 || candidate.Summary == "" {
			t.Fatalf("candidate = %+v, want what stood for it kept", candidate)
		}
	}
	if !offered[first] || !offered[second] {
		t.Fatalf("candidates = %v, want both plausible recordings", candidates)
	}
}

// A second attempt replaces the candidates rather than adding to them. They
// describe what the provider said this time, and a stale one would be a second
// opinion nobody asked for.
func TestASecondAttemptReplacesTheCandidatesItOffered(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	stale, kept, fresh := uuid.New(), uuid.New(), uuid.New()
	provider := &stubProvider{results: contested(stale, kept)}
	service := withProvider(newTestService(pool), provider)
	target := createEntry(ctx, t, service)
	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	// Asking again is what the review queue's re-resolution will do.
	if _, err := pool.Exec(ctx, `
		UPDATE acquisition_targets
		SET status = 'unresolved', review_reason = NULL, next_attempt_at = now()
		WHERE id = $1
	`, target.ID); err != nil {
		t.Fatalf("reopen the question: %v", err)
	}
	provider.results = contested(kept, fresh)
	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	candidates, err := service.Candidates(ctx, target.ID)
	if err != nil {
		t.Fatalf("Candidates() error = %v", err)
	}
	current := make(map[uuid.UUID]bool, len(candidates))
	for _, candidate := range candidates {
		current[candidate.MusicBrainzRecordingID] = true
	}
	if len(candidates) != 2 || !current[kept] || !current[fresh] || current[stale] {
		t.Fatalf("candidates = %v, want exactly what the latest attempt offered", candidates)
	}
}

// Nothing found is not a question a person can answer — there is nothing to
// choose between — and it is not permanent either. The entry stays unresolved and
// is asked about again.
func TestAnEntryNothingMatchesIsAskedAboutAgain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	service := withProvider(newTestService(pool), &stubProvider{})
	target := createEntry(ctx, t, service)
	now := time.Now().Add(time.Minute)
	service.now = func() time.Time { return now }

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	after, err := service.Target(ctx, target.ID)
	if err != nil {
		t.Fatalf("Target() error = %v", err)
	}
	if after.Status != "unresolved" {
		t.Fatalf("status = %s, want it to stay unresolved", after.Status)
	}
	if after.ReviewReason.Valid {
		t.Fatalf("review reason = %q, want no question with nothing to choose between",
			after.ReviewReason.String)
	}
	if after.Attempts != 1 {
		t.Fatalf("attempts = %d, want 1", after.Attempts)
	}
	if !after.NextAttemptAt.Valid || after.NextAttemptAt.Time.Before(now.Add(14*time.Minute)) {
		t.Fatalf("next attempt = %v, want about a quarter hour after %v", after.NextAttemptAt, now)
	}
	if outcome := lastAttempt(ctx, t, pool, target.ID); outcome != "none" {
		t.Fatalf("attempt outcome = %q, want none", outcome)
	}
}

// A provider that cannot be reached is about Schall, never about the music. It is
// recorded as an error, never as a question, and the entry is tried again.
func TestAProviderThatCannotBeReachedIsNotAQuestion(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	service := withProvider(newTestService(pool),
		&stubProvider{err: errors.New("connection refused")})
	target := createEntry(ctx, t, service)

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v, want one unreachable provider not to stop the sweeper", err)
	}

	after, err := service.Target(ctx, target.ID)
	if err != nil {
		t.Fatalf("Target() error = %v", err)
	}
	if after.Status != "unresolved" {
		t.Fatalf("status = %s, want it to stay unresolved", after.Status)
	}
	if after.LastError.String == "" {
		t.Fatal("nothing recorded about why the entry could not be resolved")
	}
	if !after.NextAttemptAt.Valid {
		t.Fatal("an entry nobody could ask about is not scheduled to be asked about again")
	}
	candidates, err := service.Candidates(ctx, target.ID)
	if err != nil {
		t.Fatalf("Candidates() error = %v", err)
	}
	if len(candidates) != 0 {
		t.Fatalf("candidates = %d, want a failure to ask to offer nothing", len(candidates))
	}
	if outcome := lastAttempt(ctx, t, pool, target.ID); outcome != "failed" {
		t.Fatalf("attempt outcome = %q, want failed", outcome)
	}
}

// A provider refusing for load is not an attempt. MusicBrainz said "not now"
// rather than anything about the music, so the entry keeps its place on the
// ladder and only the time it comes back at moves. Spending an attempt on it
// would send an entry that has never once been looked up towards the day-long
// rungs on nothing but the provider's patience.
func TestAnEntryRefusedForLoadKeepsItsPlaceOnTheLadder(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	service := withProvider(newTestService(pool), &stubProvider{
		err: fmt.Errorf("%w: MusicBrainz returned HTTP 503", musicbrainz.ErrThrottled),
	})
	target := createEntry(ctx, t, service)
	// After the entry was written, so it is due, and at the precision the column
	// keeps.
	now := time.Now().Add(time.Minute).UTC().Truncate(time.Microsecond)
	service.now = func() time.Time { return now }

	if err := service.resolveDue(ctx); err != nil {
		t.Fatalf("resolveDue() error = %v", err)
	}

	after, err := service.Target(ctx, target.ID)
	if err != nil {
		t.Fatalf("Target() error = %v", err)
	}
	if after.Status != "unresolved" {
		t.Fatalf("status = %s, want it to stay unresolved", after.Status)
	}
	if after.Attempts != 0 {
		t.Fatalf("attempts = %d, want a refusal for load to cost the entry nothing", after.Attempts)
	}
	if !after.NextAttemptAt.Valid || !after.NextAttemptAt.Time.Equal(now.Add(throttleDelay)) {
		t.Fatalf("next attempt = %v, want %v — shortly, not a rung of the ladder",
			after.NextAttemptAt, now.Add(throttleDelay))
	}
}

// The rest of the pass hears the refusal too. It is about the provider's patience
// rather than any one entry, so the entries behind it stand back with it instead
// of each finding out for themselves — which is the very pace that earned the
// refusal.
func TestTheRestOfARefusedPassStandsBackWithoutAsking(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	provider := &stubProvider{
		err: fmt.Errorf("%w: MusicBrainz returned HTTP 503", musicbrainz.ErrThrottled),
	}
	service := withProvider(newTestService(pool), provider)
	first := createEntry(ctx, t, service)
	second := createEntry(ctx, t, service)

	if err := service.resolveDue(ctx); err != nil {
		t.Fatalf("resolveDue() error = %v", err)
	}

	if len(provider.searches) != 1 {
		t.Fatalf("searches = %d, want the pass to stop asking after the first refusal",
			len(provider.searches))
	}
	for _, id := range []uuid.UUID{first.ID, second.ID} {
		after, err := service.Target(ctx, id)
		if err != nil {
			t.Fatalf("Target() error = %v", err)
		}
		if after.Attempts != 0 || after.Status != "unresolved" {
			t.Fatalf("target %s = %s after %d attempts, want it waiting and unspent",
				id, after.Status, after.Attempts)
		}
	}
}

// Two entries that resolve to one recording are one want. The loser keeps its own
// entry and points at the survivor, so whatever asked for it can follow the trail.
func TestTwoEntriesResolvingToOneRecordingBecomeOneWant(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	recordingID := uuid.New()
	service := withProvider(newTestService(pool), &stubProvider{
		results: []musicbrainz.Recording{
			recording(recordingID, "Glory Box", "Portishead", 301000),
		},
	})
	first := createEntry(ctx, t, service)
	second := createEntry(ctx, t, service)

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	survivor, err := service.Target(ctx, first.ID)
	if err != nil {
		t.Fatalf("Target() error = %v", err)
	}
	merged, err := service.Target(ctx, second.ID)
	if err != nil {
		t.Fatalf("Target() error = %v", err)
	}
	if survivor.Status != "pending" {
		t.Fatalf("survivor status = %s, want pending", survivor.Status)
	}
	if merged.Status != "superseded" {
		t.Fatalf("merged status = %s, want superseded", merged.Status)
	}
	if !merged.SupersededByID.Valid || merged.SupersededByID.UUID != first.ID {
		t.Fatalf("superseded by = %v, want %s", merged.SupersededByID, first.ID)
	}
	if merged.EntryTitle != second.EntryTitle {
		t.Fatalf("entry = %q, want the loser to keep what was asked for", merged.EntryTitle)
	}
	if merged.NextAttemptAt.Valid {
		t.Fatalf("next attempt = %v, want one want to be looked for, not two", merged.NextAttemptAt)
	}
}

// A decision to stop pursuing a recording sticks. An entry that resolves to it —
// from a re-imported playlist, or a different one entirely — is merged into that
// decision rather than quietly resurrecting the want, and says so.
func TestAnEntryResolvingToSomethingNotWantedStaysNotWanted(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	recordingID := uuid.New()
	service := withProvider(newTestService(pool), &stubProvider{
		results: []musicbrainz.Recording{
			recording(recordingID, "Glory Box", "Portishead", 301000),
		},
	})

	refused, _, err := service.Create(ctx, Entry{
		Origin: "manual", Artist: "Portishead", Title: "Glory Box",
		RecordingID: uuid.NullUUID{UUID: recordingID, Valid: true},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if _, err := service.StopPursuing(ctx, refused.ID); err != nil {
		t.Fatalf("StopPursuing() error = %v", err)
	}

	entry := createEntry(ctx, t, service)
	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	merged, err := service.Target(ctx, entry.ID)
	if err != nil {
		t.Fatalf("Target() error = %v", err)
	}
	if merged.Status != "superseded" || merged.SupersededByID.UUID != refused.ID {
		t.Fatalf("target = %+v, want it merged into the decision to stop", merged)
	}
	if merged.Summary != supersededNotWantedSummary {
		t.Fatalf("summary = %q, want it to say the recording was not wanted", merged.Summary)
	}
	stopped, err := service.Target(ctx, refused.ID)
	if err != nil {
		t.Fatalf("Target() error = %v", err)
	}
	if stopped.Status != "not_wanted" {
		t.Fatalf("status = %s, want the decision untouched", stopped.Status)
	}
}

// A target somebody handed a recording has already been resolved, by them. Asking
// the provider about it would spend a rate-limited request to second-guess a
// person.
func TestATargetSomebodyResolvedIsNotResolvedAgain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	provider := &stubProvider{}
	service := withProvider(newTestService(pool), provider)
	if _, _, err := service.Create(ctx, Entry{
		Origin: "manual", Artist: "Portishead", Title: "Glory Box",
		RecordingID: uuid.NullUUID{UUID: uuid.New(), Valid: true},
	}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	if len(provider.searches) != 0 || len(provider.lookups) != 0 {
		t.Fatalf("searches = %v lookups = %v, want a resolved target left alone",
			provider.searches, provider.lookups)
	}
}

// The entry reaches the resolver as the words somebody wrote, and as an entry
// rather than a file, because the sentences it produces are shown to them.
func TestTheEntryReachesTheResolverVerbatim(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	provider := &stubProvider{}
	service := withProvider(newTestService(pool), provider)
	duration := int32(301000)
	if _, _, err := service.Create(ctx, Entry{
		Origin: "playlist", Artist: "Portishead", Title: "Glory Box - Remastered 2011",
		Album: "Dummy", DurationMS: &duration, ISRC: "gbaaa9400123",
	}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	if len(provider.searches) != 2 {
		t.Fatalf("searches = %v, want the identifier asked about before the text",
			provider.searches)
	}
	// The ISRC goes first and alone, because it names one recording. Uppercased,
	// so an identifier is compared as the exact thing it is.
	if provider.searches[0].ISRC != "GBAAA9400123" {
		t.Fatalf("first search = %+v, want the ISRC alone", provider.searches[0])
	}
	// The title reaches the provider as it was written. Schall does not tidy up an
	// entry before asking: a search string is a guess about what to look at, and
	// the words somebody used are the best one available.
	if provider.searches[1].Title != "Glory Box - Remastered 2011" {
		t.Fatalf("second search = %+v, want the entry as it was written", provider.searches[1])
	}
	if provider.searches[1].Artist != "Portishead" {
		t.Fatalf("second search = %+v", provider.searches[1])
	}
}

// Resolution is bounded because it costs rate-limited requests, and what it did
// not reach is due now rather than in an hour: the sweeper carries straight on
// instead of quietly capping how much can be wanted at once.
func TestAFullResolutionBatchLeavesTheRestDue(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	provider := &stubProvider{}
	service := withProvider(newTestService(pool), provider)
	for range resolveBatch + 3 {
		createEntry(ctx, t, service)
	}
	now := time.Now().Add(time.Minute)
	service.now = func() time.Time { return now }

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	if len(provider.searches) != resolveBatch {
		t.Fatalf("searches = %d, want one bounded batch of %d",
			len(provider.searches), resolveBatch)
	}
	nextDue, err := service.NextDue(ctx)
	if err != nil {
		t.Fatalf("NextDue() error = %v", err)
	}
	if nextDue.After(now) {
		t.Fatalf("next sweep = %v, want the rest due by %v", nextDue, now)
	}
}

// An installation with no resolver leaves entries alone rather than guessing at
// them, and its sweeps still do the work they can.
func TestWithoutAResolverAnEntryWaits(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	service := newTestService(pool)
	target := createEntry(ctx, t, service)
	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	after, err := service.Target(ctx, target.ID)
	if err != nil {
		t.Fatalf("Target() error = %v", err)
	}
	if after.Status != "unresolved" || after.Attempts != 0 {
		t.Fatalf("target = %+v, want it untouched", after)
	}
}

// contested is two recordings the evidence cannot choose between: same artist,
// same title, lengths a rounding error apart.
func contested(recordingIDs ...uuid.UUID) []musicbrainz.Recording {
	found := make([]musicbrainz.Recording, 0, len(recordingIDs))
	for index, recordingID := range recordingIDs {
		found = append(found,
			recording(recordingID, "Glory Box", "Portishead", 301000+index*500))
	}
	return found
}

// createEntry records a want nobody has resolved yet: text alone, which is what a
// playlist entry is.
func createEntry(
	ctx context.Context, t *testing.T, service *Service,
) db.AcquisitionTargetRow {
	t.Helper()

	duration := int32(301000)
	target, _, err := service.Create(ctx, Entry{
		Origin: "playlist", Artist: "Portishead", Title: "Glory Box", Album: "Dummy",
		DurationMS: &duration,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if target.Status != "unresolved" {
		t.Fatalf("status = %s, want an entry to start unresolved", target.Status)
	}
	return target
}

func lastAttempt(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool, targetID uuid.UUID,
) string {
	t.Helper()

	var outcome string
	if err := pool.QueryRow(ctx, `
		SELECT outcome
		FROM acquisition_target_attempts
		WHERE acquisition_target_id = $1
		ORDER BY recorded_at DESC, attempt DESC
		LIMIT 1
	`, targetID).Scan(&outcome); err != nil {
		t.Fatalf("read the last attempt: %v", err)
	}
	return outcome
}

// An installation with no resolver must not be told an entry is due. The sweeper
// schedules itself from that answer, so counting work nothing can do would wake
// it forever to do nothing.
func TestWithoutAResolverAnEntryIsNotDue(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	service := newTestService(pool)
	createEntry(ctx, t, service)

	nextDue, err := service.NextDue(ctx)
	if err != nil {
		t.Fatalf("NextDue() error = %v", err)
	}
	if !nextDue.IsZero() {
		t.Fatalf("next sweep = %v, want none for work nothing can do", nextDue)
	}
}

// With a resolver, an entry waiting to be resolved is due, or nothing would ever
// pick it up.
func TestAnEntryWaitingToBeResolvedIsDue(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	service := withProvider(newTestService(pool), &stubProvider{})
	createEntry(ctx, t, service)

	nextDue, err := service.NextDue(ctx)
	if err != nil {
		t.Fatalf("NextDue() error = %v", err)
	}
	if nextDue.IsZero() {
		t.Fatal("an entry waiting to be resolved is not due")
	}
}

// A decision taken while a pass is running stands. The resolver is slow — it is
// waiting on a rate-limited provider — so a user stopping a target mid-attempt is
// ordinary rather than exotic, and the answer that arrives afterwards must not be
// written over what they decided.
func TestADecisionTakenDuringResolutionStands(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	recordingID := uuid.New()
	service := newTestService(pool)
	target := createEntry(ctx, t, service)

	// The decision lands while the provider is being waited on, which is where
	// the window actually is.
	service.WithResolver(identity.NewResolver(&decidingProvider{
		found: []musicbrainz.Recording{
			recording(recordingID, "Glory Box", "Portishead", 301000),
		},
		decide: func() {
			if _, err := service.StopPursuing(ctx, target.ID); err != nil {
				t.Errorf("StopPursuing() error = %v", err)
			}
		},
	}))

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v, want the decision to settle the attempt quietly", err)
	}

	after, err := service.Target(ctx, target.ID)
	if err != nil {
		t.Fatalf("Target() error = %v", err)
	}
	if after.Status != "not_wanted" {
		t.Fatalf("status = %s, want the decision to stand", after.Status)
	}
	if after.MusicBrainzRecordingID.Valid {
		t.Fatalf("recording = %v, want nothing written over the decision",
			after.MusicBrainzRecordingID)
	}
	if after.NextAttemptAt.Valid {
		t.Fatalf("next attempt = %v, want a stopped target left alone", after.NextAttemptAt)
	}
}

// decidingProvider lets something happen while the provider is being waited on.
type decidingProvider struct {
	found  []musicbrainz.Recording
	decide func()
	once   bool
}

func (provider *decidingProvider) Recording(
	context.Context, uuid.UUID,
) (musicbrainz.Recording, error) {
	return musicbrainz.Recording{}, musicbrainz.ErrNotFound
}

func (provider *decidingProvider) ResolveRecordingID(
	context.Context, uuid.UUID,
) (uuid.UUID, error) {
	return uuid.Nil, musicbrainz.ErrNotFound
}

func (provider *decidingProvider) SearchRecordings(
	_ context.Context, _ musicbrainz.RecordingQuery, _ int,
) ([]musicbrainz.Recording, error) {
	if !provider.once {
		provider.once = true
		provider.decide()
	}
	return provider.found, nil
}

// The wrong-target outcome end to end. Once a person says an entry does not name
// the recording it was resolved to, asking again must not return that recording —
// otherwise the outcome is a button that undoes itself on the next sweep.
func TestAnEntryIsNeverResolvedToARecordingSomebodyRuledOut(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	recordingID := uuid.New()
	provider := &stubProvider{results: []musicbrainz.Recording{
		recording(recordingID, "Glory Box", "Portishead", 301000),
	}}
	service := withProvider(newTestService(pool), provider)
	target := createEntry(ctx, t, service)

	if err := service.resolveDue(ctx); err != nil {
		t.Fatalf("resolveDue() error = %v", err)
	}
	resolved, err := service.Target(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !resolved.MusicBrainzRecordingID.Valid || resolved.MusicBrainzRecordingID.UUID != recordingID {
		t.Fatalf("recording = %v, want the entry resolved to %s first",
			resolved.MusicBrainzRecordingID, recordingID)
	}

	if _, err := service.RejectResolution(ctx, target.ID); err != nil {
		t.Fatalf("RejectResolution() error = %v", err)
	}
	if err := service.resolveDue(ctx); err != nil {
		t.Fatalf("resolveDue() after the rejection error = %v", err)
	}

	after, err := service.Target(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.MusicBrainzRecordingID.Valid {
		t.Fatalf("recording = %s, want the rejected recording never proposed again",
			after.MusicBrainzRecordingID.UUID)
	}
}

// Resolution is the first half of a sweep, so a pass that could not even read the
// entries waiting to be resolved has to say so rather than carry on and report a
// round in which nothing needed resolving.
func TestASweepThatCannotReadTheEntriesToResolveReportsIt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	service := withProvider(newTestService(pool), &stubProvider{})
	createEntry(ctx, t, service)

	stopped, cancelSweep := context.WithCancel(ctx)
	cancelSweep()

	if err := service.Sweep(stopped); err == nil {
		t.Fatal("a sweep that could not read the entries to resolve reported success")
	}
}

// A recording MusicBrainz lists on no release still names music. The want is the
// recording; the release is only the context a copy of it would be filed under,
// and its absence is silence rather than a reason to refuse the answer.
func TestARecordingOnNoReleaseStillResolvesTheEntry(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	recordingID := uuid.New()
	duration := 301000
	service := withProvider(newTestService(pool), &stubProvider{
		results: []musicbrainz.Recording{{
			ID: recordingID, Title: "Glory Box", ArtistCredit: "Portishead",
			DurationMS: &duration,
		}},
	})
	target := createEntry(ctx, t, service)

	if err := service.resolveDue(ctx); err != nil {
		t.Fatalf("resolveDue() error = %v", err)
	}

	after, err := service.Target(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !after.MusicBrainzRecordingID.Valid || after.MusicBrainzRecordingID.UUID != recordingID {
		t.Fatalf("recording = %v, want %s", after.MusicBrainzRecordingID, recordingID)
	}
	if after.MusicBrainzReleaseGroupID.Valid {
		t.Fatalf("release group = %v, want nothing claimed about a release nobody named",
			after.MusicBrainzReleaseGroupID)
	}
}

// A question is stored with whatever the provider said and no more. A candidate
// MusicBrainz gives no length and no release for is offered exactly that way,
// rather than being padded with values nobody supplied.
func TestACandidateNobodySaidTheLengthOfIsOfferedWithoutOne(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	first, second := uuid.New(), uuid.New()
	service := withProvider(newTestService(pool), &stubProvider{
		results: []musicbrainz.Recording{
			{ID: first, Title: "Glory Box", ArtistCredit: "Portishead"},
			{ID: second, Title: "Glory Box", ArtistCredit: "Portishead"},
		},
	})
	target := createEntry(ctx, t, service)

	if err := service.resolveDue(ctx); err != nil {
		t.Fatalf("resolveDue() error = %v", err)
	}

	candidates, err := service.Candidates(ctx, target.ID)
	if err != nil {
		t.Fatalf("Candidates() error = %v", err)
	}
	if len(candidates) != 2 {
		t.Fatalf("candidates = %d, want both recordings the entry could name", len(candidates))
	}
	for _, candidate := range candidates {
		if candidate.DurationMS != nil {
			t.Errorf("candidate %s has length %d, want nothing claimed about one nobody gave",
				candidate.MusicBrainzRecordingID, *candidate.DurationMS)
		}
		if candidate.MusicBrainzReleaseGroupID != nil {
			t.Errorf("candidate %s names release %s, want nothing claimed about one nobody gave",
				candidate.MusicBrainzRecordingID, *candidate.MusicBrainzReleaseGroupID)
		}
	}
}
