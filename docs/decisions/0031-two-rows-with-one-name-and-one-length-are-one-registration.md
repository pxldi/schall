# 0031 — Two rows with one name and one length are one registration

**Status:** accepted, 2026-09-05. Written from what the first YouTube anchors
produced on the deployed instance the same day. Amended the same day, when the
filing path was made to ask the same question: point 4 and the paragraph on the
filing path are the amendment. Amended again on 2026-09-08, when point 1 was
made to compare MusicBrainz artist identifiers where both rows carry them.
Amended again on 2026-09-09, when the filing path stopped filing a copy on the
two rows reading alike.

## Context

[0025](0025-one-registration-entered-twice-is-not-other-audio.md) settled what
happens when MusicBrainz enters one performance as two recordings and never
merges them: the fingerprint names one row, the want is keyed to the other, and
read as a difference that is the audio naming other music — the one refusal that
survives everything else agreeing, and the one that is final.

0025's test for "these two rows are one track" is a shared ISRC, the code a
record label registers a track under. That test cannot speak about most of the
pairs, because the row a want is keyed to often carries no ISRC at all.

### What happened

[0029](0029-a-popular-upload-found-by-name-is-an-audio-anchor.md) shipped and the
first YouTube anchors landed at 13:13 UTC on 2026-09-05. The re-judge of the
copies those wants were holding produced 25 refusals: copies whose audio
reproduces the want's anchor at bit error rates of 0.01 to 0.08, discarded as
`discarded_audio`, "The audio is a different recording", because AcoustID's
leading cluster named a different MusicBrainz row.

Two of the pairs, as MusicBrainz holds them:

| | the want's row | the row AcoustID named |
| --- | --- | --- |
| title | Dónde | Dónde |
| artist | Souly | Souly |
| length | 160000 ms | 160000 ms |
| ISRC | none | one |
| release | Dónde | traence |

| | the want's row | the row AcoustID named |
| --- | --- | --- |
| title | Nichts hittet mehr | Nichts hittet mehr |
| artist | Souly | Souly |
| length | 208695 ms | 208696 ms |
| ISRC | none | one |

One registration entered twice, which is exactly the case 0025 sets aside,
except that the wanted row has no code to share.

Across the whole history, 1244 copies on 83 wants — 72 of them still pending —
sit as `discarded_audio` with `evidence.differs == ["audio (identified as
another recording)"]` and exactly one AcoustID recording ID. The tags agreed.
The audio witness naming another row is the only thing that refused them.

### Why the code is missing on the side that matters

A row entered from a release the label registered carries the ISRC. A row
somebody typed in from a tracklist does not. The want is keyed to whichever of
the two the resolution found, and the missing code is on that side about half
the time.

It compounds. A want with an ISRC is anchored to the excerpt Deezer publishes
for that code; a want without one is anchored to a YouTube upload found by name
(0029). So the wants that cannot use 0025's test are the same wants whose anchor
0025 does not read, and both halves fail together.

### What the pairs actually look like

A sample of the 1244, looked up on MusicBrainz:

```
nothingleftnothingleft | $uicideboy$ Travis Barker | 100070 | no ISRC
  vs same | same | 100070 | ISRC                                    → one track
I Will Celebrate for Stepping on Broken Glass... | $‪uicideboy$ | 138000
  vs $uicideboy$ | 138000 | ISRC                                    → one track
Resin [Prod. GeeKey] | $uicideboy$ | 139000
  vs Resin | $uicideboy$ | 139000 | ISRC                            → one track
Who Am I | Travis Scott | 210000
  vs Who Am I | The Graduates | 207000                              → two tracks
Ziploc | 88GLAM | length unknown
  vs Ziploc | 88GLAM | 157933 | ISRC                                → the copy decides
Want To | 88GLAM & PnB Rock | length unknown
  vs Want To | 88GLAM | 147853                                      → two tracks
```

The second pair differs by a left-to-right embedding sitting inside the artist
name, which prints nothing. The third carries a producer credit in brackets on
one row and not the other.

## Decision

**Two MusicBrainz rows are one registration when they share an ISRC, as 0025
says, or when all of these hold:**

1. **Their artist credits name the same set of artists**, compared under
   `tagmatch.Artists` in both directions, with the characters that print nothing
   removed before the names are normalized.
2. **Their titles agree** under `tagmatch.Title`, with bracketed groups whose
   words are all uploader labels, or which open with a producer label, removed
   first.
3. **Their lengths are known and sit within 2000 ms of each other.** A row whose
   length MusicBrainz does not know is compared through the copy instead: the
   copy's own length has to sit within `DurationToleranceMS` of every row length
   that is known. Two rows with no length between them are not one registration
   by this test.
4. **Their registration codes do not contradict each other.** Two rows that both
   carry an ISRC and share none are two registered tracks whatever else agrees. A
   row with no code is silence and contradicts nothing, which is the usual shape
   of these pairs.

**Then 0025 applies unchanged.** The AcoustID identification of that row is set
aside — neither agreement nor contradiction — and the copy falls to whatever else
stands.

**What can then admit it is an anchor agreement.** A Deezer excerpt as before. An
upload found by name too, but only on this name-and-length branch: two rows of
equal length, with a copy inside the tolerance of both, exclude the sped-up edit
that made #591 restrict the ISRC branch to Deezer. The ISRC-only branch stays
Deezer-only.

**With no anchor, or an anchor between the thresholds, the copy is held.** It is
never refused, and its summary names the row AcoustID identified and says
MusicBrainz holds it as this same registered track.

**The same question is asked where the library filed a want's copy under a second
row.** A want names one recording, something proved a copy was it, the copy was
imported, and the library then filed the file under another MusicBrainz row. That
pair is read by the same test (`internal/acquisition/filing.go`, through
`OneRegistration`). What passing it may do there is set out in the amendment of
2026-09-09 below: on its own, nothing. The ISRC-only branch stays Deezer-only
there too.

**A lookup is a check and never evidence.** The row the audio named is read from
MusicBrainz with its artist credits, aliases, ISRCs and length. A lookup that
fails leaves the copy held with a sentence saying the check could not run, and it
is never read as a contradiction.

### Amendment, 2026-09-08: point 1 reads the artist identifiers

**Where both rows say which MusicBrainz artist every credited artist is, point 1
compares those identifiers and does not read the names at all.** The two credits
name the same set of artists when the sets of identifiers are equal, in any order
and under any join phrases. Two rows printing one name under two identifiers are
two artists MusicBrainz spells alike, and they stay two registrations. Where
either row credits somebody MusicBrainz holds no artist entity for, the name
comparison above applies unchanged, in both directions as before. A credit naming
Various Artists is read by name as before too, because it says nothing about any
one track. Points 2, 3 and 4 are untouched and all four still have to hold.

MusicBrainz renamed the artist Kanye West to Ye in 2021 and left the rows entered
before that printing the old name. A row credited "Kanye West" and a row credited
"Ye" both point at 164f0d73-1234-4e2c-8743-d77bf2191051, and the name comparison
failed them. On 2026-09-08 that stopped 29 wants, whose summary read "Your
library files that copy as … by Kanye West. This entry wants … by Ye, and the
audio cannot tell them apart", and it held part of 317 copies.

Which artist a row credits is MusicBrainz's own statement about that row. No
resemblance is measured and no threshold is read, and the amendment admits
nothing on its own: a pair that now passes point 1 still has to pass points 2, 3
and 4, and still falls to the same anchor and audio tests as any other pair.

[0032](0032-an-explicit-edition-is-preferred-over-a-clean-one.md) asks point 1 of
the clean edition and the explicit one through this same test, so it reads the
identifiers too.

### Amendment, 2026-09-09: on the filing path the rows alone file nothing

**The registration test is a check on a proof and never a substitute for one.**
Two things file a want's copy under the recording the want names, and neither is
a resemblance between the two rows:

1. **MusicBrainz merged them.** A lookup of the want's identifier and a lookup of
   the identifier the library used arrive at one row, because the provider answers
   a merged-away identifier with the recording it was merged into. That is the
   catalogue's own answer, so it is written to `recording_aliases` as a merge, and
   the want settles whatever else is known about the copy.
2. **Something proved the copy is the recording the want names**, and the second
   row was all that stood in the way. The audio answered — AcoustID at the leading
   cluster, the excerpt this recording's distributor publishes, or a file the
   library already holds and has proven — or a person listened and said so. Points
   1 to 4 above still have to hold as well, because a copy proved against the
   want's row and filed under a row that contradicts it is a question either way.

**Why the old rule was wrong.** The paragraph above used to say the
name-and-length branch needs no anchor on the filing path, because the copy is on
the disc from its own audio. That was an assumption about the copy rather than a
fact read off it. A copy can reach the filing path admitted by its tags, and a
copy judged before its want was resolved a second time was answered about a
recording the want no longer names. What admitted the copy is recorded on the
want's own copy row, so it is now read instead of assumed
(`db.AcquiredCopyFiling.ProvenMethod`, which is blank where the copy was judged
against a recording the want has since stopped naming).

**Everything else keeps its question.** Two rows with one artist, one title and
one length, one of them carrying no ISRC, with no merge and nothing proving the
copy, are different edits until something says otherwise. The want stops for a
person and names both recordings.

**A merge reaches the copies waiting on it.** Learning one queues one
`judge_want_copies` per want, as it already did for the copies held because
MusicBrainz could not be asked. The copies held under this amendment are found by
the recording their audio named, which is the other end of the merge.

**What the re-filed identity records.** Its evidence names what proved the copy
and says which of the two things filed it. It used to say, on every row it wrote,
that the copy had matched this recording's published sample and that the two rows
shared an ISRC. That was true on the ISRC branch and invented on this one, where
the wanted row usually carries no code at all.

**Nothing is admitted that was not admitted before.** Fewer copies are filed, not
more: every pair that settled on the two rows reading alike now needs a merge or a
proof of the copy as well.

### Why each test is the shape it is

**Both directions on the credit.** `tagmatch.Artists` answers Unknown where the
credit it is given names part of a longer one, which is the right answer about a
file that names only the lead artist and the wrong answer about two catalogue
rows. "Want To" by 88GLAM and "Want To" by 88GLAM & PnB Rock are two rows, and
they fail on the direction the shorter credit is read from.

**Two seconds, not five.** Both numbers are the catalogue's own, taken from the
same audio when it is the same audio: the two rows of "Nichts hittet mehr" sit
one millisecond apart. What the tolerance absorbs is one editor typing a length
off a sleeve while another took it from the disc. The five seconds a duration tag
gets is for a stranger's encoder, and it is wide enough to cover edits that are
genuinely different recordings.

**The copy stands in for a length MusicBrainz does not have.** The copy is the
audio both rows are supposed to describe. A row with no length cannot be set
against anything, so the question becomes whether the audio in hand fits the
length the catalogue does know, and that is the same tolerance any duration gets.
Two rows with no length leave only the title and the credit, which is what a
cover version also has.

**Codes that disagree end it before either test is asked.** MusicBrainz enters
the clean edit and the explicit one under one title, one credit and one length:
"Timeless" by The Weeknd runs 256 seconds on both rows, with USUG12406537 on one
and USUG12406536 on the other. The codes are the only thing that separates them,
and they are different audio.

**The producer group comes off the title.** "Resin [Prod. GeeKey]" and "Resin"
are one recording under two naming conventions, and the same is true of the
uploader labels 0029 already drops. Every version word stays: "sped up",
"slowed", "remix", "edit", "instrumental" and "live" all name different audio,
and dropping any of them is the false match this rule exists to refuse.

## What this admits that nothing could before

**A copy whose audio AcoustID identified as another MusicBrainz row, where the
two rows name one set of artists, hold one title and run one length, and where
an anchor agrees.** Before this, that copy was discarded permanently and the
want went on searching for a copy nobody could prove. The 25 refusals of
2026-09-05 are that case exactly.

**An upload found by name may now clear that refusal**, where before only the
Deezer excerpt could, and only where the two rows share a code. What makes that
safe is the length: 0029's upload is chosen within five seconds of the want, and
the two rows have to run within two seconds of each other, so the sped-up edit
that the ISRC branch had to guard against is outside both.

**An upload whose title carries a bracketed producer credit now agrees with a
want's title that does not.** That widens what `internal/anchor` will anchor a
want to, in one direction: a want for "Resin" may now be anchored to an upload
titled "Resin [Prod. GeeKey]". The upload still has to sit within five seconds
of the want's length and its credit still has to agree.

**Nothing is admitted on tags.** A copy from a stranger is admitted only where
the audio proved it, which is unchanged (ADR 0002): with the row set aside and no
anchor, the copy is a question and the want carries on looking.

## What can go wrong

**Two genuinely different recordings under one name, one credit and one length.**
A re-recording entered without a disambiguation comment, or a live take of the
same length, would pass all three tests. Admission then still needs an anchor
agreeing at chromaprint's measured threshold, so a wrong admission needs the
anchor to be that other recording as well — which, for an upload chosen by the
want's name and length, is possible. This is the residual risk, and the 2000 ms
agreement is what keeps it to pairs a person looking at the two rows would also
call one track.

**One performance registered twice under two codes.** A reissue MusicBrainz gave
its own ISRC reads as two registrations under point 4, and on the cluster path
that refusal is final: the copy is discarded and the want carries on looking. The
cost falls on pairs where two labels each registered the same recording, and it
is paid to keep the clean edit from answering for the explicit one.

**The lookups cost more than they did.** The check now runs wherever the audio
named another row and the excerpt has not already refused the copy, including on
wants with no anchor, which paid nothing before. It is one MusicBrainz request
per name in the leading cluster, paid only where a copy was about to be struck
off for good, against a provider that serves one request a second.

**A producer credit is not always noise.** Two producers' versions of one title
by one artist collapse to one title here. Nothing in the sample of 1244 is that,
and the length agreement has to hold as well, so such a pair would have to be
two recordings of one song, by one artist, within two seconds of each other.

## What breaks if reversed

**If the name-and-length test needs an anchor to run at all,** the 1244 copies on
wants that never got one stay refused for good, and the refusal is a wrong answer
nobody is shown.

**If the set-aside identification is read as agreement,** the fingerprint gains
credit for naming a recording it did not name, and a stored decision stops being
readable as the reason it was made. That is 0025's rule and it is unchanged here.

**If a failed lookup counts as the audio naming other music,** a MusicBrainz
outage becomes a permanent verdict about music, which is the one thing a network
failure must never be.

**If the ISRC branch takes an upload found by name,** the sped-up edit that
shares the original's code answers for a want for the original, silently and
permanently. #591 established that and this decision does not touch it.

**If the lengths may sit five seconds apart,** an edit that is genuinely other
audio reads as the same registration, and the one thing separating the two rows
stops separating them.
