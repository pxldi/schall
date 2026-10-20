import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, setup, waitFor, within } from '@testing-library/svelte';
import { QueryClient, QueryClientProvider } from '@tanstack/svelte-query';
import type { AcquiredCopy, AcquisitionCandidate, DownloadRequest, ReviewItem } from '$lib/api';

// What the review screen is now: three kinds and nothing else, the actions in
// the panel's header in a fixed order, and copies compared in one table.
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
      anchorAttempts: 0,
      nextSearchAt: null,
      searchAttempts: 0
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
  if (url.startsWith('/api/v1/review-queue')) {
    return {
      ...listing(piles.wants),
      resolutions: piles.wants.filter((item) => item.kind === 'resolution').length
    };
  }
  if (url.startsWith('/api/v1/downloads?')) {
    return {
      ...listing(piles.downloads),
      counts: { open: 0, review: piles.downloads.length, questions: piles.downloads.length, imported: 0, discarded: 0, failed: 0, all: 0 }
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

/** The names of the buttons in the panel's header, in order. */
function headerActions() {
  const header = screen.getByRole('heading', { level: 2 }).closest('header')!;
  return within(header)
    .getAllByRole('button')
    .map((button) => button.getAttribute('aria-label') ?? button.textContent?.trim());
}

describe('the three kinds Review holds', () => {
  it('heads a downloaded question with None, Skip, Wrong song and Remove, in order', async () => {
    piles.wants = [want('copies')];
    await open('Evensong');

    expect(headerActions()).toEqual(['None of these', 'Skip', 'Wrong song', 'Remove']);
  });

  it('puts an Accept under every copy', async () => {
    piles.wants = [
      { ...want('copies'), copies: [copy({ id: 'copy-1' }), copy({ id: 'copy-2', name: 'evensong-2.flac' })] }
    ];
    await open('Evensong');

    expect(screen.getByRole('button', { name: 'Accept Copy 1' })).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Accept Copy 2' })).toBeTruthy();
  });

  it('makes Accept on the chosen copy the main action', async () => {
    piles.wants = [want('copies')];
    await open('Evensong');

    expect(screen.getByRole('button', { name: 'Accept Copy 1' }).getAttribute('data-variant')).toBe(
      'primary'
    );
  });

  it('does not make Accept the main action when the chosen copy has a field that differs', async () => {
    const differing = copy();
    differing.evidence = { ...differing.evidence!, agrees: ['title'], differs: ['artist'] };
    piles.wants = [{ ...want('copies'), copies: [differing] }];
    await open('Evensong');

    expect(screen.getByRole('button', { name: 'Accept Copy 1' }).getAttribute('data-variant')).toBe(
      'outline'
    );
  });

  it('heads a version question with Use version, Skip and Remove, in order', async () => {
    piles.wants = [want('resolution')];
    await open('Evensong');

    expect(headerActions()).toEqual(['Use version', 'Skip', 'Remove']);
  });

  it('heads a folder question with Check again before Skip', async () => {
    piles.downloads = [download()];
    await open('Signal Fires');

    expect(headerActions()).toEqual(['Check again', 'Skip']);
  });

  it('shows what was wished in its own column beside the copies', async () => {
    piles.wants = [want('copies')];
    await open('Evensong');

    const wished = screen.getByText('Wished').closest('th')!;
    const album = screen.getByText('Nine Lanterns').closest('td')!;
    expect(wished.cellIndex).toBe(album.cellIndex);
  });

  it('marks a copy field the grader found to differ', async () => {
    const differing = copy();
    differing.evidence = {
      ...differing.evidence!,
      observed: { ...differing.evidence!.observed, album: 'Nine Lanterns (Deluxe)' },
      agrees: ['title'],
      differs: ['album']
    };
    piles.wants = [{ ...want('copies'), copies: [differing] }];
    await open('Evensong');

    expect(screen.getByText('Nine Lanterns (Deluxe)').getAttribute('aria-invalid')).toBe('true');
  });

  it('ticks an album the copy names the same as the wish when the grader left it unjudged', async () => {
    const same = copy();
    same.evidence = {
      ...same.evidence!,
      observed: { ...same.evidence!.observed, album: 'Nine Lanterns' }
    };
    piles.wants = [{ ...want('copies'), copies: [same] }];
    await open('Evensong');

    const cells = screen.getAllByText('Nine Lanterns').map((node) => node.closest('td')!);
    expect(cells.map((cell) => cell.title)).toEqual(['', 'Agrees']);
  });

  it('keeps the reason for a missing sample in the cell tooltip', async () => {
    const reason = 'This entry carries no ISRC, and a preview is only ever fetched by ISRC.';
    const item = want('copies');
    item.target.anchorUnavailable = reason;
    piles.wants = [item];
    await open('Evensong');

    expect(screen.queryByText(reason)).toBeNull();
    expect(within(screen.getByRole('table')).getByText('None').closest('td')!.getAttribute('title')).toBe(
      reason
    );
  });

  it('names why a question is here beside its title', async () => {
    piles.wants = [want('copies')];
    await open('Evensong');

    const header = screen.getByRole('heading', { level: 2 }).closest('header')!;
    expect(within(header).getByText('No audio check')).toBeTruthy();
  });

  it('carries no wrong song link on a version question', async () => {
    piles.wants = [want('resolution')];
    await open('Evensong');
    expect(screen.queryByRole('button', { name: 'Wrong song' })).toBeNull();
  });

  it('keeps Review as the page title and heads the panel with the current track', async () => {
    piles.wants = [want('copies')];
    await open('Evensong');

    expect(screen.getByRole('heading', { level: 1, name: 'Review' })).toBeTruthy();
    expect(screen.getByRole('heading', { level: 2, name: 'Evensong' })).toBeTruthy();
  });

  it('puts the volume beside the player', async () => {
    piles.wants = [want('copies')];
    await open('Evensong');

    const volume = screen.getByRole('slider', { name: 'Volume' });
    const meter = screen.getByRole('meter', { name: 'Level' });
    expect(volume.closest('div')).toBe(meter.parentElement);
  });
});

describe('a stopped want', () => {
  it('heads the question with Skip, Wrong song and Remove, and no None of these', async () => {
    piles.wants = [stoppedWant()];
    await open('In your library');

    expect(headerActions()).toEqual(['Skip', 'Wrong song', 'Remove']);
  });

  it('shows the file it is filed as', async () => {
    piles.wants = [stoppedWant()];
    await open('In your library');

    expect(screen.getByText('as “OUT WEST (feat. Young Thug)”')).toBeTruthy();
  });

  it('marks the credit in fail colour where the file disagrees with the want', async () => {
    piles.wants = [stoppedWant()];
    await open('In your library');

    const credit = screen.getByText('JACKBOYS feat. Young Thug');
    expect(credit.getAttribute('aria-invalid')).toBe('true');
  });

  it('does not make Accept the main action when the credit names somebody else', async () => {
    piles.wants = [stoppedWant()];
    await open('In your library');

    expect(
      screen.getByRole('button', { name: 'Accept In your library' }).getAttribute('data-variant')
    ).toBe('outline');
  });

  it('makes Accept the main action when the credit and title agree', async () => {
    const stopped = stoppedWant();
    piles.wants = [{ ...stopped, fileArtist: 'JACKBOYS', fileTitle: 'OUT WEST' }];
    await open('In your library');

    expect(
      screen.getByRole('button', { name: 'Accept In your library' }).getAttribute('data-variant')
    ).toBe('primary');
  });

  it('renders a credit-only stopped want as an ordinary downloaded question', async () => {
    piles.wants = [creditOnlyStoppedWant()];
    await open('Evensong');

    expect(screen.queryByText('In your library')).toBeNull();
    expect(screen.getByRole('button', { name: 'None of these' })).toBeTruthy();
  });
});

describe('the rail', () => {
  it('counts the questions behind each reason', async () => {
    piles.wants = [want('copies'), want('resolution', 'ffffffff-0000-4000-8000-000000000009')];
    await open('Evensong');

    expect(screen.getByRole('button', { name: 'All 2' })).toBeTruthy();
    expect(screen.getByRole('button', { name: 'No audio check 1' })).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Which version 1' })).toBeTruthy();
  });

  it('lists no reason the queue holds none of', async () => {
    piles.wants = [want('copies')];
    await open('Evensong');

    expect(screen.queryByRole('button', { name: /^Folder/ })).toBeNull();
  });

  it('opens the questions of the reason chosen', async () => {
    piles.wants = [want('copies'), want('resolution', 'ffffffff-0000-4000-8000-000000000009')];
    await open('Evensong');

    await fireEvent.click(screen.getByRole('button', { name: 'Which version 1' }));
    await waitFor(() => expect(screen.getByRole('button', { name: 'Use version' })).toBeTruthy());
  });

  it('leaves All in the rail while a reason is chosen', async () => {
    piles.wants = [want('copies'), want('resolution', 'ffffffff-0000-4000-8000-000000000009')];
    await open('Evensong');

    await fireEvent.click(screen.getByRole('button', { name: 'Which version 1' }));
    await fireEvent.click(screen.getByRole('button', { name: 'All 2' }));
    await waitFor(() => expect(screen.queryByRole('button', { name: 'Use version' })).toBeNull());
  });
});

describe('a queue longer than one page', () => {
  it('marks a reason count as partial while pages remain unread', async () => {
    const first = want('copies');
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) => {
        const url = String(input);
        if (url.includes('/waveform')) return new Response(null, { status: 404 });
        let body: unknown = {};
        if (url.startsWith('/api/v1/review-queue')) {
          const offset = Number(new URL(url, 'http://x').searchParams.get('offset'));
          // Later pages never answer, as on a slow server.
          if (offset) return new Promise<Response>(() => {});
          body = { items: [first], total: 300, resolutions: 0, limit: 100, offset };
        } else if (url.startsWith('/api/v1/downloads?')) {
          body = { items: [], total: 0, limit: 100, offset: 0, counts: { open: 0, review: 0, questions: 0, imported: 0, discarded: 0, failed: 0, all: 0 } };
        }
        return new Response(JSON.stringify(body), {
          status: 200,
          headers: { 'Content-Type': 'application/json' }
        });
      })
    );
    await open('Evensong');

    expect(screen.getByRole('button', { name: 'All 300' })).toBeTruthy();
    expect(screen.getByRole('button', { name: 'No audio check 1+' })).toBeTruthy();
  });

  it('keeps the loaded questions when the next page fails', async () => {
    const first = want('copies');
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) => {
        const url = String(input);
        if (url.includes('/waveform')) return new Response(null, { status: 404 });
        let body: unknown = {};
        if (url.startsWith('/api/v1/review-queue')) {
          const offset = Number(new URL(url, 'http://x').searchParams.get('offset'));
          if (offset) return new Response(JSON.stringify({ title: 'library is unavailable' }), { status: 503 });
          body = { items: [first], total: 2, resolutions: 0, limit: 100, offset };
        } else if (url.startsWith('/api/v1/downloads?')) {
          body = { items: [], total: 0, limit: 100, offset: 0, counts: { open: 0, review: 0, questions: 0, imported: 0, discarded: 0, failed: 0, all: 0 } };
        }
        return new Response(JSON.stringify(body), {
          status: 200,
          headers: { 'Content-Type': 'application/json' }
        });
      })
    );
    await open('Evensong');

    expect(await screen.findByText("Couldn't load the library.")).toBeTruthy();
    expect(screen.getAllByText('Evensong').length).toBeGreaterThan(0);
    expect(screen.getByRole('button', { name: 'Accept Copy 1' })).toBeTruthy();
  });

  it('counts All from the server and reads on to count the reasons', async () => {
    const first = want('copies');
    const second = want('copies', 'ffffffff-0000-4000-8000-000000000042');
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) => {
        const url = String(input);
        requested.push(url);
        let body: unknown = {};
        if (url.includes('/waveform')) return new Response(null, { status: 404 });
        if (url.startsWith('/api/v1/review-queue')) {
          const offset = Number(new URL(url, 'http://x').searchParams.get('offset'));
          body = { items: offset ? [second] : [first], total: 2, resolutions: 0, limit: 100, offset };
        } else if (url.startsWith('/api/v1/downloads?')) {
          body = {
            items: [],
            total: 8532,
            limit: 100,
            offset: 0,
            counts: { open: 0, review: 8532, questions: 0, imported: 0, discarded: 0, failed: 0, all: 0 }
          };
        }
        return new Response(JSON.stringify(body), {
          status: 200,
          headers: { 'Content-Type': 'application/json' }
        });
      })
    );
    await open('Evensong');

    // The figure is the server's, not the length of a page it sent.
    expect(screen.getByRole('button', { name: 'All 8534' })).toBeTruthy();
    // The second page is asked for at the offset the first ended, and its
    // question joins the count.
    await waitFor(() =>
      expect(requested.some((url) => url.startsWith('/api/v1/review-queue?limit=100&offset=1'))).toBe(true)
    );
    expect(await screen.findByRole('button', { name: 'No audio check 2' })).toBeTruthy();
  });
});

describe('the queue reads failing', () => {
  it('names what failed and asks again by itself, with no Retry button', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => new Response(JSON.stringify({ title: 'library is unavailable' }), { status: 503 }))
    );
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(Review, undefined, { wrapper: QueryClientProvider, wrapperProps: { client } });

    expect(await screen.findByText("Couldn't load the library.")).toBeTruthy();
    expect(screen.getByText('Trying again.')).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Retry' })).toBeNull();
  });

  it('says where to sign in on a 401, with no Retry to repeat it', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => new Response(JSON.stringify({ title: 'unauthorized' }), { status: 401 }))
    );
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(Review, undefined, { wrapper: QueryClientProvider, wrapperProps: { client } });

    expect(await screen.findByText('Not signed in.')).toBeTruthy();
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

  it('does not accept a copy when Enter lands on a copy\'s Play control', async () => {
    piles.wants = [want('copies')];
    await open('Evensong');

    const play = screen.getByRole('button', { name: 'Play Copy 1' });
    play.focus();
    await fireEvent.keyDown(play, { key: 'Enter' });

    expect(requested.some((url) => url.endsWith('/accept'))).toBe(false);
  });
});

describe('the copy columns as a radio group', () => {
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

describe('the print', () => {
  it('takes the stored inks of the release the question came from', async () => {
    const { duoton } = await import('$lib/duoton');
    const inked = want('copies');
    inked.target.originAlbumId = 'ffffffff-0000-4000-8000-000000000006';
    inked.inks = { dark: '#0d1420', light: '#f2b48c' };
    piles.wants = [inked];
    await open('Evensong');

    await waitFor(() => expect(duoton.inks).toEqual({ dark: '#0d1420', light: '#f2b48c' }));
  });
});
