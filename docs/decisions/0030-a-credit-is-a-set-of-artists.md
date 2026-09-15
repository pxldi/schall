# 0030 — A credit is a set of artists

**Status:** accepted, 2026-09-05. Asked for by the owner after 30 wants had
stopped for a person to look at and none of them held a question worth asking.

## Context

Schall keeps a **want**: a piece of music somebody asked for that is not in the
library yet. A loop searches the Soulseek network, fetches a copy, and checks it
against the MusicBrainz recording the want is keyed to. One field of that check
is the artist: the file's `artist` tag against the recording's artist credit.

The comparison was string against string. It allowed a guest after the expected
artist, and it normalised case, punctuation and the spellings of a join. It
could not do three things:

- Read "Travi$ Scott" and "Travis Scott" as one artist.
- Read a file naming one credited artist as saying nothing about the others.
- Read the same five artists in another order as the same credit.

A credit that disagreed voided the pair whatever else agreed, so the copy was
discarded as `discarded_tags`. When every copy of a want was refused that way
the want stopped and waited for a person.

On 2026-09-05 the deployed instance held 30 wants stopped like that, and 308
copies refused on the artist tag alone over the project's life. In 110 of the
308 an audio witness had already identified the wanted recording: AcoustID's
leading cluster named it in 80, AcoustID and the Deezer sample both in 23, the
Deezer sample alone in 7.

The pairs look like this, file tag against MusicBrainz credit:

```
Travis Scott <> Travi$ Scott
Travis Scott <> DJ Mustard feat. Travis Scott
Playboi Carti <> $ir Cartier
M.I.A. <> Travis Scott feat. Future, Young Thug & M.I.A.
Kavinsky feat. Lovefoxxx <> Kavinsky & Lovefoxxx
Hudson Mohawke, Pusha T, French Montana, Future, Travis Scott
    <> Hudson Mohawke feat. Pusha T, Future, Travi$ Scott, French Montana
```

None of those is two artists disagreeing.

## Decision

**1. A credit is compared as the set of artists it names.** The wanted side is
the MusicBrainz recording's artist credit read as the list MusicBrainz sends it
as: for each credited artist, the name the release prints, the artist's own
name, and the artist's aliases. The recording lookup asks for `aliases`, which
costs no extra request. The observed side is the file's artist tag split where
it joins two artists: feat., ft., featuring, with, and, `&`, x, `×`, comma,
slash, plus. Both sides are normalised the way `tagmatch` normalises anything,
with every join spelled one way so that "Earth, Wind & Fire" and "Earth, Wind
and Fire" are the one artist they are.

The tag is read as a whole before it is read as a list, and every span of the
split is tried, so a name that carries a join of its own — "Tyler, The Creator",
"AC/DC" — is the one artist it is. A span counts only by being exactly a name
the artist answers to. A split that lands in the wrong place matches nothing; it
cannot match the wrong artist.

**2. Three outcomes.** Every name the tag holds is a credited artist and every
credited artist is named: **agrees**. Every name the tag holds is a credited
artist and some credited artist is not named: **unknown**, which is what a
tagger writing only the lead artist produces. Some name the tag holds is nobody
this recording credits: **differs**. An empty tag is unknown, as it always was.

**3. A credit difference alone does not void an audio identification.** Where
AcoustID's leading cluster names the wanted recording, or the copy reproduces
the excerpt fetched by that recording's own ISRC, an artist that differs is
recorded as differing and shown on the copy, and it does not refuse the copy. A
differing recording ID, a differing ISRC and a duration outside the tolerance
still void the pair whatever else agrees, exactly as
[0007](0007-one-grader-answers-both-matching-questions.md) says.

Those two witnesses and no others. A library witness is one copy of the music
rather than anybody who published it, and it never refuses anything, so it is
not asked to clear a refusal either. Neither is an excerpt found under the
recording's name instead of by its ISRC (ADR 0029), so the veto stays in force
for any anchor whose source is not `deezer`.

**4. Nothing changes about what tags alone may admit.** Tags never admit a
downloaded file ([0002](0002-could-not-verify-is-not-verified.md)). A credit
that agrees is one field in the same tag grades as before.

**5. What was refused is re-judged without a person asking.** A pass offers the
copies refused on the artist tag alone whose bytes are still in the inbox, and
puts each through the same validation any copy gets. The wants stopped by
credit-only refusals get their next look back. Both are done once, by migration
00087.

## What this admits that nothing could before

A copy whose artist tag names the artists the recording credits, under names
MusicBrainz records for them, in any order.

That is not a new kind of proof. Rule 1 turns a comparison that could not read
its input into one that can, and every outcome it reaches still has to pass the
same grader: tags alone admit no downloaded file, and in the library the artist
still has to agree together with the title and the duration.

Rule 3 admits copies the audio had already proven and the credit was overruling.
Nothing there rests on the credit. What is admitted is admitted by AcoustID or
by the distributor's excerpt, which is what admits any copy a stranger sent.

## What can go wrong

**A name with a join in it that MusicBrainz spells differently.** "Florence and
the Machine" against a credit printed "Florence + the Machine" agrees; against
an artist MusicBrainz holds only as "Florence Welch" it does not. The comparison
says differs, the copy is refused, and a person is asked. That is the direction
this fails in.

**Two different artists with the same name.** Names are compared, so two
MusicBrainz artists spelled alike read as one. The credit then agrees where a
person would ask. It is one field of a tag grade and admits nothing on its own,
and where the audio has spoken the credit is no longer deciding anything.

**A cover version with the original's credit on it.** Unchanged: the credit
agrees, and title and duration have to agree as well before the tags conclude
anything. What has changed is the case where the audio identified the recording
and the credit disagreed, which is now admitted — by the audio.

## What breaks if reversed

**If a credit goes back to being one string,** the 30 stopped wants stop again
on the next copy, and the artist tag goes on refusing music for spelling a name
the way its own sleeve spells it.

**If a credit difference voids an audio identification again,** the strongest
evidence in the system is overruled by the weakest, on the one path where the
tags were written by a stranger.

**If any anchor clears a credit,** an excerpt found under this recording's name
rather than by its ISRC becomes able to admit a copy whose credit says it is
somebody else's record. That is two weak claims standing in for a proof.

**If the veto is dropped for a recording ID, an ISRC or a duration too,** a
contradiction stops voiding a pair, and 0007's rule is gone.
