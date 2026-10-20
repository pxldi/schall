import type { FetchQueryOptions, QueryKey } from '@tanstack/svelte-query';
import { coverAt, printPictureWidth } from '$lib/cover-size';
import { queriesRestored, queryClient } from '$lib/query-client';
import { loadPicture } from './paint';
import { printHeight, resolveCover, type PagePrint } from './page.svelte';

/** How long a page change waits for the page's data. A slower answer opens
 *  the page on its placeholders. Waiting 400 ms for the print pictures as well
 *  made a click on a release or an artist feel ignored. */
export const PRELOAD_WAIT_MS = 250;

// A page whose data arrived after it was drawn showed itself in the previous
// page's inks and then changed colour. A route's load calls this instead, so
// the page opens with its data and its inks together. The print pictures start
// loading at the same time and are not waited for: the print paints over the
// bare ink when they land. SvelteKit runs loads when a link is hovered
// (`data-sveltekit-preload-data` in app.html), so by the click the wait is
// usually over.
export async function preloadPage<T, K extends QueryKey>(
  options: FetchQueryOptions<T, Error, T, K>,
  print?: (data: T) => PagePrint | undefined
): Promise<void> {
  const ready = (async () => {
    await queriesRestored;
    const data = await queryClient.ensureQueryData(options);
    const request = print?.(data);
    if (request) void warmPrint(request);
  })().catch(() => undefined);
  await Promise.race([ready, new Promise((resolve) => setTimeout(resolve, PRELOAD_WAIT_MS))]);
}

/** Loads the pictures the shell's print will ask for, at the width it will ask
 *  for them, so the print paints as soon as the page is up. */
export function warmPrint(request: PagePrint): Promise<unknown> {
  if (typeof document === 'undefined') return Promise.resolve();
  const covers = (request.covers ?? []).map(resolveCover);
  const mode = request.mode ?? 'strip';
  const rem = parseFloat(getComputedStyle(document.documentElement).fontSize) || 16;
  const width =
    printPictureWidth(
      mode,
      covers.length,
      document.documentElement.clientWidth,
      printHeight(request) * rem
    ) ?? 500;
  return Promise.all(
    covers.map((cover) =>
      cover.src ? loadPicture(width === 500 ? cover.src : coverAt(cover.src, width)) : undefined
    )
  );
}
