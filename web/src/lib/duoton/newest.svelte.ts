import { createQuery } from '@tanstack/svelte-query';
import { fromStore } from 'svelte/store';
import { api } from '$lib/api';
import { usePagePrint, type PagePrint } from './page.svelte';

// The print for a page that has no music of its own (Settings, search, the
// error page): a strip of the newest arrivals, inked from the newest one. It
// reads the Overview's own query, so it costs nothing when that is cached.
export function useNewestPrint(options: Omit<PagePrint, 'covers'> & { count?: number } = {}) {
  const zone = Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC';
  const overview = fromStore(
    createQuery({
      queryKey: ['overview', zone],
      queryFn: () => api.overview(zone)
    })
  );
  const { count = 6, ...rest } = options;
  usePagePrint(() => {
    // Recently added lists tracks, and several tracks of one album arrive
    // together, so the same cover was printed twice side by side.
    const seen = new Set<string>();
    const added = (overview.current.data?.recentlyAdded ?? []).filter((item) => {
      const key = item.coverUrl ?? item.releaseId ?? `${item.artist} ${item.title}`;
      if (seen.has(key)) return false;
      seen.add(key);
      return true;
    });
    return {
      ...rest,
      covers: added
        .slice(0, count)
        .map((item) => ({ src: item.coverUrl, seed: `${item.artist} ${item.title}`, inks: item.inks }))
    };
  });
}
