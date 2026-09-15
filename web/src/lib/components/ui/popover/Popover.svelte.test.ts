import { afterEach, beforeAll, describe, expect, it } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { letTheScrollbarComeBack, shimTheMissingBrowser } from '../test-window';
import Fixture from './PopoverFixture.svelte';

// A popover has to be dismissible without a pointer and it has to be drawn
// outside whatever box its trigger sits in. The second is the whole reason for
// the `Portal`: a popover opened from a table cell that hides its overflow is
// otherwise cut off at that cell's edge, and half of it is simply not on screen.

beforeAll(shimTheMissingBrowser);

afterEach(async () => {
  cleanup();
  await letTheScrollbarComeBack();
});

describe('ui/Popover', () => {
  it('opens from its trigger', async () => {
    render(Fixture);

    await fireEvent.click(screen.getByRole('button', { name: 'Filter' }));

    expect(await screen.findByLabelText('Search sources')).toBeTruthy();
  });

  it('closes on Escape', async () => {
    render(Fixture);

    await fireEvent.click(screen.getByRole('button', { name: 'Filter' }));
    await screen.findByLabelText('Search sources');
    await fireEvent.keyDown(document, { key: 'Escape' });

    await waitFor(() => expect(screen.queryByLabelText('Search sources')).toBeNull());
  });

  it('draws the panel outside the box its trigger sits in', async () => {
    const { container } = render(Fixture);

    await fireEvent.click(screen.getByRole('button', { name: 'Filter' }));
    const panel = await screen.findByLabelText('Search sources');

    expect(container.contains(panel)).toBe(false);
    expect(document.body.contains(panel)).toBe(true);
  });

  it('is told to keep itself inside the window', async () => {
    render(Fixture);

    await fireEvent.click(screen.getByRole('button', { name: 'Filter' }));
    const panel = (await screen.findByLabelText('Search sources')).closest('[data-popover-content]');

    // The panel says which side it settled on. That attribute only exists
    // because the positioning library chose the side, which is what replaces
    // the fixed number a hand-written menu had to guess with.
    expect(panel?.getAttribute('data-side')).toBeTruthy();
  });
});
