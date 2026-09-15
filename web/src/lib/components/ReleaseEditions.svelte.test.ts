import { afterEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen } from '@testing-library/svelte';
import type { ReleaseEdition } from '$lib/api';
import ReleaseEditions from './ReleaseEditions.svelte';

// An edition is one pressing of a record. MusicBrainz holds several of them per
// release, Schall works from exactly one, and choosing between them is a
// comparison — so every pressing is a block on screen and no press is needed to
// see the next one.
//
// The editions endpoint carries four things and no more: the title, the date,
// the country and the MusicBrainz status. These tests hold that line: a fact the
// endpoint has no answer for is said to be missing, never left blank.

afterEach(cleanup);

function edition(overrides: Partial<ReleaseEdition> = {}): ReleaseEdition {
  return {
    musicbrainzReleaseId: 'e1000000-0000-4000-8000-000000000001',
    title: 'Spirit of Eden',
    releaseDate: '1988-03-16',
    country: 'GB',
    status: 'Official',
    ...overrides
  };
}

const two = [
  edition(),
  edition({
    musicbrainzReleaseId: 'e1000000-0000-4000-8000-000000000002',
    title: 'Spirit of Eden (2012 remaster)',
    releaseDate: '2012-10-01',
    country: 'US'
  })
];

describe('ReleaseEditions', () => {
  it('draws every pressing at once, with no control to open', () => {
    render(ReleaseEditions, {
      editions: two,
      selectedId: two[0].musicbrainzReleaseId,
      onselect: () => {}
    });

    expect(screen.getByText('Spirit of Eden')).toBeTruthy();
    expect(screen.getByText('Spirit of Eden (2012 remaster)')).toBeTruthy();
    // The only buttons on screen choose a pressing. Nothing has to be expanded
    // first, so no control carries `aria-expanded`.
    for (const control of screen.getAllByRole('button')) {
      expect(control.getAttribute('aria-expanded')).toBeNull();
    }
  });

  it('states the facts MusicBrainz carries, in the same three places per block', () => {
    const { container } = render(ReleaseEditions, {
      editions: [edition()],
      selectedId: null,
      onselect: () => {}
    });

    const labels = [...container.querySelectorAll('dt')].map((node) => node.textContent);
    expect(labels).toEqual(['Released', 'Country', 'Status']);
    expect(screen.getByText('1988-03-16')).toBeTruthy();
    expect(screen.getByText('GB')).toBeTruthy();
    expect(screen.getByText('Official')).toBeTruthy();
  });

  // The point of the fixed three places: a pressing MusicBrainz knows almost
  // nothing about keeps its three labels and says so under each one. A blank
  // cell in a column of dates reads as a page that failed to draw.
  it('says a fact is missing rather than leaving the place blank', () => {
    const { container } = render(ReleaseEditions, {
      editions: [
        edition({ title: 'Unknown pressing', releaseDate: undefined, country: undefined, status: undefined })
      ],
      selectedId: null,
      onselect: () => {}
    });

    const labels = [...container.querySelectorAll('dt')].map((node) => node.textContent);
    expect(labels).toEqual(['Released', 'Country', 'Status']);
    expect(screen.getAllByText('Not recorded')).toHaveLength(3);
    for (const value of container.querySelectorAll('dd')) {
      expect(value.textContent?.trim()).not.toBe('');
    }
  });

  it('names the pressing Schall is working from, and offers the others', async () => {
    const chosen = vi.fn();
    render(ReleaseEditions, {
      editions: two,
      selectedId: two[0].musicbrainzReleaseId,
      onselect: chosen
    });

    expect(screen.getByText('Selected')).toBeTruthy();
    const controls = screen.getAllByRole('button', { name: 'Use this' });
    expect(controls).toHaveLength(1);

    await fireEvent.click(controls[0]);
    expect(chosen).toHaveBeenCalledWith(two[1].musicbrainzReleaseId);
  });

  it('shows the selected pressing’s barcode and why it was picked, on its block alone', () => {
    render(ReleaseEditions, {
      editions: two,
      selectedId: two[0].musicbrainzReleaseId,
      barcode: '0724386008428',
      selectionReason: 'it is the only official British pressing',
      onselect: () => {}
    });

    expect(screen.getByText('Barcode')).toBeTruthy();
    expect(screen.getByText('0724386008428')).toBeTruthy();
    expect(
      screen.getByText(/Picked automatically: it is the only official British pressing/)
    ).toBeTruthy();
  });

  it('stops taking presses while a choice is being written', () => {
    render(ReleaseEditions, {
      editions: two,
      selectedId: two[0].musicbrainzReleaseId,
      pending: true,
      onselect: () => {}
    });

    expect(screen.getByRole('button', { name: 'Use this' }).hasAttribute('disabled')).toBe(true);
  });

  // A release with no MusicBrainz release group is music somebody ripped
  // themselves. It has no pressings, which is an ordinary permanent state and
  // not a failure, so it is said in those words and no error is drawn.
  it('says a release outside MusicBrainz has no pressings to compare', () => {
    render(ReleaseEditions, { editions: [], selectedId: null, linked: false, onselect: () => {} });

    expect(screen.getByText(/not linked to MusicBrainz/)).toBeTruthy();
  });

  it('reports a failed lookup in the words the API used', () => {
    render(ReleaseEditions, {
      editions: [],
      selectedId: null,
      error: 'edition lookup is unavailable',
      onselect: () => {}
    });

    expect(screen.getByText(/edition lookup is unavailable/)).toBeTruthy();
  });

  // MusicBrainz is a third party and the list can fail to arrive. The pressing
  // Schall is working from comes from the release endpoint instead, so the page
  // can always say which one it is on, and the failure is a line under it
  // rather than in place of it.
  it('keeps the pressing in use on screen when the list does not arrive', () => {
    render(ReleaseEditions, {
      editions: [],
      selectedId: 'e1000000-0000-4000-8000-000000000001',
      selected: edition(),
      error: 'edition lookup is unavailable',
      selectionReason: 'chosen by hand',
      onselect: () => {}
    });

    expect(screen.getByText('Spirit of Eden')).toBeTruthy();
    expect(screen.getByText('Selected')).toBeTruthy();
    expect(screen.getByText(/Picked automatically: chosen by hand/)).toBeTruthy();
    // The words a reader gets rather than the sentinel the server sent, and the
    // sentinel one press away underneath it.
    expect(screen.getByText('Schall could not read the editions of this release.')).toBeTruthy();
    expect(screen.getByText('What the server said')).toBeTruthy();
  });

  // The list arrived and the pressing in use is not in it. It is put first
  // rather than left off, because a page that cannot say which pressing it is
  // working from is worse than one that cannot offer the alternatives.
  it('puts the pressing in use first when the list leaves it out', () => {
    const { container } = render(ReleaseEditions, {
      editions: [two[1]],
      selectedId: two[0].musicbrainzReleaseId,
      selected: two[0],
      onselect: () => {}
    });
    const titles = [...container.querySelectorAll('[data-edition] > div > p')].map((node) =>
      node.textContent?.trim()
    );
    expect(titles[0]).toBe('Spirit of Eden');
    expect(titles).toHaveLength(2);
  });
});
