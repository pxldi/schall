import { afterEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/svelte';

import WantToggle from '$lib/components/WantToggle.svelte';

// The + on a track. What is pinned here is the order of things: the toggle
// shows the new state on the press, before the server answers, and goes back
// when the server refuses. The turning and drawing are the stylesheet's.

afterEach(cleanup);

function toggle(props: Partial<{ wanted: boolean; onwant: () => unknown; onunwant: () => unknown; label: [string, string] }> = {}) {
  return render(WantToggle, {
    props: { wanted: false, onwant: () => {}, onunwant: () => {}, title: 'Teardrop', ...props }
  });
}

describe('pressing + on a track nobody wants yet', () => {
  it('asks for the want and shows it wanted before the answer', async () => {
    let answer: () => void = () => {};
    const onwant = vi.fn(() => new Promise<void>((resolve) => (answer = resolve)));
    toggle({ onwant });

    const button = screen.getByRole('button', { name: 'Want Teardrop' });
    expect(button.getAttribute('aria-pressed')).toBe('false');
    await fireEvent.click(button);

    expect(onwant).toHaveBeenCalledOnce();
    expect(button.getAttribute('aria-pressed')).toBe('true');
    answer();
  });

  it('takes no second press while the first is in flight', async () => {
    const onwant = vi.fn(() => new Promise<void>(() => {}));
    const onunwant = vi.fn();
    toggle({ onwant, onunwant });

    const button = screen.getByRole('button', { name: 'Want Teardrop' });
    await fireEvent.click(button);
    await fireEvent.click(button);

    expect(onwant).toHaveBeenCalledOnce();
    expect(onunwant).not.toHaveBeenCalled();
  });

  it('goes back to unwanted when the server refuses', async () => {
    const onwant = vi.fn(() => Promise.reject(new Error('no')));
    toggle({ onwant });

    const button = screen.getByRole('button', { name: 'Want Teardrop' });
    await fireEvent.click(button);

    await waitFor(() => expect(button.getAttribute('aria-pressed')).toBe('false'));
  });
});

describe('pressing a wanted track', () => {
  it('asks to stop wanting it', async () => {
    const onwant = vi.fn();
    const onunwant = vi.fn();
    toggle({ wanted: true, onwant, onunwant });

    const button = screen.getByRole('button', { name: 'Want Teardrop' });
    expect(button.getAttribute('aria-pressed')).toBe('true');
    expect(button.getAttribute('title')).toBe('Not wanted');
    await fireEvent.click(button);

    expect(onunwant).toHaveBeenCalledOnce();
    expect(onwant).not.toHaveBeenCalled();
    expect(button.getAttribute('aria-pressed')).toBe('false');
  });
});

describe('a toggle with words', () => {
  it('reads the word for the state it shows', async () => {
    toggle({ label: ['Want it', 'Wanted'] });

    const button = screen.getByRole('button', { name: 'Want it' });
    await fireEvent.click(button);

    expect(button.getAttribute('aria-pressed')).toBe('true');
    expect(screen.getByRole('button', { name: 'Wanted' })).toBe(button);
  });
});
