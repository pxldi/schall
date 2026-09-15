import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, setup, waitFor } from '@testing-library/svelte';
import { QueryClient, QueryClientProvider, type Query } from '@tanstack/svelte-query';
import WantsTab from '$lib/components/WantsTab.svelte';
import type { AcquisitionTarget, AcquisitionTargets } from '$lib/api';

// A want is a recording somebody asked Schall to find. Until it arrives or
// raises a question it appears nowhere else — Review has the ones with a
// question, the transfers list the ones with a download — so what is asserted
// here is that a want nothing is happening to is still visible, and that it
// says what has been tried for it. A row that only said "wanted" would leave
// the reader exactly where issue #339 found them.
//
// The only boundary stubbed is `fetch`.

const clock = new Date('2026-08-13T21:00:00Z').getTime();

function want(overrides: Partial<AcquisitionTarget> = {}): AcquisitionTarget {
  return {
    id: '20000000-0000-4000-8000-000000000001',
    origin: 'recommendation',
    artist: 'Talk Talk',
    title: 'Ascension Day',
    album: 'Laughing Stock',
    durationMs: 360_000,
    isrc: null,
    recordingId: 'e0000000-0000-4000-8000-00000000001b',
    status: 'pending',
    summary: 'looking for a copy',
    attempts: 3,
    anchorAttempts: 0,
    createdAt: new Date(clock - 6 * 86_400_000).toISOString(),
    lastAttemptAt: new Date(clock - 2 * 3_600_000).toISOString(),
    nextAttemptAt: new Date(clock + 4 * 3_600_000).toISOString(),
    ...overrides
  };
}

let asked: string[] = [];
let stopped: string[] = [];
let resumed: string[] = [];
let takenBest: string[] = [];
let keptFloor: string[] = [];
let actions: { method: string; path: string; body: BodyInit | null | undefined }[] = [];

// The two piles the tab reads, keyed by the states it asks for. A test names
// what is in each; anything it does not name is empty. `notice` is the one
// line the API sends while wants are waiting and nothing on the installation
// could prove a copy is the recording — a test names it to see the banner.
function answering(
  piles: { looking?: AcquisitionTarget[]; stopped?: AcquisitionTarget[]; notice?: string } = {}
) {
  asked = [];
  stopped = [];
  resumed = [];
  takenBest = [];
  keptFloor = [];
  actions = [];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (path: string, init?: RequestInit) => {
      if (init?.method === 'POST' || init?.method === 'DELETE') {
        const id = path.split('/')[4] ?? '';
        actions.push({ method: init.method, path, body: init.body });
        if (path.includes('take-best-available')) {
          (init.method === 'POST' ? takenBest : keptFloor).push(id);
        } else {
          (init.method === 'POST' ? stopped : resumed).push(id);
        }
        return new Response(JSON.stringify(want({ id })), { status: 200 });
      }
      asked.push(path);
      const items = path.includes('not_wanted') ? (piles.stopped ?? []) : (piles.looking ?? []);
      const body: Record<string, unknown> = { items, total: items.length, limit: 25, offset: 0 };
      if (piles.notice) body.notice = piles.notice;
      return new Response(JSON.stringify(body), { status: 200 });
    })
  );
}

function opened() {
  // The client is per test: a cache shared between them would answer the second
  // one out of the first one's list.
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(WantsTab, undefined, {
    wrapper: QueryClientProvider,
    wrapperProps: { client }
  });
}

beforeAll(setup);

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe('the wants being looked for', () => {
  it('lists a want nothing is transferring for and nothing is asking about', async () => {
    answering({ looking: [want()] });

    opened();

    expect(await screen.findByText('Talk Talk — Ascension Day')).toBeTruthy();
  });

  it('says where the want came from and how many copies have been looked at', async () => {
    answering({ looking: [want()] });

    opened();

    const line = await screen.findByText(/from a suggestion/);
    expect(line.textContent).toContain('3 copies looked at');
  });

  // The one thing this feature adds to the tab: an upgrade want says which
  // file it exists to replace, rather than reading as an ordinary suggestion.
  it('names the file an upgrade want exists to replace', async () => {
    answering({
      looking: [want({ origin: 'upgrade', upgradeOfPath: '/music/Talk Talk/roads.mp3' })]
    });

    opened();

    const line = await screen.findByText(/upgrade of roads\.mp3/);
    expect(line.textContent).toContain('upgrade of roads.mp3');
  });

  it('shows why a want has no sample', async () => {
    const reason = "No upload matched this entry's name and length.";
    answering({ looking: [want({ anchorUnavailable: reason, anchorNextAttemptAt: null })] });

    opened();

    expect(await screen.findByText(`No sample · ${reason}`)).toBeTruthy();
    expect(asked.some((path) => path.includes('status=unresolved%2Cpending%2Csearching'))).toBe(
      true
    );
  });

  it('shows when a deferred sample will be retried', async () => {
    answering({
      looking: [
        want({
          anchorUnavailable: 'No upload could be fetched for this entry.',
          anchorNextAttemptAt: new Date(Date.now() + 2 * 86_400_000).toISOString()
        })
      ]
    });

    opened();

    expect(await screen.findByText('Sample retry in 2d')).toBeTruthy();
  });

  it('marks a want whose copy the library calls something else as needing a person', async () => {
    answering({
      looking: [
        want({
          nextAttemptAt: null,
          waitingOnYou: true,
          summary: 'A copy arrived and was proven, but your library calls that file a different recording.'
        })
      ]
    });

    opened();

    expect(await screen.findByText('Needs you')).toBeTruthy();
    expect(await screen.findByText(/your library calls that file a different recording/)).toBeTruthy();
    expect(await screen.findByText('waiting for you')).toBeTruthy();
  });

  it('says a want has not been looked for yet rather than leaving the time blank', async () => {
    answering({
      looking: [want({ attempts: 0, lastAttemptAt: null, nextAttemptAt: null })]
    });

    opened();

    expect(await screen.findByText('not looked for yet')).toBeTruthy();
  });

  it('asks for the three states a want passes through in one request', async () => {
    answering({ looking: [want()] });

    opened();

    await screen.findByText('Talk Talk — Ascension Day');
    expect(asked.some((path) => path.includes('status=unresolved%2Cpending%2Csearching'))).toBe(
      true
    );
  });

  it('offers to stop looking, and stops the one want it was pressed on', async () => {
    answering({ looking: [want()] });

    opened();
    await fireEvent.click(await screen.findByRole('button', { name: 'Stop looking' }));

    await waitFor(() => expect(stopped).toEqual(['20000000-0000-4000-8000-000000000001']));
  });

  it('offers to take the best available copy from a parked want', async () => {
    answering({ looking: [want({ belowFloorSince: new Date(clock).toISOString() })] });

    opened();
    await fireEvent.click(await screen.findByRole('button', { name: 'Take best available' }));

    await waitFor(() => expect(takenBest).toEqual(['20000000-0000-4000-8000-000000000001']));
    expect(actions).toEqual([
      {
        method: 'POST',
        path: '/api/v1/acquisition-targets/20000000-0000-4000-8000-000000000001/take-best-available',
        body: undefined
      }
    ]);
  });

  it('shows a waiver and offers to keep the floor', async () => {
    answering({ looking: [want({ floorWaivedAt: new Date(clock).toISOString() })] });

    opened();

    expect(await screen.findByText('Floor waived')).toBeTruthy();
    await fireEvent.click(await screen.findByRole('button', { name: 'Keep the floor' }));

    await waitFor(() => expect(keptFloor).toEqual(['20000000-0000-4000-8000-000000000001']));
  });

  it('shows the wants somebody stopped under their own filter, with a way back', async () => {
    answering({
      looking: [want()],
      stopped: [want({ id: '20000000-0000-4000-8000-000000000002', status: 'not_wanted' })]
    });

    opened();
    // Pressed while the opening request is still in flight, which is when a
    // reader actually presses it. Another pile is another question, so it is
    // asked rather than swallowed as a refetch of the one already running.
    await fireEvent.click(await screen.findByRole('button', { name: /^Not wanted/ }));

    await fireEvent.click(await screen.findByRole('button', { name: 'Look again' }));
    await waitFor(() => expect(resumed).toEqual(['20000000-0000-4000-8000-000000000002']));
  });

  it('names the reason a list is empty rather than showing an empty list', async () => {
    answering({ looking: [] });

    opened();

    expect(await screen.findByText('Nothing is being looked for')).toBeTruthy();
  });

  it('carries the API notice as a banner while wishlist tracks are waiting on the audio check', async () => {
    answering({
      looking: [want()],
      notice: 'Wishlist tracks are waiting: add an AcoustID key in Settings to search for them.'
    });

    opened();

    expect(
      await screen.findByText(
        'Wishlist tracks are waiting: add an AcoustID key in Settings to search for them.'
      )
    ).toBeTruthy();
  });

  it('says nothing extra when the API sends no notice', async () => {
    answering({ looking: [want()] });

    opened();

    await screen.findByText('Talk Talk — Ascension Day');
    expect(screen.queryByText(/Wishlist tracks are waiting/)).toBeNull();
  });

  it("shows a waiting want's own summary under it", async () => {
    answering({
      looking: [
        want({
          status: 'pending',
          summary: 'Add an AcoustID key in Settings — nothing here can prove a copy is this recording.'
        })
      ]
    });

    opened();

    expect(
      await screen.findByText(
        'Add an AcoustID key in Settings — nothing here can prove a copy is this recording.'
      )
    ).toBeTruthy();
  });

  it('leaves off the summary once a want is no longer waiting on anything', async () => {
    answering({ looking: [want({ status: 'acquired', summary: 'looking for a copy' })] });

    opened();

    await screen.findByText('Talk Talk — Ascension Day');
    expect(screen.queryByText('looking for a copy')).toBeNull();
  });
});

// The loop announces itself over the event stream; the interval is the safety
// net, and it used to run every 60 seconds forever, on a page nobody was
// looking at. It should run only while something on the page is actually out
// searching.
describe('the poll for the wants list', () => {
  function refetchIntervalOf(client: QueryClient) {
    const query = client.getQueryCache().find({ queryKey: ['wants', 'list', 'looking', 0] });
    if (!query) throw new Error('the wants list query was never created');
    const options = query.options as { refetchInterval?: (q: Query<AcquisitionTargets>) => number | false };
    if (!options.refetchInterval) throw new Error('refetchInterval was not set');
    return options.refetchInterval(query as unknown as Query<AcquisitionTargets>);
  }

  it('polls while a want is searching', async () => {
    answering({ looking: [want({ status: 'searching' })] });
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(WantsTab, undefined, { wrapper: QueryClientProvider, wrapperProps: { client } });

    await screen.findByText('Talk Talk — Ascension Day');
    expect(refetchIntervalOf(client)).toBe(60_000);
  });

  it('does not poll once nothing is searching', async () => {
    answering({ looking: [want({ status: 'pending' })] });
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(WantsTab, undefined, { wrapper: QueryClientProvider, wrapperProps: { client } });

    await screen.findByText('Talk Talk — Ascension Day');
    expect(refetchIntervalOf(client)).toBe(false);
  });
});
