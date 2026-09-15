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
