# 0006 — An acquisition target is the want, not the answer

**Status:** accepted, 2026-07-29

## Context

A playlist entry has to become a concrete MusicBrainz recording before
anything can be acquired (ADR 0001). The obvious reading is that the
recording *is* the target, and that a target therefore comes into existence
only once resolution has succeeded. Three things depend on the opposite
reading: resolution needs somewhere to put its answer, the retry loop needs
something to requeue, and the review queue's *wrong target* outcome
(ADR 0003) needs something that survives its own resolution being wrong.

## Decision

`acquisition_targets` holds one row per wanted recording, created **before**
resolution runs. The entry evidence — artist, title, album, duration, ISRC,
as whoever asked for it wrote it — is stored verbatim and never overwritten.
`musicbrainz_recording_id` is an answer written into the row, and can be
cleared and re-answered.

Seven states: `unresolved`, `pending`, `searching`, `awaiting_review`,
`acquired`, `not_wanted`, `superseded`.

**There is no failure state.** An exhausted search, an unreachable provider
and a rate-limited one all return the target to `pending` (or `unresolved`)
with a reason and a `next_attempt_at`. `next_attempt_at` lives on the target
rather than as a queued job's `run_after`: a want outlives any single
attempt, the `jobs` table is never purged, and stopping a target must be one
update rather than a hunt through the queue.

One live target per recording, enforced by a partial unique index. A second
entry resolving onto an existing target is `superseded` and points at the
survivor.

## Why

The entry is the durable fact; the recording ID is a conclusion about it.
Storing the conclusion as the identity makes a wrong conclusion unfixable
without losing the fact — which is precisely the failure ADR 0003's outcome
(4) exists to prevent.

Keeping the schedule on the row rather than in the queue means "not wanted"
is a single write, a cancelled want cannot leave a job behind that still
fires, and the retry cadence — the one acquisition parameter PRODUCT.md
deliberately leaves open — can be retuned without a migration.

The unique recording index is what makes re-importing a playlist idempotent
and what makes *not wanted* permanent: a target somebody stopped pursuing
keeps its recording, so the next import finds it rather than creating a
fresh want for music that was already refused.

## What breaks if reversed

If a target is created only once resolved, an entry that cannot be resolved
has nowhere to wait, and the review queue cannot offer *wrong target* at all
— the only correction for a bad resolution becomes manual database surgery.

If exhaustion is a failure state, Soulseek's intermittency reads as absence
and the collection quietly stops at whatever was online the day it was
asked for.

If the schedule lives in the job queue, every requeue is a row in a table
that is never purged, and every user decision has to reach into that queue
to take effect.
