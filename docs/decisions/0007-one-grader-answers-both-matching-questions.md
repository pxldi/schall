# 0007 — One grader answers both matching questions

**Status:** accepted, 2026-07-29

## Context

PRODUCT.md names two matching problems that compose: **B** resolves a playlist
entry to a MusicBrainz recording before any file exists, and **A** resolves a
file to one. B proposes, A verifies.

`internal/identity` was written for A. But its grader takes artist, title,
album, duration, ISRC and an optional recording ID, compares each against a
provider recording, and refuses to conclude unless exactly one candidate is
identified with nothing contradicting it. That is B's specification word for
word. The only file-shaped things in the package were at the two ends: where
the evidence was read from, and where the answer was written to.

So the choice was not how to write B. It was whether B gets its own copy of
the rules.

## Decision

One grader, used by both. The provider-asking loop moved out of
`identity.Service` into a storage-free `identity.Resolver`; `FileEvidence`
became `Evidence` with a `Subject` field so the sentences it produces say
"file" or "entry" as appropriate. `internal/acquisition` calls the same
`Resolve`, and writes the outcome onto an acquisition target instead of a
library file.

The grades are unchanged: recording-ID agreement, ISRC agreement, or
artist-and-title-and-duration together, with any contradiction — differing
recording ID, ISRC, artist credit, or duration more than five seconds out —
voiding the pair whatever else agrees.

**Amended 2026-09-05** by
[0030](0030-a-credit-is-a-set-of-artists.md), which took the artist credit out
of that list where the audio has already named the recording. A differing
recording ID, ISRC or duration still voids a pair whatever else agrees.

**Amended 2026-07-30**, when acquiring one file for one want needed a third
question — is this file that recording — answered by the same grader rather than
by a second one. Two grades were added, and both rest on the one piece of
evidence a file does not write itself: `fingerprint`, where the audio's
identification names exactly one recording and it is this one, and
`fingerprint-and-identifier`, where it names this one among others and the
file's own recording ID or ISRC names it too. Audio identified as some other
recording joins the contradictions. A file nobody listened to grades exactly as
it did before, which is what keeps the library's own resolution unchanged.

**Amended again 2026-08-02** by 0011, which changed the unit those two grades
read the audio at rather than the grades themselves. `fingerprint` is now the
leading fingerprint cluster naming this recording, however many names it
carries, and `fingerprint-and-identifier` is the same with the file's own
identifier naming it as well. One grader still answers all three questions.

**One rule did change.** `linked()`, which decides whether a recording is worth
*showing* as a possible answer, now also admits a pair whose title disagrees
while the artist and the duration agree. `weakestConclusive` is untouched, so
such a pair can never be concluded automatically.

Inconclusive resolutions store their candidates in
`acquisition_target_candidates`, deliberately the same shape as
`library_file_candidates`.

## Why

Two graders would not stay in step. `internal/tagmatch` already says this about
the comparison primitives — "a rule loosened for one of them is loosened for
both, on purpose" — and the reason applies with more force one level up, where
the rules decide whether music enters the library unasked. Divergence would not
show up as a build failure or a red test. It would show up as the looser of the
two implementations quietly becoming the one that matters, because it is the one
that says yes.

The `linked()` widening exists because of what real streaming metadata looks
like. Spotify supplies titles like "Glory Box - Remastered 2011"; no
MusicBrainz recording carries that title, and `tagmatch.Text` keeps the extra
words rather than guessing which suffixes are decoration. Before the widening,
such an entry with no ISRC produced *nothing at all* — not a wrong answer, but
no candidate either, so the user was told nothing matched a song plainly sitting
in the database. An empty queue is a worse failure than a queue with a question
in it, because there is nothing in it to correct.

Widening what is asked is safe in a way that widening what is concluded is not.
The only route from candidate to accepted runs through a person looking at the
agreements and the disagreements together.

## What breaks if reversed

If B gets its own grader, the two drift, and the drift is invisible until a
wrong file is in the library under the wrong name.

If the widening is reverted, entries whose titles carry streaming decoration and
whose ISRC is missing resolve to nothing, with no candidates and nothing for the
review queue to offer. Since ISRC coverage is not total and is worst for exactly
the obscure music this tool is for, that is a silent hole rather than an edge
case.

If the widening is taken further — letting artist and duration conclude without
the title — then every remaster, radio edit and live take of a song becomes
interchangeable with the original at the same length, decided automatically,
which is the single failure this project exists to prevent.
