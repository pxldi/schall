package identity

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/dbtest"
	"github.com/rs/zerolog"
)

// A file can be identified by another file. The library holds a file whose
// identity somebody settled by hand; a copy that turns out to hold the same
// music is that recording too, on the strength of that decision, and the
// identity written for it says which file admitted it.
//
// The decision is therefore lent rather than given. These tests are about
// getting it back: when the decision is taken back, everything admitted on the
// strength of it is withdrawn, and everything admitted on the strength of those.

// chain builds three library files. The first is settled by hand, the second is
// admitted by the first, and the third is admitted by the second.
type chain struct {
	pool             *pgxpool.Pool
	decided          uuid.UUID
	admitted, second uuid.UUID
	recordingID      uuid.UUID
}

func setupChain(t *testing.T, ctx context.Context) chain {
	t.Helper()
	pool := dbtest.Setup(t)
	built := chain{
		pool: pool, decided: uuid.New(), admitted: uuid.New(), second: uuid.New(),
		recordingID: uuid.New(),
	}

	for index, fileID := range []uuid.UUID{built.decided, built.admitted, built.second} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO library_files (
			    id, path, size_bytes, modified_at, artist_tag, title_tag, duration_ms,
			    fingerprint, resolution_status
			) VALUES ($1, $2, 1024, now(), 'Anetha', 'Candy from Strangers', 487000,
			          'AQAAS47CSxLaY_vB5yK2H-0yC1fs4zmuI0wd', 'resolved')
		`, fileID, "/music/anetha/"+fileID.String()+".flac"); err != nil {
			t.Fatal(err)
		}
		_ = index
	}

	// The decision a person made. Everything below hangs off it.
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_file_identities (
		    library_file_id, kind, musicbrainz_recording_id, track_title, method,
		    confidence, is_manual, summary
		) VALUES ($1, 'external', $2, 'Candy from Strangers', 'manual', 1, true,
		          'Resolved by hand.')
	`, built.decided, built.recordingID); err != nil {
		t.Fatal(err)
	}
	// The copy that decision admitted, and the copy that one admitted in turn.
	for _, borrowed := range []struct{ file, ancestor uuid.UUID }{
		{built.admitted, built.decided},
		{built.second, built.admitted},
	} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO library_file_identities (
			    library_file_id, kind, musicbrainz_recording_id, track_title, method,
			    confidence, is_manual, summary, admitted_by_file_id, admitted_by_proof
			) VALUES ($1, 'external', $2, 'Candy from Strangers', 'library-witness', 1,
			          false, 'Proven by a file you already own.', $3, 'manual')
		`, borrowed.file, built.recordingID, borrowed.ancestor); err != nil {
			t.Fatal(err)
		}
	}
	return built
}

func (built chain) identityOf(t *testing.T, ctx context.Context, fileID uuid.UUID) bool {
	t.Helper()
	var exists bool
	if err := built.pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM library_file_identities WHERE library_file_id = $1)
	`, fileID).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	return exists
}

func (built chain) statusOf(t *testing.T, ctx context.Context, fileID uuid.UUID) (string, string) {
	t.Helper()
	var status string
	var summary *string
	if err := built.pool.QueryRow(ctx, `
		SELECT resolution_status, resolution_summary FROM library_files WHERE id = $1
	`, fileID).Scan(&status, &summary); err != nil {
		t.Fatal(err)
	}
	if summary == nil {
		return status, ""
	}
	return status, *summary
}

// The whole chain falls. A person taking back the decision at the head of it
// leaves nothing behind that was identified on the strength of it, however many
// files away that is.
func TestWithdrawingADecisionWithdrawsEveryIdentityItAdmitted(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	built := setupChain(t, ctx)
	service := NewService(built.pool, &stubProvider{}, zerolog.Nop())

	if err := service.Clear(ctx, built.decided); err != nil {
		t.Fatalf("Clear() error = %v", err)
	}

	for _, fileID := range []uuid.UUID{built.admitted, built.second} {
		if built.identityOf(t, ctx, fileID) {
			t.Errorf("file %s still has an identity, want it withdrawn with its ancestor", fileID)
		}
		status, summary := built.statusOf(t, ctx, fileID)
		if status != StatusNeedsReview {
			t.Errorf("status = %q, want the file to be a question again", status)
		}
		if summary != withdrawnWithItsAncestor {
			t.Errorf("summary = %q, want the sentence saying why", summary)
		}
	}
}

// A person's own answer is permanent. Somebody who decided about the second file
// themselves has answered it, and taking back the decision behind the first one
// must not take back theirs.
func TestWithdrawingADecisionLeavesAManualAnswerBelowItAlone(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	built := setupChain(t, ctx)
	if _, err := built.pool.Exec(ctx, `
		UPDATE library_file_identities
		SET is_manual = true, method = 'manual'
		WHERE library_file_id = $1
	`, built.admitted); err != nil {
		t.Fatal(err)
	}
	service := NewService(built.pool, &stubProvider{}, zerolog.Nop())

	if err := service.Clear(ctx, built.decided); err != nil {
		t.Fatalf("Clear() error = %v", err)
	}

	if !built.identityOf(t, ctx, built.admitted) {
		t.Error("a decision somebody made by hand was withdrawn, want it kept")
	}
	if !built.identityOf(t, ctx, built.second) {
		t.Error("a file below a manual answer was withdrawn, want the chain to stop there")
	}
}

// Choosing a different recording is the same event: what the old answer lent is
// taken back with it. The file whose answer changed keeps its own identity, and
// it is the new one.
func TestChoosingAnotherRecordingWithdrawsWhatTheOldOneAdmitted(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	built := setupChain(t, ctx)
	other := uuid.New()
	if _, err := built.pool.Exec(ctx, `
		INSERT INTO library_file_candidates (
		    library_file_id, musicbrainz_recording_id, artist_name, track_title, rank
		) VALUES ($1, $2, 'Anetha', 'Candy from Strangers (edit)', 1)
	`, built.decided, other); err != nil {
		t.Fatal(err)
	}
	service := NewService(built.pool, &stubProvider{}, zerolog.Nop())

	if err := service.Accept(ctx, built.decided, other); err != nil {
		t.Fatalf("Accept() error = %v", err)
	}

	if built.identityOf(t, ctx, built.admitted) {
		t.Error("an identity admitted by the old answer survived it, want it withdrawn")
	}
	if !built.identityOf(t, ctx, built.decided) {
		t.Error("the file that was decided about has no identity, want the new one")
	}
}

// A want re-arms even when the file it stopped on borrowed its identity rather
// than being the file the decision was about directly.
//
// A want's copy can land as any file in the chain — here it lands as `second`,
// two files below the decision. The want stopped because the library called
// that copy a different recording, and it has no time to be looked at again on
// its own. Taking back the decision at the head of the chain withdraws
// `second`'s identity too, and the want holding it must get its next look
// back in the same transaction: db.RearmWantsHoldingFile only reads the one
// file it is given, so this is wasted unless every fallen file is rearmed, not
// only the file the decision was about.
func TestWithdrawingADecisionRearmsAWantHoldingAFileTwoLevelsBelowIt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	built := setupChain(t, ctx)

	targetID := uuid.New()
	if _, err := built.pool.Exec(ctx, `
		INSERT INTO acquisition_targets (
		    id, origin, entry_artist, entry_title, musicbrainz_recording_id,
		    status, summary
		) VALUES ($1, 'manual', 'Anetha', 'Candy from Strangers', $2,
		          'pending', 'your library calls that file a different recording')
	`, targetID, built.recordingID); err != nil {
		t.Fatal(err)
	}
	if _, err := built.pool.Exec(ctx, `
		INSERT INTO acquisition_target_files (
		    acquisition_target_id, provider, source_username, remote_path, verdict,
		    library_file_id
		) VALUES ($1, 'slskd', 'peer', '/peer/03.flac', 'accepted', $2)
	`, targetID, built.second); err != nil {
		t.Fatal(err)
	}

	service := NewService(built.pool, &stubProvider{}, zerolog.Nop())
	if err := service.Clear(ctx, built.decided); err != nil {
		t.Fatalf("Clear() error = %v", err)
	}

	var nextAttemptAt *time.Time
	var summary string
	if err := built.pool.QueryRow(ctx, `
		SELECT next_attempt_at, summary FROM acquisition_targets WHERE id = $1
	`, targetID).Scan(&nextAttemptAt, &summary); err != nil {
		t.Fatal(err)
	}
	if nextAttemptAt == nil {
		t.Fatal("the want still has no next look, so nothing will read the withdrawal")
	}
	if summary != db.RearmedByADecisionSummary {
		t.Errorf("summary = %q, want %q", summary, db.RearmedByADecisionSummary)
	}
}

// Nothing else moves. A file identified on its own evidence owes the decision
// nothing, and taking the decision back leaves it exactly as it was.
func TestWithdrawingADecisionLeavesUnrelatedIdentitiesAlone(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	built := setupChain(t, ctx)
	unrelated := uuid.New()
	if _, err := built.pool.Exec(ctx, `
		INSERT INTO library_files (
		    id, path, size_bytes, modified_at, title_tag, resolution_status
		) VALUES ($1, '/music/other.flac', 1024, now(), 'Something else', 'resolved')
	`, unrelated); err != nil {
		t.Fatal(err)
	}
	if _, err := built.pool.Exec(ctx, `
		INSERT INTO library_file_identities (
		    library_file_id, kind, musicbrainz_recording_id, method, confidence, summary
		) VALUES ($1, 'external', $2, 'fingerprint', 1, 'Identified by its audio.')
	`, unrelated, uuid.New()); err != nil {
		t.Fatal(err)
	}
	service := NewService(built.pool, &stubProvider{}, zerolog.Nop())

	if err := service.Clear(ctx, built.decided); err != nil {
		t.Fatalf("Clear() error = %v", err)
	}

	if !built.identityOf(t, ctx, unrelated) {
		t.Error("an unrelated identity was withdrawn")
	}
	if status, _ := built.statusOf(t, ctx, unrelated); status != StatusResolved {
		t.Errorf("status = %q, want the unrelated file left resolved", status)
	}
}
