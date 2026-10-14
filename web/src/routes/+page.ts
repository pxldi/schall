import { api } from '$lib/api';
import { preloadPage } from '$lib/duoton/preload';

// The overview opens with its data and its print (lib/duoton/preload.ts). The
// key and the print match the page's own.
export async function load() {
  const zone = Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC';
  await preloadPage({ queryKey: ['overview', zone], queryFn: () => api.overview(zone) }, (data) => {
    const seen = new Set<string>();
    const covers = data.recentlyAdded
      .filter((file) => {
        const key = file.coverUrl ?? `${file.artist}\u0000${file.title}`;
        if (seen.has(key)) return false;
        seen.add(key);
        return true;
      })
      .slice(0, 8)
      .map((file) => ({ src: file.coverUrl, seed: file.title }));
    return { covers, height: 15 };
  });
}
