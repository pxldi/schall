# 0016 — Recommendation signals and candidates have their own lifetimes

**Status:** accepted, 2026-08-10 — approved by the owner on CAN-30. This
document is draft DDL, not a migration.

## Context

Roadmap 9 needs three kinds of persisted state for its read-only first part:

- an explicit dismissal, permanent at recording, release-group or artist level;
- repeated impressions, which are weak evidence and suppress one recording for
  90 days only after the third ignored showing; and
- fetched recommendation candidates, carrying MusicBrainz identifiers and the
  reason each candidate was proposed.

These facts do not have the same identity or lifetime. A dismissal is a durable
decision. Impression fatigue is a recording-level counter with a temporary
effect. A fetched candidate is a replaceable source snapshot. Putting them in
one table would make either the durable facts expire with a fetch or the fetched
answers accumulate as history.

ADR 0005 already fixes another boundary: review-queue decisions are statements
about file identity, not taste. In particular,
`acquisition_targets.status = 'not_wanted'` is the terminal state of a want. It
stops that target and prevents the follow feed from recreating it, but it is not
a recommendation dismissal.

The parallel source proposal, CAN-29, reserves ADR 0015 and proposes a
ListenBrainz sweep whose candidates are expanded through MusicBrainz before
storage. The schema here does not select or implement that client. It relies on
the project-wide rule that every recommendation identifier is a MusicBrainz ID.

## Decision

### 1. Dismissals are one level-tagged permanent fact

`recommendation_dismissals` has the natural key `(subject_type,
musicbrainz_id)`. The UUID is deliberately not a foreign key into the local
catalogue: an external recommendation may be dismissed without importing its
artist or release into Schall.

The level is part of the key, so dismissing a recording does not silently become
a release or artist decision. There is no expiry column. Repeating the same
dismissal is an idempotent upsert of the same fact.

At list read, the suppression query performs three exact probes against the
primary key: recording, release group and artist. This avoids an `OR` join and
makes every level-scoped lookup use the same small B-tree.

### 2. Impressions are one state row per recording, not an event log

`recommendation_impressions` has one row per recording MBID. It keeps the number
of ignored eligible showings in the current cycle, when the recording was last
shown, and the end of any active suppression.

On the third eligible showing, one atomic update sets `suppressed_until` to 90
days after that showing. The third showing is visible; subsequent reads suppress
the recording while `suppressed_until > now()`. When it becomes eligible again,
the next counted showing starts a new cycle at one **and clears the expired
`suppressed_until` in the same update**. A candidate arriving from another source
continues the same cycle because fatigue belongs to the recording, not to the
provider's row.

A list read is not automatically a new showing. Re-fetches, query invalidations
and reloads may ask for the same list many times while the user is still looking
at it, so `last_shown_at` gates the write: one recording counts at most once per
24-hour interval. A read inside that interval leaves all three columns unchanged.
The service records a counted impression only for an eligible candidate actually
returned in a successful response.

An append-only row for every render would keep detail no behaviour needs and
would need a second rule to decide which old rows count after a 90-day window.
The state row records the answer the read path needs and stays bounded.

The constants live as named values in the recommendation service, not in a
database trigger or default:

- ignored-impression threshold: **3**;
- suppression duration: **90 days**.
- minimum interval between counted showings: **24 hours**.

They are passed into the atomic update, along with one `observed_at` value used
for every comparison and assignment in that statement. The schema only requires
a non-negative counter, so changing policy does not require a migration. Existing
`suppressed_until` values remain the decisions made under the policy in force at
the time; a policy change is not retroactive unless a later proposal says so.

Part 1 has no persisted positive feedback action. An explicit dismissal writes
the dismissal store and permanently wins over the impression row; an active
acquisition target or a new follow is suppressed by its own read-time rule.
Review-queue actions never write or reset this row.

### 3. Candidates are the current snapshot, not a recommendation history

The candidate store is two tables. `recommendation_snapshots` records the one
current usable sweep per source, including source provenance and whether the
sweep was complete or deliberately partial. `recommendation_candidates` holds
that snapshot's rows.

Before writing, the service deduplicates by recording MBID within the source,
deduplicates credited artist MBIDs, merges distinct reason codes and their
context, keeps the strongest source score, and assigns a deterministic dense
rank from 1. This makes the two candidate uniqueness constraints assertions
about a prepared snapshot rather than failure modes discovered halfway through
its write.

A complete or partial sweep replaces one source atomically: upsert the snapshot
with `INSERT ... ON CONFLICT (source) DO UPDATE`, delete that source's old
candidate rows, and insert the prepared rows in one transaction. The upsert is
what makes the first sweep and every later sweep the same operation. A failed
sweep does none of those things, so the prior snapshot remains available and its
`fetched_at` tells the UI exactly how stale it is. A successful empty sweep is
different: it upserts the snapshot and replaces its candidates with zero rows.

Candidates therefore have no guessed clock TTL. They expire when the next
usable sweep for their source replaces them. Only the current snapshot is kept;
there is no inactive-candidate history to prune. This bounds storage by the
number of sources and their current result sets while preserving the last known
answer through an outage.

Every candidate carries a recording MBID, a release-group MBID, and **all
MusicBrainz artist MBIDs in the recording's credit**. Release group is required
even though the issue's minimum input names recording and artist: without it, a
release-group dismissal and the release-owned half of rule 1 cannot be evaluated.
The artist list is an array rather than a fifth table because the approved store
has four tables and candidate credits are replaced with their parent snapshot;
they have no identity or lifecycle of their own. A candidate that cannot be
expanded to a recording, release group and at least one credited artist MBID is
not stored because it cannot pass all five suppressions.

Reason codes follow Aurral's useful convention: stable machine-readable codes,
with structured context beside them rather than user-facing prose in storage.
Aurral v2 can attach several codes to one candidate, so the column is a non-empty
`TEXT[]`, not a lossy single value. The database rejects NULL and empty-string
elements; the service trims and validates codes before the write. The initial
service vocabulary may include
`tag_affinity`, `tag_affinity_strong`, `seed_consensus`, `multi_seed`,
`two_hop`, `history_aligned`, `deeper_pick`, `mainstream_overlap`,
`mode_deeper` and `mode_safer`; source-specific evidence such as `cf_raw` or
`similar_to` uses the same shape. A code is emitted only when its evidence
exists. `reason_context` carries identifiers such as a model ID or seed
recording MBID. The database requires at least one code but deliberately does
not freeze an application vocabulary into a migration.

Names are cached only for rendering. Suppression never compares a title or
artist string; it uses the MBIDs.

### 4. All five suppressions are evaluated when the list is read

Fetching candidates does not remove, reject or annotate them based on current
local state. The list query applies all five rules to the current candidate
snapshot:

1. **Owned:** use the existing decided recording evidence — a present mapped
   file, or a present `library_file_identities` row — plus the equivalent
   release-group ownership check. Raw file tags do not count.
2. **Requested:** suppress only active acquisition targets: `unresolved`,
   `pending`, `searching` and `awaiting_review`. An unresolved target has no
   recording MBID today, but keeping the positive state list states the policy
   and avoids silently changing it when a future state is added.
3. **Followed artist:** unnest the candidate's artist-credit MBIDs and suppress
   when **any** matches `artists.musicbrainz_id` with
   `artists.followed_at IS NOT NULL`. A followed guest credit is still a
   followed artist; weakening this to one selected "primary" credit makes the
   rule depend on an attribution choice PRODUCT.md does not make.
4. **Dismissed:** perform the three exact level probes described above.
5. **Impression fatigue:** suppress when the recording's
   `suppressed_until > now()`.

Read-time evaluation means a new follow or dismissal takes effect immediately,
a 90-day suppression ends without a fetch, and a request that becomes active is
hidden without rewriting provider data.

`acquisition_targets.status = 'not_wanted'` participates in none of these five
rules. It is not active, so it is not rule 2; ADR 0005 and the review-queue model
make it a want-lifecycle fact rather than rule 4 taste evidence. `acquired` is
handled by ownership, and `superseded` is not a live want. If a recommendation
must never be shown again, the user makes that separate statement in
`recommendation_dismissals`.

No query in this path reads `acquisition_target_files`,
`acquisition_target_rejected_recordings`, `download_import_decisions`, or any
other review store. The implementation issue must include a regression test in
which a review rejection is added and the recommendation result is unchanged.

## Draft DDL

This is the approved schema contract to turn into the then-next migration. It is
not itself a migration and deliberately allocates no migration number. The fence
does not implement the atomic impression and snapshot writes described above;
the implementation issue must add those queries and their lifecycle tests rather
than copying these `CREATE TABLE` statements and calling the store complete.

```sql
-- +goose Up
-- A dismissal is a permanent recommendation decision at exactly the level the
-- user chose. The MBID deliberately has no catalogue foreign key: an external
-- recommendation can be dismissed without importing it into the catalogue.
CREATE TABLE recommendation_dismissals (
    subject_type TEXT NOT NULL,
    musicbrainz_id UUID NOT NULL,
    dismissed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (subject_type, musicbrainz_id),
    CONSTRAINT recommendation_dismissals_subject_valid
        CHECK (subject_type IN ('recording', 'release_group', 'artist'))
);

-- Impression fatigue is bounded state, not a render log. Policy constants and
-- the atomic transition live in the recommendation service so retuning them does
-- not require a migration.
CREATE TABLE recommendation_impressions (
    musicbrainz_recording_id UUID PRIMARY KEY,
    ignored_impressions INTEGER NOT NULL DEFAULT 0,
    last_shown_at TIMESTAMPTZ NOT NULL,
    suppressed_until TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT recommendation_impressions_count_nonnegative
        CHECK (ignored_impressions >= 0),
    CONSTRAINT recommendation_impressions_window_ordered
        CHECK (suppressed_until IS NULL OR suppressed_until > last_shown_at)
);

-- One row is the current usable answer from one source. A failed fetch does not
-- touch it; a complete or explained-partial fetch upserts it in the same
-- transaction that replaces its candidates.
CREATE TABLE recommendation_snapshots (
    source TEXT PRIMARY KEY,
    source_snapshot_id TEXT,
    source_snapshot_at TIMESTAMPTZ,
    status TEXT NOT NULL,
    detail TEXT NOT NULL DEFAULT '',
    fetched_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT recommendation_snapshots_source_present
        CHECK (btrim(source) <> ''),
    CONSTRAINT recommendation_snapshots_status_valid
        CHECK (status IN ('complete', 'partial')),
    CONSTRAINT recommendation_snapshots_partial_explained
        CHECK (status <> 'partial' OR btrim(detail) <> ''),
    CONSTRAINT recommendation_snapshots_source_id_present
        CHECK (source_snapshot_id IS NULL OR btrim(source_snapshot_id) <> '')
);

-- Candidates are provider answers, not local catalogue entities. Every
-- suppression comparison uses the expanded MusicBrainz IDs; display names never
-- decide eligibility. Artist credits stay inline because they share the
-- candidate's replacement lifecycle and have no independent identity.
CREATE TABLE recommendation_candidates (
    source TEXT NOT NULL
        REFERENCES recommendation_snapshots(source) ON DELETE CASCADE,
    musicbrainz_recording_id UUID NOT NULL,
    musicbrainz_release_group_id UUID NOT NULL,
    musicbrainz_artist_ids UUID[] NOT NULL,
    recording_title TEXT NOT NULL,
    release_title TEXT NOT NULL DEFAULT '',
    artist_name TEXT NOT NULL,
    rank INTEGER NOT NULL,
    source_score DOUBLE PRECISION,
    reason_codes TEXT[] NOT NULL,
    reason_context JSONB NOT NULL DEFAULT '{}'::jsonb,
    fetched_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (source, musicbrainz_recording_id),
    UNIQUE (source, rank),
    CONSTRAINT recommendation_candidates_recording_title_present
        CHECK (btrim(recording_title) <> ''),
    CONSTRAINT recommendation_candidates_artist_name_present
        CHECK (btrim(artist_name) <> ''),
    CONSTRAINT recommendation_candidates_rank_positive CHECK (rank > 0),
    CONSTRAINT recommendation_candidates_artists_present
        CHECK (cardinality(musicbrainz_artist_ids) > 0),
    CONSTRAINT recommendation_candidates_artists_identified
        CHECK (array_position(musicbrainz_artist_ids, NULL) IS NULL),
    CONSTRAINT recommendation_candidates_reasons_present
        CHECK (cardinality(reason_codes) > 0),
    CONSTRAINT recommendation_candidates_reasons_identified
        CHECK (array_position(reason_codes, NULL) IS NULL
               AND array_position(reason_codes, '') IS NULL),
    CONSTRAINT recommendation_candidates_reason_context_object
        CHECK (jsonb_typeof(reason_context) = 'object')
);

-- +goose Down
DROP TABLE IF EXISTS recommendation_candidates;
DROP TABLE IF EXISTS recommendation_snapshots;
DROP TABLE IF EXISTS recommendation_impressions;
DROP TABLE IF EXISTS recommendation_dismissals;
```

## Why

The split follows the meaning of the data rather than the screen that happens
to use it. Permanent user decisions cannot live on replaceable provider rows.
Weak implicit evidence cannot become permanent merely because both are called a
suppression. Provider answers cannot be discarded during fetch based on local
state that may change a moment later.

Natural MusicBrainz keys keep all three stores independent of catalogue
ingestion and make the suppression probes exact. A current-snapshot model keeps
outages honest without turning recommendations into an audit log nobody reads.

## What breaks if reversed

If suppression happens during fetch, a follow, dismissal or request made after
the sweep leaks until the next one, and a 90-day suppression cannot end without
provider traffic. If candidate rows carry dismissals, the next replacement
erases a permanent choice. If impressions are append-only without a cycle
boundary, the first three showings suppress the recording again immediately
after every 90-day window forever.

If `not_wanted` is reused as a dismissal, a review-queue action crosses ADR
0005's boundary and silently becomes taste. If review rejection tables are read
directly, difficult-to-acquire music is suppressed in proportion to how many
wrong files were offered for it — the exact failure ADR 0005 forbids.

## Consequences

- The implementation migration creates four tables representing three stores;
  candidate snapshot metadata and candidate rows are one store with one
  replacement transaction.
- Candidate fetches may write rows that are currently suppressed. That is
  intentional: they are provider facts, and the read query owns eligibility.
- A stale snapshot remains renderable and visibly stamped until a usable sweep
  replaces it. There is no background TTL that turns an outage into an empty
  answer.
- The implementation must test all three dismissal levels, the third-impression
  boundary, the 24-hour counting gate, 90-day re-eligibility with the stale
  window cleared, immediate read-time changes, active versus terminal
  acquisition states, every credited artist, decided ownership evidence, first
  and later atomic snapshot replacements, duplicate-input preparation, reason
  and artist array constraints, and review-rejection non-interference.
- Part 2's lease, keep and post-deletion suppression state is not represented
  here. It remains behind its own schema proposal.
