import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { output, render, stylesheet } from './tokens.mjs';

test('src/design/tokens.ts matches web/src/styles.css', () => {
  const expected = render(readFileSync(stylesheet, 'utf8'));
  const committed = readFileSync(output, 'utf8');
  assert.equal(committed, expected, 'run `npm run tokens` in app/ and commit the result');
});

test('the palette names every colour the app reads', () => {
  const rendered = render(readFileSync(stylesheet, 'utf8'));
  for (const name of ['ground', 'surface-thin', 'surface-regular', 'line-thin', 'ink', 'ink-2', 'ink-3', 'accent', 'accent-ink', 'ok', 'fail', 'decide', 'busy', 'idle', 'done']) {
    assert.match(rendered, new RegExp(`'${name}': '#`), `${name} is missing from styles.css`);
  }
});
