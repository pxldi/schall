import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, setup, waitFor, within } from '@testing-library/svelte';
import { QueryClient, QueryClientProvider } from '@tanstack/svelte-query';
import type { AcquiredCopy, AcquisitionCandidate, DownloadRequest, ReviewItem } from '$lib/api';

// What the review screen is now: three kinds and nothing else, one card grid
// or row-list per question, right-aligned footer buttons in a fixed order.
// Identity, match and duplicate questions moved to Library; this file no
// longer opens on them at all.

vi.mock('$app/state', () => ({
  page: { params: {}, url: new URL('http://localhost/review') }
}));

vi.mock('$app/navigation', () => ({
  afterNavigate: () => {},
  goto: vi.fn(),
  replaceState: vi.fn()
}));

// Cast rather than typed against the real `$app/state` module: the mock above
// is a plain object with a mutable `url`, and the real module's `Page` type
// pins `pathname` to a union of the app's known routes.
const page = (await import('$app/state')).page as { params: Record<string, string>; url: URL };
const { goto } = await import('$app/navigation');
const { default: Review } = await import('./+page.svelte');

const TARGET = 'aaaaaaaa-0000-4000-8000-000000000001';
const FILE = 'dddddddd-0000-4000-8000-000000000004';
const DOWNLOAD = 'eeeeeeee-0000-4000-8000-000000000005';
const RECORDING = 'cccccccc-0000-4000-8000-000000000003';
const WANTED_RECORDING = '11111111-0000-4000-8000-000000000001';
const LIBRARY_RECORDING = '22222222-0000-4000-8000-000000000002';

function copy(overrides: Partial<AcquiredCopy> = {}): AcquiredCopy {
  return {
    id: 'bbbbbbbb-0000-4000-8000-000000000002',
    provider: 'slskd',
    username: 'harbourmaster',
    path: '/incoming/evensong.flac',
    name: 'evensong.flac',
    sizeBytes: 30_000_000,
    verdict: 'held',
    decidedBy: 'schall',
    summary: 'Nobody could identify the audio.',
    decidedAt: '2026-08-01T10:00:00Z',
    evidence: {
      name: 'evensong.flac',
      observed: { artist: 'Kestrel Grove', title: 'Evensong', durationMs: 214_000 },
      wanted: { artist: 'Kestrel Grove', title: 'Evensong', durationMs: 214_000 },
      agrees: ['artist', 'title', 'duration'],
      differs: [],
      problems: [],
      bitRate: 960,
      sizeBytes: 30_000_000
    },
    ...overrides
  };
}

function want(kind: ReviewItem['kind'], id = TARGET): ReviewItem {
  return {
    kind,
    target: {
      id,
      origin: 'playlist',
      artist: 'Kestrel Grove',
      title: 'Evensong',
      album: 'Nine Lanterns',
      durationMs: 214_000,
      isrc: null,
      recordingId: RECORDING,
      status: 'awaiting_review',
      summary: 'Waiting for an ear.',
      attempts: 2,
      anchorAttempts: 0
    },
    copies: kind === 'copies' ? [copy()] : [],
    candidates:
      kind === 'resolution'
        ? [
            {
              recordingId: RECORDING,
              artistName: 'Kestrel Grove',
              releaseTitle: 'Nine Lanterns',
              trackTitle: 'Watchfire',
              durationMs: 201_000,
              isrc: null,
              rank: 1,
              agrees: ['artist'],
              differs: ['duration (3 seconds out)'],
              summary: 'Two recordings fit this entry.'
            } as AcquisitionCandidate
          ]
        : [],
    ruledOut: 0
  };
}

function stoppedWant(): ReviewItem {
  return {
    ...want('stopped'),
    target: {
      ...want('stopped').target,
      artist: 'JACKBOYS',
      title: 'OUT WEST',
      album: 'NYE 2022',
      recordingId: WANTED_RECORDING
    },
    copies: [
      copy({
        verdict: 'accepted',
        libraryFileId: FILE,
        summary: 'Proven to be the wanted recording and imported.',
        evidence: {
          name: 'out-west.flac',
          observed: { artist: 'JACKBOYS feat. Young Thug', title: 'OUT WEST (feat. Young Thug)' },
          wanted: { artist: 'JACKBOYS', title: 'OUT WEST' },
          agrees: [],
          differs: [],
          problems: []
        }
      })
    ],
    fileRecordingId: LIBRARY_RECORDING,
    fileTitle: 'OUT WEST (feat. Young Thug)',
    fileArtist: 'JACKBOYS feat. Young Thug',
    fileReleaseTitle: 'JACKBOYS'
  };
}

function creditOnlyStoppedWant(): ReviewItem {
  const stopped = want('copies');
  return {
    ...stopped,
    kind: 'stopped',
    copies: [
      copy({
        verdict: 'discarded_tags',
        evidence: {
          name: 'evensong.flac',
          observed: { artist: 'DJ Archive', title: 'Evensong', durationMs: 214_000 },
          wanted: { artist: 'Kestrel Grove', title: 'Evensong', durationMs: 214_000 },
          agrees: ['title', 'audio'],
          differs: ['artist'],
          problems: []
        }
      })
    ]
  };
}

function download(overrides: Partial<DownloadRequest> = {}): DownloadRequest {
  return {
    id: DOWNLOAD,
    albumId: '11111111-0000-4000-8000-000000000008',
    albumTitle: 'Signal Fires',
    artistId: '22222222-0000-4000-8000-000000000009',
    artistName: 'Vela Nine',
    entryTitle: 'Signal Fires',
    entryArtist: 'Vela Nine',
    provider: 'slskd',
    username: 'harbourmaster',
    directory: '/incoming/signal-fires',
    status: 'completed',
    fileCount: 1,
    expectedTrackCount: 1,
    totalSizeBytes: 30_000_000,
    format: 'flac',
    score: 0.9,
    reasons: [],
    files: [],
    progress: {
      transferCount: 1,
      completedCount: 1,
      failedCount: 0,
      queuedCount: 0,
      transferredBytes: 30_000_000
    },
    startable: false,
    retryableCount: 0,
    requestedAt: '2026-08-01T10:00:00Z',
    startedAt: '2026-08-01T10:01:00Z',
    cancelledAt: null,
    importStatus: 'needs_review',
    importedAt: null,
    importReviews: [],
    importDecisions: [],
    revalidatable: true,
    importEvidence: { files: [], unmatchedTracks: [], problems: [] },
    ...overrides
  };
}

let piles: { wants: ReviewItem[]; downloads: DownloadRequest[] };
let requested: string[] = [];

function listing(items: unknown[]) {
  return { items, total: items.length, limit: 100, offset: 0 };
}

function answer(url: string) {
  if (url.startsWith('/api/v1/review-queue/copies') && url.endsWith('/waveform')) {
    return new Response(null, { status: 404 });
  }
  if (url.startsWith('/api/v1/review-queue')) return listing(piles.wants);
  if (url.startsWith('/api/v1/downloads?')) {
    return {
      ...listing(piles.downloads),
      counts: { open: 0, review: piles.downloads.length, imported: 0, discarded: 0, failed: 0, all: 0 }
    };
  }
  if (url.startsWith('/api/v1/library/files?')) return listing([]);
  return {};
}

beforeAll(setup);

beforeEach(() => {
  piles = { wants: [], downloads: [] };
  requested = [];
  Object.assign(HTMLMediaElement.prototype, {
    play: vi.fn().mockResolvedValue(undefined),
    pause: vi.fn(),
    load: vi.fn()
  });
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      requested.push(url);
      const already = answer(url);
      if (already instanceof Response) return already;
      return new Response(JSON.stringify(already), {
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

async function open(text: string) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(Review, undefined, { wrapper: QueryClientProvider, wrapperProps: { client } });
  await screen.findAllByText(text);
}

describe('the three kinds Review holds', () => {
  it('shows a downloaded question with Skip, Remove from Wishlist, None of these and Accept, in order', async () => {
    piles.wants = [want('copies')];
    await open('Evensong');

    const footer = screen.getByRole('button', { name: 'Accept copy' }).closest('footer')!;
    const labels = within(footer)
      .getAllByRole('button')
      .map((button) => button.textContent?.trim());
    expect(labels).toEqual(['Skip', 'Remove from Wishlist', 'None of these', 'Accept copy']);
  });

  it('shows a version question with Skip, Remove from Wishlist and Use, in order', async () => {
    piles.wants = [want('resolution')];
    await open('Evensong');

    const footer = screen.getByRole('button', { name: 'Use recording' }).closest('footer')!;
    const labels = within(footer)
      .getAllByRole('button')
      .map((button) => button.textContent?.trim());
    expect(labels).toEqual(['Skip', 'Remove from Wishlist', 'Use recording']);
  });

  it('shows a folder question with Check again before Skip', async () => {
    piles.downloads = [download()];
    await open('Signal Fires');

    const footer = screen.getByRole('button', { name: 'Check again' }).closest('footer')!;
    const labels = within(footer)
      .getAllByRole('button')
      .map((button) => button.textContent?.trim());
    expect(labels).toEqual(['Check again', 'Skip']);
  });

  it('carries a wrong song link on a downloaded question', async () => {
    piles.wants = [want('copies')];
    await open('Evensong');
    expect(screen.getByRole('button', { name: 'Wrong song' })).toBeTruthy();
  });

  it('carries no wrong song link on a version question', async () => {
    piles.wants = [want('resolution')];
    await open('Evensong');
    expect(screen.queryByRole('button', { name: 'Wrong song' })).toBeNull();
  });

  it('names the current track in the page heading', async () => {
    piles.wants = [want('copies')];
    await open('Evensong');

    expect(screen.getByRole('heading', { level: 1, name: 'Evensong' })).toBeTruthy();
  });
});

describe('a stopped want', () => {
  it('shows "In your library" with no None of these button', async () => {
    piles.wants = [stoppedWant()];
    await open('In your library');

    expect(screen.queryByRole('button', { name: 'None of these' })).toBeNull();
    const footer = screen.getByRole('button', { name: 'Accept copy' }).closest('footer')!;
    const labels = within(footer)
      .getAllByRole('button')
      .map((button) => button.textContent?.trim());
    expect(labels).toEqual(['Skip', 'Remove from Wishlist', 'Accept copy']);
  });

  it('shows the file it is filed as', async () => {
    piles.wants = [stoppedWant()];
    await open('In your library');

    expect(screen.getByText('as “OUT WEST (feat. Young Thug)”')).toBeTruthy();
  });

  it('marks the credit row in fail colour where the file disagrees with the want', async () => {
    piles.wants = [stoppedWant()];
    await open('In your library');

    const credit = screen.getByText('JACKBOYS feat. Young Thug');
    expect(credit.getAttribute('aria-invalid')).toBe('true');
  });

  it('renders a credit-only stopped want as an ordinary downloaded card', async () => {
    piles.wants = [creditOnlyStoppedWant()];
    await open('Evensong');

    expect(screen.queryByText('In your library')).toBeNull();
    expect(screen.getByRole('button', { name: 'None of these' })).toBeTruthy();
  });
});

describe('the rail', () => {
  it('filters by kind and counts every chip', async () => {
    piles.wants = [want('copies'), want('resolution', 'ffffffff-0000-4000-8000-000000000009')];
    await open('Evensong');

    expect(screen.getByRole('button', { name: 'All 2' })).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Downloaded 1' })).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Version 1' })).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Folder 0' })).toBeTruthy();

    await fireEvent.click(screen.getByRole('button', { name: 'Version 1' }));
    await waitFor(() => expect(screen.getByRole('button', { name: 'Use recording' })).toBeTruthy());
  });
});

describe('the queue reads failing', () => {
  it('names what failed and offers Retry', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => new Response(JSON.stringify({ title: 'library is unavailable' }), { status: 503 }))
    );
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(Review, undefined, { wrapper: QueryClientProvider, wrapperProps: { client } });

    expect(await screen.findByText('Schall could not read the library.')).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Retry' })).toBeTruthy();
  });

  it('says where to sign in on a 401, with no Retry to repeat it', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => new Response(JSON.stringify({ title: 'unauthorized' }), { status: 401 }))
    );
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(Review, undefined, { wrapper: QueryClientProvider, wrapperProps: { client } });

    expect(await screen.findByText('No sign-in on this connection.')).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Retry' })).toBeNull();
  });
});

describe('the Enter shortcut', () => {
  it('does not accept a copy when Enter lands on the Skip button', async () => {
    piles.wants = [want('copies')];
    await open('Evensong');

    const skip = screen.getByRole('button', { name: 'Skip' });
    skip.focus();
    await fireEvent.keyDown(skip, { key: 'Enter' });

    expect(requested.some((url) => url.endsWith('/accept'))).toBe(false);
  });

  it('does not accept a copy when Enter lands on the card\'s Play control', async () => {
    piles.wants = [want('copies')];
    await open('Evensong');

    const play = screen.getByRole('button', { name: /^Play /});
    play.focus();
    await fireEvent.keyDown(play, { key: 'Enter' });

    expect(requested.some((url) => url.endsWith('/accept'))).toBe(false);
  });
});

describe('the copy grid as a radio group', () => {
  it('moves the checked copy with ArrowRight', async () => {
    piles.wants = [
      { ...want('copies'), copies: [copy({ id: 'copy-1' }), copy({ id: 'copy-2', name: 'evensong-2.flac' })] }
    ];
    await open('Evensong');

    const radios = screen.getAllByRole('radio');
    expect(radios).toHaveLength(2);
    expect(radios[0].getAttribute('aria-checked')).toBe('true');

    radios[0].focus();
    await fireEvent.keyDown(radios[0], { key: 'ArrowRight' });

    expect(radios[0].getAttribute('aria-checked')).toBe('false');
    expect(radios[1].getAttribute('aria-checked')).toBe('true');
  });
});

describe('landmarks', () => {
  it('draws no main of its own, so AppShell\'s is the only one', async () => {
    piles.wants = [want('copies')];
    await open('Evensong');

    expect(screen.queryAllByRole('main')).toHaveLength(0);
  });
});

describe('a file named in the address bar', () => {
  afterEach(() => {
    page.url = new URL('http://localhost/review');
  });

  it('redirects to the files view in Library', async () => {
    page.url = new URL(`http://localhost/review?file=${FILE}`);
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(Review, undefined, { wrapper: QueryClientProvider, wrapperProps: { client } });

    await waitFor(() =>
      expect(goto).toHaveBeenCalledWith(`/library?view=files&id=${FILE}`, { replaceState: true })
    );
  });
});
