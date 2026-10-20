import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, setup } from '@testing-library/svelte';
import { QueryClient, QueryClientProvider } from '@tanstack/svelte-query';

const pageState = vi.hoisted(() => ({ url: new URL('http://localhost/artists'), params: {} }));

vi.mock('$app/state', () => ({
  page: pageState
}));

vi.mock('$app/navigation', () => ({
  afterNavigate: () => {},
  goto: vi.fn(),
  replaceState: vi.fn()
}));

const { default: ArtistsPage } = await import('./+page.svelte');

function opened() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(ArtistsPage, undefined, { wrapper: QueryClientProvider, wrapperProps: { client } });
}

beforeAll(setup);

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe('the artists loading list', () => {
  it('fills its pending shell with a fixed screen-filling count', () => {
    vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>(() => {})));
    opened();

    expect(screen.getByRole('status', { name: 'Loading artists' })).toBeTruthy();
    // 60 rows of 36px fill a tall 4K window; the shell clips whatever the
    // viewport cannot show.
    expect(screen.getByRole('status').querySelectorAll('[aria-hidden="true"]').length).toBe(60);
  });
});

describe('the page heading', () => {
  it('names the room in its level-1 heading', () => {
    vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>(() => {})));
    opened();

    expect(screen.getByRole('heading', { level: 1, name: 'Artists' })).toBeTruthy();
  });
});

describe('artist filters', () => {
  it('clears every filter in one step', async () => {
    pageState.url = new URL('http://localhost/artists?q=burial&scope=held&completeness=complete&sort=most-missing');
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => new Response(JSON.stringify({ items: [] }), { status: 200 }))
    );
    opened();

    await fireEvent.click(await screen.findByRole('button', { name: 'Clear filters' }));

    expect((screen.getByRole('textbox', { name: 'Search artists' }) as HTMLInputElement).value).toBe('');
    expect(screen.queryByRole('button', { name: 'Clear filters' })).toBeNull();
  });
});

describe('artist rows', () => {
  it('shows the compact fraction so the row fits on one line', async () => {
    pageState.url = new URL('http://localhost/artists');
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) => {
        const url = new URL(String(input), 'http://localhost');
        const body = (value: unknown) => new Response(JSON.stringify(value), { status: 200 });
        if (url.pathname === '/api/v1/artists') {
          return body({
            items: [
              {
                id: 'a1',
                musicbrainzId: '46f0f4cd-8aab-4b33-b698-f459faf64190',
                name: 'Burial',
                sortName: 'Burial',
                followed: true,
                followedAt: '2026-01-01T00:00:00Z',
                lastRefreshedAt: '2026-01-01T00:00:00Z',
                refreshStatus: 'completed',
                releaseCount: 13,
                ownedReleaseCount: 13,
                trackCount: 40,
                ownedTrackCount: 40,
                inFlightCount: 0,
                reviewCount: 0,
                needsAttention: false
              }
            ],
            total: 1,
            limit: 50,
            offset: 0,
            followedCount: 1,
            heldCount: 0,
            allCount: 1,
            incompleteCount: 0,
            completeCount: 1,
            attentionCount: 0,
            refreshingCount: 0
          });
        }
        return body({});
      })
    );

    opened();

    expect((await screen.findByLabelText('13 of 13 releases in your library')).textContent).toContain(
      '13/13'
    );
  });
});

describe('an uploader from SoundCloud', () => {
  it('says where they are from without a chip', async () => {
    pageState.url = new URL('http://localhost/artists');
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) => {
        const url = new URL(String(input), 'http://localhost');
        const body = (value: unknown) => new Response(JSON.stringify(value), { status: 200 });
        if (url.pathname === '/api/v1/artists') {
          return body({
            items: [
              {
                id: 'a1',
                musicbrainzId: null,
                name: 'Abo',
                sortName: 'Abo',
                followed: false,
                followedAt: null,
                lastRefreshedAt: null,
                refreshStatus: 'pending',
                source: 'soundcloud',
                releaseCount: 0,
                ownedReleaseCount: 0,
                trackCount: 0,
                ownedTrackCount: 0,
                inFlightCount: 0,
                reviewCount: 0,
                needsAttention: false
              }
            ],
            total: 1,
            limit: 50,
            offset: 0,
            followedCount: 0,
            heldCount: 1,
            allCount: 1,
            incompleteCount: 0,
            completeCount: 0,
            attentionCount: 0,
            refreshingCount: 0
          });
        }
        return body({});
      })
    );

    opened();

    expect(await screen.findByText('from SoundCloud')).toBeTruthy();
    expect(screen.queryByText('not applicable')).toBeNull();
  });
});

describe('artist pictures', () => {
  it('asks only for the pictures the rows say are cached', async () => {
    pageState.url = new URL('http://localhost/artists');
    const card = (id: string, name: string, hasImage: boolean) => ({
      id,
      musicbrainzId: null,
      name,
      sortName: name,
      followed: true,
      followedAt: '2026-01-01T00:00:00Z',
      lastRefreshedAt: '2026-01-01T00:00:00Z',
      refreshStatus: 'completed',
      releaseCount: 1,
      ownedReleaseCount: 1,
      trackCount: 1,
      ownedTrackCount: 1,
      inFlightCount: 0,
      reviewCount: 0,
      needsAttention: false,
      hasImage,
      coverAlbumIds: [],
      coverInks: null
    });
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) => {
        const url = new URL(String(input), 'http://localhost');
        const body = (value: unknown) => new Response(JSON.stringify(value), { status: 200 });
        if (url.pathname === '/api/v1/artists') {
          return body({
            items: [card('a1', 'Burial', true), card('a2', 'Sewerslvt', false)],
            total: 2,
            limit: 50,
            offset: 0,
            followedCount: 2,
            heldCount: 0,
            allCount: 2,
            incompleteCount: 0,
            completeCount: 2,
            attentionCount: 0,
            refreshingCount: 0
          });
        }
        return body({});
      })
    );

    opened();
    await screen.findByText('Sewerslvt');

    const pictures = Array.from(document.querySelectorAll('img')).map((img) => img.getAttribute('src'));
    expect(pictures).toEqual(['/api/v1/artists/a1/image']);
  });

  it('draws the flat placeholder for an artist with no photo and no covers', async () => {
    pageState.url = new URL('http://localhost/artists');
    const card = (id: string, name: string, coverAlbumIds: string[]) => ({
      id,
      musicbrainzId: null,
      name,
      sortName: name,
      followed: true,
      followedAt: '2026-01-01T00:00:00Z',
      lastRefreshedAt: '2026-01-01T00:00:00Z',
      refreshStatus: 'completed',
      releaseCount: 1,
      ownedReleaseCount: 1,
      trackCount: 1,
      ownedTrackCount: 1,
      inFlightCount: 0,
      reviewCount: 0,
      needsAttention: false,
      hasImage: false,
      coverAlbumIds,
      coverInks: null
    });
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) => {
        const url = new URL(String(input), 'http://localhost');
        const body = (value: unknown) => new Response(JSON.stringify(value), { status: 200 });
        if (url.pathname === '/api/v1/artists') {
          return body({
            items: [card('a1', 'Burial', ['r1']), card('a2', 'Sewerslvt', [])],
            total: 2,
            limit: 50,
            offset: 0,
            followedCount: 2,
            heldCount: 0,
            allCount: 2,
            incompleteCount: 0,
            completeCount: 2,
            attentionCount: 0,
            refreshingCount: 0
          });
        }
        return body({});
      })
    );

    opened();
    await screen.findByText('Sewerslvt');

    const placeholders = Array.from(document.querySelectorAll('[data-placeholder]')).map((node) =>
      node.getAttribute('data-placeholder')
    );
    expect(placeholders).toContain('a2');
    expect(placeholders).not.toContain('a1');
  });
});

describe('a 401 on the artist list', () => {
  it('says where to sign in instead of asking to reload', async () => {
    pageState.url = new URL('http://localhost/artists');
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) => {
        const url = new URL(String(input), 'http://localhost');
        if (url.pathname === '/api/v1/artists') {
          return new Response(JSON.stringify({ title: 'unauthorized' }), { status: 401 });
        }
        return new Response('{}', { status: 200 });
      })
    );

    opened();

    await screen.findByText('Not signed in.');
    expect(
      screen.getByText('Open Schall at its usual address and sign in.')
    ).toBeTruthy();
  });
});

describe('a long artist list', () => {
  // jsdom lays nothing out. This stands in for the browser: like a real
  // observer it reports once on every observe, and `atTheEnd` is the reader
  // having scrolled to the bottom of the list.
  let atTheEnd = false;
  let report: (() => void) | undefined;
  class FakeIntersectionObserver {
    constructor(private answer: (entries: IntersectionObserverEntry[]) => void) {}
    observe() {
      report = () => this.answer([{ isIntersecting: atTheEnd } as IntersectionObserverEntry]);
      queueMicrotask(report);
    }
    unobserve() {}
    disconnect() {}
  }

  function artistsAnswering(count: number) {
    const items = Array.from({ length: count }, (_, index) => ({
      id: `a${index}`,
      musicbrainzId: null,
      name: `Artist ${index}`,
      sortName: `Artist ${index}`,
      followed: true,
      followedAt: '2026-01-01T00:00:00Z',
      lastRefreshedAt: '2026-01-01T00:00:00Z',
      refreshStatus: 'completed',
      releaseCount: 1,
      ownedReleaseCount: 1,
      trackCount: 1,
      ownedTrackCount: 1,
      inFlightCount: 0,
      reviewCount: 0,
      needsAttention: false
    }));
    return vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), 'http://localhost');
      const body = (value: unknown) => new Response(JSON.stringify(value), { status: 200 });
      if (url.pathname === '/api/v1/artists') {
        return body({
          items,
          total: count,
          limit: 0,
          offset: 0,
          followedCount: count,
          heldCount: 0,
          allCount: count,
          incompleteCount: 0,
          completeCount: count,
          attentionCount: 0,
          refreshingCount: 0
        });
      }
      return body({});
    });
  }

  it('draws the first rows before the reader scrolls', async () => {
    pageState.url = new URL('http://localhost/artists?scope=followed');
    vi.stubGlobal('IntersectionObserver', FakeIntersectionObserver);
    vi.stubGlobal('fetch', artistsAnswering(300));
    atTheEnd = false;

    opened();

    await screen.findByText('Artist 0');
    expect(screen.queryByText('Artist 119')).not.toBeNull();
    expect(screen.queryByText('Artist 120')).toBeNull();
  });

  it('draws the rest as the reader nears the end', async () => {
    pageState.url = new URL('http://localhost/artists?scope=held');
    vi.stubGlobal('IntersectionObserver', FakeIntersectionObserver);
    vi.stubGlobal('fetch', artistsAnswering(300));
    atTheEnd = false;

    opened();

    await screen.findByText('Artist 0');
    atTheEnd = true;
    report?.();

    expect(await screen.findByText('Artist 299')).not.toBeNull();
  });
});

describe('reads that fail', () => {
  it('says the genre list is unavailable and drops the followed count skeleton', async () => {
    pageState.url = new URL('http://localhost/artists');
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) => {
        const url = new URL(String(input), 'http://localhost');
        if (url.pathname === '/api/v1/artists/genres' || url.pathname === '/api/v1/dashboard') {
          return new Response(JSON.stringify({ title: 'broken' }), { status: 400 });
        }
        return new Response(JSON.stringify({ items: [] }), { status: 200 });
      })
    );
    opened();

    expect(await screen.findByRole('option', { name: 'Genres unavailable' })).toBeTruthy();
    expect(screen.queryByRole('option', { name: 'Any genre' })).toBeNull();
    expect(await screen.findByText('–')).toBeTruthy();
  });
});

describe('the artist list cursor', () => {
  function threeArtists() {
    pageState.url = new URL('http://localhost/artists');
    const row = (id: string, name: string) => ({
      id,
      musicbrainzId: null,
      name,
      sortName: name,
      followed: true,
      followedAt: '2026-01-01T00:00:00Z',
      lastRefreshedAt: '2026-01-01T00:00:00Z',
      refreshStatus: 'completed',
      releaseCount: 2,
      ownedReleaseCount: 1,
      trackCount: 2,
      ownedTrackCount: 1,
      inFlightCount: 0,
      reviewCount: 0,
      needsAttention: false,
      hasImage: false,
      coverAlbumIds: [],
      coverInks: null
    });
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) => {
        const url = new URL(String(input), 'http://localhost');
        const body = (value: unknown) => new Response(JSON.stringify(value), { status: 200 });
        if (url.pathname === '/api/v1/artists') {
          return body({
            items: [row('a1', 'Burial'), row('a2', 'Sewerslvt'), row('a3', 'Autechre')],
            total: 3,
            limit: 50,
            offset: 0,
            followedCount: 3,
            heldCount: 0,
            allCount: 3,
            incompleteCount: 3,
            completeCount: 0,
            attentionCount: 0,
            refreshingCount: 0
          });
        }
        return body({});
      })
    );
  }

  const current = () =>
    Array.from(document.querySelectorAll('a[aria-current="true"]')).map((node) =>
      node.getAttribute('data-artist')
    );

  it('puts no cursor on a row until a key moves it', async () => {
    threeArtists();
    opened();
    await screen.findByText('Burial');

    expect(current()).toEqual([]);
  });

  it('moves down with J and up with K', async () => {
    threeArtists();
    opened();
    await screen.findByText('Burial');

    await fireEvent.keyDown(window, { key: 'j' });
    expect(current()).toEqual(['a1']);
    await fireEvent.keyDown(window, { key: 'j' });
    expect(current()).toEqual(['a2']);
    await fireEvent.keyDown(window, { key: 'k' });
    expect(current()).toEqual(['a1']);
  });

  it('opens the artist under the cursor with Enter', async () => {
    const { goto } = await import('$app/navigation');
    threeArtists();
    opened();
    await screen.findByText('Burial');

    await fireEvent.keyDown(window, { key: 'j' });
    await fireEvent.keyDown(window, { key: 'Enter' });

    expect(goto).toHaveBeenCalledWith('/artists/a1');
  });

  it('leaves J alone while a field has focus', async () => {
    threeArtists();
    opened();
    await screen.findByText('Burial');

    await fireEvent.keyDown(screen.getByRole('textbox', { name: 'Search artists' }), { key: 'j' });

    expect(current()).toEqual([]);
  });
});
