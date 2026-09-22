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

// A want MusicBrainz has no recording for is searched on its anchor when that
// anchor can admit a copy (ADR 0037). These tests pin the schema and the
// queries that carry it.

// anchoredWithoutARecording records one unresolved want anchored by source, due
// a search, with one request serving it and one completed transfer.
func anchoredWithoutARecording(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool, source, reference string,
) (*Queries, uuid.UUID, uuid.UUID) {
	t.Helper()
	targetID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO acquisition_targets (
			id, origin, entry_artist, entry_title, entry_isrc, entry_duration_ms,
			status, next_attempt_at, next_search_at, attempts,
			anchor_fingerprint, anchor_source, anchor_reference, anchor_seconds,
			anchor_fetched_at
		)
		VALUES ($1, 'playlist', 'LONOWN', 'worry - ultra slowed', 'QZES82415069', 255000,
		        'unresolved', now() + interval '3 days', now(), 2,
		        'AQAAfingerprint', $2, $3, 30, now())
	`, targetID, source, reference); err != nil {
		t.Fatalf("seed want: %v", err)
	}

	requestID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO download_requests (
			id, acquisition_target_id, provider, source_username, source_directory,
			file_count, expected_track_count, total_size_bytes, format, score, files
		)
		VALUES ($1, $2, 'slskd', 'peer', 'Music/LONOWN', 1, 1, 9000000, 'flac', 0.77,
		        $3::jsonb)
	`, requestID, targetID, `[{"path":"Music\\LONOWN\\worry.flac","name":"worry.flac","sizeBytes":9000000}]`,
	); err != nil {
		t.Fatalf("record the request: %v", err)
	}
	queries := New(pool)
	if _, err := queries.StartDownloadRequest(ctx, requestID); err != nil {
		t.Fatalf("start the request: %v", err)
	}
	if _, err := queries.RecordTransferProgress(ctx, requestID, []TransferProgress{{
		Path: `Music\LONOWN\worry.flac`, State: "completed",
		SizeBytes: 9000000, TransferredBytes: 9000000,
	}}); err != nil {
		t.Fatalf("settle the transfer: %v", err)
	}
	return queries, targetID, requestID
}

// importedOnTheAnchor claims the copy, imports it with evidence, and has the
// scan make a library file of it.
func importedOnTheAnchor(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool, queries *Queries,
	targetID, requestID uuid.UUID, evidence *AcquiredFileEvidence,
) uuid.UUID {
	t.Helper()
	if err := queries.CompleteTargetImport(ctx, RecordAcquiredFileParams{
		AcquisitionTargetID: targetID, Provider: "slskd", SourceUsername: "peer",
		RemotePath: `Music\LONOWN\worry.flac`, FileName: "worry.flac", SizeBytes: 9000000,
		Verdict: AcquiredFileAccepted, Summary: "Proven by its anchor.",
		Evidence:          evidence,
		DownloadRequestID: uuid.NullUUID{UUID: requestID, Valid: true},
	}, "It is being taken into your library.",
		"/music", "/music/LONOWN", map[string]ImportedDownloadFile{
			`Music\LONOWN\worry.flac`: {LocalPath: "/music/LONOWN/worry.flac"},
		}); err != nil {
		t.Fatalf("import the proven copy: %v", err)
	}
	fileID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_files (id, path, size_bytes, modified_at)
		VALUES ($1, '/music/LONOWN/worry.flac', 9000000, now())
	`, fileID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE downloads SET library_file_id = $2 WHERE download_request_id = $1
	`, requestID, fileID); err != nil {
		t.Fatal(err)
	}
	return fileID
}

func TestAWantWithNoRecordingIsClaimedOnItsAnchor(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, _, requestID := anchoredWithoutARecording(ctx, t, pool, "deezer", "2178654")

	row, err := queries.ClaimTargetImport(ctx, requestID)
	if err != nil {
		t.Fatalf("ClaimTargetImport() error = %v", err)
	}
	if row.MusicBrainzRecordingID != uuid.Nil {
		t.Errorf("recording = %s, want none", row.MusicBrainzRecordingID)
	}
	if row.AnchorSource != "deezer" || row.AnchorReference != "2178654" ||
		row.EntryISRC != "QZES82415069" || row.EntryDurationMS != 255000 {
		t.Errorf("row = %+v, want the anchor and the whole entry", row)
	}
}

// A plain name-found upload cannot admit, so a copy for such a want has
// nothing to be judged against.
func TestAWantAnchoredByANameFoundUploadIsNotClaimed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, _, requestID := anchoredWithoutARecording(ctx, t, pool, "youtube", "dQw4w9WgXcQ")

	if _, err := queries.ClaimTargetImport(ctx, requestID); !errors.Is(err, ErrNotATargetImport) {
		t.Fatalf("ClaimTargetImport() error = %v, want %v", err, ErrNotATargetImport)
	}
}

// The admitted copy becomes a library file with a source identity keyed by the
// anchor, and the want is acquired with no recording and nothing scheduled
// (ADR 0037 §2, §4).
func TestACopyProvenByTheAnchorSettlesAWantWithNoRecording(t *testing.T) {
	for _, test := range []struct {
		anchor, reference string
		byHand            bool
		source, key, url  string
	}{
		{"deezer", "2178654", false, "deezer", "deezer:2178654",
			"https://www.deezer.com/track/2178654"},
		{"youtube-topic", "dQw4w9WgXcQ", false, "youtube", "youtube:dQw4w9WgXcQ",
			"https://www.youtube.com/watch?v=dQw4w9WgXcQ"},
		{"deezer", "2178654", true, "deezer", "deezer:2178654",
			"https://www.deezer.com/track/2178654"},
	} {
		t.Run(test.anchor, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			pool := dbtest.Setup(t)
			queries, targetID, requestID := anchoredWithoutARecording(
				ctx, t, pool, test.anchor, test.reference)
			if _, err := queries.ClaimTargetImport(ctx, requestID); err != nil {
				t.Fatal(err)
			}
			key, ok := SourceOfAnchor(test.anchor, test.reference)
			if !ok {
				t.Fatalf("SourceOfAnchor(%q) named no key", test.anchor)
			}
			fileID := importedOnTheAnchor(ctx, t, pool, queries, targetID, requestID,
				&AcquiredFileEvidence{
					Name: "worry.flac", Method: "published-sample", Confidence: 1,
					Agrees: []string{"audio"}, Differs: []string{}, Problems: []string{},
					Anchor: &ImportAnchor{
						Source: test.anchor, Reference: test.reference, Measured: true, Rate: 0.04,
					},
					Source: &key,
				})
			if test.byHand {
				if _, err := pool.Exec(ctx, `
					UPDATE acquisition_target_files SET decided_by = 'user'
					WHERE acquisition_target_id = $1
				`, targetID); err != nil {
					t.Fatal(err)
				}
			}

			outcome, err := queries.CompleteAcquiredFile(ctx, targetID,
				"A copy was imported.", "Proven by its anchor.")
			if err != nil {
				t.Fatalf("CompleteAcquiredFile() error = %v", err)
			}
			if !outcome.Settled || outcome.Disagreed || outcome.LibraryFileID.UUID != fileID {
				t.Fatalf("outcome = %+v, want the want settled against %s", outcome, fileID)
			}

			var (
				kind, source, externalID, externalURL, method, status, isrc string
				manual                                                      bool
				recording                                                   uuid.NullUUID
				recheck                                                     *time.Time
			)
			if err := pool.QueryRow(ctx, `
				SELECT identities.kind, identities.source, identities.external_id,
				       identities.external_url, identities.method, identities.is_manual,
				       identities.musicbrainz_recording_id, files.resolution_status,
				       coalesce(identities.isrc, ''), identities.source_recheck_after
				FROM library_file_identities identities
				JOIN library_files files ON files.id = identities.library_file_id
				WHERE identities.library_file_id = $1
			`, fileID).Scan(&kind, &source, &externalID, &externalURL, &method, &manual,
				&recording, &status, &isrc, &recheck); err != nil {
				t.Fatalf("read the identity: %v", err)
			}
			// The preview's ISRC is what a recording has to carry for the file to
			// move onto it, and only an automatic identity is asked about again,
			// a day on (ADR 0037 §6).
			wantISRC := ""
			if test.anchor == "deezer" {
				wantISRC = "QZES82415069"
			}
			if isrc != wantISRC {
				t.Errorf("isrc = %q, want %q", isrc, wantISRC)
			}
			if test.byHand != (recheck == nil) {
				t.Errorf("next look = %v, want one only for an automatic identity", recheck)
			}
			if recheck != nil {
				if wait := time.Until(*recheck); wait < 23*time.Hour || wait > 25*time.Hour {
					t.Errorf("next look in %v, want a day", wait)
				}
			}
			if kind != "source" || source != test.source || externalID != test.key ||
				externalURL != test.url || recording.Valid || status != "source" {
				t.Errorf("identity = %s %s %s %s (recording %v), file %s, want the source key",
					kind, source, externalID, externalURL, recording, status)
			}
			if manual != test.byHand {
				t.Errorf("is_manual = %v, want %v", manual, test.byHand)
			}
			if test.byHand && method != "manual" {
				t.Errorf("method = %q, want a person's decision", method)
			}

			target, err := queries.AcquisitionTarget(ctx, targetID)
			if err != nil {
				t.Fatal(err)
			}
			if target.Status != "acquired" || target.MusicBrainzRecordingID.Valid ||
				target.NextAttemptAt.Valid || target.NextSearchAt.Valid ||
				target.AcquiredLibraryFileID.UUID != fileID {
				t.Errorf("want = %s recording %v next %v search %v file %v, want acquired "+
					"with no recording and nothing scheduled", target.Status,
					target.MusicBrainzRecordingID, target.NextAttemptAt, target.NextSearchAt,
					target.AcquiredLibraryFileID)
			}
		})
	}
}

// A person accepting a held copy writes the anchor's address, and a name-found
// upload has none (ADR 0037 §5).
func TestAHeldCopyCanBeAcceptedOnlyWhereTheAnchorNamesATrack(t *testing.T) {
	for _, test := range []struct {
		anchor   string
		accepted bool
	}{
		{"deezer", true}, {"youtube-topic", true}, {"youtube", false},
	} {
		t.Run(test.anchor, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			pool := dbtest.Setup(t)
			queries, targetID, requestID := anchoredWithoutARecording(
				ctx, t, pool, "deezer", "2178654")
			holdOneCopy(ctx, t, queries, targetID, requestID)
			if _, err := pool.Exec(ctx, `
				UPDATE acquisition_targets
				SET anchor_source = $2,
				    next_search_at = CASE WHEN $2 = 'youtube' THEN NULL ELSE next_search_at END
				WHERE id = $1
			`, targetID, test.anchor); err != nil {
				t.Fatal(err)
			}
			copies, err := queries.AcquisitionTargetFiles(ctx, targetID)
			if err != nil {
				t.Fatal(err)
			}

			_, err = queries.AcceptHeldCopy(ctx, copies[0].ID, "that is the one")
			if test.accepted && err != nil {
				t.Fatalf("AcceptHeldCopy() error = %v", err)
			}
			if !test.accepted && !errors.Is(err, ErrNotWaitingForADecision) {
				t.Fatalf("AcceptHeldCopy() error = %v, want %v", err, ErrNotWaitingForADecision)
			}
		})
	}
}

// Holding a copy puts the want back on its search, and leaves resolution's
// schedule where it was.
func TestSettlingACopyOfAWantWithNoRecordingMovesOnlyItsSearch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := anchoredWithoutARecording(ctx, t, pool, "deezer", "2178654")
	before, err := queries.AcquisitionTarget(ctx, targetID)
	if err != nil {
		t.Fatal(err)
	}

	holdOneCopy(ctx, t, queries, targetID, requestID)

	after, err := queries.AcquisitionTarget(ctx, targetID)
	if err != nil {
		t.Fatal(err)
	}
	if !after.NextAttemptAt.Time.Equal(before.NextAttemptAt.Time) {
		t.Errorf("next_attempt_at = %v, want resolution's schedule left at %v",
			after.NextAttemptAt.Time, before.NextAttemptAt.Time)
	}
	if !after.NextSearchAt.Valid || after.NextSearchAt.Time.Before(time.Now().Add(30*time.Minute)) {
		t.Errorf("next_search_at = %v, want the search moved to the time it was given",
			after.NextSearchAt)
	}
	if after.Summary != "Not in your library yet." {
		t.Errorf("summary = %q, want the want told what came of its copy", after.Summary)
	}
}

// A search that found nothing climbs the search's own counter.
func TestASearchThatFoundNothingCountsOnTheSearchLadder(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, _ := anchoredWithoutARecording(ctx, t, pool, "deezer", "2178654")
	next := time.Now().Add(24 * time.Hour).Truncate(time.Microsecond)

	if err := queries.RequeueAcquisitionTarget(ctx, targetID, "none",
		"No peer is sharing a copy yet.", "Nothing on offer.", "", next); err != nil {
		t.Fatalf("RequeueAcquisitionTarget() error = %v", err)
	}

	target, err := queries.AcquisitionTarget(ctx, targetID)
	if err != nil {
		t.Fatal(err)
	}
	if target.SearchAttempts != 1 || target.Attempts != 2 {
		t.Errorf("search_attempts = %d, attempts = %d, want 1 and resolution's 2 untouched",
			target.SearchAttempts, target.Attempts)
	}
	if !target.NextSearchAt.Time.Equal(next) || target.NextAttemptAt.Time.Equal(next) {
		t.Errorf("next_search_at = %v, next_attempt_at = %v, want only the search moved",
			target.NextSearchAt.Time, target.NextAttemptAt.Time)
	}
}

// Resolution finding a recording moves the want to the recording path and ends
// its search (ADR 0037 §2).
func TestResolvingAWantEndsItsSearchOnTheAnchor(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, _ := anchoredWithoutARecording(ctx, t, pool, "deezer", "2178654")

	resolved, err := queries.ResolveAcquisitionTarget(ctx, ResolveAcquisitionTargetParams{
		ID:                     targetID,
		MusicBrainzRecordingID: uuid.New(), ResolutionMethod: "isrc",
		Summary: "Resolved.", Detail: "Resolved.", NextAttemptAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("ResolveAcquisitionTarget() error = %v", err)
	}
	if resolved.Status != "pending" || resolved.NextSearchAt.Valid || resolved.SearchAttempts != 0 {
		t.Errorf("want = %s search %v (%d), want pending with no search scheduled",
			resolved.Status, resolved.NextSearchAt, resolved.SearchAttempts)
	}
}

// The due query reads only unresolved wants whose anchor can admit.
func TestOnlyAnAdmittingAnchorMakesAWantWithNoRecordingDue(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	wants := map[string]uuid.UUID{}
	for _, source := range []string{"deezer", "youtube-topic", "youtube"} {
		id := uuid.New()
		wants[source] = id
		if _, err := pool.Exec(ctx, `
			INSERT INTO acquisition_targets (id, origin, entry_artist, entry_title, status,
			                                 next_attempt_at, anchor_next_attempt_at)
			VALUES ($1, 'playlist', 'LONOWN', 'worry', 'unresolved', now() + interval '1 day', now())
		`, id); err != nil {
			t.Fatal(err)
		}
		if err := queries.RecordAnchor(ctx, AnchorParams{
			TargetID: id, Fingerprint: "AQAAfingerprint", Source: source,
			Reference: "ref", Seconds: 30,
		}); err != nil {
			t.Fatalf("RecordAnchor(%s) error = %v", source, err)
		}
	}

	due, err := queries.DueSearchTargets(ctx, time.Now().Add(time.Second), 10)
	if err != nil {
		t.Fatalf("DueSearchTargets() error = %v", err)
	}
	found := map[uuid.UUID]bool{}
	for _, target := range due {
		found[target.ID] = true
	}
	if !found[wants["deezer"]] || !found[wants["youtube-topic"]] || found[wants["youtube"]] ||
		len(due) != 2 {
		t.Errorf("due = %d wants, want the Deezer and Topic ones and not the name-found one", len(due))
	}
	next, err := queries.NextSearchTargetDue(ctx)
	if err != nil || !next.Valid {
		t.Errorf("NextSearchTargetDue() = %v, %v, want a time", next, err)
	}
	var sweeps int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM jobs WHERE kind = 'sweep_acquisition_targets' AND status = 'queued'
	`).Scan(&sweeps); err != nil {
		t.Fatal(err)
	}
	if sweeps != 1 {
		t.Errorf("sweeps queued = %d, want the anchor landing to ask for one", sweeps)
	}
}

// The schema refuses the two states ADR 0037 rules out.
func TestTheSchemaRefusesASearchScheduleOffAnUnresolvedWant(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	for name, statement := range map[string]string{
		"pending with no recording": `
			INSERT INTO acquisition_targets (origin, entry_title, status, anchor_source)
			VALUES ('playlist', 'worry', 'pending', 'deezer')`,
		"a search on a pending want": `
			INSERT INTO acquisition_targets (origin, entry_title, status,
			                                 musicbrainz_recording_id, next_search_at)
			VALUES ('playlist', 'worry', 'pending', gen_random_uuid(), now())`,
		"acquired with no recording on a name-found upload": `
			INSERT INTO acquisition_targets (origin, entry_title, status, acquired_at,
			                                 anchor_source)
			VALUES ('playlist', 'worry', 'acquired', now(), 'youtube')`,
	} {
		if _, err := pool.Exec(ctx, statement); err == nil {
			t.Errorf("%s was stored", name)
		}
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO acquisition_targets (origin, entry_title, status, acquired_at, anchor_source)
		VALUES ('playlist', 'worry', 'acquired', now(), 'deezer')
	`); err != nil {
		t.Errorf("acquired with no recording on a Deezer preview was refused: %v", err)
	}
}
