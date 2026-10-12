import adapter from '@sveltejs/adapter-static';
import { vitePreprocess } from '@sveltejs/vite-plugin-svelte';

/** @type {import('@sveltejs/kit').Config} */
const config = {
  preprocess: vitePreprocess(),
  kit: {
    adapter: adapter({
      fallback: 'index.html'
    }),
    // An open tab asks the server for the build it serves, so a tab left open
    // across a deploy moves to the new interface on its next page change.
    version: { pollInterval: 60_000 }
  }
};

export default config;
