import { QueryClient } from '@tanstack/svelte-query';
import { queryRetry } from '$lib/errors';
import { PERSIST_MAX_AGE, persistQueries } from '$lib/persist';

// The one query client of the application. It lives in a module rather than in
// the root layout so a route's load can fill it before the page is drawn
// (`$lib/preload`), and the page then reads the same answers.
export const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 30_000,
      // Answers are kept in memory as long as they are kept in the browser,
      // so going back to a page draws it from what it showed last time.
      gcTime: PERSIST_MAX_AGE,
      refetchOnWindowFocus: false,
      retry: queryRetry
    }
  }
});

// The pages wait for the kept answers before they first draw, so a page that
// has been seen before opens on its data rather than on placeholders. A slow
// or missing store is not waited on for long.
export const queriesRestored: Promise<void> = Promise.race([
  persistQueries(queryClient, __SCHALL_VERSION__),
  new Promise<void>((resolve) => setTimeout(resolve, 300))
]);
