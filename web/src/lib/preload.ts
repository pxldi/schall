import type { FetchQueryOptions, QueryKey } from '@tanstack/svelte-query';
import { queriesRestored, queryClient } from '$lib/query-client';

/** How long a page change waits for the page's data. A slower answer opens
 *  the page on its placeholders. */
export const PRELOAD_WAIT_MS = 250;

// A route's load calls this so the page usually opens with its data instead of
// on placeholders. SvelteKit runs loads when a link is hovered
// (`data-sveltekit-preload-data` in app.html), so by the click the wait is
// usually over.
export async function preloadPage<T, K extends QueryKey>(
  options: FetchQueryOptions<T, Error, T, K>
): Promise<void> {
  const ready = (async () => {
    await queriesRestored;
    await queryClient.ensureQueryData(options);
  })().catch(() => undefined);
  await Promise.race([ready, new Promise((resolve) => setTimeout(resolve, PRELOAD_WAIT_MS))]);
}
