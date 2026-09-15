import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen } from '@testing-library/svelte';
import { tick } from 'svelte';

// The arrow points at wherever the reader came from, which is the one thing on
// this page that cannot be checked by looking at it: the address it carries is
// decided by a navigation that already happened. SvelteKit's router is the
// boundary, so that is what is stubbed — as the shipped one behaves, collecting
// callbacks and calling them only for navigations that follow, with nothing
// replayed for whoever subscribed late. Driving whole navigations rather than
// one captured callback is what makes it possible to render the arrow after a
// navigation instead of during it, which is the case it used to get wrong.
const listeners = new Set<(navigation: unknown) => void>();

vi.mock('$app/navigation', () => ({
  afterNavigate: (callback: (navigation: unknown) => void) => {
    listeners.add(callback);
  }
}));

const { trackNavigation } = await import('$lib/navigation.svelte');
const { default: BackLink } = await import('$lib/components/BackLink.svelte');

// The root layout takes the subscription out once, when the application starts,
// before any page has been drawn. Every test then starts the application, which
// is the one arrival with nothing behind it and so also a clean slate.
trackNavigation();

beforeEach(() => {
  navigate(null, 'http://localhost/releases/laughing-stock', 'enter');
  document.addEventListener('click', swallowNavigation);
});

function navigate(from: string | null, to: string, type = 'link') {
  for (const listener of listeners) {
    listener({
      type,
      from: from === null ? null : { url: new URL(from) },
      to: { url: new URL(to) }
    });
  }
  return tick();
}

function drawArrow() {
  render(BackLink, { fallback: '/library', label: 'Library' });
}

function arrow() {
  return screen.getByRole('link');
}

// jsdom has no navigation and complains on stderr about every click this
// deliberately leaves to the browser. Cancelling the default at the document
// keeps that out of the run: the anchor's own handler has already had the event
// by the time this sees it, so nothing under test is changed.
const swallowNavigation = (event: Event) => event.preventDefault();

afterEach(() => {
  document.removeEventListener('click', swallowNavigation);
  cleanup();
});

describe('BackLink', () => {
  it('points at the section it was given before anything has been navigated', () => {
    drawArrow();

    expect(arrow().getAttribute('href')).toBe('/library');
  });

  it('names the section it falls back to when there is nowhere to return to', () => {
    drawArrow();

    expect(arrow().getAttribute('aria-label')).toBe('Library');
  });

  it('points at the page the reader came from', async () => {
    drawArrow();

    await navigate('http://localhost/artists/talk-talk', 'http://localhost/releases/laughing-stock');

    expect(arrow().getAttribute('href')).toBe('/artists/talk-talk');
  });

  it('knows where the reader came from even when it is drawn after they arrived', async () => {
    await navigate('http://localhost/artists/talk-talk', 'http://localhost/releases/laughing-stock');

    drawArrow();

    expect(arrow().getAttribute('href')).toBe('/artists/talk-talk');
  });

  it('restores the filters the page it came from was carrying', async () => {
    drawArrow();

    await navigate('http://localhost/library?scope=followed&sort=title', 'http://localhost/artists');

    expect(arrow().getAttribute('href')).toBe('/library?scope=followed&sort=title');
  });

  it('keeps pointing at that page after a change of parameters on this one', async () => {
    drawArrow();
    await navigate('http://localhost/artists/talk-talk', 'http://localhost/releases/laughing-stock');

    await navigate(
      'http://localhost/releases/laughing-stock',
      'http://localhost/releases/laughing-stock?tab=sources'
    );

    expect(arrow().getAttribute('href')).toBe('/artists/talk-talk');
  });

  it('reads as Back once there is somewhere to go back to', async () => {
    drawArrow();

    await navigate('http://localhost/artists/talk-talk', 'http://localhost/releases/laughing-stock');

    expect(arrow().getAttribute('aria-label')).toBe('Back');
  });

  it('falls back to its section for a page that was opened directly', async () => {
    await navigate('http://localhost/artists/talk-talk', 'http://localhost/library');
    await navigate(null, 'http://localhost/releases/laughing-stock', 'enter');

    drawArrow();

    expect(arrow().getAttribute('href')).toBe('/library');
  });

  it('pops the history entry rather than pushing the same address on top of it', async () => {
    const back = vi.spyOn(history, 'back').mockImplementation(() => {});
    drawArrow();
    await navigate('http://localhost/artists/talk-talk', 'http://localhost/releases/laughing-stock');

    await fireEvent.click(arrow(), { button: 0 });

    expect(back).toHaveBeenCalledOnce();
  });

  it('follows the address instead of popping when that screen is further back', async () => {
    const back = vi.spyOn(history, 'back').mockImplementation(() => {});
    drawArrow();
    await navigate('http://localhost/artists/talk-talk', 'http://localhost/releases/laughing-stock');
    await navigate(
      'http://localhost/releases/laughing-stock',
      'http://localhost/releases/laughing-stock?tab=sources'
    );

    await fireEvent.click(arrow(), { button: 0 });

    expect(back).not.toHaveBeenCalled();
  });

  it('leaves a click with a modifier to the browser', async () => {
    const back = vi.spyOn(history, 'back').mockImplementation(() => {});
    drawArrow();
    await navigate('http://localhost/artists/talk-talk', 'http://localhost/releases/laughing-stock');

    await fireEvent.click(arrow(), { button: 0, ctrlKey: true });

    expect(back).not.toHaveBeenCalled();
  });

  it('leaves a click that is not the left button to the browser', async () => {
    const back = vi.spyOn(history, 'back').mockImplementation(() => {});
    drawArrow();
    await navigate('http://localhost/artists/talk-talk', 'http://localhost/releases/laughing-stock');

    await fireEvent.click(arrow(), { button: 1 });

    expect(back).not.toHaveBeenCalled();
  });

  it('follows the fallback normally when there is nothing to go back to', async () => {
    const back = vi.spyOn(history, 'back').mockImplementation(() => {});
    drawArrow();

    await fireEvent.click(arrow(), { button: 0 });

    expect(back).not.toHaveBeenCalled();
  });
});
