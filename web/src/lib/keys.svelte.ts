// Every key Schall answers, in one registry (ADR 0042). A screen registers
// what its keys do with `useKeys`; a Button given a `key` registers itself
// while it is mounted. The window listener in `handleKey` and the key strip
// both read the same registry, so the strip cannot name a key that does
// something else.
//
// A key is left alone when something else is its reader: a field, an open
// dialog or menu, or a modifier held for the browser. Enter and Space are left
// to a focused control, which already answers them itself.

import { onDestroy } from 'svelte';

export interface KeyBinding {
  /** `KeyboardEvent.key` values, lowercase: 'w', 'enter', ' ', 'arrowdown'. */
  keys: string[];
  /** The keycaps the strip and the sheet draw. Defaults to the first key. A
   *  key in `keys` echoes the cap at its own index, wrapping, so `j k
   *  arrowdown arrowup` with caps `j k` echoes `j` for ArrowDown. */
  caps?: string[];
  /** What the key does, verb first, in the strip: "want", "move". */
  verb: string;
  run: (event: KeyboardEvent) => void;
  /** False while the key would do nothing. The strip draws it dimmed. */
  enabled?: () => boolean;
  /** Left off the strip, still listed on the key sheet. */
  hidden?: boolean;
  /** Answered even while a button or a link has focus (never in a field). */
  fromControls?: boolean;
  /** Sort position in the strip; lower comes first. Default 50. Bindings
   *  with the same order follow the screen, by `element`. */
  order?: number;
  /** The control the key presses, so the strip lists keys in the order the
   *  controls sit on screen. */
  element?: () => Element | undefined;
}

interface Scope {
  id: number;
  /** The shell's keys: the tabs, search, the sheet. Listed apart on the sheet
   *  and never on the strip. */
  global: boolean;
  bindings: () => KeyBinding[];
}

let nextId = 0;
// A plain array, with a counter the strip reads so it hears scopes come and
// go. Each scope's getter is reactive on its own.
const scopes: Scope[] = [];
const registry = $state({ version: 0 });

/** The key that fired last, for the keycaps that echo it. `at` changes on
 *  every press so a key pressed twice echoes twice. */
export const echo = $state({ key: '', at: 0 });

/** Whether focus is in a field, which the strip says instead of naming keys
 *  that would not fire. */
export const focus = $state({ typing: false });

let listening = false;

// One window listener for the whole application, added with the first scope,
// so a screen mounted on its own (a test, a storybook) answers its keys too.
function listen() {
  if (listening || typeof window === 'undefined') return;
  listening = true;
  window.addEventListener('keydown', handleKey);
}

/** Registers bindings for as long as the calling component is mounted. The
 *  getter is read on every press, so it can close over component state. */
export function useKeys(bindings: () => KeyBinding[], options: { global?: boolean } = {}) {
  const scope: Scope = { id: nextId++, global: options.global ?? false, bindings };
  listen();
  scopes.push(scope);
  registry.version++;
  onDestroy(() => {
    const at = scopes.indexOf(scope);
    if (at >= 0) scopes.splice(at, 1);
    registry.version++;
  });
}

/** Order: the explicit order first, then where the controls sit on screen,
 *  then the order the bindings were registered in. */
function byPlace(bindings: KeyBinding[]): KeyBinding[] {
  return bindings
    .map((binding, index) => ({ binding, index, element: binding.element?.() }))
    .sort((a, b) => {
      const order = (a.binding.order ?? 50) - (b.binding.order ?? 50);
      if (order) return order;
      if (a.element && b.element && a.element !== b.element) {
        return a.element.compareDocumentPosition(b.element) & Node.DOCUMENT_POSITION_FOLLOWING ? -1 : 1;
      }
      return a.index - b.index;
    })
    .map(({ binding }) => binding);
}

/** What the strip draws: every visible binding of the screen, in order. */
export function stripBindings(): KeyBinding[] {
  void registry.version;
  return byPlace(
    scopes
      .filter((scope) => !scope.global)
      .flatMap((scope) => scope.bindings())
      .filter((binding) => !binding.hidden)
  );
}

/** What the key sheet lists: the screen's keys, then the ones that work
 *  everywhere. */
export function sheetBindings(): { screen: KeyBinding[]; everywhere: KeyBinding[] } {
  void registry.version;
  const screen = byPlace(scopes.filter((scope) => !scope.global).flatMap((scope) => scope.bindings()));
  const everywhere = scopes
    .filter((scope) => scope.global)
    .flatMap((scope) => scope.bindings())
    .filter((binding) => !binding.hidden);
  return { screen, everywhere };
}

/** The name a keycap prints for a key. */
export function capLabel(key: string): string {
  switch (key) {
    case ' ':
      return 'Space';
    case 'escape':
      return 'Esc';
    case 'backspace':
      return 'Bksp';
    case 'enter':
      return 'Enter';
    case 'arrowdown':
      return 'Down';
    case 'arrowup':
      return 'Up';
    default:
      return key.length === 1 ? key.toUpperCase() : key;
  }
}

/** A key as `aria-keyshortcuts` spells it: `W`, `Enter`, `Space`. */
export function ariaKey(key: string): string {
  if (key === ' ') return 'Space';
  return key.length === 1 ? key.toUpperCase() : key[0].toUpperCase() + key.slice(1);
}

const TYPING =
  'input:not([type="checkbox"]):not([type="radio"]):not([type="range"]), textarea, select, [contenteditable]:not([contenteditable="false"])';
const CONTROL =
  'button, a[href], summary, input, [role="button"], [role="radio"], [role="checkbox"], [role="tab"], [role="option"], [role="switch"], [role="slider"], [role="menuitem"]';
const OVERLAY = '[role="dialog"], [role="alertdialog"], dialog[open], [role="menu"], [role="listbox"]';

/** Whether a key belongs to something other than the registry. */
export function leftAlone(event: KeyboardEvent): boolean {
  if (event.defaultPrevented || event.isComposing) return true;
  if (event.metaKey || event.ctrlKey || event.altKey) return true;
  const target = event.target instanceof Element ? event.target : null;
  if (target?.closest(`${TYPING}, input[type="range"], [role="slider"]`)) return true;
  if (target?.closest(OVERLAY)) return true;
  if (typeof document !== 'undefined' && document.querySelector('[role="dialog"][aria-modal="true"], dialog[open], [role="menu"]')) {
    return true;
  }
  return false;
}

function normalise(event: KeyboardEvent): string {
  return event.key.toLowerCase();
}

/** Finds the binding a key press means, newest scope first. */
function bindingFor(event: KeyboardEvent): { binding: KeyBinding; key: string } | undefined {
  const key = normalise(event);
  // Shift picks the symbol on a key ('?' is Shift+/ on most layouts); with a
  // letter it is somebody typing a capital, which is not a command here.
  if (event.shiftKey && /^[a-z0-9]$/.test(key)) return undefined;
  const target = event.target instanceof Element ? event.target : null;
  const onControl = Boolean(target?.closest(CONTROL));
  for (let at = scopes.length - 1; at >= 0; at--) {
    for (const binding of scopes[at].bindings()) {
      if (!binding.keys.includes(key)) continue;
      if (onControl && !binding.fromControls && (key === 'enter' || key === ' ' || key.startsWith('arrow'))) {
        return undefined;
      }
      if (onControl && !binding.fromControls && key.length === 1 && target?.closest('[role="radio"], [role="slider"]')) {
        return undefined;
      }
      return { binding, key };
    }
  }
  return undefined;
}

/** The window's keydown handler, added by `listen`. */
export function handleKey(event: KeyboardEvent) {
  // Esc leaves a field on the page, as the strip says while one has focus. A
  // field in a dialog or menu belongs to that surface's own Esc.
  const target = event.target instanceof HTMLElement ? event.target : null;
  if (
    event.key === 'Escape' &&
    !event.defaultPrevented &&
    target?.matches(TYPING) &&
    !target.closest(OVERLAY)
  ) {
    target.blur();
    return;
  }
  if (leftAlone(event)) return;
  const found = bindingFor(event);
  if (!found) return;
  const { binding, key } = found;
  // Claimed whether or not it does anything now, so a key that is the
  // screen's never opens search instead.
  event.preventDefault();
  if (binding.enabled && !binding.enabled()) return;
  const caps = binding.caps ?? [binding.keys[0]];
  echo.key = caps[binding.keys.indexOf(key) % caps.length] ?? key;
  echo.at++;
  binding.run(event);
}

/** Keeps `focus.typing` true while a field holds focus. The shell mounts it. */
export function trackFocus(): () => void {
  const update = () => {
    const active = document.activeElement;
    focus.typing = Boolean(active instanceof Element && active.matches(TYPING) && !active.closest(OVERLAY));
  };
  const left = () => queueMicrotask(update);
  document.addEventListener('focusin', update);
  document.addEventListener('focusout', left);
  update();
  return () => {
    document.removeEventListener('focusin', update);
    document.removeEventListener('focusout', left);
  };
}
