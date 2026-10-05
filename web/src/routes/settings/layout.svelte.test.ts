import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, render, screen, setup } from '@testing-library/svelte';
import { createRawSnippet } from 'svelte';
import { QueryClient, QueryClientProvider } from '@tanstack/svelte-query';

// The five category links are drawn twice: a scrollable row above the
// content below `lg`, and the side rail at `lg` and up. Both sit in the DOM
// at once (CSS decides which one shows), so a query by role and name finds
// both, and the active category is marked current in both.

let address = 'http://localhost/settings/sources';

vi.mock('$app/state', () => ({
  get page() {
    return { params: {}, url: new URL(address) };
  }
}));

const { default: SettingsLayout } = await import('./+layout.svelte');

function opened() {
  // The print reads the Overview's newest arrivals; an empty answer is enough.
  vi.stubGlobal(
    'fetch',
    vi.fn(async () => new Response(JSON.stringify({ recentlyAdded: [] }), { status: 200 }))
  );
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    SettingsLayout,
    { children: createRawSnippet(() => ({ render: () => '<p>a page</p>' })) },
    { wrapper: QueryClientProvider, wrapperProps: { client } }
  );
}

const categories = ['Sources', 'Library', 'Automation', 'Phone', 'Jobs'];

beforeAll(setup);

beforeEach(() => {
  address = 'http://localhost/settings/sources';
});

afterEach(() => {
  vi.unstubAllGlobals();
  cleanup();
});

describe('the settings page', () => {
  it('is titled Settings', () => {
    opened();

    expect(screen.getByRole('heading', { level: 1, name: 'Settings' })).toBeTruthy();
  });
});

describe('the settings category navigation', () => {
  it('names every category once, in the rail', () => {
    opened();

    for (const name of categories) {
      expect(screen.getAllByRole('link', { name }).length).toBe(1);
    }
  });

  it('marks the open category current and no other', () => {
    address = 'http://localhost/settings/library';
    opened();

    expect(screen.getByRole('link', { name: 'Library' }).getAttribute('aria-current')).toBe('page');
    expect(screen.getByRole('link', { name: 'Sources' }).getAttribute('aria-current')).toBe(null);
  });
});
