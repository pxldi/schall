# 0025 — One registration entered twice is not other audio

**Status:** accepted, 2026-08-16. Written from a case the deployed instance
produced within an hour of the preview gate going live, and accepted by the
owner the same day.

## Context

### The things involved, and who uses them

Schall keeps a **want**: a piece of music somebody has asked for that is not in
the library yet. A background loop searches the Soulseek network for a copy. A
copy comes from a stranger and carries no provenance, so before it is let in
something has to prove the audio is that recording.

Two things can prove it, and both are the audio itself rather than what anybody
wrote about the audio:

- **AcoustID**, a service that turns audio into a fingerprint and answers with
  the MusicBrainz recordings that fingerprint belongs to.
- **A distributor's published sample**, the thirty-second excerpt Deezer or
  Apple publishes for a track, fetched by the recording's own **ISRC** — the
  code a record label registers a track under, an exact key rather than a text
  search ([0024](0024-a-distributors-preview-is-an-audio-anchor.md)).

A **recording** in MusicBrainz is one row for one performance. Schall keys a
want to one such row.

Both witnesses run automatically, on every copy. What should normally happen is
that they agree, or that only one of them has anything to say.

### What is wrong

MusicBrainz sometimes enters one performance as **two recordings** and never
merges them. When it does, the fingerprint names one row and the want is keyed
to the other. Read as a difference, that is the audio naming other music — the
one refusal that survives everything else agreeing, and the one that is final:
the copy is discarded and never offered for that want again.

The exact condition, all four at once:

1. A want is keyed to MusicBrainz recording **A**.
2. MusicBrainz also holds recording **B** for the same performance, and the two
   were never merged, so asking the provider which recording A's identifier
   names now still answers A.
3. AcoustID's leading fingerprint cluster names **B** and not A.
4. A copy of the music arrives.

### What happened, with the real objects

On 2026-08-16, minutes after the preview gate was deployed, the want **"Racks"
by 88GLAM feat. Gunna** received two copies from peers.

For both copies:

- The Deezer sample, fetched by the want's own ISRC, was reproduced at **0.066**
  and **0.070**. The threshold for "this is that recording" is 0.20, and the
  worst true pair ever measured was 0.140.
- The ISRC agreed, and the duration was **one millisecond** out.
- AcoustID named `f0a32851-2df8-4643-8649-f33f9a82c19d` at 0.97 likeness.

The want is keyed to `e8059316-e5f8-4963-b950-5b1dae76b313`. Asked directly,
MusicBrainz describes the two rows like this:

| | the want's row | the row AcoustID named |
| --- | --- | --- |
| title | Racks | Racks |
| length | 208525 ms | 208525 ms |
| ISRC | USUM71819697 | **USUM71819697** |
| artist credit | 88GLAM, Gunna | 88GLAM, Gunna |
| release | 88GLAM 2.5 | 88GLAM2 |

One performance, entered twice, differing only in which release each hangs off.

**The user-visible result:** the right music was fetched and thrown away, twice,
permanently, with the copy recorded as "The audio is a different recording."

### How likely the path is

Common enough to have arrived on the first day. A track that appears on both a
release and its deluxe or alternate edition is the ordinary case, and
`namedByRelease` was written because MusicBrainz hands such rows the same
identifiers. What is new is that Schall now has a second witness that can
notice.

It was invisible before the preview gate, because AcoustID was the only witness
that could speak about audio, and nothing can contradict the only witness.

## Decision

**An AcoustID identification that names another MusicBrainz row of the same
registered track is neither agreement nor contradiction.** It is set aside, and
the copy falls to whatever else stands.

Two things must be true, and neither is a resemblance:

1. **The copy reproduces the wanted recording's own published sample.** That is
   the audio itself saying this is the wanted music.
2. **The named recording carries an ISRC the wanted recording also carries.**
   MusicBrainz holding both rows under one registration code is the catalogue's
   own statement that one track is entered twice.

"Its own published sample" is the excerpt fetched by that recording's ISRC and
nothing else. An upload found by searching for the want's name
([0029](0029-a-popular-upload-found-by-name-is-an-audio-anchor.md)) is audio
published under this name, and the sibling edit sharing the ISRC is exactly what
such a search returns, so reading it here would cancel the fingerprint's refusal
with the audio the fingerprint was refusing.

**A second test was added on 2026-09-05.**
[0031](0031-two-rows-with-one-name-and-one-length-are-one-registration.md) holds
two rows to be one registration where they name the same artists, hold the same
title and run the same length, which is what the 1244 refusals here had and a
shared ISRC was not. That test takes an upload found by name, because two row
lengths within two seconds of each other rule out the edit this section is
about. Everything below applies to both tests unchanged.

It is asked at one moment only: where a copy was about to be refused for good
and the sample had already said the copy was right. It costs one provider
lookup for each name in the leading cluster, and nothing in the ordinary case.

### Set aside, not read as agreement

The identification is turned into silence and never into a tick. AcoustID named
a row this want is not keyed to; recording that as AcoustID having identified
this recording would write down a thing AcoustID never said, on the field a
person reads the decision back from.

So what admits the copy is the sample, and the stored method says so. This is
also the safer of the two: agreement would let the fingerprint alone carry a
later copy, and it is the sample that did the work.

### Nothing is written down

A **merge** is the provider's own answer — MusicBrainz redirects one identifier
to another — and Schall remembers merges, because the row it stores is a copy of
what the provider said. This is not that. It is Schall reading two of the
provider's rows and concluding they hold one registration, which is a conclusion
about one comparison and not a fact to file under the provider's name.

The filing path looks two identifiers up and can therefore see a merge where this
test was about to run. That merge is written down, because it is the provider
answering and not Schall concluding
([0031](0031-two-rows-with-one-name-and-one-length-are-one-registration.md), the
amendment of 2026-09-09). The conclusion this section is about is still written
nowhere.

## Why both conditions, and not either

**The ISRC alone would admit the wrong music.** MusicBrainz puts one ISRC on
genuinely different audio as well: the ISRC on "1992" is the ISRC on "1992 (sped
Up + slowed mixes)", and this codebase already documents that case in
`namedByRelease`. A want for the original, offered the sped-up edit, would have
AcoustID naming the edit's row — and a shared code alone would then turn that
refusal into silence, after which the file's own agreeing ISRC would admit it.
A false positive of exactly the kind the governing rule exists to prevent.

**The sample is what separates them.** Stretching audio in time rewrites its
fingerprint. 0024 measured the pair: a slowed edit against the original's sample
scores 0.38 to 0.45, against true pairs at 0.005 to 0.140. Six times the
separation, on the case designed to break it.

**The sample alone would not be enough either.** A copy that contains the wanted
thirty seconds inside other audio — a mix, a megamix — reproduces the sample
while being something else. The registration code answers that: a mix is
registered under its own code and shares none with the track. The length
contradiction catches it a second time, independently, and both refusals stay in
force.

## What this is worth

It stops the right music being discarded permanently. No question is added to
the review queue — the copy is proven and imported, exactly as it would have
been had MusicBrainz merged the two rows.

The two copies already discarded stay discarded. The want is still open, so the
next copy a peer offers is judged under this rule.

## What breaks if reversed

**If a shared registration code alone sets the identification aside,** a speed
edit is admitted for a want for the original, silently and permanently. This is
the whole reason the sample is required and not merely welcome.

**If the set-aside identification is read as agreement,** the fingerprint gains
credit for naming a recording it did not name, and a stored decision stops being
readable as the reason it was made.

**If the second row is remembered like a merge,** Schall's own inference ends up
filed as the provider's answer, and every later comparison reads it as though
MusicBrainz had said it.

**If nothing is done,** Schall goes on discarding correct copies for every track
MusicBrainz happens to hold twice, and the refusal is final each time. It is
worse than a wrong question: it is a wrong answer nobody is shown.
