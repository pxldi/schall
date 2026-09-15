import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, setup } from '@testing-library/svelte';
import { QueryClient, QueryClientProvider } from '@tanstack/svelte-query';
import type { SourceSearchResult, SourceSearchRun } from '$lib/api';

// Named without the leading `+` that the page itself carries: SvelteKit reads
// every `+` file in a route folder as a route file and has no meaning for this
// one. A file beside it without the plus is a colocated module, which is what a
// test is.
//
// This page shows one source-search run: what was searched for, and what peers
// offered. Everything on it comes from one request, so the request failing is
// the whole screen failing, and what the reader is told then is what these
// tests are about. Nobody is asked to press Try again for a fault a second
// request would have cured, and nobody is offered a button that cannot help.
//
// The two boundaries stubbed are the two the page has: SvelteKit's router,
// which is where the run identifier in the address comes from, and `fetch`.
const runId = 'a3f21c40-8f0b-4e42-9d1c-6b5a4e3c2110';

vi.mock('$app/state', () => ({
  page: {
    params: { runId },
    url: new URL(`http://localhost/releases/sources/${runId}`)
  }
}));

vi.mock('$app/navigation', () => ({
  afterNavigate: () => {},
  goto: vi.fn(),
  replaceState: vi.fn()
}));

const { default: SourcesRun } = await import('./+page.svelte');

function result(overrides: Partial<SourceSearchResult> = {}): SourceSearchResult {
  return {
    albumId: 'b17c5d2e-4a44-4e21-8f0c-7d6b5a4e3c21',
    albumTitle: 'Laughing Stock',
    artistId: 'c28d6e3f-5b55-4f32-9a1d-8e7c6b5d4e32',
    artistName: 'Talk Talk',
    trackCount: 6,
    ownedTrackCount: 0,
    firstReleaseDate: '1991-09-16',
    status: 'none',
    query: 'talk talk laughing stock',
    candidates: [],
    refused: [],
    refusedBelowBitRate: 0,
    searchedAt: '2026-08-12T10:00:00Z',
    autoRequestedAt: null,
    ...overrides
  };
}

function runBody(): SourceSearchRun {
  return {
    id: runId,
    pending: 0,
    total: 1,
    foundCount: 0,
    noneCount: 1,
    cancelledCount: 0,
    failedCount: 0,
    requestedAt: '2026-08-12T09:59:00Z',
    items: [result()]
  };
}

/** What the server does to each request in turn, in order. The last answer
 * stands for every request after it, which is what lets a test say "unreachable
 * from now on" without counting the tries the page makes. */
type Answer = () => Promise<Response>;

let answers: Answer[] = [];
let asked = 0;

function answering(...planned: Answer[]) {
  answers = planned;
  asked = 0;
  vi.stubGlobal(
    'fetch',
    vi.fn(async () => {
      const answer = answers[Math.min(asked, answers.length - 1)];
      asked += 1;
      return answer();
    })
  );
}

const ok: Answer = async () => new Response(JSON.stringify(runBody()), { status: 200 });
const unreachable: Answer = async () => {
  throw new TypeError('Failed to fetch');
};
const missing: Answer = async () =>
  new Response(JSON.stringify({ title: 'search not found' }), { status: 404 });
const refused: Answer = async () =>
  new Response(JSON.stringify({ title: 'internal error', details: ['read source search: ECONNRESET'] }), {
    status: 500
  });

// `retryDelay` is the client's, not the page's: the page decides how many times
// to ask and leaves the waiting between tries to the default backoff, so a test
// can take the wait away without touching what is under test.
function opened() {
  const client = new QueryClient({ defaultOptions: { queries: { retryDelay: 0 } } });
  render(SourcesRun, undefined, { wrapper: QueryClientProvider, wrapperProps: { client } });
  return client;
}

beforeAll(setup);

beforeEach(() => {
  answers = [];
  asked = 0;
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe('the page heading', () => {
  it('names the room in a level-1 heading', () => {
    answering(ok);
    opened();

    expect(screen.getByRole('heading', { level: 1, name: 'Sources' })).toBeTruthy();
  });
});

describe('a run whose request fails and then works', () => {
  it('shows the run and says nothing about the failure', async () => {
    answering(unreachable, ok);
    opened();

    expect(await screen.findByText('Laughing Stock')).toBeTruthy();
    expect(screen.queryByText('The server did not answer')).toBeNull();
    expect(screen.queryByRole('button', { name: 'Try again' })).toBeNull();
  });
});

describe('a run the server does not have', () => {
  it('says so, and sends the reader where a search is started', async () => {
    answering(missing);
    opened();

    expect(await screen.findByText('This search is not on the server')).toBeTruthy();
    const library = screen.getByRole('link', { name: 'Open Library' });
    expect(library.getAttribute('href')).toBe('/library?status=missing');
  });

  it('offers nothing to press again, and does not ask twice', async () => {
    answering(missing);
    opened();

    await screen.findByText('This search is not on the server');
    expect(screen.queryByRole('button', { name: 'Try again' })).toBeNull();
    expect(asked).toBe(1);
  });
});

describe('a server that cannot be reached', () => {
  it('asks three times before it says anything', async () => {
    answering(unreachable);
    opened();

    await screen.findByText('The server did not answer');
    expect(asked).toBe(3);
  });

  it('offers Try again beside the reason, and asks again when it is pressed', async () => {
    answering(unreachable);
    opened();

    await screen.findByText('The server did not answer');
    expect(screen.getByText('Schall could not be reached. Try again once it is running.')).toBeTruthy();

    answering(ok);
    await fireEvent.click(screen.getByRole('button', { name: 'Try again' }));
    expect(await screen.findByText('Laughing Stock')).toBeTruthy();
  });
});

describe('a server that answers with a failure', () => {
  it('says the search could not be read and keeps the words the server used', async () => {
    answering(refused);
    opened();

    expect(await screen.findByText('The search could not be read')).toBeTruthy();
    // The whole of what the server said, title and detail together, because
    // that is what a bug report has to quote. It used to be the detail alone:
    // the disclosure now reads the same words every other failure discloses.
    expect(screen.getByText('internal error — read source search: ECONNRESET')).toBeTruthy();
    // Nothing to press again: three tries have already been spent on this
    // record of this search, and a fourth asks the same broken question. What
    // the screen offers instead is the place a new search is started, because
    // a screen with no control at all is a dead end.
    expect(screen.queryByRole('button', { name: 'Try again' })).toBeNull();
    const library = screen.getByRole('link', { name: 'Open Library' });
    expect(library.getAttribute('href')).toBe('/library?status=missing');
  });
});

describe('a run already on screen when the asking stops', () => {
  it('keeps the run and says what stopped, without taking the page away', async () => {
    answering(ok);
    const client = opened();

    await screen.findByText('Laughing Stock');

    // What the event stream does when a release settles, and what the interval
    // does while any release is still queued: it asks again for a run that is
    // already on screen.
    answering(unreachable);
    await client.invalidateQueries({ queryKey: ['source-search', runId] });

    expect(
      await screen.findByText('The server stopped answering. What is below is the last answer it gave.')
    ).toBeTruthy();
    expect(screen.getByText('Laughing Stock')).toBeTruthy();
    expect(screen.queryByText('The server did not answer')).toBeNull();
  });
});

describe('a run holding one of each ending', () => {
  it('counts only the releases a search was actually made for', async () => {
    // Four releases: one answered with peers, one pending, one stopped before
    // anybody looked, one the provider failed to answer. Only the first was
    // searched. The header used to take the states that are plainly not
    // searched off the total, which left the failed one inside the figure —
    // so the header said four while a row below it said "not searched".
    const mixed: SourceSearchRun = {
      id: runId,
      pending: 1,
      total: 4,
      foundCount: 1,
      noneCount: 0,
      cancelledCount: 1,
      failedCount: 1,
      requestedAt: '2026-08-12T09:59:00Z',
      items: [
        result({ albumTitle: 'Laughing Stock', status: 'found' }),
        result({ albumId: 'd39e7f40-6c66-4043-ab2e-9f8d7c6e5f43', albumTitle: 'Spirit of Eden', status: 'queued' }),
        result({ albumId: 'e4af8051-7d77-4154-bc3f-a09e8d7f6a54', albumTitle: 'The Colour of Spring', status: 'cancelled' }),
        result({ albumId: 'f5b09162-8e88-4265-cd40-b1af9e807b65', albumTitle: 'It’s My Life', status: 'failed' })
      ]
    };
    answering(async () => new Response(JSON.stringify(mixed), { status: 200 }));
    opened();

    await screen.findByText('Laughing Stock');
    expect(screen.getByText('of 4 searched').previousElementSibling?.textContent).toBe('1');
    // "not searched" is said twice: once as the figure in the header, once as
    // the chip on the failed release's own row. The header is the first of the
    // two, and the two now agree instead of contradicting each other.
    const notSearched = screen.getAllByText('not searched');
    expect(notSearched).toHaveLength(2);
    expect(notSearched[0].previousElementSibling?.textContent).toBe('1');
  });

  // A release whose every offer was under the bitrate floor found nothing, and
  // saying "no peer was sharing this" points at the network when the one thing
  // that can change the answer is a setting.
  it('counts the copies the bit rate floor refused instead of saying nobody had it', async () => {
    const refused: SourceSearchRun = {
      ...runBody(),
      items: [
        result({
          status: 'none',
          refusedBelowBitRate: 2,
          refused: [
            {
              username: 'peer one',
              directory: 'Laughing Stock [MP3]',
              format: 'mp3',
              reason: 'MP3 at 128 kbps is below the 320 kbps you asked for',
              kind: 'bit_rate'
            },
            {
              username: 'peer two',
              directory: 'Laughing Stock [V2]',
              format: 'mp3',
              reason: 'MP3 at 192 kbps is below the 320 kbps you asked for',
              kind: 'bit_rate'
            }
          ]
        })
      ]
    };
    answering(async () => new Response(JSON.stringify(refused), { status: 200 }));
    opened();

    await screen.findByText('2 copies below your minimum bit rate');
    expect(screen.queryByText('No peer was sharing this while the search ran.')).toBeNull();
    expect(
      screen.getByText('Copies were on offer, but none matched your format settings.')
    ).toBeTruthy();
    expect(screen.getByText('MP3 at 128 kbps is below the 320 kbps you asked for')).toBeTruthy();
    const link = screen.getByText('Change your minimum bit rate');
    expect(link.getAttribute('href')).toBe('/settings#bitrate-floor');
  });

  // Two rules can turn copies away, and they send the reader to different
  // settings. The line only names the floor when the floor is all of it.
  it('does not blame the bit rate floor for a copy a format list refused', async () => {
    const refused: SourceSearchRun = {
      ...runBody(),
      items: [
        result({
          status: 'none',
          refusedBelowBitRate: 1,
          refused: [
            {
              username: 'peer one',
              directory: 'Laughing Stock [MP3]',
              format: 'mp3',
              reason: 'MP3 at 128 kbps is below the 320 kbps you asked for',
              kind: 'bit_rate'
            },
            {
              username: 'peer two',
              directory: 'Laughing Stock [DSF]',
              format: 'dsf',
              reason: 'DSF is a format you do not accept',
              kind: 'format'
            }
          ]
        })
      ]
    };
    answering(async () => new Response(JSON.stringify(refused), { status: 200 }));
    opened();

    await screen.findByText('2 copies outside your format settings');
    const link = screen.getByText('Change the formats you accept');
    expect(link.getAttribute('href')).toBe('/settings');
  });
});
