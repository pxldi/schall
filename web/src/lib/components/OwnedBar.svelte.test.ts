import { afterEach, describe, expect, it } from 'vitest';
import { cleanup, render, screen } from '@testing-library/svelte';
import OwnedBar from './OwnedBar.svelte';

// The bar-plus-figure every redesigned board draws for "how much of this is
// in the library". The only things worth testing: the figure reads the two
// numbers handed to it, the sentence a screen reader gets names what is being
// counted, and an empty total draws an empty bar rather than dividing by zero.

afterEach(cleanup);

describe('OwnedBar', () => {
  it('shows the figure and the sentence for a partial release', () => {
    render(OwnedBar, { owned: 9, total: 12 });

    const bar = screen.getByLabelText('9 of 12 tracks in your library');
    expect(bar.textContent).toContain('9 of 12');
  });

  it('names what is being counted when the caller says so', () => {
    render(OwnedBar, { owned: 7, total: 12, noun: 'releases' });

    expect(screen.getByLabelText('7 of 12 releases in your library')).toBeTruthy();
  });

  it('draws no dots rather than dividing by zero when there is nothing to own', () => {
    render(OwnedBar, { owned: 0, total: 0 });

    const bar = screen.getByLabelText('0 of 0 tracks in your library');
    expect(bar.querySelectorAll('[data-held]')).toHaveLength(0);
  });

  it('draws one dot per track while they fit, held ones filled', () => {
    render(OwnedBar, { owned: 0, total: 1, width: 120 });

    const dots = screen.getByLabelText('0 of 1 tracks in your library').querySelectorAll('[data-held]');
    expect(dots).toHaveLength(1);
    expect(dots[0].getAttribute('data-held')).toBe('false');
  });

  it('shares a long list across the dots that fit, never showing a part as all', () => {
    render(OwnedBar, { owned: 323, total: 324, width: 120 });

    const dots = [...screen.getByLabelText('323 of 324 tracks in your library').querySelectorAll('[data-held]')];
    expect(dots).toHaveLength(15);
    expect(dots.filter((dot) => dot.getAttribute('data-held') === 'true')).toHaveLength(14);
  });

  it('tightens the fraction for a card footer without shortening the sentence', () => {
    render(OwnedBar, { owned: 13, total: 13, noun: 'releases', compact: true });

    const bar = screen.getByLabelText('13 of 13 releases in your library');
    expect(bar.textContent).toContain('13/13');
  });
});
