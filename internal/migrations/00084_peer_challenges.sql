-- +goose Up
-- What a Soulseek peer asked in private chat, and what was sent back.
--
-- A few peers run a plugin that holds every transfer until one word is typed in
-- chat. The word is theirs and the challenge text is how they asked for it, so
-- both are kept verbatim: the once-a-day rule below compares the text a peer
-- sent, and a peer that changes its wording is asking a new question.
--
-- A row with no reply is a message nobody here could read. It is shown on the
-- Downloads page with the request it is holding up, and answered by hand.
CREATE TABLE peer_challenges (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    username TEXT NOT NULL,
    challenge TEXT NOT NULL,
    reply TEXT,
    sent_at TIMESTAMPTZ,
    outcome TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT peer_challenges_username_present CHECK (btrim(username) <> ''),
    CONSTRAINT peer_challenges_challenge_present CHECK (btrim(challenge) <> ''),
    CONSTRAINT peer_challenges_outcome_valid
        CHECK (outcome IN ('sent', 'requested_new', 'answered_by_person', 'unanswered')),
    -- A row either records something that was sent or records that nothing
    -- was. It can never be half of each.
    CONSTRAINT peer_challenges_reply_consistent
        CHECK ((outcome = 'unanswered') = (reply IS NULL AND sent_at IS NULL))
);

-- The two questions asked of this table: what has this peer already been sent,
-- and how much has been sent altogether today.
CREATE INDEX peer_challenges_username_idx ON peer_challenges (username, sent_at DESC);
CREATE INDEX peer_challenges_sent_at_idx ON peer_challenges (sent_at DESC)
    WHERE sent_at IS NOT NULL;

-- The unread messages are read by one job at a time, in the shape 00007 gives
-- the download poll. Two passes over the same conversation would answer the
-- same challenge twice, and the Soulseek server bans this account for repeating
-- itself in private chat.
CREATE UNIQUE INDEX jobs_answer_peer_challenges_idx
    ON jobs (kind)
    WHERE kind = 'answer_peer_challenges' AND status IN ('queued', 'running');

-- +goose Down
DROP INDEX IF EXISTS jobs_answer_peer_challenges_idx;
DROP TABLE IF EXISTS peer_challenges;
