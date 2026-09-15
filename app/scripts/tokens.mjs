// Copies the colours of web/src/styles.css into src/design/tokens.ts, so the
// app and the web read one palette. Run with `npm run tokens`; the test beside
// this file fails when the committed tokens.ts is behind the stylesheet.
import { readFileSync, writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
export const stylesheet = join(here, '..', '..', 'web', 'src', 'styles.css');
export const output = join(here, '..', 'src', 'design', 'tokens.ts');

export function render(css) {
  const colors = new Map();
  for (const match of css.matchAll(/^\s*--color-([a-z0-9-]+):\s*([^;]+);/gm)) {
    const [, name, value] = match;
    // A token that points at another token is a role, not a colour. The app
    // reads the colours and names its own roles.
    if (value.includes('var(') || colors.has(name)) continue;
    colors.set(name, value.trim());
  }
  const lines = [...colors].map(([name, value]) => `  '${name}': '${value}',`);
  return [
    '// Generated from web/src/styles.css by scripts/tokens.mjs. Do not edit;',
    '// run `npm run tokens` in app/ after changing the stylesheet.',
    'export const palette = {',
    ...lines,
    '} as const;',
    '',
    'export type PaletteName = keyof typeof palette;',
    ''
  ].join('\n');
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  writeFileSync(output, render(readFileSync(stylesheet, 'utf8')));
}
