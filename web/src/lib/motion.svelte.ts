// The motion scale is written in `web/src/styles.css` and every duration and
// curve in the application comes from it. Most of it is drawn by CSS alone: a
// class names one of the motions, the stylesheet times it, and no script is
// involved. Two things CSS cannot answer on its own are answered here.
//
// The first is *when* something changed. A card arriving and a card that was
// already there are the same element to a stylesheet, so a state mark whose
// role has just gone from "busy" to "failed" is indistinguishable from the same
// mark drawn for the first time. The difference matters: a list of a thousand
// rows drawing itself is not an event, and one row in it changing is the only
// event on the screen.
//
// The second is a duration a script has to hand to something else — Svelte's
// own `fade`, which takes a number of milliseconds rather than reading CSS.

import { untrack } from 'svelte';
import { reducedMotion } from '$lib/utils';

// changed watches one value and says whether it has just become something else.
//
// It is deliberately quiet on the first run. Whatever the value is when a
// component is first drawn is not a change; it is the starting state, and
// treating it as a change is what makes a whole list animate on every visit.
//
// Call it once while a component is setting up, read `.now` in the class list,
// and clear it when the animation ends:
//
//   const role = changed(() => file.state);
//   <span class:turn={role.now} onanimationend={role.settle}>
export function changed(read: () => unknown) {
  let now = $state(false);
  let seen: unknown;
  let first = true;

  $effect(() => {
    const next = read();
    // The bookkeeping is untracked so that this effect depends on the value
    // being watched and on nothing else. Reading `seen` as a dependency would
    // make every write to it run the effect again, which is a loop.
    untrack(() => {
      if (!first && seen !== next) now = true;
      first = false;
      seen = next;
    });
  });

  return {
    get now() {
      return now;
    },
    settle() {
      now = false;
    }
  };
}

// motionMs reads one step of the scale as a plain number of milliseconds, for
// the one place a duration has to be handed to a script rather than written in
// CSS: Svelte's `fade` transition, which takes a number.
//
// It reads the custom property at the moment it is asked rather than once at
// start-up, so it cannot fall out of step with the stylesheet, and it returns 0
// for a reader who has asked for less motion — which is Svelte's own way of
// saying "no transition". A document that has no computed styles to read, which
// is a test environment or a server render, also gets 0 and therefore no motion
// at all, which is the safe answer in both.
export function motionMs(step: 'state' | 'surface' | 'enter' | 'read'): number {
  if (reducedMotion() || typeof document === 'undefined') return 0;
  const value = getComputedStyle(document.documentElement)
    .getPropertyValue(`--motion-${step}`)
    .trim();
  const figure = Number.parseFloat(value);
  // Anything unreadable is no motion rather than a guess.
  if (!Number.isFinite(figure)) return 0;
  // The scale is written in milliseconds and does not arrive that way. A
  // custom property is handed back exactly as the stylesheet holds it, and the
  // production build shortens `150ms` to `.15s` because it is four characters
  // cheaper — so `.15` read as milliseconds would be a modal that fades in a
  // sixth of a millisecond. Seconds are the unit unless `ms` is written.
  return value.endsWith('ms') ? figure : figure * 1000;
}

// leave is the out transition for one row taken off a list because somebody
// acted on it: a want stopped, a suggestion wanted. The row fades and slides a
// few pixels aside, then closes the gap it leaves, so the rows under it move up
// instead of jumping. Closing the gap has to animate height, which is the one
// place a motion here moves layout; the rows below are the thing that moves.
//
// `active` is false for a row leaving because the whole list was replaced, by
// a new page or a new filter. Twenty-five rows folding away at once is not an
// event, so they go on the cut.
//
//   {#each items as item (item.id)}
//     <div out:leave={{ active: leaving.has(item.id) }}>
//
// `after` holds the row in place first, for a row whose own control is still
// drawing the answer to the press that removed it.
export function leave(
  node: Element,
  { active = true, after = 0 }: { active?: boolean; after?: number } = {}
) {
  const half = active ? motionMs('enter') : 0;
  if (half === 0) return { duration: 0 };
  const style = getComputedStyle(node);
  const box = ['height', 'padding-top', 'padding-bottom', 'border-top-width', 'border-bottom-width'].map(
    (property) => [property, Number.parseFloat(style.getPropertyValue(property)) || 0] as const
  );
  const shape = (t: number) => 1 - (1 - t) ** 3;
  return {
    delay: after,
    duration: half * 2,
    // An out transition runs t from 1 down to 0: the first half fades the row,
    // the second closes the gap it stood in.
    css: (t: number) => {
      const seen = shape(Math.max(0, t * 2 - 1));
      const room = shape(Math.min(1, t * 2));
      const sizes = box.map(([property, value]) => `${property}: ${value * room}px;`).join(' ');
      return `overflow: hidden; min-height: 0; opacity: ${seen}; transform: translateX(${(1 - seen) * -8}px); ${sizes}`;
    }
  };
}
