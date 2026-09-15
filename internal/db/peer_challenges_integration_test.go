package db

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/dbtest"
)

const proveIt = `To prove you are a human downloading these files, please type "open sesame" in this chat to be added to my whitelist.`

// One answer stands for a day, and only for the question it answered. A peer
// that changes a word is asking a new question and gets a new answer.
func TestPeerChallengeAnsweredIsPerPeerPerQuestionAndPerDay(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	queries := New(dbtest.Setup(t))
	now := time.Now()

	if _, err := queries.RecordPeerChallenge(ctx, RecordPeerChallengeParams{
		Username:  "PSXDupe",
		Challenge: proveIt,
		Reply:     pgtype.Text{String: "open sesame", Valid: true},
		SentAt:    pgtype.Timestamptz{Time: now, Valid: true},
		Outcome:   "sent",
	}); err != nil {
		t.Fatal(err)
	}

	answered, err := queries.PeerChallengeAnswered(ctx, PeerChallengeAnsweredParams{
		Username:  "PSXDupe",
		Challenge: proveIt,
		Since:     pgtype.Timestamptz{Time: now.Add(-24 * time.Hour), Valid: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !answered {
		t.Error("a word sent a moment ago did not read as answered")
	}

	for name, params := range map[string]PeerChallengeAnsweredParams{
		"another peer": {
			Username:  "delightful",
			Challenge: proveIt,
			Since:     pgtype.Timestamptz{Time: now.Add(-24 * time.Hour), Valid: true},
		},
		"another question": {
			Username:  "PSXDupe",
			Challenge: "please type \"getthefiles\" in this chat",
			Since:     pgtype.Timestamptz{Time: now.Add(-24 * time.Hour), Valid: true},
		},
		"outside the window": {
			Username:  "PSXDupe",
			Challenge: proveIt,
			Since:     pgtype.Timestamptz{Time: now.Add(time.Hour), Valid: true},
		},
	} {
		answered, err := queries.PeerChallengeAnswered(ctx, params)
		if err != nil {
			t.Fatal(err)
		}
		if answered {
			t.Errorf("%s read as already answered", name)
		}
	}
}

// A message nobody could read is kept once, however often the peer repeats it,
// and stops being shown once something has been sent to that peer.
func TestUnansweredPeerChallengesReportsTheLastQuestionPerPeer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	queries := New(dbtest.Setup(t))

	for _, challenge := range []string{"the first thing it said", "the second thing it said"} {
		if _, err := queries.RecordPeerChallenge(ctx, RecordPeerChallengeParams{
			Username:  "PSXDupe",
			Challenge: challenge,
			Outcome:   "unanswered",
		}); err != nil {
			t.Fatal(err)
		}
	}

	seen, err := queries.PeerChallengeSeen(ctx, PeerChallengeSeenParams{
		Username: "PSXDupe", Challenge: "the first thing it said",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !seen {
		t.Error("a recorded message did not read as seen")
	}

	questions, err := queries.UnansweredPeerChallenges(ctx, []string{"PSXDupe", "delightful"})
	if err != nil {
		t.Fatal(err)
	}
	if len(questions) != 1 || questions[0].Challenge != "the second thing it said" {
		t.Fatalf("questions = %#v, want the last one from PSXDupe", questions)
	}

	// One peer is one line, so what it said before goes when it says something
	// new. What was sent to it is not touched: the once-a-day rule and the
	// daily cap are counted from those rows.
	if err := queries.ForgetPeerQuestions(ctx, "PSXDupe"); err != nil {
		t.Fatal(err)
	}
	questions, err = queries.UnansweredPeerChallenges(ctx, []string{"PSXDupe"})
	if err != nil {
		t.Fatal(err)
	}
	if len(questions) != 0 {
		t.Fatalf("questions = %#v, want none once they were forgotten", questions)
	}

	if _, err := queries.RecordPeerChallenge(ctx, RecordPeerChallengeParams{
		Username:  "PSXDupe",
		Challenge: "the second thing it said",
		Reply:     pgtype.Text{String: "open sesame", Valid: true},
		SentAt:    pgtype.Timestamptz{Time: time.Now(), Valid: true},
		Outcome:   "answered_by_person",
	}); err != nil {
		t.Fatal(err)
	}
	questions, err = queries.UnansweredPeerChallenges(ctx, []string{"PSXDupe"})
	if err != nil {
		t.Fatal(err)
	}
	if len(questions) != 0 {
		t.Fatalf("questions = %#v, want none once the peer was answered", questions)
	}
}

// The daily cap counts every message sent to a peer, whoever sent it.
func TestPeerRepliesSinceCountsWhatWasSent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	queries := New(dbtest.Setup(t))
	now := time.Now()

	for _, sentAt := range []time.Time{now, now.Add(-2 * time.Hour), now.Add(-48 * time.Hour)} {
		if _, err := queries.RecordPeerChallenge(ctx, RecordPeerChallengeParams{
			Username:  "PSXDupe",
			Challenge: sentAt.String(),
			Reply:     pgtype.Text{String: "open sesame", Valid: true},
			SentAt:    pgtype.Timestamptz{Time: sentAt, Valid: true},
			Outcome:   "sent",
		}); err != nil {
			t.Fatal(err)
		}
	}
	// And one nobody could read, which was never sent anywhere.
	if _, err := queries.RecordPeerChallenge(ctx, RecordPeerChallengeParams{
		Username: "PSXDupe", Challenge: "prose", Outcome: "unanswered",
	}); err != nil {
		t.Fatal(err)
	}

	replies, err := queries.PeerRepliesSince(ctx, pgtype.Timestamptz{
		Time: now.Add(-24 * time.Hour), Valid: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if replies != 2 {
		t.Fatalf("replies = %d, want the two sent inside the day", replies)
	}
}

// The pass reads the chat of peers with something open, and of peers whose
// request failed recently — which is what a refused transfer looks like.
func TestPeersAwaitingTransfersCoversOpenAndRecentlyFailedRequests(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)
	now := time.Now()

	albumID := insertAlbumForPeerChallenge(ctx, t, pool)
	failed := insertPeerRequest(ctx, t, pool, albumID, "PSXDupe", "failed", now)
	insertPeerRequest(ctx, t, pool, albumID, "delightful", "started", now)
	insertPeerRequest(ctx, t, pool, albumID, "Elegy", "failed", now.Add(-72*time.Hour))
	insertPeerRequest(ctx, t, pool, albumID, "JohnnyMB", "cancelled", now)

	peers, err := queries.PeersAwaitingTransfers(ctx, PeersAwaitingTransfersParams{
		Provider: "slskd",
		Since:    pgtype.Timestamptz{Time: now.Add(-24 * time.Hour), Valid: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(peers) != 2 {
		t.Fatalf("peers = %#v, want the failed and the started one", peers)
	}
	open := make(map[string]bool, len(peers))
	for _, peer := range peers {
		open[peer.SourceUsername] = peer.Open
	}
	if open["delightful"] != true || open["PSXDupe"] != false {
		t.Fatalf("open = %#v", open)
	}

	requests, err := queries.FailedDownloadRequestsForPeer(ctx, FailedDownloadRequestsForPeerParams{
		Provider: "slskd",
		Username: "PSXDupe",
		Since:    pgtype.Timestamptz{Time: now.Add(-24 * time.Hour), Valid: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 1 || requests[0] != failed {
		t.Fatalf("requests = %#v, want the one that failed", requests)
	}
}

// One live row, however many transfers ask for a read. Two passes over the same
// conversation would answer the same challenge twice.
func TestQueuePeerChallengesHoldsOneLiveRow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	runAfter := time.Now().Add(time.Minute)
	for range 3 {
		if err := queries.QueuePeerChallenges(ctx, runAfter); err != nil {
			t.Fatal(err)
		}
	}

	var queued int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)::int FROM jobs WHERE kind = 'answer_peer_challenges'
	`).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 1 {
		t.Fatalf("jobs queued = %d, want one", queued)
	}
}

func insertAlbumForPeerChallenge(ctx context.Context, t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	artistID, albumID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO artists (id, musicbrainz_id, name, sort_name)
		VALUES ($1, $2, 'Talk Talk', 'Talk Talk')
	`, artistID, uuid.New()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO albums (id, artist_id, title, album_type)
		VALUES ($1, $2, 'Spirit of Eden', 'album')
	`, albumID, artistID); err != nil {
		t.Fatal(err)
	}
	return albumID
}

func insertPeerRequest(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool,
	albumID uuid.UUID, username, status string, updatedAt time.Time,
) uuid.UUID {
	t.Helper()
	requestID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO download_requests (
			id, album_id, provider, source_username, source_directory, status,
			file_count, expected_track_count, total_size_bytes, format, score,
			cancelled_at, updated_at
		)
		VALUES ($1, $2, 'slskd', $3, '@@peer\Music\Folder', $4,
			1, 1, 1, 'flac', 0.5,
			CASE WHEN $4 = 'cancelled' THEN now() END, $5)
	`, requestID, albumID, username, status, updatedAt); err != nil {
		t.Fatal(err)
	}
	return requestID
}
