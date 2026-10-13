import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest';
import { cleanup, render, screen, setup } from '@testing-library/svelte';
import { createRawSnippet, flushSync } from 'svelte';

// The root layout. SvelteKit keeps a page component when only its route
// parameters change, so going from one artist to another from the command
// palette used to leave the page holding the first artist's id: its data,
// and every action on it, stayed on the first artist. What is asserted here
// is that the page is drawn afresh when the parameters change, and only then.
//
// The boundaries stubbed are SvelteKit's own modules and `fetch`.

vi.mock('$app/state', async () => {
  const { SvelteMap } = await import('svelte/reactivity');
  const params = new SvelteMap<string, string>();
  const search = new SvelteMap<string, string>([['url', 'http://localhost/artists/a']]);
  return {
    page: {
      get params() {
        return Object.fromEntries(params);
      },
      get url() {
        return new URL(search.get('url')!);
      },
      // What a test moves to stand in for a navigation.
      go(to: string, next: Record<string, string>) {
        search.set('url', to);
        params.clear();
        for (const [key, value] of Object.entries(next)) params.set(key, value);
      }
    },
    updated: { current: false }
  };
});

vi.mock('$app/navigation', () => ({
  afterNavigate: () => {},
  beforeNavigate: () => {},
  onNavigate: () => {},
  goto: vi.fn(),
  replaceState: vi.fn()
}));

const { page } = (await import('$app/state')) as unknown as {
  page: { go: (to: string, params: Record<string, string>) => void };
};
const { default: Layout } = await import('./+layout.svelte');

beforeAll(setup);

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

function drawn() {
  let mounts = 0;
  const children = createRawSnippet(() => ({
    render: () => '<p>the page</p>',
    setup: () => {
      mounts += 1;
    }
  }));
  page.go('http://localhost/artists/a', { id: 'a' });
  render(Layout, { props: { children } });
  return () => mounts;
}

describe('the page under the layout', () => {
  it('is drawn afresh when the route parameters change', async () => {
    vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>(() => {})));
    const mounts = drawn();
    await screen.findByText('the page', {}, { timeout: 2000 });
    const before = mounts();

    page.go('http://localhost/artists/b', { id: 'b' });
    flushSync();

    expect(mounts()).toBe(before + 1);
  });

  it('is kept when only the query string changes', async () => {
    vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>(() => {})));
    const mounts = drawn();
    await screen.findByText('the page', {}, { timeout: 2000 });
    const before = mounts();

    page.go('http://localhost/artists/a?show=missing', { id: 'a' });
    flushSync();

    expect(mounts()).toBe(before);
  });
});
