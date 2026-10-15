import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, setup } from '@testing-library/svelte';
import { QueryClient, QueryClientProvider } from '@tanstack/svelte-query';
import ArtistsRail from './ArtistsRail.svelte';
import { artistsList } from '$lib/list-memory.svelte';

// The artists list beside an artist page.

function artist(id: string, name: string) {
  return {
    id,
    name,
    followed: true,
    releaseCount: 4,
    ownedReleaseCount: 3,
    trackCount: 0,
    ownedTrackCount: 0,
    inFlightCount: 0,
    reviewCount: 0,
    needsAttention: false,
    hasImage: false,
    coverAlbumIds: [],
    coverInks: null
  };
}

function list(names: [string, string][]) {
  return { items: names.map(([id, name]) => artist(id, name)), total: names.length, limit: 500, offset: 0 };
}

function drawn(openID: string) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(ArtistsRail, { openID }, { wrapper: QueryClientProvider, wrapperProps: { client } });
}

beforeAll(setup);

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  artistsList.choice = { scope: '', search: '', tab: '', genre: '', sort: 'name' };
});

describe('the artists list beside an artist', () => {
  it('marks the open artist', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify(list([['a', 'Bully'], ['b', 'Souly']])))));
    drawn('b');

    const open = await screen.findByRole('link', { name: /Souly/ });
    expect(open.getAttribute('aria-current')).toBe('page');
    expect(screen.getByRole('link', { name: /Bully/ }).getAttribute('aria-current')).toBeNull();
  });

  it('links each artist to their page', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify(list([['a', 'Bully']])))));
    drawn('');

    expect((await screen.findByRole('link', { name: /Bully/ })).getAttribute('href')).toBe('/artists/a');
  });

  it('asks for the list the reader picked the artist from', async () => {
    const fetch = vi.fn(async () => new Response(JSON.stringify(list([]))));
    vi.stubGlobal('fetch', fetch);
    artistsList.choice = { scope: 'followed', search: 'sou', tab: '', genre: '', sort: 'name' };
    drawn('');

    await vi.waitFor(() => expect(fetch).toHaveBeenCalled());
    const asked = String((fetch.mock.calls[0] as unknown[])[0]);
    expect(asked).toContain('scope=followed');
    expect(asked).toContain('q=sou');
  });

  it('narrows to the names that match the filter', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify(list([['a', 'Bully'], ['b', 'Souly']])))));
    drawn('');
    await screen.findByRole('link', { name: /Bully/ });

    await fireEvent.input(screen.getByRole('searchbox', { name: 'Filter artists' }), { target: { value: 'sou' } });

    expect(screen.queryByRole('link', { name: /Bully/ })).toBeNull();
    expect(screen.getByRole('link', { name: /Souly/ })).toBeTruthy();
  });
});
