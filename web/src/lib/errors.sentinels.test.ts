/** The map in `errors.ts` keys on the exact words the Go source uses, because a
 * problem document carries no error code. That is a link nothing enforces: a
 * sentinel reworded in Go leaves the key behind, the lookup falls through to the
 * status backstop, and the sentence a reader gets quietly becomes vaguer. That
 * is the failure this whole batch exists to fix, returning without a sound.
 *
 * So the link is enforced here. Every key of the map has to be present,
 * literally, somewhere under `internal/`. A rewording in Go turns into a red
 * build naming the key that moved, and whoever reworded it writes the new
 * sentence at the same time.
 *
 * This test never asks the Go source to change. A sentinel is allowed to say
 * whatever it says; it is the map that has to follow.
 */

import { readFileSync, readdirSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';
import { sentinels } from '$lib/errors';

// Resolved from this file rather than from the working directory, because the
// tests are started from `web/` and the Go source is three levels above this
// one.
const goRoot = join(dirname(fileURLToPath(import.meta.url)), '..', '..', '..', 'internal');

function goSources(directory: string): string[] {
  const files: string[] = [];
  for (const entry of readdirSync(directory, { withFileTypes: true })) {
    const path = join(directory, entry.name);
    if (entry.isDirectory()) {
      files.push(...goSources(path));
      continue;
    }
    // Go test files are left out: a sentinel that lives only in a Go test is
    // not one the server can ever answer with. Two entries were caught by
    // exactly that the first time this test was run.
    if (entry.name.endsWith('.go') && !entry.name.endsWith('_test.go')) files.push(path);
  }
  return files;
}

// Read once, and searched with `includes` rather than a regular expression,
// because several sentinels contain characters a pattern would have to escape.
const source = goSources(goRoot)
  .map((path) => readFileSync(path, 'utf8'))
  .join('\n');

describe('the sentinels the interface writes a sentence for', () => {
  it('reads a good deal of Go', () => {
    // A guard on the guard: an empty read would pass every check below by
    // finding nothing to disagree with.
    expect(source.length).toBeGreaterThan(100_000);
  });

  it.each(Object.keys(sentinels))('is still in the Go source: %s', (sentinel) => {
    expect(
      source.includes(sentinel),
      `errors.ts writes a sentence for "${sentinel}", and no file under internal/ says that any more. ` +
        `Either the Go sentinel was reworded — in which case update the key in web/src/lib/errors.ts to match ` +
        `and keep the sentence — or the failure is gone, in which case delete the entry.`
    ).toBe(true);
  });
});
