# 0024 — A distributor's preview is an audio anchor

**Status:** accepted, 2026-08-16. Written at the owner's request, from a
pipeline proposal the owner brought, and amended the same day with a
calibration run against the 129 copies waiting in the live review queue. Two of
the three preconditions were met by that run and the thresholds below are
measured numbers rather than proposals. The owner accepted it on the same day.

Being accepted does not make it built. `docs/ROADMAP.md` item 10 is the plan
this feeds, and it is built in phases, each of which is proposed on its own.
The gate was built refusal-first and admission was switched on the same day, at
the owner's decision; the third precondition below records what was asked for
and what was done instead.

## Context

### What Schall is trying to do, and where it stops

Schall keeps a **want**: a piece of music somebody has asked for that is not in
the library yet. A want is created when a playlist is followed, when a followed
artist releases something, or by hand. It is stored in the
`acquisition_targets` table, and a background loop called the **sweeper** works
through the wants that are due, searches the Soulseek network for a copy, and
brings one in.

Before it can search, the loop has to know what the want *is*. That step is
called **resolution**, and it asks MusicBrainz — the open music catalogue Schall
uses to name music — which **recording** the entry names. A recording there is
one row for one performance. Resolution runs automatically, and it happens once
per want and then again on a schedule if it did not succeed.

A want that never resolves is never searched for. The query that picks work up,
`dueTargets` in `internal/db/acquisitions.go`, asks only for wants whose status
is `pending`, and a want only reaches `pending` once it has a recording.

### What actually happened

On 2026-08-14 a Spotify playlist of 314 entries was followed and imported. It
was mostly recent slowed and pitched edits — "worry - ultra slowed",
"starly - Slowed" — released through small digital distributors.

Of the 314 entries:

- **252** resolved to nothing. MusicBrainz holds no recording for them.
- **34** resolved, and were searched for.
- **28** of those 34 found no peer sharing a copy.
- **5** were fetched, proven by their audio, and imported.

The 252 were checked by hand against MusicBrainz, both by ISRC — the
registration code a recording carries, which is an exact key and not a text
search — and by artist and title. Every query that answered returned zero. Four
of sixteen requests returned a server error and are not counted either way. The
absence is real; it is not Schall being strict.

Those 252 wants can never be acquired as the system stands, however many times
they are asked about, because the chain that admits a copy ends at a MusicBrainz
recording identifier and there is none.

### Why the obvious fix does not work

The obvious fix is to add a second catalogue. Deezer holds this music: ten of
ten sampled ISRCs returned an exact match, with the right title, artist,
duration and album.

That does not help by itself, for two reasons.

**The description is not what is missing.** Spotify already gives Schall the
artist, title, album, duration and ISRC of every one of those entries, and
stores them on the want. Deezer would return the same five fields.

**The proof is what is missing.** The only thing that admits a fetched copy is
**AcoustID**: a service that turns audio into a fingerprint and answers with the
recordings that fingerprint belongs to. It answers in MusicBrainz recording
identifiers and nothing else — `internal/acoustid/client.go` types them as
such, because that is what the service returns. A want keyed to a Deezer
identifier has nothing that audio can agree with, so a copy fetched for it could
never be admitted. It would be held for a person to decide, every time.

Tags cannot close that gap. A peer's copy needs its audio to agree, because
consistently wrong tags on the wrong audio pass every check a tag can answer
([0002](0002-could-not-verify-is-not-verified.md)).

### The thing that was missed

There is a third possibility, and it is the reason this document exists.

Deezer and Apple both publish a **thirty-second preview** of each track they
carry, as a plain audio file, on a public address, with no key needed. The
preview is fetched by ISRC — the want's own ISRC, the exact key — so the file
that comes back is the audio of exactly the recording the want names.

That is an audio reference for a recording MusicBrainz has never heard of. It
does not route through AcoustID and does not need a MusicBrainz recording to
exist. Compared against a copy a peer sent, it answers the same question
AcoustID answers: is this file that piece of audio.

## What was measured

Nothing below is taken from the proposal or from anywhere else. It was measured
on 2026-08-16 with `fpcalc` 1.6.1, the same Chromaprint tool AcoustID uses.
The scripts were scratch work and are not kept; the calibration that decides the
thresholds is the one described under *Before it admits anything*, and that one
belongs in the repository.

### Previews exist for this music

Six unmatched entries, looked up at `api.deezer.com/2.0/track/isrc:{ISRC}`:

| Entry | Deezer returned | Preview |
| --- | --- | --- |
| worry - ultra slowed | worry (ultra slowed), 255s | yes |
| worry - Slowed | worry (Slowed), 201s | yes |
| starly - Slowed | starly (Slowed), 172s | yes |
| archangel - Slowed | archangel (Slowed), 186s | yes |
| deadrose - Slowed | deadrose (Slowed), 156s | yes |
| starly & jessie - Mashup | starly & jessie (Mashup), 318s | yes |

Six of six, and each returned the **edit**, not the song it was made from,
because the edit carries its own ISRC.

### The comparison separates cleanly

Chromaprint emits about eight 32-bit frames a second. Two fingerprints are
compared by sliding one across the other and counting how many bits differ at
the best alignment. That figure — the **bit error rate** — is the measurement.

The test built the real operation rather than assuming it: a whole file with the
wanted thirty seconds somewhere inside it and other music either side, all
re-encoded to 96 kbps mono, with the reference preview slid across it and read
only where the reference sits wholly inside.

| | pairs | lowest | highest |
| --- | --- | --- | --- |
| The preview **is** in the file | 21 | 0.0271 | **0.0771** |
| The preview **is not** in the file | 378 | **0.3978** | 0.5107 |

A gap of 0.32, with nothing in it.

The hardest cases are the ones that decide whether this is safe, and they hold:

```
0.4273   "worry - ultra slowed" against a file holding "worry - Slowed"
0.4924   "worry - Slowed" against a file holding "worry - ultra slowed"
0.4318   "archangel - Slowed" against a file holding "deadrose - Slowed"
```

Two speed edits of the same source song are told apart with room to spare,
because stretching audio in time rewrites its fingerprint.

### What the measurement does not cover

- The whole file in the test was built from the preview, so both sides carry the
  same master. This shows the comparison survives re-encoding; it does not show
  what a genuinely different master or remaster scores.
- Twenty-one true pairs is a small sample.
- No file from the actual library was used, because the library is on the
  server and this ran on a workstation.

A calibration run against real copies was a precondition of switching this on.
It has since been done, and it is the section below.

### The calibration against real copies

Run on 2026-08-16 against the live instance, after the review-queue fix shipped.
The queue then held **45 questions: 44 about copies and one about a resolution**,
and behind those 44 sat **129 copies a peer actually sent**. A copy is a file
Schall fetched, could not decide about, and kept. Those files are the real
corpus, better than the library for this purpose, because they are the exact
population the gate would judge.

The candidate audio was pulled through `/api/v1/review-queue/copies/{id}/audio`,
which re-encodes to 128 kbps MP3 before sending. So every candidate below was
already degraded before it was measured. That makes this a harder test than
production, where the gate reads the file in the inbox untouched.

**AcoustID knew nothing about any of them.** Of 129 copies, **0** carried a
single AcoustID recording ID. This is the whole problem in one number: the
witness Schall relies on has no evidence to give about this music.

**Previews exist for it anyway.** Of the 44 wants, 33 carry an ISRC, and **33 of
33 returned a Deezer preview** — every one. Asking MusicBrainz for an ISRC on the
other 11 recordings recovered **one** more, for *Mirage* by Skrillex, and that
ISRC returned a preview too. Anchorable today: **34 of 44 wants, 77%**.

**The separation.** Twenty wants, one copy each, compared against their own ISRC's
preview, then every copy against every other want's preview:

| | pairs | lowest | highest |
| --- | --- | --- | --- |
| The copy **is** the wanted recording | 20 | 0.0051 | **0.0682** |
| The copy **is not** it | 380 | **0.3521** | 0.5146 |

A gap of 0.28 with nothing in it, on real peer copies rather than constructed
files. The constructed-file run above found the same shape, which is the point of
repeating it.

**The hardest pair there is.** The queue holds two songs that each exist twice, as
the original and as a slowed edit. Same artist, same title, same catalogue.

```
        jessie   vs its own preview   0.0609      glamorgeddon   vs its own   0.0532
jessie - Slowed  vs its own preview   0.0571   glamorgeddon - S. vs its own   0.0622
        jessie   vs the Slowed file   0.3792      glamorgeddon   vs Slowed    0.4176
jessie - Slowed  vs the original      0.3804   glamorgeddon - S. vs original  0.4518
```

Six times the separation, on the case designed to break it.

**Every copy in the queue, measured.** All 76 copies belonging to one of the 33
wants whose preview was fetched scored between **0.005 and 0.140**. Not one
landed near the false range. The gate would have an answer for every
copies-question open today. The 34th want, *Mirage*, was anchored after this run
and its copies are not in the 76.

**A second master, found by measuring.** One want, *worry* by LONOWN and
riserayss, holds eleven copies, and they split into two groups that nothing in
the file names announces:

```
0.042   five copies    "LONOWN, Riserayss - worry"     180716-180767 ms
0.140   six copies     "LONOWN - worry", "01 Worry"    180706 ms
```

Both groups are the wanted recording — both sit far below any false pair. They
are two masters of it. This is the reason rule 2 has three outcomes and not two,
and it is the reason the dead band is wide: a true pair reached 0.140, so an
admit threshold of 0.15 would have been set one point above the worst real
answer.

**The thresholds this sets**, per the second precondition below:

| Bit error rate | Outcome |
| --- | --- |
| below **0.20** | the copy is that recording — admit |
| 0.20 to 0.30 | nothing is concluded — hold, a person decides |
| above **0.30** | the copy is not that recording — discard |

The worst true pair sits 30% below the admit line. The best false pair sits 17%
above the void line. Both bands are set from measurement, not from preference.

### The same test where the answer was already known

The run above has one weakness worth naming rather than hiding. A held copy has
no settled identity — that is why it is held — so a "true pair" there is true
because the copy was fetched for that want, not because anything independent said
so. What keeps the run honest is the false side: 380 pairs that are certainly
wrong all sat at 0.35 and above, and no true pair came near it.

The precondition asked for something stronger, and it was run as well: **library
files whose identity AcoustID has already settled.** Those have an answer that
this test did not supply. Eleven were sampled across the library, each one
identified by its audio rather than by a person, and each compared against the
Deezer preview for the recording AcoustID named:

| | pairs | lowest | highest |
| --- | --- | --- | --- |
| The file **is** that recording, per AcoustID | 11 | 0.0040 | **0.0631** |
| The file **is not** that recording | 110 | **0.3903** | 0.5034 |

Different artists, different music from the queue, labels supplied by AcoustID
and not by this test. Same two clusters, same gap. **The preview agrees with
AcoustID everywhere AcoustID has an opinion**, which is the claim this decision
rests on.

That sample also measures the coverage the library would have: of 19 resolved
files looked at, six had no ISRC in MusicBrainz and one ISRC had no preview, so
**11 of 19** were anchorable. Lower than the queue's 77%, and expected — the
library holds older music than the queue does.

### What these runs still do not cover

The 44 wants are one snapshot of one person's queue and lean heavily on two
artists; the library sample leans on two more. Nothing here says how the gate
behaves on a genre it has not seen, and nothing here has watched it refuse
anything in production. That is why the third precondition — refusal only,
first — stays in force.

### One trap, found by falling into it

The first attempt compared Deezer's preview against Apple's preview of the same
track and produced nonsense — true pairs scoring as badly as 0.44. The two
distributors cut their excerpts from different parts of the song, so there is
often no true alignment to find at all.

**The reference must be the short clip and the candidate must be the whole
file.** A whole file necessarily contains whatever thirty seconds the preview
was cut from. Two previews compared against each other do not.

That run also caught Apple's text search returning *Mau P* for a Tame Impala
query and *White Ferrari* for an *Ivy* query. Previews are fetched by ISRC and
never by text.

[0029](0029-a-popular-upload-found-by-name-is-an-audio-anchor.md) reverses that
last clause for wants no ISRC can reach: a YouTube upload found by name, length
and views is an anchor for those, and an anchor found that way may never refuse
a copy.

## Decision

**A distributor's preview, fetched by the want's own ISRC, is audio proof of
that recording, on the same footing as an AcoustID identification.**

Four rules, and they are the whole decision.

### 1. The anchor belongs to the want, not to the copy

When a want is created with an ISRC, Schall fetches the preview once and stores
its fingerprint on the want. Every candidate is then compared against a
fingerprint already on disk.

This is not an optimisation. It is what keeps the decision independent of a
third party being reachable at the moment somebody is waiting: no request is
made while a copy is being judged, no rate limit can delay a decision, and the
evidence that admitted a file is still on record long after the preview address
has rotted.

### 2. The gate is binary, and it has three outcomes, not a score

- Below the calibrated **admit** threshold: the copy is that recording. It is
  admitted, and the anchor is recorded as the evidence.
- Above the calibrated **void** threshold: the copy is not that recording. It is
  discarded, exactly as audio identified as another recording is discarded
  today.
- Between them, or where no preview exists, or where the fetch failed: nothing
  is concluded. The copy is held and a person decides.

An absent preview is silence and is never read as agreement. A fetch that failed
is recorded as a check that could not run, never as a pass
([0002](0002-could-not-verify-is-not-verified.md)).

### 3. Evidence is never summed

The proposal this came from ends with a scoring section: weight each signal, add
them up, and route by band. That is refused.

Weights let three weak signals add up to an admission, and the zero-false-positive
rule exists precisely to stop that. Duration agreeing is not a fraction of a
proof. It is a fact that can **void** a pair and can never build one. A
contradiction voids whatever else agrees, and that stays true here: a duration
more than five seconds out, or a differing ISRC, voids the pair however good the
fingerprint comparison was.

The way to fewer questions is more independent proofs, not heavier weights.

### 4. A want with an anchor may be searched without a recording

This is the part that delivers the coverage. `dueTargets` currently requires
`status = 'pending'`, and only resolution grants that. A want that MusicBrainz
has nothing for but that holds a preview fingerprint may be searched on its
stored text, and its copies judged by the anchor.

`acquisition_targets.musicbrainz_recording_id` is already nullable and the entry
text is already stored, so this is a behaviour change and not a migration.

## What this is worth

Of the 252 wants MusicBrainz has nothing for, the sample suggests nearly all
carry a usable preview. Each becomes a want that is searched, and whose copies
are admitted or refused without asking anybody.

It does not fix everything, and the number that matters is the one from the run
above: of the 34 wants that did resolve, **28 found no peer at all**. For this
music the scarce thing is people sharing it, not names for it. This decision
removes the naming wall. The sharing wall is still there and nothing in this
document touches it.

## Before it admits anything

1. ~~**Calibrate against the real library.**~~ **Done, 2026-08-16, twice.** The
   129 copies waiting in the review queue were one corpus — the population the
   gate would actually judge. Eleven library files whose identity AcoustID had
   already settled were the other, and those carry an answer the test did not
   supply. Both produced the same two clusters and the same gap.
2. ~~**Set the thresholds from the measured gap.**~~ **Done, 2026-08-16.** Admit
   below 0.20, discard above 0.30, hold between. A wide dead band is the correct
   answer while the sample is small, and the second master found in the *worry*
   copies is why it is this wide.
3. ~~**Run it in refusal only, first.**~~ **Amended by the owner, 2026-08-16.**
   The precondition asked for the gate to discard for a period with admission
   left off, then for admission to be switched on after watching what it
   refused. Refusal was built and shipped that way. The owner then chose to
   switch admission on the same day and to watch the running instance instead of
   waiting.

   The reason it was worth asking for stands unchanged: a wrong refusal costs a
   search, and a wrong admission puts the wrong music in the library
   permanently. What replaces the waiting period is reading the deployed
   instance's own log — an import recorded as proven by a published sample, and
   a copy discarded on its audio, are both single lines there.

   This is written here rather than dropped because a precondition that quietly
   disappears is one nobody can weigh later.

## Why

The rule Schall is built on is that a match is provable or it is a question.
What has changed is not the standard of proof but the number of things that can
meet it. AcoustID was treated as the only witness that could speak about audio,
and that made MusicBrainz's coverage the ceiling on everything, including music
MusicBrainz will plausibly never hold.

A preview fetched by ISRC is a better witness than a crowd-submitted
fingerprint in one specific way: it comes from the distributor that published
the recording, keyed by the code that recording was registered under. There is
no clustering to interpret and no submitter to trust.

## What breaks if reversed

**If the gate is scored rather than binary,** the guarantee goes. A score is a
threshold with extra steps: it admits a copy that nothing proved, and it does it
silently, which is worse than asking.

**If the anchor is fetched at decision time rather than stored on the want,**
every acquisition decision becomes dependent on a third party being up, and a
copy's evidence stops being reproducible the day the preview address changes.

**If a want may be searched without a recording and without an anchor,** copies
arrive that nothing can judge, and the review queue fills with rows nobody can
answer — the state it was just emptied of. The anchor is the licence to search;
the missing recording is not.

**If previews are matched by text rather than by ISRC,** the wrong recording
becomes the reference and every judgement after it is confidently wrong. This
was observed, not imagined: a text search for *Ivy* returned *White Ferrari*.
