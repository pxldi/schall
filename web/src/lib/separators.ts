import type { Action } from 'svelte/action';

// A line of facts that wraps puts a dot between its parts. Each part carries
// its dot in front, and a part that starts a line must not show it. Clipping
// the row's left edge hid those dots on screen, but they were still in the
// page at x = 11 px, left of the text edge, and read as "· Made by Schall".

/** Which items start a line, from their left edges in order: the first item,
 *  and every item that sits no further right than the one before it. */
export function lineStarts(lefts: number[]): boolean[] {
  return lefts.map((left, index) => index === 0 || left <= lefts[index - 1]);
}

/** Hides the `[data-sep]` dot of every child of a wrapping row that starts a
 *  line, and checks again whenever the row changes size. The dot keeps its
 *  space, so hiding it cannot move the wrap it was hidden for. */
export const lineSeparators: Action<HTMLElement> = (node) => {
  const mark = () => {
    const items = [...node.children] as HTMLElement[];
    const starts = lineStarts(items.map((item) => item.getBoundingClientRect().left));
    items.forEach((item, index) => {
      const dot = item.querySelector<HTMLElement>(':scope > [data-sep]');
      if (dot) dot.style.visibility = starts[index] ? 'hidden' : '';
    });
  };
  mark();
  if (typeof ResizeObserver === 'undefined') return {};
  const observer = new ResizeObserver(mark);
  observer.observe(node);
  for (const child of node.children) observer.observe(child);
  return { destroy: () => observer.disconnect() };
};

/** The same for a row whose dots are drawn by CSS in front of each child, as
 *  `.hero-sub` draws them: a child that starts a line gets `data-line-start`,
 *  and the stylesheet hides that child's dot. */
export const lineStartMarks: Action<HTMLElement> = (node) => {
  const mark = () => {
    const items = [...node.children] as HTMLElement[];
    const starts = lineStarts(items.map((item) => item.getBoundingClientRect().left));
    items.forEach((item, index) => item.toggleAttribute('data-line-start', starts[index]));
  };
  mark();
  if (typeof ResizeObserver === 'undefined') return {};
  // The parts arrive with the data, so a part added later is observed too.
  const observer = new ResizeObserver(mark);
  const watch = () => {
    observer.disconnect();
    observer.observe(node);
    for (const child of node.children) observer.observe(child);
  };
  watch();
  const children = new MutationObserver(() => {
    watch();
    mark();
  });
  children.observe(node, { childList: true });
  return {
    destroy: () => {
      observer.disconnect();
      children.disconnect();
    }
  };
};
