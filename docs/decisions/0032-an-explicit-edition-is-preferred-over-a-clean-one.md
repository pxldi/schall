# 0032 — An explicit edition is preferred over a clean one

**Status:** accepted, 2026-09-06. The owner's decision, in their words: "Always
take the explicit version. I don't want any clean versions when an explicit one
is there."

## Context

A record label registers a censored track twice. The explicit master gets one
ISRC and the clean edition gets another, and MusicBrainz holds them as two
recordings with the same title, the same artist credit and the same length.
"Timeless" by The Weeknd runs 256 seconds on both rows, with USUG12406537 on one
and USUG12406536 on the other.

[0031](0031-two-rows-with-one-name-and-one-length-are-one-registration.md) point
4 reads two rows that both carry ISRCs and share none as two registered tracks,
whatever the names and the lengths say. That is right: they are two tracks and
the audio does differ. This pair was the case the rule was written to keep out.

So today the pair is a question, in both places one can arrive:

- **Entry resolution.** A playlist entry finds both rows, both fit its title,
  credit and length equally well, and the evidence does not choose between them.
  The want stops and asks which recording it is.
- **The filing path.** A want keyed to one row holds a copy the library filed
  under the other. `OneRegistration` answers no, the want stops, and its own
  music sits on the disc.

On the deployed instance on 2026-09-06, 11 stopped wants are such pairs.
Timeless, Watch, Paid in Full, It Ain't Hard to Tell and Take What You Want are
named clean/explicit pairs among them.

The owner does not want the question. Where MusicBrainz holds both editions,
the explicit one is the one to keep, and that is a preference about which music
to acquire rather than a claim about what any audio is.

Spotify says of every track whether it is explicit. Schall read the field and
threw it away.

## Decision

**1. A clean row is what MusicBrainz says is one, and nothing else.** A
recording is the clean edition when the editors' disambiguation comment on the
recording says so, when the recording's own title carries the marker, or when
every release it appears on says so in its own comment or its title. Every release, because a row that appears on one clean
compilation and on the ordinary album is the ordinary recording.

The marker words are enumerated: **clean**, **censored**, **edited**. They are
matched as whole words, case-insensitive. "edit" is not among them: a radio edit
runs a different length and is different audio. In a title the marker has to sit
in a qualifier group of its own — "(Clean)", "[Clean]", "Song - Clean", "Song
(Clean Version)", "Song (Edited)" — because a title is a name, and "Clean" by
Taylor Swift is a song. `cleanEdition` and its table tests are in
`internal/identity/clean.go`.

**2. A clean/explicit pair is two rows that 0031 separates on point 4 alone,
where exactly one is marked clean.** The two name the same set of artists
(0031 point 1, 0030's set comparison), hold the same title once the bracketed
labels and the clean marker are dropped (point 2), run lengths within 2000 ms of
each other (point 3), and both carry ISRCs sharing none (point 4 failing). If
neither row is marked, or both are, nothing here answers and the pair stays a
person's question.

**3. An entry resolves to the explicit row.** Where the recordings that fit an
entry equally well hold such a pair, the clean row is dropped and, if exactly one
recording is left, the entry resolves to it. The stored method carries
`-and-explicit-edition`, so the decision is readable as the reason it was made.
This is asked of an entry and never of a library file: an entry is a request for
a piece of music, and a file is a piece of music in hand, for which the clean
edition is a true answer.

**4. A want keyed to the clean row settles on the explicit file.** Want keyed to
X, copy filed by the library under Y, X and Y a clean/explicit pair: if X is the
clean row, the want settles against the file as the library has it. The file
keeps the recording the library gave it and is not re-filed under X, because the
two rows are two recordings and re-filing would write down that this audio is a
recording it is not. If Y is the clean row and X is not, the want does not
settle: it goes on looking for the explicit copy and the file stays where it is.

**5. The same on the cluster path.** Where a want names the clean row and
AcoustID's leading cluster names the explicit one, the identification is set
aside, exactly as 0031 sets aside a second row of one registration. Set aside is
neither agreement nor contradiction: the copy still needs an anchor agreeing at
chromaprint's measured threshold before anything admits it. The other direction
is untouched — a want for the explicit edition, offered a copy the audio says is
the clean one, is refused as it always was.

**6. Spotify's `explicit` flag is stored, and it only excludes.** It is kept on
the playlist entry and on the acquisition target (migration 00092, nullable, null
is silence). An entry flagged explicit is never resolved to a row MusicBrainz
marks as the clean edition: the row is dropped before grading, the way a
recording somebody rejected is, so it can be neither the answer nor a candidate.
The flag admits nothing on its own and is read nowhere else.

### Why each part is the shape it is

**The catalogue says it, not a resemblance.** Nothing here reads a string
distance, a score or a threshold. Two rows are a pair only where MusicBrainz's
own comment or release titles mark one of them, and where 0031's three positive
tests already hold. The one thing this rule overrides is 0031 point 4, in one
direction, and only where the catalogue itself explains why the codes differ.

**One direction, never both.** The clean row is dropped and the explicit row
answers for it. A want for the explicit edition is never answered by the clean
one, on any path. That asymmetry is the owner's decision written down.

**An entry, not a file.** The rule says which edition to acquire. Applied to a
file in hand it would relabel the clean copy somebody owns as the explicit
recording, which is a false identification, so `decide` asks it only where the
subject is an entry.

**The file is not re-filed.** 0031's filing path files the library file under the
want's recording, because there the two rows are one registration and the file is
that music. Here they are two recordings, so the want settles against the file
and the library's answer about the file stands.

**The flag cannot admit.** Spotify saying "explicit" is a fact about the track
somebody asked for, from the weakest evidence in the system. It removes an answer
and never supplies one, so an entry whose only MusicBrainz row is marked clean
goes unresolved rather than resolving to something the source ruled out.

## What this admits that it could not before

**An entry that fits a clean/explicit pair resolves to the explicit row instead
of asking.** Before this, both rows fit equally well and the want stopped for a
person.

**A want keyed to the clean row settles on a file that is the explicit edition
instead of stopping.** Before this, `OneRegistration` answered no on point 4 and
the want stopped with its own music on the disc.

Nothing else changes. A pair MusicBrainz marks neither way, or both ways, is
still a question. A copy whose audio names the clean row when the explicit one is
wanted is still refused by 0031 point 4. Tags still admit no downloaded file
(0002), and a copy admitted on the cluster path still needs an anchor.

## What can go wrong

**A row MusicBrainz marks wrongly.** An editor's comment saying "clean" on the
explicit master would send a want to the wrong edition. The cost is one wrong
edition of a track somebody asked for, and it is visible: the stored method and
the copy's summary both say the rule ran.

**A title marker on a release that is not about censoring.** A release titled
"Clean" would mark every row on it, so a recording whose only release is that
album reads as the clean edition. It has to be every release, and the pair still
needs a sibling row that is one track in every other respect, so the failure is a
want that stays a question rather than a wrong answer.

**A row marked nowhere.** A clean edition MusicBrainz entered with a plain
title, no comment and no marked release is not read as clean. Those pairs stay
questions.

**The entry the owner actually wanted clean.** There is no way to ask for the
clean edition of a track any more. That is the decision.

## What breaks if reversed

**If the rule runs on files as well as entries,** a clean copy in the library is
identified as the explicit recording, which is a false identification the whole
system exists to refuse.

**If it runs in both directions,** the clean edition answers a want for the
explicit one, which is the case 0031 point 4 was written for and the reason the
codes end it.

**If the file is re-filed under the want's clean row,** the library's record says
this audio is a recording it is not, and every later comparison reads that as the
catalogue's own answer.

**If Spotify's flag may admit,** a field written by a streaming service becomes
evidence about what audio is, which is the one thing tags never are.
