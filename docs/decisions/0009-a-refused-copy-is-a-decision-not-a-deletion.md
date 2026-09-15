# 0009 — A refused copy is a decision, not a deletion

**Status:** accepted, 2026-07-30. A refusal still deletes nothing; the pass over
the download inbox in 0036 is what deletes a refused copy's file later, by a rule
of its own and never as part of the refusal.

## Context

Until the acquisition loop, no downloaded file was ever refused without a
person. A folder somebody chose was validated against its edition, and anything
uncertain paused for review. A want is different: it fetches copies nobody
asked for individually, one at a time, and something has to say what became of
each one without waking the user for every peer who shared the wrong song.

That is a change to import philosophy — automatic refusal of a downloaded file
did not exist — and PRODUCT.md described it in two words, "discard the file",
which read both as deletion and as a two-valued question.

## Decision

**Four verdicts about the music, and one about the transfer**, on
`acquisition_target_files`, one row per copy offered for a want:

- `accepted` — proven to be the wanted recording, and imported.
- `discarded_audio` — the audio was identified and it is not this recording.
- `discarded_tags` — the file's own identifiers or its length contradict it.
- `held` — nothing could decide. The copy is kept as a question with its
  evidence, and the want carries on looking.
- `undelivered` — the bytes never arrived, or were not what was offered.

The judgements are permanent: they will be as true tomorrow, so an offer that
earned one is never fetched again. `undelivered` is a fact about a transfer and
says nothing about whether that peer holds the right music, so the offer stays
in the running — it may be the only copy anybody is sharing.

**A refusal deletes nothing.** The bytes are the provider's, the inbox is
mounted read-only, and a discard is a decision about what Schall will use. The
evidence outlives the transfer record and the library file both, which is why
those references are `SET NULL` rather than `CASCADE`.

**A refusal is written by name.** `decided_by` is `'schall'` or `'user'`,
because the review queue's *none of these* (0003) is the same sentence as a
discard and belongs in the same store.

## Why

Two verdicts would force a choice between two mistakes. Discarding what could
not be identified throws away correct obscure files forever, and AcoustID
coverage is worst for exactly the music this tool exists for. Accepting them
admits a stranger's file on its tags, which is the one corruption path 0001 and
0002 close. `held` refuses the choice: it costs a question rather than a file
or the guarantee.

Keeping `undelivered` apart from the discards is what lets a want keep trying
the only peer who ever had the music. A failed transfer is the ordinary
condition of Soulseek, not evidence about a recording.

## What breaks if reversed

If `undelivered` is collapsed into the discards, a want stops retrying the one
peer holding the file and reports that nobody is sharing it.

If the discards are collapsed into `undelivered`, Schall re-fetches audio it
has already proven wrong, forever, at that peer's expense.

If `held` is collapsed into a discard, unverifiable music is thrown away
silently and the review queue never receives the traffic it exists for. If it
is collapsed into an accept, tags admit a downloaded file and 0002 is reversed.

If a refusal deletes, an import can never be redone from the same bytes, and
the probe file stops being the only thing Schall writes to a folder it does not
own.

## What is not decided here

Nothing drains `held` yet. The unified review queue (roadmap 5) is what reads
`acquisition_target_files_held_idx`, and until it exists a held copy is, in
practice, a refusal nobody was told about.
