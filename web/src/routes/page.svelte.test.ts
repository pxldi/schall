import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, setup, waitFor } from '@testing-library/svelte';
import { QueryClient, QueryClientProvider } from '@tanstack/svelte-query';
import type { Overview } from '$lib/api';

// Named without the leading `+`, like the other colocated route tests: SvelteKit
// reads every `+` file in a route folder as a route file.
//
// The Overview is one read drawn nine ways. What is under test is that each
// panel says what the read said — the period beside its name, the figures,
// the tooltip on a mark — and that the one action on it sends the right want.
vi.mock('$app/state', () => ({
  page: { params: {}, url: new URL('http://localhost/') }
}));

vi.mock('$app/navigation', () => ({
  afterNavigate: () => {},
  goto: vi.fn(),
  replaceState: vi.fn()
}));

const { default: Overview } = await import('./+page.svelte');

function days(n: number, count: (index: number) => number, last = new Date('2026-09-06T12:00:00Z')) {
  return Array.from({ length: n }, (_, index) => {
    const date = new Date(last);
    date.setUTCDate(date.getUTCDate() - (n - 1 - index));
    return { date: date.toISOString().slice(0, 10), count: count(index) };
  });
}

function yesterdayAt(hour: number, minute: number): Date {
  const at = new Date();
  at.setDate(at.getDate() - 1);
  at.setHours(hour, minute, 0, 0);
  return at;
}

function overview(overrides: Partial<Overview> = {}): Overview {
  const cells = Array.from({ length: 7 }, () => Array.from({ length: 24 }, () => 0));
  cells[4][21] = 14;
  return {
    listening: {
      available: true,
      listens: { total: 812, previous: 745, days: days(30, (index) => (index === 29 ? 61 : 20)) },
      mostPlayed: [
        {
          title: 'Maske weg',
          artist: 'Pashanim',
          listens: 18,
          recordingMbid: 'r1',
          coverUrl: null,
          trackId: 't1',
          inLibrary: true,
          wanted: false
        },
        {
          title: 'All white',
          artist: 'Pashanim',
          listens: 17,
          recordingMbid: 'r2',
          coverUrl: null,
          trackId: 't2',
          inLibrary: false,
          wanted: false
        },
        {
          title: 'No Lie',
          artist: 'Playboi Carti',
          listens: 9,
          recordingMbid: null,
          coverUrl: null,
          trackId: null,
          inLibrary: false,
          wanted: false
        }
      ],
      topArtists: [{ name: 'Pashanim', artistMbid: 'a1', listens: 95, pictureUrl: null }],
      whenYouListen: { cells, peakHour: 21, busiestWeekday: 'Friday' },
      topAlbums: [{ title: 'traence', artist: 'Pashanim', listens: 95, releaseMbid: null, coverUrl: null }],
      sessions: [
        {
          startedAt: new Date(Date.now() - 900_000).toISOString(),
          endedAt: new Date(Date.now() - 120_000).toISOString(),
          ongoing: true,
          songs: 6,
          artists: ['PinkPantheress', 'Ninajirachi', 'Himera', 'Ye'],
          coverUrls: ['/api/v1/albums/a/cover', '/api/v1/albums/b/cover']
        },
        {
          startedAt: yesterdayAt(21, 47).toISOString(),
          endedAt: yesterdayAt(22, 27).toISOString(),
          ongoing: false,
          songs: 1,
          artists: ['Sewerslvt'],
          coverUrls: []
        }
      ]
    },
    arrived: { today: 31, downloading: 7, days: days(14, (index) => (index === 13 ? 31 : 3)) },
    library: {
      fileCount: 2500,
      totalBytes: 44_600_000_000,
      growth: Array.from({ length: 12 }, (_, index) => ({ month: `2025-${String(index + 1).padStart(2, '0')}`, files: 1800 + index * 50 })),
      storage: { usedBytes: 4_400_000_000_000, totalBytes: 9_800_000_000_000 }
    },
    recentlyAdded: [
      { title: 'Nachtfalter', artist: 'Nemo Vice', addedAt: new Date(Date.now() - 3_600_000).toISOString(), trackId: null, coverUrl: null }
    ],
    ...overrides
  };
}

const sent: { path: string; method: string }[] = [];

function answering(body: Overview) {
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), 'http://localhost');
      sent.push({ path: url.pathname + url.search, method: init?.method ?? 'GET' });
      if (url.pathname === '/api/v1/overview') {
        return new Response(JSON.stringify(body), { status: 200 });
      }
      if (url.pathname.endsWith('/wanted')) {
        return new Response(JSON.stringify({ id: 'w1' }), { status: 200 });
      }
      return new Response('{}', { status: 404 });
    })
  );
}

function show() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(Overview, undefined, { wrapper: QueryClientProvider, wrapperProps: { client } });
}

function refusingSignIn() {
  vi.stubGlobal(
    'fetch',
    vi.fn(async () =>
      new Response(JSON.stringify({ title: 'unauthorized' }), { status: 401 })
    )
  );
}

beforeAll(setup);

describe('the overview', () => {
  beforeEach(() => {
    sent.length = 0;
  });
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it('names the nine panels with their periods', async () => {
    answering(overview());
    show();
    await screen.findByText('812');
    for (const title of [
      'Listens',
      'Most played',
      'Top artists',
      'When you listen',
      'Top albums',
      'Recently played',
      'Downloaded',
      'Library',
      'Recently added'
    ]) {
      expect(screen.getByRole('heading', { name: title, level: 2 })).toBeTruthy();
    }
    expect(screen.getByText('last 30 days')).toBeTruthy();
    expect(screen.getAllByText('this month')).toHaveLength(3);
    expect(screen.getByText('30 days')).toBeTruthy();
    expect(screen.getByText('14 days')).toBeTruthy();
    expect(screen.getByRole('heading', { level: 1, name: 'Overview' })).toBeTruthy();
  });

  it('asks in the browser zone', async () => {
    answering(overview());
    show();
    await screen.findByText('812');
    const zone = Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC';
    expect(sent[0].path).toBe(`/api/v1/overview?tz=${encodeURIComponent(zone)}`);
  });

  it('anchors on the total and says nothing else under it', async () => {
    answering(overview());
    show();
    expect(await screen.findByText('812')).toBeTruthy();
    expect(screen.queryByText(/in the 30 days before/)).toBeNull();
    expect(screen.queryByText(/Busiest day/)).toBeNull();
    expect(screen.queryByText(/Most arrivals/)).toBeNull();
    expect(screen.queryByText(/Quietest/)).toBeNull();
  });

  it('measures the listens against the thirty days before', async () => {
    answering(overview());
    show();
    expect(await screen.findByText('+9%')).toBeTruthy();
  });

  it('shows a fall with its sign', async () => {
    const data = overview();
    data.listening.listens.previous = 1000;
    answering(data);
    show();
    expect(await screen.findByText('-19%')).toBeTruthy();
  });

  it('says where the listening peaks', async () => {
    answering(overview());
    show();
    await screen.findByText('812');
    // "21:00" and "Friday" also name an hour column and a row in the
    // accessible table below the heatmap, so more than one element carries
    // each.
    expect(screen.getAllByText('21:00').length).toBeGreaterThan(0);
    expect(screen.getAllByText('Friday').length).toBeGreaterThan(0);
  });

  it('labels the day axis with the first day and today', async () => {
    answering(overview());
    show();
    await screen.findByText('812');
    expect(screen.getByText('8 Aug')).toBeTruthy();
    expect(screen.getAllByText('today').length).toBeGreaterThan(0);
  });

  it('tells the count of a hovered bar', async () => {
    answering(overview());
    show();
    await screen.findByText('812');
    // The chart is aria-hidden now (the table below it is the accessible
    // view), so the hitbox is reached by position: two rects per day, the
    // transparent one second, the last day's pair last.
    const section = screen.getByRole('heading', { name: 'Listens', level: 2 }).closest('section')!;
    const bars = section.querySelectorAll('svg rect');
    const bar = bars[bars.length - 1];
    await fireEvent.mouseEnter(bar);
    expect(screen.getByRole('tooltip').textContent).toContain('Sun 6 Sep · 61 listens');
    await fireEvent.mouseLeave(bar);
    expect(screen.queryByRole('tooltip')).toBeNull();
  });

  it('tells the count of a hovered hour', async () => {
    answering(overview());
    show();
    await screen.findByText('812');
    // Same as the bars: the grid is aria-hidden, so the cell is reached by
    // position (day 4, hour 21) rather than by role.
    const section = screen.getByRole('heading', { name: 'When you listen', level: 2 }).closest('section')!;
    const cells = section.querySelectorAll('[role="group"] span[aria-hidden="true"]');
    const cell = cells[4 * 24 + 21];
    await fireEvent.mouseEnter(cell);
    expect(screen.getByRole('tooltip').textContent).toContain('Fri 21:00 · 14 listens');
  });


  it('moves the highlighted hour and announces it on ArrowRight', async () => {
    answering(overview());
    show();
    await screen.findByText('812');
    const grid = screen.getByRole('group', { name: /arrow keys to explore/ });
    await fireEvent.keyDown(grid, { key: 'ArrowRight' });
    expect(screen.getByText('Mon 01:00 · 0 listens')).toBeTruthy();
  });


  it('wants a played song the library does not hold', async () => {
    answering(overview());
    show();
    await screen.findByText('812');
    // One song is held and one has no catalogue track: neither carries a mark.
    // The third can be wanted.
    expect(screen.getAllByRole('button', { name: 'Want' })).toHaveLength(1);
    expect(screen.queryByLabelText('in your library')).toBeNull();
    await fireEvent.click(screen.getByRole('button', { name: 'Want' }));
    await waitFor(() => {
      expect(sent.some((call) => call.path === '/api/v1/tracks/t2/wanted' && call.method === 'POST')).toBe(true);
    });
  });

  it('shows every row the read returned, scrolling inside the panel', async () => {
    const data = overview();
    data.listening.topArtists = Array.from({ length: 8 }, (_, index) => ({
      name: `Artist ${index}`,
      artistMbid: `a${index}`,
      listens: 8 - index,
      pictureUrl: null
    }));
    answering(data);
    show();
    await screen.findByText('812');
    expect(screen.getAllByText(/^Artist \d$/)).toHaveLength(8);
  });

  it('shows recently played as sittings: when, how many songs, who was on', async () => {
    answering(overview());
    show();
    await screen.findByText('812');
    const section = screen.getByRole('heading', { name: 'Recently played', level: 2 }).closest('section')!;
    const rows = section.querySelectorAll('li');
    expect(rows).toHaveLength(2);
    expect(rows[0].textContent).toContain('6 songs');
    expect(rows[0].textContent).toContain('PinkPantheress, Ninajirachi, Himera + 1 more');
    expect(rows[0].textContent).not.toContain('Yesterday');
    expect(rows[0].textContent).toMatch(/\d\d:\d\d–now/);
    expect(Array.from(rows[0].querySelectorAll('img')).map((img) => img.getAttribute('src'))).toEqual([
      '/api/v1/albums/a/cover',
      '/api/v1/albums/b/cover'
    ]);
    expect(rows[1].querySelector('img')).toBeNull();
    expect(rows[1].textContent).toContain('Yesterday 21:47–22:27');
    expect(rows[1].textContent).toContain('1 song');
    expect(rows[1].textContent).toContain('Sewerslvt');
  });

  it('renders two files added in the same second under one title', async () => {
    const data = overview();
    const addedAt = new Date(Date.now() - 60_000).toISOString();
    data.recentlyAdded = [
      { title: 'Beat 01', artist: 'Ye', addedAt, trackId: null, coverUrl: null },
      { title: 'Beat 01', artist: 'Ye', addedAt, trackId: null, coverUrl: null }
    ];
    answering(data);
    show();
    await screen.findByText('812');
    expect(screen.getAllByText('Beat 01')).toHaveLength(2);
    // The panel after it still stands.
    expect(screen.getByRole('heading', { name: 'Downloaded', level: 2 })).toBeTruthy();
  });

  it('pictures a top artist the catalogue has a picture of', async () => {
    const data = overview();
    data.listening.topArtists = [
      { name: 'Cynthoni', artistMbid: 'a1', listens: 95, pictureUrl: '/api/v1/artists/x/image' },
      { name: 'Ye', artistMbid: null, listens: 4, pictureUrl: null }
    ];
    answering(data);
    show();
    await screen.findByText('812');
    const pictured = screen.getByText('Cynthoni').closest('li')!;
    expect(pictured.querySelector('img')?.getAttribute('src')).toBe('/api/v1/artists/x/image');
    const bare = screen.getByText('Ye').closest('li')!;
    expect(bare.querySelector('img')).toBeNull();
    expect(bare.textContent).toContain('Y');
  });

  it('keeps a labelled cover frame when a song has no cover to load', async () => {
    answering(overview());
    show();
    await screen.findByText('812');
    expect(screen.getByRole('img', { name: 'Cover for Maske weg' })).toBeTruthy();
  });

  it('breathes in every panel while the overview is being read', async () => {
    vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>(() => {})));
    show();
    expect(await screen.findAllByLabelText(/^Loading /)).toHaveLength(9);
    expect(screen.queryByText('No listens yet')).toBeNull();
  });

  it('replaces every placeholder once the overview has arrived', async () => {
    answering(overview());
    show();
    await screen.findByText('files');
    expect(screen.queryAllByLabelText(/^Loading /)).toHaveLength(0);
  });

  it('says the listening panels are empty until listens have been read', async () => {
    const data = overview();
    data.listening.available = false;
    answering(data);
    show();
    await screen.findByText('files');
    expect(screen.getAllByText('No listens yet')).toHaveLength(6);
    expect(screen.getByText('2,500')).toBeTruthy();
    expect(screen.getByText('files')).toBeTruthy();
    expect(screen.getByText('Nachtfalter')).toBeTruthy();
  });

  it('states the arrivals and the room left', async () => {
    answering(overview());
    show();
    await screen.findByText('Room left');
    expect(screen.getByText('7')).toBeTruthy();
    expect(screen.getByText('downloading')).toBeTruthy();
    expect(screen.getByText('Room left')).toBeTruthy();
    expect(screen.getByRole('img', { name: /used of/ })).toBeTruthy();
  });

  it('shows only the error on a 401, none of the panels', async () => {
    refusingSignIn();
    show();
    await screen.findByText('No sign-in on this connection.');
    expect(screen.queryByText('Listens')).toBeNull();
    expect(screen.queryByText('Most played')).toBeNull();
  });
});
