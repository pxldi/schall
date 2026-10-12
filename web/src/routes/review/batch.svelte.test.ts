import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, setup, waitFor } from '@testing-library/svelte';
import { QueryClient, QueryClientProvider } from '@tanstack/svelte-query';
import type { AcquiredCopy, AcquisitionTarget, ReviewItem } from '$lib/api';

// Two ways to decide faster: a whole album from one batch, and listening
// through the queue with Y and N. Each still sends the requests the single
// view sends, one per answer, and these tests hold them to that.

vi.mock('$app/state', () => ({
  page: { params: {}, url: new URL('http://localhost/review') }
}));

vi.mock('$app/navigation', () => ({
  afterNavigate: () => {},
  goto: vi.fn(),
  replaceState: vi.fn()
}));

const { default: Review } = await import('./+page.svelte');

function target(id: string, title: string, album: string): AcquisitionTarget {
  return {
    id,
    origin: 'playlist',
    artist: 'Kestrel Grove',
    title,
    album,
    durationMs: 214_000,
    isrc: null,
    recordingId: null,
    status: 'awaiting_review',
    summary: 'Waiting for an ear.',
    attempts: 1,
    anchorAttempts: 0,
    nextSearchAt: null,
    searchAttempts: 0
  };
}

function copy(id: string): AcquiredCopy {
  return {
    id,
    provider: 'slskd',
    username: 'harbourmaster',
    path: `/incoming/${id}.flac`,
    name: `${id}.flac`,
    verdict: 'held',
    decidedBy: 'schall',
    summary: 'Nobody could identify the audio.',
    decidedAt: '2026-08-01T10:00:00Z',
    evidence: {
      name: `${id}.flac`,
      observed: { durationMs: 214_000 },
      agrees: [],
      differs: [],
      problems: []
    }
  };
}

function want(id: string, title: string, album: string, overrides: Partial<ReviewItem> = {}): ReviewItem {
  return {
    kind: 'copies',
    target: target(id, title, album),
    copies: [copy(`copy-${id}`)],
    candidates: [],
    ruledOut: 0,
    ...overrides
  };
}

let sent: { method: string; url: string }[] = [];
let wants: ReviewItem[] = [];

beforeAll(setup);

beforeEach(() => {
  sent = [];
  wants = [];
  try {
    localStorage.clear();
  } catch {
    // jsdom always has storage; the page copes without it.
  }
  Object.assign(HTMLMediaElement.prototype, {
    play: vi.fn().mockResolvedValue(undefined),
    pause: vi.fn(),
    load: vi.fn()
  });
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      sent.push({ method: (init?.method ?? 'GET').toUpperCase(), url });
      if (url.includes('/waveform')) return new Response(null, { status: 404 });
      const body = url.startsWith('/api/v1/review-queue')
        ? { items: wants, total: wants.length, resolutions: 0, limit: 100, offset: 0 }
        : url.startsWith('/api/v1/downloads?')
          ? { items: [], total: 0, limit: 100, offset: 0 }
          : {};
      return new Response(JSON.stringify(body), {
        status: 200,
        headers: { 'Content-Type': 'application/json' }
      });
    })
  );
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

function writes() {
  return sent.filter((request) => request.method !== 'GET');
}

async function open(text: string) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(Review, undefined, { wrapper: QueryClientProvider, wrapperProps: { client } });
  await screen.findAllByText(text);
}

describe('deciding an album at once', () => {
  it('groups the rail by album with a count on each album', async () => {
    wants = [
      want('a', 'Evensong', 'Nine Lanterns'),
      want('b', 'Watchfire', 'Low Tide'),
      want('c', 'Ember', 'Nine Lanterns')
    ];
    await open('Evensong');

    await fireEvent.click(screen.getByRole('button', { name: 'Group by album' }));

    const albums = screen
      .getAllByRole('button')
      .map((button) => button.getAttribute('aria-label') ?? '')
      .filter((label) => label.includes(' by Kestrel Grove'));
    expect(albums).toEqual(['Nine Lanterns by Kestrel Grove, 2', 'Low Tide by Kestrel Grove, 1']);
  });

  it('accepts each checked row once and nothing for a row taken out', async () => {
    wants = [
      want('a', 'Evensong', 'Nine Lanterns'),
      want('b', 'Ember', 'Nine Lanterns'),
      want('c', 'Ashfall', 'Nine Lanterns'),
      want('d', 'Undertow', 'Nine Lanterns', { waitingFor: 'musicbrainz' })
    ];
    await open('Evensong');

    await fireEvent.click(screen.getByRole('button', { name: 'Group by album' }));
    await fireEvent.click(screen.getByRole('button', { name: 'Nine Lanterns by Kestrel Grove, 4' }));

    expect(await screen.findByText('1 to decide one by one')).toBeTruthy();
    expect(screen.queryByRole('checkbox', { name: 'Include Undertow' })).toBeNull();

    await fireEvent.click(screen.getByRole('checkbox', { name: 'Include Ember' }));
    await fireEvent.click(screen.getByRole('button', { name: /^Accept checked/ }));

    await waitFor(() => expect(screen.getByRole('status').textContent).toContain('Accepted 2.'));
    expect(writes()).toEqual([
      { method: 'POST', url: '/api/v1/review-queue/copies/copy-a/accept' },
      { method: 'POST', url: '/api/v1/review-queue/copies/copy-c/accept' }
    ]);
  });
});

describe('listening mode', () => {
  it('accepts the chosen copy on Y', async () => {
    wants = [want('a', 'Evensong', 'Nine Lanterns')];
    await open('Evensong');

    await fireEvent.click(screen.getByRole('button', { name: 'Listening mode' }));
    await fireEvent.keyDown(window, { key: 'y' });

    await waitFor(() => expect(writes().length).toBe(1));
    expect(writes()[0]).toEqual({ method: 'POST', url: '/api/v1/review-queue/copies/copy-a/accept' });
  });

  it('refuses every copy on N', async () => {
    wants = [want('a', 'Evensong', 'Nine Lanterns')];
    await open('Evensong');

    await fireEvent.click(screen.getByRole('button', { name: 'Listening mode' }));
    await fireEvent.keyDown(window, { key: 'n' });

    await waitFor(() => expect(writes().length).toBe(1));
    expect(writes()[0]).toEqual({ method: 'POST', url: '/api/v1/acquisition-targets/a/none-of-these' });
  });

  it('sends nothing for Y or N outside listening mode', async () => {
    wants = [want('a', 'Evensong', 'Nine Lanterns')];
    await open('Evensong');

    await fireEvent.keyDown(window, { key: 'y' });
    await fireEvent.keyDown(window, { key: 'n' });
    await new Promise((resolve) => setTimeout(resolve, 20));

    expect(writes()).toEqual([]);
  });

  it('plays the copy on Space', async () => {
    wants = [want('a', 'Evensong', 'Nine Lanterns')];
    await open('Evensong');

    await fireEvent.click(screen.getByRole('button', { name: 'Listening mode' }));
    const play = HTMLMediaElement.prototype.play as ReturnType<typeof vi.fn>;
    play.mockClear();
    await fireEvent.keyDown(window, { key: ' ' });

    expect(play).toHaveBeenCalled();
    expect(writes()).toEqual([]);
  });
});
