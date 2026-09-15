# 0020 — The monospace stops being the loudest thing on screen

**Status:** accepted, 2026-08-13
**Amends:** 0017 (the interface has typography of its own)

## Context

Schall is used through a web interface. Every page of it is drawn in three
typefaces, named in `web/src/styles.css` as `--font-sans`, `--font-display` and
`--font-mono`. `--font-mono` is the monospace — a face where every character is
the same width, so that figures line up in a column. It draws the material that
proves a match: paths, MusicBrainz recording IDs, ISRCs, durations, byte counts,
bitrates and sample rates. A `.numeric` class in `styles.css` is how a piece of
markup asks for it.

Decision 0017 created that role in August 2026 and gave it to **Martian Mono**.
That document did not hide what it was choosing. It measured the face at one
character per 0.7em where JetBrains Mono, IBM Plex Mono, Geist Mono, DM Mono and
the system stack all set 0.6em, called it "16.7% wider than the alternatives",
and accepted the width **on four conditions**: paths truncate from the left,
short identifiers get a copy control, the review candidate row stops depending
on width it does not have, and the mono columns take the room that cutting the
interface's repeated prose gives back. The last of those is the one that pays,
and 0017 said so plainly: "The wide monospace is a bet that the interface will
hold less text."

## What is wrong

**The bet was lost, and it was lost in a way that could only be seen after the
face shipped.** Three things happened at once.

1. **The prose was not cut, so the room never arrived.** A review of the live
   interface on 2026-08-13 found the monospace drawing whole English sentences.
   `saving moves nothing`. `read-only — Schall never writes to ListenBrainz`.
   `must exist in the container, inside an allowed root`. `The list of releases
   could not be loaded`, followed by the server's own error. There were 212
   places asking for `.numeric` and a large share of them held no figure at all.

2. **The width became visible as emphasis.** A monospace 16.7% wider than its
   neighbours does not read as neutral beside a grotesque. It reads as bold.
   Set on the least important text in the product — a hint under a field, a
   timestamp, the word `seconds` after a number box — it made that text the
   loudest thing in its row.

3. **It broke a phone.** In Settings, the System health panel draws one row per
   connected service: a coloured dot, the service name, and a reading. The name
   is a flex child, which may shrink to its content, and the reading beside it
   does not wrap. At 390px there was no room for both, so the name gave way and
   `slskd` was drawn as three lines of two letters. `ListenBrainz` took five.
   The row layout was the fault; the width is what made the fault reachable.

The owner saw all of it in place, on the real screens, and asked for a narrower
and quieter face. That is the same test 0017 applied to Fraunces, which held the
display role during the build and was replaced before it ever landed — seen on
the real screens, it was wrong.

## Decision

**`--font-mono` becomes IBM Plex Mono.**

- 0.6em advance width, which is the figure 0017 measured for every alternative
  it weighed. A 36-character recording ID at 12px goes from 302px back to 259px.
- Two static files, `ibm-plex-mono-latin-400.woff2` (14.4 kB) and
  `ibm-plex-mono-latin-600.woff2` (15.3 kB). **IBM Plex Mono has no variable
  release**, so this role is the one place in Schall that ships two files
  instead of one. Only the 400 is preloaded in `app.html`; the 600 draws a
  handful of glyphs and is never the first thing read.
- `font-synthesis: none` is already set, so a weight asked for between the two
  is drawn at 400 rather than faked. Nothing asks for 500 any more: `.field` and
  the cycling placeholder behind it both moved to 400, and they have to agree
  with each other exactly or the text steps on the first keystroke.
- SIL Open Font License, like the other two. Self-hosted, like the other two.
  A page load still reaches no font service.

**The mono is cut back to what the mono is for.** `DESIGN.md` already carried
the rule and the interface was not keeping it: *"Monospace is for data —
identifiers, paths, durations, sizes, rates — and never for prose that happens
to be technical. A sentence set in the monospace role is a sentence wearing a
costume."* Every `.numeric` was read against that rule. 212 call sites became
115. What lost the face:

- **Sentences.** Every error message, every hint under a field, every empty
  state, every explanation.
- **Proper nouns.** Service names in Settings — `slskd`, `Spotify`,
  `Navidrome` — and the account they are connected as. An artist name, an album
  title, a track title. A name is not an identifier.
- **Words that are not values.** `seconds` beside a number box. The legend under
  an artist's albums. A chip whose content is a state, not a figure.

What kept it: paths, URLs, environment variable names, recording IDs, ISRCs,
barcodes, durations, byte counts, bitrates, positions, dates in a column, bare
figures, and the keycaps that name a keyboard shortcut. Where a sentence
contains one identifier, the sentence is set in the body face and only the
identifier is wrapped — `completed downloads are read from <path>`.

**The four conditions from 0017 stand on their own merits.** They were written
as the price of the width and they are not repealed with it. A path that
ellipsises on the right still hides the only part that identifies the file. An
identifier shown short still needs a way to copy the whole of it. The review
candidate row still has to state what differs rather than depend on width. Each
is now an ordinary defect rather than a debt against a typeface.

## Why

**A monospace is a labelling device, and a label that shouts stops labelling.**
What the mono is for is telling the reader "this is a value, not a sentence" —
that a string is something the machine produced and can be compared, copied or
searched for. It does that by being visibly different from the text around it.
Being *wider* is a bad way to be different, because width is the same signal
weight uses, and weight means importance. The reader gets told that a byte count
matters more than the sentence explaining it.

**0017 was right about the role and wrong about the face, and the difference is
worth keeping.** Before 0017 the monospace was `ui-monospace, SFMono-Regular,
Menlo, monospace` written out four times, which meant every machine drew Schall
differently and Schall had never chosen. That is fixed and stays fixed. Only the
name in the token changes.

**Narrow is what this product needs from a monospace.** Schall shows its
working, and the working is long: a recording ID is 36 characters and no edit
can shorten it, a library path is longer, and both appear inside table rows next
to everything else a row has to carry. The face that fits more of an identifier
into a row is the face that lets the row keep it. That is the argument 0017 set
aside and it did not stop being true.

**The cut is worth more than the swap.** A narrower face makes every string 14%
shorter. Taking the face off 97 pieces of prose makes those strings ordinary
text, which is what they always were. The second change is the one that stops
Settings reading like a terminal, and it would have been worth doing even if the
face had stayed.

## The check

Open Settings on a 390px screen. Every service name in System health reads on
one line. Then read down the same page: the only monospace left on it is a path,
a URL, a key, a size, or a figure — no sentence anywhere is set in it.

## What was rejected

- **Keeping Martian Mono and cutting the prose only.** This was the plan 0017
  wrote and it is half the fix. It would leave `slskd` and every recording ID
  17% wider than they need to be, in the rows where width is scarcest, for a
  character the owner has now looked at on every screen and does not want.
- **JetBrains Mono, Geist Mono, DM Mono.** All three set at 0.6em and would
  solve the width identically. IBM Plex Mono is the quietest of them: a smaller
  x-height and less character in the terminals, which is what the role wants.
  This is a preference, and it is recorded as one.
- **Dropping the monospace role and using tabular figures in the body face.**
  The quietest option available, and it gives up the labelling. A path set in
  Schibsted Grotesk is a path that looks like a sentence, which is the thing the
  role exists to prevent.
- **Shrinking the mono a step below the text beside it.** A real optical
  correction — a monospace reads larger than a proportional face at equal size.
  It is refused because 0017's twelve pixel rule is the floor for anything that
  carries meaning, `--text-meta` is already 12px, and a step down from there
  puts recording IDs under it.
