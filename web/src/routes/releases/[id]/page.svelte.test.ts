import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, setup } from '@testing-library/svelte';
import { QueryClient, QueryClientProvider } from '@tanstack/svelte-query';
import type { Release, ReleaseDetail, ReleaseEdition, Track } from '$lib/api';
import { holding, quiet } from '$lib/vocabulary';

// Named without the leading `+`, like the other colocated route tests:
// SvelteKit reads every `+` file in a route folder as a route file.
//
// This is the page about one record. It draws the sleeve, the track list, and
// how much of the record the library holds, and it names the pressing in use
// behind a "Change" control that opens every pressing MusicBrainz knows of.
// Three things are worth a test in a browser-less runner: that the pressings
// are fetched when the page opens rather than when "Change" is pressed, that
// they are drawn once it is, and that a track the library does not hold reads
// as absent rather than carrying a word that names a state.

const releaseID = 'a1b2c3d4-0000-4000-8000-000000000001';

vi.mock('$app/state', () => ({
  page: {
    params: { id: releaseID },
    url: new URL(`http://localhost/releases/${releaseID}`)
  }
}));

vi.mock('$app/navigation', () => ({
  afterNavigate: () => {},
  goto: vi.fn(),
  replaceState: vi.fn()
}));

const { default: ReleasePage } = await import('./+page.svelte');

function detail(overrides: Partial<ReleaseDetail> = {}): ReleaseDetail {
  return {
    id: releaseID,
    artistId: 'b1c2d3e4-0000-4000-8000-000000000001',
    artistName: 'Talk Talk',
    artistFollowed: true,
    musicbrainzReleaseGroupId: 'c1d2e3f4-0000-4000-8000-000000000001',
    title: 'Spirit of Eden',
    firstReleaseDate: '1988-03-16',
    albumType: 'Album',
    musicbrainzReleaseId: 'e1000000-0000-4000-8000-000000000001',
    mediaCount: 1,
    trackCount: 2,
    selectedAutomatically: true,
    trackRefreshStatus: 'completed',
    genres: [],
    inks: null,
    ...overrides
  };
}

function track(overrides: Partial<Track> = {}): Track {
  return {
    id: 'd1000000-0000-4000-8000-000000000001',
    musicbrainzRecordingId: 'f1000000-0000-4000-8000-000000000001',
    title: 'The Rainbow',
    discNumber: 1,
    trackNumber: 1,
    durationMs: 350_000,
    owned: true,
    heldElsewhere: false,
    matchManual: false,
    ...overrides
  };
}

function edition(overrides: Partial<ReleaseEdition> = {}): ReleaseEdition {
  return {
    musicbrainzReleaseId: 'e1000000-0000-4000-8000-000000000001',
    title: 'Spirit of Eden',
    releaseDate: '1988-03-16',
    country: 'GB',
    status: 'Official',
    ...overrides
  };
}

/** Every address the page asked for, so a test can say what it fetched without
 * being handed a control to press first. */
let asked: string[] = [];

function sibling(id: string, title: string, overrides: Partial<Release> = {}): Release {
  return {
    id,
    artistId: 'b1c2d3e4-0000-4000-8000-000000000001',
    artistName: 'Talk Talk',
    title,
    firstReleaseDate: '1988-03-16',
    albumType: 'album',
    trackCount: 6,
    ownedTrackCount: 6,
    dismissedTrackCount: 0,
    monitored: true,
    artistMonitorLevel: 'everything',
    trackRefreshStatus: 'completed',
    hasCover: false,
    ...overrides
  } as Release;
}

function answering(
  options: {
    release?: ReleaseDetail;
    tracks?: Track[];
    editions?: ReleaseEdition[];
    discography?: Release[];
  } = {}
) {
  asked = [];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), 'http://localhost');
      asked.push(url.pathname);
      const body = (value: unknown) => new Response(JSON.stringify(value), { status: 200 });
      if (url.pathname.endsWith('/tracks')) return body({ items: options.tracks ?? [track()] });
      if (url.pathname.endsWith('/editions')) return body({ items: options.editions ?? [edition()] });
      if (url.pathname.endsWith('/downloads')) return body({ items: [] });
      if (url.pathname === `/api/v1/albums/${releaseID}`) return body(options.release ?? detail());
      if (url.pathname === '/api/v1/albums') return body({ items: options.discography ?? [] });
      return body({ items: [] });
    })
  );
}

function opened() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(ReleasePage, undefined, { wrapper: QueryClientProvider, wrapperProps: { client } });
  return client;
}

beforeAll(setup);

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe('the page heading', () => {
  it('names the release in a level-1 heading', async () => {
    answering();
    opened();

    expect(await screen.findByRole('heading', { level: 1, name: 'Spirit of Eden' })).toBeTruthy();
  });
});

describe('the pressings', () => {
  it('fetches every pressing when the page opens, before "Change" is pressed', async () => {
    answering({
      editions: [
        edition(),
        edition({
          musicbrainzReleaseId: 'e1000000-0000-4000-8000-000000000002',
          title: 'Spirit of Eden (2012 remaster)'
        })
      ]
    });
    opened();

    await screen.findByText('Spirit of Eden');
    expect(asked).toContain(`/api/v1/albums/${releaseID}/editions`);
  });

  it('shows every fetched pressing once "Change" is pressed', async () => {
    answering({
      editions: [
        edition(),
        edition({
          musicbrainzReleaseId: 'e1000000-0000-4000-8000-000000000002',
          title: 'Spirit of Eden (2012 remaster)'
        })
      ]
    });
    opened();

    await fireEvent.click(await screen.findByRole('button', { name: 'Change' }));

    expect(await screen.findByText('Spirit of Eden (2012 remaster)')).toBeTruthy();
  });

  // The event stream invalidates every key under ['releases'], and this request
  // leaves the installation for musicbrainz.org. Keeping it out from under that
  // prefix is what stops a release-wide notice sending it out again.
  it('holds the pressings under a key of their own', async () => {
    answering();
    const client = opened();

    await screen.findByText('Spirit of Eden');
    await vi.waitFor(() => expect(client.getQueryData(['editions', releaseID])).toBeTruthy());
    expect(client.getQueryData(['releases', releaseID, 'editions'])).toBeUndefined();
  });

  it('asks for no pressings at all for a release outside MusicBrainz', async () => {
    answering({ release: detail({ musicbrainzReleaseGroupId: null, musicbrainzReleaseId: null }) });
    opened();

    expect(await screen.findByText(/not linked to MusicBrainz/)).toBeTruthy();
    expect(asked).not.toContain(`/api/v1/albums/${releaseID}/editions`);
  });
});

describe('a track the library holds', () => {
  it('names its tick for a pointer and a screen reader', async () => {
    answering({ tracks: [track()] });
    opened();

    const held = await screen.findByText('The Rainbow');
    const tick = held.closest('div.flex')!.querySelector('[title="complete"]');
    expect(tick).not.toBeNull();
    expect(screen.getByRole('img', { name: 'complete' })).toBeTruthy();
  });
});

describe('a track the library does not hold', () => {
  it('reads as absent, with no word naming a state on the row', async () => {
    answering({
      tracks: [
        track(),
        track({
          id: 'd1000000-0000-4000-8000-000000000002',
          title: 'Eden',
          trackNumber: 2,
          owned: false
        })
      ]
    });
    opened();

    const absent = await screen.findByText('Eden');
    const held = screen.getByText('The Rainbow');
    // The held title is at full ink; the absent one stands back a step. That
    // step is the whole of what the row says, so no state word appears beside
    // it. The count above the list still says how many are missing — that is
    // the release speaking, not the row.
    expect(held.getAttribute('data-tone')).toBe('present');
    expect(absent.getAttribute('data-tone')).toBe('absent');
    const row = absent.closest('div.flex')!;
    expect(row.textContent).not.toMatch(/missing|not owned|absent|wanted/i);
  });

  // A decision somebody took is not a state word about absence, and it stays.
  it('keeps the word for a track somebody decided not to want', async () => {
    answering({
      tracks: [
        track({
          id: 'd1000000-0000-4000-8000-000000000002',
          title: 'Eden',
          owned: false,
          wantStatus: 'not_wanted',
          wantId: 'aa000000-0000-4000-8000-000000000001'
        })
      ]
    });
    opened();

    const title = await screen.findByText('Eden');
    expect(title.closest('div.flex')!.querySelector('[aria-label="dismissed"]')).not.toBeNull();
  });

  it('counts what the library holds on the owned bar beside the title', async () => {
    answering({
      tracks: [
        track(),
        track({ id: 'd1000000-0000-4000-8000-000000000002', title: 'Eden', owned: false })
      ]
    });
    opened();

    const bar = await screen.findByLabelText('1 of 2 tracks in your library');
    expect(bar.textContent).toContain('1 of 2');
    expect(bar.parentElement?.textContent).toContain('1 missing');
  });

  // The library holds a recording proven on a file filed under another release —
  // a single, an EP, a compilation. The music is here, so the bar counts it.
  it('counts a recording held under another release as held', async () => {
    answering({
      tracks: [
        track(),
        track({
          id: 'd1000000-0000-4000-8000-000000000002',
          title: 'Eden',
          owned: false,
          heldElsewhere: true
        })
      ]
    });
    opened();

    const bar = await screen.findByLabelText('2 of 2 tracks in your library');
    expect(bar.parentElement?.textContent).not.toContain('missing');
  });
});

describe('the track list fails to load', () => {
  it('shows the failure instead of the permanent "Importing" panel', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) => {
        const url = new URL(String(input), 'http://localhost');
        const body = (value: unknown) => new Response(JSON.stringify(value), { status: 200 });
        if (url.pathname.endsWith('/tracks')) {
          return new Response(JSON.stringify({ title: 'library is unavailable' }), { status: 503 });
        }
        if (url.pathname.endsWith('/editions')) return body({ items: [edition()] });
        if (url.pathname.endsWith('/downloads')) return body({ items: [] });
        if (url.pathname === `/api/v1/albums/${releaseID}`) return body(detail());
        return body({ items: [] });
      })
    );
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(ReleasePage, undefined, { wrapper: QueryClientProvider, wrapperProps: { client } });

    expect(await screen.findByText('Schall could not read the library.')).toBeTruthy();
    expect(screen.queryByText('Importing the track list…')).toBeNull();
  });
});

describe('an empty track list', () => {
  it('says there is no track list, not "Importing", when no refresh is running', async () => {
    answering({ release: detail({ trackRefreshStatus: 'pending', trackCount: 0 }), tracks: [] });
    opened();

    expect(await screen.findByRole('heading', { name: 'No track list' })).toBeTruthy();
    expect(screen.queryByText('Importing the track list…')).toBeNull();
  });

  it('says it is importing while the refresh runs', async () => {
    answering({ release: detail({ trackRefreshStatus: 'running', trackCount: 0 }), tracks: [] });
    opened();

    expect(await screen.findByText('Importing the track list…')).toBeTruthy();
  });
});

describe('the downloads fail to load', () => {
  it('says so in the Sources section instead of showing no open requests', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) => {
        const url = new URL(String(input), 'http://localhost');
        const body = (value: unknown) => new Response(JSON.stringify(value), { status: 200 });
        if (url.pathname.endsWith('/downloads')) {
          return new Response(JSON.stringify({ title: 'downloads broke' }), { status: 400 });
        }
        if (url.pathname.endsWith('/tracks')) return body({ items: [track()] });
        if (url.pathname.endsWith('/editions')) return body({ items: [edition()] });
        if (url.pathname === `/api/v1/albums/${releaseID}`) return body(detail());
        return body({ items: [] });
      })
    );
    opened();

    expect(await screen.findByText('downloads broke')).toBeTruthy();
    expect(screen.getByText('Sources')).toBeTruthy();
  });
});

describe('the want button', () => {
  it('counts only the missing tracks nobody has wanted yet', async () => {
    answering({
      tracks: [
        track({ id: 'd1000000-0000-4000-8000-000000000002', title: 'Eden', owned: false }),
        track({
          id: 'd1000000-0000-4000-8000-000000000003',
          title: 'Desire',
          owned: false,
          wantStatus: 'searching',
          wantId: 'aa000000-0000-4000-8000-000000000002'
        })
      ]
    });
    opened();

    expect(await screen.findByRole('button', { name: /Want 1 missing/ })).toBeTruthy();
  });

  it('is left out once every missing track is wanted', async () => {
    answering({
      tracks: [
        track(),
        track({
          id: 'd1000000-0000-4000-8000-000000000002',
          title: 'Eden',
          owned: false,
          wantStatus: 'searching',
          wantId: 'aa000000-0000-4000-8000-000000000002'
        })
      ]
    });
    opened();

    await screen.findByText('Eden');
    expect(screen.queryByRole('button', { name: /Want \d+ missing/ })).toBeNull();
  });
});

describe('removing', () => {
  /** The presses that changed something: method and path. */
  function pressesSent() {
    const answer = globalThis.fetch as ReturnType<typeof vi.fn>;
    return answer.mock.calls
      .map(([input, init]) => ({
        method: (init as RequestInit | undefined)?.method ?? 'GET',
        path: new URL(String(input), 'http://localhost').pathname
      }))
      .filter((call) => call.method !== 'GET');
  }

  it('removes the release with one press', async () => {
    answering();
    opened();

    await fireEvent.click(await screen.findByRole('button', { name: 'Remove' }));

    expect(pressesSent()).toContainEqual({ method: 'DELETE', path: `/api/v1/albums/${releaseID}/wanted` });
  });

  it('removes one track the library holds from its toggle', async () => {
    answering();
    opened();

    await fireEvent.click(await screen.findByRole('button', { name: 'Remove The Rainbow' }));

    expect(pressesSent()).toContainEqual({
      method: 'DELETE',
      path: '/api/v1/tracks/d1000000-0000-4000-8000-000000000001/wanted'
    });
  });

  it('says the day a removed track is deleted', async () => {
    answering({ tracks: [track({ removesAt: '2026-11-11T20:00:00Z' })] });
    opened();

    expect(await screen.findByLabelText(/^Deleted on 11 November$|^Deleted on November 11$/)).toBeTruthy();
  });

  it('keeps a removed track when it is wanted again', async () => {
    answering({ tracks: [track({ removesAt: '2026-11-11T20:00:00Z' })] });
    opened();

    const toggle = await screen.findByRole('button', { name: 'Want The Rainbow' });
    await fireEvent.click(toggle);

    expect(pressesSent()).toContainEqual({
      method: 'POST',
      path: '/api/v1/tracks/d1000000-0000-4000-8000-000000000001/wanted'
    });
  });
});

describe('the header actions', () => {
  it('names each icon button by what it does', async () => {
    answering({ tracks: [track(), track({ id: 't2', title: 'Inheritance', owned: false })] });
    opened();

    expect(await screen.findByRole('button', { name: 'Find sources' })).toBeTruthy();
    expect(await screen.findByRole('button', { name: 'Remove' })).toBeTruthy();
    expect(await screen.findByRole('button', { name: 'Dismiss it' })).toBeTruthy();
  });
});

// A want can settle on a file the library no longer maps. The row used to draw
// the tick for it, so 88GLAM 2.5 said "2 missing" over sixteen ticks.
describe('a track fetched once whose file is gone', () => {
  const gone = () =>
    track({ owned: false, heldElsewhere: false, wantId: 'w1', wantStatus: 'acquired' });

  it('draws no tick', async () => {
    answering({ tracks: [gone()] });
    opened();

    expect(await screen.findByLabelText('Fetched once, file gone')).toBeTruthy();
    expect(screen.queryByLabelText(quiet(holding.complete))).toBeNull();
  });

  it('offers no + that would change nothing', async () => {
    answering({ tracks: [gone()] });
    opened();
    await screen.findByLabelText('Fetched once, file gone');

    expect(screen.queryByRole('button', { name: /The Rainbow$/ })).toBeNull();
  });

  it('is not counted by the want button', async () => {
    answering({ tracks: [gone(), track({ id: 't2', title: 'Inheritance', owned: false })] });
    opened();

    expect(await screen.findByRole('button', { name: 'Want 1 missing' })).toBeTruthy();
  });
});

describe('the discography beside the tracks', () => {
  it('lists the artist\'s releases and marks this one', async () => {
    answering({
      discography: [
        sibling('a1b2c3d4-0000-4000-8000-000000000002', 'Laughing Stock', { firstReleaseDate: '1991-09-16' }),
        sibling(releaseID, 'Spirit of Eden')
      ]
    });
    opened();

    const other = await screen.findByRole('link', { name: /Laughing Stock/ });
    expect(other.getAttribute('href')).toBe('/releases/a1b2c3d4-0000-4000-8000-000000000002');
    const here = screen.getAllByRole('link', { name: /Spirit of Eden/ }).find((link) => link.getAttribute('aria-current'));
    expect(here?.getAttribute('aria-current')).toBe('page');
  });

  it('asks for the discography by the artist', async () => {
    answering();
    opened();
    await screen.findByText('Spirit of Eden');

    expect(asked).toContain('/api/v1/albums');
  });
});
