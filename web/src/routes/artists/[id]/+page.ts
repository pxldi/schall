import { api } from '$lib/api';
import { preloadPage } from '$lib/preload';

// The artist page opens with its data (lib/preload.ts). The key matches the
// page's own.
export async function load({ params }) {
  await preloadPage({ queryKey: ['artists', params.id], queryFn: () => api.artist(params.id) });
}
