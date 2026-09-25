import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, setup } from '@testing-library/svelte';
import { QueryClient, QueryClientProvider } from '@tanstack/svelte-query';
import Recommendations from '$lib/components/Recommendations.svelte';
import type { Recommendation, RecommendationList } from '$lib/api';

// This view offers music the library does not hold. What it has to get right is
// what it does not show: five rules hold suggestions back, and a list that
// silently dropped two thirds of the source's answer would read as a source
// with little to say. So the count of what was held back, and the reason for
// it, are asserted as plainly as the rows themselves.
//
// The two writes are asserted the same way: a dismissal is permanent, and a
// showing is what the fifth rule counts, so both have to leave the browser.
//
// The only boundary stubbed is `fetch`.

const clock = new Date('2026-08-11T14:00:00Z').getTime();

function suggestion(overrides: Partial<Recommendation> = {}): Recommendation {
  return {
    recordingId: 'e2000000-0000-4000-8000-000000000001',
    releaseGroupId: 'b2000000-0000-4000-8000-000000000001',
    artistIds: ['a2000000-0000-4000-8000-000000000001'],
    recordingTitle: 'Weather Report',
    releaseTitle: 'Long Reach',
    artistName: 'Halyard',
    rank: 1,
    reasonCodes: ['cf_raw'],
    ...overrides
  };
}

function answer(overrides: Partial<RecommendationList> = {}): RecommendationList {
  const items = overrides.items ?? [suggestion()];
  return {
    items,
    total: items.length,
    limit: 25,
    offset: 0,
    hidden: {
      total: 0,
      owned: 0,
      requested: 0,
      notWanted: 0,
      followedArtist: 0,
      followedLabel: 0,
      dismissed: 0,
      impressionFatigue: 0,
      unkept: 0
    },
    snapshot: {
      source: 'listenbrainz',
      fetched: true,
      status: 'complete',
      detail: '',
      fetchedAt: new Date(clock - 2 * 60 * 60_000).toISOString()
    },
    // The ordinary shape: a stored list with no sweep in the job queue behind
    // it, which is what a seeded or restored database looks like. Nothing is
    // said about refreshing unless a sweep has run and left the list alone.
    refresh: {
      attempted: false,
      succeeded: false,
      lastAttemptAt: null,
      nextAttemptAt: null
    },
    ...overrides
  };
}

// jsdom lays nothing out, so it has no IntersectionObserver and nothing is ever
// on screen by itself. This stands in for it, and it is also what lets a test
// say which rows the reader can see: `looking(...)` is the reader scrolling.
const watched = new Map<Element, (entries: IntersectionObserverEntry[]) => void>();

class FakeIntersectionObserver {
  constructor(private answer: (entries: IntersectionObserverEntry[]) => void) {}
  observe(row: Element) {
    watched.set(row, this.answer);
  }
  unobserve(row: Element) {
    watched.delete(row);
  }
  disconnect() {
    for (const [row, answer] of watched) if (answer === this.answer) watched.delete(row);
  }
}

/** The reader looking at the rows with these titles, and at nothing else. */
async function looking(...titles: string[]) {
  for (const [row, answer] of [...watched]) {
    const showing = titles.some((title) => row.textContent?.includes(title));
    answer([{ target: row, isIntersecting: showing } as IntersectionObserverEntry]);
  }
  // A row counts once it has stayed on screen for a moment, and the report of a
  // screenful is collected before it is sent.
  await vi.advanceTimersByTimeAsync(1500);
}

/** Every write the view made, in the order it made them. */
let posted: { path: string; body: unknown }[] = [];
/** Every list read the view made, as the address it asked. */
let read: string[] = [];

function answering(list: RecommendationList) {
  posted = [];
  read = [];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (path: string, init?: RequestInit) => {
      if (init?.method && init.method !== 'GET') {
        posted.push({ path, body: init.body ? JSON.parse(String(init.body)) : undefined });
        return new Response(JSON.stringify({}), { status: 200 });
      }
      read.push(path);
      return new Response(JSON.stringify(list));
    })
  );
}

/** The same, for the writes whose answer the view reads back: creating a want
 * answers with the want, which may be one that already existed. */
function answeringWith(list: RecommendationList, target: Record<string, unknown>) {
  posted = [];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (path: string, init?: RequestInit) => {
      if (init?.method && init.method !== 'GET') {
        posted.push({ path, body: init.body ? JSON.parse(String(init.body)) : undefined });
        return new Response(JSON.stringify(target), { status: 200 });
      }
      return new Response(JSON.stringify(list));
    })
  );
}

/** A stored list of that many suggestions, in the order a sweep wrote it. */
function suggestions(count: number): Recommendation[] {
  return Array.from({ length: count }, (_, index) =>
    suggestion({
      recordingId: `e2000000-0000-4000-8000-0000000000${String(index).padStart(2, '0')}`,
      recordingTitle: `Suggestion ${index + 1}`,
      rank: index + 1
    })
  );
}

// A stand-in for the read that addresses a page the way the server does, so
// that pressing Next here means what it means there. It decides no suppression
// of its own: `visible` is what the five rules have left, and a test moves it.
function serving(visible: () => Recommendation[], { onpost }: { onpost?: (path: string) => void }) {
  posted = [];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (path: string, init?: RequestInit) => {
      if (init?.method === 'POST') {
        posted.push({ path, body: JSON.parse(String(init.body)) });
        onpost?.(path);
        return new Response(JSON.stringify({}), { status: 200 });
      }
      const asked = new URL(path, 'http://test').searchParams;
      const left = visible();
      const limit = Number(asked.get('limit') ?? 25);
      let start = Number(asked.get('offset') ?? 0);
      if (asked.has('after')) {
        const after = Number(asked.get('after'));
        const next = left.findIndex((item) => item.rank > after);
        start = next < 0 ? left.length : next;
      } else if (asked.has('before')) {
        start = left.filter((item) => item.rank < Number(asked.get('before'))).length - limit;
      }
      if (start >= left.length) start = Math.floor((left.length - 1) / limit) * limit;
      if (start < 0 || left.length === 0) start = 0;
      return new Response(
        JSON.stringify(
          answer({ items: left.slice(start, start + limit), total: left.length, offset: start })
        )
      );
    })
  );
}

// Saying no is one decision at three widths, so it is two presses: the trigger
// that carries the decision, then the scope it is meant at. The scope is found
// by its label, which is the only part of an item a reader chooses by.
async function sayingNo(scope: RegExp) {
  await fireEvent.click(screen.getByRole('button', { name: 'Not interested' }));
  await fireEvent.click(await screen.findByRole('menuitem', { name: scope }));
}

function opened() {
  // The client is per test: a cache shared between them would answer the second
  // one from the first one's list without asking anything.
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(Recommendations, undefined, {
    wrapper: QueryClientProvider,
    wrapperProps: { client }
  });
}

beforeAll(setup);

beforeEach(() => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
  vi.setSystemTime(clock);
  localStorage.clear();
  watched.clear();
  vi.stubGlobal('IntersectionObserver', FakeIntersectionObserver);
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  cleanup();
});

describe('Recommendations', () => {
  it('reserves the read timestamp slot while loading', () => {
    vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>(() => {})));
    opened();

    expect(screen.getByText('read from ListenBrainz …')).toBeTruthy();
  });

  it('fills the viewport with recommendation skeleton rows while loading', () => {
    answering(answer());
    opened();

    const loading = screen.getByLabelText('Loading recommendations');
    expect(loading.getAttribute('aria-busy')).toBe('true');
    expect(loading.querySelectorAll('[aria-hidden="true"]')).toHaveLength(40);
    expect(screen.getByText('reading your suggestions…')).toBeTruthy();
  });

  it('lists what the source suggested', async () => {
    answering(answer({ items: [suggestion(), suggestion({
      recordingId: 'e2000000-0000-4000-8000-000000000002',
      recordingTitle: 'Cold Sun',
      artistName: 'Cassiopeia Fields',
      releaseTitle: 'Field Notes',
      rank: 2
    })] }));
    opened();

    expect(await screen.findByText('Weather Report')).toBeTruthy();
    expect(await screen.findByText('Cold Sun')).toBeTruthy();
  });

  it('shows the feedback reason beside the source reason', async () => {
    answering(
      answer({
        items: [suggestion({ reasonCodes: ['cf_raw', 'feedback_more_like_this'] })]
      })
    );
    opened();

    expect(await screen.findByText(/you asked for more like this/)).toBeTruthy();
  });

  it('sends a More like this signal for the selected suggestion', async () => {
    answering(answer());
    opened();
    await screen.findByText('Weather Report');

    await fireEvent.click(screen.getByRole('button', { name: 'More like this' }));

    await vi.waitFor(() =>
      expect(posted).toContainEqual({
        path: '/api/v1/recommendations/feedback',
        body: {
          recordingId: 'e2000000-0000-4000-8000-000000000001',
          signal: 'more_like_this',
          source: 'listenbrainz'
        }
      })
    );
  });

  it('sends a Less like this signal for the selected suggestion', async () => {
    answering(answer());
    opened();
    await screen.findByText('Weather Report');

    await fireEvent.click(screen.getByRole('button', { name: 'Less like this' }));

    await vi.waitFor(() =>
      expect(posted).toContainEqual({
        path: '/api/v1/recommendations/feedback',
        body: {
          recordingId: 'e2000000-0000-4000-8000-000000000001',
          signal: 'less_like_this',
          source: 'listenbrainz'
        }
      })
    );
  });

  it('confirms and clears all recommendation feedback', async () => {
    answering(answer());
    opened();

    await fireEvent.click(screen.getByRole('button', { name: 'Clear feedback' }));
    expect(screen.getByText('Clear all ListenBrainz feedback?')).toBeTruthy();
    expect(screen.getByText(/The next sweep uses source ranking alone/)).toBeTruthy();

    await fireEvent.click(screen.getByRole('button', { name: 'Clear feedback' }));

    await vi.waitFor(() =>
      expect(posted).toContainEqual({
        path: '/api/v1/recommendations/feedback?source=listenbrainz',
        body: undefined
      })
    );
  });

  it('reads the ListenBrainz list until the reader switches', async () => {
    answering(answer());
    opened();
    await screen.findByText('Weather Report');

    expect(read.every((path) => new URL(path, 'http://test').searchParams.get('source') === 'listenbrainz')).toBe(true);
    expect(screen.getByRole('button', { name: 'ListenBrainz' }).getAttribute('aria-pressed')).toBe('true');
    expect(screen.getByRole('button', { name: 'Schall' }).getAttribute('aria-pressed')).toBe('false');
  });

  it('reads the Schall list once the reader switches to it', async () => {
    answering(answer());
    opened();
    await screen.findByText('Weather Report');

    await fireEvent.click(screen.getByRole('button', { name: 'Schall' }));

    await vi.waitFor(() =>
      expect(read.map((path) => new URL(path, 'http://test').searchParams.get('source'))).toContain('schall')
    );
    expect(screen.getByRole('button', { name: 'Schall' }).getAttribute('aria-pressed')).toBe('true');
    expect(await screen.findByText(/built from your listens/)).toBeTruthy();
  });

  it('starts the Schall list at its first page', async () => {
    answering(answer());
    opened();
    await screen.findByText('Weather Report');

    await fireEvent.click(screen.getByRole('button', { name: 'Schall' }));

    await vi.waitFor(() => {
      const schall = read
        .map((path) => new URL(path, 'http://test').searchParams)
        .filter((asked) => asked.get('source') === 'schall');
      expect(schall.length).toBeGreaterThan(0);
      expect(schall[0].get('offset')).toBe('0');
    });
  });

  it('does not show the ListenBrainz rows while the Schall list is on its way', async () => {
    answering(answer());
    const listenBrainz = globalThis.fetch;
    vi.stubGlobal(
      'fetch',
      vi.fn((path: string, init?: RequestInit) =>
        new URL(path, 'http://test').searchParams.get('source') === 'schall'
          ? new Promise<Response>(() => {})
          : listenBrainz(path, init)
      )
    );
    opened();
    await screen.findByText('Weather Report');

    await fireEvent.click(screen.getByRole('button', { name: 'Schall' }));

    await vi.waitFor(() => expect(screen.queryByText('Weather Report')).toBeNull());
  });

  it('sends feedback on a Schall suggestion as Schall feedback', async () => {
    answering(answer());
    opened();
    await screen.findByText('Weather Report');
    await fireEvent.click(screen.getByRole('button', { name: 'Schall' }));

    await fireEvent.click(await screen.findByRole('button', { name: 'More like this' }));

    await vi.waitFor(() =>
      expect(posted).toContainEqual({
        path: '/api/v1/recommendations/feedback',
        body: {
          recordingId: 'e2000000-0000-4000-8000-000000000001',
          signal: 'more_like_this',
          source: 'schall'
        }
      })
    );
  });

  it('names Schall when it asks before clearing Schall feedback', async () => {
    answering(answer());
    opened();
    await screen.findByText('Weather Report');
    await fireEvent.click(screen.getByRole('button', { name: 'Schall' }));

    await fireEvent.click(screen.getByRole('button', { name: 'Clear feedback' }));

    const confirmation = screen.getByRole('group', { name: 'Clear feedback confirmation' });
    expect(confirmation.textContent).toContain('Clear all Schall feedback?');
    expect(confirmation.textContent).toContain('pressed on the Schall list');
  });

  it('clears only the Schall feedback from the Schall list', async () => {
    answering(answer());
    opened();
    await screen.findByText('Weather Report');
    await fireEvent.click(screen.getByRole('button', { name: 'Schall' }));

    await fireEvent.click(screen.getByRole('button', { name: 'Clear feedback' }));
    await fireEvent.click(screen.getByRole('button', { name: 'Clear feedback' }));

    await vi.waitFor(() =>
      expect(posted).toContainEqual({
        path: '/api/v1/recommendations/feedback?source=schall',
        body: undefined
      })
    );
  });

  it('writes out why the own engine suggested a recording', async () => {
    answering(answer({ items: [suggestion({ reasonCodes: ['co_listened', 'listened'] })] }));
    opened();

    expect(
      await screen.findByText("played beside music you have · you played it, you don't have it")
    ).toBeTruthy();
  });

  it('says no Schall sweep has run when the Schall list holds nothing', async () => {
    answering(
      answer({
        items: [],
        snapshot: { source: 'schall', fetched: false, status: '', detail: '', fetchedAt: null }
      })
    );
    opened();
    await screen.findByText('No listening history read yet');

    await fireEvent.click(screen.getByRole('button', { name: 'Schall' }));

    expect(await screen.findByText('No sweep has run yet')).toBeTruthy();
  });

  it('does not send a Schall reader to Settings when the list stopped refreshing', async () => {
    answering(
      answer({
        refresh: {
          attempted: true,
          succeeded: false,
          lastAttemptAt: new Date(clock - 5 * 60_000).toISOString(),
          nextAttemptAt: new Date(clock + 25 * 60_000).toISOString()
        }
      })
    );
    opened();
    await screen.findByText(/This list stopped refreshing/);

    await fireEvent.click(screen.getByRole('button', { name: 'Schall' }));

    // The line leaves while the Schall list loads and comes back with it.
    expect(await screen.findByText(/This list stopped refreshing/)).toBeTruthy();
    expect(screen.queryByRole('link', { name: 'Settings' })).toBeNull();
  });

  it('says a partial Schall list is still growing', async () => {
    answering(
      answer({
        snapshot: {
          source: 'schall',
          fetched: true,
          status: 'partial',
          detail: '25 of 340 recordings looked up in MusicBrainz',
          fetchedAt: new Date(clock - 5 * 60_000).toISOString()
        }
      })
    );
    opened();
    await screen.findByText(/This list is shorter than usual/);

    await fireEvent.click(screen.getByRole('button', { name: 'Schall' }));

    expect(await screen.findByText(/This list is still growing/)).toBeTruthy();
    expect(screen.queryByText(/This list is shorter than usual/)).toBeNull();
  });

  it('says how many suggestions were held back and which rule held each one', async () => {
    answering(
      answer({
        hidden: {
          total: 5,
          owned: 2,
          requested: 1,
          notWanted: 1,
          followedArtist: 0,
          followedLabel: 0,
          dismissed: 1,
          impressionFatigue: 0,
          unkept: 0
        }
      })
    );
    opened();

    expect(
      await screen.findByText(
        '5 more suggestions are not shown: 2 already in your library, 1 already requested, ' +
          '1 you stopped looking for, 1 you said no to.'
      )
    ).toBeTruthy();
  });

  it('counts a showing for the suggestions it put on screen', async () => {
    answering(answer());
    opened();
    await screen.findByText('Weather Report');

    await looking('Weather Report');

    await vi.waitFor(() =>
      expect(posted).toContainEqual({
        path: '/api/v1/recommendations/impressions',
        body: { recordingIds: ['e2000000-0000-4000-8000-000000000001'] }
      })
    );
  });

  // A page is twenty-five rows and a window fits about thirteen. Counting the
  // rest held suggestions back for ninety days that nobody had scrolled to, and
  // then told the reader they had ignored them (issue #327).
  it('counts no showing for a suggestion that stayed below the fold', async () => {
    answering(
      answer({
        items: [
          suggestion(),
          suggestion({
            recordingId: 'e2000000-0000-4000-8000-000000000002',
            recordingTitle: 'Cold Sun',
            rank: 2
          })
        ]
      })
    );
    opened();
    await screen.findByText('Cold Sun');

    await looking('Weather Report');

    await vi.waitFor(() =>
      expect(posted).toContainEqual({
        path: '/api/v1/recommendations/impressions',
        body: { recordingIds: ['e2000000-0000-4000-8000-000000000001'] }
      })
    );
    expect(
      posted.flatMap(({ body }) => (body as { recordingIds: string[] }).recordingIds)
    ).not.toContain('e2000000-0000-4000-8000-000000000002');
  });

  // A row that goes past under a fast scroll was not read, and counting it
  // would hold it back for ninety days on the strength of a flick.
  it('counts no showing for a row that only went past', async () => {
    answering(answer());
    opened();
    const row = await screen.findByText('Weather Report');

    for (const [watching, tell] of [...watched]) {
      if (!watching.contains(row)) continue;
      tell([{ target: watching, isIntersecting: true } as IntersectionObserverEntry]);
      await vi.advanceTimersByTimeAsync(200);
      tell([{ target: watching, isIntersecting: false } as IntersectionObserverEntry]);
    }
    await vi.advanceTimersByTimeAsync(1500);

    expect(posted).toEqual([]);
  });

  it('turns Not interested into a permanent dismissal of that recording', async () => {
    answering(answer());
    opened();
    await screen.findByText('Weather Report');

    await sayingNo(/This recording/);

    await vi.waitFor(() =>
      expect(posted).toContainEqual({
        path: '/api/v1/recommendations/dismissals',
        body: {
          subject: 'recording',
          musicBrainzId: 'e2000000-0000-4000-8000-000000000001'
        }
      })
    );
  });

  // The store has held all three levels since it was designed and the
  // suppression rules read all three. Only the screen could say one of them,
  // so a reader who wanted nothing more from an artist had to answer for one
  // recording at a time while every sweep brought more (issues #318, #320).
  it('says no to the release the suggestion came from', async () => {
    answering(answer());
    opened();
    await screen.findByText('Weather Report');

    await sayingNo(/Anything from this release/);

    await vi.waitFor(() =>
      expect(posted).toContainEqual({
        path: '/api/v1/recommendations/dismissals',
        body: {
          subject: 'release_group',
          musicBrainzId: 'b2000000-0000-4000-8000-000000000001'
        }
      })
    );
  });

  it('says no to the artist behind the suggestion', async () => {
    answering(answer());
    opened();
    await screen.findByText('Weather Report');

    await sayingNo(/Anything by this artist/);

    await vi.waitFor(() =>
      expect(posted).toContainEqual({
        path: '/api/v1/recommendations/dismissals',
        body: {
          subject: 'artist',
          musicBrainzId: 'a2000000-0000-4000-8000-000000000001'
        }
      })
    );
  });

  // A credit can name several artists, and the row shows the whole credit. A
  // reader saying no to it has not been shown which of those names Schall would
  // act on, so it acts on all of them and the words say so.
  it('says no to every artist a shared credit names', async () => {
    answering(
      answer({
        items: [
          suggestion({
            artistIds: [
              'a2000000-0000-4000-8000-000000000001',
              'a2000000-0000-4000-8000-000000000002'
            ],
            artistName: 'Halyard & Marconi Union'
          })
        ]
      })
    );
    opened();
    await screen.findByText('Weather Report');

    await sayingNo(/Anything by these artists/);

    await vi.waitFor(() =>
      expect(
        posted.filter((write) => write.path === '/api/v1/recommendations/dismissals')
      ).toEqual([
        {
          path: '/api/v1/recommendations/dismissals',
          body: { subject: 'artist', musicBrainzId: 'a2000000-0000-4000-8000-000000000001' }
        },
        {
          path: '/api/v1/recommendations/dismissals',
          body: { subject: 'artist', musicBrainzId: 'a2000000-0000-4000-8000-000000000002' }
        }
      ])
    );
  });

  // The scopes name what they rule out, because a permanent decision taken from
  // a menu is taken away from the row that explains it.
  it('names the recording, the release and the artist it would rule out', async () => {
    answering(answer());
    opened();
    await screen.findByText('Weather Report');

    await fireEvent.click(screen.getByRole('button', { name: 'Not interested' }));

    const scopes = await screen.findAllByRole('menuitem');
    expect(scopes.map((scope) => scope.textContent?.replace(/\s+/g, ' ').trim())).toEqual([
      'This recording Weather Report',
      'Anything from this release Long Reach',
      'Anything by this artist Halyard'
    ]);
  });

  // A want records where it came from. Nothing in the acquisition loop branches
  // on the origin, but a want that cannot say who asked for it is one nobody
  // can answer for later.
  it('turns Want it into a want that says a suggestion asked for it', async () => {
    answering(answer());
    opened();
    await screen.findByText('Weather Report');

    await fireEvent.click(screen.getByRole('button', { name: 'Want it' }));

    await vi.waitFor(() =>
      expect(posted).toContainEqual({
        path: '/api/v1/acquisition-targets',
        body: {
          origin: 'recommendation',
          artist: 'Halyard',
          title: 'Weather Report',
          album: 'Long Reach',
          recordingId: 'e2000000-0000-4000-8000-000000000001',
          releaseGroupId: 'b2000000-0000-4000-8000-000000000001'
        }
      })
    );
  });

  // The row leaves the list on the next read, because a recording that is
  // wanted is one the second rule holds back. Without a sentence saying so, the
  // only answer to the press would be the music disappearing.
  it('says what became of the recording it wanted', async () => {
    answeringWith(answer(), { title: 'Weather Report', status: 'pending' });
    opened();
    await screen.findByText('Weather Report');

    await fireEvent.click(screen.getByRole('button', { name: 'Want it' }));

    expect(await screen.findByText(/Weather Report is wanted\./)).toBeTruthy();
  });

  // A want already holding a question is not one Schall is still looking for,
  // and telling the reader to wait for it would hide the fact that it is
  // waiting for them.
  it('sends the reader to Review for a want that is already asking them something', async () => {
    answeringWith(answer(), { title: 'Weather Report', status: 'awaiting_review' });
    opened();
    await screen.findByText('Weather Report');

    await fireEvent.click(screen.getByRole('button', { name: 'Want it' }));

    expect(await screen.findByText(/waiting for a decision from you on Review/)).toBeTruthy();
  });

  // Asking for a recording that is already wanted answers with the want that
  // exists, and one somebody stopped pursuing is such a want. Pressing here
  // does not take that decision back, so the reader is told where it was made
  // rather than left pressing a control that does nothing.
  it('says a recording somebody stopped pursuing was not asked for again', async () => {
    answeringWith(answer(), { title: 'Weather Report', status: 'not_wanted' });
    opened();
    await screen.findByText('Weather Report');

    await fireEvent.click(screen.getByRole('button', { name: 'Want it' }));

    expect(await screen.findByText(/was marked as not wanted earlier/)).toBeTruthy();
  });

  // An installation that has never connected an account has nothing to read,
  // and the answer to that is a setting rather than a retry.
  it('points an installation with no stored list at its settings', async () => {
    answering(
      answer({
        items: [],
        total: 0,
        snapshot: {
          source: 'listenbrainz',
          fetched: false,
          status: '',
          detail: '',
          fetchedAt: null
        }
      })
    );
    opened();

    expect(await screen.findByText('No listening history read yet')).toBeTruthy();
  });

  // A list emptied by the five rules is not the same as a source with nothing
  // to say, and saying "nothing suggested" for it would be wrong.
  it('separates a list the rules emptied from a source with nothing to say', async () => {
    answering(
      answer({
        items: [],
        total: 0,
        hidden: {
          total: 3,
          owned: 3,
          requested: 0,
          notWanted: 0,
          followedArtist: 0,
          followedLabel: 0,
          dismissed: 0,
          impressionFatigue: 0,
          unkept: 0
        }
      })
    );
    opened();

    expect(await screen.findByText('Every suggestion is held back')).toBeTruthy();
  });

  it('says the source had nothing to suggest when nothing was held back either', async () => {
    answering(answer({ items: [], total: 0 }));
    opened();

    expect(await screen.findByText('Nothing suggested yet')).toBeTruthy();
  });

  // A background sweep refreshes this list once a day and nobody presses
  // anything. When it stops being able to, the last complete list stays on
  // screen and gets older, which reads as a quiet week rather than as a fault.
  it('says the list is not being refreshed when the last sweep left it alone', async () => {
    answering(
      answer({
        refresh: {
          attempted: true,
          succeeded: false,
          lastAttemptAt: new Date(clock - 5 * 60_000).toISOString(),
          nextAttemptAt: new Date(clock + 25 * 60_000).toISOString()
        }
      })
    );
    opened();

    expect(
      await screen.findByText(/This list stopped refreshing, and Schall tries again in 25m\./)
    ).toBeTruthy();
    // The line names a control the reader can see, and that control is a link
    // rather than a sentence about one.
    expect(screen.getByRole('link', { name: 'Settings' })).toBeTruthy();
  });

  // The line is a fault, so it stays off a list that is being kept up to date.
  it('says nothing about refreshing while the sweep is keeping up', async () => {
    answering(
      answer({
        refresh: {
          attempted: true,
          succeeded: true,
          lastAttemptAt: new Date(clock - 2 * 60 * 60_000).toISOString(),
          nextAttemptAt: new Date(clock + 22 * 60 * 60_000).toISOString()
        }
      })
    );
    opened();
    await screen.findByText('Weather Report');

    expect(screen.queryByText(/stopped refreshing/)).toBeNull();
  });

  // A restored or seeded database holds a list with no sweep behind it. That is
  // ordinary, and calling it stale would put a fault on a screen that has none.
  it('leaves a stored list with no sweep behind it alone', async () => {
    answering(answer());
    opened();
    await screen.findByText('Weather Report');

    expect(screen.queryByText(/stopped refreshing/)).toBeNull();
  });

  // A short list and an old list are two different faults. This is the short
  // one: the read arrived on time and got part of the answer.
  it('says the list is short when the sweep got only part of the answer', async () => {
    answering(
      answer({
        snapshot: {
          source: 'listenbrainz',
          fetched: true,
          status: 'partial',
          detail: 'similarity fan-out stopped at seed 9f5b1c8a-0000-4000-8000-000000000001: 503',
          fetchedAt: new Date(clock - 5 * 60_000).toISOString()
        }
      })
    );
    opened();

    expect(
      await screen.findByText(
        /This list is shorter than usual\. The read that built it did not get the whole answer from ListenBrainz/
      )
    ).toBeTruthy();
  });

  // A list can be both old and short, and then both lines are on screen. The
  // stale line owns when the next read is, so the short line must not promise
  // one — "the next read tries for all of it" under "no next read is waiting"
  // is the screen arguing with itself.
  it('leaves the next read to the stale line when the list is old and short', async () => {
    answering(
      answer({
        snapshot: {
          source: 'listenbrainz',
          fetched: true,
          status: 'partial',
          detail: 'top-recording seeds unavailable: 503',
          fetchedAt: new Date(clock - 6 * 24 * 60 * 60_000).toISOString()
        },
        refresh: {
          attempted: true,
          succeeded: false,
          lastAttemptAt: new Date(clock - 5 * 60_000).toISOString(),
          nextAttemptAt: null
        }
      })
    );
    opened();

    expect(await screen.findByText(/no next read is waiting/)).toBeTruthy();
    expect(
      await screen.findByText(
        /This list is shorter than usual\. The read that built it did not get the whole answer from ListenBrainz\./
      )
    ).toBeTruthy();
    expect(screen.queryByText(/tries for all of it/)).toBeNull();
  });

  // The sweep's own sentence about what was missing names a MusicBrainz
  // identifier or repeats a provider's error. It is a log line, and a reader
  // can do nothing with it.
  it("keeps the sweep's own words about what was missing off the screen", async () => {
    answering(
      answer({
        snapshot: {
          source: 'listenbrainz',
          fetched: true,
          status: 'partial',
          detail: 'top-recording seeds unavailable: 503 Service Unavailable',
          fetchedAt: new Date(clock - 5 * 60_000).toISOString()
        }
      })
    );
    opened();
    await screen.findByText('Weather Report');

    expect(screen.queryByText(/503 Service Unavailable/)).toBeNull();
  });

  it('says nothing about a short list when the sweep walked the whole answer', async () => {
    answering(answer());
    opened();
    await screen.findByText('Weather Report');

    expect(screen.queryByText(/shorter than usual/)).toBeNull();
  });

  // A sweep stores a list only when it walks the whole answer, so a first sweep
  // that stopped part way leaves no list at all. "ListenBrainz had nothing to
  // suggest" is a third thing, and it is not what happened.
  it('separates a first sweep that did not finish from a source with nothing to say', async () => {
    answering(
      answer({
        items: [],
        total: 0,
        snapshot: {
          source: 'listenbrainz',
          fetched: false,
          status: '',
          detail: '',
          fetchedAt: null
        },
        refresh: {
          attempted: true,
          succeeded: false,
          lastAttemptAt: new Date(clock - 10 * 60_000).toISOString(),
          nextAttemptAt: new Date(clock + 24 * 60 * 60_000).toISOString()
        }
      })
    );
    opened();

    expect(await screen.findByText('The first read did not finish')).toBeTruthy();
  });

  // Dismissing the last suggestion on the last page empties that page. The
  // reader is put back on a page that still has rows, rather than left looking
  // at an empty panel with no control on it.
  it('steps back a page when the last suggestion on it is dismissed', async () => {
    const many = suggestions(26);
    let dismissed = false;
    serving(() => (dismissed ? many.slice(0, 25) : many), {
      onpost: (path) => {
        if (path.endsWith('/dismissals')) dismissed = true;
      }
    });
    opened();
    await screen.findByText('Suggestion 1');

    await fireEvent.click(screen.getByRole('button', { name: 'Next' }));
    await screen.findByText('Suggestion 26');
    await sayingNo(/This recording/);

    expect(await screen.findByText('Suggestion 1')).toBeTruthy();
  });

  // The rows on page one gather their showings together, so they reach the
  // third one together and the fifth rule takes all of them at once. Everything
  // after them then moves up by a page. Next has to mean "the ones after the
  // ones I just saw", or twenty-five suggestions go past unseen (issue #329).
  it('shows the suggestions that followed the page it just showed', async () => {
    const many = suggestions(60);
    let heldBack = false;
    serving(() => (heldBack ? many.slice(25) : many), {
      onpost: (path) => {
        if (path.endsWith('/impressions')) heldBack = true;
      }
    });
    opened();
    await screen.findByText('Suggestion 1');
    await looking('Suggestion 1');
    await vi.waitFor(() => expect(heldBack).toBe(true));

    await fireEvent.click(screen.getByRole('button', { name: 'Next' }));

    expect(await screen.findByText('Suggestion 26')).toBeTruthy();
  });
});
