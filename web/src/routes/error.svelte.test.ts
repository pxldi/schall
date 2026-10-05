import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, setup } from '@testing-library/svelte';
import { QueryClient, QueryClientProvider } from '@tanstack/svelte-query';

let status = 404;

vi.mock('$app/state', () => ({
  get page() {
    return {
      status,
      error: status === 404 ? null : { message: 'the database is gone' },
      url: new URL('http://localhost/nowhere')
    };
  }
}));

const { default: ErrorPage } = await import('./+error.svelte');

function opened() {
  vi.stubGlobal(
    'fetch',
    vi.fn(async () => new Response(JSON.stringify({ recentlyAdded: [] }), { status: 200 }))
  );
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(ErrorPage, undefined, { wrapper: QueryClientProvider, wrapperProps: { client } });
}

beforeAll(setup);

afterEach(() => {
  status = 404;
  vi.unstubAllGlobals();
  cleanup();
});

describe('the error page', () => {
  it('says 404 and offers the way back and search', async () => {
    opened();

    expect(screen.getByRole('heading', { level: 1, name: '404' })).toBeTruthy();
    expect(screen.getByText('Nothing at /nowhere')).toBeTruthy();
    expect(screen.getByRole('link', { name: 'Go to Overview' }).getAttribute('href')).toBe('/');

    const summoned = vi.fn();
    window.addEventListener('schall:search', summoned);
    await fireEvent.click(screen.getByRole('button', { name: 'Search' }));
    window.removeEventListener('schall:search', summoned);
    expect(summoned).toHaveBeenCalledOnce();
  });

  it('offers a reload and the failure for any other error', () => {
    status = 500;
    opened();

    expect(screen.getByRole('heading', { level: 1, name: 'Error' })).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Reload' })).toBeTruthy();
    expect(screen.getByText('the database is gone')).toBeTruthy();
  });
});
