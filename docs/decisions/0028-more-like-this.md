# 0028 — More like this is a soft taste signal

**Status:** accepted, 2026-08-30 — #387. The three questions the draft ended
with were answered on the same day; *Answers* below records them and the build
follows those answers.

## Context

The recommendations screen can record a want, a permanent dismissal, and an
impression. A want enters `acquisition_targets`; a dismissal enters
`recommendation_dismissals`; an impression updates `recommendation_impressions`.
These answers have different meanings and lifetimes. The screen has no positive
answer that says a suggestion was useful.

The current recommendation snapshot is stored in `recommendation_snapshots` and
`recommendation_candidates` (`internal/migrations/00047_recommendation_stores.sql`).
Each candidate has the following fields that can describe its neighbourhood:

- `source` and `musicbrainz_recording_id` identify the stored suggestion;
- `musicbrainz_release_group_id` and `musicbrainz_artist_ids` identify its
  catalogue context;
- `reason_codes` records why the source included it;
- `reason_context` records source evidence. The current sweep writes
  `model_id` and `latest_listened_at` for the collaborative-filtered answer and
  `seed_recording_ids` for the similarity answer
  (`internal/recommendations/service.go:806-820`, `:857-870`, `:1065-1082`).

There is no recommendation `tags` column in the current schema. This ADR uses
the existing `reason_codes` array as the sweep's tags and keeps the exact
`reason_context` JSON beside it. Genre or library tag data is outside this
proposal.

The source score is ranked in `orderedSourceCandidates`
(`internal/recommendations/service.go:947-965`) for the sweep walk. The final
stored rank is assigned by `prepareCandidates`
(`internal/recommendations/service.go:1528-1577`), and the read query returns
that rank from `queries/recommendations.sql:175-305`. The read path applies the
hard rules from `recommendation_candidates` against current local state. The
pure `SuppressionRuleFor` function in `internal/recommendations/service.go:1135-1159`
decides which rule wins. `internal/server/recommendations.go:219-244` counts
those suppressed rows and `:246-262` returns only rows with no suppression.

The issue calls the hard path five suppression rules. The current product text
lists six entries because the acquisition answer and the weekly-playlist
answer are separate entries, and the current Go code exposes the corresponding
rules such as `SuppressedNotWanted`, `SuppressedFollowedLabel`, and
`SuppressedUnkept` (`docs/PRODUCT.md:305-335`,
`internal/recommendations/service.go:1096-1120`). This ADR changes none of
those rules.

Review decisions stay outside this feature. In particular,
`acquisition_target_rejected_recordings.musicbrainz_recording_id`
(`internal/migrations/00029_rejected_resolutions.sql:22-41`) says that a file
was the wrong answer to a want. `download_import_decisions` records a person's
judgement about a downloaded file, its `track_id`, and its
`observed_fingerprint` (`internal/migrations/00011_import_decisions.sql:9-24`).
Neither table is taste feedback. `TestReviewQueueRejectionDoesNotAffectRecommendations`
in `internal/recommendations/suppression_integration_test.go:589-615` remains
the boundary test.

## Decision

### 1. Store one feedback event for each press

Add `recommendation_feedback` in
`internal/migrations/00080_recommendation_feedback.sql`. Migration `00080` is
the next free number after `00079_slskd_logged_out_since.sql`.

The table has these columns:

| Column | Definition and purpose |
| --- | --- |
| `id` | `UUID NOT NULL`; primary key with `gen_random_uuid()`. It identifies one press. |
| `source` | `TEXT NOT NULL`; the recommendation source, currently `listenbrainz`. It is required so a later source cannot consume another source's feedback. |
| `musicbrainz_recording_id` | `UUID NOT NULL`; the recording the user pressed on. Together with `source`, this identifies the suggestion. It has no foreign key because `recommendation_candidates` is a replaceable snapshot. |
| `musicbrainz_release_group_id` | `UUID NOT NULL`; copied from the candidate at press time. It is one part of the judged context. |
| `musicbrainz_artist_ids` | `UUID[] NOT NULL`; the complete, deduplicated artist credit copied from the candidate. It is one part of the judged context. |
| `signal` | `TEXT NOT NULL`; either `more_like_this` or `less_like_this`. |
| `reason_codes` | `TEXT[] NOT NULL`; the candidate's sweep tags, such as the current `cf_raw` and `similar_to`, copied at press time. |
| `reason_context` | `JSONB NOT NULL DEFAULT '{}'::jsonb`; the candidate's context copied at press time, including the existing `model_id`, `latest_listened_at`, and `seed_recording_ids` keys when present. |
| `created_at` | `TIMESTAMPTZ NOT NULL DEFAULT now()`; when the press was recorded. |

The primary key is `id`. There is no uniqueness constraint on the suggestion,
signal, or timestamp. The following checks belong in the migration:

- `source` is not blank;
- `signal` is one of `more_like_this` and `less_like_this`;
- `musicbrainz_artist_ids` is non-empty and contains no NULL element;
- `reason_codes` is non-empty and contains no NULL or empty element;
- `reason_context` is a JSON object.

Create these indexes:

- `recommendation_feedback_source_created_idx` on `(source, created_at DESC)`
  for the sweep's source read and the source-scoped clear operation;
- `recommendation_feedback_source_recording_idx` on
  `(source, musicbrainz_recording_id, created_at DESC)` for the suggestion's
  feedback history and diagnostics.

The feedback row copies the candidate context. It does not hold display names,
`rank`, or `fetched_at`; those describe one source snapshot and do not describe
the taste judgement. The event is still useful after that snapshot has been
replaced.

### 2. A second press appends

A second press appends another row. It does not update the first row.

The table is an event store for a soft, adjustable signal. Repeated presses can
express stronger confidence, one press can be paired with a later opposite press,
and clearing feedback can remove the complete history. The ranking code bounds
the total effect, so repeated presses cannot move a candidate without limit.

The API accepts only the current suggestion ID and signal. The handler reads the
current `recommendation_candidates` row for `source = 'listenbrainz'` and copies
its identifiers, `reason_codes`, and `reason_context` into the event. The client
does not submit the context. Add `POST /api/v1/recommendations/feedback` beside
the existing recommendation routes in `internal/server/api.go:691-697`.

If a sweep replaced the candidate before the press reaches the server, the
handler reports that the suggestion is stale and writes no partial event.

### 3. Apply feedback to the effective rank score

The sweep reads feedback after `expandCandidates` has populated the full
`CandidateInput` context and before the snapshot is replaced in `SweepPage`
(`internal/recommendations/service.go:423-704`). The provider ordering in
`orderedSourceCandidates` remains the expansion order. Feedback does not decide
which source records are expanded.

The final rank comparison currently happens in `prepareCandidates` at
`internal/recommendations/service.go:1535-1547`. Extend that comparison with an
in-memory effective score:

```text
effective score = source score + feedback delta
```

Keep `recommendation_candidates.source_score` as the source's original score.
Use the effective score only to sort and assign the existing `rank` column.

For each stored feedback row, compare the candidate against the copied
neighbourhood. A row contributes when the candidate shares a
`reason_context.seed_recording_ids` value, its
`musicbrainz_release_group_id`, or any `musicbrainz_artist_ids` value. The
recording that received the press is the anchor and does not match itself.
Shared `reason_codes` remain stored for provenance and explanation. A generic
code such as `cf_raw` does not create a neighbourhood on its own.

The starting weight is `0.10` for each matching context dimension. `More like
this` adds the weight; `Less like this` subtracts it. Clamp the summed feedback
delta for one candidate to `-0.30` through `+0.30`. These are starting weights,
not a permanent tuning claim. Taste-space is the part of recommendations where
a wrong guess costs an eye-roll, so this ADR permits tuning here. It must not
alter library admission, matching, acquisition proof, or suppression.

When the net delta is positive, append `feedback_more_like_this` to the
candidate's existing `reason_codes`. When it is negative, append
`feedback_less_like_this`. If the net delta is zero, append neither. The code is
added in the same candidate preparation path that trims and merges existing
reason codes (`internal/recommendations/service.go:1505-1510`). The resulting
array is persisted in `recommendation_candidates.reason_codes` and returned by
`recommendationResponse.ReasonCodes`
(`internal/server/recommendations.go:70-84, :383-401`).

### 4. Feedback cannot suppress

The structural boundary is the read query. `queries/recommendations.sql:151-305`
does not join `recommendation_feedback`. It returns the candidate's hard facts
from ownership, acquisition, follows, dismissals, impression fatigue, and the
weekly-playlist state. `factsFromRow` maps those facts to `SuppressionFacts`
(`internal/recommendations/service.go:1230-1240`), and `SuppressionRuleFor` has
no feedback case. `List` removes a row only when `SuppressedBy` is non-empty
(`internal/recommendations/service.go:1212-1227`). Feedback changes rank and
reason codes before storage. It cannot set a suppression fact.

Lowering a row is also not hiding it. The list is read a page at a time,
ordered by `rank`, with a default of 50 rows and no cap on what the sweep
stores (`internal/server/recommendations.go:139-176`). A negative delta moves a
row down inside that paged list. It never removes the row from the list, and it
never adds to the suppressed counts the header reports, because those counts
are read from the hard rules alone
(`internal/server/recommendations.go:219-244`).

The implementation must add
`TestRecommendationFeedbackNeverSuppresses`. The test inserts one candidate,
records `less_like_this`, and asserts that `List` still returns it and
`Evaluate` returns an empty `SuppressedBy`. It then adds a review-queue rejection
and repeats the assertion so the new test remains a mirror of
`TestReviewQueueRejectionDoesNotAffectRecommendations`. The test fails if the
feedback table is joined into the read query, if feedback is added to
`SuppressionFacts`, or if the list treats a negative soft score as hidden.

### 5. Explain feedback in the existing reason line

The reason codes join the codes from ADR 0016 in the existing
`recommendation_candidates.reason_codes TEXT[]` column. The current provider
codes are `cf_raw` and `similar_to` in `internal/recommendations/service.go:813-819`
and `:866-870`; ADR 0016 also defines the open vocabulary around
`tag_affinity`, `seed_consensus`, and related source reasons
(`docs/decisions/0016-recommendation-signals-and-candidates-have-their-own-lifetimes.md:136-143`).

Add these display mappings to the existing `reasons` map in
`web/src/lib/components/Recommendations.svelte:299-312`:

- `feedback_more_like_this` → `you asked for more like this`;
- `feedback_less_like_this` → `you asked for less like this`.

The row continues to show the source reason and the feedback reason together.
The UI does not reconstruct the neighbourhood from IDs. The source and stored
context remain available to diagnostics, while the short reason is readable on
the row.

### 6. Clear feedback with a visible control

Clearing all feedback is a source-scoped row delete. Add
`DELETE /api/v1/recommendations/feedback`, which deletes every
`recommendation_feedback` row for the current source in one transaction. It
does not delete dismissals, impressions, wants, or review decisions.

Place a `Clear feedback` control in the recommendations header beside the
existing `What is this` disclosure. The control opens a confirmation containing
the scope, `Clear all recommendation feedback?`, and the consequence that the
next sweep will use source ranking alone. On success, invalidate the
recommendations query. The next sweep has no feedback rows to read, so the
stored list returns to the ordering produced by the sweep alone.

### 7. Documentation changes belong to the build

Update `docs/PRODUCT.md` with these entries:

- state that `More like this` and `Less like this` are advisory taste signals;
- describe the two signal values, their effect on the next sweep, the bounded
  starting weight, and the fact that neither hides a suggestion;
- state that a press stores the recording, release group, complete artist
  credit, sweep reason codes, and reason context;
- describe the append-on-repeat behavior and the visible `Clear feedback`
  control;
- state that dismissals, impressions, `recommendation_unkept`, wants, and
  review decisions retain their existing boundaries;
- update the stale rule-count wording so the list in the document matches the
  current six entries and the split reason constants in the Go code;
- state that a feedback-boosted row explains itself with
  `feedback_more_like_this` and that clearing feedback restores sweep-only
  order.

Update `DESIGN.md` with these entries:

- a recommendation-row pattern for the paired `More like this` and
  `Less like this` controls and their pressed, pending, and error states;
- the rule that neither control uses the ember accent as a taste state, in line
  with the Chrome Rule at `DESIGN.md:263-288`;
- keyboard and coarse-pointer requirements for both controls, using the button
  rules at `DESIGN.md:638-660`;
- the placement and confirmation treatment for `Clear feedback`;
- the reason-line treatment: the feedback reason stays readable beside the
  source reason, and it cannot rely on hue alone under the Glyph Rule;
- the mobile arrangement for the controls and reason line so the row stays
  usable at the existing recommendation list widths.

## Why

The existing candidate row already contains the identifiers and explanation
material needed to describe a neighbourhood. Copying that material at the
press preserves the judgement after a source snapshot is replaced. An append-only
event gives the service a bounded, auditable signal that can be cleared without
touching permanent dismissals.

The score adjustment is confined to the sweep's ordering step. The read query
continues to answer eligibility from the existing hard stores, so a soft opinion
cannot become a permanent block. The returned reason code makes a ranking change
visible without changing what the source or Schall claims about the recording.

## What breaks if reversed

If feedback is stored on `recommendation_candidates`, the next snapshot replacement
erases it. If it is stored in `recommendation_dismissals`, a request for less
similar music becomes a permanent recording, release-group, or artist block. If
the read query treats a negative delta as a filter, `Less like this` becomes an
unannounced dismissal. If review tables are read as feedback, a wrong downloaded
file changes taste and the regression boundary in
`TestReviewQueueRejectionDoesNotAffectRecommendations` fails.

## Consequences

- Migration `00080_recommendation_feedback.sql` adds one append-only table and
  two B-tree indexes. No existing dismissal, impression, want, or review table
  changes.
- A feedback press survives candidate snapshot replacement and can affect the
  next completed sweep.
- Repeated presses accumulate until the per-candidate delta clamp. Opposite
  presses can cancel. Clearing feedback removes all source events.
- `recommendation_candidates.source_score` remains the provider score. The
  effective score exists only during rank preparation.
- A less-like-this event can move a row down while the row remains eligible and
  visible. The hard path still applies exactly as before.
- The implementation needs tests for append behavior, source and context
  capture, More and Less rank changes, the delta clamp, reason-code rendering,
  clear-and-restore behavior, review-queue non-interference, and dismissals
  remaining permanent.

## Answers

The draft ended with three questions. These are the answers the build is
written against.

### Feedback rows do not expire

A row stays until the person clears it. Nothing else removes one.

An expiry would be a second tuning number with no reading to set it from.
Impression fatigue already covers the case an expiry would be reaching for:
a suggestion seen often and never acted on falls back on its own
(`docs/decisions/0016`). Feedback is a different statement. The person said
this suggestion was useful, and that does not become untrue after ninety days.
The clamp in section 3 bounds what an old row can do, so nothing accumulates
into a surprise, and section 6's control removes the lot in one press.

### `reason_codes` is the whole context this proposal copies

The build copies `reason_codes` and `reason_context` as the sweep writes them
today. It adds no tag column and no genre lookup.

`reason_context` is `JSONB`, so a later proposal that wants genre or library
tags in the judged context writes another key into it and needs no migration.
Section 3's matching then gains a dimension. Nothing here has to be undone for
that to happen, which is the reason the context is stored as a document rather
than as columns.

### Feedback re-orders what the sweep found. It does not seed what the sweep looks for

This is the answer with a safety argument behind it, so it is written down
rather than left as scope.

Section 4 proves feedback cannot suppress, and the paragraph after it proves
lowering a row does not hide it. Both proofs rest on the same fact: every
candidate the sweep found is stored, and feedback only decides the order they
are stored in. Let feedback seed the expansion and that stops being true. A
`Less like this` would then keep a candidate from being fetched at all, and a
recording that never enters `recommendation_candidates` is hidden more
completely than any suppression rule hides one — with no row, no reason code,
and no suppressed count to report it. That is the unannounced dismissal this
ADR exists to prevent, arriving through the door marked ranking.

So the soft path reads the sweep's output and never its input.

The cost is real and belongs in the record. #387 asks that pressing
`More like this` "visibly raises related candidates on the next sweep". Under
this answer a related candidate is raised only when the sweep returned it
anyway. Feedback makes the list better ordered, not larger. Whether the sweep
should also ask the source for more of what was pressed is a separate proposal,
and it has to carry its own argument for why the resulting invisibility is
acceptable.

## The questions as they were asked

1. Should feedback rows remain until the user clears them, or should they expire
   after a period chosen by the owner?
2. Does #387 intend `reason_codes` to be the sweep's complete tag set, or should
   a later proposal add explicit genre or library-tag context?
3. Feedback re-orders the candidates the sweep already found. It does not ask
   the source for more of them, because section 3 keeps expansion out of the
   soft path. So a `More like this` raises related candidates only when the
   sweep returned them anyway. #387 asks that the press "visibly raises related
   candidates on the next sweep". Is re-ordering what the sweep found enough,
   or should a later proposal let feedback seed the expansion?
