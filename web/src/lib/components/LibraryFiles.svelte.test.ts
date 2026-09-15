import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, setup } from '@testing-library/svelte';
import { QueryClient, QueryClientProvider } from '@tanstack/svelte-query';
import type { LibraryFile, LibrarySummary } from '$lib/api';

// The files view of the Library screen. It lists the audio files a scan found,
// narrowed by match state, by identity state, by a search and by page. None of
// that narrowing happens in the browser: the list is longer than one page, so
// the screen asks the server for the pile it is about to show. What is asserted
// here is the request that went out, because that is where the narrowing lives
// and the screen looks much the same either way.
//
// The only boundaries stubbed are SvelteKit's address bar, which this view
// reads a search out of, and `fetch`.
const pageState = vi.hoisted(() => ({
  params: {},
  url: new URL('http://localhost/library?view=files')
}));

vi.mock('$app/state', () => ({ page: pageState }));
vi.mock('$app/navigation', () => ({ replaceState: vi.fn() }));

const { default: LibraryFiles } = await import('$lib/components/LibraryFiles.svelte');

function file(overrides: Partial<LibraryFile> = {}): LibraryFile {
  return {
    id: 'c1d2e3f4-0000-4000-8000-000000000001',
    path: '/music/Talk Talk/Laughing Stock/01 Myrrhman.flac',
    sizeBytes: 30_000_000,
    modifiedAt: '2026-08-01T10:00:00Z',
    artistTag: 'Talk Talk',
    albumTag: 'Laughing Stock',
    titleTag: 'Myrrhman',
    durationMs: 320_000,
    matchStatus: 'unmatched',
    matchCandidateCount: 0,
    mappingManual: false,
    resolutionStatus: 'pending',
    identityManual: false,
    identityCandidateCount: 0,
    ...overrides
  };
}

function summary(): LibrarySummary {
  return {
    rootCount: 1,
    fileCount: 400,
    missingCount: 0,
    errorCount: 0,
    matchedCount: 360,
    unmatchedCount: 40,
    ambiguousCount: 0,
    duplicateCount: 0,
    duplicatesToDecideCount: 0,
    resolvedCount: 360,
    needsReviewCount: 0,
    conflictCount: 0,
    localOnlyCount: 0,
    pendingCount: 40,
    failedCount: 0,
    totalSizeBytes: 12_000_000_000,
    scanStatus: 'completed',
    scanStartedAt: '2026-08-01T09:00:00Z',
    scanCompletedAt: '2026-08-01T09:20:00Z'
  };
}

/** Every address the view asked for, in order, so a test can say which pile it
 * asked the server for rather than which rows it kept. */
let asked: string[] = [];
let sent: { method: string; url: string }[] = [];

/** What the Held twice tab would show, which the files view reads only to point
 * at it. */
let heldTwice: unknown[] = [];

/** What the file list answers with. Empty is the case every empty-state test is
 * about. */
let listed: LibraryFile[] = [];

/** Lets go of the first file-list answer, which `answering(true)` holds back.
 * Between the view being drawn and that answer arriving is the window a reader
 * actually presses a filter in. */
let letTheFirstAnswerThrough = () => {};

function answering(holdTheFirstAnswer = false) {
  asked = [];
  sent = [];
  const held = new Promise<void>((resolve) => (letTheFirstAnswerThrough = resolve));
  let outstanding = holdTheFirstAnswer;
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), 'http://localhost');
      asked.push(url.pathname + url.search);
      sent.push({
        method: (init?.method ?? 'GET').toUpperCase(),
        url: url.pathname + url.search
      });
      if (url.pathname === '/api/v1/library/files') {
        if (outstanding) {
          outstanding = false;
          await held;
        }
        const body = { items: listed, total: listed.length, limit: 25, offset: 0 };
        return new Response(JSON.stringify(body), { status: 200 });
      }
      if (url.pathname.endsWith('/matches')) {
        return new Response(JSON.stringify({ items: [] }), { status: 200 });
      }
      // The music folders listed along the top of the view.
      if (url.pathname === '/api/v1/library/roots') {
        return new Response(JSON.stringify({ items: [] }), { status: 200 });
      }
      // The recordings the library holds more than once, read only so an empty
      // "Already held" can say where the reader's real question is answered.
      if (url.pathname === '/api/v1/library/duplicates') {
        return new Response(
          JSON.stringify({ recordings: heldTwice, total: heldTwice.length }),
          { status: 200 }
        );
      }
      return new Response(JSON.stringify(summary()), { status: 200 });
    })
  );
}

function opened() {
  // The client is per test: a cache shared between them would answer the second
  // one out of the first one's list.
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(LibraryFiles, undefined, {
    wrapper: QueryClientProvider,
    wrapperProps: { client }
  });
}

function filesAsked() {
  return asked.filter((address) => address.startsWith('/api/v1/library/files'));
}

beforeAll(setup);

beforeEach(() => {
  asked = [];
  sent = [];
  pageState.url = new URL('http://localhost/library?view=files');
  listed = [file()];
  heldTwice = [];
  localStorage.removeItem('schall:count:library-files');
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe('a match filter pressed while the opening request is still in flight', () => {
  it('asks the server for that pile too', async () => {
    // Every file is still on its way when Unmatched is pressed. Each filter is
    // its own question, so the press has to send one; it used to ask the request
    // already running to run again, which asked nothing and left the strip on
    // Unmatched over the whole library.
    answering(true);
    opened();

    await fireEvent.click(await screen.findByRole('button', { name: 'Unmatched' }));
    letTheFirstAnswerThrough();

    await vi.waitFor(() => {
      expect(filesAsked()).toContain('/api/v1/library/files?status=unmatched&limit=25');
    });
  });
});

describe('files set aside from Review', () => {
  it('lists them and clears the set-aside row from Return to review', async () => {
    pageState.url = new URL('http://localhost/library?view=files&setAside=true');
    listed = [file({ setAside: true })];
    answering();
    opened();

    const control = await screen.findByRole('button', { name: 'Return to review' });
    expect(screen.getByText('Set-aside files')).toBeTruthy();
    expect(control.textContent?.trim()).toBe('Return to review');
    expect(control.getAttribute('aria-pressed')).toBeNull();
    expect((control as HTMLButtonElement).disabled).toBe(false);
    expect(filesAsked()).toContain('/api/v1/library/files?limit=25&setAside=true');

    await fireEvent.click(control);

    await vi.waitFor(() => {
      expect(sent).toContainEqual({
        method: 'DELETE',
        url: '/api/v1/library/files/c1d2e3f4-0000-4000-8000-000000000001/set-aside'
      });
    });
  });
});

// The two states the library summary counted and nothing could list. The number
// was on the screen and the rows behind it were not reachable at all.
describe('the states the scanner records', () => {
  it('asks for the files the scanner could not read', async () => {
    answering();
    opened();
    await screen.findByRole('button', { name: 'Unreadable' });

    await fireEvent.click(screen.getByRole('button', { name: 'Unreadable' }));

    await vi.waitFor(() => {
      expect(filesAsked()).toContain('/api/v1/library/files?status=unreadable&limit=25');
    });
  });

  it('asks for the files the last scan did not find', async () => {
    answering();
    opened();
    await screen.findByRole('button', { name: 'Missing' });

    await fireEvent.click(screen.getByRole('button', { name: 'Missing' }));

    await vi.waitFor(() => {
      expect(filesAsked()).toContain('/api/v1/library/files?status=missing&limit=25');
    });
  });
});

// An empty list is empty for a reason, and "No audio files found" was an answer
// to a question nobody asked: the library has files, and what is empty is the
// filter.
describe('an empty list', () => {
  it('names the question that came back empty', async () => {
    answering();
    listed = [];
    opened();
    await screen.findByRole('button', { name: 'Missing' });

    await fireEvent.click(screen.getByRole('button', { name: 'Missing' }));

    expect(await screen.findByText('Every file is where the library left it')).toBeTruthy();
  });

  // "Already held" is about a decision; the reader who pressed it almost always
  // meant the music. The tab that answers that says how many, and opens.
  it('points at Held twice when that is what the reader meant', async () => {
    answering();
    listed = [];
    heldTwice = [{ recordingId: 'a' }, { recordingId: 'b' }];
    const opening = vi.fn();
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(LibraryFiles, { props: { onheldtwice: opening } } as never, {
      wrapper: QueryClientProvider,
      wrapperProps: { client }
    });
    await screen.findByRole('button', { name: 'Already held' });

    await fireEvent.click(screen.getByRole('button', { name: 'Already held' }));
    expect(await screen.findByText(/2 recordings more than once/)).toBeTruthy();
    const pointer = await screen.findByRole('button', { name: 'Open Held twice' });
    await fireEvent.click(pointer);

    expect(opening).toHaveBeenCalled();
  });

  it('says nothing about Held twice when the library holds nothing twice', async () => {
    answering();
    listed = [];
    opened();
    await screen.findByRole('button', { name: 'Already held' });

    await fireEvent.click(screen.getByRole('button', { name: 'Already held' }));
    await screen.findByText('No file is blocked by a decision');

    expect(screen.queryByRole('button', { name: /more than once/ })).toBeNull();
  });
});

describe('the file list request failing', () => {
  it('shows the failure and not "No audio files found"', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) => {
        const url = new URL(String(input), 'http://localhost');
        if (url.pathname === '/api/v1/library/files') {
          return new Response(JSON.stringify({ title: 'library is unavailable' }), {
            status: 503
          });
        }
        if (url.pathname === '/api/v1/library/roots') {
          return new Response(JSON.stringify({ items: [] }), { status: 200 });
        }
        if (url.pathname === '/api/v1/library/duplicates') {
          return new Response(JSON.stringify({ recordings: [], total: 0 }), { status: 200 });
        }
        return new Response(JSON.stringify(summary()), { status: 200 });
      })
    );
    opened();

    expect(await screen.findByText('Schall could not read the library.')).toBeTruthy();
    expect(screen.queryByText('No audio files found')).toBeNull();
    expect(screen.queryByText('Add a music folder below, then scan it.')).toBeNull();
  });
});

describe('the file list loading shape', () => {
  it('uses a generous fixed number of rows while the next answer is pending', async () => {
    answering(true);
    opened();

    expect(await screen.findByRole('status', { name: 'Loading files' })).toBeTruthy();
  });
});

describe('file filters', () => {
  it('clears every filter in one step', async () => {
    pageState.url = new URL('http://localhost/library?view=files&q=burial&setAside=true');
    answering();
    opened();

    await fireEvent.click(await screen.findByRole('button', { name: 'Clear filters' }));

    expect((screen.getByRole('textbox', { name: 'Search discovered files' }) as HTMLInputElement).value).toBe('');
    expect(screen.queryByRole('button', { name: 'Clear filters' })).toBeNull();
  });
});

describe('the unidentified filter and inline decisions', () => {
  it('asks for unidentified files', async () => {
    answering();
    opened();
    await fireEvent.click(await screen.findByRole('button', { name: 'Unidentified' }));

    await vi.waitFor(() => {
      expect(filesAsked()).toContain('/api/v1/library/files?limit=25&unidentified=true');
    });
  });

  it('opens an ambiguous file in the row', async () => {
    answering();
    listed = [file({ matchStatus: 'ambiguous' })];
    opened();
    await fireEvent.click(await screen.findByRole('button', { name: 'Decide' }));

    expect(await screen.findByRole('textbox', { name: 'Search catalogue tracks' })).toBeTruthy();
  });

  it('opens the file named by a deep link', async () => {
    pageState.url = new URL('http://localhost/library?view=files&id=c1d2e3f4-0000-4000-8000-000000000001');
    answering();
    listed = [file({ matchStatus: 'ambiguous' })];
    opened();

    expect(await screen.findByRole('textbox', { name: 'Search catalogue tracks' })).toBeTruthy();
    expect(filesAsked()).toContain('/api/v1/library/files?limit=25&id=c1d2e3f4-0000-4000-8000-000000000001');
  });
});

// How the rows are drawn, which is a different question from which rows they
// are. Nothing below changes a filter, a search or a page: the same file is
// listed in every one of them and what is asserted is where its parts sit.
// The Comfortable/Compact picker is gone: one row density, drawn the way
// Comfortable used to be.
describe('the one row density', () => {
  it('stacks the artist under the title', async () => {
    answering();
    opened();
    await screen.findByText('Myrrhman');

    // No column of its own, and the name is there all the same.
    expect(screen.queryByRole('columnheader', { name: 'Artist' })).toBeNull();
    expect(screen.getByText('Talk Talk')).toBeTruthy();
  });

  it('sets the title at the size a body of text reads at', async () => {
    answering();
    opened();

    expect((await screen.findByText('Myrrhman')).getAttribute('data-text-size')).toBe('body');
  });
});

// A track title is the thing being identified and must never be cut. Everything
// beside it is metadata, and metadata takes the ellipsis.
describe('what wraps and what truncates', () => {
  it('lets the title wrap', async () => {
    answering();
    opened();

    expect((await screen.findByText('Myrrhman')).getAttribute('data-truncated')).toBeNull();
  });

  it('cuts the album off rather than growing the column', async () => {
    answering();
    opened();

    expect((await screen.findByText('Laughing Stock')).getAttribute('data-truncated')).toBe('true');
  });

  it('cuts the quiet line under the title off', async () => {
    answering();
    opened();

    expect((await screen.findByText('Talk Talk')).getAttribute('data-truncated')).toBe('true');
  });
});

// Ink 4 clears the contrast floor up to the regular surface and no further, and
// a pointed-at row is drawn on a thicker one. So every quiet line in a row
// promotes when the row does. It is invisible in a screenshot, which is why it
// is asserted.
describe('the quiet line on a row somebody is pointing at', () => {
  it('promotes the artist under the title', async () => {
    answering();
    opened();

    const quiet = await screen.findByText('Talk Talk');
    expect(quiet.getAttribute('data-tone')).toBe('quiet');
    expect(quiet.getAttribute('data-promotes-on-hover')).toBe('true');
  });

  it('promotes the format', async () => {
    answering();
    opened();

    const format = await screen.findByText('FLAC');
    expect(format.getAttribute('data-tone')).toBe('quiet');
    expect(format.getAttribute('data-promotes-on-hover')).toBe('true');
  });
});

// The format is read off the extension, because that is the only place it is
// written down. Saying more than FLAC or MP3 from here would be inventing it.
describe('the format column', () => {
  it('says what kind of file it is', async () => {
    answering();
    opened();

    expect(await screen.findByText('FLAC')).toBeTruthy();
  });

  // The states matching's refusals are readable in. A table column of them is
  // read at a glance, so the row shows a short tag now rather than the full
  // sentence; the sentence stays reachable as the tag's title.
  it('keeps saying where the file stands', async () => {
    answering();
    listed = [file({ matchStatus: 'ambiguous' })];
    opened();

    const tag = await screen.findByText('ambiguous');
    expect(tag.getAttribute('title')).toBe('More than one catalogue track fits this file');
  });
});

// jsdom applies no CSS, so it cannot see the opacity change that makes the
// play control visible without a hover — that is checked in
// e2e/library.spec.ts instead. What this proves is that the control was never
// gated behind a hover or focus event to begin with: it is always in the DOM
// and always clickable.
describe('the play control', () => {
  beforeEach(() => {
    // jsdom has no real media pipeline, so `hear()`'s `audio.play().catch(...)`
    // needs a real promise to chain onto instead of jsdom's unimplemented
    // default.
    window.HTMLMediaElement.prototype.play = vi.fn().mockResolvedValue(undefined);
    window.HTMLMediaElement.prototype.pause = vi.fn();
  });

  it('is reachable and works without a hover or focus event first', async () => {
    answering();
    opened();

    const play = await screen.findByRole('button', { name: 'Play Myrrhman' });
    await fireEvent.click(play);

    expect(await screen.findByRole('button', { name: 'Stop Myrrhman' })).toBeTruthy();
  });
});
