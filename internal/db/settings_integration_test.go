package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pxldi/schall/internal/dbtest"
)

func TestSlskdSettingsLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	queries := New(pool)
	if _, err := queries.SlskdSettings(ctx); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("unconfigured settings error = %v, want pgx.ErrNoRows", err)
	}

	stored, err := queries.SaveSlskdSettings(ctx, SaveSlskdSettingsParams{
		BaseURL: "http://slskd:5030", APIKey: "first-key", Enabled: true, SearchTimeoutSeconds: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if stored.APIKey != "first-key" || stored.ConnectionStatus != "unknown" {
		t.Fatalf("stored = %#v", stored)
	}

	checked, err := queries.RecordSlskdConnection(ctx, "ok", "slskd 0.22.1, Soulseek Connected, LoggedIn", "")
	if err != nil {
		t.Fatal(err)
	}
	if checked.ConnectionStatus != "ok" || !checked.LastCheckedAt.Valid || checked.ConnectionError.Valid {
		t.Fatalf("checked = %#v", checked)
	}

	// An unchanged endpoint with an omitted key keeps both the key and the
	// verified status, so toggling an unrelated field does not force a recheck.
	kept, err := queries.SaveSlskdSettings(ctx, SaveSlskdSettingsParams{
		BaseURL: "http://slskd:5030", APIKey: "", Enabled: false, SearchTimeoutSeconds: 60,
	})
	if err != nil {
		t.Fatal(err)
	}
	if kept.APIKey != "first-key" {
		t.Fatalf("API key = %q, want the stored key retained", kept.APIKey)
	}
	if kept.ConnectionStatus != "ok" || !kept.LastCheckedAt.Valid {
		t.Fatalf("unchanged endpoint should keep its verified status: %#v", kept)
	}
	if kept.Enabled || kept.SearchTimeoutSeconds != 60 {
		t.Fatalf("kept = %#v", kept)
	}

	// Changing the endpoint invalidates the recorded status, because the
	// previous check says nothing about the new one.
	moved, err := queries.SaveSlskdSettings(ctx, SaveSlskdSettingsParams{
		BaseURL: "http://other:5030", APIKey: "", Enabled: true, SearchTimeoutSeconds: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if moved.ConnectionStatus != "unknown" || moved.LastCheckedAt.Valid || moved.ConnectionDetail.Valid {
		t.Fatalf("moved = %#v", moved)
	}
	if moved.APIKey != "first-key" {
		t.Fatalf("API key = %q, want the stored key retained", moved.APIKey)
	}

	rotated, err := queries.SaveSlskdSettings(ctx, SaveSlskdSettingsParams{
		BaseURL: "http://other:5030", APIKey: "second-key", Enabled: true, SearchTimeoutSeconds: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rotated.APIKey != "second-key" || rotated.ConnectionStatus != "unknown" {
		t.Fatalf("rotated = %#v", rotated)
	}

	failed, err := queries.RecordSlskdConnection(ctx, "failed", "", "slskd rejected the API key")
	if err != nil {
		t.Fatal(err)
	}
	if failed.ConnectionStatus != "failed" || failed.ConnectionError.String != "slskd rejected the API key" {
		t.Fatalf("failed = %#v", failed)
	}

	var rows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM slskd_settings`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("slskd_settings holds %d rows, want a single row", rows)
	}
}

// A refusal heard twice while the outage continues must not push the clock
// forward: "deferred for two hours" has to count from the first refusal, not
// the latest one (#495).
func TestMarkSourceLoggedOutKeepsTheEarliestMoment(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	if _, err := queries.SaveSlskdSettings(ctx, SaveSlskdSettingsParams{
		BaseURL: "http://slskd:5030", APIKey: "key", Enabled: true, SearchTimeoutSeconds: 20,
	}); err != nil {
		t.Fatal(err)
	}

	first := time.Now().UTC().Truncate(time.Second)
	since, err := queries.MarkSourceLoggedOut(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	if !since.Equal(first) {
		t.Fatalf("since = %v, want the first moment heard %v", since, first)
	}

	later := first.Add(30 * time.Minute)
	since, err = queries.MarkSourceLoggedOut(ctx, later)
	if err != nil {
		t.Fatal(err)
	}
	if !since.Equal(first) {
		t.Fatalf("since = %v, want the clock to stay at the first moment %v rather than move to %v",
			since, first, later)
	}

	stored, err := queries.SlskdSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.LoggedOutSince.Valid || !stored.LoggedOutSince.Time.Equal(first) {
		t.Fatalf("stored logged_out_since = %#v, want %v", stored.LoggedOutSince, first)
	}

	backOnline, err := queries.ClearSourceLoggedOut(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !backOnline {
		t.Fatal("ClearSourceLoggedOut() = false, want true: this is the call that stopped the clock")
	}
	cleared, err := queries.SlskdSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cleared.LoggedOutSince.Valid {
		t.Fatalf("logged_out_since = %v, want it cleared", cleared.LoggedOutSince)
	}

	// A clock already stopped stays stopped, and costs no row rewrite, so a
	// second call reports nothing to wake for.
	backOnline, err = queries.ClearSourceLoggedOut(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if backOnline {
		t.Fatal("ClearSourceLoggedOut() = true on an already-stopped clock, want false")
	}
}

// slskd has to be configured for a refusal to be recorded against anything;
// acquisition cannot reach a search to refuse otherwise, but the store method
// still has to say plainly that it wrote nothing.
func TestMarkSourceLoggedOutWithNoConfiguredSourceReportsNoRows(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	if _, err := queries.MarkSourceLoggedOut(ctx, time.Now()); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("error = %v, want pgx.ErrNoRows", err)
	}
}

// A connection check that finds slskd logged in is real confirmation the
// account is back, so it clears a clock acquisition left running even though
// nothing acquisition has done triggered the check.
func TestRecordSlskdConnectionClearsTheClockOnSuccess(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	if _, err := queries.SaveSlskdSettings(ctx, SaveSlskdSettingsParams{
		BaseURL: "http://slskd:5030", APIKey: "key", Enabled: true, SearchTimeoutSeconds: 20,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := queries.MarkSourceLoggedOut(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}

	// A failed check is a single poll and must not touch the clock either way
	// (the constraint the dashboard has to hold: one bad check never flips it).
	failed, err := queries.RecordSlskdConnection(ctx, "failed", "", "slskd rejected the API key")
	if err != nil {
		t.Fatal(err)
	}
	if !failed.LoggedOutSince.Valid {
		t.Fatal("a failed check cleared logged_out_since, want it left exactly as acquisition set it")
	}

	ok, err := queries.RecordSlskdConnection(ctx, "ok", "slskd 0.22.1, Soulseek Connected, LoggedIn", "")
	if err != nil {
		t.Fatal(err)
	}
	if ok.LoggedOutSince.Valid {
		t.Fatal("a successful check left logged_out_since set, want it cleared")
	}
}

// Changing the endpoint invalidates everything a previous check learned,
// logged_out_since included: the clock an old slskd left running says nothing
// about whether the new one is signed in.
func TestSavingSlskdSettingsWithANewEndpointClearsTheClock(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	if _, err := queries.SaveSlskdSettings(ctx, SaveSlskdSettingsParams{
		BaseURL: "http://slskd:5030", APIKey: "key", Enabled: true, SearchTimeoutSeconds: 20,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := queries.MarkSourceLoggedOut(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}

	moved, err := queries.SaveSlskdSettings(ctx, SaveSlskdSettingsParams{
		BaseURL: "http://other:5030", APIKey: "second-key", Enabled: true, SearchTimeoutSeconds: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if moved.LoggedOutSince.Valid {
		t.Fatalf("logged_out_since = %v, want a new endpoint to clear it", moved.LoggedOutSince)
	}
}

func TestImportSettingsLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	// This test stores a destructive policy. Leaving it behind would arm
	// deletion for anyone who later runs Schall against the test database. The
	// defer is registered here so it runs while the pool and the lock are both
	// still held.
	defer func() { _, _ = pool.Exec(context.Background(), `TRUNCATE import_settings`) }()

	queries := New(pool)
	// An installation that never opened Settings is not unconfigured; it is
	// running the non-destructive default.
	unset, err := queries.ImportSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if unset.SourceRetention != SourceRetentionKeep {
		t.Fatalf("unset = %#v", unset)
	}

	stored, err := queries.SaveImportSettings(ctx, SaveImportSettingsParams{SourceRetention: SourceRetentionDelete})
	if err != nil {
		t.Fatal(err)
	}
	if stored.SourceRetention != SourceRetentionDelete || !stored.UpdatedAt.Valid {
		t.Fatalf("stored = %#v", stored)
	}
	readBack, err := queries.ImportSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if readBack.SourceRetention != SourceRetentionDelete {
		t.Fatalf("readBack = %#v", readBack)
	}

	// The table is the authority on which policies exist, so an invented one is
	// refused by the database rather than stored and acted on later.
	if _, err := queries.SaveImportSettings(ctx, SaveImportSettingsParams{SourceRetention: "archive"}); err == nil {
		t.Fatal("an unknown retention policy was accepted")
	}

	var rows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM import_settings`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("import_settings holds %d rows, want a single row", rows)
	}
}

// A caller that means to change only the source retention sends no opinion
// about re-encoding at all, and TranscodeEnabled is a pointer for exactly this
// reason: nil must keep the stored policy rather than silently turn it off.
func TestSavingImportSettingsWithNoTranscodeOpinionKeepsTheStoredPolicy(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	defer func() { _, _ = pool.Exec(context.Background(), `TRUNCATE import_settings`) }()
	queries := New(pool)

	enabled, err := queries.SaveImportSettings(ctx, SaveImportSettingsParams{
		SourceRetention:  SourceRetentionKeep,
		TranscodeEnabled: ptr(true),
		TranscodeTarget:  "mp3",
		TranscodeBitrate: "256",
		TranscodeWhen:    "lossless",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !enabled.TranscodeEnabled {
		t.Fatalf("stored = %#v, want transcoding enabled", enabled)
	}

	// Only the retention changes here; TranscodeEnabled is left nil.
	after, err := queries.SaveImportSettings(ctx, SaveImportSettingsParams{
		SourceRetention: SourceRetentionDelete,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !after.TranscodeEnabled || after.TranscodeBitrate != "256" || after.TranscodeWhen != "lossless" {
		t.Fatalf("after = %#v, want the re-encoding policy unchanged", after)
	}
}

func TestAPlayerNobodyConfiguredHasNoSettings(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	queries := New(dbtest.Setup(t))

	if _, err := queries.NavidromeSettings(ctx); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("unconfigured settings error = %v, want pgx.ErrNoRows", err)
	}
}

// The page is never shown the password, so it cannot send it back to keep it.
func TestSavingThePlayerWithNoPasswordKeepsTheStoredOne(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	queries := New(dbtest.Setup(t))

	if _, err := queries.SaveNavidromeSettings(ctx, SaveNavidromeSettingsParams{
		BaseURL: "http://navidrome:4533", Username: "schall", Password: "hunter2", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	kept, err := queries.SaveNavidromeSettings(ctx, SaveNavidromeSettingsParams{
		BaseURL: "http://navidrome:4533", Username: "schall", Password: "", Enabled: false,
	})
	if err != nil {
		t.Fatal(err)
	}

	if kept.Password != "hunter2" {
		t.Fatalf("password = %q, want the stored one retained", kept.Password)
	}
	if kept.Enabled {
		t.Error("the saved setting was not applied")
	}
}

// A check against one address says nothing about another.
func TestMovingThePlayerDropsTheVerifiedStatus(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	queries := New(dbtest.Setup(t))

	if _, err := queries.SaveNavidromeSettings(ctx, SaveNavidromeSettingsParams{
		BaseURL: "http://navidrome:4533", Username: "schall", Password: "hunter2", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := queries.RecordNavidromeConnection(ctx, "ok", "navidrome 0.63.2", ""); err != nil {
		t.Fatal(err)
	}
	moved, err := queries.SaveNavidromeSettings(ctx, SaveNavidromeSettingsParams{
		BaseURL: "http://elsewhere:4533", Username: "schall", Password: "", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	if moved.ConnectionStatus != "unknown" || moved.LastCheckedAt.Valid || moved.ConnectionDetail.Valid {
		t.Fatalf("moved = %#v, want the earlier check dropped", moved)
	}
}

// The settings page shows when the player last heard from Schall, which is only
// meaningful if a telling that landed stamps it.
func TestATellingThatLandedIsStamped(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	queries := New(dbtest.Setup(t))

	stored, err := queries.SaveNavidromeSettings(ctx, SaveNavidromeSettingsParams{
		BaseURL: "http://navidrome:4533", Username: "schall", Password: "hunter2", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if stored.LastNotifiedAt.Valid {
		t.Fatal("a player that was never told is stamped as told")
	}
	if err := queries.RecordNavidromeNotice(ctx); err != nil {
		t.Fatal(err)
	}

	told, err := queries.NavidromeSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !told.LastNotifiedAt.Valid {
		t.Error("a telling that landed was not stamped")
	}
}

// A burst of library changes queueing this one after another must not queue a
// rescan per change: the partial unique index from 00073 makes every insert
// after the first do nothing while one is still waiting.
func TestQueueNotifyPlayerCoalescesABurst(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	for attempt := 0; attempt < 3; attempt++ {
		if err := queries.QueueNotifyPlayer(ctx); err != nil {
			t.Fatalf("QueueNotifyPlayer() error = %v", err)
		}
	}

	var waiting int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM jobs WHERE kind = 'notify_player' AND status IN ('queued', 'running')
	`).Scan(&waiting); err != nil {
		t.Fatal(err)
	}
	if waiting != 1 {
		t.Fatalf("%d notify_player jobs are queued, want exactly one", waiting)
	}
}

func TestARecommendationSourceNobodyConfiguredHasNoSettings(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	queries := New(dbtest.Setup(t))

	if _, err := queries.ListenBrainzSettings(ctx); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("unconfigured settings error = %v, want pgx.ErrNoRows", err)
	}
}

// The address has a right answer, so the row exists to hold the account. An
// installation that never touches either URL still gets a usable one.
func TestTheRecommendationSourceStoresTheAccountAndItsDefaults(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	queries := New(dbtest.Setup(t))

	stored, err := queries.SaveListenBrainzSettings(ctx, SaveListenBrainzSettingsParams{
		Username:            "listener",
		SimilarityAlgorithm: "session_based",
		Enabled:             true,
	})
	if err != nil {
		t.Fatal(err)
	}

	if stored.Username != "listener" {
		t.Errorf("username = %q", stored.Username)
	}
	// A save cannot name an address, so these are the column defaults and this
	// is the only way they are ever set.
	if stored.BaseURL != "https://api.listenbrainz.org" {
		t.Errorf("base URL = %q, want the hosted service", stored.BaseURL)
	}
	if stored.LabsURL != "https://labs.api.listenbrainz.org" {
		t.Errorf("labs URL = %q, want the hosted dataset host", stored.LabsURL)
	}
	// Nothing Schall asks ListenBrainz needs authentication, so an account
	// saved without a token is the ordinary case rather than a half-saved one.
	if stored.UserToken.Valid {
		t.Errorf("user token = %#v, want none stored", stored.UserToken)
	}
	if stored.ConnectionStatus != "unknown" {
		t.Errorf("connection status = %q, want unknown before any check", stored.ConnectionStatus)
	}
}

// recordCheck names the row as it stands, which is what a check that nothing
// raced against carries when it comes to write its verdict down.
func recordCheck(
	t *testing.T, ctx context.Context, queries *Queries, status, detail, failure string,
) RecordListenBrainzConnectionParams {
	t.Helper()
	current, err := queries.ListenBrainzSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return RecordListenBrainzConnectionParams{
		ExpectedUsername:  current.Username,
		ExpectedUpdatedAt: current.UpdatedAt,
		Status:            status,
		Detail:            detail,
		Failure:           failure,
	}
}

// The page is never shown the token, so it cannot send it back to keep it.
func TestSavingTheRecommendationSourceWithNoTokenKeepsTheStoredOne(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	queries := New(dbtest.Setup(t))

	if _, err := queries.SaveListenBrainzSettings(ctx, SaveListenBrainzSettingsParams{
		Username:            "listener",
		UserToken:           "a-token",
		SimilarityAlgorithm: "session_based",
		Enabled:             true,
	}); err != nil {
		t.Fatal(err)
	}
	kept, err := queries.SaveListenBrainzSettings(ctx, SaveListenBrainzSettingsParams{
		Username:            "listener",
		SimilarityAlgorithm: "session_based",
		Enabled:             false,
	})
	if err != nil {
		t.Fatal(err)
	}

	if kept.UserToken.String != "a-token" {
		t.Fatalf("user token = %q, want the stored one retained", kept.UserToken.String)
	}
	if kept.Enabled {
		t.Error("the saved setting was not applied")
	}
}

// The token is optional, so removing one has to be possible. An empty field
// cannot mean it: that is already how "leave the stored one alone" is spelled.
func TestClearingTheTokenRemovesItRatherThanKeepingIt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	queries := New(dbtest.Setup(t))

	if _, err := queries.SaveListenBrainzSettings(ctx, SaveListenBrainzSettingsParams{
		Username:            "listener",
		UserToken:           "a-token",
		SimilarityAlgorithm: "session_based",
		Enabled:             true,
	}); err != nil {
		t.Fatal(err)
	}
	cleared, err := queries.SaveListenBrainzSettings(ctx, SaveListenBrainzSettingsParams{
		Username:            "listener",
		ClearUserToken:      true,
		SimilarityAlgorithm: "session_based",
		Enabled:             true,
	})
	if err != nil {
		t.Fatal(err)
	}

	if cleared.UserToken.Valid {
		t.Fatalf("user token = %#v, want it removed", cleared.UserToken)
	}
}

// A check against one account says nothing about another.
func TestChangingTheAccountDropsTheVerifiedStatus(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	queries := New(dbtest.Setup(t))

	if _, err := queries.SaveListenBrainzSettings(ctx, SaveListenBrainzSettingsParams{
		Username:            "listener",
		SimilarityAlgorithm: "session_based",
		Enabled:             true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := queries.RecordListenBrainzConnection(ctx, recordCheck(t, ctx, queries, "ok", "1000 recommendations", "")); err != nil {
		t.Fatal(err)
	}
	moved, err := queries.SaveListenBrainzSettings(ctx, SaveListenBrainzSettingsParams{
		Username:            "somebody-else",
		SimilarityAlgorithm: "session_based",
		Enabled:             true,
	})
	if err != nil {
		t.Fatal(err)
	}

	if moved.ConnectionStatus != "unknown" || moved.LastCheckedAt.Valid || moved.ConnectionDetail.Valid {
		t.Fatalf("moved = %#v, want the earlier check dropped", moved)
	}
}

// Renaming the labs algorithm is a settings edit, and it is not something a
// connection check ever asked about — so it leaves the check standing.
func TestRenamingTheAlgorithmKeepsTheVerifiedStatus(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	queries := New(dbtest.Setup(t))

	if _, err := queries.SaveListenBrainzSettings(ctx, SaveListenBrainzSettingsParams{
		Username:            "listener",
		SimilarityAlgorithm: "session_based",
		Enabled:             true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := queries.RecordListenBrainzConnection(ctx, recordCheck(t, ctx, queries, "ok", "1000 recommendations", "")); err != nil {
		t.Fatal(err)
	}
	renamed, err := queries.SaveListenBrainzSettings(ctx, SaveListenBrainzSettingsParams{
		Username:            "listener",
		SimilarityAlgorithm: "session_based_v2",
		Enabled:             true,
	})
	if err != nil {
		t.Fatal(err)
	}

	if renamed.ConnectionStatus != "ok" {
		t.Fatalf("connection status = %q, want the check to stand", renamed.ConnectionStatus)
	}
	if renamed.SimilarityAlgorithm != "session_based_v2" {
		t.Errorf("algorithm = %q, want the rename applied", renamed.SimilarityAlgorithm)
	}
}

// A failed check records why, so the settings page can say it without anybody
// reading the server log.
func TestAFailedCheckRecordsItsReason(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	queries := New(dbtest.Setup(t))

	if _, err := queries.SaveListenBrainzSettings(ctx, SaveListenBrainzSettingsParams{
		Username:            "nobody",
		SimilarityAlgorithm: "session_based",
		Enabled:             true,
	}); err != nil {
		t.Fatal(err)
	}
	failed, err := queries.RecordListenBrainzConnection(ctx, recordCheck(t, ctx, queries, "failed", "",
		`listenbrainz does not know the account "nobody"; check the username`))
	if err != nil {
		t.Fatal(err)
	}

	if failed.ConnectionStatus != "failed" {
		t.Fatalf("connection status = %q", failed.ConnectionStatus)
	}
	if !failed.ConnectionError.Valid || failed.ConnectionDetail.Valid {
		t.Fatalf("failed = %#v, want a reason and no detail", failed)
	}
	if !failed.LastCheckedAt.Valid {
		t.Error("a check that ran was not stamped")
	}
}

// The table is a singleton by constraint, not by convention.
func TestTheRecommendationSourceKeepsASingleRow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	for _, username := range []string{"first", "second", "third"} {
		if _, err := queries.SaveListenBrainzSettings(ctx, SaveListenBrainzSettingsParams{
			Username:            username,
			SimilarityAlgorithm: "session_based",
			Enabled:             true,
		}); err != nil {
			t.Fatal(err)
		}
	}

	var rows int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM listenbrainz_settings").Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("listenbrainz_settings holds %d rows, want a single row", rows)
	}
}

// The migration's defaults are the answer for the fields that have a right one:
// a row that names only the account is a complete, usable configuration.
func TestTheRecommendationSourceRowDefaultsEverythingButTheAccount(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	if _, err := pool.Exec(ctx,
		"INSERT INTO listenbrainz_settings (username) VALUES ($1)", "listener"); err != nil {
		t.Fatal(err)
	}
	stored, err := New(pool).ListenBrainzSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if stored.BaseURL != "https://api.listenbrainz.org" {
		t.Errorf("base URL = %q, want the hosted service", stored.BaseURL)
	}
	if stored.LabsURL != "https://labs.api.listenbrainz.org" {
		t.Errorf("labs URL = %q, want the hosted dataset host", stored.LabsURL)
	}
	if stored.SimilarityAlgorithm == "" {
		t.Error("no similarity algorithm was defaulted")
	}
	if !stored.Enabled {
		t.Error("a configured source starts disabled")
	}
}

// A check is a statement about the account it asked. If a save switches the
// singleton to another account while the check is in flight, writing the verdict
// down would show one account's success or failure under the other's name — so
// the write names the row it is a verdict about and finds nothing when it moved.
func TestAConnectionCheckOvertakenByASaveIsRefused(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	queries := New(dbtest.Setup(t))

	if _, err := queries.SaveListenBrainzSettings(ctx, SaveListenBrainzSettingsParams{
		Username: "listener", SimilarityAlgorithm: "session_based", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	// What the check read when it started.
	inFlight := recordCheck(t, ctx, queries, "ok", "1000 recommendations for listener", "")

	// The account changes while it is still running.
	if _, err := queries.SaveListenBrainzSettings(ctx, SaveListenBrainzSettingsParams{
		Username: "somebody-else", SimilarityAlgorithm: "session_based", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := queries.RecordListenBrainzConnection(ctx, inFlight); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("stale check error = %v, want pgx.ErrNoRows", err)
	}

	landed, err := queries.ListenBrainzSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if landed.ConnectionStatus != "unknown" || landed.ConnectionDetail.Valid {
		t.Fatalf("row = %#v, want the new account left unchecked", landed)
	}
}

// The guard has to let the ordinary case through, or every check would be
// refused and the status would never move off unknown.
func TestAConnectionCheckNothingRacedIsRecorded(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	queries := New(dbtest.Setup(t))

	if _, err := queries.SaveListenBrainzSettings(ctx, SaveListenBrainzSettingsParams{
		Username: "listener", SimilarityAlgorithm: "session_based", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	recorded, err := queries.RecordListenBrainzConnection(ctx,
		recordCheck(t, ctx, queries, "ok", "1000 recommendations for listener", ""))
	if err != nil {
		t.Fatalf("RecordListenBrainzConnection() error = %v", err)
	}
	if recorded.ConnectionStatus != "ok" || !recorded.LastCheckedAt.Valid {
		t.Fatalf("recorded = %#v, want the check stored", recorded)
	}
}

// A token swap is a different credential against the same name, so a verdict
// obtained under the old one no longer describes the configured source.
func TestAConnectionCheckOvertakenByATokenChangeIsRefused(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	queries := New(dbtest.Setup(t))

	if _, err := queries.SaveListenBrainzSettings(ctx, SaveListenBrainzSettingsParams{
		Username: "listener", SimilarityAlgorithm: "session_based", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	inFlight := recordCheck(t, ctx, queries, "ok", "1000 recommendations for listener", "")

	if _, err := queries.SaveListenBrainzSettings(ctx, SaveListenBrainzSettingsParams{
		Username: "listener", UserToken: "a-token",
		SimilarityAlgorithm: "session_based", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := queries.RecordListenBrainzConnection(ctx, inFlight); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("stale check error = %v, want pgx.ErrNoRows", err)
	}
}

// The write path cannot name an address at all, which is what stops a caller
// pointing the credential at a host of its own. Saving repeatedly never moves
// the hosts off what the migration defaulted them to.
func TestSavingTheRecommendationSourceNeverMovesTheHost(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	queries := New(dbtest.Setup(t))

	for _, username := range []string{"listener", "somebody-else"} {
		stored, err := queries.SaveListenBrainzSettings(ctx, SaveListenBrainzSettingsParams{
			Username: username, UserToken: "a-token",
			SimilarityAlgorithm: "session_based", Enabled: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		if stored.BaseURL != "https://api.listenbrainz.org" {
			t.Fatalf("base URL = %q after saving %q", stored.BaseURL, username)
		}
		if stored.LabsURL != "https://labs.api.listenbrainz.org" {
			t.Fatalf("labs URL = %q after saving %q", stored.LabsURL, username)
		}
	}
}
