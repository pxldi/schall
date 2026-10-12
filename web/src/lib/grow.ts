import type { Action } from 'svelte/action';

// A long grid draws a screenful at a time. The artist list on a library of
// 1,500 artists took 1.4 s to draw every card at once, and nobody sees more
// than a few hundred before they search. A marker after the last drawn card
// asks for the next step while it is still well below the screen, so the
// reader scrolling down does not reach the end of what is drawn.

/** Cards drawn at a time. 120 fills a 1440 x 900 window twice; a larger screen
 *  draws a second step at once because the marker is already in range. */
export const GROW_STEP = 120;

/** Calls `more` while the node is within two screens of the viewport.
 *  The argument is the number already drawn; a new one re-asks, because an
 *  observer reports a crossing and the marker may still be in range after
 *  the list grew. Without an observer (a test, an old browser) it asks once
 *  per step until the list is whole. */
export const nearEnd: Action<HTMLElement, { drawn: number; more: () => void }> = (node, initial) => {
  let current = initial;
  if (typeof IntersectionObserver === 'undefined') {
    queueMicrotask(() => current.more());
    return {
      update(next) {
        current = next;
        queueMicrotask(() => current.more());
      }
    };
  }
  const observer = new IntersectionObserver(
    (entries) => {
      if (entries.some((entry) => entry.isIntersecting)) current.more();
    },
    { rootMargin: '200% 0px' }
  );
  observer.observe(node);
  return {
    update(next) {
      current = next;
      observer.unobserve(node);
      observer.observe(node);
    },
    destroy() {
      observer.disconnect();
    }
  };
};
