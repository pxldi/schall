import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, setup } from '@testing-library/svelte';
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
    expect(loading.querySelectorAll('[aria-hidden="true"]')).toHaveLength(4);
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

  it('shows the failure instead of "No playlists followed" when the request fails', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(
        async () =>
          new Response(JSON.stringify({ title: 'library is unavailable' }), { status: 503 })
      )
    );
    opened();

    expect(await screen.findByText('Schall could not read the library.')).toBeTruthy();
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

    await screen.findByText('Schall could not read the library.');
    await vi.advanceTimersByTimeAsync(4000);
    expect(await screen.findByText('Late night')).toBeTruthy();

    vi.useRealTimers();
  });
});
