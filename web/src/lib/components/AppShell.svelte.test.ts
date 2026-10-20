import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, setup, within } from '@testing-library/svelte';
import { QueryClient, QueryClientProvider } from '@tanstack/svelte-query';
import { createRawSnippet, tick } from 'svelte';
import { shimTheMissingBrowser } from './ui/test-window';

// The top strip is the chrome every screen is drawn inside (ADR 0042): the
// wordmark, the seven destinations each with its number key, what is running,
// and search. A key strip along the foot names the screen's keys. There is no
// phone form of it.

// Where the reader is. The shell reads it to decide which destination is the
// open one, and a test says so by writing here before it renders.
let address = 'http://localhost/';

vi.mock('$app/state', () => ({
  get page() {
    return { params: {}, url: new URL(address) };
  },
  updated: { current: false }
}));

// The shell remembers the section a release was opened from, which it hears
// as each navigation lands. A test lands one by calling what it registered.
type Arrival = (navigation: { to: { url: URL } | null }) => void;
let arrivals: Arrival[] = [];
function arrive(at: string) {
  address = at;
  for (const arrival of arrivals) arrival({ to: { url: new URL(at) } });
}

vi.mock('$app/navigation', () => ({
  afterNavigate: (arrival: Arrival) => void arrivals.push(arrival),
  beforeNavigate: () => {},
  goto: vi.fn(),
  replaceState: vi.fn()
}));

const { default: AppShell } = await import('./AppShell.svelte');

/** What the strip's counts read: the review queue and the paused imports. */
let questions = { wants: 0, folders: 0 };

function opened() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    AppShell,
    { children: createRawSnippet(() => ({ render: () => '<p>a page</p>' })) },
    { wrapper: QueryClientProvider, wrapperProps: { client } } as never
  );
}

/** The seven, in the order the top bar draws them. */
const seven = ['Overview', 'Artists', 'Playlists', 'Downloads', 'Review', 'Library', 'Settings'];

beforeAll(setup);
beforeAll(shimTheMissingBrowser);

beforeEach(() => {
  address = 'http://localhost/';
  arrivals = [];
  questions = { wants: 0, folders: 0 };
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      const body = url.startsWith('/api/v1/review-queue')
        ? { items: [], total: questions.wants, offset: 0 }
        : url.startsWith('/api/v1/downloads')
          ? { items: [], total: questions.folders, offset: 0 }
          : { activeDownloadCount: 0, sourceLoggedOutSince: null, sourceSilencedUntil: null };
      return new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } });
    })
  );
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe('AppShell', () => {
  it('puts the skip link first in the document, aimed at #main', () => {
    opened();

    const skip = screen.getByRole('link', { name: 'Skip to content' });
    expect(skip.getAttribute('href')).toBe('#main');
    expect(document.querySelectorAll('a, button')[0]).toBe(skip);
    expect(document.querySelector('main')?.id).toBe('main');
  });

  it('names every destination in the top bar', () => {
    opened();

    const top = within(screen.getByRole('navigation', { name: 'Sections' }));
    for (const label of seven) {
      expect(top.getByRole('link', { name: label })).toBeTruthy();
    }
  });

  it('names each destination by a tooltip, for the width where only its icon shows', () => {
    opened();

    const top = within(screen.getByRole('navigation', { name: 'Sections' }));
    for (const label of seven) {
      expect(top.getByRole('link', { name: label }).getAttribute('title')).toBe(label);
    }
  });

  it('draws the top bar as the only navigation', () => {
    opened();

    expect(screen.getAllByRole('navigation')).toHaveLength(1);
    expect(screen.queryByRole('button', { name: 'More' })).toBeNull();
  });

  it('marks the open destination current and no other', () => {
    address = 'http://localhost/review';

    opened();

    expect(screen.getByRole('link', { name: 'Review' }).getAttribute('aria-current')).toBe('page');
    expect(screen.getByRole('link', { name: 'Overview' }).getAttribute('aria-current')).toBe(null);
  });

  it('marks Downloads current on any of its views', () => {
    address = 'http://localhost/downloads?view=all';

    opened();

    expect(screen.getByRole('link', { name: 'Downloads' }).getAttribute('aria-current')).toBe('page');
  });

  it('counts a release opened straight from a link as Library', () => {
    address = 'http://localhost/releases/abc';

    opened();

    expect(screen.getByRole('link', { name: 'Library' }).getAttribute('aria-current')).toBe('page');
  });

  it('keeps the section a release was opened from', async () => {
    opened();
    arrive('http://localhost/artists/a1');
    arrive('http://localhost/releases/abc');
    await tick();

    expect(screen.getByRole('link', { name: 'Artists' }).getAttribute('aria-current')).toBe('page');
    expect(screen.getByRole('link', { name: 'Library' }).getAttribute('aria-current')).toBe(null);
  });

  it('keeps that section from one release to the next', async () => {
    opened();
    arrive('http://localhost/playlists/p1');
    arrive('http://localhost/releases/abc');
    arrive('http://localhost/releases/def');
    await tick();

    expect(screen.getByRole('link', { name: 'Playlists' }).getAttribute('aria-current')).toBe('page');
  });

  it('numbers each destination with the key that opens it', () => {
    opened();

    const nav = screen.getByRole('navigation', { name: 'Sections' });
    expect(nav.textContent?.replace(/\s+/g, '')).toBe(
      '1Overview2Artists3Playlists4Downloads5Review6Library7Settings'
    );
  });

  it('opens a destination from its number key', async () => {
    const { goto } = await import('$app/navigation');
    opened();

    await fireEvent.keyDown(document.body, { key: '2' });

    expect(goto).toHaveBeenCalledWith('/artists');
  });

  it('counts what Review holds beside its name', async () => {
    questions = { wants: 2, folders: 1 };
    opened();

    expect(await screen.findByText('3 to decide')).toBeTruthy();
    expect(screen.getByRole('link', { name: 'Review' }).textContent).toContain('3');
  });

  it('names no settings category, and calls Settings the page being displayed', () => {
    address = 'http://localhost/settings/sources';

    opened();

    expect(screen.queryByRole('link', { name: 'Sources' })).toBeNull();
    expect(screen.queryByRole('link', { name: 'Matching' })).toBeNull();
    expect(screen.getByRole('link', { name: 'Settings' }).getAttribute('aria-current')).toBe('page');
  });

  it('opens the command palette from the search button', () => {
    opened();
    const heard = vi.fn();
    window.addEventListener('schall:search', heard);

    screen.getByRole('button', { name: 'Search' }).click();

    window.removeEventListener('schall:search', heard);
    expect(heard).toHaveBeenCalledOnce();
  });
});
