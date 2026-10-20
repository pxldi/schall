import { api } from '$lib/api';
import { preloadPage } from '$lib/duoton/preload';

// The playlist opens with its entries, its inks and its print
// (lib/duoton/preload.ts). The key and the print match the page's own.
export async function load({ params }) {
  await preloadPage(
    {
      queryKey: ['playlists', params.id],
      queryFn: () => api.playlist(params.id)
    },
    (detail) => {
      const releaseIds = [
        ...new Set(detail.entries.map((entry) => entry.releaseId).filter((id): id is string => !!id))
      ].slice(0, 5);
      return releaseIds.length
        ? {
            covers: releaseIds,
            mode: releaseIds.length > 1 ? 'strip' : 'single',
            height: 20
          }
        : undefined;
    }
  );
}
