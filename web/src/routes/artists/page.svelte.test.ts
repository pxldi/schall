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

describe('the artists loading grid', () => {
  it('fills its pending shell with a fixed screen-filling count', () => {
    vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>(() => {})));
    opened();

    expect(screen.getByRole('status', { name: 'Loading artists' })).toBeTruthy();
    // 210 tiles is 23 columns of 9 rows, which fills a 4K screen; the shell
    // clips whatever the viewport cannot show.
    expect(screen.getByRole('status').querySelectorAll('[aria-hidden="true"]').length).toBe(210);
  });
});

describe('the page heading', () => {
  it('names the room in a hidden level-1 heading', () => {
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

describe('artist cards', () => {
  it('shows the compact fraction so the footer fits on one line', async () => {
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

    await screen.findByText('No sign-in on this connection.');
    expect(
      screen.getByText('Open Schall at its usual address and sign in there.')
    ).toBeTruthy();
  });
});
