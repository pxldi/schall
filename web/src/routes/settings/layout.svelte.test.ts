import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, render, screen, setup } from '@testing-library/svelte';
import { createRawSnippet } from 'svelte';

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
  return render(SettingsLayout, {
    children: createRawSnippet(() => ({ render: () => '<p>a page</p>' }))
  });
}

const categories = ['Sources', 'Library', 'Automation', 'Phone', 'Jobs'];

beforeAll(setup);

beforeEach(() => {
  address = 'http://localhost/settings/sources';
});

afterEach(() => {
  cleanup();
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
