import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, setup, within } from '@testing-library/svelte';
import { QueryClient, QueryClientProvider } from '@tanstack/svelte-query';
import type { Playlist } from '$lib/api';

// Named without the leading `+`, like the other colocated route tests:
// SvelteKit reads every `+` file in a route folder as a route file.
//
// This is the followed-playlists page. A failed fetch used to fall through to
// the empty state and say "No playlists followed", which is a different claim
// from "the request failed". That is what this test guards.

vi.mock('$app/state', () => ({
  page: { params: {}, url: new URL('http://localhost/playlists') }
}));

vi.mock('$app/navigation', () => ({
  afterNavigate: () => {},
  goto: vi.fn(),
  replaceState: vi.fn()
}));

const { default: PlaylistsPage } = await import('./+page.svelte');

function playlist(overrides: Partial<Playlist> = {}): Playlist {
  return {
    id: 'a1b2c3d4-0000-4000-8000-000000000001',
    source: 'spotify',
    name: 'Late night',
    description: '',
    ownerName: 'owner',
    trackCount: 20,
    entryCount: 20,
    ownedCount: 12,
    importedAt: '2026-08-01T00:00:00Z',
    createdAt: '2026-08-01T00:00:00Z',
    coverReleaseId: null,
    inks: null,
    ...overrides
  };
}

function opened() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(PlaylistsPage, undefined, { wrapper: QueryClientProvider, wrapperProps: { client } });
  return client;
}

beforeAll(setup);

beforeEach(() => {
  localStorage.clear();
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe('the playlist list', () => {
  it('fills the viewport with playlist skeleton rows while loading', () => {
    vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>(() => {})));
    opened();

    const loading = screen.getByLabelText('Loading playlists');
    expect(loading.getAttribute('aria-busy')).toBe('true');
    expect(loading.querySelectorAll('[data-placeholder-row]')).toHaveLength(4);
  });

  it('names the room in a hidden level-1 heading', () => {
    vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>(() => {})));
    opened();

    expect(screen.getByRole('heading', { level: 1, name: 'Playlists' })).toBeTruthy();
  });

  it('mounts the selected pane with its loading state immediately', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL) => {
        if (String(input).includes('/recommendations')) return new Promise<Response>(() => {});
        return new Promise<Response>(() => {});
      })
    );
    opened();

    await fireEvent.click(screen.getByRole('button', { name: 'Recommended' }));

    expect(screen.getByLabelText('Loading recommendations')).toBeTruthy();
  });

  it('shows every followed playlist when the request succeeds', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => new Response(JSON.stringify({ items: [playlist()] }), { status: 200 }))
    );
    opened();

    expect(await screen.findByText('Late night')).toBeTruthy();
  });

  it('explains an owner that is still unresolved during import', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () =>
        new Response(JSON.stringify({ items: [playlist({ ownerName: '', importedAt: null })] }), {
          status: 200
        })
      )
    );
    opened();

    expect(await screen.findByText('Importing playlist…')).toBeTruthy();
    expect(screen.queryByText(/unknown/)).toBeNull();
  });

  it('says Schall made the weekly list instead of reading it as importing', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(
        async () =>
          new Response(
            JSON.stringify({
              items: [playlist({ source: 'weekly', ownerName: '', importedAt: null })]
            }),
            { status: 200 }
          )
      )
    );
    opened();

    expect(await screen.findByText('Made by Schall')).toBeTruthy();
    expect(screen.queryByText('Importing playlist…')).toBeNull();
  });

  it('shows the failure instead of "No playlists followed" when the request fails', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(
        async () =>
          new Response(JSON.stringify({ title: 'library is unavailable' }), { status: 503 })
      )
    );
    opened();

    expect(await screen.findByText("Couldn't load the library.")).toBeTruthy();
    expect(screen.queryByText('No playlists followed')).toBeNull();
  });

  it('asks again by itself and shows the list once the retry succeeds', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const fetch = vi
      .fn()
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ title: 'library is unavailable' }), { status: 503 })
      )
      .mockResolvedValue(new Response(JSON.stringify({ items: [playlist()] }), { status: 200 }));
    vi.stubGlobal('fetch', fetch);
    opened();

    await screen.findByText("Couldn't load the library.");
    await vi.advanceTimersByTimeAsync(4000);
    expect(await screen.findByText('Late night')).toBeTruthy();

    vi.useRealTimers();
  });

  // The browser's confirm() blocked the tab and looked like nothing else in
  // Schall. The question is now the page's own panel.
  it('asks in its own panel before it stops following a list', async () => {
    const sent: string[] = [];
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        if (init?.method === 'DELETE') {
          sent.push(String(input));
          return new Response(null, { status: 204 });
        }
        return new Response(JSON.stringify({ items: [playlist()] }), { status: 200 });
      })
    );
    opened();

    await fireEvent.click(await screen.findByRole('button', { name: 'Unfollow' }));
    const question = await screen.findByRole('dialog');
    expect(question.textContent).toContain('Unfollow “Late night”?');
    // "Cancel" beside "Stop following" did not say which of the two it undid.
    expect(within(question).getByRole('button', { name: 'Keep following' })).toBeTruthy();
    expect(sent).toEqual([]);

    await fireEvent.click(within(question).getByRole('button', { name: 'Unfollow' }));

    await vi.waitFor(() =>
      expect(sent).toEqual(['/api/v1/playlists/a1b2c3d4-0000-4000-8000-000000000001'])
    );
  });
});

describe('the safety-net poll', () => {
  function playlistReads(fetch: ReturnType<typeof vi.fn>) {
    return fetch.mock.calls.filter(
      ([input]) => new URL(String(input), 'http://localhost').pathname === '/api/v1/playlists'
    ).length;
  }

  async function readsOver(items: Playlist[]) {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const fetch = vi.fn(async () => new Response(JSON.stringify({ items }), { status: 200 }));
    vi.stubGlobal('fetch', fetch);
    opened();
    await screen.findByText(items[0].name);
    const before = playlistReads(fetch);
    await vi.advanceTimersByTimeAsync(31_000);
    const after = playlistReads(fetch);
    vi.useRealTimers();
    return after - before;
  }

  it('asks again while a Spotify playlist waits on its first import', async () => {
    expect(await readsOver([playlist({ importedAt: null })])).toBeGreaterThan(0);
  });

  // Weekly, new releases, manual and Navidrome lists never get an import
  // date, so waiting on one would poll for as long as the page is open.
  it('stays quiet over lists that are never imported', async () => {
    expect(
      await readsOver([
        playlist({ name: 'Weekly', source: 'weekly', importedAt: null }),
        playlist({ id: 'n', name: 'New', source: 'new_releases', importedAt: null }),
        playlist({ id: 'm', name: 'Mine', source: 'manual', importedAt: null }),
        playlist({ id: 'd', name: 'Player', source: 'navidrome', importedAt: null })
      ])
    ).toBe(0);
  });
});
