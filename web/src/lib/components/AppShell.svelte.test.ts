import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, render, screen, setup, within } from '@testing-library/svelte';
import { createRawSnippet } from 'svelte';
import { shimTheMissingBrowser } from './ui/test-window';

// The top bar is the chrome every screen is drawn inside (ADR Duoton): the
// wordmark, the seven destinations as named pills, and search, sitting on the
// page's print. There is no side rail, no readout, no per-room marks, and no
// phone form of it.

// Where the reader is. The shell reads it to decide which destination is the
// open one, and a test says so by writing here before it renders.
let address = 'http://localhost/';

vi.mock('$app/state', () => ({
  get page() {
    return { params: {}, url: new URL(address) };
  }
}));

vi.mock('$app/navigation', () => ({
  afterNavigate: () => {},
  goto: vi.fn(),
  replaceState: vi.fn()
}));

const { default: AppShell } = await import('./AppShell.svelte');

function opened() {
  return render(AppShell, {
    children: createRawSnippet(() => ({ render: () => '<p>a page</p>' }))
  });
}

/** The seven, in the order the top bar draws them. */
const seven = ['Overview', 'Artists', 'Playlists', 'Downloads', 'Review', 'Library', 'Settings'];

beforeAll(setup);
beforeAll(shimTheMissingBrowser);

beforeEach(() => {
  address = 'http://localhost/';
});

afterEach(() => {
  cleanup();
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

  it('counts a release page as Library', () => {
    address = 'http://localhost/releases/abc';

    opened();

    expect(screen.getByRole('link', { name: 'Library' }).getAttribute('aria-current')).toBe('page');
  });

  it('draws no count or state mark on any destination', () => {
    opened();

    // The owner scrapped the pills, the dots and the slskd reading alike:
    // a destination is its icon and its name, nothing else.
    for (const nav of screen.getAllByRole('navigation')) {
      expect(nav.textContent).not.toMatch(/\d/);
    }
    expect(screen.queryByRole('link', { name: /slskd/ })).toBeNull();
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
