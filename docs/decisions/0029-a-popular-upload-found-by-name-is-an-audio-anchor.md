# 0029 — A popular upload found by name is an audio anchor

**Status:** accepted, 2026-09-05, by the owner. It extends the preview gate of
[0024](0024-a-distributors-preview-is-an-audio-anchor.md) and reverses one
clause of it.


[0034](0034-only-a-topic-upload-admits.md) narrows which name-found upload may
admit a copy.
## Context

0024 gave a want an audio reference: the thirty-second preview Deezer publishes
for a track, fetched by the want's ISRC, fingerprinted and stored on the want.
A copy that reproduces the preview is admitted, one that does not is refused,
and the band between is a question. 0024 also said previews are fetched by
ISRC and never by text, because a text search once returned *White Ferrari*
for *Ivy*.

That key leaves out the music the review queue is actually full of. On
2026-09-05 the queue held 216 copies on 75 wants that no witness could speak
for. 71 of those wants have no ISRC on the entry and none on MusicBrainz. They
are leaks, sessions and unreleased tracks. Deezer has nothing for them, AcoustID
does not know 153 of the fingerprints and knows 14 more without a recording
attached, and the library holds no copy to vouch. Every one of them waits for a
person to listen.

YouTube carries most of this music. The owner's observation is that for a
given song there is usually one upload far more viewed than the rest, and that
its audio is the song. That upload cannot be fetched by a key. It has to be
searched for by name, and the search has to pick.

## Decision

**A YouTube upload chosen by name, length and views is an audio anchor for a
want that has no ISRC-keyed one. It may admit a copy and may ask about one. It
never refuses one.**

1. **Deezer first.** A want with an ISRC is anchored by Deezer as 0024 says. A
   want with no ISRC, or whose ISRC Deezer has no preview for, is anchored from
   YouTube instead of being recorded as having no anchor.

2. **The upload is chosen, not scored.** Schall searches YouTube for the
   want's primary artist and title and reads the first ten results. An upload
   is a candidate only when its length is within five seconds of the want's
   and its title, read as `Artist - Title` with the uploader's labels stripped
   (official, video, audio, lyrics, visualizer, HQ, HD, 4K, CDQ, unreleased,
   leak, full), agrees with the want under the tag grader. Among candidates the
   most-viewed one is the reference. No candidate is silence, and a want with
   no length cannot be keyed and is silence too.

3. **The gate is 0024's gate, minus refusal.** The excerpt is thirty seconds
   from the middle of the upload, fingerprinted and compared with the copy
   through the chromaprint thresholds. Same audio admits. The band between the
   thresholds asks. Other audio is recorded on the copy and shown, and the copy
   stays held. A reference found by name can be the wrong version of the song,
   and a wrong version must not make the right copy unfillable.

4. **The reference is written down.** The want stores the source, the video
   id, the upload's title and channel, its view count and when it was fetched.
   The admitted copy's evidence names the upload. The admission can be taken
   back as any other can.

5. **Held copies are re-judged when a reference lands.** A copy already in the
   queue is measured against the new anchor from its stored fingerprint, or
   from its bytes if they are still there, without anybody asking.

## What this admits that nothing could before

A copy whose audio reproduces the most-viewed YouTube upload that matches the
want's name and length. Before this, such a copy was held until a person
listened.

## What can go wrong

The chosen upload can be a sibling version of the song with the same name and
length. The copy is then admitted as the wanted recording when it is the
sibling. The length gate, the title gate and the view rule make that rarer, not
impossible. The owner accepts this for music that has no other reference. The
reference is on the row, so a wrong admission can be seen and taken back.

yt-dlp is a client boundary that YouTube breaks regularly. When it fails the
anchor is deferred and asked again. Nothing about a copy is decided by a fetch
that failed.

## What breaks if reversed

If a name-found reference may refuse, the queue fills with wants whose right
copies are thrown away against a wrong upload, silently and forever.

If the reference is chosen by relevance rank rather than by views, the first
result decides, and search ranking answers to the query, not to the song.
