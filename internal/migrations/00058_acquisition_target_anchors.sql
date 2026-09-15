-- +goose Up
-- A want (acquisition_targets) is one piece of music somebody asked for that is
-- not in the library yet. Before Schall lets in a copy a stranger sent for one,
-- something has to prove the audio is that recording.
--
-- Until now only AcoustID could. AcoustID turns audio into a fingerprint and
-- answers with the MusicBrainz recordings that fingerprint belongs to, so music
-- MusicBrainz has never heard of can be neither admitted nor refused: every copy
-- fetched for it becomes a question for a person. On 2026-08-16 that was 129
-- copies behind 44 questions, and AcoustID named a recording for none of them.
--
-- An anchor is a second witness that needs no catalogue entry. Deezer publishes
-- a thirty-second preview of every track it carries, on a public address, with
-- no key needed, and it is fetched by the want's own ISRC — the registration code
-- the recording was published under, an exact key and not a text search. So the
-- audio that comes back is the audio of exactly the recording the want names.
-- The columns below hold its fingerprint. See
-- docs/decisions/0024-a-distributors-preview-is-an-audio-anchor.md.
--
-- The anchor is stored on the want rather than fetched when a copy is judged.
-- That is not an optimisation: it keeps every decision independent of a third
-- party being reachable at the moment somebody is waiting, no rate limit can
-- delay an answer, and the evidence that decided a copy is still on record long
-- after the preview address has rotted.
--
-- Nothing reads these columns to admit anything yet. The gate is switched on for
-- refusing first and for admitting separately, which is the third precondition
-- of that decision record.
ALTER TABLE acquisition_targets
    -- The fingerprint of the preview audio, in the compressed form fpcalc emits
    -- and every other fingerprint in Schall is kept in.
    ADD COLUMN anchor_fingerprint TEXT,
    -- Who published the preview and what they call the track there. Provenance:
    -- nothing branches on it, but an anchor nobody can trace back to a published
    -- track is an anchor nobody can check.
    ADD COLUMN anchor_source TEXT,
    ADD COLUMN anchor_reference TEXT,
    -- How long the preview audio ran, as the decoder measured it rather than as
    -- anybody claimed.
    ADD COLUMN anchor_seconds INTEGER,
    ADD COLUMN anchor_fetched_at TIMESTAMPTZ,
    -- Why there is no fingerprint, when there is none. "the distributor has no
    -- preview for this ISRC" and "the fetch failed" are different facts and must
    -- never read as one sentence: the first is silence, which is never agreement,
    -- and the second is a check that could not run and is asked again
    -- (docs/decisions/0002).
    ADD COLUMN anchor_unavailable TEXT,
    -- The requeue, kept apart from the want's own next_attempt_at because they
    -- are different schedules: that one is when to look for a copy again, this
    -- one is when to look for the preview again, and a want that has been
    -- acquired still wants its anchor. NULL means nothing is scheduled.
    ADD COLUMN anchor_attempts INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN anchor_next_attempt_at TIMESTAMPTZ;

-- The sweep asks one question — which wants are due an anchor — and this is the
-- whole of its answer. Wants that already have one are the overwhelming majority
-- once the collection settles, so they are left out of the index rather than
-- walked past.
CREATE INDEX acquisition_targets_anchor_due_idx
    ON acquisition_targets (anchor_next_attempt_at)
    WHERE anchor_fingerprint IS NULL AND anchor_next_attempt_at IS NOT NULL;

-- One anchor fetcher, enforced by the table rather than hoped for, exactly as
-- the picture and lyrics sweeps are. Deezer is a public service with no key and
-- no bill that did not ask to be asked, and it refuses a caller that asks too
-- fast. The sweep paces itself, which is only a pace if there is one sweep.
CREATE UNIQUE INDEX jobs_preview_anchor_sweep_idx
    ON jobs (kind)
    WHERE kind = 'sweep_preview_anchors' AND status IN ('queued', 'running');

-- The wants that already exist. A want created from here on is given a due time
-- when it is created, but the ones already on record were written before there
-- was anything to schedule, and they are the ones the queue is full of. A want
-- with no ISRC is left alone: there is no exact key to fetch a preview by, and a
-- text search returns the wrong recording often enough that it is refused
-- outright — a search for "Ivy" was answered with "White Ferrari".
--
-- Only the wants that can still receive a copy. An anchor is what a copy is
-- judged against, so a want somebody stopped looking for, one that already has
-- its music, and one replaced by another have nothing to judge — and fetching
-- their previews would be a run of requests to a free public service for
-- answers nothing would ever read.
UPDATE acquisition_targets
   SET anchor_next_attempt_at = now()
 WHERE anchor_fingerprint IS NULL
   AND coalesce(entry_isrc, '') <> ''
   AND status IN ('unresolved', 'pending', 'searching', 'awaiting_review');

-- +goose Down
DROP INDEX IF EXISTS jobs_preview_anchor_sweep_idx;
DROP INDEX IF EXISTS acquisition_targets_anchor_due_idx;

ALTER TABLE acquisition_targets
    DROP COLUMN IF EXISTS anchor_fingerprint,
    DROP COLUMN IF EXISTS anchor_source,
    DROP COLUMN IF EXISTS anchor_reference,
    DROP COLUMN IF EXISTS anchor_seconds,
    DROP COLUMN IF EXISTS anchor_fetched_at,
    DROP COLUMN IF EXISTS anchor_unavailable,
    DROP COLUMN IF EXISTS anchor_attempts,
    DROP COLUMN IF EXISTS anchor_next_attempt_at;
