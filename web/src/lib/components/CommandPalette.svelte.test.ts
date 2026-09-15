import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, setup } from '@testing-library/svelte';
import { QueryClient, QueryClientProvider } from '@tanstack/svelte-query';
import type { GlobalSearchResults } from '$lib/api';

// The palette is a keyboard, a request and a link. So the two boundaries stubbed
// are the two it has — SvelteKit's router, which is where a chosen result goes,
// and `fetch` — and what is asserted is what is on screen and where a press
// took the reader.

const routed = vi.hoisted(() => ({ goto: vi.fn() }));

vi.mock('$app/navigation', () => ({ goto: routed.goto }));

const { default: CommandPalette } = await import('$lib/components/CommandPalette.svelte');

function answer(overrides: Partial<GlobalSearchResults> = {}): GlobalSearchResults {
  return {
    query: 'radiohead',
    limit: 5,
    artists: [
      {
        id: 'a1',
        name: 'Radiohead',
        sortName: 'Radiohead',
        followed: true,
        fileCount: 148,
        releaseCount: 9
      }
    ],
    releases: [
      {
        id: 'r1',
        title: 'Kid A',
        artistId: 'a1',
        artistName: 'Radiohead',
        albumType: 'album',
        releaseDate: '2000-10-02',
        trackCount: 10,
        ownedTrackCount: 8
      }
    ],
    tracks: [],
    files: [],
    playlists: [],
    totals: { artists: 3, releases: 24, tracks: 0, files: 0, playlists: 0 },
    ...overrides
  };
}

let asked: string[] = [];

function answering(body: GlobalSearchResults | { status: number }) {
  vi.stubGlobal(
    'fetch',
    vi.fn(async (path: string) => {
      asked.push(path);
      if ('status' in body) {
        return new Response(JSON.stringify({ title: 'the index is rebuilding' }), {
          status: body.status
        });
      }
      return new Response(JSON.stringify(body), { status: 200 });
    })
  );
}

function mount() {
  // The client is per test: a cache shared between them would answer the second
  // one out of the first one's results without asking anything.
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(CommandPalette, undefined, {
    wrapper: QueryClientProvider,
    wrapperProps: { client }
  });
}

/** The one press that brings the palette up, from wherever the reader is. */
async function summon() {
  await fireEvent.keyDown(window, { key: 'k', metaKey: true });
}

function field() {
  return screen.getByRole('combobox', { name: 'Search Schall' });
}

async function ask(query: string) {
  await summon();
  await fireEvent.input(field(), { target: { value: query } });
  return field();
}

/** The row the arrow keys are on, which the field points at. */
function marked() {
  return field().getAttribute('aria-activedescendant');
}

beforeAll(setup);

// jsdom draws nothing, so it has no scrolling to do and no method for it. The
// palette keeps its selection in view; the document under test cannot.
beforeAll(() => {
  Element.prototype.scrollIntoView = vi.fn();
});

beforeEach(() => {
  asked = [];
  routed.goto.mockClear();
  vi.useFakeTimers({ shouldAdvanceTime: true });
  answering(answer());
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  cleanup();
});

/** Waits past the settling of a keystroke and for the answer to arrive. */
async function answered() {
  await vi.advanceTimersByTimeAsync(200);
  return screen.findByText('Kid A');
}

describe('CommandPalette', () => {
  it('is not on the page until it is summoned', () => {
    mount();

    expect(screen.queryByRole('combobox')).toBeNull();
  });

  it('comes up on the one press, from wherever the reader is', async () => {
    mount();

    await summon();

    expect(field()).toBeTruthy();
  });

  // Summoned and empty, the palette is one line high. A hint line saying a query
  // is needed is microcopy restating a constraint, and there is none.
  it('says nothing at all before a character is typed', async () => {
    mount();

    await summon();

    expect(screen.queryByText(/↑↓ move/)).toBeNull();
    expect(asked).toEqual([]);
  });

  it('asks the one question once the typing settles', async () => {
    mount();
    await ask('radiohead');
    await answered();

    expect(asked).toEqual(['/api/v1/search?q=radiohead']);
  });

  // A stale list under a newer query is the one thing a palette must never show.
  it('shows no answer at all while a newer query is still settling', async () => {
    mount();
    await ask('radiohead');
    await answered();

    await fireEvent.input(field(), { target: { value: 'radioheads' } });

    expect(screen.queryByText('Kid A')).toBeNull();
    expect(screen.getByText(/Searching five categories/)).toBeTruthy();
  });

  it('groups what answered under the heading for its kind', async () => {
    mount();
    await ask('radiohead');
    await answered();

    expect(screen.getByRole('group', { name: 'Artists' })).toBeTruthy();
    expect(screen.getByRole('group', { name: 'Releases' })).toBeTruthy();
  });

  // A category that answered with nothing is absent, not listed as empty.
  it('draws no heading for a category that answered with nothing', async () => {
    mount();
    await ask('radiohead');
    await answered();

    expect(screen.queryByRole('group', { name: 'Playlists' })).toBeNull();
  });

  // "View all results" is a row now, not a header button: the arrow keys can
  // reach it and Enter activates it, the same as any other result.
  it('says how many there are and where the rest of them live', async () => {
    mount();
    await ask('radiohead');
    await answered();

    const row = screen.getByRole('option', { name: /View all results in Releases/ });
    expect(row.getAttribute('href')).toBe('/library?view=releases&q=radiohead');
  });

  it('marks the first result as soon as an answer is on screen', async () => {
    mount();
    await ask('radiohead');
    await answered();

    expect(marked()).toBe('palette-artists-a1');
  });

  // Artists answered 3 for a category that fits 1, so the row after it is the
  // escape for Artists before the arrow keys reach Releases.
  it('moves the mark down through every result and view-all row as one list', async () => {
    mount();
    const box = await ask('radiohead');
    await answered();

    await fireEvent.keyDown(box, { key: 'ArrowDown' });
    expect(marked()).toBe('palette-view-all-artists');

    await fireEvent.keyDown(box, { key: 'ArrowDown' });
    expect(marked()).toBe('palette-releases-r1');
  });

  // The list does not wrap at either end.
  it('stays on the last entry when there is nothing below it', async () => {
    mount();
    const box = await ask('radiohead');
    await answered();

    await fireEvent.keyDown(box, { key: 'ArrowDown' });
    await fireEvent.keyDown(box, { key: 'ArrowDown' });
    await fireEvent.keyDown(box, { key: 'ArrowDown' });
    await fireEvent.keyDown(box, { key: 'ArrowDown' });

    expect(marked()).toBe('palette-view-all-releases');
  });

  it('stays on the first result when there is nothing above it', async () => {
    mount();
    const box = await ask('radiohead');
    await answered();

    await fireEvent.keyDown(box, { key: 'ArrowUp' });

    expect(marked()).toBe('palette-artists-a1');
  });

  it('opens the marked result and closes', async () => {
    mount();
    const box = await ask('radiohead');
    await answered();

    await fireEvent.keyDown(box, { key: 'Enter' });

    expect(routed.goto).toHaveBeenCalledWith('/artists/a1');
    expect(screen.queryByRole('combobox')).toBeNull();
  });

  // The escape belonging to a category used to be a jump Tab made from
  // wherever the mark sat. It is a row now: the arrow keys reach it like any
  // other result and Enter opens it the same way.
  it('reaches a category’s remaining results as a row, with the arrow keys and Enter', async () => {
    mount();
    const box = await ask('radiohead');
    await answered();

    await fireEvent.keyDown(box, { key: 'ArrowDown' });
    expect(marked()).toBe('palette-view-all-artists');

    await fireEvent.keyDown(box, { key: 'Enter' });

    expect(routed.goto).toHaveBeenCalledWith('/artists?q=radiohead');
  });

  // Tab used to be caught and repurposed; now it is left alone; a keyboard
  // reader gets the field, the results and the close button in that order.
  it('does not intercept Tab any more', async () => {
    mount();
    const box = await ask('radiohead');
    await answered();

    const notPrevented = await fireEvent.keyDown(box, { key: 'Tab' });

    expect(notPrevented).toBe(true);
  });

  // The first esc with text in the field clears the field; the second closes.
  it('clears the field on the first escape', async () => {
    mount();
    const box = await ask('radiohead');
    await answered();

    await fireEvent.keyDown(box, { key: 'Escape' });

    expect((field() as HTMLInputElement).value).toBe('');
    expect(screen.queryByRole('combobox')).not.toBeNull();
  });

  it('closes on the escape after the field is empty', async () => {
    mount();
    const box = await ask('radiohead');
    await answered();

    await fireEvent.keyDown(box, { key: 'Escape' });
    await fireEvent.keyDown(field(), { key: 'Escape' });

    expect(screen.queryByRole('combobox')).toBeNull();
  });

  it('says what was searched when nothing matched', async () => {
    answering(
      answer({
        query: 'kidb',
        artists: [],
        releases: [],
        totals: { artists: 0, releases: 0, tracks: 0, files: 0, playlists: 0 }
      })
    );
    mount();
    await ask('kidb');
    await vi.advanceTimersByTimeAsync(200);

    expect(await screen.findByText(/Nothing matches/)).toBeTruthy();
  });

  // A failed query is reported in the panel, which is the surface that failed,
  // and not in a toast.
  it('reports a failed query in the panel it happened in', async () => {
    answering({ status: 500 });
    mount();
    await ask('radiohead');
    await vi.advanceTimersByTimeAsync(200);

    // `role="status"` rather than `alert`: the note is a live region that is
    // read out when it arrives without taking focus from the box being typed
    // in, which is what every other failure on the screen does.
    expect(await screen.findByText('Schall could not finish that.')).toBeTruthy();
    // No Try again button: the only thing it did was ask the question that had
    // just failed, so the palette asks by itself and says it is asking.
    expect(screen.queryByRole('button', { name: 'Try again' })).toBeNull();
    expect(screen.getByText('Trying again.')).toBeTruthy();
  });

  // The legend is always present once there is a panel to read it under, so the
  // keys are learnable by looking. Tab is not a Schall shortcut any more, so it
  // is not on it.
  it('shows the legend under an answer', async () => {
    mount();
    await ask('radiohead');
    await answered();

    expect(screen.getByText('↑↓ move')).toBeTruthy();
    expect(screen.getByText('↵ open')).toBeTruthy();
    expect(screen.queryByText(/all in category/)).toBeNull();
  });
});

describe('CommandPalette, focus', () => {
  it('has a close button for a reader with no Escape key', async () => {
    mount();
    await summon();

    const closeButton = screen.getByRole('button', { name: 'Close search' });
    await fireEvent.click(closeButton);

    expect(screen.queryByRole('combobox')).toBeNull();
  });

  it('gives focus back to the control that summoned it', async () => {
    mount();
    const opener = document.createElement('button');
    opener.textContent = 'Search';
    document.body.append(opener);
    opener.focus();

    await summon();
    await fireEvent.keyDown(field(), { key: 'Escape' });

    expect(document.activeElement).toBe(opener);
    opener.remove();
  });

  it('falls back to the body when the opener is gone', async () => {
    mount();
    const opener = document.createElement('button');
    document.body.append(opener);
    opener.focus();

    await summon();
    opener.remove();
    await fireEvent.keyDown(field(), { key: 'Escape' });

    expect(document.activeElement).toBe(document.body);
  });
});
