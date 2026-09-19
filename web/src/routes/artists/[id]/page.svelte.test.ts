import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, setup, waitFor } from '@testing-library/svelte';
import { QueryClient, QueryClientProvider } from '@tanstack/svelte-query';
import type { ArtistDetail } from '$lib/api';

// Named without the leading `+`, like the other colocated route tests:
// SvelteKit reads every `+` file in a route folder as a route file.
//
// This is the page about one artist. Two things on it are new and worth a test
// in a browser-less runner: the genres MusicBrainz's community voted them, and
// the few lines Wikipedia has about who they are. The second carries a licence
// with it — the words are CC BY-SA — so the line naming Wikipedia and linking
// the article is part of the feature rather than decoration.

const artistID = 'a1b2c3d4-0000-4000-8000-000000000001';

vi.mock('$app/state', () => ({
  page: {
    params: { id: artistID },
    url: new URL(`http://localhost/artists/${artistID}`)
  }
}));

vi.mock('$app/navigation', () => ({
  afterNavigate: () => {},
  goto: vi.fn(),
  replaceState: vi.fn()
}));

const { default: ArtistPage } = await import('./+page.svelte');

function detail(overrides: Partial<ArtistDetail> = {}): ArtistDetail {
  return {
    id: artistID,
    musicbrainzId: 'b1c2d3e4-0000-4000-8000-000000000001',
    name: 'Talk Talk',
    sortName: 'Talk Talk',
    followed: true,
    followedAt: '2026-01-01T00:00:00Z',
    lastRefreshedAt: '2026-08-01T00:00:00Z',
    refreshStatus: 'completed',
    albumCount: 5,
    monitorLevel: 'everything',
    wantMissing: false,
    discography: { releases: 5, owned: 3, missing: 2, dismissed: 0, counted: 5, loading: 0 },
    genres: [],
    ...overrides
  };
}

function answering(artist: ArtistDetail) {
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), 'http://localhost');
      const body = (value: unknown) => new Response(JSON.stringify(value), { status: 200 });
      if (url.pathname === `/api/v1/artists/${artistID}`) return body(artist);
      return body({ items: [], total: 0, limit: 24, offset: 0 });
    })
  );
}

/** Like `answering`, but also answers the writes the follow control can send —
 * follow, unfollow and set-monitor — and tells the caller which of them was
 * asked for. */
function answeringWithActions(
  artist: ArtistDetail,
  seen: { method: string; pathname: string; body?: string }[]
) {
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), 'http://localhost');
      const method = (init?.method ?? 'GET').toUpperCase();
      const body = (value: unknown) => new Response(JSON.stringify(value), { status: 200 });
      if (url.pathname === `/api/v1/artists/${artistID}` && method === 'GET') return body(artist);
      if (url.pathname === `/api/v1/artists/${artistID}/monitor` && method === 'PUT') {
        seen.push({ method, pathname: url.pathname, body: init?.body as string });
        return body({ artistId: artistID, monitorLevel: 'main' });
      }
      if (url.pathname === `/api/v1/artists/${artistID}/follow` && method === 'POST') {
        seen.push({ method, pathname: url.pathname });
        return body({ ...artist, followed: true, monitorLevel: 'everything' });
      }
      if (url.pathname === `/api/v1/artists/${artistID}/want-missing` && method === 'PUT') {
        seen.push({ method, pathname: url.pathname, body: init?.body as string });
        return body({
          artistId: artistID,
          wantMissing: JSON.parse(String(init?.body ?? '{}')).on === true,
          releases: 2,
          wanted: 3,
          alreadyWanted: 0,
          owned: 0,
          unresolvable: 0
        });
      }
      if (url.pathname === `/api/v1/artists/${artistID}/follow` && method === 'DELETE') {
        seen.push({ method, pathname: url.pathname });
        return body({
          id: artistID,
          musicbrainzId: artist.musicbrainzId,
          name: artist.name,
          sortName: artist.sortName,
          followed: false,
          outcome: 'held',
          mappedTrackCount: 0
        });
      }
      return body({ items: [], total: 0, limit: 24, offset: 0 });
    })
  );
}

function opened() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(ArtistPage, undefined, { wrapper: QueryClientProvider, wrapperProps: { client } });
}

beforeAll(setup);

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe('the page heading', () => {
  it('names the artist in a level-1 heading', async () => {
    answering(detail());
    opened();

    expect(await screen.findByRole('heading', { level: 1, name: 'Talk Talk' })).toBeTruthy();
  });
});

describe('the genres', () => {
  it('names each genre the community voted', async () => {
    answering(detail({ genres: ['post-rock', 'art rock'] }));
    opened();

    expect(await screen.findByText('post-rock')).toBeTruthy();
    expect(screen.getByText('art rock')).toBeTruthy();
  });

  it('shows nothing for an artist nobody voted for', async () => {
    answering(detail({ genres: [] }));
    opened();

    expect(await screen.findByRole('heading', { name: 'Talk Talk' })).toBeTruthy();
    expect(screen.queryByText('post-rock')).toBeNull();
  });
});

describe('the biography', () => {
  it('keeps the words behind a disclosure that starts shut', async () => {
    answering(
      detail({
        biography: 'Talk Talk were an English band formed in 1981.',
        biographySourceUrl: 'https://en.wikipedia.org/wiki/Talk_Talk'
      })
    );
    opened();

    const about = await screen.findByText('About');
    const disclosure = about.closest('details');
    expect(disclosure).toBeTruthy();
    expect(disclosure!.open).toBe(false);
  });

  it('credits Wikipedia and links the article the words came from', async () => {
    answering(
      detail({
        biography: 'Talk Talk were an English band formed in 1981.',
        biographySourceUrl: 'https://en.wikipedia.org/wiki/Talk_Talk'
      })
    );
    opened();

    const link = await screen.findByRole('link', { name: 'Wikipedia' });
    expect(link.getAttribute('href')).toBe('https://en.wikipedia.org/wiki/Talk_Talk');
  });

  it('shows the words themselves', async () => {
    answering(
      detail({
        biography: 'Talk Talk were an English band formed in 1981.',
        biographySourceUrl: 'https://en.wikipedia.org/wiki/Talk_Talk'
      })
    );
    opened();

    expect(
      await screen.findByText('Talk Talk were an English band formed in 1981.')
    ).toBeTruthy();
  });

  it('shows nothing for an artist no encyclopaedia has heard of', async () => {
    answering(detail());
    opened();

    expect(await screen.findByRole('heading', { name: 'Talk Talk' })).toBeTruthy();
    expect(screen.queryByText('About')).toBeNull();
  });
});

describe('the follow control', () => {
  it('shows the level a followed artist is monitored at', async () => {
    answering(detail({ followed: true, monitorLevel: 'everything' }));
    opened();

    const select = (await screen.findByLabelText('Follow this artist')) as HTMLSelectElement;
    expect(select.value).toBe('everything');
  });

  it('shows not following for a held artist nobody follows', async () => {
    answering(detail({ followed: false }));
    opened();

    const select = (await screen.findByLabelText('Follow this artist')) as HTMLSelectElement;
    expect(select.value).toBe('off');
  });

  it('sends the new level when a followed artist changes it', async () => {
    const seen: { method: string; pathname: string }[] = [];
    answeringWithActions(detail({ followed: true, monitorLevel: 'everything' }), seen);
    opened();

    const select = (await screen.findByLabelText('Follow this artist')) as HTMLSelectElement;
    await fireEvent.change(select, { target: { value: 'main' } });

    await waitFor(() =>
      expect(seen).toContainEqual(
        expect.objectContaining({ method: 'PUT', pathname: `/api/v1/artists/${artistID}/monitor` })
      )
    );
  });

  it('follows at the chosen level when nobody follows the artist yet', async () => {
    const seen: { method: string; pathname: string }[] = [];
    answeringWithActions(detail({ followed: false }), seen);
    opened();

    const select = (await screen.findByLabelText('Follow this artist')) as HTMLSelectElement;
    await fireEvent.change(select, { target: { value: 'main' } });

    await waitFor(() =>
      expect(seen).toContainEqual(
        expect.objectContaining({ method: 'POST', pathname: `/api/v1/artists/${artistID}/follow` })
      )
    );
    await waitFor(() =>
      expect(seen).toContainEqual(
        expect.objectContaining({ method: 'PUT', pathname: `/api/v1/artists/${artistID}/monitor` })
      )
    );
  });

  it('asks for confirmation instead of unfollowing immediately', async () => {
    answering(detail({ followed: true, monitorLevel: 'everything', name: 'Talk Talk' }));
    opened();

    const select = (await screen.findByLabelText('Follow this artist')) as HTMLSelectElement;
    await fireEvent.change(select, { target: { value: 'off' } });

    expect(await screen.findByText('Unfollow Talk Talk?')).toBeTruthy();
  });

  it('keeps following when the confirmation is declined', async () => {
    const seen: { method: string; pathname: string }[] = [];
    answeringWithActions(detail({ followed: true, monitorLevel: 'everything' }), seen);
    opened();

    const select = (await screen.findByLabelText('Follow this artist')) as HTMLSelectElement;
    await fireEvent.change(select, { target: { value: 'off' } });
    await screen.findByText('Unfollow Talk Talk?');
    await fireEvent.click(screen.getByRole('button', { name: 'Keep following' }));

    expect(screen.queryByText('Unfollow Talk Talk?')).toBeNull();
    expect(seen.some((call) => call.method === 'DELETE')).toBe(false);
  });

  it('unfollows once the confirmation is accepted', async () => {
    const seen: { method: string; pathname: string }[] = [];
    answeringWithActions(detail({ followed: true, monitorLevel: 'everything' }), seen);
    opened();

    const select = (await screen.findByLabelText('Follow this artist')) as HTMLSelectElement;
    await fireEvent.change(select, { target: { value: 'off' } });
    await screen.findByText('Unfollow Talk Talk?');
    await fireEvent.click(screen.getByRole('button', { name: 'Unfollow' }));

    await waitFor(() =>
      expect(seen).toContainEqual(
        expect.objectContaining({ method: 'DELETE', pathname: `/api/v1/artists/${artistID}/follow` })
      )
    );
  });
});

describe('wanting everything missing', () => {
  it('names the count of missing releases', async () => {
    answering(
      detail({
        discography: { releases: 5, owned: 3, missing: 2, dismissed: 0, counted: 5, loading: 0 }
      })
    );
    opened();

    expect(await screen.findByRole('button', { name: 'Want 2 missing' })).toBeTruthy();
  });

  it('shows no want button when nothing is missing', async () => {
    answering(
      detail({
        discography: { releases: 5, owned: 5, missing: 0, dismissed: 0, counted: 5, loading: 0 }
      })
    );
    opened();

    await screen.findByRole('heading', { name: 'Talk Talk' });
    expect(screen.queryByText(/Want \d+ missing/)).toBeNull();
  });

  // Pressing it sets the standing want, so the releases whose tracklists arrive
  // over the following hours are wanted too.
  it('sets the standing want when the button is pressed', async () => {
    const seen: { method: string; pathname: string; body?: string }[] = [];
    answeringWithActions(detail(), seen);
    opened();

    await fireEvent.click(await screen.findByRole('button', { name: 'Want 2 missing' }));

    await waitFor(() =>
      expect(seen).toContainEqual({
        method: 'PUT',
        pathname: `/api/v1/artists/${artistID}/want-missing`,
        body: JSON.stringify({ on: true })
      })
    );
  });

  it('shows the standing want in place of the button once it is on', async () => {
    answering(detail({ wantMissing: true }));
    opened();

    expect(await screen.findByText('Wanting missing')).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Want 2 missing' })).toBeNull();
  });

  it('clears the standing want when Stop is pressed', async () => {
    const seen: { method: string; pathname: string; body?: string }[] = [];
    answeringWithActions(detail({ wantMissing: true }), seen);
    opened();

    await fireEvent.click(await screen.findByRole('button', { name: 'Stop' }));

    await waitFor(() =>
      expect(seen).toContainEqual({
        method: 'PUT',
        pathname: `/api/v1/artists/${artistID}/want-missing`,
        body: JSON.stringify({ on: false })
      })
    );
  });
});

describe('the releases still on their way', () => {
  it('says how many have no tracklist yet', async () => {
    answering(
      detail({
        discography: { releases: 22, owned: 0, missing: 13, dismissed: 0, counted: 22, loading: 9 }
      })
    );
    opened();

    expect(await screen.findByText(/9\s+releases still loading/)).toBeTruthy();
  });

  it('says it in the singular for one', async () => {
    answering(
      detail({
        discography: { releases: 22, owned: 0, missing: 13, dismissed: 0, counted: 22, loading: 1 }
      })
    );
    opened();

    expect(await screen.findByText(/1\s+release still loading/)).toBeTruthy();
  });

  it('says nothing when every tracklist has arrived', async () => {
    answering(detail());
    opened();

    await screen.findByRole('heading', { name: 'Talk Talk' });
    expect(screen.queryByText(/still loading/)).toBeNull();
  });
});

describe('release tiles', () => {
  it('ask only for the covers the rows say are cached', async () => {
    const tile = (id: string, title: string, hasCover: boolean) => ({
      id,
      artistId: artistID,
      artistName: 'Burial',
      artistFollowed: true,
      musicbrainzReleaseGroupId: null,
      title,
      firstReleaseDate: '2007-11-05',
      albumType: 'album',
      trackCount: 13,
      ownedTrackCount: 13,
      dismissedTrackCount: 0,
      monitored: true,
      artistMonitorLevel: 'everything',
      musicbrainzReleaseId: null,
      trackRefreshStatus: 'completed',
      hasCover
    });
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) => {
        const url = new URL(String(input), 'http://localhost');
        const body = (value: unknown) => new Response(JSON.stringify(value), { status: 200 });
        if (url.pathname === `/api/v1/artists/${artistID}`) return body(detail());
        if (url.pathname === '/api/v1/albums') {
          const items = [
            tile('r1', 'Untrue', true),
            tile('r2', 'Burial', false)
          ];
          return body({ items, total: 2, limit: 24, offset: 0, scopeTotal: 2 });
        }
        return body({ items: [], total: 0, limit: 24, offset: 0 });
      })
    );

    opened();
    await screen.findByText('Untrue');

    const covers = Array.from(document.querySelectorAll('img'))
      .map((img) => img.getAttribute('src'))
      .filter((src) => src?.includes('/cover'));
    expect(covers).toEqual(['/api/v1/albums/r1/cover?cached=1']);
  });
});

describe('the releases fail to load', () => {
  it('shows the failure instead of the empty "Nothing scanned yet" state', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) => {
        const url = new URL(String(input), 'http://localhost');
        const body = (value: unknown) => new Response(JSON.stringify(value), { status: 200 });
        if (url.pathname === `/api/v1/artists/${artistID}`) return body(detail());
        if (url.pathname === '/api/v1/albums') {
          return new Response(JSON.stringify({ title: 'library is unavailable' }), { status: 503 });
        }
        return body({ items: [], total: 0, limit: 24, offset: 0 });
      })
    );
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(ArtistPage, undefined, { wrapper: QueryClientProvider, wrapperProps: { client } });

    expect(await screen.findByText('Schall could not read the library.')).toBeTruthy();
    expect(screen.queryByText('Nothing scanned yet')).toBeNull();
  });
});
