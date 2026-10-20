import { api } from '$lib/api';
import { preloadPage } from '$lib/preload';

// The playlist opens with its entries (lib/preload.ts). The key matches the
// page's own.
export async function load({ params }) {
  await preloadPage({
    queryKey: ['playlists', params.id],
    queryFn: () => api.playlist(params.id)
  });
}
