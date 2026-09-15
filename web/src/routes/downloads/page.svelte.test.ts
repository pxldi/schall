import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, setup, within } from '@testing-library/svelte';
import { QueryClient, QueryClientProvider } from '@tanstack/svelte-query';
import type { DownloadCounts, DownloadRequest, DownloadRequests } from '$lib/api';

// Named without the leading `+`, like the other colocated route test: SvelteKit
// reads every `+` file in a route folder as a route file.
//
// This page lists what Schall asked other people on the Soulseek network for.
// A request is open while the transfer is still under way, and once the copy
// arrives it is imported, held for a person, or not used. There are thousands
// of settled requests and a handful of open ones, so which of them the page
// asks the server for is the whole behaviour under test: it used to ask for the
// hundred most recent requests and sieve those for the open ones, which found
// four of twenty and left sixteen live transfers unreachable.
vi.mock('$app/state', () => ({
  page: { params: {}, url: new URL('http://localhost/downloads') }
}));

vi.mock('$app/navigation', () => ({
  afterNavigate: () => {},
  goto: vi.fn(),
  replaceState: vi.fn()
}));

const { default: Downloads } = await import('./+page.svelte');

function request(overrides: Partial<DownloadRequest> = {}): DownloadRequest {
  return {
    id: 'f1a2b3c4-0000-4000-8000-000000000001',
    albumId: 'a1b2c3d4-0000-4000-8000-000000000001',
    albumTitle: 'Spirit of Eden',
    artistId: 'b1c2d3e4-0000-4000-8000-000000000001',
    artistName: 'Talk Talk',
    provider: 'slskd',
    username: 'peer-one',
    directory: '/music/spirit',
    status: 'started',
    fileCount: 6,
    expectedTrackCount: 6,
    totalSizeBytes: 1_000_000,
    format: 'flac',
    score: 0.9,
    reasons: [],
    files: [],
    progress: {
      transferCount: 6,
      completedCount: 1,
      failedCount: 0,
      queuedCount: 0,
      transferredBytes: 100_000
    },
    startable: false,
    retryableCount: 0,
    requestedAt: '2026-08-01T10:00:00Z',
    startedAt: '2026-08-01T10:01:00Z',
    cancelledAt: null,
    importStatus: 'pending',
    importedAt: null,
    importReviews: [],
    importDecisions: [],
    revalidatable: false,
    ...overrides
  };
}

function counts(overrides: Partial<DownloadCounts> = {}): DownloadCounts {
  return { open: 20, review: 3, imported: 620, discarded: 40, failed: 288, all: 971, ...overrides };
}

function listing(overrides: Partial<DownloadRequests> = {}): DownloadRequests {
  return { items: [request()], total: 20, limit: 0, offset: 0, counts: counts(), ...overrides };
}

/** Every address the page asked for, in order, so a test can say which pile it
 * asked the server for rather than which rows it kept. */
let asked: string[] = [];

/** Lets go of the first list answer, which `answering(body, true)` holds back.
 * Between the screen being drawn and that answer arriving is the window a
 * reader actually presses a filter in, and it is the window this page used to
 * lose the press in. */
let letTheFirstAnswerThrough = () => {};

function answering(body: () => DownloadRequests, holdTheFirstAnswer = false) {
  asked = [];
  const held = new Promise<void>((resolve) => (letTheFirstAnswerThrough = resolve));
  let outstanding = holdTheFirstAnswer;
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), 'http://localhost');
      asked.push(url.pathname + url.search);
      if (url.pathname === '/api/v1/downloads') {
        if (outstanding) {
          outstanding = false;
          await held;
        }
        return new Response(JSON.stringify(body()), { status: 200 });
      }
      // The uploads tab reads its own list for the figure on its tab.
      return new Response(JSON.stringify({ items: [] }), { status: 200 });
    })
  );
}

function opened() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(Downloads, undefined, { wrapper: QueryClientProvider, wrapperProps: { client } });
  return client;
}

function downloadsAsked() {
  return asked.filter((address) => address.startsWith('/api/v1/downloads'));
}

beforeAll(setup);

beforeEach(() => {
  asked = [];
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe('the page heading', () => {
  it('names the room in a hidden level-1 heading', async () => {
    answering(listing);
    opened();

    await screen.findByRole('article');
    expect(screen.getByRole('heading', { level: 1, name: 'Downloads' })).toBeTruthy();
  });
});

describe('the pile the screen opens on', () => {
  it('asks the server for all open transfers', async () => {
    answering(listing);
    opened();

    await screen.findByRole('article');
    expect(downloadsAsked()[0]).toBe('/api/v1/downloads?view=open');
  });

  it('shows the server’s count of open work, not a count of the rows on screen', async () => {
    // Twenty open transfers are in the answer. The figure beside
    // the filter is the server's, which is the same rule the navigation badge
    // counts by, so the two cannot disagree.
    answering(() => listing({ items: [request()], total: 20 }));
    opened();

    await screen.findByRole('article');
    expect(screen.getByRole('button', { name: /^Open/ }).textContent).toContain('20');
  });

  it('names every other pile with what it holds', async () => {
    answering(listing);
    opened();

    await screen.findByRole('article');
    for (const [name, figure] of [
      ['Needs review', '3'],
      ['Imported', '620'],
      ['Discarded', '40'],
      ['Failed', '288'],
      ['All', '971']
    ]) {
      expect(screen.getByRole('button', { name: new RegExp(`^${name}`) }).textContent).toContain(figure);
    }
  });

  it('exposes transfer progress', async () => {
    answering(listing);
    opened();

    const row = await screen.findByRole('article');
    const progressbar = within(row).getByRole('progressbar', {
      name: 'Download progress for Talk Talk — Spirit of Eden'
    });
    expect(progressbar.getAttribute('aria-valuenow')).toBe('10');
    expect(progressbar.getAttribute('aria-valuemin')).toBe('0');
    expect(progressbar.getAttribute('aria-valuemax')).toBe('100');
  });

  it('announces a failed transfer', async () => {
    answering(() => listing({ items: [request({ status: 'failed' })] }));
    opened();

    const row = await screen.findByRole('article');
    expect(within(row).getByText('Failed').getAttribute('aria-live')).toBe('polite');
  });
});

describe('choosing another pile', () => {
  it('asks the server for that pile', async () => {
    answering(listing);
    opened();

    await screen.findByRole('article');
    await fireEvent.click(screen.getByRole('button', { name: /^Imported/ }));

    await vi.waitFor(() => {
      expect(downloadsAsked().at(-1)).toBe('/api/v1/downloads?view=imported');
    });
  });
});

describe("a row's own name", () => {
  it('draws the release before the artist, the artist dim', async () => {
    answering(listing);
    opened();

    const row = await screen.findByRole('article');
    expect(
      within(row).getByText(
        (_, node) => node?.textContent?.replace(/\s+/g, ' ').trim() === 'Spirit of Eden · Talk Talk'
      )
    ).toBeTruthy();
  });
});

describe('a paused import', () => {
  it('offers Decide on the row itself, addressed to this download', async () => {
    const item = request({
      status: 'completed',
      importStatus: 'needs_review',
      importEvidence: { files: [], unmatchedTracks: [], problems: [] }
    });
    answering(() => listing({ items: [item] }));
    opened();

    const row = await screen.findByRole('article');
    expect(within(row).getByRole('link', { name: 'Decide' }).getAttribute('href')).toBe(
      `/review?download=${item.id}`
    );
  });
});

describe('the search field', () => {
  it('narrows the list to rows that match what was typed', async () => {
    const other = request({
      id: 'f1a2b3c4-0000-4000-8000-000000000002',
      albumTitle: 'Moth Music',
      artistName: 'Nemo Vice'
    });
    answering(() => listing({ items: [request(), other] }));
    opened();

    await screen.findByText('Moth Music');
    await fireEvent.input(screen.getByLabelText('Filter downloads'), {
      target: { value: 'moth' }
    });

    await vi.waitFor(() => {
      expect(screen.queryByText('Spirit of Eden')).toBeNull();
    });
    expect(screen.getByText('Moth Music')).toBeTruthy();
  });
});

describe('a pile pressed while the opening request is still in flight', () => {
  it('asks the server for that pile too', async () => {
    // The open transfers are still on their way when Imported is pressed. Each
    // pile is its own question, so the press has to send one; it used to ask the
    // request already running to run again, which asked nothing and left the
    // strip naming a pile the rows under it were not.
    answering(listing, true);
    opened();

    await fireEvent.click(await screen.findByRole('button', { name: /^Imported/ }));
    letTheFirstAnswerThrough();

    await vi.waitFor(() => {
      expect(downloadsAsked()).toContain('/api/v1/downloads?view=imported');
    });
  });
});

describe('an open pile with nothing in it', () => {
  it('says nothing is downloading and leaves the other piles counted beside it', async () => {
    answering(() => listing({ items: [], total: 0, counts: counts({ open: 0 }) }));
    opened();

    expect(await screen.findByText('Nothing downloading')).toBeTruthy();
    expect(screen.getByRole('link', { name: 'Search releases' }).getAttribute('href')).toBe('/releases');
    expect(screen.getByRole('button', { name: /^Imported/ }).textContent).toContain('620');
  });

  it('sends a reader who has never asked for anything to the library', async () => {
    answering(() =>
      listing({
        items: [],
        total: 0,
        counts: { open: 0, review: 0, imported: 0, discarded: 0, failed: 0, all: 0 }
      })
    );
    opened();

    expect(await screen.findByText('Nothing requested yet')).toBeTruthy();
    expect(screen.getByRole('link', { name: 'Search releases' }).getAttribute('href')).toBe('/releases');
  });
});

// A want is a recording somebody asked Schall to find, before any transfer for
// it exists. It is one tab along from the transfers because it is the same
// question one step earlier, and it is the only screen that answers it: Review
// has the wants with a question, this page's own list the ones with a download.
describe('the wants, one tab along from the transfers', () => {
  it('asks for the wants that are being looked for', async () => {
    answering(listing);
    opened();

    await screen.findByRole('article');
    await fireEvent.click(screen.getByRole('button', { name: /^Wishlist/ }));

    await vi.waitFor(() => {
      expect(asked).toContain(
        '/api/v1/acquisition-targets?status=unresolved%2Cpending%2Csearching&limit=25'
      );
    });
  });
});

// Three places on this row used to print the raw string a failure carried
// straight onto the screen, ahead of `errors.ts`'s sentinel map: the
// request's own failure, the reason an import paused, and a file's transfer
// failure. All three now go through `ErrorNote`, which writes the sentence
// the reader gets and keeps the server's exact words behind a disclosure —
// the same shape `RowProblem` already draws for a button's own failure.
describe('a raw failure kept behind the row', () => {
  it('writes the sentence for the request failure, with the raw words behind a disclosure', async () => {
    answering(() =>
      listing({
        items: [
          request({ status: 'failed', error: 'could not start the transfer', startable: false })
        ]
      })
    );
    opened();

    await fireEvent.click(await screen.findByRole('button', { name: 'Show requested files' }));

    expect(await screen.findByText('The peer did not start sending.')).toBeTruthy();

    const disclosure = screen.getByText('What the server said').closest('details');
    expect(disclosure).toBeTruthy();
    expect((disclosure as HTMLDetailsElement).open).toBe(false);
    expect(disclosure?.textContent).toContain('could not start the transfer');
  });

  it('writes the sentence for a paused import, with the raw words behind a disclosure', async () => {
    answering(() =>
      listing({
        items: [
          request({
            status: 'completed',
            importStatus: 'needs_review',
            importError: 'is no longer readable audio'
          })
        ]
      })
    );
    opened();

    await fireEvent.click(await screen.findByRole('button', { name: 'Show requested files' }));

    expect(await screen.findByText('This file can no longer be read as audio.')).toBeTruthy();

    const disclosure = screen.getByText('What the server said').closest('details');
    expect(disclosure).toBeTruthy();
    expect((disclosure as HTMLDetailsElement).open).toBe(false);
    expect(disclosure?.textContent).toContain('is no longer readable audio');
  });

  it('writes the sentence for a failed file transfer, with the raw words behind a disclosure', async () => {
    answering(() =>
      listing({
        items: [
          request({
            status: 'failed',
            files: [
              {
                name: 'Spirit of Eden.flac',
                extension: 'flac',
                sizeBytes: 40_000_000,
                transfer: {
                  path: 'peer-one/Spirit of Eden.flac',
                  status: 'failed',
                  transferredBytes: 0,
                  error: 'the copy is no longer there',
                  retryable: false,
                  attempt: 1,
                  attempts: []
                }
              }
            ]
          })
        ]
      })
    );
    opened();

    await fireEvent.click(await screen.findByRole('button', { name: 'Show requested files' }));

    expect(
      await screen.findByText('The downloaded copy has gone from the volume.')
    ).toBeTruthy();

    const disclosure = screen.getByText('What the server said').closest('details');
    expect(disclosure).toBeTruthy();
    expect((disclosure as HTMLDetailsElement).open).toBe(false);
    expect(disclosure?.textContent).toContain('the copy is no longer there');
  });
});

// A few Soulseek peers hold every transfer until a word is typed back in
// private chat. Schall answers the ones it recognises by itself; a message it
// cannot read is shown here, with the peer's own words, and answered by hand.
describe('a peer that asked something', () => {
  const question = {
    username: 'PSXDupe',
    message: 'I am happy to share these files with anyone who is sharing.',
    askedAt: '2026-09-04T08:32:24Z'
  };

  function answeringWithARecord(sent: { method?: string; body: string | null }[]) {
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = new URL(String(input), 'http://localhost');
        if (url.pathname === '/api/v1/downloads') {
          return new Response(
            JSON.stringify(listing({ items: [request({ status: 'failed', peerQuestion: question })] })),
            { status: 200 }
          );
        }
        if (url.pathname.endsWith('/peer-reply')) {
          sent.push({
            method: init?.method,
            body: typeof init?.body === 'string' ? init.body : null
          });
          return new Response(JSON.stringify(request()), { status: 200 });
        }
        return new Response(JSON.stringify({ items: [] }), { status: 200 });
      })
    );
  }

  it('shows what the peer said and what to do about it', async () => {
    answeringWithARecord([]);
    opened();

    expect(
      await screen.findByText(
        'Peer PSXDupe wrote: “I am happy to share these files with anyone who is sharing.”'
      )
    ).toBeTruthy();
    expect(
      screen.getByText('Schall could not read this. If it asks for something, type it and press Send.')
    ).toBeTruthy();
  });

  it('sends what was typed to that peer', async () => {
    const sent: { method?: string; body: string | null }[] = [];
    answeringWithARecord(sent);
    opened();

    const field = await screen.findByLabelText('Reply to PSXDupe');
    await fireEvent.input(field, { target: { value: 'open sesame' } });
    await fireEvent.click(screen.getByRole('button', { name: 'Send' }));

    await vi.waitFor(() => {
      expect(sent).toHaveLength(1);
    });
    expect(sent[0].method).toBe('POST');
    expect(sent[0].body).toBe(JSON.stringify({ username: 'PSXDupe', message: 'open sesame' }));
  });

  it('sends nothing until something is typed', async () => {
    const sent: { method?: string; body: string | null }[] = [];
    answeringWithARecord(sent);
    opened();

    const send = await screen.findByRole('button', { name: 'Send' });
    expect(send.hasAttribute('disabled')).toBe(true);
    await fireEvent.click(send);
    expect(sent).toHaveLength(0);
  });
});
