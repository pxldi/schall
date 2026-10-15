import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, setup, waitFor } from '@testing-library/svelte';
import { QueryClient, QueryClientProvider } from '@tanstack/svelte-query';
import type { ArtistDetail, PopularTrack, Release } from '$lib/api';
import { holding, quiet } from '$lib/vocabulary';

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
    leadAlbumId: null,
    inks: null,
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

    expect(await screen.findByText('post-rock, art rock')).toBeTruthy();
  });

  it('shows nothing for an artist nobody voted for', async () => {
    answering(detail({ genres: [] }));
    opened();

    expect(await screen.findByRole('heading', { name: 'Talk Talk' })).toBeTruthy();
    expect(screen.queryByText(/post-rock/)).toBeNull();
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

    expect(await screen.findByRole('button', { name: 'Add 2 missing' })).toBeTruthy();
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

    await fireEvent.click(await screen.findByRole('button', { name: 'Add 2 missing' }));

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

    expect(await screen.findByText('Missing on wishlist')).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Add 2 missing' })).toBeNull();
  });

  it('clears the standing want when Stop is pressed', async () => {
    const seen: { method: string; pathname: string; body?: string }[] = [];
    answeringWithActions(detail({ wantMissing: true }), seen);
    opened();

    await fireEvent.click(await screen.findByRole('button', { name: 'Remove' }));

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
      hasCover,
      inks: null,
      secondaryTypes: [],
      wantedTrackCount: 0,
      leavingTrackCount: 0
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

function releaseRow(id: string, title: string, overrides: Partial<Release> = {}): Release {
  return {
    id,
    artistId: artistID,
    artistName: 'Kendrick Lamar',
    artistFollowed: false,
    musicbrainzReleaseGroupId: null,
    title,
    firstReleaseDate: '2015-03-15',
    albumType: 'album',
    trackCount: 12,
    ownedTrackCount: 0,
    dismissedTrackCount: 0,
    monitored: true,
    artistMonitorLevel: 'main',
    musicbrainzReleaseId: null,
    trackRefreshStatus: 'completed',
    hasCover: false,
    inks: null,
    secondaryTypes: [],
    wantedTrackCount: 0,
    leavingTrackCount: 0,
    ...overrides
  };
}

type Write = { method: string; pathname: string; body?: string };

/** Answers the artist, these releases and a popular list, and records every
 * write the page sends. */
function answeringReleases(items: Release[], seen: Write[] = [], popular: unknown = { items: [] }) {
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), 'http://localhost');
      const method = (init?.method ?? 'GET').toUpperCase();
      const body = (value: unknown) => new Response(JSON.stringify(value), { status: 200 });
      if (method !== 'GET') {
        seen.push({ method, pathname: url.pathname, body: init?.body as string });
        if (method === 'DELETE') {
          return body({
            removed: 1,
            scheduled: [{ fileId: 'f1', path: '/music/x.flac', removesAt: '2026-11-11T12:00:00Z' }],
            kept: 0,
            unresolvable: 0
          });
        }
        return body({ albumId: 'r1', wanted: 12, alreadyWanted: 0, dismissed: 12 });
      }
      if (url.pathname === `/api/v1/artists/${artistID}`) return body(detail());
      if (url.pathname === `/api/v1/artists/${artistID}/popular`) return body(popular);
      if (url.pathname === '/api/v1/albums') {
        return body({ items, total: items.length, limit: 0, offset: 0, scopeTotal: items.length });
      }
      return body({ items: [], total: 0, limit: 24, offset: 0 });
    })
  );
}

function titlesIn(kind: string) {
  const section = screen.getByRole('region', { name: kind });
  return Array.from(section.querySelectorAll('a[href^="/releases/"]')).map(
    (link) => link.getAttribute('title')?.split(' · ')[0]
  );
}

function singles(count: number) {
  return Array.from({ length: count }, (_, index) =>
    releaseRow(`s${index}`, `Single ${index}`, {
      albumType: 'single',
      firstReleaseDate: `20${10 + index}-01-01`
    })
  );
}

describe('the order of releases', () => {
  // Newest on top, as Poldi asked. A held release no longer jumps the row,
  // because the row is read as a timeline.
  it('puts the newest first even when an older one is held', async () => {
    answeringReleases([
      releaseRow('r1', 'Section.80', { firstReleaseDate: '2011-07-02' }),
      releaseRow('r2', 'To Pimp a Butterfly', { ownedTrackCount: 12 }),
      releaseRow('r3', 'GNX', { firstReleaseDate: '2024-11-22' })
    ]);
    opened();
    await screen.findByText('GNX');

    expect(titlesIn('Albums')).toEqual(['GNX', 'To Pimp a Butterfly', 'Section.80']);
  });
});

describe('the rows of releases', () => {
  it('files an album with a secondary type under Other', async () => {
    answeringReleases([
      releaseRow('r1', 'DAMN.'),
      releaseRow('r2', 'Overly Dedicated', { secondaryTypes: ['mixtape/street'] })
    ]);
    opened();
    await screen.findByText('DAMN.');

    expect(titlesIn('Other')).toEqual(['Overly Dedicated']);
  });

  it('names the kind of an Other release after its year', async () => {
    answeringReleases([
      releaseRow('r2', 'Overly Dedicated', {
        firstReleaseDate: '2010-09-14',
        secondaryTypes: ['mixtape/street']
      })
    ]);
    opened();

    expect(await screen.findByText('2010 · Mixtape')).toBeTruthy();
  });

  it('names a release MusicBrainz types as Other', async () => {
    answeringReleases([
      releaseRow('r2', 'luther', { albumType: 'other', firstReleaseDate: '2025-04-11' })
    ]);
    opened();

    expect(await screen.findByText('2025 · Other')).toBeTruthy();
  });

  it('draws no row for a kind with nothing in it', async () => {
    answeringReleases([releaseRow('r1', 'DAMN.')]);
    opened();
    await screen.findByText('DAMN.');

    expect(screen.queryByRole('region', { name: 'Other' })).toBeNull();
  });

  it('shows the newest eight of a long row', async () => {
    answeringReleases(singles(10));
    opened();
    await screen.findByText('Single 9');

    expect(titlesIn('Singles')).toHaveLength(8);
  });

  it('opens the whole row from Show all', async () => {
    answeringReleases(singles(10));
    opened();
    await fireEvent.click(await screen.findByRole('button', { name: /Show all 10/ }));

    expect(titlesIn('Singles')).toHaveLength(10);
  });
});

describe('wanting a release from its tile', () => {
  it('wants the missing tracks when the + is pressed', async () => {
    const seen: Write[] = [];
    answeringReleases([releaseRow('r1', 'GNX')], seen);
    opened();
    await fireEvent.click(await screen.findByRole('button', { name: 'Add GNX to wishlist' }));

    await waitFor(() =>
      expect(seen).toContainEqual(
        expect.objectContaining({ method: 'POST', pathname: '/api/v1/albums/r1/wanted' })
      )
    );
  });

  it('removes a wanted release when it is pressed again', async () => {
    const seen: Write[] = [];
    answeringReleases([releaseRow('r1', 'GNX', { wantedTrackCount: 12 })], seen);
    opened();
    await fireEvent.click(await screen.findByRole('button', { name: 'Remove GNX' }));

    await waitFor(() =>
      expect(seen).toContainEqual(
        expect.objectContaining({ method: 'DELETE', pathname: '/api/v1/albums/r1/wanted' })
      )
    );
  });

  it('says when the files of a removed release are deleted', async () => {
    answeringReleases([releaseRow('r1', 'GNX', { wantedTrackCount: 12 })]);
    opened();
    await fireEvent.click(await screen.findByRole('button', { name: 'Remove GNX' }));

    expect(await screen.findByText(/^1 file deleted on .+\. Add again to keep\.$/)).toBeTruthy();
  });

  it('counts a release the library holds whole as wanted', async () => {
    answeringReleases([releaseRow('r1', 'GNX', { ownedTrackCount: 12 })]);
    opened();
    expect(await screen.findByRole('button', { name: 'Remove GNX' })).toBeTruthy();
  });

  it('removes a release the library holds whole', async () => {
    const seen: Write[] = [];
    answeringReleases([releaseRow('r1', 'GNX', { ownedTrackCount: 12 })], seen);
    opened();
    await fireEvent.click(await screen.findByRole('button', { name: 'Remove GNX' }));

    await waitFor(() =>
      expect(seen).toContainEqual(
        expect.objectContaining({ method: 'DELETE', pathname: '/api/v1/albums/r1/wanted' })
      )
    );
  });

  const leaving = { ownedTrackCount: 12, leavingTrackCount: 12, removesAt: '2026-11-11T12:00:00Z' };

  it('marks a removed release whose files are waiting to be deleted', async () => {
    answeringReleases([releaseRow('r1', 'GNX', leaving)]);
    opened();

    expect(await screen.findByLabelText(/^Deleted on /)).toBeTruthy();
  });

  it('wants a removed release again from its +', async () => {
    const seen: Write[] = [];
    answeringReleases([releaseRow('r1', 'GNX', leaving)], seen);
    opened();
    const toggle = await screen.findByRole('button', { name: 'Add GNX to wishlist' });
    await fireEvent.click(toggle);

    await waitFor(() =>
      expect(seen).toContainEqual(
        expect.objectContaining({ method: 'POST', pathname: '/api/v1/albums/r1/wanted' })
      )
    );
  });
});

describe('the popular tracks', () => {
  const track = (overrides: Partial<PopularTrack> = {}): PopularTrack => ({
    recordingId: 'c0ffee00-0000-4000-8000-000000000001',
    title: 'HUMBLE.',
    releaseTitle: 'DAMN.',
    lengthMs: 177000,
    listenCount: 900,
    trackId: null,
    albumId: null,
    albumTitle: null,
    hasCover: false,
    held: false,
    wantId: null,
    wantStatus: null,
    ...overrides
  });

  it('lists them under Popular in the order given', async () => {
    answeringReleases([releaseRow('r1', 'DAMN.')], [], {
      items: [
        track(),
        track({ recordingId: 'c0ffee00-0000-4000-8000-000000000002', title: 'luther' })
      ]
    });
    opened();
    // The heading is drawn while the list is still loading, so wait for a row.
    await screen.findByText('luther');
    const box = screen.getByRole('heading', { name: 'Popular' }).parentElement;
    const rows = Array.from(box?.querySelectorAll('li') ?? []);

    expect(rows.map((row) => row.querySelector('[title]')?.getAttribute('title'))).toEqual([
      'HUMBLE.',
      'luther'
    ]);
  });

  it('says what to do when there is no ListenBrainz token', async () => {
    const unavailable = 'Add a ListenBrainz token in Settings to see popular tracks.';
    answeringReleases([releaseRow('r1', 'DAMN.')], [], { items: [], unavailable });
    opened();

    expect(await screen.findByText(unavailable)).toBeTruthy();
  });

  it('shows the failure under Popular, not an empty list, when the read fails', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) => {
        const url = new URL(String(input), 'http://localhost');
        const body = (value: unknown) => new Response(JSON.stringify(value), { status: 200 });
        if (url.pathname === `/api/v1/artists/${artistID}`) return body(detail());
        if (url.pathname === `/api/v1/artists/${artistID}/popular`) {
          return new Response(JSON.stringify({ title: 'listenbrainz broke' }), { status: 400 });
        }
        if (url.pathname === '/api/v1/albums') {
          const items = [releaseRow('r1', 'DAMN.')];
          return body({ items, total: 1, limit: 0, offset: 0, scopeTotal: 1 });
        }
        return body({ items: [], total: 0, limit: 24, offset: 0 });
      })
    );
    opened();
    await screen.findByRole('heading', { name: 'Popular' });

    expect(await screen.findByText('listenbrainz broke')).toBeTruthy();
    expect(screen.queryByText('No listens yet')).toBeNull();
  });

  it('says there are no listens yet when the list is empty', async () => {
    answeringReleases([releaseRow('r1', 'DAMN.')], [], { items: [] });
    opened();

    expect(await screen.findByRole('heading', { name: 'No listens yet' })).toBeTruthy();
  });

  it('wants a recording no release carries by its MusicBrainz ID', async () => {
    const seen: Write[] = [];
    answeringReleases([releaseRow('r1', 'DAMN.')], seen, { items: [track()] });
    opened();
    await fireEvent.click(await screen.findByRole('button', { name: 'Add HUMBLE. to wishlist' }));

    await waitFor(() => expect(seen[0]?.pathname).toBe('/api/v1/acquisition-targets'));
    expect(JSON.parse(seen[0].body ?? '{}').recordingId).toBe(
      'c0ffee00-0000-4000-8000-000000000001'
    );
  });

  it('wants a recording through its track when the catalogue has one', async () => {
    const seen: Write[] = [];
    answeringReleases([releaseRow('r1', 'DAMN.')], seen, {
      items: [track({ trackId: 't1', albumId: 'r1' })]
    });
    opened();
    await fireEvent.click(await screen.findByRole('button', { name: 'Add HUMBLE. to wishlist' }));

    await waitFor(() => expect(seen[0]?.pathname).toBe('/api/v1/tracks/t1/wanted'));
  });

  it('draws a tick for a recording the library holds', async () => {
    answeringReleases([releaseRow('r1', 'DAMN.')], [], { items: [track({ held: true })] });
    opened();

    expect(await screen.findByLabelText(quiet(holding.complete))).toBeTruthy();
  });

  it('removes a held recording through its track', async () => {
    const seen: Write[] = [];
    answeringReleases([releaseRow('r1', 'DAMN.')], seen, {
      items: [track({ held: true, trackId: 't1', albumId: 'r1' })]
    });
    opened();
    const toggle = await screen.findByRole('button', { name: 'Remove HUMBLE.' });
    await fireEvent.click(toggle);

    await waitFor(() =>
      expect(seen).toContainEqual(
        expect.objectContaining({ method: 'DELETE', pathname: '/api/v1/tracks/t1/wanted' })
      )
    );
  });

  it('removes a wanted recording no release carries through its want', async () => {
    const seen: Write[] = [];
    answeringReleases([releaseRow('r1', 'DAMN.')], seen, {
      items: [track({ wantId: 'w1', wantStatus: 'pending' })]
    });
    opened();
    await fireEvent.click(await screen.findByRole('button', { name: 'Remove HUMBLE.' }));

    await waitFor(() =>
      expect(seen).toContainEqual(
        expect.objectContaining({ method: 'DELETE', pathname: '/api/v1/acquisition-targets/w1' })
      )
    );
  });

  it('says when the file of a removed recording is deleted', async () => {
    answeringReleases([releaseRow('r1', 'DAMN.')], [], {
      items: [
        track({
          held: true,
          trackId: 't1',
          wantId: 'w1',
          wantStatus: 'not_wanted',
          removesAt: '2026-11-11T12:00:00Z'
        })
      ]
    });
    opened();

    expect(await screen.findByLabelText(/^Deleted on /)).toBeTruthy();
  });

  it('wants a removed recording again to keep its file', async () => {
    const seen: Write[] = [];
    answeringReleases([releaseRow('r1', 'DAMN.')], seen, {
      items: [
        track({
          held: true,
          trackId: 't1',
          wantId: 'w1',
          wantStatus: 'not_wanted',
          removesAt: '2026-11-11T12:00:00Z'
        })
      ]
    });
    opened();
    const toggle = await screen.findByRole('button', { name: 'Add HUMBLE. to wishlist' });
    await fireEvent.click(toggle);

    await waitFor(() =>
      expect(seen).toContainEqual(
        expect.objectContaining({ method: 'POST', pathname: '/api/v1/tracks/t1/wanted' })
      )
    );
  });
});

describe('an artist nobody follows', () => {
  it('does not claim nothing counts as missing', async () => {
    answering(detail({ followed: false, followedAt: null }));
    opened();
    await screen.findByRole('heading', { name: 'Talk Talk' });

    expect(screen.queryByText(/nothing counts as missing/)).toBeNull();
  });

  it('says not following once, on the follow control', async () => {
    answering(detail({ followed: false, followedAt: null }));
    opened();
    await screen.findByRole('heading', { name: 'Talk Talk' });

    expect(screen.queryByText(/won't add their new releases/)).toBeNull();
    expect(screen.getAllByText(/Not following/).length).toBeGreaterThan(0);
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

    expect(await screen.findByText("Couldn't load the library.")).toBeTruthy();
    expect(screen.queryByText('No releases yet')).toBeNull();
  });
});

describe('the safety-net poll', () => {
  async function artistReadsOver(artist: ArtistDetail) {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    answering(artist);
    opened();
    await screen.findByRole('heading', { level: 1, name: 'Talk Talk' });
    const reads = () =>
      vi
        .mocked(fetch)
        .mock.calls.filter(([input]) => String(input).endsWith(`/api/v1/artists/${artistID}`))
        .length;
    const before = reads();
    await vi.advanceTimersByTimeAsync(31_000);
    const after = reads();
    vi.useRealTimers();
    return after - before;
  }

  it('asks again while a refresh is queued', async () => {
    expect(await artistReadsOver(detail({ refreshStatus: 'queued' }))).toBeGreaterThan(0);
  });

  // 'pending' is what the server says when no refresh job exists at all, which
  // for an artist who is only held stays true for good.
  it('stays quiet for an artist no refresh was ever queued for', async () => {
    expect(await artistReadsOver(detail({ followed: false, refreshStatus: 'pending' }))).toBe(0);
  });
});

describe('the standing want', () => {
  function albumReads() {
    return vi
      .mocked(fetch)
      .mock.calls.filter(
        ([input]) => new URL(String(input), 'http://localhost').pathname === '/api/v1/albums'
      ).length;
  }

  // Each tile counts the tracks still wanted, so wanting has to read them again.
  it('reads the releases again once it is set', async () => {
    const seen: { method: string; pathname: string; body?: string }[] = [];
    answeringWithActions(detail(), seen);
    opened();

    const button = await screen.findByRole('button', { name: 'Add 2 missing' });
    await waitFor(() => expect(albumReads()).toBeGreaterThan(0));
    const before = albumReads();
    await fireEvent.click(button);

    await waitFor(() => expect(albumReads()).toBeGreaterThan(before));
  });
});

describe('a follow that half lands', () => {
  // Following and setting the level are two requests. When the second fails
  // the artist is followed all the same, and the page has to say so.
  it('reads the artist again when the follow lands but the level does not', async () => {
    let followed = false;
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = new URL(String(input), 'http://localhost');
        const method = (init?.method ?? 'GET').toUpperCase();
        const body = (value: unknown, status = 200) =>
          new Response(JSON.stringify(value), { status });
        if (url.pathname === `/api/v1/artists/${artistID}` && method === 'GET') {
          return body(detail({ followed, monitorLevel: 'main' }));
        }
        if (url.pathname === `/api/v1/artists/${artistID}/follow` && method === 'POST') {
          followed = true;
          return body(detail({ followed: true }));
        }
        if (url.pathname === `/api/v1/artists/${artistID}/monitor`) {
          return body({ title: 'database is unavailable' }, 503);
        }
        return body({ items: [], total: 0, limit: 24, offset: 0 });
      })
    );
    opened();

    const select = (await screen.findByLabelText('Follow this artist')) as HTMLSelectElement;
    await fireEvent.change(select, { target: { value: 'everything' } });

    await waitFor(() =>
      expect((screen.getByLabelText('Follow this artist') as HTMLSelectElement).value).toBe('main')
    );
  });
});
