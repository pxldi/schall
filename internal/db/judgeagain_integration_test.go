package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/dbtest"
)

// A verdict Schall reached is normally permanent. One pass re-opens them,
// because two rules changed under copies already decided (docs/decisions/0024
// and 0025), and what that pass is allowed to offer is the whole of its safety.
// These tests are that list.

// settledCopy is one copy already decided about, recorded the way validation
// records one and then settled so the request is no longer being validated.
func settledCopy(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool,
	targetID, requestID uuid.UUID, verdict, evidence string,
) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		INSERT INTO acquisition_target_files (
			acquisition_target_id, provider, source_username, remote_path,
			file_name, size_bytes, verdict, decided_by, summary, evidence,
			download_request_id
		)
		VALUES ($1, 'slskd', 'peer', $2, '03.flac', 9000000, $3, 'schall',
		        'what it came to', $4::jsonb, $5)
	`, targetID, `Music\Anetha\03.flac`, verdict, evidence, requestID); err != nil {
		t.Fatalf("record the copy: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE download_requests SET import_status = 'discarded' WHERE id = $1
	`, requestID); err != nil {
		t.Fatalf("settle the request: %v", err)
	}
}

// anchored gives the want the sample its copies would now be measured against.
func anchored(ctx context.Context, t *testing.T, queries *Queries, targetID uuid.UUID) {
	t.Helper()
	if err := queries.RecordAnchor(ctx, AnchorParams{
		TargetID: targetID, Fingerprint: "AQABz0mUSEkSHV-OH0d_HG9wJXo0Hz-uI9-C_sfx4XmC",
		Source: "deezer", Reference: "2178654", Seconds: 30,
	}); err != nil {
		t.Fatalf("record the anchor: %v", err)
	}
}

// The evidence of a copy discarded because the audio was identified as some
// other recording. That is the refusal docs/decisions/0025 changed.
const namedOtherMusic = `{"acoustic": {"recordingIds": ["f0a32851-2df8-4643-8649-f33f9a82c19d"]}}`

// A copy refused because the audio named another recording is offered again.
// The rule that made that refusal has changed, the want now holds a sample, and
// the file is still on disk to be measured.
func TestACopyRefusedOnItsAudioIsOfferedAgain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
	anchored(ctx, t, queries, targetID)
	settledCopy(ctx, t, pool, targetID, requestID,
		AcquiredFileDiscardedAudio, namedOtherMusic)

	offered, err := queries.CopiesToJudgeAgain(ctx)
	if err != nil {
		t.Fatalf("CopiesToJudgeAgain() error = %v", err)
	}
	if len(offered) != 1 || offered[0].RequestID != requestID {
		t.Fatalf("offered = %+v, want the copy back", offered)
	}
	if offered[0].SourceDirectory != "Music/Anetha" || offered[0].FileName != "03.flac" {
		t.Errorf("offered = %+v, want where its bytes are", offered[0])
	}
}

// A want with no sample is left alone. Nothing has changed for it, and judging
// its copies again would decode every one of them to reach the same answer.
func TestAWantWithNoSampleOffersNothing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
	settledCopy(ctx, t, pool, targetID, requestID,
		AcquiredFileDiscardedAudio, namedOtherMusic)

	offered, err := queries.CopiesToJudgeAgain(ctx)
	if err != nil {
		t.Fatalf("CopiesToJudgeAgain() error = %v", err)
	}
	if len(offered) != 0 {
		t.Fatalf("offered = %+v, want nothing for a want with no sample", offered)
	}
}

// A copy the sample itself refused stays refused. Nothing about that answer has
// changed, and re-running it would decode the file to write down what it says
// already.
func TestACopyTheSampleRefusedIsNotOfferedAgain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
	anchored(ctx, t, queries, targetID)
	settledCopy(ctx, t, pool, targetID, requestID, AcquiredFileDiscardedAudio,
		`{"acoustic": {"recordingIds": ["f0a32851-2df8-4643-8649-f33f9a82c19d"]},
		  "anchor": {"rate": 0.42, "measured": true, "agrees": false}}`)

	offered, err := queries.CopiesToJudgeAgain(ctx)
	if err != nil {
		t.Fatalf("CopiesToJudgeAgain() error = %v", err)
	}
	if len(offered) != 0 {
		t.Fatalf("offered = %+v, want the sample's own refusal left standing", offered)
	}
}

// A copy refused on its own tags is left alone. The rules that changed are both
// about audio.
func TestACopyRefusedOnItsTagsIsNotOfferedAgain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
	anchored(ctx, t, queries, targetID)
	settledCopy(ctx, t, pool, targetID, requestID,
		AcquiredFileDiscardedTags, `{"acoustic": {"recordingIds": []}}`)

	offered, err := queries.CopiesToJudgeAgain(ctx)
	if err != nil {
		t.Fatalf("CopiesToJudgeAgain() error = %v", err)
	}
	if len(offered) != 0 {
		t.Fatalf("offered = %+v, want a refusal about tags left standing", offered)
	}
}

// A decision somebody made by hand is never re-opened. Manual decisions win and
// are permanent, and a pass that overruled one would be the worst thing here.
func TestACopySomebodyDecidedAboutIsNeverOfferedAgain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
	anchored(ctx, t, queries, targetID)
	settledCopy(ctx, t, pool, targetID, requestID,
		AcquiredFileDiscardedAudio, namedOtherMusic)
	if _, err := pool.Exec(ctx, `
		UPDATE acquisition_target_files SET decided_by = 'user'
		WHERE acquisition_target_id = $1
	`, targetID); err != nil {
		t.Fatalf("record the decision as a person's: %v", err)
	}

	offered, err := queries.CopiesToJudgeAgain(ctx)
	if err != nil {
		t.Fatalf("CopiesToJudgeAgain() error = %v", err)
	}
	if len(offered) != 0 {
		t.Fatalf("offered = %+v, want a person's decision left alone", offered)
	}
}

// A want whose music already arrived offers nothing. Validation imports what it
// proves without asking whether the want was answered since, so a second copy
// would be a second import of music already in the library.
func TestAWantWithItsMusicAlreadyOffersNothing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
	anchored(ctx, t, queries, targetID)
	settledCopy(ctx, t, pool, targetID, requestID,
		AcquiredFileDiscardedAudio, namedOtherMusic)
	if _, err := pool.Exec(ctx, `
		INSERT INTO acquisition_target_files (
			acquisition_target_id, provider, source_username, remote_path,
			file_name, size_bytes, verdict, decided_by, summary
		)
		VALUES ($1, 'slskd', 'peer', 'Music/Anetha/04.flac', '04.flac', 9000000,
		        $2, 'schall', 'proven')
	`, targetID, AcquiredFileAccepted); err != nil {
		t.Fatalf("record the accepted copy: %v", err)
	}

	offered, err := queries.CopiesToJudgeAgain(ctx)
	if err != nil {
		t.Fatalf("CopiesToJudgeAgain() error = %v", err)
	}
	if len(offered) != 0 {
		t.Fatalf("offered = %+v, want nothing for a want that has its music", offered)
	}
	answered, err := queries.WantHasAcceptedCopy(ctx, targetID)
	if err != nil || !answered {
		t.Fatalf("answered = %v, err = %v; want the want reported as answered", answered, err)
	}
}

// Putting a copy back into validation changes the one thing validation reads and
// nothing else. The request's own status stays completed — the bytes did arrive
// — and the recorded verdict stands until a new one overwrites it.
func TestReopeningACopyOnlyPutsItBackWhereValidationLooks(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
	anchored(ctx, t, queries, targetID)
	settledCopy(ctx, t, pool, targetID, requestID,
		AcquiredFileDiscardedAudio, namedOtherMusic)

	if err := queries.ReopenCopyForJudging(ctx, requestID); err != nil {
		t.Fatalf("ReopenCopyForJudging() error = %v", err)
	}

	var status, importStatus, verdict string
	if err := pool.QueryRow(ctx, `
		SELECT requests.status, requests.import_status, copies.verdict
		FROM download_requests requests
		JOIN acquisition_target_files copies ON copies.download_request_id = requests.id
		WHERE requests.id = $1
	`, requestID).Scan(&status, &importStatus, &verdict); err != nil {
		t.Fatalf("read the request back: %v", err)
	}
	if status != "completed" {
		t.Errorf("status = %q, want the transfer still recorded as completed", status)
	}
	if importStatus != "pending" {
		t.Errorf("import status = %q, want it back where validation claims from", importStatus)
	}
	if verdict != AcquiredFileDiscardedAudio {
		t.Errorf("verdict = %q, want the recorded answer left until a new one replaces it", verdict)
	}

	// And the copy can then be claimed, which is the whole point of the write.
	if _, err := queries.ClaimTargetImport(ctx, requestID); err != nil {
		t.Fatalf("ClaimTargetImport() error = %v, want the copy claimable again", err)
	}
}

// A copy something else is already validating is not taken. Two passes deciding
// one copy is the one collision worth refusing outright.
func TestACopyBeingValidatedIsNotReopened(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
	anchored(ctx, t, queries, targetID)
	settledCopy(ctx, t, pool, targetID, requestID,
		AcquiredFileDiscardedAudio, namedOtherMusic)
	if _, err := pool.Exec(ctx, `
		UPDATE download_requests SET import_status = 'validating' WHERE id = $1
	`, requestID); err != nil {
		t.Fatalf("mark the request as being validated: %v", err)
	}

	err := queries.ReopenCopyForJudging(ctx, requestID)
	if !errors.Is(err, ErrCopyNotReopened) {
		t.Fatalf("err = %v, want %v", err, ErrCopyNotReopened)
	}
	offered, err := queries.CopiesToJudgeAgain(ctx)
	if err != nil {
		t.Fatalf("CopiesToJudgeAgain() error = %v", err)
	}
	if len(offered) != 0 {
		t.Fatalf("offered = %+v, want a copy being validated left out", offered)
	}
}

// Asking twice queues one pass. A person pressing a button twice must not get
// two passes over the same copies.
func TestAskingForTwoPassesQueuesOne(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	for range 2 {
		if err := queries.QueueJudgeCopiesAgain(ctx, time.Now()); err != nil {
			t.Fatalf("QueueJudgeCopiesAgain() error = %v", err)
		}
	}

	var queued int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM jobs WHERE kind = 'judge_copies_again'
	`).Scan(&queued); err != nil {
		t.Fatalf("count the passes: %v", err)
	}
	if queued != 1 {
		t.Errorf("passes = %d, want one", queued)
	}
}

// A want anchored while its copies are already being judged is not queued a
// second pass; the running one is told to run again when it finishes. The write
// that tells it failed in production for a day (SQLSTATE 42P08, an untyped
// parameter inside jsonb_build_object), so this is the call as Postgres sees it.
func TestQueuingAJudgingPassWhileOneRunsAsksItToRunAgain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, _ := fetchedForAWant(ctx, t, pool)
	if _, err := pool.Exec(ctx, `
		INSERT INTO jobs (kind, payload, status, started_at)
		VALUES ('judge_want_copies', jsonb_build_object('acquisition_target_id', $1::text), 'running', now())
	`, targetID); err != nil {
		t.Fatalf("start a judging pass: %v", err)
	}

	again := time.Date(2026, 9, 6, 8, 0, 0, 0, time.UTC)
	if err := queries.QueueWantCopyJudging(ctx, targetID, again); err != nil {
		t.Fatalf("QueueWantCopyJudging() error = %v", err)
	}

	var queued int
	var rejudgeAfter time.Time
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE status = 'queued'),
		       min((payload->>'rejudge_after')::timestamptz) FILTER (WHERE status = 'running')
		FROM jobs WHERE kind = 'judge_want_copies' AND payload->>'acquisition_target_id' = $1
	`, targetID.String()).Scan(&queued, &rejudgeAfter); err != nil {
		t.Fatalf("read the jobs back: %v", err)
	}
	if queued != 0 {
		t.Errorf("queued = %d, want no second pass beside the running one", queued)
	}
	if !rejudgeAfter.Equal(again) {
		t.Errorf("rejudge_after = %v, want %v written on the running pass", rejudgeAfter, again)
	}

	// With nothing running, the same call queues one pass, and only one.
	if _, err := pool.Exec(ctx, `UPDATE jobs SET status = 'completed' WHERE kind = 'judge_want_copies'`); err != nil {
		t.Fatalf("finish the pass: %v", err)
	}
	for range 2 {
		if err := queries.QueueWantCopyJudging(ctx, targetID, again); err != nil {
			t.Fatalf("QueueWantCopyJudging() error = %v", err)
		}
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM jobs WHERE kind = 'judge_want_copies' AND status = 'queued'
	`).Scan(&queued); err != nil {
		t.Fatalf("count the queued passes: %v", err)
	}
	if queued != 1 {
		t.Errorf("queued = %d, want exactly one pass", queued)
	}
}

// A judging pass that claims a copy and then fails gives the claim back. The
// request returns to where its verdict put it, so the copy is a question the
// queue shows and the want is free to fetch another; a request that is not
// being validated is left alone.
func TestAFailedJudgementGivesTheCopyBack(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
	settledCopy(ctx, t, pool, targetID, requestID, AcquiredFileHeld, `{}`)
	if _, err := pool.Exec(ctx, `
		UPDATE download_requests SET import_status = 'needs_review' WHERE id = $1
	`, requestID); err != nil {
		t.Fatalf("hold the copy: %v", err)
	}
	if err := queries.ReopenCopyForJudging(ctx, requestID); err != nil {
		t.Fatalf("ReopenCopyForJudging() error = %v", err)
	}
	if _, err := queries.ClaimTargetImport(ctx, requestID); err != nil {
		t.Fatalf("ClaimTargetImport() error = %v", err)
	}
	open, err := queries.OpenTargetRequest(ctx, targetID)
	if err != nil || !open.Valid {
		t.Fatalf("open = %v, err = %v; want the claimed request read as the want's open one", open, err)
	}

	if err := queries.ReleaseCopyFromJudging(ctx, requestID); err != nil {
		t.Fatalf("ReleaseCopyFromJudging() error = %v", err)
	}

	var importStatus, verdict string
	if err := pool.QueryRow(ctx, `
		SELECT requests.import_status, copies.verdict
		FROM download_requests requests
		JOIN acquisition_target_files copies ON copies.download_request_id = requests.id
		WHERE requests.id = $1
	`, requestID).Scan(&importStatus, &verdict); err != nil {
		t.Fatalf("read the request back: %v", err)
	}
	if importStatus != "needs_review" {
		t.Errorf("import status = %q, want the held copy back in the queue", importStatus)
	}
	if verdict != AcquiredFileHeld {
		t.Errorf("verdict = %q, want the recorded verdict untouched", verdict)
	}
	open, err = queries.OpenTargetRequest(ctx, targetID)
	if err != nil || open.Valid {
		t.Fatalf("open = %v, err = %v; want the want free to fetch another copy", open, err)
	}

	// Released twice, or released when nothing claimed it, changes nothing.
	if err := queries.ReleaseCopyFromJudging(ctx, requestID); err != nil {
		t.Fatalf("ReleaseCopyFromJudging() error = %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT import_status FROM download_requests WHERE id = $1
	`, requestID).Scan(&importStatus); err != nil {
		t.Fatalf("read the request back: %v", err)
	}
	if importStatus != "needs_review" {
		t.Errorf("import status = %q, want it left alone", importStatus)
	}
}
