# 0022 — The serif retires, and two voices remain

**Status:** accepted, 2026-08-14
**Amends:** 0017 (the interface has typography of its own)

## Context

Schall is used through a web interface. Since 0017 it was drawn in three
typefaces, each with one job: Newsreader, a serif, on the headings and the
wordmark; Schibsted Grotesk, a sans-serif, on everything that is read; IBM Plex
Mono on every identifier and figure. 0017 argued the serif was the sleeve over
the catalogue — the cheapest true thing a record library could say about
itself.

## Decision

The serif leaves, entirely. The display role — page and question headings, the
wordmark, the large figures on stat cards — is set in Schibsted Grotesk, the
same face as the body, at the same 1.5rem and weight 700 it always had, with
its tracking tightened one step to -0.02em. The `--font-display` token and the
`.font-display` class stay: a heading is a role, not a face, and the role keeps
its own handle so display-size tracking can be tuned apart from body text.

The Newsreader font file, its `@font-face` rule, and its preload are removed.
The interface ships two faces.

## Why

The owner looked at the settings page and did not like what the serif was
doing. Three observations survived scrutiny:

1. **The serif sat on the wrong words.** Its argument was made for reading
   surfaces, and its most visible seats were product nouns — "slskd" set
   lowercase in a bookish serif, card headings on a settings form. A literary
   face on a tool's name reads as a costume.
2. **Its best surface was leaving anyway.** The review queue's long question
   prose — the one place a reading face earned its keep — is moving to compact
   evidence rows (issue #390). A display serif whose main text is departing is
   a voice kept for the sake of having three.
3. **Hierarchy never needed it.** The grotesque distinguishes a heading from a
   row with weight and size alone, which is how the rest of the interface
   already works; the third face was a second mechanism for a job one mechanism
   was doing.

Dropping it also removes one of the three font files the first paint waits on.

## Consequences

- Hierarchy in headings is carried by weight, size and tracking. If they ever
  fail to separate a heading from its surroundings, the answer is a size or
  weight step, not a new face.
- The wordmark is set in the grotesque like everything else. If a mark with
  its own character is wanted later, that is a logo decision, not a return of
  the display face.
- 0017's record stands as history; this amends its outcome the way 0020
  amended its monospace.
