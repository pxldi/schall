# Schall — product end state

This document describes the **target**, not the current state. It is the
reference for intended behaviour, and docs/ROADMAP.md orders the gap between
it and what exists today. Decisions a contributor might
reverse by accident are recorded in docs/decisions/.

## Premise

Schall lets a user leave streaming services and own their music. They arrive
with playlists on Spotify (and similar), and end up with those songs as files
they own, organised in a library they control, playable through their own
Navidrome server.

Playlists are ingested through the **Spotify Web API**. An entry carries
artist, title, album, and duration — and, where Spotify supplies it, an
**ISRC**, which resolves most entries to a concrete recording by identifier
rather than by text.

## Acquisition

Songs are acquired two ways:

1. **Automatically**, by requesting and downloading through slskd.
2. **Manually**, by the user supplying their own files — ripped, purchased
   from Bandcamp, etc. — which are matched and imported through the same path.

Manual supply is an **upload**, not a folder the user is expected to have put
somewhere. The library lives wherever Schall is deployed and the purchase lands
on whatever machine the user bought it from; assuming the two are the same
machine is assuming a shell on the server. Uploaded files land in a staging
directory that is never a library root — a scanner that indexed a half-written
upload would be indexing a truncated file — and are validated there before
anything moves into the library.

An uploaded file is validated with the rules for a folder somebody chose, not
the rules for a copy a stranger sent. Acoustic identification can only object:
it blocks a file identified as a different recording, and its silence is
recorded and never treated as failure. This is deliberately the opposite of the
acquisition path, where audio agreement is the only thing that admits a peer's
copy (ADR 0002). The asymmetry is the point — a stranger's tags prove nothing
and the user's receipt does, so the provenance the upload carries is the user's
own word, which is exactly what a manual decision is worth everywhere else.

An upload of a recording the library already owns is subject to the duplicate
rule below, and until that rule is implemented it raises a duplicate question
rather than replacing anything.

**Granularity.** A playlist target acquires a **single file** — the matching
file out of a peer's folder, verified on its own against the target recording.
Whole-release acquisition remains the path for albums and EPs requested from a
followed artist's catalogue. The two validation shapes coexist.

Automatic acquisition is a retry loop, not a single attempt:

- Fetch all search results for a target.
- Rank them and pick the best candidate.
- Download and verify it.
- On verification failure, refuse the file and try the next candidate. A refusal
  is a decision about what Schall will use and never a deletion: the bytes are
  the provider's and the inbox is mounted read-only.
- When candidates are exhausted, do not fail permanently. Requeue the target
  for a later attempt: Soulseek peers are intermittently online, and the file
  may be available tomorrow.
- When a candidate is a close contender but not provable, send it to the
  review queue rather than accepting or discarding it.
- Some peers hold every transfer until a word is typed back in their private
  chat. Send the word where the message says which word it is, and show the
  message to the person where it does not. A file that arrives after a reply is
  proved exactly like any other file.

The requeue behaviour means targets have a long-lived pending state, not a
terminal failure state. **Retry cadence and backoff still need a rule** — this
is the one acquisition parameter deliberately left open.

**A want is visible for the whole of that state.** A want with a question is in
the review queue and a want with a copy on the way is among the transfers, but a
want that is only being looked for has neither, and for anything hard to find
that is most of its life. The Downloads screen shows those wants as its first
tab, with what has been tried for each and when: the screen answers "what became
of the thing I asked for" one step earlier than the transfers do, which is why
they are together rather than on a page of their own. The wants somebody stopped
looking for are kept beside them, because a decision to stop is taken back where
it was taken.

## Matching — the governing constraint

Matching must never produce a false positive. A wrong match pollutes the
library and is worse than no match at all. When the system is not certain it
either tries another candidate or asks the user. It never guesses, and never
resolves ambiguity with a similarity score or a confidence threshold.

One numeric tolerance exists and is the only one permitted: two durations
within **five seconds** of each other are read as agreeing. This is
exact-agreement-with-measurement-noise, not a similarity rank; it never
orders candidates, only admits or refuses a pair.

There are two matching problems and they compose: **B proposes, A verifies.**

**B. Playlist entry → MusicBrainz recording** (no file exists yet)
Evidence: ISRC where the API supplied one, else artist / title / album /
duration text. Its job is to resolve an entry to a *specific MusicBrainz
recording ID*, which becomes the acquisition target and the thing later
verification is checked against. ISRC agreement is conclusive on its own; text
evidence concludes only when it identifies exactly one recording and nothing
contradicts. A fuzzy search string is not an acceptable output — the target is
a concrete ID, or the entry goes to the user.

**A. File → MusicBrainz recording** (a file exists)
Evidence: AcoustID fingerprint, recording ID, ISRC, exact tag agreement. Runs
on both user-supplied files and slskd downloads.

**Verification.** After a download, run A on the file and ask whether it is the
target from B. A's answer is read at AcoustID's **leading fingerprint cluster
and nowhere else**: that cluster naming the target admits the file, however many
recordings the cluster carries, because a cluster is AcoustID's own statement
that they are one piece of audio and MusicBrainz keeps several rows for one
performance routinely. Names in a lower cluster are other audio that resembled
the fingerprint and are no part of the answer. A leading cluster naming
recordings that do not include the target refuses the file; an answer that
decides neither way holds it as a question rather than accepting or refusing it.
Nothing is admitted on tags. This is what stops a wrong B match from reaching the
library. See docs/decisions/0011.

**The anchor.** A want also carries thirty seconds of the audio it names,
fingerprinted once and kept on the want, so a copy can be proven without
MusicBrainz or AcoustID having heard the music. Where the entry has an ISRC that
audio is the preview the recording's distributor publishes for that code, and a
copy that fails to reproduce it is refused (docs/decisions/0024). Where the entry
has no ISRC the audio is a YouTube upload, chosen by the want's name and length:
ten results are read, the ones whose length is within the five-second tolerance
and whose title agrees with the want under the tag grader are candidates, and the
an artist-matching Topic upload wins over other candidates, and views choose within one kind. No candidate is silence.

A matching Topic upload may admit a copy. Another upload found by name leaves a
question. **Neither refuses one**, because it can be a sibling version of the song
with the same name and length, and a wrong reference must not make the right copy unfillable. The
upload's title, channel and view count are stored on the want and shown on the
copy, so a wrong admission can be seen and taken back. See
docs/decisions/0029.

**One registration entered twice.** MusicBrainz enters one performance as two
recordings and does not always merge them, so the fingerprint names one row while
the want is keyed to the other. Where the two rows are one registered track, that
identification is set aside — neither agreement nor contradiction — and the copy
falls to whatever else stands. Two things establish it: the rows share an ISRC
and the copy reproduces the excerpt the wanted recording's own distributor
publishes (docs/decisions/0025), or the rows name the same set of artists, hold
the same title once bracketed labels come off, and run lengths within two seconds
of each other (docs/decisions/0031). The second test takes an upload found by
name as the proof that admits; the first takes only the distributor's excerpt. A
copy with nothing to prove it is held with the row AcoustID named written on it,
never refused, and a lookup that fails holds it too.

**The artist credit** is compared as the set of artists it names. The wanted
side is the recording's credited artists, each with their own name and their
MusicBrainz aliases; the observed side is the file's artist tag split where it
joins two artists. A file naming part of a longer credit says nothing about the
rest of it. A name nobody credits refuses the pair. Where the audio has already
named the recording — AcoustID's leading cluster, or the excerpt fetched by the
recording's own ISRC — a credit that disagrees is recorded and shown and does
not refuse the copy; a differing recording ID, ISRC or duration still does. An
upload found by name does not clear a credit disagreement, because it is audio
published under the want's name rather than the recording's own. See
docs/decisions/0030.

Two failure modes, handled explicitly:

- **No AcoustID data exists** for a recording — obscure tracks, live versions,
  some remasters. "Could not verify" is not "verified". For **slskd-sourced
  files it fails closed**: the file routes to the review queue. For
  **user-supplied files it fails open** to the tag-conclusive rules: the
  user's own provenance is evidence, and AcoustID coverage is worst exactly
  for the self-released music imported by hand.
- **A fingerprint identifies a recording, not a release.** Which release a
  verified track files under is decided by rule, in order: (a) the release
  the target already implies, when B resolved through an album context or the
  ISRC pins one; (b) else the earliest official **album** release group
  containing the recording, through representative-edition selection; (c)
  else the earliest official release of any type. The choice is recorded per
  recording; a manual override is permanent.

## Review queue

Everything a want or a downloaded folder cannot settle on its own surfaces
here. This queue is what makes the zero-false-positive rule survivable in
practice: if resolving items is slow, users will resolve ambiguity by
reflexively accepting, and the guarantee dies in practice while still looking
alive in code. Speed is a correctness requirement, not polish.

Review holds three kinds of question, nothing else:

- **Downloaded** — one of a want's fetched copies is played and compared
  against the wanted recording, and the reader says whether one of them is it.
  A want whose accepted copy is already a library file under a different
  recording is the same card, headed "In your library"; accepting it files
  the copy as the wanted recording without fetching anything new.
- **Version** — which MusicBrainz recording a playlist entry means, when more
  than one fits it.
- **Folder** — what a downloaded album folder's odd files belong to, against
  its edition.

A file already on disk whose identity or catalogue match nothing could settle,
and a piece of music the library holds twice, are not review questions: they
are found through a filter in Library, where the file already lives. Deciding
about them there rather than in a queue keeps the queue to the things that
are still in motion — a want being fetched, a folder mid-import — and puts
everything already at rest beside the rest of the library.

The screen is named for the thing rather than asked as a question: a sentence
with a question mark on it reads as a sentence, is the widest thing on the
header, and says what every item of that kind already says. A rail lists the
open questions, filterable by kind; the evidence for the one open is read
straight down rather than through prose. There is no score anywhere on it —
Schall has none, and presentation that implies one would be a lie.

A downloaded copy is played and its waveform drawn, not decorative: one
player at a time, one volume for the room. A length that disagrees with what
was wanted is named beside the length itself rather than left as something to
subtract. Two candidate recordings that read alike on every field shown are
never preselected — the one thing this screen may never do is guess between
them on the reader's behalf.

Outcomes, all persisted permanently except the first:

1. **Skip** — move to the next question. Not a persisted decision; the
   question reappears exactly as it was.
2. **Remove from Wishlist** — stop pursuing this want entirely. Track-scoped
   only: the release and artist are untouched.
3. **None of these** (downloaded only, and not on the library card) — every
   copy offered is rejected; each rejected copy is remembered so it is never
   offered again for this want.
4. **Wrong song** (downloaded only) — the copies are irrelevant because
   resolution named the wrong recording; the want is returned for
   re-resolution.
5. **Accept** (downloaded) / **Use** (version) — the copy, or the recording,
   is the answer.
6. **Check again** (folder) — revalidate the folder against its edition.

The distinction between "None of these", "Remove from Wishlist" and "Wrong
song" is essential. Losing any one of the three either loses wanted music or
loops forever.

Interaction is keyboard-driven: Enter commits the selected answer, the arrow
keys and j/k move between questions, and the digit keys pick a copy or a
candidate.

## Library

Schall keeps an internal database and a filesystem library.

The database holds: followed artists, owned tracks, requested albums/EPs and
individual songs, pending acquisition targets, and the permanent record of
every user decision.

The filesystem library holds the audio files. Matched or manually assigned
files are moved into it. Layout is user-configurable, with a sensible default.
Changing the layout must be a supported operation, not a one-way door — it
implies moving existing files and repairing anything that references their
paths.

**Duplicates: the better copy replaces the worse one, and the loser is
deleted. No duplicates.** "Better" means higher audio quality *and* better
metadata fit; when the two disagree — a pristine FLAC with wrong tags against
a well-tagged lossy copy — the pair goes to review rather than being ranked.

Format and quality preferences are the user's, stored in `source_preferences`
and said in Settings → Library: an order over formats, the formats never to
fetch, and the lowest bitrate a lossy copy may have. They decide only what gets
tried. A refused format leaves the results rather than ranking low, because a
copy that only ranks low is picked the moment it is the only one on offer; a
want whose every offer was refused says so and names the preference. An
installation that says nothing ranks as Schall always has.

Still needing rules:

- **Naming** — the default layout and the escaping rules for artist and title
  strings that are not filesystem-safe.

## Following artists — the feed

Following an artist means Schall watches for their new releases and
automatically creates acquisition targets for them. This is a *feed*, and it
is a distinct mechanism from recommendations.

This matters because followed artists are suppressed from recommendations (see
below). If the feed is not built, new releases by followed artists surface
nowhere at all — suppressed from recommendations and never fetched. The feed
and the suppression rule must be built as a pair.

## Navidrome integration

Navidrome is the playback layer. It is pointed at the Schall library
directory. Users play through Symfonium on mobile and Feishin on desktop.

**Files.** Schall owns the library directory and is the sole writer. Navidrome
only ever reads: its scanner must never move, rename or rewrite tags on those
files. After Schall writes, it triggers a Navidrome rescan through the
Navidrome API rather than waiting for a scheduled scan.

**Playlists sync in both directions**, with a stored snapshot as the arbiter.

The complication is that a Schall playlist and a Navidrome playlist are not
the same object. A Schall playlist is a *want list* and may contain tracks not
yet acquired. Navidrome can only contain files that exist. A 50-track Schall
playlist with 30 acquired appears in Navidrome as 30 tracks.

Therefore:

- Each playlist stores a **snapshot of its last pushed state**. Every sync is
  a three-way comparison between that snapshot, the current Navidrome state,
  and the current Schall state.
- A track present in the last snapshot and absent from Navidrome now is a
  **deliberate removal**.
- A track never pushed — because it is not yet acquired — is **not a
  removal**. It is invisible to Navidrome by definition and must be left
  untouched.
- Navidrome-side removals may only ever remove from the acquired subset. They
  must never remove an unacquired want.
- Tracks added to a playlist in Navidrome are added to the Schall playlist.
- Playlists created in Navidrome can be adopted by Schall.

Without the snapshot there is no way to distinguish deliberate removal from
not-yet-downloaded, and the sync silently deletes exactly the music the user
is still waiting for.

## Recommendations

Two sources, in order of ambition:

1. **External** — Last.fm / MusicBrainz, driven by the user's scrobbling
   history.
2. **Own engine** — later, larger, built in-house.

Never recommend an item if any of the following holds:

1. **Owned** — the recording or release is already in the library.
2. **Already answered about** — the recording is in the acquisition queue,
   awaiting review, or was marked not wanted. The list offers two things, "find
   me a copy" and "never suggest this again", and somebody who has already
   answered the first has nothing left to press. Being marked not wanted is
   counted apart from being requested, because the reader is owed the
   difference, and it is taken back where it was taken: the want itself.
3. **By a followed artist or label** — because the follow feed already handles
   them. This is a routing rule, not a taste signal.
4. **Explicitly dismissed** — the user marked the track, release or artist as
   not interested. Permanent, at whichever level it was set.
5. **Had and not kept** — the weekly playlist obtained the recording, the user
   had it on their disc for a week, nothing said they wanted to keep it, and it
   was removed again. Suppress for 180 days, then make eligible again. Stronger
   than an ignored impression, because the song was on their disc and not only
   on their screen; weaker than a dismissal, because the user never said "not
   interested" and one busy week must not become a permanent refusal.
6. **Shown and ignored repeatedly** — after 3 impressions with no interaction,
   suppress for 90 days, then make eligible again. Time-limited rather than
   permanent: ignoring is weak evidence and taste changes.

An impression is a row that was on the reader's screen: it must have stayed
there for a moment, in a tab somebody is looking at. A row that was fetched,
that sat below the fold, or that went past under a fast scroll was not shown.
Only a recording the stored list offers can be counted at all.

Paging through the list steps by the rank the sweep stored, not by a position
in it. The six product entries are applied as the list is read, so a position
moves under a reader whose own reading suppressed something. The acquisition
entry has separate requested and not-wanted rule reasons. The follow entry has
separate artist and label rule reasons.

`More like this` and `Less like this` are advisory taste signals. More adds a
starting weight of 0.10 for each shared release group, credited artist, or
similarity seed context dimension on the next completed sweep. Less subtracts
the same weight. The total effect for one candidate is bounded to -0.30 through
0.30. Both signals only reorder candidates the sweep already found. Neither
hides a suggestion or changes the six hard suppression entries.

A press stores the recording, release group, complete artist credit, sweep
reason codes, and reason context copied from the current candidate. A second
press appends another event. It does not replace the first. The header has a
visible `Clear feedback` control. Confirmation clears all recommendation
feedback for ListenBrainz; the next sweep uses source ranking alone.

A feedback-boosted row explains itself with the `feedback_more_like_this` or
`feedback_less_like_this` reason code beside the source reason. Dismissals stay
permanent at their chosen level. Impressions, `recommendation_unkept`, wants,
and review decisions keep their existing lifetimes and boundaries. A review
queue rejection says that a file was the wrong recording; it never changes
recommendation feedback or recommendation rank.

The list belongs to the account settings name. Reading one takes about half an
hour of background work, and saving a different account — a different username,
or a different similarity algorithm — ends the reading under way rather than
finishing it. Nobody is shown one account's suggestions under another account's
name, because "Not interested" is permanent and has to be a decision about
music that was offered to that person.

### The weekly playlist

Once a week Schall takes twenty suggestions from that list, obtains them the
ordinary way, and puts them on one playlist the user's player can see. The user
listens for a week. A song they kept stays; a song nobody kept is removed again,
so the collection does not grow by twenty songs a week forever.

Two things keep a song, and each one only ever keeps:

1. **A star in the player** — the heart in Feishin or Symfonium.
2. **Keep in Schall**, next to the song on the weekly playlist screen.

**Silence keeps.** This is the inverse of the rule that governs matching, and it
is deliberate. Everywhere else an absent signal never admits anything; here an
absent signal never deletes anything. A read has three outcomes — the song was
kept, the signal was not there, or the signal could not be read — and only the
second may end in a deletion. A player that was down, a file the player has not
seen yet, or no player at all keeps the song and says so.

**Nothing is deleted that was not announced a week earlier.** One refresh finds
no keep and sets a date; the next reads the signals again and carries it out.
Keep pressed in between stops it for good. A removal marks the want not wanted
so nothing fetches the song again, and holds the recording back from the list for
180 days — the fifth suppression rule.

The feature is off until somebody turns it on, and it has a mode that reports
what it would remove without removing anything.

**Review-queue rejections must never feed this model.** A rejection there
means "this *file* is not that recording." It says nothing about whether the
user wants the recording. Treating it as disinterest silently abandons music
the user explicitly asked for. The two decision stores stay separate.

## UI

Modern, clean, fast, fully usable in a mobile browser. The review queue must
be operable entirely by keyboard on desktop.

The design source of truth is **`DESIGN.md`** at the root of this repository:
the colour tokens, the two type scales, the corner radii, the spacing steps,
the named components, and the named rules the interface must obey. The values
it declares are the values `web/src/styles.css` declares, and
`web/src/lib/design-system.test.ts` fails if the two ever disagree.

A session that needs a new page, or visual or layout changes to an existing
one, reads `DESIGN.md` first. Design is never inferred from existing components
and never invented in-session; a session that cannot find the answer there asks
for it. A change that moves a token changes `DESIGN.md` in the same commit —
the test is what makes that unavoidable rather than a habit.
