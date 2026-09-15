# 0026 — A source is an identity

**Status:** accepted, 2026-08-27. Written from the owner's decisions, which are
implemented as given. This is phase 1: Schall can hold music MusicBrainz has
never heard of, and can say what that music is. Phases 3 and 4 — a want that
names a SoundCloud track, and acquiring one — are not decided here.

## Context

### What Schall is, and what it names music with

Schall is a music collection manager. It keeps a library of audio files on a
volume, and it says what each file is by asking **MusicBrainz**, the open music
catalogue. A **recording** in MusicBrainz is one row for one performance, and it
carries an identifier. That identifier is the key everything else in Schall
hangs from: a want names a recording, a download is admitted by proving it is
that recording, a duplicate is two files answering one recording, and the weekly
playlist decides what to delete by reading a recording.

The step that asks MusicBrainz what one file is, is called **resolution**. It
runs automatically, in the background, over every file the scanner finds. It
records its answer in `library_file_identities`, one row per file, and the file's
`resolution_status` says what came of it. One of those states is `local_only`:
the providers know of no such recording, and the file is owned music all the
same.

### What is wrong

`local_only` is where a large part of what people actually listen to ends up.

ADR 0024 recorded the measurement. A Spotify playlist of 314 entries was
followed on 2026-08-14. Of those, **252 resolved to nothing** — MusicBrainz
holds no recording for them, checked by hand both by ISRC and by artist and
title. They were slowed edits, pitched edits, bootlegs and remixes: music
published on SoundCloud and nowhere else, or published through a distributor so
small that no registration reached MusicBrainz.

For a file the user already owns, `local_only` means it sits in the library as a
path with a filename for a name. Navidrome — the music server the user plays
through — groups by tags, so a file whose tags nobody wrote reads as an unknown
track by an unknown artist on an unknown album. Schall knows nothing to write
into it, because everything Schall writes is what MusicBrainz proved.

That is the gap this closes. The music exists, the user owns it, and the person
who uploaded it knows exactly what it is called. The only thing missing is a way
for Schall to hold that name.

### The obvious fix, and why it is not taken

The obvious fix is to let the user type the title and the artist. It is refused
for the reason every guess is refused here: a name somebody typed is a claim
with nothing behind it, it cannot be checked, and it cannot be told apart later
from a name Schall proved. Schall would then hold two kinds of identity that
look identical and mean different things.

## Decision

### 1. A source identity is a second key, beside the MusicBrainz one

`library_file_identities` gains a third kind, `source`, alongside `external` and
`local_only`. A source identity carries three new columns:

| column | what it holds |
| --- | --- |
| `source` | the service the track lives on — `soundcloud` |
| `external_id` | that service's own identifier — `soundcloud:293` |
| `external_url` | the page the track is published at |

The database refuses everything that would blur the two keys together. A
`source` row must carry a non-blank service and identifier, and must carry **no**
`musicbrainz_recording_id`. A row of any other kind must carry no service and no
identifier. The existing `local_only` check stays exactly as it was.

The point of the second key is that it is a real key and not a label. It is
SoundCloud's own number for the track, so two files holding the same track can
be recognised as the same track, and a file can be traced back to the page it
was taken from. It is not a recording, it never becomes one, and nothing in
Schall reads it as one.

**A source identity proves nothing about any MusicBrainz recording.** It
therefore cannot satisfy a want, cannot admit a downloaded copy, and cannot make
two files duplicates of each other. Everything that decides by recording
identifier goes on deciding by recording identifier, and simply does not see
these files.

### 2. It is a manual decision, and manual decisions are permanent

A source identity is created by a person: they paste the address of a track, are
shown what SoundCloud says about it, and confirm. That is exactly the shape of
every other manual decision in Schall, and it is written the same way, with
`is_manual` set.

`is_manual` is what already stops resolution from touching a file again — the
`decided` branch of `identity.Resolve`. A test asserts it, because the skip is
the whole guarantee: a resolver that asked MusicBrainz about one of these files
could only produce a second answer contradicting the first, and the first one is
the one a person made.

The file's `resolution_status` becomes `source`. That is a new value in an
enumeration the resolver never writes; it exists so that the library list can be
narrowed to these files, and so that the queue which picks up unasked files —
which asks only for `pending` and `failed` — never picks one up. Two guards, and
a test for each.

### 3. Schall asks SoundCloud through oEmbed, and never asks for audio

**oEmbed** is a small convention a website can implement to describe one of its
pages to another program. SoundCloud implements it, and it needs no key, no
registration and no credentials:

```
GET https://soundcloud.com/oembed?format=json&url=<the track's page>
```

The answer carries the title, `author_name` (the uploader), `author_url`, a
`thumbnail_url`, and an HTML snippet embedding SoundCloud's own player. The
track's number is inside that snippet, in the player's address:
`api.soundcloud.com/tracks/293`.

**Schall never fetches the audio.** SoundCloud's terms of use forbid taking the
stream, and nothing in `internal/soundcloud` does. The bytes come from the
person, through the ordinary upload path, from a copy they already have. What
this reaches out for is the name of the thing and its picture.

The track number in the player is what **admits** an address. oEmbed answers
just as readily for a profile page or a playlist, and neither is a piece of
music; only a track's answer embeds a player pointed at a track. A path-shape
check — exactly two segments under the site, no `sets` — refuses the common
mistakes before a request is made, but it is an early refusal, not the
admission.

### 4. The title is stored whole, and only oEmbed's own suffix comes off

oEmbed's `title` field is stored **verbatim**, in the identity's evidence,
exactly as it arrived: `PinkPantheress - Illegal (Abo Edit) by Abo`.

The tags written into the file use that string with the trailing ` by <uploader>`
removed, and **only** when the words after the last ` by ` equal `author_name`
character for character.

This is not parsing the title. SoundCloud builds the oEmbed title by joining the
uploader's own title to the uploader's name with the word "by"; that join is
SoundCloud's, and undoing exactly that join is undoing a transformation this
program knows was applied. A track genuinely called "Illegal by Abo" uploaded by
somebody else keeps its whole title, and so does one whose uploader has since
renamed themselves.

Nothing else about the title is touched. `PinkPantheress - Illegal (Abo Edit)`
is **not** split at the dash. Which half of that is an artist is a guess, and a
guess written into a file is a wrong name that outlives the guess. The whole
string is the title.

The tags written are therefore:

| tag | value |
| --- | --- |
| `TITLE` | the title with oEmbed's suffix off |
| `ALBUM` | the same string — the track is a single and belongs to no release |
| `ARTIST`, `ALBUMARTIST` | the uploader |
| `COMMENT` | the track's page |

`ALBUM` repeats the title rather than being invented or left empty. A music
server groups by album; an empty album files the track under "Unknown Album"
with everything else, and a made-up album name files it under a release that
does not exist.

`COMMENT` is deliberately **not** in `tagging`'s `replaced` list — the set of
tags a managed copy states in Schall's words and clears when Schall has nothing
to say. A file Schall has nothing to say about therefore keeps whatever comment
it arrived with, and, as a consequence, `Revert` does not clear a comment Schall
wrote. That is chosen: the address is the truest thing in the file, and a
mapping falling away does not make it untrue.

### 5. The uploader is an artist row, keyed by their account

`artists.musicbrainz_id` was already nullable, for artists the library merely
holds. The table gains the same two columns the identity table now carries,
`source` and `external_id`, with a unique index over the pair. Two nullable
columns rather than an `artist_sources` table: every query that lists an artist
stays exactly as it was, where a separate table would have added a join to all
of them.

The uploader is found by service and account, **never by name**. "Abo" on
SoundCloud and "Abo" in MusicBrainz are two names that happen to match, and
merging them would put an uploader's single into a catalogue artist's
discography for good. An integration test holds this: with a MusicBrainz "Abo"
already in the table, naming a file from SoundCloud makes a second row.

The uploader is **not followed**. Following is a promise to keep somebody's
releases complete, and there are no releases. They are in the table for the same
reason a catalogue artist is — the library holds their music — with
`followed_at` null and `catalogue_summary` saying so in one sentence. The
refresh sweep and the follow feed both already require a followed artist with a
MusicBrainz identifier, so neither can ever ask about them.

Their completeness reads as **not applicable**: a new state, not "attention".
The attention rule badges an artist whose discography was never fetched, and for
these artists that is permanent and will never change, so the badge would be a
promise nothing is going to keep. `needs_attention` and the `attention` filter
both exclude them in SQL.

### 6. Artwork is fetched at import and held against the file

SoundCloud's `thumbnail_url` is rewritten to the `-t500x500` variant — every
size of one picture exists, and oEmbed usually points at a 100-pixel one that
looks like a smudge full-screen — and fetched at the moment the decision is
made. The answer must be a picture; a page served as `image/jpeg` is refused,
for the same reason `internal/coverart` refuses one.

It is stored in a new `file_cover_art` table, keyed by `library_file_id`.
Reusing `release_cover_art` with a nullable key was considered and refused:
`album_id` is that table's primary key, and making it nullable would take the
primary key off a table whose entire shape is one picture per release. The new
table holds the same four columns and makes the same bargain — a row with no
image records "asked, and there was none", so the question is not asked again.

The picture is written in two places. It is **embedded in the file**, through a
new `tagging.WriteArtwork` that makes the same copy-tag-verify-rename bargain
every tag write makes; and it is written **beside the file as cover.jpg** by the
existing disk pass, which gains a third leg for files that belong to no release.

One folder holds one cover.jpg, and an upload is a flat set of files (ROADMAP
8a), so two SoundCloud singles can land in one folder and only the first one's
picture is written. That is accepted rather than worked around: the copy that
always travels with the right music is the embedded one, and the folder copy
stays best-effort, exactly as it is for releases.

### 7. What these files can and cannot join

**They can be added to a playlist by hand.** Playlist entries carry
`owned_library_file_id`, so a file is addable whatever names it.

**The weekly playlist ignores them entirely.** The weekly playlist puts a song
on trial: it grants a lease, watches whether the player's star lands on it, and
deletes it if none does. Every part of that reads the recording — the
suppression of what the library already holds, and the read of the star back
against the recording. None of it can be done honestly here. A lease is the only
licence to delete music (ADR 0021), and the machinery never puts a file it
cannot reason about on trial. The exclusion is written into
`WeeklyPlaylistArrivals` as a clause of its own rather than being left to fall
out of the recording join, and a test holds it.

**They cannot satisfy a want.** Wants stay MusicBrainz-keyed. A want that names
a SoundCloud track is phase 3.

**They are never untagged.** The pass that gives a file back the tags it arrived
with looks for a file Schall tagged that no `track_mappings` row accounts for.
That describes every one of these files, permanently — their tags came from what
SoundCloud published, and having no mapping is their ordinary state. Left
unguarded, the first thing to ask about such a file would strip the track's name
off it. The untag query excludes them, and a test fails without the guard.

## Consequences

Schall can hold music MusicBrainz has never heard of, name it, tag it, picture
it, and show it in the library beside everything else — with the uploader as an
artist page of their own and a link back to where the music came from.

The zero-false-positives rule is untouched. Nothing here admits a match. A
source identity is a statement about **where a file came from**, made by the
person who put it there, and it is stored in a shape that cannot be mistaken for
a statement about which recording the audio holds. Every mechanism that decides
by recording identifier is blind to these files by construction — they carry no
recording, and the database refuses to store one on them.

Three things are owed. A tag write that fails — a read-only volume, a container
taglib will not write — is logged and nothing more: the decision stands, and
there is no pass that comes back for it later. The control on the uploads page is
a line pointing at the library rather than a control of its own, because naming
needs a library file and an upload in flight is not one yet. And the library
summary's resolution counts do not bucket these files: they are counted under
neither `resolved` nor `local_only`, because they are neither, and no count of
their own was added. Those figures are a list of outstanding questions, and a
file somebody has already answered raises none — the filter on the library list
is how they are found.

Going back is handled. The Down migration deletes the source identities and puts
their files back to `pending` before it restores the old CHECK constraints,
because `ADD CONSTRAINT` validates the rows already there and would otherwise
fail on any database where somebody had used the feature. The uploader's artist
row survives, minus the two columns that said which account it was, as an
ordinary artist the library merely holds — which is what its `catalogue_summary`
already says it is.
