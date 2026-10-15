import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, setup, waitFor } from '@testing-library/svelte';
import { QueryClient, QueryClientProvider } from '@tanstack/svelte-query';
import type { AcquiredCopy, AcquisitionTarget, ReviewItem } from '$lib/api';

// Two ways to decide faster: a whole album from one batch, and stepping
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

/** A copy whose ISRC agreed, which an album batch checks before the reader. */
function proven(id: string): AcquiredCopy {
  const held = copy(id);
  held.evidence!.agrees = ['ISRC'];
  return held;
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
      want('a', 'Evensong', 'Nine Lanterns', { copies: [proven('copy-a')] }),
      want('b', 'Ember', 'Nine Lanterns', { copies: [proven('copy-b')] }),
      want('c', 'Ashfall', 'Nine Lanterns', { copies: [proven('copy-c')] }),
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

  it('leaves a row unchecked when its evidence names no proof, and says what it has', async () => {
    wants = [
      want('a', 'Evensong', 'Nine Lanterns', { copies: [proven('copy-a')] }),
      want('b', 'Ember', 'Nine Lanterns')
    ];
    await open('Evensong');

    await fireEvent.click(screen.getByRole('button', { name: 'Group by album' }));
    await fireEvent.click(screen.getByRole('button', { name: 'Nine Lanterns by Kestrel Grove, 2' }));

    const proof = (await screen.findByRole('checkbox', { name: 'Include Evensong' })) as HTMLInputElement;
    const none = screen.getByRole('checkbox', { name: 'Include Ember' }) as HTMLInputElement;
    expect([proof.checked, none.checked]).toEqual([true, false]);
    expect(screen.getByText('ISRC')).toBeTruthy();
    expect(screen.getAllByLabelText('agrees')).toHaveLength(1);
    expect(screen.getByText('Audio unidentified')).toBeTruthy();
    expect(screen.getByRole('button', { name: /^Accept checked/ }).textContent).toContain('1');
  });

  it('leaves a row unchecked when a field differs, and names the field', async () => {
    const differs = proven('copy-a');
    differs.evidence!.differs = ['duration (9s out)'];
    wants = [want('a', 'Evensong', 'Nine Lanterns', { copies: [differs] })];
    await open('Evensong');

    await fireEvent.click(screen.getByRole('button', { name: 'Group by album' }));
    await fireEvent.click(screen.getByRole('button', { name: 'Nine Lanterns by Kestrel Grove, 1' }));

    const row = (await screen.findByRole('checkbox', { name: 'Include Evensong' })) as HTMLInputElement;
    expect(row.checked).toBe(false);
    expect(screen.getByText('duration (9s out)')).toBeTruthy();
    expect(screen.getByLabelText('differs')).toBeTruthy();
  });
});

describe('the keys', () => {
  it('accepts the chosen copy on Y', async () => {
    wants = [want('a', 'Evensong', 'Nine Lanterns')];
    await open('Evensong');

    await fireEvent.keyDown(window, { key: 'y' });

    await waitFor(() => expect(writes().length).toBe(1));
    expect(writes()[0]).toEqual({ method: 'POST', url: '/api/v1/review-queue/copies/copy-a/accept' });
  });

  it('refuses every copy on N', async () => {
    wants = [want('a', 'Evensong', 'Nine Lanterns')];
    await open('Evensong');

    await fireEvent.keyDown(window, { key: 'n' });

    await waitFor(() => expect(writes().length).toBe(1));
    expect(writes()[0]).toEqual({ method: 'POST', url: '/api/v1/acquisition-targets/a/none-of-these' });
  });

  it('sends nothing for Y or N while the volume has focus', async () => {
    wants = [want('a', 'Evensong', 'Nine Lanterns')];
    await open('Evensong');

    const volume = screen.getByRole('slider', { name: 'Volume' });
    volume.focus();
    await fireEvent.keyDown(volume, { key: 'y' });
    await fireEvent.keyDown(volume, { key: 'n' });
    await new Promise((resolve) => setTimeout(resolve, 20));

    expect(writes()).toEqual([]);
  });

  it('plays nothing by itself when the page opens', async () => {
    wants = [want('a', 'Evensong', 'Nine Lanterns')];
    await open('Evensong');
    const play = HTMLMediaElement.prototype.play as ReturnType<typeof vi.fn>;
    play.mockClear();
    await fireEvent(document.querySelector('audio')!, new Event('loadedmetadata'));

    expect(play).not.toHaveBeenCalled();
  });

  it('plays the next question by itself once the reader has played one', async () => {
    wants = [want('a', 'Evensong', 'Nine Lanterns'), want('b', 'Watchfire', 'Nine Lanterns')];
    await open('Evensong');

    await fireEvent.keyDown(window, { key: ' ' });
    await fireEvent.keyDown(window, { key: 'j' });
    await screen.findByRole('heading', { level: 2, name: 'Watchfire' });
    const play = HTMLMediaElement.prototype.play as ReturnType<typeof vi.fn>;
    play.mockClear();
    await fireEvent(document.querySelector('audio')!, new Event('loadedmetadata'));

    expect(play).toHaveBeenCalled();
  });

  it('pauses a playing copy on Space', async () => {
    wants = [want('a', 'Evensong', 'Nine Lanterns')];
    await open('Evensong');

    const pause = HTMLMediaElement.prototype.pause as ReturnType<typeof vi.fn>;
    const play = HTMLMediaElement.prototype.play as ReturnType<typeof vi.fn>;
    pause.mockClear();
    play.mockClear();
    Object.defineProperty(document.querySelector('audio')!, 'paused', { get: () => false });
    await fireEvent.keyDown(window, { key: ' ' });

    expect(pause).toHaveBeenCalled();
    expect(play).not.toHaveBeenCalled();
  });

  it('plays the copy on Space', async () => {
    wants = [want('a', 'Evensong', 'Nine Lanterns')];
    await open('Evensong');

    const play = HTMLMediaElement.prototype.play as ReturnType<typeof vi.fn>;
    play.mockClear();
    await fireEvent.keyDown(window, { key: ' ' });

    expect(play).toHaveBeenCalled();
    expect(writes()).toEqual([]);
  });
});
