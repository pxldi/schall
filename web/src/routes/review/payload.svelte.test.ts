import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, setup, waitFor } from '@testing-library/svelte';
import { QueryClient, QueryClientProvider } from '@tanstack/svelte-query';
import type {
  AcquiredCopy,
  AcquisitionCandidate,
  AcquisitionTarget,
  DownloadRequest,
  ReviewItem
} from '$lib/api';

// The review queue is the screen where somebody decides something permanent.
// An answer given here is written to a decision store and kept for good, so
// what a press sends is the one thing about this page that may never move.
//
// This file renders the real page over a stubbed `fetch`, presses every
// control that answers something, and asserts the exact request that reaches
// the network. Redrawing the screen leaves every assertion here untouched;
// changing what a press records breaks it.

vi.mock('$app/state', () => ({
  page: { params: {}, url: new URL('http://localhost/review') }
}));

vi.mock('$app/navigation', () => ({
  afterNavigate: () => {},
  goto: vi.fn(),
  replaceState: vi.fn()
}));

const { default: Review } = await import('./+page.svelte');

const TARGET = 'aaaaaaaa-0000-4000-8000-000000000001';
const COPY = 'bbbbbbbb-0000-4000-8000-000000000002';
const RECORDING = 'cccccccc-0000-4000-8000-000000000003';
const DOWNLOAD = 'eeeeeeee-0000-4000-8000-000000000005';
const DECISION = 'ffffffff-0000-4000-8000-000000000006';
const TRACK = '99999999-0000-4000-8000-000000000007';

function target(overrides: Partial<AcquisitionTarget> = {}): AcquisitionTarget {
  return {
    id: TARGET,
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
    anchorAttempts: 0,
    ...overrides
  };
}

function copy(): AcquiredCopy {
  return {
    id: COPY,
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
    }
  };
}

function creditOnlyCopy(): AcquiredCopy {
  return {
    ...copy(),
    verdict: 'discarded_tags',
    summary: 'The artist credit differs.',
    evidence: {
      ...copy().evidence!,
      observed: { artist: 'DJ Archive', title: 'Evensong', durationMs: 214_000 },
      wanted: { artist: 'Kestrel Grove', title: 'Evensong', durationMs: 214_000 },
      agrees: ['title', 'isrc', 'audio'],
      differs: ['artist']
    }
  };
}

function candidate(): AcquisitionCandidate {
  return {
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
  };
}

function want(kind: ReviewItem['kind'], id = TARGET): ReviewItem {
  return {
    kind,
    target:
      kind === 'stopped'
        ? target({ id, status: 'pending', nextAttemptAt: null })
        : target({ id }),
    copies: kind === 'copies' ? [copy()] : kind === 'stopped' ? [creditOnlyCopy()] : [],
    candidates: kind === 'resolution' ? [candidate()] : [],
    ruledOut: 0
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
    fileCount: 2,
    expectedTrackCount: 2,
    totalSizeBytes: 60_000_000,
    format: 'flac',
    score: 0.9,
    reasons: [],
    files: [],
    progress: {
      transferCount: 2,
      completedCount: 2,
      failedCount: 0,
      queuedCount: 0,
      transferredBytes: 60_000_000
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
    importEvidence: {
      files: [
        {
          name: 'track-01.flac',
          position: '1-1',
          problems: ['The title does not agree.'],
          resolvable: true,
          observed: { artist: 'Vela Nine', title: 'Sparks', durationMs: 180_000 },
          expected: {
            artist: 'Vela Nine',
            title: 'Signal Fires',
            durationMs: 181_000,
            trackId: TRACK,
            trackNumber: 1,
            discNumber: 1
          },
          candidates: []
        }
      ],
      unmatchedTracks: [],
      problems: []
    },
    ...overrides
  };
}

let sent: { method: string; url: string; body: string | null }[] = [];
let piles: { wants: ReviewItem[]; downloads: DownloadRequest[] };

function listing(items: unknown[]) {
  return { items, total: items.length, limit: 100, offset: 0 };
}

function answer(url: string): unknown {
  if (url.includes('/waveform')) return new Response(null, { status: 404 });
  if (url.startsWith('/api/v1/review-queue')) return listing(piles.wants);
  if (url.startsWith('/api/v1/downloads?')) {
    return {
      ...listing(piles.downloads),
      counts: {
        open: 0,
        review: piles.downloads.length,
        imported: 0,
        discarded: 0,
        failed: 0,
        all: piles.downloads.length
      }
    };
  }
  return {};
}

beforeAll(setup);

beforeEach(() => {
  sent = [];
  piles = { wants: [], downloads: [] };
  Object.assign(HTMLMediaElement.prototype, {
    play: vi.fn().mockResolvedValue(undefined),
    pause: vi.fn(),
    load: vi.fn()
  });
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      sent.push({
        method: (init?.method ?? 'GET').toUpperCase(),
        url,
        body: typeof init?.body === 'string' ? init.body : null
      });
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

function writes() {
  return sent.filter((request) => request.method !== 'GET');
}

async function open(text: string) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(Review, undefined, { wrapper: QueryClientProvider, wrapperProps: { client } });
  await screen.findAllByText(text);
}

async function press(name: string | RegExp) {
  await fireEvent.click(await screen.findByRole('button', { name }));
  await waitFor(() => expect(writes().length).toBeGreaterThan(0));
  return writes()[0];
}

describe('what the queue says a want is waiting for', () => {
  it('names the evidence a want is still waiting for', async () => {
    piles.wants = [{ ...want('copies'), waitingFor: 'musicbrainz' }];
    await open('Evensong');

    expect(await screen.findByText('Waiting on MusicBrainz')).toBeTruthy();
  });

  it('says nothing about a want whose evidence is in', async () => {
    piles.wants = [want('copies')];
    await open('Evensong');

    expect(screen.queryByText(/^Waiting on/)).toBeNull();
  });
});

describe('what a review answer sends', () => {
  it('accepts the selected copy against that copy, with no body', async () => {
    piles.wants = [want('copies')];
    await open('Evensong');

    expect(await press('Accept copy')).toEqual({
      method: 'POST',
      url: `/api/v1/review-queue/copies/${COPY}/accept`,
      body: null
    });
  });

  it('sends the same thing when Enter is pressed on the selected card', async () => {
    piles.wants = [want('copies')];
    await open('Evensong');

    const card = (await screen.findAllByRole('radio'))[0];
    await fireEvent.click(card);
    await fireEvent.keyDown(window, { key: 'Enter' });
    await waitFor(() => expect(writes().length).toBeGreaterThan(0));

    expect(writes()[0]).toEqual({
      method: 'POST',
      url: `/api/v1/review-queue/copies/${COPY}/accept`,
      body: null
    });
  });

  it('refuses every copy against the want, with no body', async () => {
    piles.wants = [want('copies')];
    await open('Evensong');

    expect(await press('None of these')).toEqual({
      method: 'POST',
      url: `/api/v1/acquisition-targets/${TARGET}/none-of-these`,
      body: null
    });
  });

  it('accepts a credit-only stopped want as an ordinary downloaded question', async () => {
    piles.wants = [want('stopped')];
    await open('Evensong');

    expect(await press('Accept copy')).toEqual({
      method: 'POST',
      url: `/api/v1/review-queue/copies/${COPY}/accept`,
      body: null
    });
  });

  it('accepts a filed stopped want against the target, with no body', async () => {
    piles.wants = [
      {
        ...want('stopped'),
        copies: [
          {
            ...copy(),
            verdict: 'accepted',
            libraryFileId: 'file-1',
            evidence: { name: 'evensong.flac', agrees: [], differs: [], problems: [] }
          }
        ],
        fileRecordingId: 'other-recording',
        fileTitle: 'Evensong',
        fileArtist: 'Kestrel Grove',
        fileReleaseTitle: 'Nine Lanterns'
      }
    ];
    await open('In your library');

    expect(await press('Accept copy')).toEqual({
      method: 'POST',
      url: `/api/v1/review-queue/wants/${TARGET}/accept-file`,
      body: null
    });
  });

  it('reports the wrong recording against the want, with no body', async () => {
    piles.wants = [want('copies')];
    await open('Evensong');

    expect(await press('Wrong song')).toEqual({
      method: 'POST',
      url: `/api/v1/acquisition-targets/${TARGET}/wrong-recording`,
      body: null
    });
  });

  it('stops pursuing a want against the want, with no body', async () => {
    piles.wants = [want('copies')];
    await open('Evensong');

    expect(await press('Remove from Wishlist')).toEqual({
      method: 'POST',
      url: `/api/v1/acquisition-targets/${TARGET}/not-wanted`,
      body: null
    });
  });

  it('chooses a recording by sending that recording id and nothing else', async () => {
    piles.wants = [want('resolution')];
    await open('Evensong');

    expect(await press('Use recording')).toEqual({
      method: 'POST',
      url: `/api/v1/acquisition-targets/${TARGET}/resolution`,
      body: JSON.stringify({ recordingId: RECORDING })
    });
  });

  it('validates a folder again against the download, with no body', async () => {
    piles.downloads = [download()];
    await open('Signal Fires');

    expect(await press('Check again')).toEqual({
      method: 'POST',
      url: `/api/v1/downloads/${DOWNLOAD}/revalidate`,
      body: null
    });
  });

  it('names which track a downloaded file is by its name and that track', async () => {
    piles.downloads = [download()];
    await open('Signal Fires');

    expect(await press('Resolve')).toEqual({
      method: 'POST',
      url: `/api/v1/downloads/${DOWNLOAD}/resolutions`,
      body: JSON.stringify({ fileName: 'track-01.flac', trackId: TRACK })
    });
  });

  it('withdraws an import resolution by deleting that decision', async () => {
    piles.downloads = [
      download({
        importDecisions: [
          {
            id: DECISION,
            fileName: 'track-01.flac',
            trackId: TRACK,
            trackTitle: 'Signal Fires',
            decidedAt: '2026-08-02T10:00:00Z'
          }
        ]
      })
    ];
    await open('Signal Fires');

    expect(await press('Undo')).toEqual({
      method: 'DELETE',
      url: `/api/v1/downloads/${DOWNLOAD}/resolutions/${DECISION}`,
      body: null
    });
  });

  // Skip is the one answer that is not written down.
  it('records nothing at all when the reader presses Skip', async () => {
    piles.wants = [want('copies'), want('resolution', 'ffffffff-0000-4000-8000-000000000009')];
    await open('Evensong');

    await fireEvent.click(await screen.findByRole('button', { name: 'Skip' }));
    await new Promise((resolve) => setTimeout(resolve, 20));

    expect(writes()).toEqual([]);
  });
});
