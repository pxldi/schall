import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';

const source = readFileSync(fileURLToPath(new URL('./api-types.ts', import.meta.url)), 'utf8');

describe('api-types.ts', () => {
  // The types are read on their own, apart from the client, so this file stays free of fetch and SvelteKit.
  it('imports nothing but type-only fetch, $app or @sveltejs bindings', () => {
    const imports = [...source.matchAll(/^import\s+(type\s+)?[^;]*from\s+'([^']+)';/gm)];
    for (const [statement, isTypeOnly, from] of imports) {
      expect(isTypeOnly, statement).toBeTruthy();
      expect(from === 'fetch' || from.startsWith('$app') || from.startsWith('@sveltejs')).toBe(
        true
      );
    }
  });
});
