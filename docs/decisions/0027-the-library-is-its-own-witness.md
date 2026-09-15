# 0027 — The library is its own witness

**Status:** accepted, 2026-08-28. It is `docs/ROADMAP.md` item 10b, built as the
owner specified it, on top of the preview gate of
[0024](0024-a-distributors-preview-is-an-audio-anchor.md).

## Context

### What is being decided, and by whom

Schall keeps a **want**: one piece of music somebody asked for that is not in
the library yet. It is a row in the `acquisition_targets` table, and it names one
**recording** — one row in MusicBrainz, the open music catalogue Schall uses to
name music, standing for one performance.

A background loop called the **sweeper** searches the Soulseek network for a copy
of that music, fetches one file from a stranger, and then has to decide one
question before letting it in: **is this file that recording?** The copy carries
no provenance at all. Its tags were written by whoever ripped it, and
consistently wrong tags on the wrong audio pass every check a tag can answer
([0002](0002-could-not-verify-is-not-verified.md)), so tags alone never admit it.

Until now, two witnesses could answer, and both live outside the machine:

1. **AcoustID.** Schall computes an acoustic fingerprint of the audio and asks
   the AcoustID service which MusicBrainz recordings that fingerprint belongs to.
   It answers only for music somebody has submitted to it.
2. **The distributor's published sample.** For a want that carries an ISRC — the
   code a record label registers a track under — Schall fetches the thirty-second
   excerpt Deezer publishes for that code and keeps its fingerprint on the want
   ([0024](0024-a-distributors-preview-is-an-audio-anchor.md)). A copy that
   reproduces the excerpt is that recording.

A copy neither of them can decide about is **held**: neither imported nor thrown
away, with its evidence, as a question for a person in the review queue.

### The problem

Every copy that reaches a person is a question that had to be asked because
nothing in the machine could answer it. On 2026-08-16 the queue held 44 questions
about copies and 129 files behind them, and AcoustID returned no recording for a
single one of the 129. Both witnesses that can answer need somebody outside to
have heard the music first.

Meanwhile the library itself holds 1,609 files, each with a Chromaprint
fingerprint already on record, and many of them are files whose identity is
**settled** — proven by a person's own decision, by AcoustID, by an ISRC, or by a
MusicBrainz recording identifier. When a want names a recording the library
already holds a settled file for, there is a reference at home, and nobody has
been asking it.

### The blocker that turned out not to exist

`library_files.fingerprint` holds `fpcalc`'s compressed form. A bit error rate —
the measure Schall compares two fingerprints by — needs the raw frames, so this
looked like a schema change and a re-scan of the whole library. It is not: the
compressed form decodes back to the raw frames exactly, which was verified on
2026-08-16 over 1,192 of 1,192 frames, and `chromaprint.Decode` already does it
for the preview gate. No migration of fingerprints, and no re-scan.

## Decision

**A copy that holds the same music as a library file whose identity is settled is
that recording, on the strength of the proof that settled the older file.**

Six things make that safe, and each is a rule rather than a preference.

### 1. Only a settled file may speak

The list is enumerated in `settledProofs` (`internal/db/witness.go`) and it is
short: a person's own decision (`manual`), an AcoustID identification
(`fingerprint`, `fingerprint-and-identifier`), an ISRC (`isrc`), a MusicBrainz
recording identifier (`recording-id`), and a file admitted by this rule itself
(`library-witness`). Each also counts with the `-and-release` ending the grader
adds where a release singled one recording out of several already-proven ones,
because the proof underneath it is unchanged.

Left out: `title-artist-duration`, which is three tags agreeing and nothing more —
a file identified by its paperwork must not go on to admit a stranger's copy.
Also left out for now: `published-sample`. It is a proof of the same standing as
the others and the owner's enumeration did not include it, so widening the set to
cover it is a decision of its own and one line of code. A local-only decision
names no recording and can speak for none.

The file must also still be on disk and have a fingerprint kept.

### 2. It admits and it never refuses

A copy close to a settled file is that recording. A copy **far** from every
settled file says **nothing at all**. It is not a refusal, and this is the one
place where this witness is deliberately weaker than the other two: two masters
of one recording — a remaster, another pressing — measure far apart and both are
that recording. Only a witness that was told what the recording is by somebody who
published it may say a copy is other music. AcoustID and the distributor's sample
may; a library file may not.

### 3. It is read from the start of both files, never slid

`chromaprint.BitErrorRateFromStart` compares the two fingerprints from their
beginnings, over as much audio as both hold, allowing about five seconds of drift
for the ordinary difference between a rip that kept the silence before the music
and one that trimmed it.

This is deliberately not the sliding comparison the published sample uses. Sliding
finds the reference anywhere inside the candidate, which answers "does this file
*contain* that audio" — true of an hour-long DJ mix that plays the track twenty
minutes in. Reading from the start answers "is this file that audio". A stored
fingerprint covers only the opening two minutes, which is all `fpcalc` reads
unless told otherwise, and that is harmless here because both files start at the
same place.

### 4. It never overrides a contradiction

The pair is voided whatever this witness says when AcoustID's leading fingerprint
cluster names a different recording, when the copy fails to reproduce the
recording's published sample, or when the copy's own identifier, ISRC or measured
length contradicts the recording. `gradeOf` reads `contradicted()` first and
nothing below it runs. Evidence is never added up.

### 5. The debt is written down, and it is called in

An identity proven this way is **borrowed**. The row records
`admitted_by_file_id` — the file that answered — and `admitted_by_proof`, the
method that settled that file (migration `00069_library_witness.sql`).

When a manual decision is taken back, everything admitted on the strength of it is
withdrawn, and everything admitted on the strength of *those*, all the way down
(`withdrawAdmittedBy`, `internal/identity/service.go`). Those files return to
`needs_review` with one sentence saying why. Two paths call it: `Clear`, which
withdraws a decision, and `replaceIdentity`, where a person chooses a different
recording. Choosing the *same* recording again changes nothing and withdraws
nothing.

A manual decision met on the way down is left alone: somebody answered for that
file themselves, and a manual decision is permanent. A copy a person accepted by
hand records no debt at all, even where a library file happened to agree — it
rests on the person.

The review undo path needs nothing: `TakeBackAcceptedCopy` is only reversible
while `library_file_id IS NULL` and the request is not imported, so a copy that
has landed and been given an identity cannot be taken back there.

`admitted_by_file_id` is `ON DELETE SET NULL`. A file leaving the library does not
unprove the audio that was compared against it, so what is lost is the link and
never the identity; `admitted_by_proof` keeps the sentence readable.

A want re-arms with the identity it rested on. A want can stop on a copy that
landed as *any* file in a borrowed chain, not only the one whose decision
changed — a want's accepted copy may be the file two levels below the decision
that is being taken back, and `db.RearmWantsHoldingFile` reads only the one
file it is given. `rearmWantsHoldingFiles` (`internal/identity/service.go`)
therefore walks the file the decision was about *and* everything
`withdrawAdmittedBy` returned as fallen, in the same transaction as the
withdrawal, so every want stuck on any of them gets its next look back rather
than only the one nearest the decision.

### 6. It is asked first, and it costs nothing outside the machine

The order in `ImportOne` is: the copy's bytes, then its own fingerprint computed
once, then the library, then the published sample, then — only if the library did
not answer — AcoustID. A copy the library recognises therefore costs no request to
any service, which is the point: fewer questions for a person and fewer requests
for everybody else.

Where AcoustID is skipped, the length a match rests on is still the *measured*
one: `fpcalc` reports how long the audio it read ran, and that number is used
instead of the stranger's duration tag. A decode that hit the fifteen-minute read
limit is reported as unmeasured rather than as a fifteen-minute track, because a
length that disagrees by more than five seconds voids a pair.

## Consequences

- Every file admitted makes the next question easier to answer. This is the only
  proof in item 10 that compounds.
- A wrong manual decision now has reach: it can admit other copies. That is why
  the cascade exists and why it is tested two levels deep.
- The review queue gains one row per copy where the library had something to say:
  `same audio as <path> (proven by <what settled that file>)`.
- Nothing is backfilled. Files already held are still decided by hand.
- What this can admit that nothing could before: audio that is bit-near-identical,
  read from the start, to a file already proven by one of the enumerated proofs.
  Every contradiction that voided a pair before still voids it.
