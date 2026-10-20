import { afterEach, describe, expect, it } from 'vitest';
import { flushSync } from 'svelte';
import { arrivals, listCursor, strike } from '$lib/live-list.svelte';

// The cursor holds its row by id, so a list that changes under the reader does
// not move the cursor onto a different row. `$effect.root` stands in for the
// component the helpers are called from.

let teardown: (() => void) | undefined;
afterEach(() => teardown?.());

function cursorOver(initial: string[]) {
  let ids = $state(initial);
  let cursor!: ReturnType<typeof listCursor>;
  teardown = $effect.root(() => {
    cursor = listCursor(() => ids);
  });
  flushSync();
  return {
    cursor,
    set(next: string[]) {
      ids = next;
      flushSync();
    }
  };
}

describe('the list cursor', () => {
  it('starts on the first row', () => {
    const { cursor } = cursorOver(['a', 'b', 'c']);
    expect(cursor.at).toBe(0);
    expect(cursor.id).toBe('a');
  });

  it('stays on its row when a row arrives above it', () => {
    const { cursor, set } = cursorOver(['a', 'b', 'c']);
    cursor.move(1);
    set(['new', 'a', 'b', 'c']);
    expect(cursor.id).toBe('b');
    expect(cursor.at).toBe(2);
  });

  it('takes the next row when its own row leaves', () => {
    const { cursor, set } = cursorOver(['a', 'b', 'c']);
    cursor.move(1);
    set(['a', 'c']);
    expect(cursor.id).toBe('c');
  });

  it('takes the last row when the last row leaves', () => {
    const { cursor, set } = cursorOver(['a', 'b', 'c']);
    cursor.move(2);
    set(['a', 'b']);
    expect(cursor.id).toBe('b');
  });

  it('stops at either end', () => {
    const { cursor } = cursorOver(['a', 'b']);
    cursor.move(-1);
    expect(cursor.at).toBe(0);
    cursor.move(5);
    expect(cursor.at).toBe(1);
  });

  it('has no row in an empty list', () => {
    const { cursor, set } = cursorOver(['a']);
    set([]);
    expect(cursor.at).toBe(-1);
    expect(cursor.id).toBe('');
  });
});

describe('arrivals', () => {
  function arrivalsOver(initial: string[], quiet = false) {
    let ids = $state(initial);
    let hold = $state(quiet);
    let seen!: ReturnType<typeof arrivals>;
    teardown = $effect.root(() => {
      seen = arrivals(
        () => ids,
        () => hold
      );
    });
    flushSync();
    return {
      seen,
      set(next: string[]) {
        ids = next;
        flushSync();
      },
      hold(value: boolean) {
        hold = value;
        flushSync();
      }
    };
  }

  it('does not treat the first rows as arrivals', () => {
    const { seen } = arrivalsOver(['a', 'b']);
    expect(seen.has('a')).toBe(false);
  });

  it('names a row that appears after the first draw', () => {
    const { seen, set } = arrivalsOver(['a', 'b']);
    set(['a', 'b', 'c']);
    expect(seen.has('c')).toBe(true);
    expect(seen.has('a')).toBe(false);
  });

  it('does not treat a list that opened empty as primed', () => {
    const { seen, set } = arrivalsOver([]);
    set(['a', 'b']);
    expect(seen.has('a')).toBe(false);
  });

  it('names nothing while the list is being replaced', () => {
    const { seen, set, hold } = arrivalsOver(['a']);
    hold(true);
    set(['x', 'y']);
    expect(seen.has('x')).toBe(false);
  });
});

describe('a row that leaves', () => {
  it('gives up the cursor before it folds away', () => {
    const row = document.createElement('article');
    row.innerHTML = '<div data-cursor="true" class="row on-ink"></div>';
    strike(row, { active: true });
    const inner = row.firstElementChild as HTMLElement;
    expect(row.classList.contains('struck')).toBe(true);
    expect(inner.getAttribute('data-cursor')).toBe('false');
    expect(inner.classList.contains('on-ink')).toBe(false);
  });

  it('is left alone when the whole list was replaced', () => {
    const row = document.createElement('article');
    strike(row, { active: false });
    expect(row.classList.contains('struck')).toBe(false);
  });
});
