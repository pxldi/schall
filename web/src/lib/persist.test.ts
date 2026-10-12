import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { QueryClient } from '@tanstack/svelte-query';
import { PERSIST_DELAY_MS, saveAfterAnswers } from '$lib/persist';

beforeEach(() => vi.useFakeTimers());
afterEach(() => vi.useRealTimers());

describe('saveAfterAnswers', () => {
  it('writes once for answers that arrive together', () => {
    const client = new QueryClient();
    const save = vi.fn();
    saveAfterAnswers(client, save);

    client.setQueryData(['artists'], [1]);
    client.setQueryData(['playlists'], [2]);
    client.setQueryData(['artists'], [3]);
    vi.advanceTimersByTime(PERSIST_DELAY_MS);

    expect(save).toHaveBeenCalledTimes(1);
  });

  // Most cache events are a query starting or finishing a fetch, which changes
  // nothing a reload would draw.
  it('does not write when a query only starts fetching', () => {
    const client = new QueryClient();
    const save = vi.fn();
    saveAfterAnswers(client, save);

    void client.prefetchQuery({ queryKey: ['slow'], queryFn: () => new Promise(() => {}) });
    vi.advanceTimersByTime(PERSIST_DELAY_MS);

    expect(save).not.toHaveBeenCalled();
  });

  it('writes when a query leaves the cache', () => {
    const client = new QueryClient();
    client.setQueryData(['artists'], [1]);
    const save = vi.fn();
    saveAfterAnswers(client, save);

    client.removeQueries({ queryKey: ['artists'] });
    vi.advanceTimersByTime(PERSIST_DELAY_MS);

    expect(save).toHaveBeenCalledTimes(1);
  });
});
