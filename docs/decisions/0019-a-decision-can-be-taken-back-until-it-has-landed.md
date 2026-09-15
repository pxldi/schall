# 0019 — A decision can be taken back until it has landed

**Status:** accepted, 2026-08-13. The owner asked for undo, was told it
contradicts the two documents named below, confirmed the request, and then
accepted this specific mechanism — which is not the countdown the request had in
mind. That approval is what permits the work, and it covers the conditions
written here and nothing wider.

**Supersedes:** the sentence in
[0003](0003-five-review-queue-outcomes.md) reading *"Negative decisions persist
with the same weight as positive ones"* — only to the extent set out below. The
weight is unchanged. What changes is that a decision is not final at the instant
it is written; it is final once the thing it asked for has happened.

**Also amends:** CLAUDE.md's *"manual decisions always win and are permanent"*,
in the same narrow way. A manual decision still wins, and still cannot be
replaced by an automatic one. It is permanent from the moment it has been acted
on rather than from the moment it is written.

## Context

### What the review queue is

Schall keeps a **review queue**. It is a list of questions Schall could not
answer for itself, and a person answers them by hand, one at a time. It exists
because Schall is not allowed to guess: a wrong match puts the wrong music in
the library, and that is the one failure the project is built to prevent.

### What an answer does today

[0003](0003-five-review-queue-outcomes.md) gives the queue five outcomes. Four
are recorded and one is not:

1. **Accept candidate N** — this copy is the recording that was wanted.
2. **None of these** — every copy offered is refused, and the search continues.
3. **Not wanted** — stop looking for this recording at all.
4. **Wrong target** — the candidates are irrelevant, because the entry was
   resolved to the wrong recording in the first place.
5. **Defer** — decide later. Records nothing. Built in #343.

Each of the four is written to `acquisition_target_files`, a table with one row
per copy a peer offered, carrying a `verdict`, who decided it (`decided_by` is
`'user'` or `'schall'`) and when.

### What happens after an answer

Accepting does not bring the file in. It marks the row `verdict = 'accepted'`,
and the **import** that runs afterwards reads
`UserAcceptedCopy` (`internal/db/acquisition_files.go`) and brings the file into
the library without re-grading it, because a person's decision is not
re-litigated by a check. When the import finishes it writes the new library
file's identifier into `library_file_id` on that same row.

That is the fact this decision rests on. The schema already has a column that
says whether the consequence has happened:

```sql
CHECK (library_file_id IS NULL OR verdict = 'accepted')
```

`library_file_id IS NULL` means: a person has decided, and nothing has moved on
disk yet.

### What is wrong

There is no way back from a mis-press. The answer bar has four buttons and a
keyboard shortcut for each, the queue advances the moment one is pressed, and
until #343 the only thing it said about this was the word *"Permanent"*. A
person working a queue of sixteen questions with the keyboard, at the speed the
keyboard invites, will eventually press `W` on the question after the one they
meant. Today that recording is not wanted, for good, and nothing on the screen
offers a way back.

The existing partial answers make the gap plainer rather than smaller.
An identity question already has **Withdraw**; a paused import already has a
withdraw path. Wants — the most common kind of question in the queue — have
nothing.

## Decision

**A recorded decision may be taken back for as long as the thing it asked for
has not happened. Once it has happened, the decision stands and the way back is
a different action with a different name.**

"Has happened" is not a length of time and is not a countdown. It is a fact in
the database, different for each outcome, and each one is stated below.

### 1. Accept

**Reversible while `library_file_id IS NULL` on that row.**

Taking it back returns the row to `verdict = 'held'`, clears `decided_by`,
`summary` and `decided_at`, and the want returns to the queue with the same
copies it had.

**Refused once `library_file_id` is set.** The file is in the library. Undoing
the decision then would leave a file nothing points at, and the want would be
free to fetch and import the same recording a second time. What a person wants
at that point is to remove a file from the library, which is an existing action
with its own name, its own confirmation and its own handling of the bytes on
disk. The refusal says so.

### 2. None of these

**Reversible while every row it wrote is still `discarded_audio`, decided by
`'user'`, and no copy on that want has since been accepted or imported.**

Taking it back returns those rows to `held`.

**Refused if the want has moved on** — if the sweeper has since fetched another
copy and a decision has been recorded against it, or one has been imported.
Reversing then would put two answered copies on one want, which is the exact
state `AcceptHeldCopy`'s "one answer per want" guard exists to prevent.

### 3. Not wanted

**Reversible while the target's status is `not_wanted` and nothing else has
claimed the recording.**

Taking it back returns the target to `pending` and lets the sweeper look again.

**Refused if the recording has since been wanted by another target**
(`superseded`), because the thing being restored is already being pursued
elsewhere and restoring it would have Schall chase one recording twice.

### 4. Wrong target

**Reversible while the entry has not been re-resolved.**

Taking it back removes the bad recording identifier from the exclusion list and
restores the entry's previous resolution.

**Refused once resolution has run again** and produced a different recording,
because by then the decision has been acted on: a new recording is being
pursued, possibly with copies already fetched against it. Undoing the exclusion
would not remove those.

**Amended 2026-08-13.** "The entry's previous resolution" is now the whole
answer rather than one third of it. A resolved want carries three facts: which
MusicBrainz recording the entry names, which release group the answer ran
through (`musicbrainz_release_group_id`, the first rule of the release-choice
order at import time), and how the answer was reached (`resolution_method`).
Recording *wrong target* erases all three from the want, because the want goes
back to `unresolved` and an unresolved want may hold no answer. Only the
recording survived, in the exclusion list, so the first version of this undo
could restore only the recording: the want came back with no release group and
with `manual` as its method. The rejection row now keeps the release group and
the method as well, written at the moment the rejection is written, and taking
the rejection back puts both back on the want beside the recording. Nothing is
inferred from either on the way back — the values written are the values the
want had before anybody said the recording was wrong, and neither is read by
anything while the rejection stands.

A rejection recorded before that migration remembers neither, because the
columns did not exist when the row was written and a back-fill would be a fact
Schall invented. Taking one of those back behaves exactly as it did before:
the recording comes back, there is no release group, and the method reads
`manual`. It succeeds; it does not fail.

### What is never reversible

**Anything Schall decided for itself.** `decided_by = 'schall'` rows are not
touched by any of this. The acquisition loop writes refusals of its own
(0003, amended 2026-07-30) and those are the loop's reading of the audio, not a
person's answer. A person who disagrees with the loop answers the question the
loop raised; they do not edit its record.

**A manual identity decision**, which already has Withdraw, and a paused import,
which already has its own withdraw. Those paths are unchanged and this document
does not touch them.

## Proof it cannot produce a false positive

The governing rule is that a match is provable only by a recording-ID
agreement, an ISRC agreement, an enumerated exact-tag combination, or an AcoustID
identification at the leading cluster. **Nothing in this document creates a
match.** Every operation it describes deletes a decision or returns a row to
`held`, which is the state meaning *nobody has decided this*.

Each invariant, and why it still holds:

- **A contradiction voids a pair whatever else agrees.** Untouched. Grading is
  not involved; taking back an answer does not change what the grader will say
  about that copy when it is next asked. A copy returned to `held` is graded
  again from scratch.
- **An absent tag is silence, never agreement.** Untouched, same reason.
- **Assignment runs both ways.** Strengthened, not weakened. Every reversal
  above is refused precisely when it would produce two answers on one want,
  which is the state that would break it.
- **Manual decisions take precedence.** Unchanged for as long as they exist. A
  manual decision that has been taken back no longer exists, and a decision that
  has landed cannot be taken back.
- **A guest credit is the same artist; a shared name prefix is not.** Not
  involved.

**The concrete wrong-match scenario this could introduce, and why it is
refused:** a person accepts copy A for want W; the import brings A into the
library and writes `library_file_id`; the person then presses undo; the row
returns to `held`; the sweeper, seeing W unsatisfied, fetches copy B and imports
it. The library now holds the same recording twice, and `A`'s row points at a
library file no decision claims. This is refused by the `library_file_id IS
NULL` condition on outcome 1, which is checked in the same statement that
performs the reversal rather than read beforehand.

The second scenario: a person refuses all copies on want W; the sweeper fetches
copy C and the loop accepts it on the audio; the person then undoes the refusal;
the old copies return to `held` while C is accepted. Two answers on one want.
Refused by the condition on outcome 2, checked in the same statement.

## Every ambiguous case, and how each is surfaced

Reversal is not a judgment and produces no new ambiguity — it either applies or
is refused, and both are certain. What must be surfaced is the refusal, because
a person pressing undo and seeing nothing happen is the worst outcome available.

| Condition | What the person sees |
| --- | --- |
| The file is already in the library | "This copy is already in your library. Undoing the decision would leave it there with nothing pointing at it. Remove the file from the library instead." with a link to it. |
| The want has taken another answer | "This want has already been answered another way since. Nothing was changed." |
| The recording is wanted by another target | "Another want is already looking for this recording. Nothing was changed." |
| The entry has been resolved again | "This entry has already been resolved again. Nothing was changed." |
| The decision was Schall's, not a person's | The control is not offered at all. |

Each of these is a package-level sentinel checked with `errors.Is` in the
handler and mapped through `api.problem`, in the shape the codebase already
uses. None of them is a retry: every one of them is final and says what to do
instead, which is the project's standing rule about buttons whose only option is
to press them again.

## How it is offered

**Not a timer.** A countdown would make the interface lie: it would take the
undo away after six seconds while the decision was still perfectly reversible,
and it would offer undo for six seconds on a decision that had already landed in
two. The condition is a fact, so the control follows the fact.

The queue keeps the last answer of this session in the tab — the same place
Defer's list lives, and for the same reason: it is a fact about the person, not
about the library. The answer bar shows **Undo** naming what it would take back
("Undo — not wanted, Watchfire") for as long as that answer is still
reversible, and the control disappears when it stops being. `u` presses it.

One step, not a history. Schall is not offering a stack of undos, because the
second step back is indistinguishable from answering the previous question
again, which the queue already allows.

## The tests that pin the refusals

New behaviour needs tests proving what it **refuses**. All are integration
tests against a scratch database (`SCHALL_TEST_DATABASE_URL`), beside the
existing acquisition suites:

1. Undoing an accept whose `library_file_id` is set is refused, and the row is
   unchanged afterwards.
2. Undoing an accept whose `library_file_id` is null returns the row to `held`
   and clears `decided_by`, `summary` and `decided_at`.
3. Undoing a refusal on a want that has since accepted another copy is refused,
   and both the old rows and the new answer are unchanged.
4. Undoing "not wanted" on a target that has since been superseded is refused.
5. Undoing "wrong target" after the entry has been resolved again is refused.
6. A row with `decided_by = 'schall'` cannot be undone by any of the four
   operations, whatever its verdict.
7. Two undos of the same decision, run concurrently, settle as one reversal and
   one refusal — the guard is in the statement, not in a prior read.
8. A copy returned to `held` and then graded again reaches the same verdict it
   would have reached had it never been answered, so undoing does not
   launder a copy past a check.

**Amended 2026-08-13**, with the wider restoration above:

9. Undoing "wrong target" puts the release group and the resolution method back
   on the want, not only the recording.
10. Undoing a rejection that remembers neither — one written before the columns
    existed — still restores the recording, leaves the want with no release
    group and a method of `manual`, and does not fail.
11. Two undos of one rejection, run concurrently, settle as one reversal and one
    refusal, which is test 7 asked of the wrong-target path.

```sh
go test ./internal/acquisition/ ./internal/db/ ./internal/server/
make check
```

## What breaks if reversed

Little. Every operation is a delete or a state return; removing the feature
leaves the four outcomes exactly as they were before it, and any decision taken
back before removal is simply a decision that was never made. The one thing that
would persist is the `u` shortcut in muscle memory.

## Rejected

**A six-second countdown**, which is what the original critique suggested. It
answers the mis-press but it answers nothing else, and it introduces a second
notion of finality — one for the timer, one for the database — that will
disagree with each other the first time an import is slow. The condition already
exists in the schema; a timer would be a worse copy of it.

**Holding the write for a few seconds and only then committing it.** Safer in
one way — nothing to reverse, because nothing was written — and worse in two: a
tab closed inside the window silently loses an answer the person believes they
gave, and the queue either cannot advance or advances on a decision that has not
happened yet. Schall does not have optimistic writes anywhere else and this is
not the place to introduce them.

**A full undo history.** The second step back is the same as re-answering the
previous question, which the queue already supports by walking back to it.
