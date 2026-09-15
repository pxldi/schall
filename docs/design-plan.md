# Design plan

## Status: approved 2026-08-14. Building, batch by batch.

This file plans a clean rebuild of the Schall interface, starting from the
evidence in `docs/design-research.md` rather than from any earlier decision.

Read `docs/design-research.md` first. Every claim below traces to a measurement
in it.

### The four decisions, as taken

| Question | Answer |
| --- | --- |
| `bits-ui` as a dependency | **Yes.** Added in batch 2, in a diff of its own. |
| The ground | **Lift it to `#111110`.** Taken against the recommendation to keep `#0c0c0b`. |
| One type scale or two | **Two** — `dense` and `quiet`. |
| The overview page | **Leads.** It moves ahead of the three existing screens. |

Lifting the ground moves every contrast figure the stylesheet states about
itself, because contrast is measured between text and the colour behind it and
the colour behind it has changed. Section 2.4 carries the recomputed ramp.

---

## 1. The decision to take first

**Rebuild the component layer on shadcn-svelte, and derive our own tokens.**

shadcn-svelte is the Svelte port of shadcn/ui. It is Svelte 5 runes, Tailwind 4,
and CSS custom properties — our exact stack. Its command-line tool **copies
component source into `web/src/lib/components/ui/`** rather than installing a
package. A component becomes our code the moment it arrives, and we edit it like
any other file.

### What it actually costs

The frontend already depends on `clsx`, `tailwind-merge` and `@lucide/svelte`.
Those are precisely shadcn's `cn()` helper and its icon set. Adopting it adds
**one runtime dependency**: `bits-ui`, the headless primitive library underneath.
A small number of components pull one more each — `svelte-sonner` for toasts,
`@internationalized/date` for a date picker — and we take those only if we use
those components.

**This is a dependency decision and needs an explicit yes before any install.**

### Why this rather than hand-rolling

We would otherwise hand-write a dialog, a popover, a combobox, a dropdown menu
and a data table, each with its own focus trap, keyboard model and ARIA. Bits UI
has those and they are tested. The research turned up eight interfaces whose
menus, popovers and tables we admired; none of them hand-rolled the primitives
either.

### Why this rather than a full framework

Skeleton, Flowbite and Carbon Components Svelte would each impose a visual
identity we would then fight. shadcn-svelte imposes structure and leaves colour,
type and spacing entirely to us — which is the whole point, because the visual
identity is the part we want to design ourselves.

### The honest risk

The components arrive looking like shadcn: a specific radius, a specific muted
grey, a specific density. If we adopt it and never retheme it, Schall will look
like every other shadcn project. **The token work in section 2 is not optional
polish; it is the thing that stops that happening.** If we are not going to do
section 2, we should not do section 1 either.

---

## 2. The token system

Three ideas, taken from three different systems, that compose cleanly.

### 2.1 Primer's naming

Three properties, one role vocabulary, one prominence axis:

```
--fgColor-{role}       default · muted · subtle · accent · success · attention · danger · done
--bgColor-{role}       default · muted · inset · {role}-muted
--borderColor-{role}   default · muted · {role}-emphasis
```

One vocabulary applied three ways, so a reader who learns it once can predict
every token name. Two rules go with it:

- **Derive, never invent.** `--borderColor-muted` is `--borderColor-default` at
  70% alpha, exactly as GitHub does it. A derived ramp cannot drift.
- **`done` is its own role.** Purple, distinct from `success`. Schall has states
  that are finished rather than good — a manual decision, a resolved conflict, a
  merged recording — and green is the wrong word for them.

### 2.2 shadcn's foreground pairing

Every surface token carries a matching text token:

```css
--surface-raised:            oklch(…);
--surface-raised-foreground: oklch(…);
```

Used as `class="bg-surface-raised text-surface-raised-foreground"`. You never
choose a text colour separately, so a surface can never be paired with unreadable
text. This is not a convenience; it structurally prevents the failure where a
colour is defined only inside a dark block and the page renders one theme's text
on the other theme's ground.

### 2.3 Carbon's two type scales

One scale will not serve both a thousand-row table and a screen where somebody
decides something permanent. Carbon ships two, and so should we:

- **`dense`** — the library, the queue, every table. Tight leading, small steps.
- **`quiet`** — the review screen, evidence, anything read rather than scanned.
  Looser leading, larger steps, longer measure.

Each step carries its own line-height and letter-spacing, indexed together, as
Radix does — tracking tightening as size grows.

### 2.4 The ground, and the ramp it moves

Today's ground is `#0c0c0b`. It is darker than every one of the thirteen
interfaces measured, and the consequence is arithmetic, not taste: **a very dark
ground has nowhere to recess to**, so every surface must be a raise. GitHub can
afford `--bgColor-inset` at `#010409` because its default is `#0d1117`.

**Decided: lift the ground to `#111110`.** That buys the recess, which is what
gives evidence panels, wells and code a surface of their own instead of another
raise.

Two things follow, and the second is the one that will be got wrong if it is not
written down.

**The steps widen anyway.** Four steps at 2%, 3.5%, 6% and 9% are close to
invisible whatever they sit on; that was arithmetic about the steps, not about
the ground, so it carries over. Named rather than numbered, after Tidal:

| Step | White over ground | Renders | Job |
| --- | --- | --- | --- |
| inset | — | `#080807` | Wells, evidence panels, code |
| ground | — | `#111110` | The page |
| thin | 5% | `#1d1d1c` | Field fills, chips |
| regular | 8% | `#242423` | Cards, raised panels |
| thick | 12% | `#2e2e2d` | Hover |
| ultra | 16% | `#373736` | Selected, active |

**The ink ramp has to move with it.** Every grey in the product was chosen
against `#0c0c0b` and the file states its measured ratios in prose. Raising the
ground raises the luminance behind the text, so every one of those figures drops.
`--color-ink-4` goes from 4.71:1 to 4.55:1 on the bare ground and to **4.02:1 in
a field inside a card** — under the 4.5:1 floor for text at these sizes. The
recomputed ramp, each value checked on every surface it is allowed to sit on:

| Token | Was | Is | ground | thin | regular |
| --- | --- | --- | --- | --- | --- |
| `--color-ink` | `#e7e5e4` | `#e7e5e4` | 15.05 | 13.44 | 12.37 |
| `--color-ink-2` | `#a8a29e` | `#b6aea7` | 8.64 | 7.71 | 7.10 |
| `--color-ink-3` | `#948c85` | `#a8a099` | 7.34 | 6.55 | 6.03 |
| `--color-ink-4` | `#837b75` | `#928a83` | 5.56 | 4.97 | 4.58 |

**One rule comes with it.** `--color-ink-4` clears the floor up to `regular` and
no further: on `thick` it is 4.00:1 and on `ultra` 3.51:1. So a hovered or
selected row promotes its quiet line to `--color-ink-3`, which holds at 5.28:1
and 4.63:1. Written as a rule because it is invisible in a screenshot.

### 2.5 Named steps, not numbered ones

Tidal's Wave system names its opacity steps **ultra-thin, thin, regular, thick,
ultra-thick** and reuses them across roles. A name says what a step is *for*; a
number does not. Whatever count we land on, name them.

---

## 3. The screens

In the order I would build them.

### 3.1 The library table

The most-used screen, and the one with the clearest answer.

- **Two densities from one control.** `Comfortable` shows artwork and stacks the
  artist under the title. `Compact` drops the artwork and promotes artist to its
  own column. **The type size does not change between them** — that is what keeps
  compact readable, and it is what Spotify actually does.
- **The Status column shows the format held**, not a state word. `FLAC 16/44.1`,
  not a green tick. Absence then shows as absence.
- **Titles wrap; metadata truncates.** A track title is the thing being
  identified and must never be cut. The album column takes the ellipsis.
- **Column dividers in the header only.**
- **Hovering swaps the row's mode**: it lifts onto a raised surface, its
  hairlines disappear, and the row-number slot is replaced in place by the play
  control. No reflow.
- **An A–Z jump rail** on collections past a threshold.
- Keep the sticky table header. Apple Music's does not stick and it is worse for
  it.

### 3.2 The release page

Where the record shops beat the streaming services.

- **Every format as its own stacked block**, hairline-separated, each with its
  own technical line, size, path and state. All visible at once, because our
  question is nearly always a comparison. Not tabs.
- **The technical facts at full contrast** — `24-bit · 48 kHz · stereo · FLAC` —
  at body size and full-strength foreground, positioned as the header of the
  track list, the way Qobuz does it. Not a chip, not dimmed.
- **A completeness badge**, `9 / 12`, coloured by whether it is complete.
- **A wrapped fact row** for path, size, format, and wanted state. Icons only
  where the icon is conventional; a word anywhere it is not.
- **Credit MusicBrainz and AcoustID** quietly in the corner.

### 3.3 The review queue

The screen where somebody decides something permanent, and the one the research
changed my mind about most.

- **Two columns.** Wide: the evidence. Narrow: the terms of the decision.
- **Evidence as parallel tables with identical columns.** Candidate against
  candidate, same columns, differences read straight down. No score. No
  confidence number. No diff colouring beyond marking the differing token.
- **State the default outcome.** Every item says what happens if the person never
  decides — the single best idea in the research, and today entirely invisible in
  Schall.
- **Name each decision** with a type and an identifier, so it can be linked and
  quoted.
- **Evidence rows as label-above-value** with hairlines, in the `quiet` type
  scale.
- Candidates that could be several kinds of thing state their kind on the row.

### 3.4 The overview page

Not yet built, and now it has a reference.

- **A "waiting on you" list first**, with a **reason column** — why is this in
  front of me. GitHub's inbox does this and its dashboard does not, which is
  exactly why the inbox is useful and the dashboard is not.
- **Grouping controls whose current value is inside the label**: `Sort by:
  Newest first`, not a bare select.
- **Stat cards** in the shadcn-svelte `dashboard-01` shape: dim label, large
  number, one plain sentence.
- **Charts**, with ListenBrainz's good habits and none of its bad ones: controls
  inline in each panel header, interaction stated as a subtitle, a permalink per
  panel, timezone named on any time axis, binned legends. Single-hue sequential
  ramps for magnitude. **No sunbursts** — ListenBrainz's genre wheel is the
  prettiest and least readable thing on its page.

### 3.5 Jobs, wants and failure

- **Split the wanted list** into *missing entirely* and *held below the wanted
  quality*. Two jobs, two lists, as Radarr does.
- **Name the two searches**: automatic and interactive.
- **One "Show advanced" toggle** for the whole settings area, not a disclosure
  per field.
- **Format preferences as cards of chips**, in preference order.

### 3.6 Empty states

One rule, from a matched pair in the research. **When a page has no data, say it
once for the page, not once per panel.** Radarr's "Queue is empty" is a single
tinted bar with three words. ListenBrainz says the same sentence four times on
one screen and it is much worse.

And nothing is ever a dead end. Apple Music's 404 is one sentence with no action;
Bandcamp's offers a link back. Ours offers the link.

### 3.7 The shell

- **Icons in the navigation rail.** Four of the five tool interfaces have them;
  Apple Music does not and its rail is the hardest of the five to scan.
- **Sub-items revealed under the active section only.**
- **The rail floats; the content does not.** Inset and round the rail. Leave the
  content flush to the top and the right edge — Apple Music does exactly this,
  measured. Spotify panels everything and can afford to because its content is
  artwork; a thousand-row table cannot.
- **A collapsed rail** at around 72px, artwork or icons only, buying roughly 250
  pixels for the table.

---

## 4. Order of work

CI takes one pull request at a time in practice, so this is one
branch at a time, each a single reviewable change.

| # | Branch | Contents | Risk |
| --- | --- | --- | --- |
| 1 | tokens | The token system from section 2. No component changes. Old names aliased to new so nothing breaks. | Low |
| 2 | ui-foundation | Install `bits-ui`, add shadcn-svelte, bring in button, input, select, checkbox, dialog, popover, dropdown-menu, badge, table. Theme them to our tokens. Nothing consumes them yet. | Medium |
| 3 | selects | Replace the eight native `<select>` elements with the themed component. First real use of batch 2. | Low |
| 4 | overview | New page: waiting-on-you list, stat cards, first charts. | High |
| 5 | shell | Rail icons, active-section children, floating rail, collapsed rail. | Medium |
| 6 | library | Two densities, format-as-status, title wrapping, header dividers, hover mode. | High |
| 7 | release | Stacked format blocks, technical line, completeness badge, fact row. | High |
| 8 | review | Two-column layout, parallel evidence tables, default-outcome line. | High |

**The overview leads, but it does not go first.** It moved from last to fourth,
ahead of every screen that already exists. It cannot move ahead of batches 1 and
2, because a new screen built before the tokens and the components exist is a
screen that has to be built twice. Batch 3 stays in front of it as the cheap
proof that batch 2 works, on six files that already have a shape to compare
against — finding that out on a page with no previous version tells us nothing.

Building the overview early is what makes it useful: it is the one screen with
no legacy, so it shows what the token system does when nothing is inherited, and
the five screens after it have somewhere to point.

## 5. Out of scope

Named so nobody assumes otherwise.

- **Anything touching matching, identity, or the decision stores.** This is an
  interface plan. It changes what is shown and never what is concluded.
- **A light theme.** Worth doing, not now, and it needs its own decision about
  whether Schall is a dark-first product that tolerates light or a product with
  two equal themes.
- **User-resizable and user-selectable table columns.** Spotify has it, it is
  genuinely useful, and it is a large piece of work that should not ride along
  with a restyle.
- **Anything derived from Shopify Polaris or from Radarr's interactive search.**
  Neither could be examined; see the limits section of the research file.

## 6. What is still open

The four decisions at the top of this file are taken. One thing they opened is
not settled, and it blocks batch 4 rather than batch 1.

**The overview page's content.** Deciding it leads did not decide what is on it.
Three questions, all product rather than design:

1. **What feeds the "waiting on you" list.** Review-queue items are certain.
   Held copies, failed wants, releases without a cover and artists the follow
   feed cannot resolve are all candidates, and each one that is in has to carry a
   reason a person can read.
2. **Which charts come first**, and whether the overview carries any at all in
   its first version or only the list and the stat cards.
3. **Where it sits in the navigation** — whether it becomes the landing page or
   a seventh destination beside the six that exist.

Batches 1, 2 and 3 do not depend on any of it.
