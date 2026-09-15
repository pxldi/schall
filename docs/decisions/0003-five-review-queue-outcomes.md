# 0003 — The review queue has exactly five outcomes

**Status:** accepted, 2026-07-29

## Context

A review item asks: is this file the recording we were looking for? The
tempting minimal answer set is accept/reject. It is insufficient, because
"reject" conflates three different facts.

## Decision

Five outcomes, four of them persisted permanently:

1. **Accept candidate N** — this file is the target recording.
2. **None of these** — every candidate rejected; keep searching. Each
   rejected **(file, target) pair** is remembered so that file is never
   offered for that target again.
3. **Not wanted** — stop pursuing this target. **Track-scoped only**; the
   release and artist are untouched. Distinct from (2).
4. **Wrong target** — the candidates are irrelevant because B resolved the
   entry to the wrong recording. Re-opens resolution with the bad recording
   ID excluded.
5. **Defer** — decide later. The only non-persisted outcome; the item
   returns next session.

Negative decisions persist with the same weight as positive ones.

**Amended by [0019](0019-a-decision-can-be-taken-back-until-it-has-landed.md),
2026-08-13.** The weight is unchanged and so is the permanence; what 0019 moves
is when permanence begins. A decision is final once the thing it asked for has
happened — the copy is in the library, the want has taken another answer, the
entry has been resolved again — and until then it can be taken back. Read the
sentence above as being about a decision that has landed.

**Amended 2026-07-30**, when the acquisition loop began writing (2) by itself.
A copy it refuses on the audio, or on the file's own contradicting identifiers,
is the same sentence as *none of these* — this file is not that recording — so
it goes into the same store, `acquisition_target_files`, with
`decided_by = 'schall'` rather than `'user'`. Two stores would let the loop
offer a file somebody had just rejected. The outcome is unchanged; what changed
is that the user is no longer its only author.

The loop also produces a state this list never named: **held**. A copy nobody
could identify either way is neither accepted nor refused, and it is how an
item reaches this queue in the first place. Held is not a sixth outcome — it is
the question the five outcomes answer. See 0009.

## Why

(2), (3) and (4) answer different questions: "not this file", "not this
music", "not this resolution". Collapsing them either loses wanted music
(a rejection read as disinterest), loops forever (a disinterest read as
keep-searching), or leaves a bad B resolution permanently uncorrectable from
the queue — the only place the user ever sees it.

## What breaks if reversed

Without the (file, target) rejection store, the queue re-asks the same
question about the same file indefinitely. Without (3)/(2) distinction, the
retry loop either abandons wanted targets or chases unwanted ones forever.
Without (4), the only fix for a wrong B match is manual database surgery.
