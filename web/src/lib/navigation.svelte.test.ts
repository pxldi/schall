import { beforeEach, describe, expect, it, vi } from 'vitest';

// SvelteKit's router is the boundary, so it is what is stubbed — and it is
// stubbed the way the shipped one behaves rather than the way its documentation
// reads. `afterNavigate` collects callbacks and calls them for navigations that
// happen after they were collected; nothing is replayed for a callback that
// arrived late, and the mount of a component is not itself a navigation. Those
// are the two facts this module exists to work around, so a stub that got them
// wrong would prove nothing.
const listeners = new Set<(navigation: unknown) => void>();

vi.mock('$app/navigation', () => ({
  afterNavigate: (callback: (navigation: unknown) => void) => {
    listeners.add(callback);
  }
}));

let previousScreen: typeof import('$lib/navigation.svelte').previousScreen;
let applicationHasStarted: typeof import('$lib/navigation.svelte').applicationHasStarted;

// What this module remembers lasts as long as the application does, so each
// test gets its own copy of it rather than whatever the last one left behind.
// The root layout takes the subscription out once, when the application starts.
beforeEach(async () => {
  vi.resetModules();
  listeners.clear();
  const module = await import('$lib/navigation.svelte');
  ({ previousScreen, applicationHasStarted } = module);
  module.trackNavigation();
});

function navigate(from: string | null, to: string, type = 'link') {
  for (const listener of listeners) {
    listener({
      type,
      from: from === null ? null : { url: new URL(from) },
      to: { url: new URL(to) }
    });
  }
}

const started = () => navigate(null, 'http://localhost/library', 'enter');

describe('the screen the reader came from', () => {
  it('is nothing at all for a page that was opened directly', () => {
    started();

    expect(previousScreen.href).toBeNull();
  });

  it('is the page a link was followed from', () => {
    started();

    navigate('http://localhost/artists/talk-talk', 'http://localhost/releases/laughing-stock');

    expect(previousScreen.href).toBe('/artists/talk-talk');
  });

  it('carries the filters that page was showing', () => {
    started();

    navigate('http://localhost/library?scope=followed&sort=title', 'http://localhost/artists');

    expect(previousScreen.href).toBe('/library?scope=followed&sort=title');
  });

  it('survives a later navigation that only changes the parameters of one screen', () => {
    started();
    navigate('http://localhost/artists/talk-talk', 'http://localhost/releases/laughing-stock');

    navigate(
      'http://localhost/releases/laughing-stock',
      'http://localhost/releases/laughing-stock?tab=sources'
    );

    expect(previousScreen.href).toBe('/artists/talk-talk');
  });

  it('survives a navigation that cannot say where the reader came from', () => {
    started();
    navigate('http://localhost/artists/talk-talk', 'http://localhost/releases/laughing-stock');

    navigate(null, 'http://localhost/releases/laughing-stock');

    expect(previousScreen.href).toBe('/artists/talk-talk');
  });

  it('is forgotten once the reader works the browser back control themselves', () => {
    started();
    navigate('http://localhost/artists/talk-talk', 'http://localhost/releases/laughing-stock');

    navigate('http://localhost/releases/laughing-stock', 'http://localhost/artists/talk-talk', 'popstate');

    expect(previousScreen.href).toBeNull();
  });
});

describe('whether the application has started', () => {
  it('is not so until it has arrived somewhere', () => {
    expect(applicationHasStarted()).toBe(false);
  });

  it('is so once it has', () => {
    started();

    expect(applicationHasStarted()).toBe(true);
  });
});

describe('whether that screen is one history entry back', () => {
  it('is not, before anywhere has been navigated to', () => {
    started();

    expect(previousScreen.oneStepBack).toBe(false);
  });

  it('is, for the screen a link was just followed from', () => {
    started();

    navigate('http://localhost/artists/talk-talk', 'http://localhost/releases/laughing-stock');

    expect(previousScreen.oneStepBack).toBe(true);
  });

  it('is not, once the parameters of this screen have changed since', () => {
    started();
    navigate('http://localhost/artists/talk-talk', 'http://localhost/releases/laughing-stock');

    navigate(
      'http://localhost/releases/laughing-stock',
      'http://localhost/releases/laughing-stock?tab=sources'
    );

    expect(previousScreen.oneStepBack).toBe(false);
  });
});
