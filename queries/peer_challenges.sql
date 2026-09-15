-- name: RecordPeerChallenge :one
-- Keep what a peer asked and what was sent back. A row with no reply is a
-- message nobody here could read; it is what the Downloads page shows and what
-- a person answers by hand.
INSERT INTO peer_challenges (username, challenge, reply, sent_at, outcome)
VALUES (
    sqlc.arg('username'),
    sqlc.arg('challenge'),
    sqlc.narg('reply'),
    sqlc.narg('sent_at'),
    sqlc.arg('outcome')
)
RETURNING *;

-- name: PeerChallengeAnswered :one
-- Whether this peer has already been sent something for this exact question
-- since a moment. A peer that changes a word changes the question, and gets one
-- answer for the new one.
SELECT EXISTS (
    SELECT 1
    FROM peer_challenges
    WHERE username = sqlc.arg('username')
      AND challenge = sqlc.arg('challenge')
      AND outcome <> 'unanswered'
      AND sent_at >= sqlc.arg('since')
) AS answered;

-- name: PeerChallengeSeen :one
-- Whether this exact message has been recorded at all. The peers that challenge
-- send the same lines every time a transfer is requested, and one unread
-- message is one row rather than one a day.
SELECT EXISTS (
    SELECT 1
    FROM peer_challenges
    WHERE username = sqlc.arg('username')
      AND challenge = sqlc.arg('challenge')
) AS seen;

-- name: ForgetPeerQuestions :exec
-- Drop what this peer said before, so one peer is one line on the Downloads
-- page. Only the messages nobody could read: what was sent stays as the record
-- the once-a-day rule and the daily cap are counted from.
DELETE FROM peer_challenges
WHERE username = sqlc.arg('username')
  AND outcome = 'unanswered';

-- name: PeerRepliesSince :one
-- How many messages have been sent to peers since a moment, whoever sent them.
-- The Soulseek server bans this account for flooding private chat, and this is
-- what the daily cap is counted from.
SELECT count(*)::bigint AS replies
FROM peer_challenges
WHERE sent_at >= sqlc.arg('since');

-- name: UnansweredPeerChallenges :many
-- The last thing each of these peers said that could not be read, unless
-- something has been sent to that peer since. One line per peer: the Downloads
-- page shows it beside the request that peer is holding up.
SELECT DISTINCT ON (username) id, username, challenge, created_at
FROM peer_challenges
WHERE username = ANY(sqlc.arg('usernames')::text[])
  AND outcome = 'unanswered'
  AND NOT EXISTS (
      SELECT 1
      FROM peer_challenges answered
      WHERE answered.username = peer_challenges.username
        AND answered.outcome <> 'unanswered'
        AND answered.created_at > peer_challenges.created_at
  )
ORDER BY username, created_at DESC;

-- name: PeersAwaitingTransfers :many
-- The peers worth reading private chat from: the ones with a request still
-- open, and the ones whose request failed recently enough that a challenge is
-- what it failed on. Open says whether anything is still waiting, which is what
-- decides whether to look again in a minute.
SELECT
    source_username,
    bool_or(status IN ('requested', 'started')) AS open
FROM download_requests
WHERE provider = sqlc.arg('provider')
  AND (
      status IN ('requested', 'started')
      OR (status = 'failed' AND updated_at >= sqlc.arg('since'))
  )
GROUP BY source_username
ORDER BY source_username;

-- name: FailedDownloadRequestsForPeer :many
-- The requests to this peer that failed recently, so the files it refused can
-- be asked for again once it has been answered.
SELECT id
FROM download_requests
WHERE provider = sqlc.arg('provider')
  AND source_username = sqlc.arg('username')
  AND status = 'failed'
  AND updated_at >= sqlc.arg('since')
ORDER BY updated_at DESC;
