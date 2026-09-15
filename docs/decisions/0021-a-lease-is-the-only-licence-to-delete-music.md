# 0021 — A lease is the only licence to delete music

**Status:** accepted, 2026-08-14. The owner answered the seven questions at the
end of this document on the same day and the feature was built against those
answers; `internal/migrations/00052_weekly_playlist.sql` is the migration, and
"As built" below records where the implementation departed from the draft and
why. The section on byte replacement was added 2026-08-30 to answer #524. This ADR
governs library files; 0036 governs the download inbox, where a file is a second
copy of something the library already holds or a copy nothing wanted.

## Context

Schall is a self-hosted music collection manager. Part 1 of roadmap item 9
shipped on 2026-08-11: a read-only list of suggested recordings, drawn from what
ListenBrainz says about the user's listening, with five suppression rules
applied when the list is read (ADR 0015, ADR 0016). Since 2026-08-12 a
suggestion can also be turned into a want, stamped with the origin
`recommendation`.

Part 2 is the weekly playlist. Once a week Schall takes the top suggestions,
obtains them the ordinary way, and puts a playlist in front of the user so the
music can actually be listened to. At the next refresh the songs the user liked
stay in the library and the rest are removed.

The second sentence is the whole feature. A playlist that regenerates every week
is ordinary. A playlist that **deletes music** is not, and `internal/library/
remove.go` says why: removal is the one thing Schall does that cannot be undone.
It deletes the audio from the disc and then the row, in that order.

So Part 2 puts an unattended weekly job in front of an irreversible operation.
CLAUDE.md's STOP list therefore gates it twice — new tables, and new writes to
decision stores — and #65 gates it once more: no code until this proposal is
approved.

Everything the writer needs already exists. Recommendations are stored and
suppressed at read time. An acquisition target is a want, drained by a loop that
admits a downloaded file only when its audio is identified as the wanted
recording at the leading fingerprint cluster, and holds a copy nobody could
identify rather than importing it (ADR 0011, ADR 0018). The importer files a
proven copy into the ordinary library tree. `internal/navidrome/playlists.go`
creates playlists. `internal/jobs` runs recurring self-rescheduling sweeps.

What does not exist is a way to say *this file is here on approval*, a way to
record what was read about it and when, and a way to stop the acquisition sweep
from fetching next week exactly what was removed this week.

### What is already decided

From #65, and not reopened here:

- The files are ordinary library files in the ordinary tree with the ordinary
  proven identity. There is no separate directory for them. A kept song must not
  move, and `Plan()` in `internal/library/moves.go` re-files every file inside
  its own root, so nothing could be consolidated across roots afterwards.
- What is special is the expiry, not the location.
- Deletion goes through `library.Remover` and nothing else. The remover chooses
  no file; something else decides and it carries the decision out.
- **Silence keeps.** Everywhere else in Schall an absent signal is silence and
  never agreement. Here that rule inverts, and it inverts correctly, because the
  failure being guarded against is destroying music. A refresh that cannot reach
  the player, cannot read a signal, or gets an answer it cannot interpret keeps
  everything it could not decide and says so.
- Keep decisions are taste and may feed recommendations. Review-queue decisions
  are statements about audio identity and may not (ADR 0005). However alike the
  two "no"s look, they do not share a store.

## Decision

### 1. The weekly playlist is a playlist, not a new kind of object

ADR 0008 states that a playlist is a want list with a source. The weekly
playlist is exactly a want list: a set of recordings the user should end up
owning, in an order, with a name. It becomes one row in `playlists` with the new
source `weekly`, its tracks become ordinary `playlist_entries`, and those entries
join to ordinary wants through `playlist_entry_targets`.

Nothing else would be honest. A second playlist table would mean a second import
path, a second Navidrome sync, and a second screen, all to hold a list the first
one already models. Reusing `playlists` also puts the weekly list where the user
already looks for playlists, and pushes it to Navidrome through the snapshot
machinery of ADR 0004 without a line of new sync code.

Two constraints move to admit it. `playlists_source_valid` gains `weekly`, the
way 00038 already added `navidrome`. `playlists_source_id_matches_source` says
today that every source except `manual` names a remote object; the weekly list
is a local list and names none, so the exception becomes the pair
`('manual', 'weekly')`. A partial unique index holds there to one weekly list,
because "the weekly playlist" is one object that is rewritten, not a new object
each week.

The list is rewritten in place at each refresh. `playlist_entries.source_track_id`
stays NULL for it, so the reconciliation index that makes a remote re-import
idempotent simply does not apply.

### 2. The lease is the only licence to delete

A **lease** is a row that says: this library file was obtained to fill the weekly
playlist, and until it earns a keep it may be deleted.

It is the only thing in Schall that makes a file eligible for automatic
deletion, and the only way to get one is to have been imported for a weekly run.
`library_file_leases.run_id` and `acquisition_target_id` are both NOT NULL for
that reason: the lease's authority to delete music comes from the run that
granted it and the want it filled, and neither may vanish underneath it. The
target reference is `ON DELETE RESTRICT` — 0006 supersedes targets rather than
deleting them, so nothing legitimate is blocked.

A lease is in exactly one of four states.

- **`held`** — granted, not yet judged.
- **`leaving`** — judged once. The lease reached its expiry, every named signal
  was read successfully, and none of them said keep. `removes_at` says when the
  file goes, and `leaving_run_id` names the run that decided it.
- **`kept`** — a keep signal was found, or the user pressed Keep. `released_at`
  and `kept_by` are set. From that moment the file is an ordinary library file.
  It moves nowhere; it already lives in the ordinary tree under its proven
  identity. `held` and `leaving` both reach it: announcing a departure is not
  performing one, and a song can be kept right up to the moment it goes.
- **`removed`** — the file was `leaving`, its date arrived, the signals were read
  a second time and still said nothing. `removed_at` is set and the audio is
  gone.

`leaving` is the grace week, and it is the shape #325 argues for: a refresh never
deletes a song on the same run that first found no keep signal. So **every
removal rests on two successful reads a week apart**, and the song stood named on
a screen, with its evidence and a Keep button, for the week between them. It also
answers the thing a single read cannot: a song fetched on Monday and judged the
following Monday may have had one evening in front of it.

Deletion is therefore reached only from `leaving`. No path takes a lease from
`held` straight to `removed`.

**The lease row outlives the file.** This is why `library_file_id` is nullable
with `ON DELETE SET NULL` rather than the obvious cascade, and why the path is
copied onto the lease when it is granted. A run has to be able to say afterwards
what it removed, and a row that vanishes with its file cannot say anything. The
partial unique index makes one file carry at most one live lease; a removed or
kept lease is history and does not block anything.

Extension is how silence keeps. When a refresh cannot decide a lease, it pushes
`expires_at` forward, increments `extensions`, and writes the reason. A lease can
be extended without limit. A lease that is extended forever costs one row and one
undeleted song; the alternative failure costs music.

### 3. The run is the account of one refresh

`weekly_playlist_runs` holds one row per refresh, from the moment it starts to
the moment it finishes, with counts of what it chose, what arrived, what it
kept, what it extended and what it removed. A partial unique index allows one
run at a time, in the shape the acquisition sweep already uses for its job.

The run carries a **`mode`**: `report` or `remove`. A run in `report` mode does
everything a removing run does except the removal — it reads the signals, writes
the evidence, and announces departures — and then lets the dates pass without
deleting anything.

The grace week already gives the user notice, so `remove` is proposed from the
first run and `report` is the switch to reach for if the first weeks want
watching. It costs one column, and the alternative to having it is a migration
written in a hurry.

A run may end `complete`, `partial` or `failed`. `partial` is the ordinary
outcome on a slow week: Soulseek is unreliable, searches that go too fast get the
account banned for thirty minutes, so the writer rides the existing search
budgets and is content arriving over hours. A candidate the loop could not prove
in time is simply absent from this week's list. The playlist ships partial rather
than late or unproven, and the run says which.

### 4. Every read is recorded, and unreadable is never absent

`library_file_keep_reads` records one row per signal per lease per run: which
signal was read, what it said, and when.

The `outcome` column has exactly three values, and the whole feature rests on the
third being distinct from the second:

- **`kept`** — the signal is present. This song stays.
- **`absent`** — the read succeeded and the signal is not there.
- **`unreadable`** — the read did not succeed, or its answer could not be
  interpreted.

Announcing a departure requires that the lease has expired, that every signal
Schall reads produced a row for this run, that at least one of them is `absent`,
and that none of them is `unreadable`. Performing it requires the same thing again,
on the run whose date has arrived. One unreadable signal extends the lease and
announces nothing; one unreadable signal on the second read sends a `leaving`
lease back to `held` with its date cleared, because a removal that cannot see
this week's answer is a removal decided last week alone.

An `unreadable` row must carry a detail saying what happened. A run report that
says "could not read" without saying why is a dead end the user can only press
retry on.

The user's own Keep is not a read at all, and the schema says so. A person
presses it on a Tuesday, when no refresh is running; the press clears the lease
there and then, so no later run ever sees that file as leased. Its row exists
only when it happened, always says `kept`, and names no run. Every other signal
is something Schall went and asked, and names the run that asked.

That invariant lives in the service, checked in one place with its own tests, in
the same way ADR 0016 keeps the impression constants out of the database. The
schema's job is to make sure the evidence for it is always on record.

### 5. What "liked" means — two signals, and each one only ever keeps

**This is the question #65 leaves to the owner. Four signals were proposed; the
owner chose two on 2026-08-14, and these two are what is built.**

Both signals hold at once because each one only ever adds a keep. A signal that
is switched off is not consulted and produces no row.

1. **A Navidrome star.** The user starred the song in the player — the heart in
   Feishin or Symfonium. This is the plainest possible statement of "I liked
   this", and Navidrome already holds the library. The client had no starred read
   before this feature; #323 built it, and it asks Subsonic about one song rather
   than for the whole starred list, because a song's absence from a list of
   everything starred cannot be told apart from a song Navidrome never had.
2. **A Keep in Schall.** A control on the weekly playlist screen, next to the
   song that is about to be removed. It is the only signal that cannot be
   unreadable, because nothing outside Schall has to answer for it, and it is the
   answer to "I never star anything".

Two more were proposed and left out. **A ListenBrainz love** was not taken up.
**A Navidrome play count above a threshold** was not either: it is a heuristic,
which is the shape matching refuses everywhere else, and the owner declined it
rather than tuning a number. The separate 1–5 Subsonic rating was raised and
declined for the same reason — the owner uses the star and not the rating.

Leaving them out is one line of schema each to add later, not a redesign: the
signal name is a value in one CHECK constraint.

**A star belongs to a Navidrome account, not to a server.** Schall signs in as
one account, and that is the account whose stars and play counts it can see. If
the owner listens under a different account, their stars are invisible, and an
invisible star is a successful read that found nothing — which is the one shape
that permits a deletion. No client method can close this, because from the wire
the two are the same answer.

So it is closed here, by saying it: **the account in the Navidrome settings is
the account the owner listens with.** The screen that stands in front of a
removal names which account was asked, so an owner who listens elsewhere sees the
wrong name beside a song they starred, rather than losing it.

**And when nothing external is configured at all**, the only signal left is the
Keep button. A week's deletions then rest on one control that nobody who has not
found it has pressed. This ADR proposes that a refresh with no external signal in
force **removes nothing**: it extends every lease and says which signals were
consulted. It is what "silence keeps" says on its face, and an owner who is happy
to run on the Keep button alone can see from the report exactly why nothing
happened.

### 6. Not chasing what was just removed

Delete an unkept song and the obvious next move by the acquisition sweep is to
fetch it again. Two stores stop that, and they are deliberately two, because they
say different things.

**The want is settled.** A removal writes its acquisition target to `not_wanted`
with a summary naming the run. This is a new transition: today
`StopPursuingAcquisitionTarget` accepts only a live target, and a leased file's
target is `acquired`. The transition `acquired → not_wanted` is created by this
ADR and is permitted from **one caller only**, the weekly refresh carrying out a
removal. It reads correctly: the recording was obtained, it was not kept, it is
not wanted now.

The transition costs one constraint change, and it is worth naming because the
alternative is a lie. `acquisition_targets_acquired_consistent` reads today as
"status is `acquired` exactly when `acquired_at` is set", so moving the target on
would force the removal to clear `acquired_at` and the want would then say it had
never arrived. The constraint splits into the two halves that were meant:
`acquired` still requires the stamp, and the stamp is now allowed to remain on a
`not_wanted` target. The song did arrive. The record should keep saying so.

Settling the want also holds the recording's place.
`acquisition_targets_recording_idx` allows one non-superseded target per
recording, so the `not_wanted` row occupies that slot and a later want for the
same recording has to find it rather than open a second one. That is the existing
mechanism doing exactly what this feature needs, not a new rule.

**The recording is suppressed for a window.** `recommendation_unkept` holds one
row per recording with its own expiry, and it becomes a **sixth suppression rule**
in the recommendation read query.

It is its own store because it is its own statement. Acquired, delivered,
listened past and not kept is stronger than an impression the user scrolled by —
the song was on their disc and they had a week — but it is weaker than a
dismissal, because the user never said "not interested". Writing it into
`recommendation_dismissals` would make a week of indifference permanent. Writing
it into `recommendation_impressions` would make it expire in the same ninety days
as never having pressed anything.

The proposed window is **180 days**, ordered between the two: twice an impression
suppression, and not forever. Like ADR 0016's constants it is a named value in
the service, not a database default, so retuning it needs no migration.

A recording that comes back after its window and is again not kept increments
`occurrences` and gets the window again from that day. Repeated indifference is
not the same as a dismissal and this ADR does not turn it into one; if a later
proposal wants an escalation, the counter is already there to base it on.

### 7. Where the ADR 0005 line falls

`recommendation_unkept` is a taste store, written by the weekly refresh, read by
the recommendation list.

No query in this feature reads `acquisition_target_files`,
`acquisition_target_rejected_recordings`, `download_import_decisions`,
`library_file_identities` as a decision, or any other review store, and nothing
in this feature writes to them. The implementation must carry the same shape of
regression test Part 1 carries: a review-queue rejection is recorded and the
weekly run behaves identically.

The one crossing that does happen is in the safe direction. A file arrives here
only if the acquisition loop proved it acoustically, which is the review path
admitting a file, not this feature reading a review decision.

### Replacing a file's bytes is not deleting music

The shrink-the-library sweep does not delete music. It replaces the bytes of
one library file with a smaller encoding of the same audio, on the same row,
keeping the same file id, the same recording, and the same place in the tree.
The library does not hold one recording fewer afterwards. Everything that names
the file by id — a mapping, a playlist entry, a lease — still names it.

This ADR governs removing music: after it, a recording the user had is gone.
That is the operation that cannot be undone, and the one a licence is for. The
weekly sweep removes recordings, so it holds a lease and revalidates it
immediately before the unlink. The duplicate sweep removes a surplus copy while
a better copy of the same recording stays. The transcode sweep removes source
bytes only after a verified encoding of the same audio already sits on the same
row.

Three conditions make that true. They are conditions, not observations, and the
code holds each one:

1. The transcoding setting is on. That setting is the person's decision,
   standing across the library. Turning it off stops the sweep at the top of
   `Sweep` before it opens a file.
2. The smaller copy is on disc and its length is checked before anything else
   changes. `internal/transcode/service.go` refuses an encoding whose length
   differs from the source by more than the tolerance, and the sweep refuses to
   file a copy at its destination whose length differs from the length already
   recorded for the original.
3. The library row points at the smaller copy, committed, before the unlink
   runs. A crash between the two leaves a removal row with `unlinked_at` empty
   and the original still on disc, which the retry reads.

Remove any one of the three and this stops being a replacement. The sweep would
then be deleting an original with nothing proven standing in its place, and it
would need a lease like the weekly one.

The row the sweep writes to `library_file_removals` is a record of which bytes
are still to be deleted, not a licence. `RetryUnlinks` reads it, and it only
ever holds paths a committed replacement or a licensed removal already left
behind.

## Draft DDL

**This is the draft as it was proposed. The migration that was written from it
is `internal/migrations/00052_weekly_playlist.sql`, and it differs in the ways
"As built" lists below. Where the two disagree, the migration is what runs.**

Both halves were applied and rolled back against a throwaway copy of the current
schema, and each guard below was checked by trying the thing it forbids: a second
weekly playlist, a weekly playlist naming a remote object, a second running run,
an unexplained partial run, a second live lease on one file, a keep that does not
name its signal, an unreadable read with no reason, and two answers from one
signal in one run. Each was refused. A person deleting a leased file by hand was
not.

```sql
-- +goose Up
-- A playlist is a want list with a source (0008). The weekly list is a local
-- list: unlike a Spotify or Navidrome list it names no remote object, so it
-- joins 'manual' on the source_id exception.
ALTER TABLE playlists
    DROP CONSTRAINT playlists_source_valid,
    ADD CONSTRAINT playlists_source_valid
        CHECK (source IN ('spotify', 'manual', 'navidrome', 'weekly')),
    DROP CONSTRAINT playlists_source_id_matches_source,
    ADD CONSTRAINT playlists_source_id_matches_source
        CHECK ((source IN ('manual', 'weekly')) = (source_id IS NULL));

-- A want that was obtained and then removed keeps the stamp saying it arrived.
-- The old constraint was a biconditional, so moving an acquired target on would
-- have forced the removal to erase the fact that the song was ever here.
ALTER TABLE acquisition_targets
    DROP CONSTRAINT acquisition_targets_acquired_consistent,
    ADD CONSTRAINT acquisition_targets_acquired_stamped
        CHECK (status <> 'acquired' OR acquired_at IS NOT NULL),
    ADD CONSTRAINT acquisition_targets_acquired_stamp_kept
        CHECK (acquired_at IS NULL
               OR status IN ('acquired', 'not_wanted', 'superseded'));

-- "The weekly playlist" is one object that is rewritten every week, not a new
-- object each week. The runs below are what carry the history.
CREATE UNIQUE INDEX playlists_weekly_singleton_idx
    ON playlists ((source = 'weekly'))
    WHERE source = 'weekly';

-- One refresh, from the moment it starts to the account it leaves behind. mode
-- is what lets the first weeks report what they would have removed without a
-- second migration; status 'partial' is the ordinary outcome on a week when
-- Soulseek was slow, not a failure.
CREATE TABLE weekly_playlist_runs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    playlist_id UUID REFERENCES playlists(id) ON DELETE SET NULL,
    mode TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'running',
    detail TEXT NOT NULL DEFAULT '',
    chosen_count INTEGER NOT NULL DEFAULT 0,
    wanted_count INTEGER NOT NULL DEFAULT 0,
    arrived_count INTEGER NOT NULL DEFAULT 0,
    kept_count INTEGER NOT NULL DEFAULT 0,
    extended_count INTEGER NOT NULL DEFAULT 0,
    removed_count INTEGER NOT NULL DEFAULT 0,
    started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at TIMESTAMPTZ,
    CONSTRAINT weekly_playlist_runs_mode_valid
        CHECK (mode IN ('report', 'remove')),
    CONSTRAINT weekly_playlist_runs_status_valid
        CHECK (status IN ('running', 'complete', 'partial', 'failed')),
    CONSTRAINT weekly_playlist_runs_partial_explained
        CHECK (status NOT IN ('partial', 'failed') OR btrim(detail) <> ''),
    CONSTRAINT weekly_playlist_runs_finished_when_settled
        CHECK ((status = 'running') = (finished_at IS NULL)),
    CONSTRAINT weekly_playlist_runs_counts_nonnegative
        CHECK (chosen_count >= 0 AND wanted_count >= 0 AND arrived_count >= 0
               AND kept_count >= 0 AND extended_count >= 0
               AND removed_count >= 0)
);

-- One run at a time, in the shape the acquisition sweep's job index already
-- uses.
CREATE UNIQUE INDEX weekly_playlist_runs_live_idx
    ON weekly_playlist_runs ((status = 'running'))
    WHERE status = 'running';

CREATE INDEX weekly_playlist_runs_recent_idx
    ON weekly_playlist_runs (started_at DESC);

-- The lease: this file was obtained to fill the weekly playlist and may be
-- deleted until it earns a keep. It is the only thing in Schall that makes a
-- file eligible for automatic deletion, and run_id and acquisition_target_id
-- are NOT NULL because that authority comes from the run that granted it and
-- the want it filled.
--
-- The row outlives the file on purpose. library_file_id is set to NULL when the
-- audio goes, and library_path is copied here when the lease is granted, so a
-- run can always say afterwards what it removed.
CREATE TABLE library_file_leases (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    library_file_id UUID REFERENCES library_files(id) ON DELETE SET NULL,
    library_path TEXT NOT NULL,
    musicbrainz_recording_id UUID NOT NULL,
    run_id UUID NOT NULL
        REFERENCES weekly_playlist_runs(id) ON DELETE RESTRICT,
    acquisition_target_id UUID NOT NULL
        REFERENCES acquisition_targets(id) ON DELETE RESTRICT,
    state TEXT NOT NULL DEFAULT 'held',
    granted_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL,
    extensions INTEGER NOT NULL DEFAULT 0,
    extended_reason TEXT NOT NULL DEFAULT '',
    -- The grace week. A run that finds no keep sets these and deletes nothing;
    -- the run whose date has arrived reads again and carries it out.
    removes_at TIMESTAMPTZ,
    leaving_run_id UUID REFERENCES weekly_playlist_runs(id) ON DELETE SET NULL,
    -- Which signal kept it, and the run that settled it.
    kept_by TEXT,
    released_at TIMESTAMPTZ,
    removed_at TIMESTAMPTZ,
    settled_run_id UUID REFERENCES weekly_playlist_runs(id) ON DELETE SET NULL,
    CONSTRAINT library_file_leases_state_valid
        CHECK (state IN ('held', 'leaving', 'kept', 'removed')),
    CONSTRAINT library_file_leases_path_present
        CHECK (btrim(library_path) <> ''),
    CONSTRAINT library_file_leases_expiry_after_grant
        CHECK (expires_at > granted_at),
    CONSTRAINT library_file_leases_extensions_nonnegative
        CHECK (extensions >= 0),
    -- A leaving lease says when and who decided it. A lease that went back to
    -- held because the second read failed clears both again.
    CONSTRAINT library_file_leases_leaving_dated
        CHECK (state <> 'leaving'
               OR (removes_at IS NOT NULL AND leaving_run_id IS NOT NULL)),
    CONSTRAINT library_file_leases_departure_only_after_notice
        CHECK (removes_at IS NULL
               OR state IN ('leaving', 'kept', 'removed')),
    -- Nothing is removed without having been announced first.
    CONSTRAINT library_file_leases_removal_announced
        CHECK (state <> 'removed' OR removes_at IS NOT NULL),
    -- A kept lease names the signal that kept it and when; nothing else does.
    CONSTRAINT library_file_leases_kept_evidenced
        CHECK ((state = 'kept')
               = (released_at IS NOT NULL AND kept_by IS NOT NULL
                  AND btrim(kept_by) <> '')),
    CONSTRAINT library_file_leases_removed_stamped
        CHECK ((state = 'removed') = (removed_at IS NOT NULL)),
    -- Deliberately no constraint requiring a live lease to point at a file. A
    -- person may delete a leased file by hand from the duplicates screen, and a
    -- constraint here would refuse their delete. A held lease whose file has
    -- gone is settled by the next refresh as removed-by-someone-else, with
    -- nothing to delete and no want to settle.
    -- A run may only be named on a settled lease, and a removal always has one.
    -- A keep names none when the user pressed Keep on a Tuesday.
    CONSTRAINT library_file_leases_settled_run_scoped
        CHECK (settled_run_id IS NULL OR state IN ('kept', 'removed')),
    CONSTRAINT library_file_leases_removal_has_a_run
        CHECK (state <> 'removed' OR settled_run_id IS NOT NULL)
);

-- One file carries at most one live lease. A kept or removed lease is history
-- and blocks nothing.
CREATE UNIQUE INDEX library_file_leases_live_idx
    ON library_file_leases (library_file_id)
    WHERE state IN ('held', 'leaving') AND library_file_id IS NOT NULL;

-- What a refresh asks for twice: which leases are due to be judged, and which
-- announced departures have arrived.
CREATE INDEX library_file_leases_due_idx
    ON library_file_leases (expires_at)
    WHERE state = 'held';

CREATE INDEX library_file_leases_leaving_idx
    ON library_file_leases (removes_at)
    WHERE state = 'leaving';

CREATE INDEX library_file_leases_run_idx ON library_file_leases (run_id);

-- What was read about a leased file, what it said, and when. The three
-- outcomes are the point: 'absent' may allow a removal and 'unreadable' never
-- may, so a client that collapses them is the one bug this feature must not
-- have. observed_value carries the play count, which is recorded from the
-- first run whether or not a threshold makes it decide anything.
CREATE TABLE library_file_keep_reads (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    lease_id UUID NOT NULL
        REFERENCES library_file_leases(id) ON DELETE CASCADE,
    -- Every signal Schall reads is read by a run. The user's own Keep is not
    -- read at all: they press it on a Tuesday, and it names no run.
    run_id UUID REFERENCES weekly_playlist_runs(id) ON DELETE CASCADE,
    signal TEXT NOT NULL,
    outcome TEXT NOT NULL,
    observed_value INTEGER,
    detail TEXT NOT NULL DEFAULT '',
    read_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT library_file_keep_reads_signal_valid
        CHECK (signal IN ('navidrome_star', 'navidrome_play_count',
                          'listenbrainz_love', 'schall_keep')),
    CONSTRAINT library_file_keep_reads_outcome_valid
        CHECK (outcome IN ('kept', 'absent', 'unreadable')),
    -- A read that failed says why. A run report that says "could not read"
    -- without saying what happened is something a person can only press retry
    -- on.
    CONSTRAINT library_file_keep_reads_failure_explained
        CHECK (outcome <> 'unreadable' OR btrim(detail) <> ''),
    CONSTRAINT library_file_keep_reads_value_nonnegative
        CHECK (observed_value IS NULL OR observed_value >= 0),
    -- A pressed Keep is not a read. It exists only when it happened, so it is
    -- always a keep, and it names no run because a person presses it on a
    -- Tuesday. Every other signal is read by a run and names it.
    CONSTRAINT library_file_keep_reads_pressed_keep_is_not_a_read
        CHECK ((signal = 'schall_keep')
               = (run_id IS NULL AND outcome = 'kept')),
    -- One answer per signal per lease per run.
    UNIQUE (lease_id, run_id, signal)
);

CREATE INDEX library_file_keep_reads_run_idx
    ON library_file_keep_reads (run_id);

-- Acquired, delivered, listened past and not kept. Stronger than an ignored
-- impression, weaker than a dismissal, and its own store because it is its own
-- statement: the user never said "not interested". The window is a named value
-- in the service, not a default here, so retuning it needs no migration.
CREATE TABLE recommendation_unkept (
    musicbrainz_recording_id UUID PRIMARY KEY,
    unkept_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    suppressed_until TIMESTAMPTZ NOT NULL,
    occurrences INTEGER NOT NULL DEFAULT 1,
    run_id UUID REFERENCES weekly_playlist_runs(id) ON DELETE SET NULL,
    CONSTRAINT recommendation_unkept_window_ordered
        CHECK (suppressed_until > unkept_at),
    CONSTRAINT recommendation_unkept_occurrences_positive
        CHECK (occurrences >= 1)
);

-- +goose Down
DROP TABLE IF EXISTS recommendation_unkept;
DROP TABLE IF EXISTS library_file_keep_reads;
DROP TABLE IF EXISTS library_file_leases;
DROP INDEX IF EXISTS weekly_playlist_runs_recent_idx;
DROP INDEX IF EXISTS weekly_playlist_runs_live_idx;
DROP TABLE IF EXISTS weekly_playlist_runs;
DROP INDEX IF EXISTS playlists_weekly_singleton_idx;
DELETE FROM playlists WHERE source = 'weekly';
UPDATE acquisition_targets SET acquired_at = NULL WHERE status <> 'acquired';
ALTER TABLE acquisition_targets
    DROP CONSTRAINT acquisition_targets_acquired_stamp_kept,
    DROP CONSTRAINT acquisition_targets_acquired_stamped,
    ADD CONSTRAINT acquisition_targets_acquired_consistent
        CHECK ((status = 'acquired') = (acquired_at IS NOT NULL));
ALTER TABLE playlists
    DROP CONSTRAINT playlists_source_id_matches_source,
    ADD CONSTRAINT playlists_source_id_matches_source
        CHECK ((source = 'manual') = (source_id IS NULL)),
    DROP CONSTRAINT playlists_source_valid,
    ADD CONSTRAINT playlists_source_valid
        CHECK (source IN ('spotify', 'manual', 'navidrome'));
```

## What the owner answered

Answered on 2026-08-14, before any of this was built.

1. **The signals.** Two of the four: the Navidrome star and the Keep button.
   Section 5 above is rewritten to that answer, and #323 built exactly the star
   read and nothing else.
2. **The play-count threshold.** Not applicable — the play count was not taken
   up, so no count is read and none is recorded.
3. **The unkept window.** 180 days, as proposed.
4. **Which mode the first run ships in.** `remove`, as proposed: the grace week
   is the notice, and `report` on top of it means the first weeks delete nothing
   at all. It is one setting either way, and `report` is one press away on the
   settings screen.
5. **How long the grace week is.** One week, as proposed.
6. **Whether a person may bring a removal forward.** Not built. `Remove now` was
   named in the screen proposal and is not in this implementation: it is the one
   part of the feature that would delete a file the same second it was pressed,
   and nothing needs it. A person who wants the space back today can delete the
   file the ordinary way, on the library screen.
7. **How many songs a week.** Twenty, and the setting accepts 1 to 50.

## As built

The implementation departs from the draft above in six places. Each one was
found by building it, and each is in the direction of keeping music.

1. **A fifth lease state, `gone`.** The draft had four, and none of them fitted
   the case where a person deletes a leased file by hand: `removed` would claim
   Schall deleted it, settle the want, and suppress the recording, none of which
   is true. `gone` says the file left by another hand — nothing was deleted here
   and nothing was decided — and it is the only settled state that writes neither
   a `not_wanted` want nor an unkept row.

2. **A settings table, `weekly_playlist_settings`.** The draft put the mode on
   the run and the week's size in the service. A feature that deletes music needs
   a switch a person can reach without a deploy, so `enabled`, `songs_per_week`
   and `mode` are one singleton row in the shape the slskd and Navidrome settings
   already use. It ships with `enabled` false: a deleter starts when somebody
   turns it on, not when a container restarts.

3. **A lease is granted by a refresh, not at import.** The draft said "granted at
   import for a run". Doing it at import would mean the download path knowing
   about this feature, which is on the STOP list and is also the wrong shape: it
   would make a file deletable because of the way it arrived. Instead each refresh
   looks for its own playlist's wants that have since been obtained and puts them
   on trial. A song that arrives on Monday starts its week at the next refresh,
   which is later and more generous, and the import path still knows nothing.

4. **One more constraint on the keep reads.** The draft's biconditional said a
   pressed Keep names no run — but it let a star read name no run either, as long
   as its outcome was not `kept`. That would have written evidence no run report
   could show. `library_file_keep_reads_read_names_its_run` closes it. It was
   found by probing the draft against a throwaway database, not by reading it.

5. **`observed_value` is gone from the keep reads.** It existed to carry the play
   count, and the play count was not taken up. A column nothing writes is a
   column that misleads whoever reads the schema next.

6. **`leaving_count` on the run.** The draft counted kept, extended and removed.
   A refresh in `report` mode does none of those three and still has something to
   say: this is how many songs it would have announced.

## Why

Every table here exists because one fact in this feature has a lifetime no
existing table has.

A run is a bounded event with an account: it starts, does what a slow week
allowed, and finishes. A lease belongs to a file and crosses runs; it is granted
by one and settled by another, sometimes several later. A keep read belongs to
one signal in one run and is evidence, never state. An unkept suppression
belongs to a recording, outlives every file and every run, and has its own clock.

Folding any two together loses something specific. Leases on the playlist entry
would die when the weekly list is rewritten, taking the licence to delete with
them and, worse, taking the record of what was deleted. Keep reads folded into
the lease would keep only the last answer, so a removal could not show the three
weeks of successful reads behind it. The unkept suppression folded into the
dismissal store would turn one indifferent week into a permanent refusal.

The lease outliving its file is the same reasoning ADR 0009 applies to a refused
copy: what a system did to somebody's music is a fact worth more than the object
it was done to.

## What breaks if reversed

If the lease cascades with its file, a removal erases its own evidence and the
run report can only say a number.

If a file can be leased any other way — by being in the weekly playlist, by
matching a recommended recording, by any rule that looks at the file rather than
at how it arrived — then music the user obtained deliberately becomes eligible
for automatic deletion. This is the failure that matters most, and NOT NULL on
`run_id` and `acquisition_target_id` is the whole defence.

If `unreadable` and `absent` share a value, then Navidrome being down for an
hour on a Sunday reads as "the user kept nothing this week" and the refresh
deletes the lot. This is the reason the read outcome is three-valued in the
schema and not two.

If the removal does not settle the want, the acquisition sweep re-downloads next
week what was removed this week, forever, and the feature fights itself. If it
does not write the unkept suppression, the recommender offers the same recording
back and the writer buys it again.

If the unkept store is `recommendation_dismissals`, a week of not listening
becomes a permanent "never suggest this", and the user never said that.

## Consequences

- The implementation migration alters `playlists` twice, adds one index to it,
  and creates four tables representing four stores.
- The recommendation read query grows a sixth rule. docs/PRODUCT.md names five
  today and must gain the sixth in the same change, because the list's own
  hidden-counts panel names each rule to the reader.
- `acquisition_targets` gains one new transition, `acquired → not_wanted`, with
  exactly one caller. Its store function is separate from
  `StopPursuingAcquisitionTarget` so that the transition cannot be reached by a
  person pressing "Stop looking" on a screen.
- The weekly playlist appears on the Playlists screen beside the imported ones,
  and syncs to Navidrome through the existing snapshot path. Both need to know
  that a `weekly` list is not re-imported from anywhere.
- `internal/seed/fixture.sql` must stay still under this feature. A seeded lease
  with an expiry in the past would give the browser tests a run with work to do,
  which is the failure mode CLAUDE.md warns about. Any seeded lease is `kept`,
  or there is none.
- The implementation must test: a lease granted only at import for a run; a keep
  by each signal; a Keep pressed between runs clearing the lease with no run
  named; an unreadable read extending instead of announcing; a partly unreadable
  set of signals extending; an unreadable second read sending a `leaving` lease
  back to `held` with its date cleared; a keep pressed on a `leaving` lease
  stopping the removal; a removal writing the want to `not_wanted` and the
  recording to `recommendation_unkept`; the sixth suppression hiding the
  recording for its window and showing it after; a `report` run announcing and
  deleting nothing; two runs, where the second finds the first's open wants
  instead of duplicating them; and the review-queue non-interference test Part 1
  already carries.
- Nothing in this ADR authorises a removal that is not in front of the user.
  #325 decides what that looks like, and the refresh does not perform its first
  removal until it exists.
