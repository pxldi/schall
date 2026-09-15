package db

import (
	"context"
	"time"
)

// QueuePeerChallenges asks for the private chat of the peers holding transfers
// to be read, unless that pass is already queued or running.
//
// DO NOTHING rather than moving an existing row earlier, which is what makes
// this a floor: a request started, a transfer refused and a second request
// started in the same minute are one read of the same messages. The Soulseek
// server bans this account for repeating itself in private chat, so the pass
// that decides what to send is the one thing that must not run in a burst.
func (q *Queries) QueuePeerChallenges(ctx context.Context, runAfter time.Time) error {
	_, err := q.db.Exec(ctx, `
		INSERT INTO jobs (kind, payload, max_attempts, run_after)
		VALUES ('answer_peer_challenges', '{}'::jsonb, 3, $1)
		ON CONFLICT (kind)
		    WHERE kind = 'answer_peer_challenges' AND status IN ('queued', 'running')
		DO NOTHING
	`, runAfter)
	return err
}
