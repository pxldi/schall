import { afterEach, beforeAll, describe, expect, it } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { letTheScrollbarComeBack, shimTheMissingBrowser } from '../test-window';
import Fixture from './DropdownMenuFixture.svelte';

// A menu is walked with the arrow keys, and the item under the arrow is marked
// with `data-highlighted` — the same mark the item under the mouse takes, so
// that a reader using one and a reader using the other are looking at the same
// thing.
//
// The list loops. Held on the down arrow, a reader reaches the last item and
// the next press returns to the first, rather than stopping with no sign that
// anything happened. That is the behaviour worth a test, because it is the one
// a hand-written menu forgets.

beforeAll(shimTheMissingBrowser);

afterEach(async () => {
  cleanup();
  await letTheScrollbarComeBack();
});

const menu = () => screen.getByRole('menu');
// The arrow keys are answered by whichever item the keyboard is standing on,
// which moves as the list is walked, so each press is aimed where the keyboard
// actually is rather than at the panel.
const press = (key: string) => fireEvent.keyDown(document.activeElement ?? menu(), { key });
const highlighted = () => document.querySelector('[data-highlighted]')?.textContent?.trim();

describe('ui/DropdownMenu', () => {
  it('opens from its trigger and is announced as a menu', async () => {
    render(Fixture);

    await fireEvent.click(screen.getByRole('button', { name: 'More' }));

    expect(await screen.findByRole('menu')).toBeTruthy();
  });

  it('moves down the list on the down arrow', async () => {
    render(Fixture);
    await fireEvent.click(screen.getByRole('button', { name: 'More' }));
    await screen.findByRole('menu');

    await press('ArrowDown');
    await waitFor(() => expect(highlighted()).toBe('This recording'));

    await press('ArrowDown');
    await waitFor(() => expect(highlighted()).toBe('Anything on this release'));
  });

  it('loops from the last item back to the first', async () => {
    render(Fixture);
    await fireEvent.click(screen.getByRole('button', { name: 'More' }));
    await screen.findByRole('menu');

    // Five items: three decisions, the checkbox item, and the trigger for the
    // nested list. Each press is confirmed before the next, because the item
    // that answers the next one is the item this one moved the keyboard to.
    const items = [
      'This recording',
      'Anything on this release',
      'Anything by this artist',
      'Only matched files',
      'Copy'
    ];

    for (const item of items) {
      await press('ArrowDown');
      await waitFor(() => expect(highlighted()).toBe(item));
    }

    // The sixth press is the one that has to come back round.
    await press('ArrowDown');
    await waitFor(() => expect(highlighted()).toBe('This recording'));
  });

  it('closes on Escape', async () => {
    render(Fixture);
    await fireEvent.click(screen.getByRole('button', { name: 'More' }));
    await screen.findByRole('menu');

    await fireEvent.keyDown(document, { key: 'Escape' });

    await waitFor(() => expect(screen.queryByRole('menu')).toBeNull());
  });

  it('draws the panel outside the box its trigger sits in', async () => {
    const { container } = render(Fixture);

    await fireEvent.click(screen.getByRole('button', { name: 'More' }));
    const panel = await screen.findByRole('menu');

    expect(container.contains(panel)).toBe(false);
    expect(document.body.contains(panel)).toBe(true);
  });
});
