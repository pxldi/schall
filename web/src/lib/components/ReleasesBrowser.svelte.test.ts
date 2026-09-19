import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, setup } from '@testing-library/svelte';
import { QueryClient, QueryClientProvider } from '@tanstack/svelte-query';
import type { Release, ReleaseList } from '$lib/api';

// The browser is a page that has been sent to somebody as a link: it reads its
// scope, search, filter, sort, place — and now the artist it is narrowed to —
// out of the address bar, and asks the server for exactly that. So the two
// boundaries stubbed are the two it has: SvelteKit's router, which is where the
// address comes from and goes back to, and `fetch`. What is asserted is the
// request that went out, because the narrowing lives there and nowhere on
// screen: an artist reached by name would have brought along every artist whose
// own name contains theirs, and the page would look the same either way.
const routed = vi.hoisted(() => ({
  url: new URL('http://localhost/library'),
  goto: vi.fn(),
  replaceState: vi.fn(),
  listeners: [] as ((navigation: unknown) => void)[]
}));

vi.mock('$app/state', () => ({ page: routed }));
vi.mock('$app/navigation', () => ({
  goto: routed.goto,
  replaceState: routed.replaceState,
  afterNavigate: (callback: (navigation: unknown) => void) => routed.listeners.push(callback)
}));

const { default: ReleasesBrowser } = await import('$lib/components/ReleasesBrowser.svelte');
const { trackNavigation } = await import('$lib/navigation.svelte');

const anetha = '9b2f1c5d-3a44-4e21-8f0c-7d6b5a4e3c21';

function releaseRow(overrides: Partial<Release> = {}): Release {
  return {
    id: 'd1c0a1b2-1111-4222-8333-444455556666',
    artistId: anetha,
    artistName: 'Anetha',
    artistFollowed: true,
    musicbrainzReleaseGroupId: null,
    title: 'Mothearth',
    firstReleaseDate: '2022-06-24',
    albumType: 'album',
    trackCount: 8,
    ownedTrackCount: 3,
    dismissedTrackCount: 0,
    monitored: true,
    artistMonitorLevel: 'everything',
    musicbrainzReleaseId: null,
    trackRefreshStatus: 'completed',
    hasCover: true,
    ...overrides
  };
}

function releasePage(items: Release[]): ReleaseList {
  return { items, total: items.length, limit: 48, offset: 0, scopeTotal: items.length };
}

let asked: string[] = [];

/** Lets go of the first catalogue answer, which `answering(list, true)` holds
 * back. Between the page being drawn and that answer arriving is the window a
 * reader actually presses a filter in. */
let letTheFirstAnswerThrough = () => {};

function answering(list: ReleaseList, holdTheFirstAnswer = false) {
  const held = new Promise<void>((resolve) => (letTheFirstAnswerThrough = resolve));
  let outstanding = holdTheFirstAnswer;
  vi.stubGlobal(
    'fetch',
    vi.fn(async (path: string) => {
      asked.push(path);
      if (path.startsWith('/api/v1/artists/')) {
        return new Response(JSON.stringify({ id: anetha, name: 'Anetha' }), { status: 200 });
      }
      if (outstanding) {
        outstanding = false;
        await held;
      }
      return new Response(JSON.stringify(list), { status: 200 });
    })
  );
}

// A search that finds no release still has to ask the files view's own count,
// so the empty state's fetch stub answers both endpoints: the catalogue with
// `list`, and `/api/v1/library/files` with a total of `filesTotal` and no rows
// (the panel only ever reads the total).
function answeringWithFiles(list: ReleaseList, filesTotal: number) {
  vi.stubGlobal(
    'fetch',
    vi.fn(async (path: string) => {
      asked.push(path);
      if (path.startsWith('/api/v1/artists/')) {
        return new Response(JSON.stringify({ id: anetha, name: 'Anetha' }), { status: 200 });
      }
      if (path.startsWith('/api/v1/library/files')) {
        return new Response(
          JSON.stringify({ items: [], total: filesTotal, limit: 1, offset: 0 }),
          { status: 200 }
        );
      }
      return new Response(JSON.stringify(list), { status: 200 });
    })
  );
}

/** The catalogue requests, as URLs, in the order they were sent. */
function catalogueAsks() {
  return asked
    .filter((path) => path.startsWith('/api/v1/albums'))
    .map((path) => new URL(path, 'http://localhost'));
}

async function lastAsk() {
  await vi.waitFor(() => expect(catalogueAsks().length).toBeGreaterThan(0));
  return catalogueAsks()[catalogueAsks().length - 1];
}

/** The list on screen. */
function arrived() {
  return screen.findByText('Mothearth');
}

function openedAt(query: string) {
  routed.url = new URL(`http://localhost/library${query}`);
  // The client is per test: a cache shared between them would answer the second
  // one from the first one's page without asking anything.
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(ReleasesBrowser, undefined, {
    wrapper: QueryClientProvider,
    wrapperProps: { client }
  });
}

function clearButton() {
  return screen.getByRole('button', { name: 'Show every artist' });
}

/** What the address bar was last told, without the origin. */
function recorded() {
  const calls = routed.replaceState.mock.calls;
  return calls.length ? (calls[calls.length - 1][0] as string) : '';
}

// The browser lives inside a query client the way every page does, so the tests
// give it one the same way the application does, through the provider. The
// address bar is only written once the application is running, so these start
// it the way the application does too: the root layout's subscription, and then
// the one arrival that says SvelteKit has finished starting.
beforeAll(setup);

beforeAll(() => {
  trackNavigation();
  for (const listener of routed.listeners) {
    listener({ type: 'enter', from: null, to: { url: routed.url } });
  }
});

beforeEach(() => {
  asked = [];
  routed.replaceState.mockClear();
  answering(releasePage([releaseRow()]));
});

afterEach(() => {
  vi.unstubAllGlobals();
  cleanup();
});

describe('ReleasesBrowser', () => {
  it('asks the catalogue for the artist the address names', async () => {
    openedAt(`?artistId=${anetha}`);

    expect((await lastAsk()).searchParams.get('artistId')).toBe(anetha);
  });

  it('narrows by the artist rather than by searching for their name', async () => {
    openedAt(`?artistId=${anetha}`);

    expect((await lastAsk()).searchParams.get('q')).toBeNull();
  });

  // Scoped to the filter chip rather than a bare findByText: the release row
  // for Anetha's own album carries her name too once the list arrives, and a
  // plain text match answers to both once it has.
  it('names the artist the list is narrowed to', async () => {
    openedAt(`?artistId=${anetha}`);

    await vi.waitFor(() => expect(clearButton().parentElement?.textContent).toContain('Anetha'));
  });

  it('keeps the artist in the address bar while the filter stands', async () => {
    openedAt(`?artistId=${anetha}&status=missing`);

    await vi.waitFor(() => expect(recorded()).toContain(`artistId=${anetha}`));
  });

  it('asks for every artist again once the filter is cleared', async () => {
    openedAt(`?artistId=${anetha}`);
    await arrived();

    await fireEvent.click(clearButton());

    await vi.waitFor(() => expect(catalogueAsks().length).toBe(2));
    expect((await lastAsk()).searchParams.get('artistId')).toBeNull();
  });

  it('starts the list again at the first page when the filter is cleared', async () => {
    openedAt(`?artistId=${anetha}&offset=48`);
    await arrived();

    await fireEvent.click(clearButton());

    await vi.waitFor(() => expect(catalogueAsks().length).toBe(2));
    expect((await lastAsk()).searchParams.get('offset')).toBeNull();
  });

  it('takes the artist out of the address bar when the filter is cleared', async () => {
    openedAt(`?artistId=${anetha}`);
    await arrived();

    await fireEvent.click(clearButton());

    await vi.waitFor(() => expect(recorded()).not.toContain('artistId'));
  });

  it('clears every filter in one step', async () => {
    openedAt('?q=burial&status=missing&sort=year&dir=desc&scope=followed&offset=48');
    await arrived();

    await fireEvent.click(screen.getByRole('button', { name: 'Clear filters' }));

    expect((screen.getByRole('textbox', { name: 'Search releases' }) as HTMLInputElement).value).toBe('');
    expect(screen.queryByRole('button', { name: 'Clear filters' })).toBeNull();
  });

  // An artist the whole catalogue holds nothing by, which is what an artist
  // whose discography has never been fetched looks like from here.
  it('says whose releases are missing from an empty catalogue', async () => {
    answering(releasePage([]));

    const { container } = openedAt(`?artistId=${anetha}&scope=`);

    // Read off the whole panel rather than one node: the name sits in a span of
    // its own inside the sentence that carries it.
    await vi.waitFor(() =>
      expect(container.textContent).toContain('The catalogue holds no releases by Anetha')
    );
  });

  // A search that finds no release can still find the file: an unmatched
  // upload belongs to no release, so it never shows up here whatever it is
  // called. The reader who typed the search is the one who needs to be told
  // the Files tab has it.
  //
  // The href alone is the fresh-mount case: cmd-click, a shared link, or a
  // reload all land on Files with the search already in the address bar and
  // in `LibraryFiles`'s own read of `q`, the same as #489's link. The onclick
  // beside it covers the already-open tab and is left untested here — jsdom
  // has no router to click a real href against without trying to navigate for
  // real.
  it('names the files a search finds nothing among releases for, and links to them', async () => {
    answeringWithFiles(releasePage([]), 3);

    const { container } = openedAt(`?q=${encodeURIComponent('Illegal (Abo Edit)')}`);

    await vi.waitFor(() => expect(container.textContent).toContain('3 files match this search.'));
    // The count asked for is the same search the link lands on: this is the
    // files view's own endpoint and no other filter, so the number and where
    // the link goes can never disagree.
    expect(asked).toContain('/api/v1/library/files?q=Illegal+%28Abo+Edit%29&limit=1');
    const link = screen.getByRole('link', { name: 'Open Files' });
    expect(link.getAttribute('href')).toBe('/library?view=files&q=Illegal%20(Abo%20Edit)');
  });

  it('says nothing about files when the search finds none there either', async () => {
    answeringWithFiles(releasePage([]), 0);

    const { container } = openedAt('?q=Illegal');

    await vi.waitFor(() =>
      expect(container.textContent).toContain('Nothing here matches that search')
    );
    expect(screen.queryByRole('link', { name: 'Open Files' })).toBeNull();
  });

  it('leaves the address alone when nothing is narrowed', async () => {
    openedAt('');

    await vi.waitFor(() => expect(recorded()).toBe('/library'));
  });

  // The catalogue answers an id that is not one with a refusal rather than an
  // empty page, so a link carrying anything else — an older build's, a truncated
  // paste — has to read here as no filter at all.
  it('does not pass on an artist that is not an id', async () => {
    openedAt('?artistId=anetha');

    expect((await lastAsk()).searchParams.get('artistId')).toBeNull();
  });

  // The first page of the catalogue is still on its way when Missing is
  // pressed. The filter narrows on the server, so the press has to send a
  // request of its own; it used to ask the request already running to run again,
  // which asked nothing and left the strip on Missing over every release.
  it('asks for a filter pressed before the first page arrived', async () => {
    answering(releasePage([releaseRow()]), true);
    openedAt('');

    await fireEvent.click(await screen.findByRole('button', { name: /^Missing/ }));
    letTheFirstAnswerThrough();

    await vi.waitFor(() =>
      expect(catalogueAsks().map((url) => url.searchParams.get('status'))).toContain('missing')
    );
  });

  it('offers nothing to clear when the artist is not an id', async () => {
    openedAt('?artistId=anetha');
    await lastAsk();

    expect(screen.queryByRole('button', { name: 'Show every artist' })).toBeNull();
  });
});

// The rail of letters down the side of a long list. It is drawn only when it
// would tell the truth — the letters read A at the top and Z at the bottom, so
// they mean nothing unless the list is in that order — and it reaches only the
// rows this page asked the server for.
describe('the A to Z rail', () => {
  /** A page of releases whose artists start at A and run on from there, one
   * letter per pair, so a named letter either has rows or provably has none. */
  function spread(count: number): Release[] {
    return Array.from({ length: count }, (_, step) => {
      const letter = String.fromCharCode(65 + Math.floor(step / 2));
      return releaseRow({
        id: `d1c0a1b2-1111-4222-8333-4444555${String(step).padStart(5, '0')}`,
        artistName: `${letter}rtist ${step}`,
        title: `${letter} record ${step}`
      });
    });
  }

  function rail() {
    return screen.queryByRole('group', { name: 'Jump to a letter' });
  }

  // Twenty pairs, so A to T have rows and U to Z have none.
  const fullPage = spread(40);

  it('stands beside a list long enough to be worth aiming at', async () => {
    answering(releasePage(fullPage));
    openedAt('');
    await screen.findByText('A record 0');

    expect(rail()).toBeTruthy();
  });

  it('lets a letter this page holds rows for be pressed', async () => {
    answering(releasePage(fullPage));
    openedAt('');
    await screen.findByText('A record 0');

    expect(screen.getByRole('button', { name: 'Jump to B' })).not.toHaveProperty('disabled', true);
  });

  // A letter is always drawn, so the rail keeps its shape and its letters stay
  // under the same finger from one page to the next.
  it('draws a letter this page holds no rows for and refuses the press', async () => {
    answering(releasePage(fullPage));
    openedAt('');
    await screen.findByText('A record 0');

    expect(screen.getByRole('button', { name: 'Jump to Z' })).toHaveProperty('disabled', true);
  });

  it('goes to the first row of the letter that was pressed', async () => {
    const reached: string[] = [];
    // jsdom lays nothing out, so scrolling is not implemented there. What is
    // asked here is which row the rail chose, which is the part that can be
    // wrong.
    Element.prototype.scrollIntoView = function () {
      reached.push((this as HTMLElement).id);
    };
    answering(releasePage(fullPage));
    openedAt('');
    await screen.findByText('A record 0');

    await fireEvent.click(screen.getByRole('button', { name: 'Jump to C' }));

    // C's rows are the fifth and sixth; the rail goes to the fifth.
    expect(reached).toEqual([`release-${fullPage[4].id}`]);
  });

  it('stays away from a list that is one screenful and a half', async () => {
    answering(releasePage(spread(38)));
    openedAt('');
    await screen.findByText('A record 0');

    expect(rail()).toBeNull();
  });

  // Sorted by year the list is in no alphabetical order at all, and a letter
  // down the side would point at whatever row happened to be there.
  it('stays away from a list that is not in the order of the alphabet', async () => {
    answering(releasePage(fullPage));
    openedAt('?sort=year');
    await screen.findByText('A record 0');

    expect(rail()).toBeNull();
  });

  // Reversed, the list runs Z to A and the rail would read the wrong way round.
  it('stays away from a list running the other way', async () => {
    answering(releasePage(fullPage));
    openedAt('?sort=title&dir=desc');
    await screen.findByText('A record 0');

    expect(rail()).toBeNull();
  });
});

// The selection bar's three decisions. Each of them answers per release, and
// what is pinned here is that the answer reaches the reader: they pressed a
// button about a selection, and how much of it was already decided is the only
// thing the press actually taught them.
describe('deciding about a selection of releases', () => {
  /** Ticks the first row and returns the selection bar's buttons. */
  async function selectOne(answer: unknown, path: string) {
    vi.stubGlobal(
      'fetch',
      vi.fn(async (asking: string, init?: RequestInit) => {
        asked.push(asking);
        if (asking.startsWith(path) && init?.method === 'POST') {
          return new Response(JSON.stringify(answer), { status: 200 });
        }
        return new Response(JSON.stringify(releasePage([releaseRow()])), { status: 200 });
      })
    );
    openedAt('');
    await arrived();
    await fireEvent.click(screen.getByRole('button', { name: 'Select' }));
    await fireEvent.click(screen.getByRole('button', { name: 'Select Mothearth' }));
  }

  it('says how many tracks a press ignored and how many were already ignored', async () => {
    await selectOne(
      {
        releases: [{ albumId: 'a', dismissed: 4, alreadyDismissed: 2, owned: 0, unresolvable: 0 }],
        notFound: []
      },
      '/api/v1/releases/ignore'
    );

    await fireEvent.click(screen.getByRole('button', { name: /Ignore/ }));

    expect(await screen.findByText('Ignored 4 tracks · 2 already ignored')).toBeTruthy();
  });

  it('says which of a selection was already being matched again', async () => {
    await selectOne(
      {
        releases: [
          { releaseId: 'a', queued: true },
          { releaseId: 'b', queued: false }
        ],
        unaskable: []
      },
      '/api/v1/releases/rematch'
    );

    await fireEvent.click(screen.getByRole('button', { name: /Match again/ }));

    expect(await screen.findByText('Matching 1 release again · 1 already running')).toBeTruthy();
  });

  it('says how much of a selection had nothing that failed', async () => {
    await selectOne(
      {
        releases: [
          { releaseId: 'a', retried: 3 },
          { releaseId: 'b', retried: 0 }
        ]
      },
      '/api/v1/releases/retry'
    );

    await fireEvent.click(screen.getByRole('button', { name: /Try again/ }));

    expect(await screen.findByText('Trying 3 jobs again · 1 had nothing that failed')).toBeTruthy();
  });
});

// The Status column used to repeat the Owned column's own figures for a plain
// gap. It still carries a tag for whatever ownership cannot explain.
describe('the Status column', () => {
  it('shows a partial release\'s "N of M" once, with no status tag beside it', async () => {
    answering(releasePage([releaseRow({ ownedTrackCount: 3, trackCount: 8 })]));
    openedAt('');
    await arrived();

    // Before the fix this figure was drawn twice: once by `OwnedBar` in the
    // Owned column, once more by the tag `statusOf` built for the same gap.
    expect(await screen.findAllByText('3 of 8')).toHaveLength(1);
  });

  it('still tags a release whose download failed', async () => {
    answering(releasePage([releaseRow({ trackRefreshStatus: 'failed' })]));
    openedAt('');

    expect(await screen.findByText('failed')).toBeTruthy();
  });
});

describe('the cover slot', () => {
  it('keeps its box when a release has no artwork', async () => {
    const { container } = openedAt('');
    await arrived();

    // A missing cover answers 404: fire the same event the browser would.
    await fireEvent.error(container.querySelector('img')!);

    expect(screen.getByRole('img', { name: 'Cover for Mothearth' })).toBeTruthy();
  });

  it('asks for no picture when the row says none is cached', async () => {
    answering(releasePage([releaseRow({ hasCover: false })]));
    const { container } = openedAt('');
    await screen.findByText('Mothearth');

    expect(container.querySelector('img')).toBeNull();
    expect(screen.getByRole('img', { name: 'Cover for Mothearth' })).toBeTruthy();
  });
});
