import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';

// `web/src/design-tokens.yaml` names the colours, type steps and components this
// interface is made of. `web/src/styles.css` is where those values actually
// live, so the list is only worth having while the two agree.
//
// They once stopped agreeing: two ADRs renamed every surface and line step and
// none of it reached the token list, so seventeen names in it did not exist.
const read = (path: string) =>
  readFileSync(fileURLToPath(new URL(path, import.meta.url).href), 'utf8');

const css = read('../styles.css');
const frontmatter = read('../design-tokens.yaml');

/** The body of the first rule whose selector starts with `selector`. */
function blockAfter(selector: string) {
  const start = css.indexOf(`\n${selector}`);
  if (start < 0) return '';
  const open = css.indexOf('{', start);
  let depth = 0;
  for (let at = open; at < css.length; at++) {
    if (css[at] === '{') depth++;
    if (css[at] === '}' && --depth === 0) return css.slice(open + 1, at);
  }
  return '';
}

const theme = blockAfter('@theme {');
const light = blockAfter(":root[data-theme='light'],");
const darkInsideLight = blockAfter(":root[data-theme='light'] :is(.on-ink, .inverted) {");

/** Every `--<prefix>-<name>: <value>` in one block, less the Tailwind
 *  companions that hang off a size token, the namespace resets and the aliases
 *  pointing at another. */
function declared(prefix: string, block = theme) {
  const found = new Map<string, string>();
  for (const [, name, value] of block.matchAll(
    new RegExp(String.raw`--${prefix}-([a-z0-9-]+):\s*([^;]+);`, 'g')
  )) {
    if (name.endsWith('--line-height') || name.endsWith('--letter-spacing')) continue;
    if (value.includes('var(') || value.trim() === 'initial') continue;
    found.set(name, value.trim());
  }
  return found;
}

/** The `key: "value"` pairs of one token group, at one level of indent. */
function frontmatterBlock(name: string) {
  const block = frontmatter.split(`\n${name}:\n`)[1]?.split(/\n[a-z]/)[0] ?? '';
  return new Map(
    [...block.matchAll(/^ {2}([a-z0-9-]+): "([^"]+)"$/gm)].map(([, k, v]) => [k, v])
  );
}

describe.each([
  ['dark', 'colors', theme],
  ['light', 'colors-light', light]
])('the %s colours design-tokens.yaml records', (_, group, block) => {
  const documented = frontmatterBlock(group);
  const shipped = declared('color', block);

  it('are all of the colours the stylesheet defines', () => {
    expect([...shipped.keys()].sort()).toEqual([...documented.keys()].sort());
  });

  it('carry the values the stylesheet gives them', () => {
    for (const [name, value] of shipped) {
      expect(documented.get(name), `${group}.${name}`).toBe(value);
    }
  });
});

describe('an inverted surface', () => {
  // An inverted surface in one theme draws in the other theme's colours, so a
  // state glyph on the cursor row keeps the contrast it was checked at. The
  // light theme's copy of the dark values is written out by hand.
  it('in the light theme carries the dark values', () => {
    expect(declared('color', darkInsideLight)).toEqual(declared('color', theme));
  });

  it('in the dark theme takes the light block', () => {
    expect(css).toMatch(/:root\[data-theme='light'\],\n:root:not\(\[data-theme='light'\]\) :is\(\.on-ink, \.inverted\) \{/);
  });
});

describe('the type steps design-tokens.yaml records', () => {
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

describe('the corners design-tokens.yaml records', () => {
  it('include every radius the stylesheet defines', () => {
    const documented = frontmatterBlock('rounded');
    for (const [name, value] of declared('radius')) {
      expect(documented.get(name), `rounded.${name}`).toBe(value);
    }
  });

  it('are never more than 2px', () => {
    for (const value of frontmatterBlock('rounded').values()) {
      expect(Number.parseFloat(value)).toBeLessThanOrEqual(2);
    }
  });
});

/** Every Svelte and TypeScript file under web/src, less the tests. */
function sources(dir = fileURLToPath(new URL('..', import.meta.url))): string[] {
  return readdirSync(dir).flatMap((name) => {
    const path = join(dir, name);
    if (statSync(path).isDirectory()) return sources(path);
    if (!/\.(svelte|ts)$/.test(name) || /\.test\.ts$/.test(name)) return [];
    return [path];
  });
}

describe('there is no accent (ADR 0042)', () => {
  // The look before this one printed every page in two inks taken from its
  // cover and filled the current tab and the primary button with the light
  // one. Selection is drawn by inversion now, and a colour that marks chrome
  // does not exist to be reached for.
  it('defines no accent, ink pair or ember token', () => {
    expect(css).not.toMatch(/--color-(accent|duo|ember)/);
  });

  it('draws the focus ring in the ink', () => {
    expect(css).toMatch(/:focus-visible \{\n {2}outline: 2px solid var\(--color-ink\);\n {2}outline-offset: 2px;/);
  });

  it('draws a selection inverted', () => {
    expect(css).toMatch(/::selection \{\n {2}background: var\(--color-ink\);\n {2}color: var\(--color-ground\);/);
  });

  // Tailwind emits nothing for a class whose token is gone, and the build
  // stays green, so a call site still asking for the old look is only caught
  // by reading the source.
  it('leaves no call site asking for a token that no longer exists', () => {
    const retired =
      /\b(?:[a-z-]+-)?(?:accent(?:-ink|-soft)?|duo-(?:dark|light)|surface-(?:thin|regular|thick|ultra)|line-(?:thin|inset|regular|thick|live)|shell-line)\b|\brounded-(?:tight|row|control|panel|card|pill|full|sm|md|lg|xl|2xl)\b|\btext-(?:dense|quiet|poster)-[a-z]+\b|\bfont-(?:display|wordmark)\b|\$lib\/duoton/;
    const offenders = sources().filter((path) => {
      const source = readFileSync(path, 'utf8')
        // Comments may name the old look; markup and code may not.
        .replace(/<!--[\s\S]*?-->/g, '')
        .replace(/\/\*[\s\S]*?\*\//g, '')
        .replace(/^\s*\/\/.*$/gm, '');
      return retired.test(source);
    });
    expect(offenders).toEqual([]);
  });

  it('draws no shadow, gradient or blur', () => {
    const offenders = sources().filter((path) =>
      /\b(?:shadow-\[|backdrop-blur|linear-gradient|radial-gradient)/.test(readFileSync(path, 'utf8'))
    );
    expect(offenders).toEqual([]);
  });
});

describe('the six state colours on a 14% tint of the panel', () => {
  // No state is read by hue alone; this is the readable floor for the six in
  // the form they are drawn in most often, a tint of the role behind the
  // role's own colour, in both themes.
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

  function tintedFillOf(textHex: string, surfaceHex: string) {
    const text = hexToRgb(textHex);
    const surface = hexToRgb(surfaceHex);
    return text.map((c, i) => c * 0.14 + surface[i] * 0.86) as [number, number, number];
  }

  const roles = ['ok', 'idle', 'decide', 'busy', 'fail', 'done'];

  describe.each([
    ['dark', theme],
    ['light', light]
  ])('in the %s theme', (_, block) => {
    const colours = declared('color', block);
    const panel = colours.get('panel')!;

    it.each(roles)('%s clears 4.5:1', (role) => {
      const hex = colours.get(role)!;
      const ratio = contrastRatio(hexToRgb(hex), tintedFillOf(hex, panel));
      expect(ratio, `${role} (${hex}) on bg-${role}/14 over the panel`).toBeGreaterThanOrEqual(4.5);
    });
  });
});

describe('the token file itself', () => {
  it('holds only the token groups the DESIGN.md schema accepts', () => {
    const groups = [...frontmatter.matchAll(/^([a-z-]+):$/gm)].map(([, g]) => g);
    // Motion, breakpoints and shadows are not groups this schema has. Motion is
    // declared in `styles.css` and described in the design spec.
    expect(groups).toEqual(['colors', 'colors-light', 'typography', 'rounded', 'spacing', 'components']);
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

describe('no state role is drawn in another token', () => {
  it('gives every role its own literal', () => {
    for (const role of ['ok', 'idle', 'decide', 'busy', 'fail', 'done']) {
      expect(declared('color').get(role), role).toMatch(/^#[0-9a-f]{6}$/);
    }
  });
});
