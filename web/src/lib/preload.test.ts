// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest';

import { queryClient } from '$lib/query-client';
import { PRELOAD_WAIT_MS, preloadPage } from './preload';

afterEach(() => {
  vi.useRealTimers();
  queryClient.clear();
});

describe('a page change', () => {
  it('opens once the page data has arrived', async () => {
    let answer!: (value: { title: string }) => void;
    let opened = false;
    const loading = preloadPage({
      queryKey: ['preload', 'arrives'],
      queryFn: () => new Promise<{ title: string }>((resolve) => (answer = resolve))
    }).then(() => (opened = true));

    await vi.waitFor(() => expect(answer).toBeTypeOf('function'));
    expect(opened).toBe(false);
    answer({ title: 'Signal Fires' });
    await loading;

    expect(queryClient.getQueryData(['preload', 'arrives'])).toEqual({ title: 'Signal Fires' });
  });

  it('opens on the placeholders when the data is slower than the wait', async () => {
    vi.useFakeTimers();
    let opened = false;
    void preloadPage({ queryKey: ['preload', 'slow'], queryFn: () => new Promise(() => {}) }).then(
      () => (opened = true)
    );

    await vi.advanceTimersByTimeAsync(PRELOAD_WAIT_MS);

    expect(opened).toBe(true);
  });

  it('opens when the data could not be read', async () => {
    await expect(
      preloadPage({ queryKey: ['preload', 'fails'], queryFn: () => Promise.reject(new Error('down')), retry: false })
    ).resolves.toBeUndefined();
  });
});
