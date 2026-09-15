# 0017 — The interface has typography of its own

**Status:** accepted, 2026-08-12; the monospace superseded by 0020, 2026-08-13

## Context

Schall is used through a web interface. Every page of it is drawn in two
typefaces, named in `web/src/styles.css` as `--font-sans` and `--font-display`.
`--font-sans` is Inter and draws all body text, every table cell and every
control. `--font-display` is Manrope and draws every heading and the wordmark.
Both are self-hosted: the `.woff2` files ship inside the container, so a page
load makes no request to a font service. That was a deliberate choice and it
stays.

There is a third kind of text and it has no typeface of its own. Paths,
MusicBrainz recording IDs, ISRCs, durations, byte counts, bitrates and sample
rates are all set in monospace — a face where every character is the same width,
so that figures line up in a column. `styles.css` asks for that monospace four
times, and each time it asks with the same list: `ui-monospace`,
`SFMono-Regular`, `Menlo`, `monospace`. None of those files ship with Schall.
Each names a face the reader's own operating system supplies, so the same screen
is drawn differently on macOS, Windows and Linux, and Schall has never chosen
what any of it looks like.

A full design review of the interface ran on 2026-08-12 and is recorded at
`.impeccable/critique/2026-08-12T21-46-15Z__web-src.md`. Its finding about
typography was that Inter and Manrope on a near-black ground is the default
choice of the dark developer-tool interfaces of the early 2020s, that all
sixteen pages of Schall are built to one template, and that nothing on any of
them would have to change if the subject stopped being music. A separate
mechanical scan reached the same conclusion from a different direction: it
reported Inter as an overused face and measured it at between 36% and 79% of the
rendered text on each page.

The owner read that review, agreed with it, and asked for a braver replacement:
all three faces are to change, including the body face, and the interface is to
stop looking like a template.

## Decision

**Three faces, all self-hosted, all under the SIL Open Font License.**

- `--font-display` becomes **Newsreader**. It draws headings, the wordmark, and
  the large figures on stat cards. It is a serif — a face with small finishing
  strokes on the ends of its letters — drawn for reading on a screen, with a
  real weight range from 400 to 700. Variable, about 58KB.

  **Fraunces was chosen first and never committed.** It held the role while the
  work was built and was replaced before any of it landed, so no version of
  Schall was ever drawn in it. It carries two axes past weight, softness and
  **wonk**, and this document originally turned both up, on the argument that
  the default instance is the quietest Fraunces there is. Turned up is also the
  odd one: wonk tilts the terminals and swaps in a single-storey *g*. On a
  screen of file paths and durations the headings read as a costume rather than
  as a collection. Seen in place, on the real screens, it was wrong. Newsreader
  makes the same argument — a serif says "records" where a grotesque cannot —
  without a setting that has to be defended. It is also 62KB smaller, and it is
  not on the design detector's list of saturated interface faces, which Fraunces
  is. The rejected list below is what it was weighed against.
- `--font-sans` becomes **Schibsted Grotesk**. It draws body text, table cells
  and controls. It is a newsroom face: more character in its terminals than
  Inter, still drawn to be read at length. Variable weight, about 47KB.
- `--font-mono` is **created**, and becomes **Martian Mono**. It draws every
  identifier, path, duration, size and rate. Variable weight, about 24KB. The
  four hard-coded `ui-monospace, SFMono-Regular, Menlo, monospace` lists are
  replaced by this one token, so the operating system no longer decides what any
  part of Schall looks like.

Inter and Manrope are removed. Their `.woff2` files are deleted.

**Martian Mono is 16.7% wider than the alternatives, and that is accepted with
conditions.** Measured rather than estimated: it sets one character per 0.7em
where JetBrains Mono, IBM Plex Mono, Geist Mono, DM Mono and the system stack
all set 0.6em. At 12px a MusicBrainz recording ID goes from 259px to 302px, and
a 53-character library path from 382px to 445px.

The owner chose it knowing that, and the reasoning is recorded because it
changes what has to be built: the interface carries too much repeated prose, and
cutting that is work Schall needs regardless. Cutting it does not by itself pay
for the width, because the strings that consume the width are not prose. A
recording ID is 36 characters and editing cannot shorten it. So the width is
bought by four changes, and **each is a condition of the face landing**:

1. **A path truncates from the left.** Two library paths differ at the end and
   agree at the start, so ellipsising the right-hand side hides the only part
   that identifies the file. This is a defect today and Martian Mono makes it
   17% more likely to trigger.
2. **Any identifier shown short gets a control that copies the whole of it.**
   `navigator.clipboard` appears nowhere in the frontend today.
3. **The review candidate row stops being a row.** It renders
   `recordingId.slice(0, 8)` today, and on the live instance two candidates
   showed the same eight characters, which is a defect on its own. A narrower
   column would have made it worse; the row becomes a comparison that states
   what differs, so it never depends on width it does not have.
4. **The mono columns take the room the cut prose gives back.** This is where
   the shorter interface text pays, and it is the reason the two decisions are
   recorded together.

**The smallest type in the interface rises before the faces change.** Today 224
places ask for 10.5px text and 44 ask for 9.5px. Nothing that carries meaning
may be smaller than **12px**, and 11px is the absolute floor for a label that
repeats a value shown elsewhere. This is part of the same decision rather than a
separate one: a face with character is harder to read than a neutral one, and
putting one at 9.5px would trade legibility for personality. The floor rises
first, and the faces land on top of it.

**The Claude design project is set aside for this work.** `docs/PRODUCT.md`
says the design source of truth lives in the owner's Claude design project
outside this repository, and that a session needing visual changes asks for it
rather than inventing them. The owner has suspended that rule for this redesign
and asked for the interface to be rebuilt with the Impeccable skill instead.
The rule is suspended, not deleted: when the redesign settles, the result is
recorded back into the design project, and the two must be reconciled before
`docs/PRODUCT.md` is treated as current on this point again.

**Amended 2026-08-13, when the design project stopped being a second place.**
The paragraph above records how this work started, and it is accurate: the
owner suspended the `docs/PRODUCT.md` rule for this redesign and asked for the
Impeccable skill instead. What has changed since is where the design answer is
written down. Impeccable is a design toolkit, and it now runs inside this
repository and writes its output here. `DESIGN.md` at the repository root is
the committed design specification — the colour tokens, the typography roles,
the corner radii, the spacing steps, the named components, and eight named
rules. `.impeccable/design.json` beside it is the sidecar that holds what the
specification's schema cannot: the tonal ramps behind the colours, the motion
and breakpoint tokens, and a self-contained definition of each component. Those
two files are the design source of truth, and `docs/PRODUCT.md` now says so.
The obligation this decision set — record the result back into the Claude
design project, then reconcile the two before `docs/PRODUCT.md` is current
again — is discharged. There is no second place left to reconcile with. The
typography decision itself is unchanged: the same faces, the same roles, and
the same 12px floor.

## Why

**A monospace is the most characteristic surface Schall has, and it was the one
surface Schall never chose.** What separates this application from a media
player is that it shows its working: a fingerprint identified this file, this
recording ID was agreed, this duration is three seconds out. That material is
all set in monospace, at small sizes, in quantity. A face of its own turns it
from log output into a record.

**A serif is the cheapest true thing Schall can say about itself.** The point of
the application is that the user owns music rather than renting it. Sleeve
typography, label credits and liner notes are what owned records look like, and
they are set in serifs. A serif at 24px and above says "collection" in a way no
sans-serif and no amount of layout can. Newsreader earns the role over a plainer
serif on one practical ground as well as character: it has a real weight range,
so a bold heading in it is genuinely bolder rather than the same weight larger.
That is what ruled out Instrument Serif and Young Serif, which ship one weight
each, and it is the test any later replacement has to pass too.

**Bravery belongs in the display and the monospace, not in the body.** The body
face draws 3,538 track rows, 963 releases and 3,251 download requests. A face
that is interesting to look at once is tiring to read a thousand times.
Schibsted Grotesk carries more character than Inter without asking the reader to
work for it, and it comes out of the same editorial world as Newsreader, so the
two agree with each other instead of each making its own argument.

**The wide monospace is a bet that the interface will hold less text.** It is
the one choice here that costs something, and it is deliberate. Schall repeats
itself: the same two-line explanation renders verbatim on seven playlist rows,
twenty-two recommendation rows carry the same truncated sentence, and the
identity question prints one justification under both of its candidates. That is
not a writing-quality problem — the review rated the prose the best thing in the
product — it is the same good sentence printed where a heading or a disclosure
should carry it once. A face that demands room is a standing reason to keep that
discipline.

## What was rejected, and why

**Keeping Inter as the body face.** It is a good face and it is the correct
technical choice for an interface. It is also the reason the interface looks
like every other one. Since all three faces are being replaced anyway, keeping
the most common of them would leave the review's central finding unanswered.

**Bricolage Grotesque as the body face.** It is the braver option and it was
seriously considered: it is variable, it carries an optical-size axis, and it is
genuinely unlike anything else. It was rejected for the body only. Schall is a
dense application whose main screens are long lists, and a display-leaning
grotesque across thousands of rows is fatiguing in a way that is not visible in
a screenshot. It remains the substitution to make if Schibsted Grotesk turns out
to be too quiet once the rest of the redesign has landed.

**Instrument Serif and Instrument Sans**, which an earlier draft of this
decision named. Instrument Serif was rejected on a hard constraint rather than
taste: it has one weight, so it cannot carry a heading hierarchy without using
size alone. Instrument Sans went with it once its companion did. **Young Serif**
was rejected for the same reason, on the same test.

**The four display serifs weighed against Newsreader,** all rendered on the real
Review screen rather than on a specimen sheet, because a face is chosen by how
it sets this application's own words:

- **Spectral** was the closest. It is more refined than Newsreader and slightly
  lighter. It lost on two counts: it ships four fixed weights rather than one
  variable axis, and at heading size it goes delicate next to Martian Mono,
  which is heavy. It is the substitution to make if Newsreader reads as too
  plain.
- **Source Serif 4** is sturdy, neutral and completely competent, which is the
  objection. Trading a face for one with less to say answers nothing.
- **Literata** is wider and slabbier, and at heading size it turns bookish
  rather than editorial.
- **Bodoni Moda** was rejected on the ground. Its hairline strokes thin out on
  `#0c0c0b`, and a fashion didone set over file paths and ISRCs makes an
  argument this application is not making.

**A narrower monospace — IBM Plex Mono or JetBrains Mono.** Both are 0.6em and
both would have cost nothing. Rejected because neither is distinctive, and the
finding this decision answers was that nothing in the interface is. The width is
paid for by the four conditions above rather than avoided.

**Space Grotesk.** It was the obvious characterful replacement for Manrope three
years ago, which is exactly why it is now as generic as Manrope.

**A font service.** Rejected without discussion. Schall is self-hosted software;
a page load must not tell a third party that somebody opened it.

## What breaks if reversed

Nothing structural. Every heading in the application already carries the
`.font-display` class and every body element inherits `--font-sans`, so the two
existing faces are two token values. Reversing the monospace is harder in one
direction only: once `--font-mono` exists, the four hard-coded lists are gone,
and going back means either restoring them or pointing the token at the system
stack.

The type floor is the part that does not reverse cleanly. Raising 268 call sites
off 9.5px and 10.5px changes the vertical size of every list in the application,
and the layouts that follow will have been drawn against the larger sizes.

## What is not decided here

**The rest of the redesign.** This decision covers typefaces and the size floor.
The review found more that is not typography: sixteen pages built to one
template, the disagreement on a review candidate drawn at the same weight as the
agreements, cover art rendered at 16px, and a third surface tier that does not
exist yet for the places where a permanent decision is taken. Those are separate
work and some of them are more important than this.

**Which of the five state colours change, if any.** The five roles in
`styles.css` — `ok`, `idle`, `decide`, `busy`, `fail` — and the rule that each
must pair with a glyph are untouched by this decision. So is the rule that the
ember accent is chrome and never encodes state.

**Whether `docs/PRODUCT.md` is amended.** Its UI paragraph now describes a rule
the owner has suspended. It is left as written until the redesign settles,
because amending it now would record a temporary arrangement as the intent.

**Where explanation belongs, now that the interface is to carry less of it.**
The owner's position is that Schall says too much on screen, and the four
conditions above depend on that being acted on. `CLAUDE.md` requires interface
text to define a thing before naming a fault in it, and says only that it "stays
short" — which is guidance about a sentence, not about how many times one
sentence may appear or which surface should hold it. Deciding that a row carries
values, a section heading carries the explanation, and a disclosure carries the
detail is a separate decision and is not made here.
