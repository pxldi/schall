import { afterEach, describe, expect, it } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { letTheScrollbarComeBack } from '../test-window';
import Fixture from './DialogFixture.svelte';

// Three behaviours, and they are the reason a headless primitive was taken as a
// dependency rather than hand-written for the fourth time.
//
// A modal has to hold the keyboard: while it is open, Tab must not reach a
// control behind it, because a reader on a keyboard cannot see that the control
// is behind anything. It has to give the keyboard back where it found it: the
// trigger, so the reader is standing where they were before they opened it. And
// Escape has to close it, because the scrim is not something a keyboard can
// press.

afterEach(async () => {
  cleanup();
  await letTheScrollbarComeBack();
});

describe('ui/Dialog', () => {
  it('opens from its trigger and names itself', async () => {
    render(Fixture);

    await fireEvent.click(screen.getByRole('button', { name: 'Follow an artist' }));

    const panel = await screen.findByRole('dialog');

    expect(panel.getAttribute('aria-labelledby')).toBeTruthy();
  });

  it('holds the keyboard inside the panel', async () => {
    render(Fixture);
    await fireEvent.click(screen.getByRole('button', { name: 'Follow an artist' }));
    const panel = await screen.findByRole('dialog');

    // The panel takes the focus itself the moment it opens, so nothing behind
    // it is where the next key would land.
    await waitFor(() => expect(panel.contains(document.activeElement)).toBe(true));
    expect(document.activeElement).not.toBe(screen.getByRole('button', { name: 'Outside' }));
  });

  it('gives the keyboard back to the trigger when it closes', async () => {
    render(Fixture);
    const trigger = screen.getByRole('button', { name: 'Follow an artist' });

    // A press moves the focus to the control being pressed before it does
    // anything else, and that is the element the panel has to hand the keyboard
    // back to. `fireEvent` dispatches the event without the focus that comes
    // with a real press, so the focus is set here first.
    trigger.focus();
    await fireEvent.click(trigger);
    await screen.findByRole('dialog');
    await fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));

    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    await waitFor(() => expect(document.activeElement).toBe(trigger));
  });

  it('closes on Escape', async () => {
    render(Fixture);

    await fireEvent.click(screen.getByRole('button', { name: 'Follow an artist' }));
    await screen.findByRole('dialog');
    await fireEvent.keyDown(document, { key: 'Escape' });

    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
  });

  it('draws the panel at the end of the document, not inside its trigger', async () => {
    const { container } = render(Fixture);

    await fireEvent.click(screen.getByRole('button', { name: 'Follow an artist' }));
    const panel = await screen.findByRole('dialog');

    expect(container.contains(panel)).toBe(false);
    expect(document.body.contains(panel)).toBe(true);
  });
});
