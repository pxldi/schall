import { api } from '$lib/api';
import { preloadPage } from '$lib/duoton/preload';

// The artist page opens with its data, its inks and its lead cover
// (lib/duoton/preload.ts). The key matches the page's own.
export async function load({ params }) {
  await preloadPage({ queryKey: ['artists', params.id], queryFn: () => api.artist(params.id) }, (detail) =>
    detail.leadAlbumId ? { covers: [detail.leadAlbumId], mode: 'single' } : undefined
  );
}
