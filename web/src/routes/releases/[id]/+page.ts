import { api } from '$lib/api';
import { preloadPage } from '$lib/preload';

// The release page opens with its data (lib/preload.ts). The key matches the
// page's own.
export async function load({ params }) {
  await preloadPage({
    queryKey: ['releases', params.id],
    queryFn: () => api.release(params.id)
  });
}
