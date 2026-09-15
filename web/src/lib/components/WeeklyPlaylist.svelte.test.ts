import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, setup } from '@testing-library/svelte';
import { QueryClient, QueryClientProvider } from '@tanstack/svelte-query';
import WeeklyPlaylist from '$lib/components/WeeklyPlaylist.svelte';
import type { WeeklyKeepRead, WeeklyLease, WeeklyOverview, WeeklyRun } from '$lib/api';

// The weekly playlist screen. Schall fetches a few songs a week and deletes the
// ones nobody kept, so what this view has to get right is the week of notice: a
// song about to go is at the top, names the date it goes, and carries the one
// control that stops it.
//
// That is why the assertions here are about order, dates and requests rather
// than about layout. A row that reads correctly but sits under twenty others,
// or a Keep that went to the wrong file, is a deletion nobody could have
// stopped.
//
// The only boundary stubbed is `fetch`.

const clock = new Date('2026-08-14T12:00:00Z').getTime();

function lease(overrides: Partial<WeeklyLease> = {}): WeeklyLease {
  return {
    id: '11110000-0000-4000-8000-000000000001',
    libraryFileId: 'ff110000-0000-4000-8000-000000000001',
    path: '/music/Halyard/Long Reach/03 Weather Report.flac',
    artist: 'Halyard',
    title: 'Weather Report',
    state: 'held',
    grantedAt: '2026-08-10T09:00:00Z',
    expiresAt: '2026-08-21T09:00:00Z',
    removesAt: null,
    extensions: 0,
    extendedReason: '',
    ...overrides
  };
}

function run(overrides: Partial<WeeklyRun> = {}): WeeklyRun {
  return {
    id: '22220000-0000-4000-8000-000000000001',
    mode: 'remove',
    status: 'complete',
    detail: '',
    chosen: 20,
    wanted: 20,
    arrived: 18,
    kept: 5,
    extended: 0,
    leaving: 3,
    removed: 2,
    library: 0,
    replaced: 0,
    startedAt: '2026-08-10T09:00:00Z',
    finishedAt: '2026-08-10T09:20:00Z',
    ...overrides
  };
}

function overview(overrides: Partial<WeeklyOverview> = {}): WeeklyOverview {
  return {
    settings: {
      enabled: true,
      songsPerWeek: 20,
      mode: 'remove',
      libraryShare: 0,
      onePerArtist: false
    },
    playlistId: '33330000-0000-4000-8000-000000000001',
    leases: [lease()],
    runs: [run()],
    ...overrides
  };
}

function read(overrides: Partial<WeeklyKeepRead> = {}): WeeklyKeepRead {
  return {
    signal: 'navidrome_star',
    outcome: 'absent',
    detail: '',
    readAt: '2026-08-14T10:00:00Z',
    ...overrides
  };
}

/** Every request the view made, in the order it made them. */
let asked: { path: string; method: string }[] = [];

/** The server answering with this overview, and with these reads behind a
 * song's disclosure. A POST answers with the lease it was sent about. */
function answering(
  data: WeeklyOverview,
  { reads = [] as WeeklyKeepRead[], refuseKeep = '' } = {}
) {
  asked = [];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (path: string, init?: RequestInit) => {
      asked.push({ path, method: init?.method ?? 'GET' });
      if (init?.method === 'POST') {
        if (refuseKeep) {
          // The shape `api.problem` writes on the server.
          return new Response(JSON.stringify({ title: refuseKeep }), { status: 404 });
        }
        return new Response(JSON.stringify(lease()), { status: 200 });
      }
      if (path.includes('/reads')) {
        return new Response(JSON.stringify({ items: reads }));
      }
      return new Response(JSON.stringify(data));
    })
  );
}

function opened() {
  // The client is per test: a cache shared between them would answer the second
  // one from the first one's list without asking anything.
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(WeeklyPlaylist, undefined, {
    wrapper: QueryClientProvider,
    wrapperProps: { client }
  });
}

/** The rows on screen, in the order the reader meets them. */
function titlesInOrder() {
  return screen
    .getAllByText(/Weather Report|Cold Sun|Long Division/)
    .map((element) => element.textContent);
}

beforeAll(setup);

beforeEach(() => {
  vi.setSystemTime(clock);
  localStorage.clear();
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  cleanup();
});

describe('WeeklyPlaylist', () => {
  it('fills the viewport with weekly skeleton rows while loading', () => {
    answering(overview());
    opened();

    const loading = screen.getByLabelText('Loading weekly playlist');
    expect(loading.getAttribute('aria-busy')).toBe('true');
    expect(screen.getByLabelText('Loading weekly songs').querySelectorAll('[aria-hidden="true"]'))
      .toHaveLength(60);
  });

  it('says the feature is off and offers no list when nobody has switched it on', async () => {
    answering(
      overview({
        settings: {
          enabled: false,
          songsPerWeek: 20,
          mode: 'report',
          libraryShare: 0,
          onePerArtist: false
        },
        leases: []
      })
    );
    opened();

    expect(await screen.findByText('Weekly playlist is off')).toBeTruthy();
    expect(screen.queryByText('Weather Report')).toBeNull();
    expect(screen.queryByRole('button', { name: 'Keep' })).toBeNull();
  });

  it('puts a song that is leaving above one whose week is still running', async () => {
    answering(
      overview({
        leases: [
          lease(),
          lease({
            id: '11110000-0000-4000-8000-000000000002',
            libraryFileId: 'ff110000-0000-4000-8000-000000000002',
            title: 'Cold Sun',
            artist: 'Cassiopeia Fields',
            state: 'leaving',
            removesAt: '2026-08-18T09:00:00Z'
          })
        ]
      })
    );
    opened();
    await screen.findByText('Cold Sun');

    expect(titlesInOrder()).toEqual(['Cold Sun', 'Weather Report']);
  });

  it('names the date a leaving song goes', async () => {
    answering(
      overview({
        leases: [
          lease({ state: 'leaving', removesAt: '2026-08-18T09:00:00Z', title: 'Cold Sun' })
        ]
      })
    );
    opened();

    // The date is written the reader's own way round, so both orders pass.
    expect(await screen.findByText(/leaving (18 August|August 18)/)).toBeTruthy();
  });

  it('records a Keep against the song the reader pressed it on', async () => {
    answering(
      overview({
        leases: [
          lease({
            libraryFileId: 'ff110000-0000-4000-8000-0000000000aa',
            state: 'leaving',
            removesAt: '2026-08-18T09:00:00Z'
          })
        ]
      })
    );
    opened();

    await fireEvent.click(await screen.findByRole('button', { name: 'Keep' }));

    await vi.waitFor(() =>
      expect(asked).toContainEqual({
        path: '/api/v1/weekly/songs/ff110000-0000-4000-8000-0000000000aa/keep',
        method: 'POST'
      })
    );
    // The list is read again, because a keep changes what the rest of the
    // screen says about that song.
    await vi.waitFor(() =>
      expect(asked.filter((call) => call.path === '/api/v1/weekly' && call.method === 'GET').length)
        .toBeGreaterThan(1)
    );
  });

  it('offers no Keep for a song whose file has already gone', async () => {
    answering(overview({ leases: [lease({ libraryFileId: null })] }));
    opened();
    await screen.findByText('Weather Report');

    expect(screen.queryByRole('button', { name: 'Keep' })).toBeNull();
    expect(screen.getByText('file already gone')).toBeTruthy();
  });

  it('says why a song that was given more time got it', async () => {
    answering(
      overview({
        leases: [
          lease({ extensions: 1, extendedReason: 'Navidrome could not be reached on Monday' })
        ]
      })
    );
    opened();

    expect(await screen.findByText('Navidrome could not be reached on Monday')).toBeTruthy();
  });

  it('reads nothing about a leaving song until the reader opens the account of it', async () => {
    answering(
      overview({
        leases: [lease({ state: 'leaving', removesAt: '2026-08-18T09:00:00Z' })]
      }),
      {
        reads: [
          read({ signal: 'navidrome_star', outcome: 'absent' }),
          read({
            signal: 'schall_keep',
            outcome: 'unreadable',
            detail: 'the keep store did not answer'
          })
        ]
      }
    );
    opened();
    await screen.findByText('Weather Report');

    expect(asked.some((call) => call.path.includes('/reads'))).toBe(false);

    await fireEvent.click(screen.getByText("Why it's leaving"));

    expect(await screen.findByText('Star in your player')).toBeTruthy();
    expect(screen.getByText('nothing found')).toBeTruthy();
    expect(screen.getByText('Keep in Schall')).toBeTruthy();
    expect(screen.getByText('could not be read')).toBeTruthy();
    expect(screen.getByText('the keep store did not answer')).toBeTruthy();
  });

  it('reserves the evidence rows while an open disclosure is loading', async () => {
    const leaseID = '11110000-0000-4000-8000-000000000001';
    vi.stubGlobal(
      'fetch',
      vi.fn((path: string) => {
        if (path.includes('/reads')) return new Promise<Response>(() => {});
        return Promise.resolve(
          new Response(
            JSON.stringify(overview({ leases: [lease({ state: 'leaving', removesAt: '2026-08-18T09:00:00Z' })] })),
            { status: 200 }
          )
        );
      })
    );
    opened();
    await screen.findByText('Weather Report');

    await fireEvent.click(screen.getByText("Why it's leaving"));

    const loading = await screen.findByLabelText('Loading weekly evidence');
    expect(loading.querySelectorAll('[aria-hidden="true"]')).toHaveLength(40);
    expect(screen.getByText('reading what was asked…')).toBeTruthy();
  });

  it('says what a refresh chose, kept and removed', async () => {
    answering(overview({ runs: [run()] }));
    opened();

    expect(
      await screen.findByText('chose 20 · arrived 18 · kept 5 · leaving 3 · removed 2')
    ).toBeTruthy();
  });

  it('says how many songs came from the library', async () => {
    answering(overview({ runs: [run({ library: 4 })] }));
    opened();

    expect(await screen.findByText(/library 4/)).toBeTruthy();
  });

  it('says how many slots were replaced', async () => {
    answering(overview({ runs: [run({ replaced: 2 })] }));
    opened();

    expect(await screen.findByText(/replaced 2/)).toBeTruthy();
  });

  it('says what a refresh that only got part way said about itself', async () => {
    answering(
      overview({ runs: [run({ status: 'partial', detail: 'two songs never arrived' })] })
    );
    opened();

    expect(await screen.findByText('two songs never arrived')).toBeTruthy();
    expect(screen.getByText('partial')).toBeTruthy();
  });

  it('says the song is safe and names the control again when a Keep is refused', async () => {
    answering(
      overview({ leases: [lease({ state: 'leaving', removesAt: '2026-08-18T09:00:00Z' })] }),
      { refuseKeep: 'this song is not on the weekly playlist' }
    );
    opened();

    await fireEvent.click(await screen.findByRole('button', { name: 'Keep' }));

    // The first sentence is the one the error map writes for this refusal; the
    // second is this screen's own, because it can name the button that was just
    // pressed. The server's own words stay, behind the disclosure.
    expect(await screen.findByText('This song is not on this week’s playlist.')).toBeTruthy();
    expect(
      screen.getByText('Nothing has been deleted, so press Keep again.')
    ).toBeTruthy();
    expect(screen.getByText('this song is not on the weekly playlist')).toBeTruthy();
  });
});
