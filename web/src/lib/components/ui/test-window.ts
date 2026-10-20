// What the shimmed document these component tests run in does not have.
//
// The tests run under jsdom, which is a document built in Node rather than a
// browser. It draws nothing, so it has no layout, and two things every floating
// surface in this directory asks for are missing from it: `ResizeObserver`,
// which the positioning library uses to watch the trigger and the panel change
// size, and `Element.scrollIntoView`, which a list of choices calls to bring
// the row under the arrow keys into view.
//
// Both are answered here with something that does nothing. Nothing in these
// tests reads a position or a scroll offset — a test that asserted where a
// panel landed would be asserting jsdom's arithmetic, not ours — so an observer
// that reports nothing and a scroll that scrolls nothing are enough to let the
// components mount and be driven by a keyboard.
//
// It is a plain module rather than a setup file because the test runner is
// configured with no setup files, and one import at the top of a test is
// cheaper to follow than a hook nobody can see from the file they are reading.
export function shimTheMissingBrowser() {
  if (!('ResizeObserver' in globalThis)) {
    globalThis.ResizeObserver = class {
      observe() {}
      unobserve() {}
      disconnect() {}
    } as unknown as typeof ResizeObserver;
  }

  if (typeof Element !== 'undefined' && !Element.prototype.scrollIntoView) {
    Element.prototype.scrollIntoView = function scrollIntoView() {};
  }
}

// The wait every overlay test takes after it puts its component away.
//
// An overlay takes the page's own scrollbar away while it is open and gives it
// back a beat after it closes — 24 milliseconds, which is there so that a panel
// destroyed and rebuilt in the same tick does not make the page jump. That beat
// is longer than the runner takes to put the shimmed document away, and the
// timer then writes to a document that is gone. The runner reports that as a
// failure of whichever file was running, which is why the wait is taken by hand
// rather than left to chance.
export function letTheScrollbarComeBack() {
  return new Promise((resolve) => setTimeout(resolve, 50));
}
