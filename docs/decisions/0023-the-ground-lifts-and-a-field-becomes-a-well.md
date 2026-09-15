# 0023 — The ground lifts, and a field becomes a well

**Status:** accepted, 2026-08-14

## Context

Schall is used through a web interface, drawn in one theme, dark. The ground is
the colour of the page: the one value everything else is laid over. It was
`#0c0c0b`, which is very nearly black.

Every other surface in the product — a card, a chip, a hovered row, a field —
was white laid over that ground at a low opacity. There were four such steps, at
2%, 3.5%, 6% and 9%, and no surface below the page at all.

The greys the words are drawn in are the ink ramp: `--color-ink` for a title,
`--color-ink-2` for the line beside it, `--color-ink-3` for the artist and
release under it, `--color-ink-4` for a count or a status line. Contrast is
measured between a colour and the colour behind it, and 4.5:1 is the floor for
text at these sizes.

`docs/design-research.md` measured thirteen comparable interfaces and
`docs/design-plan.md` is the rebuild those measurements argued for. This decision
is the first batch of it and changes nothing but colour and type.

## Decision

Three things, and each one follows from the one before it.

**The ground lifts to `#111110`.** A surface below the page — `--color-inset`,
`#080807` — becomes possible, and is added.

**A field is drawn on the inset.** `.field`, `.check` and the cycling
placeholder's box no longer carry a wash of white. Their fill is
`--color-inset`, and their border is `--color-line-regular`.

**There are two type scales rather than one.** `dense` draws the library, the
queue, every table and all chrome; `quiet` draws the review screen, evidence and
anything read rather than scanned. Each step carries its own line height and
tracking on the size token itself, so a call site cannot take one without the
others. The four size names that already existed stay, aliased to the dense
scale, so no screen changes.

The four surface steps widen to 5%, 8%, 12% and 16%, the four line steps to 10%,
16%, 26% and 42%, and the ink ramp is recomputed against the new ground.

## Why

**The old ground had nowhere to recess to.** Every surface had to be a raise,
because there was almost nothing darker than `#0c0c0b` left to go to. A well, an
evidence panel and a block of code all had to be drawn as things standing on top
of the page, which is the opposite of what each of them is. Lifting the ground by
five points of luminance buys back a step underneath it and costs nothing a
reader can name.

**A field was the clearest thing being drawn wrong, and it was also failing.** A
field is a place where something goes in. Drawn as a wash of white it was a
raise, and a field inside a card was that wash twice — so its placeholder sat on
the lightest surface in the product. The stylesheet already recorded that at
4.06:1, under the floor, which is why the placeholder had been pushed one step
brighter to `ink-3` to clear it. On the inset the same words come to 5.90:1, the
step given up is given back, and the worst case in the product stops being the
worst case.

**One type scale cannot serve both readers.** A thousand-row table is scanned and
wants its rows close together. The review queue, where somebody decides something
permanent, is read and wants air. Carbon ships two scales for exactly this, and
the research found no interface serving both jobs well from one.

## Consequences

- **Every contrast figure the stylesheet stated about itself has moved**, because
  the colour behind the text has moved. The prose in `web/src/styles.css` states
  the recomputed figures; a figure quoted from before this change is measured
  against a ground that no longer exists.
- **`--color-ink-4` is permitted up to the `regular` surface and no further.** It
  is 4.58:1 there, 4.00:1 on `thick` and 3.51:1 on `ultra`. A hovered or selected
  row promotes its quiet line to `--color-ink-3`, which holds at 5.28:1 and
  4.63:1. Nothing on screen shows this rule being broken, so it is written in the
  stylesheet beside the token.
- **The surfaces and lines are named, not numbered** — thin, regular, thick,
  ultra — and a call site asks for one by name. About three hundred sites that
  wrote the opacity by hand now write the name instead. A handful that are a chip
  or a meter rather than a surface were left as they were, because each one needs
  a reading of its own.
- A sixth status role, `--color-done`, is added for a thing that is finished
  rather than good. It pairs with a glyph like the other five.
- **No light theme**, and no new dependency. Both are separate decisions.
