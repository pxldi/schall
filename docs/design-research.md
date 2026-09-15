# Design research, August 2026

## What this is

Schall is a self-hosted music collection manager. It holds a music library on
disk, matches it against a catalogue, and asks a person to decide whenever it
cannot prove an answer.

This file records what we learned by opening other people's interfaces in a
browser and measuring them. It starts from nothing. It does not assume any
earlier decision about how Schall should look, and it does not defend one. Where
a Schall value appears, it appears as a fact about the code as it stands today,
not as a rule to be obeyed.

Every number was read off a live page with a script, not estimated from a
screenshot. Where a number is missing, the page would not give it up, and that is
said in place.

The companion file `docs/design-plan.md` proposes what to do about all this.

## What we opened

Thirteen interfaces, in three groups.

**Streaming services** — Apple Music, Spotify, Tidal, Qobuz. These are what our
users compare us to.

**Record shops** — Boomkat, Bleep, Bandcamp. A shop must state format, pressing
and edition across a large catalogue. That is the closest anybody comes to our
actual problem.

**Tools** — MusicBrainz, ListenBrainz, Radarr, Sonarr, GitHub. These are where a
person makes a decision, or waits for background work, or both.

Six component systems were read for their tokens: Radix Themes, shadcn/ui, Vercel
Geist, GitHub Primer, IBM Carbon, and shadcn-svelte.

Captures live outside the repository, in the session scratchpad under
`design-research/`. They are evidence, not deliverables.

## Limits

Say these out loud so nobody treats the file as more than it is.

1. Spotify, Tidal, Qobuz, Radarr, Sonarr and GitHub were viewed signed in on the
   owner's own accounts. **Apple Music was viewed signed out**, so its library and
   personalised views were never seen.
2. **Radarr's interactive search was never opened.** Clicking it starts a real
   search against real indexers, and the permission classifier refused. The
   release-candidate table is missing from this research, and it is the screen
   closest to our source search.
3. **Shopify Polaris could not be read.** Its standalone token site now redirects
   to `shopify.dev` and the token pages are gone, because Polaris is being
   replaced by web components. Nothing was substituted for it.
4. **Discogs was proposed and not reached.** It has the best format and edition
   data model anywhere and is the obvious next site.
5. **Linear was skipped** at the owner's request, because signing up needs a
   phone number.

---

## The measured field

Every value read from the live page.

| | Ground | Text sizes in use | Row height | Tinted steps |
| --- | --- | --- | --- | --- |
| Apple Music | `#1f1f1f` | 12, 13, 26 | 46 / 54 | — |
| Spotify | `#121212` | 12, 14, 16, 24, 32 | 56 / 32 | 3 |
| Tidal | `#000000` | 12, 14, 19, 24 | 47 | 5, named |
| Qobuz | `#121212` | 12 and up | 62 | — |
| GitHub | `#0d1117` | 12, 14, 20 | — | — |
| Bleep | `#e6e6e6` | **12, 14** | — | — |
| Boomkat | `#ffffff` | 12, 13, 14, 16, 20, 32, 34 | — | — |
| Bandcamp | set by the seller | 10, 11, 12, 13, 14, 18 | — | — |
| Schall today | `#0c0c0b` | 11, 12, 13, 15, 24 | — | 4 surfaces, 4 lines |

Three things fall out of that table.

**Nobody else goes as dark as we do.** `#0c0c0b` is darker than every ground
here, and darker in practice than Tidal's pure black, because Tidal paints panels
over its black while we paint onto ours. Every step up from that ground is
expensive. A surface at 2% white over `#0c0c0b` is very nearly invisible.
Spotify runs a much larger product on three steps of 10%, 14% and 21%. Ours are
2%, 3.5%, 6% and 9%.

**GitHub recesses downward.** Its `--bgColor-inset` is `#010409`, *darker* than
its `#0d1117` default. We cannot do that; `#0c0c0b` has nowhere left to go. A
very dark ground forces every surface to be a raise, which is the more expensive
direction. That is a consequence of the ground choice, and it is worth choosing
knowingly.

**Restraint in type is normal, not extreme.** Bleep runs an entire shop on two
sizes. Apple Music runs its whole player on three. Five is unremarkable.

---

## Findings by problem

### Showing a list of thousands of tracks

**Two densities from one control.**

Spotify's track table has a "View as" menu with two settings, and the difference
between them is structural, not typographic.

| | List | Compact |
| --- | --- | --- |
| Row height | 56px | 32px |
| Artwork | 40px, radius 4 | none |
| Artist | stacked under the title | its own column |
| Title size | 16px | **16px** |

The title size does not change. The density comes from dropping the artwork and
unstacking the subtitle into a column. That is why the compact view stays
readable, and it nearly doubles the rows on screen.

**A hovered row stops being a list row.**

Point at a track in Apple Music and three things happen at once. The row lifts
onto a lighter rounded surface. The hairlines above and below it disappear. The
track number is replaced, in place, by a play triangle — same slot, no reflow.

A row is either in a list or it is a card, never both.

**Column dividers in the header and nowhere else.**

Apple Music's playlist table draws vertical rules in the header row only.
Columns are established once and the eye holds them down the page.

**The title never truncates; the metadata does.**

Apple grows a row to two lines rather than cut a track title, and truncates the
album column instead. Spotify does the opposite and clips titles with an
ellipsis. Apple's choice is better: the title is the thing being identified.

**Status should be the fact, not a state word.**

Sonarr's episode table has a Status column, and what it contains is the quality
actually held — `Bluray-1080p` — as a small chip. There is no `ok` or `missing`.
Absence then shows as absence.

**An A to Z jump rail.**

Radarr puts an alphabet index down the right edge of a long grid. Cheap
navigation through thousands of items for the cost of one narrow column.

**Users sizing their own columns.**

Spotify's table is a real `grid` whose `columnheader` elements each carry a
resize handle, plus a "Change visible columns" button. Recorded because it is
genuinely useful and genuinely a lot of work.

### Showing one release that exists in several forms

This is where the record shops beat the streaming services outright, and it is
our hardest screen.

**Every format on screen at once, each with its own terms.**

Bleep stacks each format as its own block, hairline-separated:

```
Vinyl, 1×LP                            Pre-order   27.99 €
Black vinyl
+ WAV / FLAC                                       [Add to Cart]
  · Printed inner sleeve
Estimated release date: October 2, 2026
────────────────────────────────────────────────────────────
CD                                     Pre-order   16.99 €
+ WAV / FLAC                                       [Add to Cart]
  · 4pp digipak CD with 12 page booklet
Estimated release date: October 2, 2026
────────────────────────────────────────────────────────────
Download                               Pre-order    9.99 €
Select format
  (•) MP3    ( ) WAV / FLAC
320 kbps, LAME-encoded                             [Add to Cart]
Available: October 2, 2026
```

Three things are right here. Every format is visible together, so comparing costs
nothing. A nested choice appears only where it is genuinely one product in two
encodings. And the technical detail is one plain line under the choice it
describes.

Boomkat solves the same problem with tabs — `MP3` `FLAC` `WAV` `BLACK VINYL 2LP`,
one visible at a time, each its own URL. Tabs make one format shareable by link.
Blocks make formats comparable. **Our question is almost always a comparison**,
so the stacked model is the one to take.

**Formats belong in the list row too, not only on the detail page.**

Boomkat's new-releases row carries, in one row: artwork, artist and title, a
right-aligned `Cat No | label | genre` line, a description with an inline `more`,
**every purchasable format as its own priced button**, and `Play All (8)` and
`Show Tracklist`. You never open a release to learn what forms of it exist.

Its toolbar above the list is equally tight: `Filter by: Format · Status · Date ·
Genre` on the left, `LIST / GRID` and `Show: 50 / 100` on the right. Filtering,
density and page size in one 40px band.

**State technical facts at full contrast.**

Qobuz puts this directly above the track table, as its header:

```
[Hi-Res AUDIO]   24-Bit
                 48 kHz – Stereo
```

Both lines are 12px, weight 500, pure white. Not dimmed, not a chip, not
decorated. It reads as a property of the tracks below because of where it sits.

The whole field disagrees about how to say this, and the spread is the useful
part:

| | How quality is said | Where |
| --- | --- | --- |
| Qobuz | `24-Bit` / `48 kHz – Stereo`, 12px, weight 500, white | header of the track table |
| Bleep | `320 kbps, LAME-encoded`, one plain line | under the format it describes |
| Bandcamp | "Download available in 24-bit/48kHz.", bold sentence | in the buy block |
| Boomkat | the format is the tab label | the tab strip |
| Tidal | `HIGH`, 8px, weight 700, `#33ffee`, 0.96px tracking | one chip beside the year |

Qobuz's is the one to copy. Tidal's 8px chip is unreadable at a glance and should
not be.

**Completeness as one badge.**

Sonarr shows `13 / 13` in a green badge beside each season. Have over total,
coloured by whether it is complete. A release that holds nine of twelve tracks
needs exactly this.

**A wrapped row of mixed facts, each labelled by its icon.**

Sonarr's series header carries this in one wrapped row:

```
📁 /media/TV/Better Call Saul   💾 112.3 GiB   👤 HD-1080p
🔖 Monitored   ⏹ Ended   🔤 English   AMC   Links
```

Path, size, quality profile, monitor state, status, language — six kinds of fact,
each self-labelling by its icon. It is a great deal of information in very little
space, and it is only legible because the icons are conventional. An icon that
has to be learned would ruin it.

**Preference sets as cards of chips.**

Radarr's quality profiles are cards: a title, and a wrapped set of small chips
listing accepted qualities in preference order. The whole model is legible at a
glance, with no table and no tree.

### Asking a person to decide something permanent

**MusicBrainz's edit page is the best decision screen we found, and it is
twenty years old.**

Two columns. The wide one holds the evidence as two tables with identical
columns, labelled `Merge:` and `Into:`. Differences are read straight down a
column. There is no diff colouring beyond a highlight on the differing token, no
score, and no confidence number.

The narrow one holds the terms of the decision as label and value pairs:

```
Status:               Open
                      This edit is open for voting.
Opened:               2026-08-14 19:24 UTC
Voting: Closes in     7 days
For quicker closing:  3 unanimous votes
If no votes cast:     Accept upon closing
```

**"If no votes cast: Accept upon closing" is the single best idea in this whole
study.** The screen states what happens if you never decide. Nothing else in
thirteen interfaces does it. Every decision Schall asks for has a default
outcome, and today that default is invisible.

The header names the decision: `Edit #151498977 - Merge recordings`. An
identifier and a type, so it can be linked, quoted and searched for.

**Evidence as label and value rows inside per-item cards.**

Tidal's Credits route is a two-column grid of per-track cards. Each card is a
header, then rows of a small dim uppercase label above a plain white value, with
a hairline between:

```
COMPOSER
W. Bevan
─────────────────────
MUSIC PUBLISHER
Hyperdub Publishing
```

No chips, no scores, no icons. Dense and completely scannable.

**A rail of short titled groups.**

MusicBrainz's release page rail is a series of small titled groups — Release
information, Additional details, Labels, Release events, Rating, Tags — each a
few label and value pairs. One field is **Data quality**, where MusicBrainz
publishes its own confidence in the record as a plain word. That is close to
something we need and do not have.

**Results that state their own kind.**

Apple Music returns a grid of mixed results where every card says what it is:
`Künstler:in`, `Album · Burial`, `Titel · Burial`. Tidal does the same job with a
flat list, a type word per row, and pill filters across the top.

The grid is better when there are six things you might have meant. The list is
better when there are sixty.

### Saying what is waiting for someone

**GitHub's dashboard is a counter-example.** Three columns, a prompt box, an
activity feed, and two dismissible advertisements. Nothing on it says what needs
you.

**GitHub's notifications inbox is the real thing.** Its best idea is a **reason
column**: every row says why it is in your inbox — "ci activity". The rail offers
three groupings at once: status (Inbox / Saved / Done), filters (Assigned,
Participating, Mentioned, Review requested), and repositories with counts. Sort
and group are dropdowns whose current value sits inside the label — `Sort by:
Newest to oldest` — so the setting reads without opening it.

**Radarr's navigation reveals its children.** The rail shows sub-items under the
active section only: Wanted expands to Missing and Cutoff Unmet.

**Radarr splits "missing" from "not good enough".** Wanted → **Missing** is
absent entirely. Wanted → **Cutoff Unmet** is present but below the quality
asked for. Two different jobs, two different lists.

**Radarr names the two kinds of search.** **Automatic Search** and **Interactive
Search** sit side by side as per-row actions. The system picks, or you pick.

**One toggle reveals every advanced field.** Radarr's settings carry a single
"Show Advanced" control in the toolbar that reveals advanced fields across the
whole settings area at once.

### Charts and statistics

ListenBrainz's statistics page is the most thorough example we found: eleven
visualisations on one page 5,852 pixels tall.

**The headline numbers sit below the chart, not above it.** `220,287 Listens` and
`11,015 Listens per year` — a very large number with a small label beside it,
placed under the plot it summarises.

**Ranked lists put the count in an accent-coloured pill on the right.** The number
is the emphasis, not the name.

**Each panel carries its own controls, inline with its title.** "Music By Decade"
has zoom buttons at the top right. "Artist Evolution" has `Top [10 ▾] artists`.
"Artist Origins" has `Rank by Artists ▾`. Nothing lives in a global toolbar that
belongs to one chart.

**Interaction is stated as a subtitle, not discovered.** "Click any bar to zoom in
and see individual years." "Click on a country to see more details." One plain
sentence under the title.

**Every panel has a permalink icon** at its top right, so a single chart can be
linked.

**The axis names its timezone.** The daily-activity heatmap labels its x axis
`Hour (Europe/Berlin)`. Every window Schall measures is measured from `now()`, so
this matters to us more than it does to them.

**A map gets a binned legend, not a continuous gradient.** Artist Origins bins
into `1.0–3.0`, `3.0–7.0`, `7.0–17`, `17–43`, `43–110`, `110–280`. Six steps you
can actually match by eye.

**Chart types, and how well each works:**

- *Daily Activity* — a day-of-week by hour-of-day heatmap on one sequential
  orange ramp. Reads instantly. No legend needed because the ramp is
  single-hued.
- *Music By Decade* — plain bars, one colour, axes labelled "Number of listens"
  and "Decade". The most legible thing on the page.
- *Artist Activity* — stacked bars, ranked descending, labels rotated. Fine at a
  glance for ranking, hard to read for any single segment.
- *Artist Evolution* — stacked area over time with an inline legend.
- *Genre Activity* — a radial sunburst split into 12AM / 6AM / 12PM / 6PM
  quadrants, with about a dozen hues and values crammed into the segments. It is
  the prettiest and the least readable thing on the page. **A counter-example**:
  a chart whose form is chosen before its question.

**And the failure.** The same "How and when are statistics calculated?" link
appears above every single panel — six times on one screen. When the page has no
data for the chosen period, the same sentence appears four times, plus a blank
panel. Say it once for the page.

### Empty states

A matched pair, and they settle the question.

**Radarr does it right.** One tinted full-width bar, three words: "Queue is
empty." No illustration, no advice, no button.

**ListenBrainz does it wrong**, as above.

**Apple Music's 404 is a dead end** — one centred sentence, no action at all.
**Bandcamp's is right**: "Please start at the beginning and you'll certainly find
what you're looking for," with the phrase linked.

### Pictures, colour and layout

**Artwork colour is always contained.** Spotify fades an artwork gradient to
`#121212` before the table starts. Qobuz runs a colour band across a full-bleed
header and ends it at a hard edge. Apple Music puts a soft bounded halo behind a
circular artist portrait. In all three it is bounded, and it never reaches the
rows.

**Placeholders are the artwork's own colour.** Apple Music fills a loading image
placeholder with the dominant colour of the image arriving, so nothing flashes
grey.

**Grain over the gradient.** Spotify overlays its artwork gradient with an inline
SVG fractal-noise texture at 5% opacity, as a `data:` URI, to stop the gradient
banding. It costs no request.

**The rail floats; the content does not.** Measured on Apple Music at 1440×900:
the rail sits at x=8, y=8, 244 wide, 884 tall, `border-radius: 20px`, background
`rgba(38, 40, 40, 0.6)`. The content starts at x=260, **y=0**, and runs to the
right edge.

Spotify panels everything — rail, content and a right panel, all inset with 8px
gutters. It can afford margins because its content is artwork. A thousand-row
table cannot.

**The rail can collapse to artwork.** Spotify's rail is drag-resizable from 72px
to 420px. At 72px it becomes a strip of cover art with no labels at all, and
buys about 250 pixels for the table.

**Rails carry icons.** Tidal, Radarr, Sonarr and Spotify all put an icon beside
each destination. Apple Music does not, and its rail is the hardest of the five
to scan.

**Credit the source.** Sonarr prints "Metadata is provided by TheTVDB" in the
corner of a series page. We take our catalogue from MusicBrainz and our
fingerprints from AcoustID.

---

## Component systems

Read for their tokens, not their looks.

| System | Tokens | Model |
| --- | --- | --- |
| Radix Themes | 1,288 | 12 numbered steps per hue, each step with a documented job |
| IBM Carbon | 676 | semantic layers, and **two complete type scales** |
| Vercel Geist | 427 | `--ds-*` and `--geist-*`, gray 100–1000, one radius |
| shadcn/ui | ~35 semantic | role pairs: every surface has a matching `-foreground` |
| GitHub Primer | — | `--{fg\|bg\|border}Color-{role}-{prominence}` |
| Shopify Polaris | — | could not be read |

### Primer's naming is the clearest

Three properties, one role vocabulary, one prominence axis. Read live from
`github.com`:

```
--fgColor-default        #f0f6fc      --bgColor-default       #0d1117
--fgColor-muted          #9198a1      --bgColor-muted         #151b23
--fgColor-accent         #4493f8      --bgColor-inset         #010409
--fgColor-success        #3fb950      --borderColor-default   #3d444d
--fgColor-attention      #d29922      --borderColor-muted     #3d444db3
--fgColor-danger         #f85149
--fgColor-done           #ab7df8
```

Two details worth taking. `--borderColor-muted` is the default border at 70%
alpha — **derived, not invented**, so the ramp cannot drift. And there is a
`done` role in purple, for things that are finished, distinct from `success`.
Schall has resolved and manually-decided states that are exactly "done" and not
"success".

### shadcn's foreground pairing prevents a real bug

Every surface token has a matching text token:

```css
--primary: oklch(0.205 0 0);
--primary-foreground: oklch(0.985 0 0);
```

Used as `class="bg-primary text-primary-foreground"`. You never choose a text
colour separately, so a surface can never be paired with unreadable text. This
structurally prevents the failure where a colour is defined only inside a dark
block and a page renders one theme's text on the other theme's ground.

### Carbon has two type scales, on purpose

Carbon ships **productive** and **expressive** scales, plus `body-short` and
`body-long`, whose line-heights differ by how much text is expected.

That is the honest answer to a problem we have. A dense library table and a quiet
review screen are not served well by one compromise scale.

### Radix indexes size, leading and tracking together

```
--font-size-1..9       12, 14, 16, 18, 20, 24, 28, 35, 60
--line-height-1..9     16, 20, 24, 26, 28, 30, 36, 40, 60
--letter-spacing-1..9  .005em → 0 → -.005em → … → -.05em
--space-1..9           4, 8, 12, 16, 24, 32, 40, 48, 64
--radius-1..6          3, 4, 6, 8, 12, 16
```

One index picks size, leading and tracking together, and tracking tightens as
size grows. Everything is `calc(x * scale)`, so a single factor resizes the whole
system.

### Tidal names its opacity steps

Tidal's system is called Wave. Its 117 root tokens follow
`--wave-color-opacity-{role}-{shade}-{thickness}`, where thickness runs
**ultra-thin (10%), thin (20%), regular (40%), thick (60%), ultra-thick (80%)**.

Named steps reused across roles beat a numbered ladder, because the name says
what the step is for.

### Spotify publishes its whole system in the page

```
--background-base               #121212
--background-elevated-base      #1f1f1f
--background-elevated-highlight #2a2a2a
--background-tinted-base        rgba(255,255,255,.10)
--background-tinted-highlight   rgba(255,255,255,.14)
--background-tinted-press       rgba(255,255,255,.21)
--text-base                     #fff
--text-subdued                  #b3b3b3
--essential-subdued             #7c7c7c   (icons)
--decorative-subdued            #292929   (hairlines)
--text-bright-accent            #1ed760
```

Three tinted steps, and they map to the three things that happen to a control:
hover, active, pressed.

### shadcn-svelte

The Svelte port of shadcn/ui: Svelte 5 runes, Tailwind 4, `oklch()` tokens, built
on Bits UI primitives. Its command-line tool **copies component source into the
repository** rather than installing a package, so components become our code the
moment they arrive.

It also ships **Blocks** — whole pre-built layouts. `dashboard-01` is described
as "a dashboard with sidebar, charts and data table" and installs with a single
command. Each stat card in it is a dim label with a trend chip on one line, a
large number, then a bold trend sentence and a dim explanation.

This is the closest match to our stack of anything examined, and the plan in
`docs/design-plan.md` is built on it.
