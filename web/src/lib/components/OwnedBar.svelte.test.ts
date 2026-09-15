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

  it('draws an empty bar rather than dividing by zero when there is nothing to own', () => {
    render(OwnedBar, { owned: 0, total: 0 });

    const bar = screen.getByLabelText('0 of 0 tracks in your library');
    const fill = [...bar.querySelectorAll('span')].find((span) =>
      span.getAttribute('style')?.includes('%')
    );
    expect(fill?.getAttribute('style')).toContain('width: 0%');
  });

  it('tightens the fraction for a card footer without shortening the sentence', () => {
    render(OwnedBar, { owned: 13, total: 13, noun: 'releases', compact: true });

    const bar = screen.getByLabelText('13 of 13 releases in your library');
    expect(bar.textContent).toContain('13/13');
  });
});
