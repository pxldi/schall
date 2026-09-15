import { defineConfig, devices } from '@playwright/test';

// The browser tests run against the whole application — the real Svelte build,
// the real Go API, and a PostgreSQL database seeded from internal/seed. Nothing
// is mocked, because the things worth testing in a browser are the ones the
// component tests cannot see: routing, the address bar as a record of what the
// reader narrowed a list to, a badge counting across four queries, a page that
// asks the API a question the component never posed.
//
// Two servers are started for a run. The API is started by scripts/e2e-api.sh,
// which seeds the database before it comes up; the Svelte dev server is the same
// one `make dev` runs, on its own port, forwarding /api to that API. Both stay
// off the ports development uses, so a run does not take somebody's stack away
// from them.

const apiAddress = process.env.SCHALL_E2E_API_ADDRESS ?? '127.0.0.1:8181';
const webPort = Number(process.env.SCHALL_E2E_WEB_PORT ?? 5174);

export default defineConfig({
  testDir: 'e2e',
  // The fixture is written once per run and every test reads it, so a test that
  // changed data would change what the others are looking at. They are all
  // read-only by rule, which e2e/seeded.ts states along with the fixture.
  fullyParallel: true,
  forbidOnly: Boolean(process.env.CI),
  retries: process.env.CI ? 1 : 0,
  // CI is a 2 GiB runner pod, and the same limit already forced the vitest
  // workers down to two (#433). A Playwright worker is a whole chromium, and it
  // shares the pod with a Vite dev server and the Go API, so CI runs one at a
  // time. A laptop keeps the default.
  workers: process.env.CI ? 1 : undefined,
  reporter: process.env.CI ? [['github'], ['list']] : [['list']],
  // Long enough to cover a first request that waits on the query cache filling,
  // short enough that a page which never renders fails rather than hangs.
  timeout: 30_000,
  expect: { timeout: 10_000 },
  use: {
    baseURL: `http://127.0.0.1:${webPort}`,
    trace: 'on-first-retry',
    screenshot: 'only-on-failure'
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
  webServer: [
    {
      command: '../scripts/e2e-api.sh',
      url: `http://${apiAddress}/api/v1/health`,
      // Never reused, because this command is the run's setup rather than just
      // a server: it builds both binaries out of the working tree and reseeds
      // the database before the API comes up. Reusing whatever answers on the
      // port runs the specs against the binary some earlier run built, over
      // whatever that database holds now — and it is the same database the
      // README invites you to point Schall at and click through, so an artist
      // somebody followed by hand is state a spec would then read as the
      // fixture. Rebuilding and reseeding costs a few seconds; it is what makes
      // "freshly seeded" true. The fixture itself no longer expires, so this is
      // not about a database going stale on its own.
      reuseExistingServer: false,
      timeout: 180_000,
      stdout: 'pipe',
      stderr: 'pipe'
    },
    {
      // Bound to 127.0.0.1 explicitly. Vite's default host is "localhost",
      // which can resolve to ::1 first, and the readiness check below asks for
      // the address rather than the name.
      command: `npm run dev -- --port ${webPort} --strictPort --host 127.0.0.1`,
      url: `http://127.0.0.1:${webPort}`,
      env: { SCHALL_API_URL: `http://${apiAddress}` },
      reuseExistingServer: !process.env.CI,
      timeout: 120_000,
      stdout: 'pipe',
      stderr: 'pipe'
    }
  ]
});
