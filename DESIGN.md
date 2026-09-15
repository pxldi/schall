---
name: Schall
description: A self-hosted music collection manager that shows its working and never guesses.
colors:
  inset: "#08060a"
  ground: "#0e0b10"
  surface-thin: "#1a1420"
  surface-regular: "#241a27"
  surface-thick: "#2d212f"
  surface-ultra: "#362838"
  line-thin: "#3d2f42"
  line-inset: "#17121b"
  line-regular: "#4a3a50"
  line-thick: "#5c4864"
  line-live: "#7a6280"
  shell-line: "#2c2233"
  accent: "#bfa3e8"
  accent-soft: "#d9c8f0"
  accent-ink: "#2a1c3d"
  ok: "#7fb387"
  ok-border: "#3d5647"
  idle: "#a59da7"
  decide: "#a3b7e8"
  busy: "#7dd3fc"
  fail: "#da8a7c"
  fail-border: "#5c3733"
  done: "#5eead4"
  ink: "#ece5ea"
  ink-2: "#bcb2b8"
  ink-3: "#aaa0aa"
  ink-4: "#9b919b"
  ink-muted-plum: "#a396a0"
typography:
  dense-micro:
    fontFamily: "Schibsted Grotesk, 'Avenir Next', 'Segoe UI', sans-serif"
    fontSize: "0.6875rem"
    fontWeight: 600
    lineHeight: 1.3
    letterSpacing: "0.1em"
  dense-meta:
    fontFamily: "Schibsted Grotesk, 'Avenir Next', 'Segoe UI', sans-serif"
    fontSize: "0.75rem"
    fontWeight: 400
    lineHeight: 1.35
    letterSpacing: "0"
  dense-body:
    fontFamily: "Schibsted Grotesk, 'Avenir Next', 'Segoe UI', sans-serif"
    fontSize: "0.8125rem"
    fontWeight: 400
    lineHeight: 1.45
    letterSpacing: "0"
  dense-lead:
    fontFamily: "Schibsted Grotesk, 'Avenir Next', 'Segoe UI', sans-serif"
    fontSize: "1.0625rem"
    fontWeight: 600
    lineHeight: 1.3
    letterSpacing: "-0.01em"
  dense-display:
    fontFamily: "Schibsted Grotesk, 'Avenir Next', 'Segoe UI', sans-serif"
    fontSize: "1.5rem"
    fontWeight: 700
    lineHeight: 1.1
    letterSpacing: "-0.02em"
  quiet-meta:
    fontFamily: "Schibsted Grotesk, 'Avenir Next', 'Segoe UI', sans-serif"
    fontSize: "0.8125rem"
    fontWeight: 400
    lineHeight: 1.6
    letterSpacing: "0"
  quiet-body:
    fontFamily: "Schibsted Grotesk, 'Avenir Next', 'Segoe UI', sans-serif"
    fontSize: "0.9375rem"
    fontWeight: 400
    lineHeight: 1.65
    letterSpacing: "0"
  quiet-lead:
    fontFamily: "Schibsted Grotesk, 'Avenir Next', 'Segoe UI', sans-serif"
    fontSize: "1.1875rem"
    fontWeight: 600
    lineHeight: 1.4
    letterSpacing: "-0.01em"
  quiet-display:
    fontFamily: "Schibsted Grotesk, 'Avenir Next', 'Segoe UI', sans-serif"
    fontSize: "1.625rem"
    fontWeight: 700
    lineHeight: 1.15
    letterSpacing: "-0.025em"
  data:
    fontFamily: "Azeret Mono, ui-monospace, SFMono-Regular, Menlo, monospace"
    fontSize: "0.9375em"
    fontWeight: 400
rounded:
  tight: "4px"
  row: "6px"
  control: "8px"
  panel: "12px"
  card: "16px"
spacing:
  row: "0.625rem"
  card: "1rem"
  page: "1.5rem"
components:
  button-primary:
    backgroundColor: "{colors.accent}"
    textColor: "{colors.accent-ink}"
    rounded: "{rounded.control}"
    padding: "0 0.75rem"
    height: "2rem"
    typography: "{typography.dense-body}"
  button-primary-hover:
    backgroundColor: "{colors.accent-soft}"
  button-outline:
    textColor: "{colors.ink}"
    rounded: "{rounded.control}"
    padding: "0 0.75rem"
    height: "2rem"
    typography: "{typography.dense-body}"
  button-danger:
    backgroundColor: "rgb(205 95 76 / 0.14)"
    textColor: "{colors.fail}"
    rounded: "{rounded.control}"
    padding: "0 0.75rem"
    height: "2rem"
    typography: "{typography.dense-body}"
  button-ghost:
    textColor: "{colors.ink-2}"
    rounded: "{rounded.control}"
    padding: "0 0.75rem"
    height: "2rem"
    typography: "{typography.dense-body}"
  field:
    backgroundColor: "{colors.inset}"
    textColor: "{colors.ink}"
    rounded: "{rounded.control}"
    padding: "0 0.625rem"
    height: "2.125rem"
    typography: "{typography.data}"
  nav-item:
    textColor: "{colors.ink-2}"
    padding: "0 0.125rem"
    typography: "{typography.dense-body}"
  nav-item-active:
    textColor: "{colors.ink}"
    borderColor: "{colors.accent}"
---

# Design System: Schall

## Overview

**Creative North Star: "Die Blende"**

Schall looks after music somebody owns. That is the whole argument the interface
makes, and the type makes it with two voices: a grotesque for everything that is
read — headings included, where weight and size carry the hierarchy — and a
monospace for the card index underneath. A serif used to sit on the headings,
the sleeve over the catalogue; it read as a third voice doing one job the
grotesque could already do, and it retired in docs/decisions/0022. A third
voice, Syne 800, was added in the Schall rebrand and carries only the
wordmark — it is not a reading face and answers to nothing under Typography's
Hierarchy. Rented music arrives in somebody else's typography. Owned music is
catalogued in your own.

It is a librarian's room lit one way: a stage light coming up on the working
end of a rail, "Die Blende" — the iris, the light that opens on one edge of a
dark room and leaves the rest of it alone. The ground is near-black and stays
there; the plum that answers it lives in the cards and panels, never in the
page; the mast's one gradient is the only place light crosses the frame at
all. The lilac accent is used about as often as a coloured tab on a drawer, and
nothing else is decorated for the sake of looking designed. Density is real
density — this is an application whose main screens are lists of thousands of
rows, and a row that spends its height on ornament is a row that shows less
music. Where the interface does spend room, it spends it on evidence: the
review screen is the widest, quietest, most generously set thing in the product,
because it is where a person decides something permanent. It has a type scale of
its own for that reason (docs/decisions/0023).

A stopped want whose copy is already a library file under a different
recording draws the same card as an ordinary downloaded copy, headed "In your
library" with the file it is filed as beside it. Where the file's credit or
title differs from what was wanted, the card adds a Credit or Title row in
fail red; nothing else on it names the disagreement. Accepting files the copy
as the wanted recording in place. A stopped want held up by nothing more than
a disagreement on artist credit draws as an ordinary grid of copies instead —
there is still a copy to choose between.

The system is flat. There is not one box-shadow in the codebase, and that is a
decision rather than an omission: depth is made from two things and nothing
else — a card or panel is drawn in the plum surface colour rather than the
ground, and a hairline separates the two. Anything that needs to feel closer
gets a lighter step of the same ladder, never a shadow. Glass — a blurred,
translucent fill — is spent only on a surface that floats above the page: the
one modal, a menu, the preview bar. It never reaches a card or a table row.
There is one surface below the page as well — the inset, `#08060a` — because a
field is a well and not a raise (docs/decisions/0023).

Destructive actions keep their actor in the removal record. The duplicates
screen records the person at the screen. The unattended duplicate-resolution
sweep records the sweep. The weekly removal pass checks its lease again before
it unlinks audio.

**Key Characteristics:**

- Near-black ground (`#0e0b10`), never pure black, with the plum surface colour
  (`#241a27`) reserved for cards and panels and one step below the page
  (`#08060a`) for the things something goes into.
- Three typefaces, one job each: a grotesque for everything read, headings
  included; a monospace for every identifier, label and figure; a display face
  that draws the wordmark alone.
- One accent, used as chrome and never as meaning.
- Six state colours, each of which must appear beside a glyph.
- No shadows. Depth is tonal, and glass is spent only on a floating surface.
- One gradient in the whole application, in the mast.
- Two type scales — `dense` for a screen that is scanned, `quiet` for one
  that is read — and nothing between their steps.

## Colors

A warm near-black ground with a plum surface for cards, a single lilac accent
and five plain state colours; the greys lean warm rather than blue, so the page
reads closer to a lit stage than to a terminal.

### Primary

- **Accent** (`#bfa3e8`): The one accent. It fills the wordmark's tone, the
  active room in the mast, the single primary button on a screen, and the
  focus ring. Nowhere else. Lilac replaced ember in the Schall rebrand and
  inherits the Chrome Rule exactly.
- **Accent Soft** (`#d9c8f0`): The lighter form, used for the accent on a
  hover, where a fill at full strength would be louder than the row beneath it.
- **Accent Ink** (`#2a1c3d`): The text drawn on top of an accent fill. Dark
  plum rather than black, so a filled button reads as one object of the
  palette rather than a hole cut in it.

### Tertiary

The six roles that carry every state the API reports. They are deliberately
plain and pale — a status is information and not decoration. `ok` and `fail`
are drawn in the palette's own agrees/objects hues, the words a matching
decision is argued in, and each carries a border a shade darker for the
evidence table's own cells.

- **Ok** (`#7fb387`, border `#3d5647`): Something is complete or verified, or
  audio agrees with a claim. Owned releases, finished transfers, confirmed
  matches.
- **Idle** (`#a59da7`): Nothing is happening and nothing is wrong. The resting
  state, drawn as ordinary secondary text rather than as a colour of its own —
  "idle" is the absence of a state, and it is close to the palette's own muted
  ink. Lightened from `#8d8390` on 2026-09-07: the tint a chip draws it in,
  14% over Surface Regular, measured 3.84:1, under the 4.5:1 floor.
- **Decide** (`#a3b7e8`): Something is waiting on a person. The review queue and
  everything that feeds it.
- **Busy** (`#7dd3fc`): Work is in flight. Active downloads, running jobs.
- **Fail** (`#da8a7c`, border `#5c3733`): Something stopped and will not resume
  by itself, or audio contradicts a claim. Lightened from `#cd5f4c` the same
  day, for the same reason: its own tint measured 3.62:1.
- **Done** (`#5eead4`): Something is finished rather than good. A manual
  decision, a resolved conflict, a merged recording: none of the three is a
  success and none is still running, and Ok says the wrong word about all of
  them.

### Neutral

- **Ground** (`#0e0b10`): The page, everywhere, in one value, and never a
  gradient — the one gradient in the application belongs to the mast alone.
- **Surface Regular** (`#241a27`): The plum. Cards and raised panels are drawn
  in this colour outright rather than as a wash over the ground, which is the
  one thing the Bühne palette changes about how a surface is made.
- **Surface Thin / Thick / Ultra** (`#1a1420`, `#2d212f`, `#362838`): The rest
  of the same ladder — thin for the barely-there (a chip, a rail chrome
  element), thick for hover, ultra for selected — each a step lighter than the
  plum before it, never a wash of white.
- **Inset** (`#08060a`): The one surface under the ground. A field, a checkbox,
  the cycling placeholder's box, the Overview's panels
  are drawn on it, because each is a place something goes into rather than a
  thing standing on the page.
- **Ink** (`#ece5ea`): Primary text. Titles, values, anything a person is
  reading rather than skimming past.
- **Ink 2 / 3 / 4** (`#bcb2b8`, `#aaa0aa`, `#9b919b`): Three steps down from
  Ink, from a line of equal importance beside a title down to the quietest
  count in the product. The lower two steps clear 4.5:1 against every surface
  they are used on, including Surface Ultra.
- **Ink Muted Plum** (`#a396a0`): The palette's own answer for a muted line
  read against the plum rather than the ground — the surface the ink ramp's
  regular steps were not tuned against.

### Named Rules

**The Chrome Rule.** The accent is chrome and never state. It marks where you
are and what the screen's one action is; it never means "good", "urgent",
"selected" or "changed". Any reading of this interface that depends on knowing
which purple means what is a reading the design has failed to support — which
is also why `decide` is drawn in a cooler, bluer violet than the warm lilac
accent, so the two are never read as the same colour meaning two things.

**The Glyph Rule.** No state is identifiable by hue alone. Every one of the six
state colours appears beside a glyph or a word wherever it is used, so the
interface survives being read by somebody who cannot tell the six apart.

**The Readable Floor Rule.** Every ink that carries words clears 4.5:1 against
the colour behind it, not merely against the ground. Anything quieter than Ink 4
is a border or a disabled glyph, carries no words, and is not held to it.

**The Glass Budget Rule.** Glass — a translucent fill over a blur — is spent on
exactly the surfaces that float above the page: the one modal, a menu, the
preview bar. `rgba(32,34,39,0.55)` behind a `rgba(232,233,231,0.09)` hairline
and a 14px blur, and that is the whole budget. A card or a table row is drawn
flat, in the plum, with no blur under it at all.

## Typography

**Display and Body Font:** Schibsted Grotesk (with Avenir Next, Segoe UI)
**Label/Mono Font:** Azeret Mono (with ui-monospace, SFMono-Regular, Menlo)
**Wordmark Font:** Syne 800, lowercase, nowhere else

**Root size:** 133%. The owner reads the product at 133% browser zoom and
asked for that as the default (2026-09-01). Every size and space in the
application is rem-based, so the one declaration on `html` scales all of it
together. The px names in this file and in the stylesheet's comments are the
design-time values at a 16px root; on screen each draws a third larger.

**Character:** A newsroom grotesque over a quiet monospace, with one display
face reserved for a signature. The grotesque has more character in its
terminals than the default web sans without asking anything of a reader going
through a thousand rows, and at display size it is the same voice speaking
louder — weight and size make the hierarchy, never a change of face. The
monospace stays out of the way, because it draws the card index and not the
argument. Syne 800 is heavier and rounder than either, and it draws exactly one
word, lowercase, wherever the wordmark appears — it never carries a heading, a
label or a figure. The mark leans 10° forward with two copies trailing behind
it at 30% and 14% of its own colour, a sound source moving past the listener;
the trails are em-sized, so the treatment survives any size the mark is drawn
at. A serif (Newsreader) used to carry the headings;
docs/decisions/0022 records why it left. All three faces are self-hosted; a
page load never reaches a font service.

The monospace was Martian Mono, which is 16.7% wider and was chosen knowing it
(docs/decisions/0017). Seen in place, across every screen, the width read as
emphasis on the least important text in the product. It became IBM Plex Mono
(0018 records why) and is Azeret Mono now, replaced in the Schall rebrand along
with the accent and the ground — the reasoning that put a plain, quiet
monospace under every figure and identifier did not change, only which
typeface answers to it.

### Hierarchy

There are two scales, not one, because one cannot serve both readers
(docs/decisions/0023). **`dense`** draws the library, the queue, every table and
all chrome — a thousand-row table is scanned, and the eye jumping down a column
wants the rows close together. **`quiet`** draws the review screen, its evidence,
and anything read rather than scanned — the eye running along a line wants air
around it. Every step carries its own line height and tracking on the size token
itself, so a call site asking for `text-dense-body` gets all three and cannot
take the size without the other two.

The dense scale:

- **`dense-display`** (700, 1.5rem, 1.1 line-height, -0.02em): Page headings, the
  wordmark, and the large figures on stat cards. Schibsted Grotesk — the display
  role keeps its own token and tracking although it names the body face now.
- **`dense-lead`** (600, 1.0625rem): The lead line of a panel or a card, and the
  largest size any body text reaches. It was 0.9375rem, one step over body,
  which put it inside two steps of the 0.75rem chip beside it — and a tinted
  chip beats plain text it is nearly the size of, so "Connected" was reading
  louder than the word "slskd" it reported on.
- **`dense-body`** (400–500, 0.8125rem): Table cells, row titles, controls.
- **`dense-meta`** (400, 0.75rem): The second line of a row, timestamps, counts,
  everything that explains the line above it.
- **`dense-micro`** (600, 0.6875rem, 0.1em tracking, uppercase): Column headings
  and section markers. Ink 3.

Column headings keep the dense-micro uppercase treatment. Tables use the
header's bottom hairline and no vertical divider rules, so the data reads as
rows instead of a grid.

The quiet scale, every step one notch larger and set looser than its dense
counterpart:

- **`quiet-display`** (700, 1.625rem, 1.15, -0.025em)
- **`quiet-lead`** (600, 1.1875rem, 1.4, -0.01em)
- **`quiet-body`** (400, 0.9375rem, 1.65)
- **`quiet-meta`** (400, 0.8125rem, 1.6)

And across both:

- **Data** (400, 0.9375em of whatever it sits in, tabular figures): Every
  identifier, path, label, chip and figure. Azeret Mono. Three static weights —
  400, 500 and 600 — self-hosted alongside an italic cut of 400 that no call
  site asks for yet; there is no variable release, and `font-synthesis: none`
  means a weight or a slant nothing here asked for is drawn at 400 upright
  rather than faked.

The four names the application wrote before the scales existed — `micro`,
`meta`, `body`, `lead` — still resolve, aliased to the dense scale, so no screen
changed when the second scale arrived. They carry the size and nothing else: a
call site written before the split sets its own leading beside the size, and
giving the aliases a line height would have changed every one of those screens
at once.

### Named Rules

**The Two Scales Rule.** A screen picks a scale before it picks a size, and it
picks it by how the screen is used: scanned takes `dense`, read takes `quiet`.
Mixing the two on one screen is drift, and so is a call site that writes its own
pixel value — there were once 507 such call sites across ten values.

**The Step Down Rule.** The monospace is set one step under the text around it
— `0.9375em`, relative rather than absolute, so it follows whatever size it was
dropped into. This is not a correction for a face that is too big. Measured
against the grotesque at 100px the two are the same size: x-heights of 52 and
53, digits both advancing 60. What differs is that every character in a
monospace takes a digit's width, so `1 of 1 · 100%` sets far wider than the
artist name above it while claiming the same line, and a `1` sitting in a `0`'s
box is what makes a figure look shouted. One step down is the ordinary answer
wherever a monospace is mixed into a sans, and it costs nothing: tabular
figures line up with each other, which was never about lining up with the prose.

The one exception is a headline figure on a stat card. It asks for tabular
figures so it does not reflow as it counts, takes the display face and the
display size, and is the single place the monospace never applies — so the step
down is written to hold off it.

**The Twelve Pixel Rule.** Nothing that carries meaning is smaller than 12px.
The 11px step exists for uppercase labels alone, and a label only ever repeats,
in full and in an ordinary size, something shown nearby.

**The Costume Rule.** Monospace is for data — identifiers, paths, durations,
sizes, rates — and never for prose that happens to be technical. A sentence set
in the monospace role is a sentence wearing a costume, and it is also how this
interface has twice overflowed a phone.

**The One Vocabulary Rule.** Two questions, and a screen may never invent a
sixth word for either. `holding` — Complete, Partial, Missing — answers how much
of a thing the library has, and does not change on its own. `progress` —
Searching, Wanted, Queued at peer, Resolving, Importing, Imported, Needs review,
Discarded, Dismissed, Failed — answers what is happening to it, and does. Both
live in `web/src/lib/vocabulary.ts` and nothing writes either word out by hand.
There were five sets of them once: Artists said `Incomplete`, an artist's
releases said `owned`, playlists said `owned` again for a different scope,
Library said `Owned`, and Downloads said `Not used` — every one reasonable on its
own screen, and together four dialects for one question.

## Layout

One 13rem mast (208px at a 16px root) down the left edge of every screen — the seven destinations
as icon-and-name rows from the top, search below them, and the wordmark with
the build's date and commit at the foot — beside one full-width scrolling
column of content. There is no boot splash; a cold load paints the ground
until the bundle runs. There is no top
bar, no readout and no per-room mark: what is waiting is read on the
pages themselves: Review's rail chips carry the counts. Below the breakpoint five tabs move to a
fixed bar along the bottom of the screen, with Playlists, Downloads, Library and Settings in More.

The Overview draws nine panels from one read (`GET /api/v1/overview?tz=`), counted
in the browser's zone. Listens is the anchor: the 30-day total as the main
figure, a plain-word line against the 30 days before, and the daily bar chart.
Most played sits beside it, and both keep the raised Surface Regular fill; the
other seven — When you listen (a Monday-first week of hours), Top artists, Top
albums, Recently played, Recently added, Downloaded (14 days) and Library (files,
size, a year of growth, room left) — sit on Inset and read quieter. The grid
runs listening content first and housekeeping last: Downloaded and Library close
it out, two columns each, and the grid keeps the full width. A list
panel fills the height of the row it sits in, so the chart beside it sets that
height; it shows at least four rows and hides its scrollbar. While there is
more below, the panel's colour fades up over the last visible row, and the
fade goes when the last row is in view. Most played carries no mark on a held
song; the count is a fixed right column and "Want" sits before it on the rows
that can be wanted. The heatmap's cells are square. There
is a visually hidden h1 ("Overview"), no stat well and no waiting list: the
mast says where you are, and what is waiting is read in Review and Library.
Every chart mark carries a
tooltip drawn above it; lists carry none. The six listening panels say "No
listens yet" until the listening history has been read. A stat panel shows
one figure and, for Listens, the change against the thirty days before; no
sentence sits under it. A list panel shows every row the read returned, up
to 25, and scrolls inside the panel past the ones that fit. Each of the three
charts is decorative to a screen reader — the SVGs and the heatmap's
coloured cells are `aria-hidden`. The heatmap is one `role="group"` tab
stop, arrow keys move a highlighted cell and announce it through a live
region, and a tap does the same on touch.

Screens are one of two shapes. A **list screen** is a header band, a filter band,
then hairline-separated rows — never a stack of cards, because a hundred cards
is a hundred borders. A **decision screen** is a queue rail on the left, the
thing being decided in the middle, and a sticky answer bar at the bottom that a
decision is never made from behind. It takes the `quiet` type scale, and it is
the only shape in the product that does. Review is the one screen built this
way: the rail opens with filter chips (All, Downloaded, Version, Folder, each
counted in mono), then the questions themselves, then — on a wide screen — one
volume slider for every preview. The rail carries no page header and no
question label above the thing being decided; the mast's own highlight already
says where the reader is. The answer bar is buttons only, right-aligned, in a
fixed order per kind — Skip, Remove from Wishlist, then whatever settles the
question — with no hotkey chips drawn on any of them.

A **detail screen** opens on one thing reached from a list: a `BackLink`
(an icon only, aria-labelled) at the top, then a 26px/600 title
(`text-quiet-display font-semibold text-ink`) beside whatever picture or cover
the thing has. No page header wraps either of them. The artist page is the
first one built this way: the name, a fact line, genre tags and the follow
control sit beside a 96px round picture, then the same list-screen shape —
a filter band, then rows or cards — carries the thing's own contents.

**Settings** is a fifth shape, built around a 9.375rem section rail (150px at
a 16px root) rather than a filter band: Sources, Library, Automation and
Jobs, inside the main column, no page header. Matching folded into Library —
it held one card. Content stops at 760px on Sources, Library and Automation.
Sources opens with status rows (a dot, an 8.125rem name, a coloured state
word, a dim detail — "checked 2 min ago"),
then one form per source: label 130px, a 340px field, dim help beside it
rather than under the label, toggles as a checkbox with a word beside it,
and Test / Save (Save is the one Accent button here) indented under the field
column. Jobs runs at the rail's own width instead, uncapped: "In flight" with
lane totals in the dim right text, a table read by `StateTag` rather than
`Chip`, "Failed for good" (renamed from "Terminal failures") with the
retention period in the dim right text, and the sweep pulse. Below `lg` the
rail moves above the content as a scrollable row of the same links, the
content takes the full width, and the two-column form grid collapses to one.

When a list has an active filter, its filter band ends with a plain text `Clear
filters` button. The button resets every filter and disappears at the default
state.

**Desktop width policy.** The shared `layout-width` container caps tables and
split-pane workspaces at 1400px and centers them in the available content area.
Playlists, Downloads and Library use it for their tables or list content.
Artist grids and the artist hero stay full-bleed. The container has no effect on
phone layouts. Review's rail and card grid do not use it: the card grid caps
itself at 64rem and left-aligns, so a want with one copy and a want with three
read alike, and two cards sit side by side well before the shared cap would
let them. Settings does not use it either: its own section rail and 760px
content cap replace it, and Jobs runs at whatever width the rail leaves rather
than the shared 1400px.

Page padding is 1.5rem, card padding 1rem, and the gap inside a row 0.625rem.
Tight groups, generous separation: the space above a heading is always larger
than the space below it.

**The Phone Gutter Rule.** A page-level container's horizontal padding is
`px-4 sm:px-6 lg:px-8`: 21px on a phone, 32px from `sm` up, unchanged from
before. `ControlRail` and `PageHeader` carry the same three steps. A dialog or
a card keeps its own padding regardless — the rule is for the box the page
itself sits in, not for what floats or sits above it.

The Uploads resolution pass labels a set-aside file Set aside. Library shows
Return to review on that row and can be filtered to the set-aside files
directly (`/library?view=files&setAside=true`).

Library adds an Unidentified filter for present files with unresolved identity
or ambiguous matching. Decide opens the existing identity or matching panel in the row.
A Library link with a file id opens the Files view with that row expanded.

Density is deliberate and touch is handled by the pointer rather than by the
width of the window. Where `pointer: coarse`, controls that sit alone on a row
grow to 44px and controls inside a line of text keep their drawn size and take
an invisible 44px box, so a phone gets bigger targets without a laptop losing
its density. Safe-area insets are added at the left of the mast and the
bottom of the phone bar and the answer bar.

The Jobs page's Sweeps section is a growing list, not a fixed panel. Each row
keeps the pass name, its work, and its last and next times in the desktop grid;
on narrow screens the same fields wrap into the page's single scrolling column.

Overview list rows are 40px (48px under a 40px cover in Top albums): a 32px
cover, title over artist, a mono count at the right. Cover frames sit in a
fixed-size, bordered, labelled box, so a cover that fails to load keeps its
place instead of collapsing the row.

## Elevation & Depth

**There are no shadows.** Not one `box-shadow` exists in the application, and
this is the system's clearest single decision. Depth in the page is made from
two colours and a hairline: the ground and the plum surface colour, never a
wash of one over the other. Depth above the page — a modal, a menu, the
preview bar — is made from glass instead, and nowhere else.

The ladder is ten steps: one below the page, the page, four surfaces above it
and five lines. They are tokens rather than a description — the frontmatter
carries every one — and the application uses these ten and nothing between
them.

The steps above the ground are **named rather than numbered**, because a name
says what a step is for and a number does not.

- **Inset** — `#08060a`, the one step below the page. A field, a checkbox, the
  cycling placeholder's box, the Overview's nine panels: the things something
  goes into.
- **Ground** — `#0e0b10`, the page.
- **Surface Thin** — `#1a1420`, the mast's own chrome, a chip, anything that
  is barely a surface.
- **Surface Regular** — `#241a27`, the plum. A card or a raised panel. The one
  a reader meets most, and the one surface named in the Bühne palette itself.
  Not an Overview panel: all nine sit on Inset.
- **Surface Thick** — `#2d212f`, a row or a ghost control under the pointer.
- **Surface Ultra** — `#362838`, a row, a segment or a control that is chosen,
  and the track a progress bar is drawn in. It sits above hover, because a thing
  under the pointer and a thing chosen are different states and are frequently
  both on screen at once.
- **Line Thin** — `#3d2f42`, the card line: the hairline between rows, and the
  edge of a card. By far the most used value in the application. A table's
  outer frame keeps it even where its own rows do not.
- **Line Inset** — `#17121b`, the hairline between rows inside an inset well,
  and the row separator inside a table (`Table.Row`) — sixty of them in one
  glance read as a texture on Line Thin, not sixty borders.
- **Line Regular** — `#4a3a50`, a border that has to be seen: a field, an
  outline button.
- **Line Thick** — `#5c4864`, the border of something chosen, and a border
  under the pointer.
- **Line Live** — `#7a6280`, the top of the ladder, for a border that has to
  read as deliberate at a small size: the checkbox answering a hover, and the
  dashed outline standing in for a release the library does not hold.
- **Shell Line** — `#2c2233`, the shell/rail line, held apart from the card
  line above it. It is the mast's own border and the phone bar's, and it
  never appears on a card.

Blur exists in exactly two budgets, held apart on purpose. The one gradient in
the application, "Die Blende", is a flat colour transition and carries no
blur at all — it lives in the mast only, black easing into plum top to
bottom. Glass — a translucent fill over a blur — is spent on the surfaces that
float above the page: the one modal, a menu, the preview bar. A card or a table
row is never glass; it is drawn flat, in the plum.

### Named Rules

**The Flat Rule.** A surface in the page never floats. If something needs to
feel closer, give it a lighter step of the surface ladder or a stronger
hairline; never a shadow, and never a glow. A surface floating above the page
is the one place glass is spent instead.

**The Hairline Rule.** Rows in a list are separated by a single hairline, not
enclosed in borders. A list is one object, not a stack of cards. Nested cards
are always wrong.

**The Surface Rule.** The page is Ground. A container that only groups things —
a settings section, a form group, a list frame, an empty-state panel, an
evidence table frame — carries a hairline and no fill. A container that is the
current working surface — the selected copy card, the two lead Overview
panels, an open dialog or sheet, the command palette, a menu — sits on Surface
Regular. Surface Thick is a hover, or the chosen value inside a filter group.
Surface Ultra draws nothing on the page; it is reserved above Thick and does
not appear. A settings card lost its plum wash under this rule (2026-09-07):
grouping sixteen settings into cards had put every one of them on the same
footing as an open dialog, which none of them is.

**The Two Bands Rule.** The first band of a page says what the page is: the way
back, the picture, the name, one line of context, and the actions pushed right.
The second band — `ControlRail` — holds everything the reader can turn: search,
scope, filters, sort. Nothing crosses between them. This is not tidiness. A name
set in the display face aligns on its baseline and a field is a 34px box that
aligns on its edges, so a line holding both has nothing for either to line up
with; Artists put a heading, a search field and a select on one line and none of
the three agreed with the others. One rail per page sticks, and it is the outer
one: two rails pinned to the same edge are drawn on top of each other.

When a control row is wider than a phone, it scrolls horizontally. The clipped
edge fades into the rail and the scrollbar stays hidden.

**The Rem Box Rule.** `html` sets `font-size: 133%`, so every rem-based size in
the application grows with it and a pixel literal does not. A box sized in
px next to text sized in rem drifts apart as the root grows: a pill fixed at
26px holding 17px text, a skeleton row fixed at 53px standing in for a real
row that has grown past it. Any box that holds text — a control's height, a
column's width, a placeholder's reserved room — is sized in rem (its design
px value divided by 16), so it grows with the text inside it. Px stays for
marks under 20px (dots, bars, the 1px hairline) and for `layout-width`'s
1400px cap, which bounds a reading measure and is not holding a piece of
text.

## Shapes

Corners are geometry, not text, so every radius is a px value (`--radius-*` in
`web/src/styles.css`) and none of them grows with the Rem Box Rule's 133% root.
Five roles, one token each, and a call site names the role rather than the
pixel value:

- **Tight** (`--radius-tight`, 4px) — a compact 24px target: the checkbox, a
  radio-shaped mark that stays a mark rather than a circle.
- **Row** (`--radius-row`, 6px) — a cover thumbnail, a chip or mark that sits
  inside a row, an inline figure.
- **Control** (`--radius-control`, 8px) — a button, a field, a select, a menu
  item, an icon button: a control that stands on its own.
- **Panel** (`--radius-panel`, 12px) — a panel, a table or list frame, a
  dialog body, a sheet, a menu's own float.
- **Card** (`--radius-card`, 16px) — a copy card, an artist tile, a settings
  card, or another freestanding card.

`rounded-full` stays outside the five roles, for a pill (Segmented's tabs and
filter chips, PillSelect), a dot, a pip, an avatar, and a circular playback
control. An 8px radius on a 24px control is a proportion this system never
uses, which is what mixing Control into a checkbox's Tight target would draw.

Everything else is a rectangle. There are no angled cuts, no asymmetric
corners, no decorative shapes, and no illustration. The only non-rectangular
marks are the status dot, the radio, and the wordmark.

## Motion

Motion is any change on screen that takes time instead of happening between two
frames: a colour easing into another colour, a panel sliding in, a spinner
turning. Schall times all of it from six durations and three curves, held as
custom properties at the top of `web/src/styles.css`, and nothing in the
application writes a duration or a curve of its own.

The unit of the system is a **job**, not a number. A step without a stated job
is a step the next person works around by inventing a seventh, which is what had
already happened: this specification said there was one duration, and the code
shipped four durations and three curves.

### The durations

| Token | Value | Its job |
| --- | --- | --- |
| `state` | 150ms | A thing already on screen changing how it looks, in place: hover, focus, a row being selected, a border strengthening, a chevron turning over. Nothing travels and nothing arrives, so this is the shortest step. |
| `surface` | 200ms | A surface opening or closing over the page: the More sheet, a menu, the command palette, the one modal, a disclosure. |
| `enter` | 280ms | A thing arriving into the page or leaving it: a card, a message where a list would have been, a mark that has changed what it says, a figure that has changed. |
| `stagger` | 60ms | The gap between one arrival and the next, where a small fixed set arrives in order. |
| `ambient` | 1000ms | One turn of a loop that says work is still in flight. A spinner turns once per step; a skeleton breathes over two. |
| `read` | 3200ms | One beat of a loop a person reads rather than watches — one name in the cycling placeholder, which has to arrive, be read, and go. |

`surface` is shorter than `enter` on purpose, and the order is the argument for
the whole scale. A surface stands between the reader and the thing they just
asked for, so it has to be got out of the way; a card arriving is readable from
its first frame and is only settling into place, and nobody is waiting on it.
Distance is not what sets these numbers — how much of the reader's time the
motion is spending is.

`read` is a step of its own rather than a multiple of `ambient` because reading
speed sets it and the scale does not.

### The curves

| Token | Value | Its job |
| --- | --- | --- |
| `ease` | `cubic-bezier(0.4, 0, 0.2, 1)` | The default. Both ends shaped, for anything that changes in place and that the reader can reverse at any moment: a hover leaving is the same event as a hover arriving, so both ends are drawn the same. |
| `ease-arrive` | `cubic-bezier(0.2, 0, 0, 1)` | Quick off the mark and slow into place, for anything arriving or opening. A thing that arrives this way has shown the reader where it is going before it gets there. |
| `ease-loop` | `linear` | No shaping at all, for a loop. A loop has no beginning and no end to shape, and a spinner that eases reads as a spinner catching on something. |

`ease` is Tailwind's own default curve. Every bare `transition` utility in the
application — about forty of them — was already drawing on it, while the
stylesheet's own hand-written rules said `ease` and the wordmark said
`cubic-bezier(0.2, 0, 0, 1)`. Naming the Tailwind value settles that in favour of
what ships, and Tailwind's four motion variables are pointed at these tokens, so
a utility written at a call site obeys the scale without the call site saying so.

### The named motions

Five compositions, written once in the component layer of `web/src/styles.css`
so that a card arriving on the settings page and a message arriving on the
artists page are the same event. Each one moves `transform` and `opacity` and
nothing else.

- **`rise`** — something arriving: four pixels of travel and a fade, on `enter`.
- **`turn`** — a mark that has changed what it says: a turn over from 72%, on
  `enter`. Drawn on the change and never on the first draw.
- **`tick`** — a figure that has changed: three pixels down into its new value,
  on `enter`. Also only on the change.
- **`press`** — a control under a press: 3% under, on `state`. The only motion
  in the application a person makes happen directly.
- **`reveal`** — the panel a disclosure opens, on `surface`.

### Two exceptions, both of them written down

The wordmark's arrival was 340ms and its key strike 400ms. Both are now `enter`.
Nothing about either needed the extra time, and a signature sitting off the scale
is the first thing anybody copies off it. The strike is on the arriving step
rather than the state one because it is a round trip — the colour out and back is
two state changes end to end.

Every other duration in the application before this was 120ms, on the menu, the
sheet, the palette and the modal. All four are `surface` now, so every surface in
the product opens at one speed.

### Named Rules

**The One Scale Rule.** Six durations, three curves, and every one of them has a
stated job. No animation or transition in `web/src/` carries a literal duration
or a literal curve; each names a token. Nothing in CI enforces this — it is
enforced by there being nowhere left that a number needs to be written.

**The Still List Rule.** Motion marks a change, not a redraw. A list of a
thousand rows never staggers and never animates on a data refresh, because that
is nausea rather than craft; a mark or a figure animates only when the thing it
reports has actually become something else. On the review queue this is a
correctness rule and not a taste one: `docs/PRODUCT.md` treats queue speed as
part of the zero-false-positive guarantee, so nothing there may stand between a
key and a permanent decision. Motion in the queue may confirm what has already
happened and may do nothing else.

**The Held Box Rule.** A figure that has not arrived is given the room it will
take, in its own units, and left blank. Blank, because a nought written in place
of an unread count is not a placeholder — it is a wrong answer, and the reader
believes it for as long as the request takes. In its own units, because a box
measured once in pixels is a guess that goes stale: the filter strip's blank
pill was `16px` against the `6.75px` a single digit actually occupies, so every
count arriving pulled the whole strip nine pixels left. It is `2ch` now, which
inside the monospace is exactly two digits whatever the type scale does later,
and the figure that replaces it is given the same floor. Two digits is the
common case; a wider figure grows the pill once, which is a smaller lie than a
nought that turns into forty.

**The Arriving Picture Rule.** A cover or an artist photograph fades up when its
own bytes have arrived, over the arriving step, and does not move while it does
— the frame under it is already drawn at the right size and holds the hatched
square or the artist's initials, so the picture is filling a frame rather than
joining the page. This does not contradict the rule above: nothing is staggered
and nothing is delayed. The offsets between one cover and the next are the
network's own, which is the honest source of them, and is why the stagger step
stays reserved for the small fixed set it was written for. Drawn without it,
forty covers hit full opacity at forty unpredictable moments, which is what
flashing is.

When the image endpoint answers that no picture exists, the frame keeps its
initials or neutral music glyph. The browser remembers that failed source for
the current session, so a known absence is not requested again on a remount.

**The Settling Skeleton Rule.** A loading skeleton does not end between two
frames. Content that mounts at full opacity in the frame its skeleton leaves,
as the Overview's panels do, needs no `Settle`: there is no blank frame to
cover, and the grid cell `Settle` adds would break a chart's `grow` chain. `Settle` (`web/src/lib/components/Settle.svelte`) holds the skeleton
and the content in one grid cell: the content mounts on top with whatever
arrival its page gives it, and the skeleton fades out underneath over the
arriving step, so the pulsing boxes are still behind the first frames of the
real thing instead of a blank area. The fade duration comes from `motionMs`,
which returns 0 under reduced motion. A skeleton also fills the screen it is
on: the shell clips at the viewport, so the tile count only has to be big
enough for the largest screen, and a grid that runs out of placeholder rows
half way down reads as the page being smaller than it is.

**Reduced motion is the whole list.** Every motion named here is switched off
under `prefers-reduced-motion: reduce`, and the guard in `web/src/styles.css`
names each one by hand. A picture is switched to its finished state rather than
its first one: the reader gets the cut the fade replaced, never an empty frame. Two things stay: the state step, because a control that
changes colour over 150ms is answered rather than switched, and the spinner,
because it is not decoration — it is the only thing saying the work has not
stopped.

## Components

### Recommendation rows

- **Feedback controls:** Every recommendation row has paired `More like this`
  and `Less like this` buttons beside the acquisition and dismissal controls.
  Each label names the action. A pressed button gives the shared button press
  response, a pending request disables both feedback buttons, and an error is
  written in words beside the list.
- **Taste state:** Neither feedback control uses the accent as a taste state.
  The controls use a neutral outline in every state. The accent remains chrome
  under the Chrome Rule.
- **Keyboard and touch:** Both controls are native buttons, focusable in row
  order, and operable with Enter and Space. They use the global focus ring and
  the coarse-pointer target treatment from the button rules. The visible label
  stays one line.
- **Clear feedback:** Place `Clear feedback` beside the `What is this`
  disclosure in the recommendations header. The confirmation names the
  ListenBrainz scope, says `Clear all recommendation feedback?`, and says that
  the next sweep uses source ranking alone. Use the danger treatment for the
  confirming action and keep Cancel visible.
- **Reason line:** Keep the source reason and the feedback reason readable in
  the same row. Render the feedback reason as text, including
  `you asked for more like this` or `you asked for less like this`; hue cannot
  carry this distinction under the Glyph Rule.
- **Mobile:** Let the recording details occupy the first line. Put the paired
  feedback controls on a wrap-safe action line with Want it and Not interested,
  then give the reason line the full row width. The existing recommendation
  list widths must not force a control or reason fragment below the next row.

### Buttons

A variant says what the control is, never what state anything is in. There are
four, and no fifth.

- **Shape:** Gently rounded (8px at the two standing sizes, 6px for the in-row
  size), three heights only — 32px at `dense-body`, 28px and 24px at
  `dense-meta`.
- **Primary:** Accent fill with Accent Ink text, semibold. The one action the
  screen exists to offer, so exactly one control per view may wear it.
- **Quiet:** An action offered rather than urged. A Line Regular hairline over
  nothing, ink text, medium weight; the border strengthens to Line Thick on
  hover.
- **Danger:** An action that destroys something or cannot be undone. Tinted
  rather than filled — Fail at 14% behind Fail text with a Fail-at-40% border,
  going to 24% on hover — because the accent is the only colour in the product
  with a matching ink to fill against, and the accent may not mean "dangerous".
- **Ghost:** An action that is only a word. No box until it is hovered: Ink 2
  text, a Surface Thick wash and Ink text on hover.
- **Hover / Focus:** Focus is the global accent ring at 2px with a 2px offset,
  never restated per component. A press gives 3% under and comes back.
- **Icon:** Any variant drawn as a square of the same height holding one glyph,
  carrying the label a text button would have shown as its title and aria-label.

### Chips

- **Style:** A status colour at 14% opacity behind that colour's text, 6px
  radius, `6px / 2px` of padding, the words at 12px and regular weight, always
  preceded by a 5px dot or a glyph of the same role.
- **Weight:** A chip carries a tint, and a tint already wins against plain text.
  So the chip gives the weight back: it was medium in an `8px / 3px` box and
  read as the loudest thing in any row it appeared in, including rows where it
  sat beside the heading it was reporting on. The size does not go below 12px —
  a state nobody can read is not a quieter state, it is a missing one.
- **State:** A badge appears only while its destination is asking for something.
  There is no grey count badge — a plain total belongs in that page's own header
  where it has room to say what it counts.

### View chips (Segmented)

`Segmented.svelte` is a group of buttons, one selected, in three variants
picked by `variant`. All three keep the same DOM: `role="group"`, one button
per option with `aria-pressed`, a count in the option's own units next to its
name. A count still on its way shows no figure and no zero — `pending` holds
the placeholder width instead.

- **`chips`** (the default). The original pill row: 28px tall (`h-7`, the same
  height as `Button.svelte`'s `sm` size), 13px text, a 6px gap between pills.
  Each pill is its own Line Thin hairline, not a shared bar with a sliding
  highlight, and an inactive pill carries no fill of its own. The active pill
  takes an Accent border and Ink text; its count switches to Accent too, mono,
  while an inactive pill's count stays Ink 3.
- **`tabs`**, for switching what the page shows. No border and no pill: each
  option is 13px text in Ink 2, `gap-x-5` between them, 32px tall (`h-8`). The
  active option turns Ink text over a 2px Accent underline; the others sit on
  a transparent one so nothing shifts on selection. The count keeps the
  chips' rule — Accent when active, Ink 3 otherwise — because a tab may carry
  the accent: it marks where you are, the way the mast marks the active room.
- **`filter`**, for narrowing an already-chosen view. One pill-shaped
  container (`rounded-full border border-line-thin p-0.5`, 28px tall) holds
  every option; the active one gets a Surface Thick fill and Ink text, the
  others Ink 2. No border on the options themselves and no accent anywhere,
  including the count, because a selected filter value marks nothing but a
  value — it is not a place, so it may not wear the chrome that marks one.

### Pill select

`PillSelect.svelte` is a native `<select>`, for scope, sort, genre and
monitor level. `appearance: none` drops the browser's own arrow; a 10px
chevron is drawn in its place, in Ink 3. The element shows the chosen
option's own name, the way any select does — no separate label is drawn over
it. This is not `ui/select`, which is a 34px form field: a control this short
has no room for that control's trigger chrome.

Two looks, picked by the `quiet` prop:

- **Pill** (the default). The same 28px pill as a view chip: a Line Thin
  hairline, Ink 2 text, the chevron on the right.
- **Quiet**, for an ordering or a preference rather than a narrowing — sort
  and genre do not change what is in the list, only how it reads, so they
  carry no chrome of their own. No border: a `prefix` word ("Sort", "Genre")
  in Ink 4 stands where the pill's own border used to, then the select in
  Ink 2 with the same chevron. The `aria-label` still carries the full label
  ("How to sort artists"), because the prefix is a visual cue, not a
  replacement for it.

### Owned bar

`OwnedBar.svelte` is the 3px bar every board draws for "how much of this the
library holds": Accent fill on a Surface Thin track, 2px corners, a settable
width, and a mono "9 of 12" beside it. It never stands in for a percentage
alone. This is not `CompletenessBadge.svelte` — that one is a pill with the
same two figures inside it; `OwnedBar` draws the bar and the figure loose in
a row, for a table cell or a card footer that has no room for a pill.

A `compact` prop tightens the figure to "9/12" for a footer too narrow for
"9 of 12" plus a check mark on one line — the artist card uses it. The
aria-label keeps the long form ("9 of 12 releases in your library") in both
modes, since it is read aloud rather than fitted to a column.

### State tag

`StateTag.svelte` is the small hairline tag a table row draws for its own
state: 20px tall, 12px text, Surface Thin background, a Line Thin border,
4px corners, `6px` of side padding. It draws only for what an ownership count
cannot say — a failed refresh, one still importing, no track list, or tracks
dismissed by hand. A release that is simply partial or simply missing gets no
tag: the Owned column's bar already draws the same two figures, so the tag
would only repeat them. Four tones: neutral (default, Ink 2)
for a plain state, attention (Accent border and text) for "needs review" or
"unidentified", broken (Fail border and text) for "unreadable", and warn
(Decide border and text) for a data problem like "no track list" — the
boards call for a warn colour the stylesheet never grew, and Decide is the
closest existing role to it. This is not `Chip.svelte`: that one carries a
tint fill and a dot and reports a status role beside a heading or a card,
and a table column of tags sixty rows tall does not want that much colour.

### Cards / Containers

- **Corner Style:** 8px for Overview panels, 6px for list panels, 12–16px for
  cards.
- **Background:** Inset for every Overview panel and for list panels; Surface
  Regular for cards and raised panels. The Overview's two lead panels
  (Listens, Most played) differ only by their heading, Ink instead of Ink 3.
- **Shadow Strategy:** None. See Elevation & Depth.
- **Border:** One Line Thin for Overview panels, none for list panels; one Line Thin
  hairline for cards.
- **Internal Padding:** Overview panels use 14px 18px 16px. Cards use 1rem, and
  one value applies to every card on every screen. A list panel has none: its
  rows carry their own padding, so the inset hairline runs the full width.

### Inputs / Fields

- **Style:** One height everywhere — 34px, rising to 44px on a coarse pointer —
  with an 8px radius, a Line Regular hairline, an **Inset** fill, and the
  monospace face, because a field usually holds an address, a path or a key.
  The fill is darker than the page, not lighter: a field is a well, not a raise.
  It used to be a wash of white, so a field inside a card was that wash twice
  and its placeholder was read against the lightest surface in the product, at
  4.02:1. On the inset the same grey is 5.90:1 (docs/decisions/0023).
- **Focus:** The global accent ring. Nothing else moves.
- **Error / Disabled:** Disabled drops to 50% opacity. Errors are stated in
  words beside the field in the Fail role, never as a red border alone.
- **Read states:** A settings field has four: loading (disabled, no value
  yet), failed (disabled, the word "Unavailable" in the idle role, never a
  default), loaded-empty (today's wording — "not set", "never run") and
  loaded. Saving is disabled until the read succeeds, so a form cannot
  overwrite an unread value with a default.
- **Text size:** 16px on a coarse pointer, whatever the role says elsewhere,
  because Safari on iOS zooms the page in on any smaller field and does not
  zoom back out.

### Navigation — the Mast

- **Style:** One 13rem rail down the left edge of the page (the stylesheet
  still calls it `.fascia`) — the seven destinations as icon-and-name rows in
  Ink 3 from the top, search below them, and at the foot the wordmark at
  `text-lg` in Ink 2 beside the build in mono micro Ink 4. The rail's own background is "Die
  Blende", the one gradient in the application: black easing into plum, top to
  bottom.
- **Active:** Accent row with a 2px Accent left edge, plus
  `aria-current="page"` — the edge alone says it only to people who can see
  it. There is no tinted fill: the rail's own gradient already does the
  rail's colour work.
- **No tooltips:** Every row prints its own name, so there is nothing to
  reveal on hover.
- **No marks:** No counts, badges, dots or connection readings anywhere on
  the mast — the owner scrapped them all on 2026-09-01 to keep it clean. What
  is waiting is read in Review and Library; the slskd
  connection is read at `/settings/sources`.
- **Phone:** Overview, Artists, Review, Search and More sit in a fixed bar along
  the bottom of the screen, with small labels under the icons. The active tab has
  the same 2px Accent mark, this time on its top edge. More opens a sheet above
  the bar with Playlists, Downloads, Library and Settings as full-width rows.
  The sheet is modal: opening it moves focus to the first row and traps Tab
  inside, the page behind goes `inert`, and Escape, the backdrop or a
  destination link close it and hand focus back to the More button. A
  destination reached through More carries a small title bar above the
  content, naming it, because the tab bar for it only ever says "More".
- **Heading:** The mast's own highlight is not a document heading — a screen
  reader does not read `class="active"`. Every page carries exactly one
  `<h1>`: visible where the design already names the page (an artist, a
  release, the track Review is deciding), a visually hidden one everywhere
  else, naming the room.
- **Skip link:** The first focusable element in the document is "Skip to
  content", hidden until it is focused. It jumps to `<main>`, which carries
  `id="main"` and `tabindex="-1"` so the jump lands focus there.

### The Evidence Table (signature)

The one component this product could not buy. A candidate is a thing Schall
might be about to record permanently, so the table is built to be compared
rather than admired: one row per candidate, a radio and its index digit on the
left, then one column per thing the grader had an opinion about. It draws
Library's identity and match panels for a file already on disk — the two
questions that used to sit in Review before they moved to a filter there — and
nothing on Review itself any more.

**The disagreeing column is drawn first.** A column where any candidate differs
moves ahead of every column where none does, except the naming column, which
stays leftmost so a reader can still tell the rows apart. A disagreement is the
only thing that can rule a candidate out, and it was arriving sixth of ten: a
reader crossed nine cells that agreed to reach the one that did not, on every
row. A cell that disagrees carries a `fail` square with an X in it — never a
tint alone.

An artist-credit disagreement stays a disagreement when the audio identifies
the recording. The file may be a cover or re-recording, and the tagging pass
would overwrite the credit after a match so the mistake would be hidden.

**A length that disagrees is drawn as both times at once** — `3:12 vs 4:05` —
rather than one time with a computed delta beside it. The subtraction is the
reader's question, not the answer to it.

**A verdict word gets one sentence, behind a disclosure.** The table speaks in
fragments because a fragment is what fits in a cell beside four others, and
three of them — "could not listen", "not recognised", "no agreement" — differed
in a way nothing on screen said. The sentence is a `title` on the cell *and* in
the open disclosure, because a tooltip is unreachable from a keyboard.

**The panel names its decision instead of asking it.** The heading over the
table is a noun phrase — "Recordings this file could be" — not the question it
used to be. Nothing here shows a score, and nothing destroys anything in one
tap.

Selecting a row scrolls it into view, and the answer cannot be committed while
the chosen row sits behind the sticky bar. A file name is set in the monospace
role and cut at the front so the end survives; a track title is prose and is
not. A row is a surface and never a status hue: hover is Surface Thick,
selected is Surface Ultra, and nothing about a table row is a state the API
reports.

### Review's cards and candidate rows

Review draws its own two shapes now, neither of them the evidence table above.
A downloaded copy is a card that fills its grid cell — a radio, a small
"Copy N" label, a play button and a waveform, then a facts grid with a 5rem
label column: Length and Format carry the decision, so their values are set
in `text-body text-ink`; Size, Credit, Title and Album stay `text-quiet-meta`.
The played part of the waveform is Accent, the rest Line Thick. The grid runs
`repeat(auto-fill, minmax(17rem, 1fr))` inside a 64rem cap, so two cards sit
side by side at 1440px and one card takes the full width on a phone. A version
question is a row per candidate instead — Recording, Release, Length, and a
column of agree/differ chips built from the grader's own verdict lists, with
no disclosure and no meaning sentence under any of it. ISRC and the
MusicBrainz IDs sit behind a per-row "Identifiers" disclosure instead of a
column of their own: nobody compares them by eye. Two candidates that read
alike on every column shown are never preselected; the reader has to say which
they mean.

The copy cards sit in one `radiogroup`; each card's selectable element is a
`role="radio"` scoped to its label row, roving `tabindex` with the arrow keys,
so the play button and seek bar beside it are never part of the radio and
never select on their own. The page's Enter shortcut confirms the current
question only when focus sits outside every button, link, radio and slider —
on the page body, or on plain text — so Enter on Skip, on a queue row, or on a
card's Play control never commits a decision meant for something else.

The stopped-want library card is the downloaded shape with a different
heading — "In your library", `as "<fileTitle>"` — and a Credit or Title row in
fail red wherever the filed file disagrees with what was wanted. See "The
stopped-want card" above.

### Named Rules

**The Control Role Rule.** A new control on a browse board takes its look
from what it does, not from where it sits. A control that switches what the
page shows is a tab (`Segmented` with `variant="tabs"`). A control that
narrows the list already on screen is a filter group (`variant="filter"`). A
control that only reorders the list, or picks a preference that changes
nothing about which rows show, is a quiet select (`PillSelect` with `quiet`).
A status that describes one row, not the whole list, stays a chip
(`Chip.svelte`), never any of the three. The accent stays chrome under the
Chrome Rule: a tab may carry it because it marks where you are, a filter
value may not because it marks nothing but a value.

## Do's and Don'ts

### Do:

- **Do** use the accent for chrome only — the wordmark's tone, the active room,
  the one primary button, focus ring.
- **Do** pair every state colour with a glyph or a word.
- **Do** keep every ink that carries words at 4.5:1 or better against whatever
  is behind it — the ground `#0e0b10`, or the surface it is drawn on.
- **Do** pick a type scale by how the screen is used — `dense` for scanned,
  `quiet` for read — and then a named step within it.
- **Do** separate list rows with one hairline and let the list be one object.
- **Do** make depth out of light, using one of the nine named steps of the
  ladder and no value between them.
- **Do** draw a field, a checkbox or anything else something goes into on the
  inset, below the page, rather than as a wash of white above it.
- **Do** draw a card or panel in the plum surface colour outright; never as a
  wash of white over the ground.
- **Do** spend glass only on a surface that floats above the page — a menu,
  the one modal, the preview bar — never on a card or a table row.
- **Do** set identifiers, paths, labels, chips and figures in the monospace
  role, with tabular figures.
- **Do** raise touch targets against `pointer: coarse` rather than a width
  breakpoint, so a touchscreen laptop is served and a mouse keeps its density.
- **Do** give a field 16px text on a coarse pointer.
- **Do** name a motion token for every transition and animation, and pick the
  token by what the motion is doing rather than by how it looks.
- **Do** animate `transform` and `opacity`, and let a thing that moves layout
  simply change.
- **Do** draw a mark or a figure moving only when what it reports has changed,
  never when the page first draws it.

### Don't:

- **Don't** add a `box-shadow`. There are none, and depth is tonal.
- **Don't** invent a new white-over-ground opacity. There are four surfaces and
  four lines, each named; a value between two of them is drift, and nine such
  values have already been collapsed onto the ladder, as Elevation & Depth
  records.
- **Don't** put a second accent-filled control on a screen that already has
  one.
- **Don't** use the accent to mean anything — not "selected", not "changed",
  not "urgent".
- **Don't** add a gradient for decoration. "Die Blende" is the one decorative
  gradient in the application; a clipped scrollable control row may use a fade
  to show that more controls continue off-screen.
- **Don't** add a radius, a side-accent stripe, a decorative grid, or a
  hairline paired with a wide shadow. Flat 1px borders only.
- **Don't** set prose in the monospace role because it is technical. It reads
  as a costume and it overflows phones.
- **Don't** write a pixel value for text at a call site.
- **Don't** nest a card inside a card, or draw a list as a stack of cards.
- **Don't** put a state below 12px, or a label below 11px.
- **Don't** mix the two type scales on one screen.
- **Don't** show a number that stands for confidence anywhere a person decides
  something. The evidence is the thing; a score is a summary of it that cannot
  be checked.
- **Don't** restate a focus style on a component; the application has one ring
  and a call site that writes `focus:outline-none` is removing the only mark a
  keyboard has.
- **Don't** introduce a second accent hue, a gradient, or a decorative
  illustration.
- **Don't** write a duration or a curve at a call site. There are six durations
  and three curves; a seventh value is drift, and this specification once
  claimed there was one while the code ran four.
- **Don't** transition `width`, `height`, `top`, `left`, `margin` or `padding`
  on anything in normal flow. That moves the text around it, which is the shift
  the font loading strategy was changed to remove. Two places are exempt and
  both are written down: the main column's left margin, which has to follow the
  rail rather than let the page's whole width jump when it opens, and the fill
  of a progress bar, whose entire job is a width that changes and around which
  nothing reflows. A third is drift.
- **Don't** stagger a list, and don't animate rows on a data refresh.
- **Don't** put anything in the review queue between a keystroke and the
  decision it makes. Speed there is a correctness requirement.
- **Don't** add a motion without adding it to the `prefers-reduced-motion`
  guard in the same change.
