import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, setup, within } from '@testing-library/svelte';
import { createRawSnippet } from 'svelte';
import { shimTheMissingBrowser } from './ui/test-window';

// The mast is the chrome every screen is drawn inside: one 208px rail down
// the left edge holding the seven destinations from the top, search, and the
// wordmark with the build at the foot
// as icon-and-name rows, and search at the foot. On a phone, five tabs sit along
// the bottom and the other four destinations live in More. There is no top bar,
// no readout and no per-room marks.

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

/** The seven, in the order the mast draws them. */
const seven = ['Overview', 'Artists', 'Playlists', 'Downloads', 'Review', 'Library', 'Settings'];
const five = ['Overview', 'Artists', 'Review', 'Search', 'More'];
const four = ['Playlists', 'Downloads', 'Library', 'Settings'];

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

  it('names a More destination in a title bar above the content', () => {
    address = 'http://localhost/library';

    opened();

    expect(within(screen.getByRole('main')).getByText('Library')).toBeTruthy();
  });

  it('draws no title bar on a tab route', () => {
    address = 'http://localhost/review';

    opened();

    for (const label of four) {
      expect(within(screen.getByRole('main')).queryByText(label)).toBeNull();
    }
  });

  it('names every destination in the mast', () => {
    opened();

    const top = within(screen.getByRole('navigation', { name: 'Sections' }));
    for (const label of seven) {
      expect(top.getByRole('link', { name: label })).toBeTruthy();
    }
  });

  it('names the five tabs in the phone bar, under their icons', () => {
    opened();

    const bottom = within(screen.getByRole('navigation', { name: 'Sections, compact' }));
    for (const label of five) {
      const control = bottom.getByRole(
        label === 'Overview' || label === 'Artists' || label === 'Review' ? 'link' : 'button',
        { name: label }
      );
      expect(control).toBeTruthy();
      expect(bottom.getByText(label)).toBeTruthy();
    }
  });

  it('opens the four remaining destinations from More', async () => {
    opened();

    const bottom = within(screen.getByRole('navigation', { name: 'Sections, compact' }));
    await fireEvent.click(bottom.getByRole('button', { name: 'More' }));

    const sheet = within(screen.getByRole('dialog', { name: 'More' }));
    for (const label of four) {
      expect(sheet.getByRole('link', { name: label })).toBeTruthy();
    }
    expect(screen.getByRole('button', { name: 'More' }).getAttribute('aria-expanded')).toBe(
      'true'
    );
    expect(screen.getByRole('button', { name: 'More' }).getAttribute('aria-controls')).toBe(
      'phone-more-sheet'
    );
  });

  it('closes More on Escape and backdrop press', async () => {
    opened();

    const more = screen.getByRole('button', { name: 'More' });
    await fireEvent.click(more);
    await fireEvent.keyDown(window, { key: 'Escape' });
    expect(screen.queryByRole('dialog', { name: 'More' })).toBeNull();
    expect(more.getAttribute('aria-expanded')).toBe('false');

    await fireEvent.click(more);
    await fireEvent.click(screen.getByRole('button', { name: 'Close More' }));
    expect(screen.queryByRole('dialog', { name: 'More' })).toBeNull();
  });

  it('moves focus into the sheet on open, traps Tab, and gives it back on Escape', async () => {
    opened();

    const more = screen.getByRole('button', { name: 'More' });
    // A real click focuses the button it lands on; fireEvent's does not, so
    // the opener has to be focused by hand the way FollowArtistModal's own
    // test does.
    more.focus();
    await fireEvent.click(more);

    const sheet = screen.getByRole('dialog', { name: 'More' });
    const first = within(sheet).getByRole('link', { name: 'Playlists' });
    const last = within(sheet).getByRole('link', { name: 'Settings' });
    expect(document.activeElement).toBe(first);
    expect((document.querySelector('main') as HTMLElement | null)?.inert).toBe(true);

    last.focus();
    await fireEvent.keyDown(window, { key: 'Tab' });
    expect(document.activeElement).toBe(first);

    await fireEvent.keyDown(window, { key: 'Tab', shiftKey: true });
    expect(document.activeElement).toBe(last);

    await fireEvent.keyDown(window, { key: 'Escape' });
    expect(document.activeElement).toBe(more);
    expect((document.querySelector('main') as HTMLElement | null)?.inert).toBe(false);
  });

  it('marks the open destination current and no other, in both navs', () => {
    address = 'http://localhost/review';

    opened();

    for (const link of screen.getAllByRole('link', { name: 'Review' })) {
      expect(link.getAttribute('aria-current')).toBe('page');
    }
    for (const link of screen.getAllByRole('link', { name: 'Overview' })) {
      expect(link.getAttribute('aria-current')).toBe(null);
    }
  });

  it('marks More current for a destination in the sheet', async () => {
    address = 'http://localhost/library';

    opened();

    const more = screen.getByRole('button', { name: 'More' });
    expect(more.getAttribute('aria-current')).toBe('page');

    await fireEvent.click(more);

    expect(
      within(screen.getByRole('dialog', { name: 'More' }))
        .getByRole('link', { name: 'Library' })
        .getAttribute('aria-current')
    ).toBe('page');
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
    for (const link of screen.getAllByRole('link', { name: 'Settings' })) {
      expect(link.getAttribute('aria-current')).toBe('page');
    }
  });
});
