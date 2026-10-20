import { api } from '$lib/api';
import { preloadPage } from '$lib/preload';

// The overview opens with its data (lib/preload.ts). The key matches the
// page's own.
export async function load() {
  const zone = Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC';
  await preloadPage({ queryKey: ['overview', zone], queryFn: () => api.overview(zone) });
}
