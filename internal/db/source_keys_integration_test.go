package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/dbtest"
)

// A person can key a want to the address of a track (ADR 0038). These tests
// pin the schema and the queries that carry the key.

func keySoundCloud(targetID uuid.UUID) KeySourceParams {
	return KeySourceParams{
		TargetID: targetID, Source: "soundcloud", ExternalID: "soundcloud:293",
		ExternalURL: "https://soundcloud.com/abo/illegal-edit",
		Lookup: SourceLookup{
			Title: "PinkPantheress - Illegal (Abo Edit)", Artist: "Abo", Uploader: "Abo",
			AccountID: "soundcloud:https://soundcloud.com/abo", DurationMS: 187000,
			ArtworkURL: "https://i1.sndcdn.com/artworks-x-t500x500.jpg",
		},
		Fingerprint: "AQAAexcerpt", Seconds: 30,
		Summary: "Keyed to an address.", AcquiredSummary: "Already in the library.",
	}
}

func queuedJobs(ctx context.Context, t *testing.T, pool *pgxpool.Pool, kind string) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM jobs WHERE kind = $1 AND status = 'queued'
	`, kind).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// Keying replaces the anchor with the excerpt, ends resolution's schedule, sets
// the floor, and asks for a search and for the held copies to be judged again
// (ADR 0038 §1, §3, §7).
func TestKeyingAWantReplacesItsAnchorAndEndsItsResolution(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, _ := anchoredWithoutARecording(ctx, t, pool, "deezer", "2178654")

	keyed, err := queries.KeyAcquisitionTargetToSource(ctx, keySoundCloud(targetID))
	if err != nil {
		t.Fatalf("KeyAcquisitionTargetToSource() error = %v", err)
	}

	if keyed.Status != "unresolved" || keyed.Source != "soundcloud" ||
		keyed.ExternalID != "soundcloud:293" ||
		keyed.ExternalURL != "https://soundcloud.com/abo/illegal-edit" {
		t.Errorf("want = %s keyed %q %q %q, want unresolved and keyed", keyed.Status,
			keyed.Source, keyed.ExternalID, keyed.ExternalURL)
	}
	if keyed.AnchorSource != "source" || keyed.NextAttemptAt.Valid || !keyed.NextSearchAt.Valid ||
		keyed.SearchAttempts != 0 {
		t.Errorf("anchor %q next %v search %v (%d), want the excerpt, no resolution and a search due",
			keyed.AnchorSource, keyed.NextAttemptAt, keyed.NextSearchAt, keyed.SearchAttempts)
	}
	if !keyed.MinimumBitrate.Valid || keyed.MinimumBitrate.Int32 != 320 {
		t.Errorf("minimum_bitrate = %v, want 320", keyed.MinimumBitrate)
	}
	if keyed.SourceLookup == nil || keyed.SourceLookup.Uploader != "Abo" ||
		keyed.SourceLookup.AccountID != "soundcloud:https://soundcloud.com/abo" {
		t.Errorf("lookup = %+v, want what the person confirmed", keyed.SourceLookup)
	}
	var fingerprint, reference string
	if err := pool.QueryRow(ctx, `
		SELECT anchor_fingerprint, anchor_reference FROM acquisition_targets WHERE id = $1
	`, targetID).Scan(&fingerprint, &reference); err != nil {
		t.Fatal(err)
	}
	if fingerprint != "AQAAexcerpt" || reference != "soundcloud:293" {
		t.Errorf("anchor = %q %q, want the excerpt keyed by the address", fingerprint, reference)
	}
	if queuedJobs(ctx, t, pool, "sweep_acquisition_targets") != 1 ||
		queuedJobs(ctx, t, pool, "judge_want_copies") != 1 {
		t.Errorf("want a sweep and a judging of held copies queued")
	}
}

// The Version question stops waiting: the address answers it (ADR 0038 §8).
func TestAWantAskingWhichVersionItIsCanBeKeyed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)
	targetID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO acquisition_targets (id, origin, entry_artist, entry_title, status, review_reason)
		VALUES ($1, 'playlist', 'Abo', 'Illegal', 'awaiting_review', 'resolution')
	`, targetID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO acquisition_target_candidates (acquisition_target_id, musicbrainz_recording_id,
		                                           artist_name, track_title, rank)
		VALUES ($1, gen_random_uuid(), 'PinkPantheress', 'Illegal', 1)
	`, targetID); err != nil {
		t.Fatal(err)
	}

	keyed, err := queries.KeyAcquisitionTargetToSource(ctx, keySoundCloud(targetID))
	if err != nil {
		t.Fatalf("KeyAcquisitionTargetToSource() error = %v", err)
	}
	if keyed.Status != "unresolved" || keyed.ReviewReason.Valid {
		t.Errorf("want = %s (%v), want it unresolved and out of the question", keyed.Status,
			keyed.ReviewReason)
	}
	candidates, err := queries.AcquisitionTargetCandidates(ctx, targetID)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 0 {
		t.Errorf("candidates = %d, want the question's answers cleared", len(candidates))
	}
}

func TestOnlyAWantWithNoAnswerCanBeKeyedAndOnlyOnce(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	pending := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO acquisition_targets (id, origin, entry_title, status, musicbrainz_recording_id)
		VALUES ($1, 'playlist', 'Illegal', 'pending', gen_random_uuid())
	`, pending); err != nil {
		t.Fatal(err)
	}
	if _, err := queries.KeyAcquisitionTargetToSource(ctx, keySoundCloud(pending)); !errors.Is(err, ErrNotKeyable) {
		t.Errorf("keying a want with a recording = %v, want %v", err, ErrNotKeyable)
	}

	_, unresolved, _ := anchoredWithoutARecording(ctx, t, pool, "deezer", "2178654")
	if _, err := queries.KeyAcquisitionTargetToSource(ctx, keySoundCloud(unresolved)); err != nil {
		t.Fatal(err)
	}
	again := keySoundCloud(unresolved)
	again.ExternalID, again.ExternalURL = "soundcloud:294", "https://soundcloud.com/abo/other"
	if _, err := queries.KeyAcquisitionTargetToSource(ctx, again); !errors.Is(err, ErrAlreadyKeyed) {
		t.Errorf("keying a keyed want again = %v, want %v", err, ErrAlreadyKeyed)
	}
	if _, err := queries.KeyAcquisitionTargetToSource(ctx, keySoundCloud(uuid.New())); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("keying a want that does not exist = %v, want no rows", err)
	}
}

// Resolution never asks about a keyed want (ADR 0038 §1). The selection itself
// excludes it, so the test gives it a resolution schedule the schema would
// refuse, inside a transaction that is rolled back.
func TestAKeyedWantIsNeverSelectedForResolution(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, _ := anchoredWithoutARecording(ctx, t, pool, "deezer", "2178654")
	if _, err := queries.KeyAcquisitionTargetToSource(ctx, keySoundCloud(targetID)); err != nil {
		t.Fatal(err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
		ALTER TABLE acquisition_targets DROP CONSTRAINT acquisition_targets_source_unscheduled
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE acquisition_targets SET next_attempt_at = now() - interval '1 hour' WHERE id = $1
	`, targetID); err != nil {
		t.Fatal(err)
	}
	inTx := New(tx)

	due, err := inTx.DueUnresolvedTargets(ctx, time.Now(), 10)
	if err != nil {
		t.Fatalf("DueUnresolvedTargets() error = %v", err)
	}
	for _, target := range due {
		if target.ID == targetID {
			t.Fatal("a keyed want was selected for resolution")
		}
	}
	if next, err := inTx.NextUnresolvedTargetDue(ctx); err != nil || next.Valid {
		t.Errorf("NextUnresolvedTargetDue() = %v, %v, want nothing due", next, err)
	}
	if _, err := inTx.ResolveAcquisitionTarget(ctx, ResolveAcquisitionTargetParams{
		ID: targetID, MusicBrainzRecordingID: uuid.New(), ResolutionMethod: "isrc",
		Summary: "Resolved.", Detail: "Resolved.", NextAttemptAt: time.Now(),
	}); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("ResolveAcquisitionTarget() = %v, want the keyed want left alone", err)
	}
	if _, err := inTx.SendAcquisitionTargetToReview(ctx, ReviewAcquisitionTargetParams{
		ID: targetID, Summary: "Which one?", Detail: "Two fit.",
	}); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("SendAcquisitionTargetToReview() = %v, want the keyed want left alone", err)
	}
	if err := inTx.RequeueUnresolvedTarget(ctx, targetID, "none", "Not found.", "Not found.", "",
		time.Now()); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("RequeueUnresolvedTarget() = %v, want the keyed want left alone", err)
	}
}

// Stopping a keyed want and wanting it again gives it back its search, and
// never a resolution schedule.
func TestAKeyedWantWantedAgainIsSearchedAndNotResolved(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, _ := anchoredWithoutARecording(ctx, t, pool, "deezer", "2178654")
	if _, err := queries.KeyAcquisitionTargetToSource(ctx, keySoundCloud(targetID)); err != nil {
		t.Fatal(err)
	}
	if _, err := queries.StopPursuingAcquisitionTarget(ctx, targetID, "Not wanted."); err != nil {
		t.Fatal(err)
	}

	again, err := queries.PursueAcquisitionTargetAgain(ctx, targetID, "Unresolved.", "Wanted.", time.Now())
	if err != nil {
		t.Fatalf("PursueAcquisitionTargetAgain() error = %v", err)
	}
	if again.Status != "unresolved" || again.NextAttemptAt.Valid || !again.NextSearchAt.Valid {
		t.Errorf("want = %s next %v search %v, want it searched and never resolved",
			again.Status, again.NextAttemptAt, again.NextSearchAt)
	}
}

// Nothing found by name or by ISRC may replace a keyed want's excerpt.
func TestTheAnchorSweepLeavesAKeyedWantAlone(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, _ := anchoredWithoutARecording(ctx, t, pool, "deezer", "2178654")
	if _, err := queries.KeyAcquisitionTargetToSource(ctx, keySoundCloud(targetID)); err != nil {
		t.Fatal(err)
	}

	if err := queries.RecordAnchor(ctx, AnchorParams{
		TargetID: targetID, Fingerprint: "AQAAdeezer", Source: "deezer", Reference: "1", Seconds: 30,
	}); err != nil {
		t.Fatal(err)
	}
	target, err := queries.AcquisitionTarget(ctx, targetID)
	if err != nil {
		t.Fatal(err)
	}
	if target.AnchorSource != "source" {
		t.Errorf("anchor = %q, want the excerpt kept", target.AnchorSource)
	}
}

// The library already holding a file with the same source identity acquires
// the want at once (ADR 0038 §1).
func TestKeyingAWantTheLibraryAlreadyHoldsAcquiresIt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, _ := anchoredWithoutARecording(ctx, t, pool, "deezer", "2178654")
	fileID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_files (id, path, size_bytes, modified_at, resolution_status)
		VALUES ($1, '/music/Abo/illegal.mp3', 4000000, now(), 'source')
	`, fileID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_file_identities (library_file_id, kind, provider, source, external_id,
		                                     external_url, method, is_manual)
		VALUES ($1, 'source', 'soundcloud', 'soundcloud', 'soundcloud:293',
		        'https://soundcloud.com/abo/illegal-edit', 'source_url', true)
	`, fileID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM jobs`); err != nil {
		t.Fatal(err)
	}

	keyed, err := queries.KeyAcquisitionTargetToSource(ctx, keySoundCloud(targetID))
	if err != nil {
		t.Fatalf("KeyAcquisitionTargetToSource() error = %v", err)
	}
	if keyed.Status != "acquired" || keyed.AcquiredLibraryFileID.UUID != fileID ||
		keyed.NextSearchAt.Valid || keyed.Summary != "Already in the library." {
		t.Errorf("want = %s file %v search %v %q, want acquired with the file already held",
			keyed.Status, keyed.AcquiredLibraryFileID, keyed.NextSearchAt, keyed.Summary)
	}
	if queuedJobs(ctx, t, pool, "sweep_acquisition_targets") != 0 {
		t.Errorf("a sweep was queued for a want that has its music")
	}
}

// A keyed want's copy is claimed with the want's own key and floor, and the
// file it becomes carries that key as a person's identity: the audio proof
// only ties the file to it (ADR 0038 §5). Naming it writes the uploader by
// account, the names and the artwork.
func TestACopyOfAKeyedWantBecomesAPersonsSourceIdentity(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := anchoredWithoutARecording(ctx, t, pool, "deezer", "2178654")
	if _, err := queries.KeyAcquisitionTargetToSource(ctx, keySoundCloud(targetID)); err != nil {
		t.Fatal(err)
	}

	row, err := queries.ClaimTargetImport(ctx, requestID)
	if err != nil {
		t.Fatalf("ClaimTargetImport() error = %v", err)
	}
	want := ImportSource{Source: "soundcloud", ExternalID: "soundcloud:293",
		ExternalURL: "https://soundcloud.com/abo/illegal-edit"}
	if row.SourceKey == nil || *row.SourceKey != want || row.MinimumBitrate != 320 ||
		row.AnchorSource != "source" {
		t.Fatalf("row key %+v floor %d anchor %q, want the want's key, its floor and the excerpt",
			row.SourceKey, row.MinimumBitrate, row.AnchorSource)
	}
	if key, ok := SourceKeyOf(row); !ok || key != want {
		t.Fatalf("SourceKeyOf() = %+v, %v", key, ok)
	}

	fileID := importedOnTheAnchor(ctx, t, pool, queries, targetID, requestID,
		&AcquiredFileEvidence{
			Name: "worry.flac", Method: "published-sample", Confidence: 1,
			Agrees: []string{"audio"}, Differs: []string{}, Problems: []string{},
			Anchor: &ImportAnchor{Source: "source", Reference: "soundcloud:293", Measured: true, Rate: 0.04},
			Source: &want,
		})
	outcome, err := queries.CompleteAcquiredFile(ctx, targetID, "A copy was imported.", "Proven.")
	if err != nil {
		t.Fatalf("CompleteAcquiredFile() error = %v", err)
	}
	if !outcome.Settled || outcome.Disagreed {
		t.Fatalf("outcome = %+v, want the want settled", outcome)
	}
	var (
		externalID string
		manual     bool
		recheck    *time.Time
	)
	if err := pool.QueryRow(ctx, `
		SELECT external_id, is_manual, source_recheck_after
		FROM library_file_identities WHERE library_file_id = $1 AND kind = 'source'
	`, fileID).Scan(&externalID, &manual, &recheck); err != nil {
		t.Fatal(err)
	}
	if externalID != "soundcloud:293" || !manual || recheck != nil {
		t.Errorf("identity %q manual %v recheck %v, want a person's identity never asked again",
			externalID, manual, recheck)
	}

	named, err := queries.KeyedFileToName(ctx, targetID)
	if err != nil {
		t.Fatalf("KeyedFileToName() error = %v", err)
	}
	if named.LibraryFileID != fileID || named.Lookup.Title != "PinkPantheress - Illegal (Abo Edit)" {
		t.Fatalf("named = %+v, want the file and what the person confirmed", named)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO artists (name, sort_name, musicbrainz_id) VALUES ('Abo', 'Abo', gen_random_uuid())
	`); err != nil {
		t.Fatal(err)
	}
	artistID, err := queries.NameSourceFile(ctx, NameSourceFileParams{
		LibraryFileID: fileID, Source: "soundcloud", ExternalID: "soundcloud:293",
		Title: named.Lookup.Title, Artist: named.Lookup.Artist, Uploader: named.Lookup.Uploader,
		AccountID: named.Lookup.AccountID, UploaderSummary: "Uploaded it.",
		Image: []byte{0xff, 0xd8}, ContentType: "image/jpeg",
	})
	if err != nil {
		t.Fatalf("NameSourceFile() error = %v", err)
	}
	var artists int
	var account string
	if err := pool.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM artists WHERE name = 'Abo'), external_id
		FROM artists WHERE id = $1
	`, artistID).Scan(&artists, &account); err != nil {
		t.Fatal(err)
	}
	if artists != 2 || account != "soundcloud:https://soundcloud.com/abo" {
		t.Errorf("artists named Abo = %d, account %q, want the uploader apart from the catalogue's",
			artists, account)
	}
	var title, artist string
	var pictured bool
	if err := pool.QueryRow(ctx, `
		SELECT identities.track_title, identities.artist_name,
		       EXISTS (SELECT 1 FROM file_cover_art WHERE library_file_id = $1 AND image IS NOT NULL)
		FROM library_file_identities identities WHERE library_file_id = $1
	`, fileID).Scan(&title, &artist, &pictured); err != nil {
		t.Fatal(err)
	}
	if title != "PinkPantheress - Illegal (Abo Edit)" || artist != "Abo" || !pictured {
		t.Errorf("named %q by %q pictured %v", title, artist, pictured)
	}
}

// A copy judged on the want's Deezer preview that lands after the want was
// keyed proves the preview, not the address. The file is not written the
// preview's key and the want does not settle on it.
func TestACopyProvenOnTheEarlierAnchorDoesNotSettleAKeyedWant(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := anchoredWithoutARecording(ctx, t, pool, "deezer", "2178654")
	if _, err := queries.ClaimTargetImport(ctx, requestID); err != nil {
		t.Fatal(err)
	}
	if _, err := queries.KeyAcquisitionTargetToSource(ctx, keySoundCloud(targetID)); err != nil {
		t.Fatal(err)
	}
	preview, _ := SourceOfAnchor("deezer", "2178654")
	fileID := importedOnTheAnchor(ctx, t, pool, queries, targetID, requestID,
		&AcquiredFileEvidence{
			Name: "worry.flac", Method: "published-sample", Confidence: 1,
			Agrees: []string{"audio"}, Differs: []string{}, Problems: []string{},
			Source: &preview,
		})

	outcome, err := queries.CompleteAcquiredFile(ctx, targetID, "Stopped.", "Proven.")
	if err != nil {
		t.Fatalf("CompleteAcquiredFile() error = %v", err)
	}
	if outcome.Settled || !outcome.Disagreed {
		t.Fatalf("outcome = %+v, want the keyed want left unsettled", outcome)
	}
	var identities int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM library_file_identities WHERE library_file_id = $1
	`, fileID).Scan(&identities); err != nil {
		t.Fatal(err)
	}
	if identities != 0 {
		t.Errorf("identities = %d, want the preview's key kept off the file", identities)
	}
}

// A want whose copy was already accepted has its answer and takes no key.
func TestAWantWithAnAcceptedCopyCannotBeKeyed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := anchoredWithoutARecording(ctx, t, pool, "deezer", "2178654")
	if _, err := queries.ClaimTargetImport(ctx, requestID); err != nil {
		t.Fatal(err)
	}
	preview, _ := SourceOfAnchor("deezer", "2178654")
	importedOnTheAnchor(ctx, t, pool, queries, targetID, requestID, &AcquiredFileEvidence{
		Name: "worry.flac", Method: "published-sample", Confidence: 1,
		Agrees: []string{"audio"}, Differs: []string{}, Problems: []string{}, Source: &preview,
	})

	if _, err := queries.KeyAcquisitionTargetToSource(ctx, keySoundCloud(targetID)); !errors.Is(err, ErrNotKeyable) {
		t.Fatalf("KeyAcquisitionTargetToSource() error = %v, want %v", err, ErrNotKeyable)
	}
}

// A held copy leaves a keyed want on its search ladder, lists it in Review,
// and can be accepted there (ADR 0038 §7, §8).
func TestAHeldCopyOfAKeyedWantKeepsItSearchingAndInReview(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := anchoredWithoutARecording(ctx, t, pool, "deezer", "2178654")
	if _, err := queries.KeyAcquisitionTargetToSource(ctx, keySoundCloud(targetID)); err != nil {
		t.Fatal(err)
	}

	holdOneCopy(ctx, t, queries, targetID, requestID)

	target, err := queries.AcquisitionTarget(ctx, targetID)
	if err != nil {
		t.Fatal(err)
	}
	if target.Status != "unresolved" || target.NextAttemptAt.Valid || !target.NextSearchAt.Valid ||
		target.NextSearchAt.Time.Before(time.Now().Add(30*time.Minute)) {
		t.Errorf("want = %s next %v search %v, want it waiting on its search ladder",
			target.Status, target.NextAttemptAt, target.NextSearchAt)
	}
	page, err := queries.AcquisitionReviewQueue(ctx, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || page.Items[0].Target.ID != targetID || len(page.Items[0].Files) != 1 {
		t.Fatalf("review = %+v, want the keyed want listed with its held copy", page)
	}
	if _, err := queries.AcceptHeldCopy(ctx, page.Items[0].Files[0].ID, "that is it"); err != nil {
		t.Fatalf("AcceptHeldCopy() error = %v", err)
	}
}

func TestOnlyAKeyedWantHasItsOwnFloor(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, _ := anchoredWithoutARecording(ctx, t, pool, "deezer", "2178654")

	if _, err := queries.SetAcquisitionTargetMinimumBitrate(ctx, targetID, 256); !errors.Is(err, ErrNotKeyed) {
		t.Errorf("floor on a want with no address = %v, want %v", err, ErrNotKeyed)
	}
	if _, err := queries.KeyAcquisitionTargetToSource(ctx, keySoundCloud(targetID)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM jobs`); err != nil {
		t.Fatal(err)
	}
	target, err := queries.SetAcquisitionTargetMinimumBitrate(ctx, targetID, 128)
	if err != nil {
		t.Fatalf("SetAcquisitionTargetMinimumBitrate() error = %v", err)
	}
	if target.MinimumBitrate.Int32 != 128 {
		t.Errorf("minimum_bitrate = %v, want 128", target.MinimumBitrate)
	}
	if queuedJobs(ctx, t, pool, "judge_want_copies") != 1 {
		t.Errorf("want the held copies judged again against the new floor")
	}
}

// The schema refuses what ADR 0038 rules out.
func TestTheSchemaHoldsTheShapeOfAKey(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	keyed := `INSERT INTO acquisition_targets (origin, entry_title, status, source, external_id,
	                                            external_url, minimum_bitrate`
	for name, statement := range map[string]string{
		"a key beside a recording": keyed + `, musicbrainz_recording_id)
			VALUES ('playlist', 'x', 'unresolved', 'soundcloud', 'soundcloud:1', 'https://s/1', 320,
			        gen_random_uuid())`,
		"a key with a resolution schedule": keyed + `, next_attempt_at)
			VALUES ('playlist', 'x', 'unresolved', 'soundcloud', 'soundcloud:1', 'https://s/1', 320, now())`,
		"a key with no floor": `INSERT INTO acquisition_targets (origin, entry_title, status, source,
			external_id, external_url)
			VALUES ('playlist', 'x', 'unresolved', 'soundcloud', 'soundcloud:1', 'https://s/1')`,
		"half a key": `INSERT INTO acquisition_targets (origin, entry_title, status, source,
			minimum_bitrate)
			VALUES ('playlist', 'x', 'unresolved', 'soundcloud', 320)`,
		"another service": keyed + `)
			VALUES ('playlist', 'x', 'unresolved', 'vimeo', 'vimeo:1', 'https://v/1', 320)`,
		"a floor on a want with no key": `INSERT INTO acquisition_targets (origin, entry_title, status,
			minimum_bitrate) VALUES ('playlist', 'x', 'unresolved', 320)`,
	} {
		if _, err := pool.Exec(ctx, statement); err == nil {
			t.Errorf("%s was stored", name)
		}
	}
	if _, err := pool.Exec(ctx, keyed+`, acquired_at, anchor_source)
		VALUES ('playlist', 'x', 'acquired', 'soundcloud', 'soundcloud:1', 'https://s/1', 320,
		        now(), 'source')`); err != nil {
		t.Errorf("a keyed want acquired on its excerpt was refused: %v", err)
	}
}
