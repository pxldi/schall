import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, setup, waitFor } from '@testing-library/svelte';
import { QueryClient, QueryClientProvider } from '@tanstack/svelte-query';
import type { DuplicateCopy, DuplicateRecording, RemovedFileStillOnDisc } from '$lib/api';

// The duplicates view. Most of what it shows is asserted through the dialog it
// opens; what is asserted here is the one thing nothing else on the screen
// would ever mention again — a file the library has let go of and the disc has
// kept — and the single press that is the only thing to do about it.

const { default: DuplicateRecordings } = await import(
  '$lib/components/DuplicateRecordings.svelte'
);

beforeAll(setup);

beforeAll(() => {
  if (!Element.prototype.animate) {
    Element.prototype.animate = function () {
      const animation = {
        onfinish: null as (() => void) | null,
        currentTime: 0,
        playbackRate: 1,
        startTime: 0,
        effect: { getComputedTiming: () => ({ duration: 0 }) },
        play() {},
        pause() {},
        cancel() {},
        finish() {
          this.onfinish?.();
        }
      };
      setTimeout(() => animation.onfinish?.(), 0);
      return animation as unknown as Animation;
    };
  }
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

/** Every address the view asked for, in order. */
let asked: string[] = [];

function copy(overrides: Partial<DuplicateCopy> & { id: string; path: string }): DuplicateCopy {
  return { sizeBytes: 1_000_000, reason: 'identified as this recording', ...overrides };
}

function recording(): DuplicateRecording {
  return {
    recordingId: 'recording-1',
    title: 'Teardrop',
    artist: 'Massive Attack',
    copies: [
      copy({ id: 'keeper', path: '/music/Mezzanine/06 Teardrop.flac' }),
      copy({ id: 'spare', path: '/music/singles/Teardrop.mp3' })
    ],
    verdict: '',
    decided: false
  };
}

function answering(stillOnDisc: RemovedFileStillOnDisc[], recordings: DuplicateRecording[] = []) {
  asked = [];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), 'http://localhost');
      asked.push(`${init?.method ?? 'GET'} ${url.pathname}${url.search}`);
      if (url.pathname === '/api/v1/library/removals/retry') {
        return new Response(JSON.stringify({ deleted: 1 }), { status: 200 });
      }
      return new Response(JSON.stringify({ recordings, total: recordings.length, stillOnDisc }), {
        status: 200
      });
    })
  );
}

/** A server that reports more groups than fit on one page. `total` is the
 * count of recording groups, the same figure whatever offset is asked for —
 * it is the page of copies that changes, not how many groups there are. */
function paged(total: number, recordings: DuplicateRecording[] = []) {
  asked = [];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), 'http://localhost');
      asked.push(`${init?.method ?? 'GET'} ${url.pathname}${url.search}`);
      if (url.pathname === '/api/v1/library/removals/retry') {
        return new Response(JSON.stringify({ deleted: 1 }), { status: 200 });
      }
      if (url.pathname.endsWith('/keep')) {
        return new Response(null, { status: 204 });
      }
      return new Response(JSON.stringify({ recordings, total, stillOnDisc: [] }), {
        status: 200
      });
    })
  );
}

function opened() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(DuplicateRecordings, undefined, {
    wrapper: QueryClientProvider,
    wrapperProps: { client }
  });
}

describe('DuplicateRecordings, files the disc kept', () => {
  it('names each one, with the reason it is still there', async () => {
    answering([
      {
        path: '/music/singles/Teardrop.mp3',
        reason: 'permission denied',
        removedAt: '2026-08-27T10:00:00Z'
      }
    ]);
    opened();

    expect(await screen.findByText('/music/singles/Teardrop.mp3')).toBeTruthy();
    expect(screen.getByText('permission denied')).toBeTruthy();
    expect(
      screen.getByText(/1 file was removed from the library and could not be deleted/)
    ).toBeTruthy();
  });

  it('says nothing at all when every removal reached the disc', async () => {
    answering([], [recording()]);
    opened();

    expect(await screen.findByText('/music/singles/Teardrop.mp3')).toBeTruthy();
    expect(screen.queryByText(/could not be deleted/)).toBeNull();
    expect(screen.queryByRole('button', { name: 'Try again' })).toBeNull();
  });

  it('offers one press, which asks the server to try again', async () => {
    answering([{ path: '/music/singles/Teardrop.mp3', removedAt: '2026-08-27T10:00:00Z' }]);
    opened();

    await fireEvent.click(await screen.findByRole('button', { name: 'Try again' }));

    await waitFor(() => expect(asked).toContain('POST /api/v1/library/removals/retry'));
  });
});

describe('DuplicateRecordings, paging groups instead of holding every one at once', () => {
  it('asks for the first page of each half on load', async () => {
    paged(1, [recording()]);
    opened();

    await screen.findByText('Teardrop · Massive Attack');

    expect(asked).toContain('GET /api/v1/library/duplicates?limit=25');
    expect(asked).toContain('GET /api/v1/library/duplicates?kept=true&limit=25');
  });

  it('sends the new offset when the pager moves to the next page', async () => {
    paged(30, [recording()]);
    opened();

    await fireEvent.click(await screen.findByRole('button', { name: 'Next' }));

    await waitFor(() =>
      expect(asked).toContain('GET /api/v1/library/duplicates?limit=25&offset=25')
    );
  });

  it('refetches the page it is on after a mutation', async () => {
    paged(30, [recording()]);
    opened();

    await fireEvent.click(await screen.findByRole('button', { name: 'Next' }));
    // Wait for the second page's own row, not just the request that asked
    // for it — the request lands in `asked` before the response is read.
    await screen.findByRole('button', { name: 'Keep both' });
    asked = [];

    await fireEvent.click(screen.getByRole('button', { name: 'Keep both' }));

    await waitFor(() =>
      expect(asked).toContain('GET /api/v1/library/duplicates?limit=25&offset=25')
    );
  });

  it('retreats off a page a mutation emptied, instead of stranding the reader', async () => {
    asked = [];
    // Twenty-six groups: one on the second page. Keeping it drops the count
    // to twenty-five, and that page has nothing left on it.
    let total = 26;
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = new URL(String(input), 'http://localhost');
        asked.push(`${init?.method ?? 'GET'} ${url.pathname}${url.search}`);
        if (url.pathname.endsWith('/keep')) {
          total = 25;
          return new Response(null, { status: 204 });
        }
        return new Response(
          JSON.stringify({ recordings: [recording()], total, stillOnDisc: [] }),
          { status: 200 }
        );
      })
    );
    opened();

    await fireEvent.click(await screen.findByRole('button', { name: 'Next' }));
    // Wait for the second page's own row, not just the request that asked
    // for it — the request lands in `asked` before the response is read.
    await screen.findByRole('button', { name: 'Keep both' });

    await fireEvent.click(screen.getByRole('button', { name: 'Keep both' }));

    // The group that stood on page two is gone, so the count drops to
    // twenty-five — one page's worth. The pager (and its Next button) has
    // nothing left to page to, and the group that was there is still visible
    // rather than the reader being left looking at an empty page two.
    await waitFor(() => expect(screen.queryByRole('button', { name: 'Next' })).toBeNull());
    expect(screen.getByRole('button', { name: 'Keep both' })).toBeTruthy();
  });
});

// The rows are the comparison: every copy offers Play from the moment its
// group is on screen, with no separate view to open first.
describe('DuplicateRecordings, playing a copy', () => {
  beforeEach(() => {
    // jsdom has no real media pipeline, so `hear()`'s `audio.play().catch(...)`
    // needs a real promise to chain onto instead of jsdom's unimplemented
    // default.
    window.HTMLMediaElement.prototype.play = vi.fn().mockResolvedValue(undefined);
    window.HTMLMediaElement.prototype.pause = vi.fn();
  });

  it('offers Play on every copy without opening a separate view first', async () => {
    answering([], [recording()]);
    opened();
    await screen.findByText('Teardrop · Massive Attack');

    expect(screen.getAllByRole('button', { name: 'Play' })).toHaveLength(2);
  });

  it('turns Play into Stop for the copy that is sounding', async () => {
    answering([], [recording()]);
    opened();
    await screen.findByText('Teardrop · Massive Attack');

    await fireEvent.click(screen.getAllByRole('button', { name: 'Play' })[0]);

    expect(await screen.findByRole('button', { name: 'Stop' })).toBeTruthy();
  });
});

describe('the duplicates request failing', () => {
  it('shows the failure and not "No recording is on disc twice"', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(
        async () =>
          new Response(JSON.stringify({ title: 'library is unavailable' }), { status: 503 })
      )
    );
    opened();

    expect(await screen.findByText('Schall could not read the library.')).toBeTruthy();
    expect(screen.queryByText('No recording is on disc twice')).toBeNull();
  });
});
