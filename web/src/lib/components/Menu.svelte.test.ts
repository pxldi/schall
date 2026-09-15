import { afterEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen } from '@testing-library/svelte';
import { tick } from 'svelte';
import Menu, { type MenuItem } from '$lib/components/Menu.svelte';

// The menu holds one decision offered at several scopes, and the two things
// that have to be right are the ones a reader without a mouse depends on: where
// the keyboard goes when the panel opens, and where it comes back to when the
// panel closes. A menu that opens and strands the caret on the page behind it
// is unusable by keyboard while looking perfectly correct on screen.

afterEach(() => {
  cleanup();
  restoreWidth();
});

describe('Menu', () => {
  it('is one closed trigger until it is asked for', () => {
    render(Menu, scopes());

    const trigger = screen.getByRole('button', { name: 'Not interested' });

    expect(trigger.getAttribute('aria-haspopup')).toBe('menu');
    expect(trigger.getAttribute('aria-expanded')).toBe('false');
    expect(screen.queryByRole('menu')).toBeNull();
  });

  it('opens on a press, with the panel named after the decision', async () => {
    render(Menu, scopes());

    await fireEvent.click(screen.getByRole('button', { name: 'Not interested' }));

    expect(screen.getByRole('menu', { name: 'Not interested' })).toBeTruthy();
    expect(screen.getAllByRole('menuitem')).toHaveLength(3);
    expect(
      screen.getByRole('button', { name: 'Not interested' }).getAttribute('aria-expanded')
    ).toBe('true');
  });

  // Enter, Space and Down all mean "open and start at the top". Up means "open
  // and start at the bottom", which is the only reason the trigger reads the
  // keys itself instead of letting them become a press.
  it('opens on the first item for Enter, Space and Down', async () => {
    for (const key of ['Enter', ' ', 'ArrowDown']) {
      render(Menu, scopes());
      const trigger = screen.getByRole('button', { name: 'Not interested' });

      await fireEvent.keyDown(trigger, { key });
      await tick();

      expect(document.activeElement?.textContent).toContain('This track only');
      cleanup();
    }
  });

  it('opens on the last item for Up', async () => {
    render(Menu, scopes());

    await fireEvent.keyDown(screen.getByRole('button', { name: 'Not interested' }), {
      key: 'ArrowUp'
    });
    await tick();

    expect(document.activeElement?.textContent).toContain('Anything by this artist');
  });

  it('moves one item at a time and wraps at both ends', async () => {
    const panel = await opened();

    await fireEvent.keyDown(panel, { key: 'ArrowDown' });
    expect(document.activeElement?.textContent).toContain('Anything from this release');

    await fireEvent.keyDown(panel, { key: 'ArrowDown' });
    expect(document.activeElement?.textContent).toContain('Anything by this artist');

    // Past the end is the beginning again.
    await fireEvent.keyDown(panel, { key: 'ArrowDown' });
    expect(document.activeElement?.textContent).toContain('This track only');

    await fireEvent.keyDown(panel, { key: 'ArrowUp' });
    expect(document.activeElement?.textContent).toContain('Anything by this artist');
  });

  it('jumps to the ends on Home and End', async () => {
    const panel = await opened();

    await fireEvent.keyDown(panel, { key: 'End' });
    expect(document.activeElement?.textContent).toContain('Anything by this artist');

    await fireEvent.keyDown(panel, { key: 'Home' });
    expect(document.activeElement?.textContent).toContain('This track only');
  });

  // A refused scope stays in the list, so the set of scopes is the same length
  // on every row and the second line can say why this one is not offered. The
  // caret steps over it in both directions rather than landing on it.
  it('steps over an item that cannot be chosen', async () => {
    const chosen: string[] = [];
    const panel = await opened({
      items: [
        { label: 'This track only', onchoose: () => chosen.push('recording') },
        {
          label: 'Anything on this label',
          detail: 'Not known for this release',
          disabled: true,
          onchoose: () => chosen.push('label')
        },
        { label: 'Anything by this artist', onchoose: () => chosen.push('artist') }
      ]
    });

    await fireEvent.keyDown(panel, { key: 'ArrowDown' });
    expect(document.activeElement?.textContent).toContain('Anything by this artist');

    await fireEvent.keyDown(panel, { key: 'ArrowUp' });
    expect(document.activeElement?.textContent).toContain('This track only');

    // And a press on it is not a choice either.
    await fireEvent.click(screen.getByRole('menuitem', { name: /Anything on this label/ }));
    expect(chosen).toEqual([]);
    expect(screen.queryByRole('menu')).toBeTruthy();
  });

  // Every item is a real button, so Enter and Space on the keyboard arrive here
  // as the press this test fires.
  it('closes and applies the choice on a press, then hands focus back', async () => {
    const chosen: string[] = [];
    await opened({
      items: [
        { label: 'This track only', onchoose: () => chosen.push('recording') },
        { label: 'Anything by this artist', onchoose: () => chosen.push('artist') }
      ]
    });

    await fireEvent.click(screen.getByRole('menuitem', { name: /Anything by this artist/ }));
    await tick();

    expect(chosen).toEqual(['artist']);
    expect(screen.queryByRole('menu')).toBeNull();
    // The trigger is still on the page here, because nothing removed the row it
    // stands on. A page that does remove it owns where focus lands next.
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Not interested' }));
  });

  it('closes on Escape without choosing anything, and returns focus to the trigger', async () => {
    const chosen: string[] = [];
    const panel = await opened({
      items: [{ label: 'This track only', onchoose: () => chosen.push('recording') }]
    });

    await fireEvent.keyDown(panel, { key: 'Escape' });

    expect(chosen).toEqual([]);
    expect(screen.queryByRole('menu')).toBeNull();
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Not interested' }));
  });

  // Tab is not taken. The menu closes, focus is put back on the trigger, and the
  // press then carries on through the page from there.
  it('closes on Tab and lets the press continue from the trigger', async () => {
    const panel = await opened();

    const taken = !(await fireEvent.keyDown(panel, { key: 'Tab' }));

    expect(taken).toBe(false);
    expect(screen.queryByRole('menu')).toBeNull();
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Not interested' }));
  });

  it('closes when something outside it is pressed', async () => {
    await opened();

    await fireEvent.pointerDown(document.body);

    expect(screen.queryByRole('menu')).toBeNull();
  });

  it('stays open when the press lands inside the panel', async () => {
    const panel = await opened();

    await fireEvent.pointerDown(panel);

    expect(screen.queryByRole('menu')).toBeTruthy();
  });

  // The panel grows away from the page edge its trigger is nearest: right edges
  // aligned under the square at the end of a row, left edges under a labelled
  // button that starts at the left of whatever holds it.
  it('anchors to the edge the trigger is nearest', async () => {
    await opened();
    expect(screen.getByRole('menu').getAttribute('data-side')).toBe('right');
    cleanup();

    await opened({ trigger: 'labelled' });
    expect(screen.getByRole('menu').getAttribute('data-side')).toBe('left');
  });

  // Room is measured off the trigger at the moment the panel opens. Near the
  // foot of the window the panel opens upwards instead; nothing else about it
  // changes.
  it('opens upwards when there is no room below the trigger', async () => {
    const height = window.innerHeight;
    try {
      const { container } = render(Menu, scopes());
      const trigger = screen.getByRole('button', { name: 'Not interested' });
      vi.spyOn(trigger, 'getBoundingClientRect')
        .mockReturnValueOnce({ bottom: 700 } as DOMRect)
        .mockReturnValueOnce({ bottom: 0 } as DOMRect);
      await fireEvent.keyDown(trigger, { key: 'ArrowDown' });
      await tick();
      expect(screen.getByRole('menu').getAttribute('data-placement')).toBe('top');
      container.remove();

      const second = render(Menu, scopes());
      const secondTrigger = screen.getByRole('button', { name: 'Not interested' });
      vi.spyOn(secondTrigger, 'getBoundingClientRect').mockReturnValue({ bottom: 0 } as DOMRect);
      await fireEvent.keyDown(secondTrigger, { key: 'ArrowDown' });
      await tick();
      expect(screen.getByRole('menu').getAttribute('data-placement')).toBe('bottom');
      second.container.remove();
    } finally {
      Object.defineProperty(window, 'innerHeight', { configurable: true, value: height });
    }
  });

  describe('on a phone', () => {
    // Below 640px the same menu is re-laid as a bottom sheet: identical items,
    // order and wording, plus a scrim to catch a tap and an explicit way out,
    // because on touch there is no Escape key and the scrim is not self-evident.
    it('adds a way out that the desktop panel does not have', async () => {
      narrow();
      const chosen: string[] = [];
      await opened({
        items: [{ label: 'This track only', onchoose: () => chosen.push('recording') }]
      });

      const items = screen.getAllByRole('menuitem');

      expect(items.map((item) => item.textContent?.trim())).toEqual([
        'This track only',
        'Cancel'
      ]);

      await fireEvent.click(items[1]);
      await tick();

      expect(chosen).toEqual([]);
      expect(screen.queryByRole('menu')).toBeNull();
      expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Not interested' }));
    });

    it('closes when the scrim is pressed', async () => {
      narrow();
      const { container } = await openedIn();

      const scrim = container.querySelector('[data-scrim]');
      expect(scrim).toBeTruthy();
      await fireEvent.mouseDown(scrim as Element);

      expect(screen.queryByRole('menu')).toBeNull();
    });

    // The grip is the third way out, beside the scrim and Esc. Dragged far
    // enough down, letting go closes the sheet; short of that it springs back,
    // because a finger that moved a little was scrolling, not dismissing.
    it('closes when the grip is dragged down and let go', async () => {
      narrow();
      const { container } = await openedIn();

      const followed = await drag(grip(container), 60);

      expect(followed).toBe('translateY(60px)');
      expect(screen.queryByRole('menu')).toBeNull();
      expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Not interested' }));
    });

    it('stays open when the grip barely moved', async () => {
      narrow();
      const { container } = await openedIn();

      const followed = await drag(grip(container), 12);

      expect(followed).toBe('translateY(12px)');
      // Still here, and back where it was rather than left sitting low.
      expect(screen.getByRole('menu').style.transform).toBe('');
    });

    it('says what the sheet was opened on, because it covers the row', async () => {
      narrow();
      await opened({ subject: 'Weightless' });

      expect(screen.getByText("Don't recommend · Weightless")).toBeTruthy();
    });
  });
});

// The three scopes of the first menu Schall has, as the Recommended view will
// hand them over.
function scopes(over: Partial<Record<string, unknown>> = {}) {
  const items: MenuItem[] = [
    { label: 'This track only', detail: 'Weightless — Marconi Union', onchoose: () => {} },
    {
      label: 'Anything from this release',
      detail: 'Ambient Transmissions Vol. 2',
      onchoose: () => {}
    },
    { label: 'Anything by this artist', detail: 'Marconi Union', onchoose: () => {} }
  ];
  return { label: 'Not interested', heading: "Don't recommend", items, ...over };
}

// Opened the way a keyboard opens it, which is also the state every test about
// what happens next starts from.
async function opened(over: Partial<Record<string, unknown>> = {}) {
  return (await openedIn(over)).panel;
}

async function openedIn(over: Partial<Record<string, unknown>> = {}) {
  const { container } = render(Menu, scopes(over));

  await fireEvent.keyDown(screen.getByRole('button', { name: 'Not interested' }), {
    key: 'ArrowDown'
  });
  await tick();

  return { container, panel: screen.getByRole('menu') };
}

// The bar at the top of the sheet, which is the only part of it a drag is read
// from.
function grip(container: HTMLElement) {
  const found = container.querySelector('[data-grip]');
  expect(found).toBeTruthy();
  return found as HTMLElement;
}

// A press, a move and a release, `by` pixels down the screen, answering with
// how far the sheet had followed the finger before it was let go. jsdom has no
// PointerEvent, so these are mouse events under the pointer names: all the
// component reads of one is where it happened, and a MouseEvent carries that.
async function drag(target: HTMLElement, by: number) {
  const from = 400;
  const press = (name: string, y: number) =>
    target.dispatchEvent(new MouseEvent(name, { bubbles: true, clientY: y }));

  press('pointerdown', from);
  press('pointermove', from + by);
  await tick();
  const followed = (screen.getByRole('menu') as HTMLElement).style.transform;

  press('pointerup', from + by);
  await tick();
  return followed;
}

// jsdom has no matchMedia, so the component draws the panel unless a test says
// the window is a narrow one. The stub answers the one question the component
// asks and reports no later change.
function narrow() {
  Object.defineProperty(window, 'matchMedia', {
    configurable: true,
    writable: true,
    value: vi.fn().mockImplementation((query: string) => ({
      matches: true,
      media: query,
      addEventListener: () => {},
      removeEventListener: () => {}
    }))
  });
}

function restoreWidth() {
  delete (window as { matchMedia?: unknown }).matchMedia;
}
