import tailwindcss from '@tailwindcss/vite';
import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vitest/config';

// This file runs in Node, which the application itself never does, so the
// project has no Node types and one declaration is cheaper than adding them for
// a single environment variable.
declare const process: { env: Record<string, string | undefined> };

export default defineConfig({
  plugins: [tailwindcss(), sveltekit()],
  // The build the rail's foot names. CI passes the image tag's date and
  // commit; anything else is "dev".
  define: { __SCHALL_VERSION__: JSON.stringify(process.env.SCHALL_VERSION ?? 'dev') },
  server: {
    proxy: {
      // The API this dev server forwards to. It is a variable so that the
      // browser tests can point a second dev server at the seeded API on
      // another port without the one somebody has open for development being
      // taken away from them.
      '/api': process.env.SCHALL_API_URL ?? 'http://localhost:8080'
    }
  },
  test: {
    // The CI runner is a 2 GiB pod with no CPU limit, so vitest's default of
    // one worker per host core forks a dozen jsdom documents at once and the
    // pod is killed for its memory before a single test fails. Two workers
    // fit; a laptop keeps the default.
    ...(process.env.CI ? { maxWorkers: 2 } : {}),
    // Two projects rather than one environment for everything. The logic under
    // src/lib is plain TypeScript and gains nothing from a shimmed document but
    // the seconds it costs to build one; the component tests cannot run without.
    // The split is by filename so neither has to be remembered: a test named for
    // a component (`Pager.svelte.test.ts`) gets a document, anything else does
    // not.
    projects: [
      {
        extends: true,
        test: {
          name: 'logic',
          environment: 'node',
          include: ['src/**/*.test.ts'],
          exclude: ['src/**/*.svelte.test.ts']
        }
      },
      {
        extends: true,
        // Svelte ships a server build that renders to a string and a client
        // build that mounts to a document. Only the second is the thing the
        // application runs, so the components project asks for browser entry
        // points even though it is running under Node. It is set here rather
        // than at the root so that neither `vite build` nor the logic project
        // has its resolution changed by the presence of a test runner.
        resolve: { conditions: ['browser'] },
        test: {
          name: 'components',
          environment: 'jsdom',
          include: ['src/**/*.svelte.test.ts']
        }
      }
    ]
  }
});
