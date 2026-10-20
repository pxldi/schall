// The pieces a list that changes under the reader's eyes shares (ADR 0042):
// a cursor that keeps its row when the rows move, rows that strike through as
// they leave, and a note of which rows have just arrived.

import { untrack } from 'svelte';
import { leave, motionMs } from '$lib/motion.svelte';

// The columns a list's rows and its header share, as fixed widths. A row that
// is leaving is taken out of flow by Svelte's `animate`, and a grid that sized
// its tracks to its content would close up around the cells it lost.
// Wishlist: state, song, origin (from lg), searched, button.
export const WANT_COLUMNS =
  'md:grid-cols-[1.75rem_minmax(0,1fr)_12rem_12rem] lg:grid-cols-[1.75rem_minmax(0,1.2fr)_minmax(0,1fr)_12rem_12rem]';
// Uploads: state, files, detail (from lg), size, buttons.
export const UPLOAD_COLUMNS =
  'md:grid-cols-[1.75rem_minmax(0,1fr)_5rem_12rem] lg:grid-cols-[1.75rem_minmax(0,1.2fr)_minmax(0,1fr)_5rem_12rem]';
// Followed playlists: cover, name, owned, buttons.
export const PLAYLIST_COLUMNS =
  'grid-cols-[2rem_minmax(0,1fr)_11rem_9rem] lg:grid-cols-[2rem_minmax(0,1fr)_11rem_22rem]';

// listCursor keeps the reader's place in a list by the row's id as well as by
// its position. A row arriving above the cursor does not move the cursor onto
// whatever slid into its place; a row leaving takes the cursor to the row that
// closes the gap.
//
//   const cursor = listCursor(() => rows.map((row) => row.id));
//   <div data-cursor={index === cursor.at}>
export function listCursor(ids: () => string[]) {
  let held = $state('');
  let last = $state(0);

  const at = $derived.by(() => {
    const list = ids();
    if (list.length === 0) return -1;
    const found = held ? list.indexOf(held) : -1;
    return found >= 0 ? found : Math.min(last, list.length - 1);
  });

  // Follows the held row, and takes the neighbouring one when it is gone.
  $effect(() => {
    const list = ids();
    untrack(() => {
      if (list.length === 0) {
        held = '';
        return;
      }
      const found = held ? list.indexOf(held) : -1;
      if (found >= 0) {
        last = found;
        return;
      }
      last = Math.min(last, list.length - 1);
      held = list[last];
    });
  });

  return {
    /** The cursor row's index, or -1 when the list is empty. */
    get at() {
      return at;
    },
    /** The cursor row's id, or '' when the list is empty. */
    get id() {
      return at >= 0 ? (ids()[at] ?? '') : '';
    },
    /** Moves by rows and stops at either end. */
    move(by: number) {
      const list = ids();
      if (list.length === 0) return;
      const next = Math.min(Math.max((at < 0 ? 0 : at) + by, 0), list.length - 1);
      held = list[next];
      last = next;
    },
    /** Puts the cursor on the row at an index. */
    set(index: number) {
      const list = ids();
      if (index < 0 || index >= list.length) return;
      held = list[index];
      last = index;
    }
  };
}

// strike is the out transition for a row that finished or was taken off: a
// line is drawn through it, then it folds away. A row that leaves because the
// whole list was replaced goes on the cut, so `active` is false for those.
//
//   <article out:strike={{ active: !replacing }}>
export function strike(node: Element, { active = true }: { active?: boolean } = {}) {
  if (!active) return { duration: 0 };
  node.classList.add('struck');
  // The cursor leaves with the row: the block is placed on whichever row now
  // holds the cursor, and this one is read on the ground like any other.
  for (const marked of [node, ...node.querySelectorAll('[data-cursor]')]) {
    if (marked.getAttribute('data-cursor') === 'true') marked.setAttribute('data-cursor', 'false');
  }
  for (const inked of [node, ...node.querySelectorAll('.on-ink')]) inked.classList.remove('on-ink');
  return leave(node, { active, after: motionMs('state') });
}

// settleDelays are the moments a list's cursor block has to be placed again:
// once rows have slid to their new places, and once a struck row has folded
// away. They follow the motion tokens, so reduced motion places it at once.
export function settleDelays() {
  return [motionMs('enter') + 60, leaveMs() + 60];
}

// leaveMs is how long a struck row stays on screen, for whatever has to wait
// for it: the empty panel that takes the list's place.
export function leaveMs() {
  return motionMs('state') + motionMs('enter') * 2;
}

// arrivals tells which rows of a list have just appeared, so they can slide in
// without the first draw of a page animating every row. The first non-empty
// list is the starting state; anything that appears after it is an arrival.
//
//   const arrived = arrivals(() => rows.map((row) => row.id), () => replacing);
//   <article class:rise={arrived.has(row.id)}>
export function arrivals(ids: () => string[], quiet: () => boolean = () => false) {
  let known = new Set<string>();
  let primed = false;
  let fresh = $state(new Set<string>());

  $effect(() => {
    const list = ids();
    const hold = quiet();
    untrack(() => {
      const next = new Set(list);
      if (primed && !hold) {
        const added = list.filter((id) => !known.has(id));
        fresh = new Set(added);
      } else {
        fresh = new Set();
      }
      // An empty list does not prime: a page that opens before its rows land
      // would otherwise treat every row as an arrival.
      if (list.length > 0) primed = true;
      known = next;
    });
  });

  return {
    has(id: string) {
      return fresh.has(id);
    }
  };
}
