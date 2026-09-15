import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, setup } from '@testing-library/svelte';
import { QueryClient, QueryClientProvider } from '@tanstack/svelte-query';
import type { Playlist, PlaylistEntry, PlayerPairing } from '$lib/api';

const playlistID = 'a1b2c3d4-0000-4000-8000-000000000001';

vi.mock('$app/state', () => ({
  page: {
    params: { id: playlistID },
    url: new URL(`http://localhost/playlists/${playlistID}`)
  }
}));

vi.mock('$app/navigation', () => ({
  afterNavigate: () => {},
  goto: vi.fn(),
  replaceState: vi.fn()
}));

const { default: PlaylistPage } = await import('./+page.svelte');

function playlist(overrides: Partial<Playlist> = {}): Playlist {
  return {
    id: playlistID,
    source: 'spotify',
    name: 'Late night',
    description: '',
    ownerName: 'owner',
    trackCount: 2,
    entryCount: 2,
    ownedCount: 1,
    importedAt: '2026-08-01T00:00:00Z',
    createdAt: '2026-08-01T00:00:00Z',
    ...overrides
  };
}

function entry(overrides: Partial<PlaylistEntry> = {}): PlaylistEntry {
  return {
    id: 'e1',
    position: 1,
    artist: 'Halyard',
    title: 'Weather Report',
    album: 'Long Reach',
    ...overrides
  };
}

function pairing(overrides: Partial<PlayerPairing> = {}): PlayerPairing {
  return {
    configured: true,
    entryCount: 1,
    acquiredCount: 1,
    checked: 1,
    pairedCount: 0,
    pushed: 0,
    lastPushedAt: null,
    unpaired: [],
    ...overrides
  };
}

function opened() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(PlaylistPage, undefined, { wrapper: QueryClientProvider, wrapperProps: { client } });
}

beforeAll(setup);

beforeEach(() => {
  localStorage.clear();
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe('the playlist detail', () => {
  it('keeps the header controls and entry table reserved while loading', () => {
    vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>(() => {})));
    opened();

    expect(screen.getByRole('heading', { level: 1, name: 'Loading playlist' })).toBeTruthy();
    expect((screen.getByRole('button', { name: 'Send to player' }) as HTMLButtonElement).disabled).toBe(true);
    expect((screen.getByRole('button', { name: 'Check player' }) as HTMLButtonElement).disabled).toBe(true);
    expect((screen.getByRole('button', { name: 'Re-import' }) as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByLabelText('Loading playlist entries').querySelectorAll('[aria-hidden="true"]'))
      .toHaveLength(9);
  });

  it('names the playlist in a level-1 heading once it loads', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(() =>
        Promise.resolve(
          new Response(JSON.stringify({ playlist: playlist(), entries: [entry()] }), { status: 200 })
        )
      )
    );
    opened();

    expect(await screen.findByRole('heading', { level: 1, name: 'Late night' })).toBeTruthy();
  });

  it('reserves the pairing area until the player check answers', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL) => {
        if (String(input).includes('navidrome-pairing')) return new Promise<Response>(() => {});
        return Promise.resolve(
          new Response(JSON.stringify({ playlist: playlist(), entries: [entry()] }), { status: 200 })
        );
      })
    );
    opened();

    await screen.findByText('Late night');
    await fireEvent.click(screen.getByRole('button', { name: 'Check player' }));

    const loading = await screen.findByLabelText('Loading player pairing');
    expect(loading.querySelectorAll('[aria-hidden="true"]')).toHaveLength(40);
  });
});
