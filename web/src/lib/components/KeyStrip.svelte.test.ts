import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, setup, within } from '@testing-library/svelte';
import KeysFixture from './KeysFixture.svelte';

// The keys of a screen and the strip that names them come from one registry
// ($lib/keys). These are the promises that makes: the strip names what a
// press does, a press does it, and a key belongs to whatever else is reading
// it first.

beforeAll(setup);

afterEach(() => {
  cleanup();
});

function drawn(disabled = false) {
  const onwant = vi.fn();
  const onmove = vi.fn();
  render(KeysFixture, { onwant, onmove, disabled });
  return { onwant, onmove };
}

describe('the key strip', () => {
  it('names every key the screen answers, in order', () => {
    drawn();

    const strip = screen.getByRole('contentinfo', { name: 'Keys on this screen' });
    const entries = within(strip)
      .getAllByRole('listitem')
      .map((item) => [...item.querySelectorAll('kbd, span:last-child')].map((part) => part.textContent?.trim()).join(' '));
    expect(entries).toEqual(['J K move', 'W want']);
  });

  it('offers every key on one sheet', () => {
    drawn();

    expect(screen.getByRole('button', { name: /all keys/ })).toBeTruthy();
  });
});

describe('a key press', () => {
  it('presses the button that carries the key', async () => {
    const { onwant } = drawn();

    await fireEvent.keyDown(document.body, { key: 'w' });

    expect(onwant).toHaveBeenCalledOnce();
  });

  it('runs a key the screen registered', async () => {
    const { onmove } = drawn();

    await fireEvent.keyDown(document.body, { key: 'j' });

    expect(onmove).toHaveBeenCalledOnce();
  });

  it('does nothing while the button it would press is disabled', async () => {
    const { onwant } = drawn(true);

    await fireEvent.keyDown(document.body, { key: 'w' });

    expect(onwant).not.toHaveBeenCalled();
  });

  it('is left to a field that has focus', async () => {
    const { onwant } = drawn();

    await fireEvent.keyDown(screen.getByRole('textbox', { name: 'Search terms' }), { key: 'w' });

    expect(onwant).not.toHaveBeenCalled();
  });

  it('leaves a field on Esc', async () => {
    drawn();
    const field = screen.getByRole('textbox', { name: 'Search terms' });
    field.focus();

    await fireEvent.keyDown(field, { key: 'Escape' });

    expect(document.activeElement).not.toBe(field);
  });

  it('is left to the browser while a modifier is held', async () => {
    const { onwant } = drawn();

    await fireEvent.keyDown(document.body, { key: 'w', ctrlKey: true });
    await fireEvent.keyDown(document.body, { key: 'w', metaKey: true });
    await fireEvent.keyDown(document.body, { key: 'w', altKey: true });

    expect(onwant).not.toHaveBeenCalled();
  });

  it('claims the key, so typing it does not also open search', async () => {
    drawn();
    const event = new KeyboardEvent('keydown', { key: 'w', bubbles: true, cancelable: true });

    document.body.dispatchEvent(event);

    expect(event.defaultPrevented).toBe(true);
  });

  it('echoes on the keycap that names it', async () => {
    drawn();
    const raf = vi.spyOn(window, 'requestAnimationFrame').mockImplementation((callback) => {
      callback(0);
      return 0;
    });

    await fireEvent.keyDown(document.body, { key: 'j' });

    const strip = screen.getByRole('contentinfo', { name: 'Keys on this screen' });
    const cap = [...strip.querySelectorAll('kbd')].find((kbd) => kbd.textContent?.trim() === 'J');
    expect(cap?.hasAttribute('data-echo')).toBe(true);
    raf.mockRestore();
  });
});
