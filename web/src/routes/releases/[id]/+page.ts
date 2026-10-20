import { api } from '$lib/api';
import { coverSrc } from '$lib/duoton';
import { preloadPage } from '$lib/duoton/preload';

// The release page opens with its data, its inks and its cover
// (lib/duoton/preload.ts). The key and the print match the page's own.
export async function load({ params }) {
  await preloadPage(
    {
      queryKey: ['releases', params.id],
      queryFn: () => api.release(params.id)
    },
    () => ({
      covers: [{ src: coverSrc(params.id), seed: params.id }],
      mode: 'single',
      height: 20
    })
  );
}
