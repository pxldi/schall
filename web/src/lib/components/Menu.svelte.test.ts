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

