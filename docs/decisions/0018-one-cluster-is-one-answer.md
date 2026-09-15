# 0018 — One cluster is one answer, and the row is chosen by rule

**Status:** accepted, 2026-08-13; narrowed by R1c on 2026-09-08. The owner approved the direction — a question
whose candidates cannot be told apart should not be asked — and then approved
this specific rule. Matching changes are on the CLAUDE.md STOP list, so that
approval is what permits the work; it covers the rule written below and nothing
wider.

**Supersedes:** the paragraph of
[0011](0011-the-audio-is-read-at-its-leading-cluster.md) headed *"Where several
rows fit, the question survives"*, for the identity path only.

## Context

Schall keeps a review queue. It is a list of questions the software could not
answer for itself, and a person answers them by hand. It exists because Schall
is not allowed to guess: a wrong match puts the wrong music in the library, and
that is the one failure the project is built to prevent.

One kind of question in that queue is an **identity question**. Schall has a
file, it has looked the file up, and more than one MusicBrainz recording fits it
equally well. MusicBrainz is the catalogue Schall uses to name music. A
*recording* there is one row for one performance.

The code that decides this lives in `internal/identity/grade.go`. It grades
every recording the lookup returned, keeps the ones whose evidence is
proof-grade, and then reaches a fork:

- one proof-grade recording — the file is resolved to it;
- several — one tie-break is tried, `namedByRelease`, which takes the recording
  the file's own release tag names if exactly one of them is named. If that
  fails, the outcome is `needs_review` and a person is asked.

**The problem is what the person is being asked.** MusicBrainz routinely holds
one performance as several rows: the same recording entered again for a
compilation, a reissue, a regional edition. AcoustID — the acoustic fingerprint
service Schall uses to identify audio — answers with ranked *clusters*, and
[0011](0011-the-audio-is-read-at-its-leading-cluster.md) established that
everything inside the leading cluster is AcoustID's own statement that those
recordings are **one piece of audio**.

So when the leading cluster names three recordings and all three come back
proof-grade, Schall asks a person to choose between three rows that describe the
same performance. The design review of 2026-08-12 found the live instance doing
exactly that: item 14 of 16 offered two candidates with the same title, the same
artist, the same 1 minute 27 seconds, and releases differing only in
capitalisation — `Thy Kingdom Come` against `THY KINGDOM COME` — under the same
justification sentence printed twice, each with the same green "This one"
button.

0011 already named this and left it open on purpose: *"What the file is has been
answered; which of MusicBrainz's rows to key the answer on has not, and that is a
question about identifiers rather than about music."*

A question about identifiers is not a question a person should be asked. Worse,
it is the most dangerous kind of question to put in this queue, because
`docs/PRODUCT.md` warns that a queue full of unanswerable items teaches people to
accept reflexively — and a reflex is how the zero-false-positive rule dies while
the code still looks correct.

## 1. The exact identifier the match rests on

**AcoustID fingerprint identification, read at the leading cluster.** That is
the identifier 0011 already established, and this decision introduces no other.
No threshold is added. No similarity score is read. `acousticFloor` stays at
0.90 and keeps meaning exactly what it means today.

The rule in one sentence:

> When every proof-grade candidate is named by the one leading fingerprint
> cluster, they are one piece of audio, the file's identity is already decided,
> and the only open question is which MusicBrainz row to key it on — so Schall
> chooses that row by a stated order instead of asking.

**The order is the one already written.** `grade.go` sorts its graded entries
before building candidates, by: grade, then evidence strength, then smallest
duration difference, then the provider's own ranking, then lowest recording ID.
The chosen row is the first of that sort. Nothing new is invented, the order is
total, and the same input gives the same answer on every run.

## 2. Proof it cannot produce a false positive

**The change adds no new trust in AcoustID.** This is the whole argument, and it
is worth stating plainly. 0011 already lets the leading cluster *admit* a file —
"the leading cluster naming the recording identifies the file as that recording,
**however many names that cluster carries**". R1c narrows this to one recording or an established same-registration set; distinct registrations and conflicting near-tied clusters remain unknown. The file is admitted today when one recording answers. What
happens today is that Schall admits the file and then asks a person which of the
cluster's names to write down. This decision writes one down by rule. The
question of whether the audio is right was already answered, by a rule that is
not changing.

**Four conditions, all of which must hold.** If any fails, the question survives
exactly as it does today.

1. **Every proof-grade candidate is named by the leading cluster** —
   `pair.acoustic == tagmatch.Agrees` for all of them. A candidate that reached
   proof grade some other way, by a recording ID or an ISRC the file's own tags
   carry, while being absent from the leading cluster, means the set is not one
   piece of audio. AcoustID has said nothing about that row. The question
   survives.
2. **The cluster is strong enough to identify anything** —
   `acousticSimilarity >= acousticFloor`. Unchanged, and restated because it is
   load-bearing here.
3. **No candidate in the set contradicts another.** No artist disagreement
   between any two, and no duration spread greater than five seconds across the
   set. Two rows AcoustID calls one audio, which MusicBrainz gives different
   artists or durations more than five seconds apart, are a disagreement in the
   data. The question survives, and its summary says why.
4. **The release tie-break has already failed.** `namedByRelease` keeps
   priority, because the file's own release tag naming one row is better
   evidence than an ordering.

**The invariants, each still standing:**

- *A contradiction voids the pair whatever else agrees.* Untouched. `gradeOf`
  and the comparison rules in `grade.go` run first and unchanged; only the fork
  after them changes. Condition 3 adds a contradiction test rather than removing
  one.
- *An absent tag is silence, never agreement.* `internal/tagmatch` is not
  touched. A file with no tags reaches this fork exactly as it does now.
- *Assignment runs both ways.* This decision is about `internal/identity`, which
  answers what a file *is*. It does not write a `track_mappings` row and does
  not bypass the single-candidate and single-owner tests in
  `matching.ReconcileFile`. A file auto-resolved here still has to win its place
  on a track by the existing rule.
- *Manual decisions win and are permanent.* The row written here is not manual.
  `library_file_identities.is_manual` is unchanged, and a person overriding this
  choice replaces it permanently, as today.
- *A guest credit is the same artist; a shared name prefix is not.* Condition 3
  compares artists through `tagmatch.Credit`, unchanged, so the cover-version
  trap is closed the same way it is everywhere else.

**The concrete wrong-match scenario, named honestly.** AcoustID puts two
recordings in one leading cluster that are not in fact the same performance —
say an album version and a radio edit that fingerprint alike. Schall then keys
the file to whichever the order picks.

What that costs: not wrong music in the library. The audio was admitted by a
rule that is not changing, and the file is the recording it is. The cost is the
wrong MusicBrainz row for the right audio — a different release credited, a
different track number. Condition 3 catches the realistic version of this,
because an edit and an album version differ by more than five seconds almost
always, and where they do not, they are the same length of the same performance
and the distinction has stopped being audible. A person can still override it,
permanently, and that override wins forever.

This is a real cost and it is smaller than the one it replaces. Today's
behaviour does not avoid that cost — it hands the same undecidable choice to a
person, with less information than the sort has, and PRODUCT.md says what people
do with a queue of those.

## 3. Every ambiguous case, and how each is surfaced

Nothing below is decided by this rule. Each keeps the surface it has today.

| Input | Surface |
| --- | --- |
| A proof-grade candidate absent from the leading cluster | `needs_review`, with all candidates and the existing per-candidate evidence |
| Artists disagree inside the cluster | `needs_review`, summary saying the rows disagree about who performed it |
| Duration spread over five seconds inside the cluster | `needs_review`, summary saying the rows disagree about how long it is |
| Leading cluster below `acousticFloor` | unchanged — not proof-grade, so this fork is never reached |
| Leading cluster names recordings, none of which this one answers to | `conflict`, unchanged |
| No cluster, tags only | unchanged |
| Nothing to search for | `local_only`, unchanged |

**What an auto-resolved file shows.** `resolvedOutcome` today drops the
candidate list, because a single conclusive answer has nothing to compare
against. On this path it must **keep it**. The row records:

- the chosen recording, as now;
- `Method` carrying the reason, in the shape the release tie-break already uses
  — today `…-and-release`, here `…-and-same-audio`;
- a summary in plain words: *"MusicBrainz enters this audio as 3 recordings.
  Schall filed it under the one whose details fit closest; the others are kept
  below."*
- the alternates, so the choice is visible and can be changed.

That last point is the difference between deciding and hiding. The user is not
asked, and is still told.

## 4. The tests that pin the refusals

Unit tests beside `internal/identity/grade.go`, each named for what it refuses:

1. Two proof-grade candidates, both named by the leading cluster, nothing
   disagreeing — **resolves**, method carries `same-audio`, both alternates
   recorded.
2. Two proof-grade candidates, one of them named only by the file's own ISRC and
   absent from the leading cluster — **still `needs_review`**.
3. Two proof-grade candidates in one cluster, durations eight seconds apart —
   **still `needs_review`**.
4. Two proof-grade candidates in one cluster, different artists — **still
   `needs_review`**.
5. Three in one cluster where the file's release names one — **release
   tie-break still wins**, method carries `release`, not `same-audio`.
6. A leading cluster below the floor — unchanged, this fork not reached.
7. The same input graded twice picks the same row — the order is total and
   stable.
8. A manual identity on the file survives untouched.

Integration coverage in `internal/identity/service_integration_test.go` against
a scratch database, proving a file that would have entered the review queue no
longer does, and that its recorded alternates are readable afterwards.

```sh
go test ./internal/matching/ ./internal/identity/ ./internal/tagmatch/ ./internal/downloads/
make check
```

## What breaks if reversed

Files already resolved this way keep their `library_file_identities` rows.
Reversing the rule stops new ones being written; it does not re-open the old
ones, and it should not — that would ask a person a question the software has
already answered and recorded.

## What is not decided here

**The other four question kinds.** `copies`, `unfound`, `match` and `duplicate`
are untouched. In particular the `copies` question — which of several downloaded
files is the wanted recording — stays exactly as it is, because there the
candidates are different files rather than different rows for one audio, and
choosing between files is a real question.

**Whether the review queue should show a count of what it decided for you.** The
alternates are recorded; whether a screen lists "resolved without asking" is an
interface question and belongs with the review-queue redesign.

**`docs/PRODUCT.md`.** Its review-queue section describes the identity question
as one a person answers. If this is accepted, that paragraph needs a sentence
saying which of them never reaches a person, and 0011's *"the question
survives"* paragraph needs the pointer added.
