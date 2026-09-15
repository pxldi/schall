import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';

// DESIGN.md is the record of what this interface is made of, and its frontmatter
// is the machine-readable half of that record: the colours, the type steps, the
// components. `web/src/styles.css` is where those values actually live, so the
// record is only worth having while the two agree.
//
// They had stopped agreeing. docs/decisions/0022 retired the serif and 0023
// lifted the ground, added a surface below it, renamed all eight surface and
// line steps, recomputed the whole ink ramp and split one type scale into two.
// None of it reached DESIGN.md. Seventeen tokens named there did not exist, four
// that existed were not named, and an agent generating a screen from the file
// would have written `bg-surface-raised`, which resolves to nothing at all.
//
// A document nobody can tell is wrong goes wrong quietly. This makes it loud.
const read = (path: string) =>
  readFileSync(fileURLToPath(new URL(path, import.meta.url).href), 'utf8');

const css = read('../styles.css');
const design = read('../../../DESIGN.md');

const frontmatter = design.split('\n---\n')[0];

/** Every `--<prefix>-<name>: <value>` in the stylesheet, less the Tailwind
 *  companions that hang off a size token and the aliases pointing at another. */
function declared(prefix: string) {
  const found = new Map<string, string>();
  for (const [, name, value] of css.matchAll(
    new RegExp(String.raw`--${prefix}-([a-z0-9-]+):\s*([^;]+);`, 'g')
  )) {
    if (name.endsWith('--line-height') || name.endsWith('--letter-spacing')) continue;
    if (value.includes('var(')) continue;
    found.set(name, value.trim());
  }
  return found;
}

/** The `key: "value"` pairs of one frontmatter block, at one level of indent. */
function frontmatterBlock(name: string) {
  const block = frontmatter.split(`\n${name}:\n`)[1]?.split(/\n[a-z]/)[0] ?? '';
  return new Map(
    [...block.matchAll(/^ {2}([a-z0-9-]+): "([^"]+)"$/gm)].map(([, k, v]) => [k, v])
  );
}

describe('the colours DESIGN.md records', () => {
  const documented = frontmatterBlock('colors');
  const shipped = declared('color');

  it('are all of the colours the stylesheet defines', () => {
    expect([...shipped.keys()].sort()).toEqual([...documented.keys()].sort());
  });

  it('carry the values the stylesheet gives them', () => {
    for (const [name, value] of shipped) {
      expect(documented.get(name), `colors.${name}`).toBe(value);
    }
  });
});

describe('the type steps DESIGN.md records', () => {
  const typography = frontmatter.split('\ntypography:\n')[1]?.split(/\n[a-z]/)[0] ?? '';
  const documented = new Set(
    [...typography.matchAll(/^ {2}([a-z0-9-]+):$/gm)].map(([, k]) => k)
  );
  const shipped = declared('text');

  it('names every step the stylesheet defines', () => {
    for (const step of shipped.keys()) {
      expect(documented, `typography.${step}`).toContain(step);
    }
  });

  it('documents every step the stylesheet needs', () => {
    for (const step of documented) {
      expect(shipped.has(step), `typography.${step}`).toBe(true);
    }
  });
});

describe('the accent stays chrome, never state', () => {
  // Two radio groups in SettingsSections.svelte — the weekly playlist mode
  // and the retention choice — used `bg-ember`/`text-ember-ink` to mean
  // "chosen", which the Chrome Rule forbids: the accent marks chrome and
  // never a selection. Both now use the treatment `.check` uses for its own
  // border, a `border-line-live` ring with an ink fill. After the fix nothing
  // in the file names the accent, which is what this would have failed on
  // before. Ember was the accent's hex under Schall; it is lilac under
  // Schall, and the rule the word guards did not move with it.
  //
  // TranscodeSettings.svelte has the same two-way choice drawn the same wrong
  // way, twice more (:131-142, :158-169), and it is not covered here — fixing
  // it is outstanding work, not a regression this test can catch yet.
  it('SettingsSections.svelte draws no accent-filled control', () => {
    const source = read('./components/SettingsSections.svelte');
    expect(source).not.toMatch(/\bbg-accent\b/);
    expect(source).not.toMatch(/\btext-accent-ink\b/);
  });

  // The armed confirm on the duplicates screen filled with the accent, the variant
  // reserved for the screen's one safe primary action, to mean "you are about
  // to delete files for good". `danger` exists for that now.
  it('the duplicates confirm asks in danger, not in primary', () => {
    const source = read('./components/DuplicateRecordings.svelte');
    expect(source).toMatch(/confirming === 'all' \? 'danger'/);
    expect(source).not.toMatch(/confirming === [^?]*\? 'primary'/);
  });
});

describe('the six state colours, tinted at 14% on Surface Regular', () => {
  // The Glyph Rule says no state is read by hue alone; this is the Readable
  // Floor Rule's own check on the same six, in the one form they are drawn in
  // most: a chip's tinted fill rather than the ground. `Chip.svelte` and
  // `Badge.svelte` write the fill as `bg-<role>/14` and the text as
  // `text-<role>` — the same hex on both sides of the compositing.
  //
  // axe measured `idle` on `bg-idle/14` at 3.82:1 on 2026-09-07; the
  // arithmetic here reads 3.84:1 for the same pair, close enough that this is
  // the same check axe runs, not a different one that happened to agree.
  function hexToRgb(hex: string): [number, number, number] {
    const n = parseInt(hex.slice(1), 16);
    return [(n >> 16) & 255, (n >> 8) & 255, n & 255];
  }

  function relativeLuminance([r, g, b]: [number, number, number]) {
    const channel = (c: number) => {
      const s = c / 255;
      return s <= 0.03928 ? s / 12.92 : Math.pow((s + 0.055) / 1.055, 2.4);
    };
    const [R, G, B] = [channel(r), channel(g), channel(b)];
    return 0.2126 * R + 0.7152 * G + 0.0722 * B;
  }

  function contrastRatio(a: [number, number, number], b: [number, number, number]) {
    const [l1, l2] = [relativeLuminance(a), relativeLuminance(b)].sort((x, y) => y - x);
    return (l1 + 0.05) / (l2 + 0.05);
  }

  // Plain alpha compositing of the text colour at 14% over the surface below
  // it, the same blend `color-mix` performs for a `/14` opacity modifier.
  function tintedFillOf(textHex: string, surfaceHex: string) {
    const text = hexToRgb(textHex);
    const surface = hexToRgb(surfaceHex);
    return text.map((c, i) => c * 0.14 + surface[i] * 0.86) as [number, number, number];
  }

  const colours = declared('color');
  const surfaceRegular = colours.get('surface-regular')!;
  const roles = ['ok', 'idle', 'decide', 'busy', 'fail', 'done'];

  it.each(roles)('%s on its own tint over Surface Regular clears 4.5:1', (role) => {
    const hex = colours.get(role)!;
    const ratio = contrastRatio(hexToRgb(hex), tintedFillOf(hex, surfaceRegular));
    expect(ratio, `${role} (${hex}) on bg-${role}/14 over Surface Regular`).toBeGreaterThanOrEqual(
      4.5
    );
  });
});

describe('the frontmatter itself', () => {
  it('holds only the token groups the DESIGN.md schema accepts', () => {
    const groups = [...frontmatter.matchAll(/^([a-z]+):$/gm)].map(([, g]) => g);
    // Motion, breakpoints and shadows are not groups this schema has. They are
    // declared in `styles.css` and described in DESIGN.md's own Motion section,
    // where the prose can say what each one is for; a second copy of the values
    // in the frontmatter would be a second thing to keep true.
    expect(groups).toEqual(['colors', 'typography', 'rounded', 'spacing', 'components']);
  });

  it('points every component at a token that exists', () => {
    const colours = frontmatterBlock('colors');
    const steps = new Set(
      [...frontmatter.split('\ntypography:\n')[1].matchAll(/^ {2}([a-z0-9-]+):$/gm)].map(
        ([, k]) => k
      )
    );
    const rounded = frontmatterBlock('rounded');
    const components = frontmatter.split('\ncomponents:\n')[1] ?? '';

    for (const [, group, token] of components.matchAll(/\{(colors|typography|rounded)\.([a-z0-9-]+)\}/g)) {
      const known = { colors: colours.has(token), typography: steps.has(token), rounded: rounded.has(token) };
      expect(known[group as keyof typeof known], `{${group}.${token}}`).toBe(true);
    }
  });
});
