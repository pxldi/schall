import { expect, test, type Locator, type Page } from '@playwright/test';
import { seeded } from '../seeded';

// Screenshots of the main pages over the seeded fixture, compared with the
// baselines in __screenshots__. They exist so a merge that moves, hides or
// restyles something on a page nobody looked at fails in CI instead of
// reaching the live app. Run them with scripts/visual.sh; after an intended
// change, redraw the baselines with `scripts/visual.sh --update-snapshots`
// and commit the new images with the change.
//
// Everything that would differ between two runs of the same code is pinned:
// the fixture, the browser and its fonts (Playwright's image), the window, the
// locale and the time zone. The clock is fixed at the moment the test starts,
// which is a few seconds after seeding, because the fixture's times are
// relative to now() and "3 hours ago" has to stay three hours.

test.beforeAll(() => {
  if (!process.env.SCHALL_VISUAL_WS_ENDPOINT) {
    throw new Error('run the screenshot tests with scripts/visual.sh');
  }
});

test.beforeEach(async ({ page }) => {
  await page.clock.setFixedTime(new Date());
  page.on('request', (request) => {
    if (request.url().includes('/api/') && !request.url().endsWith('/events')) {
      lastRead.set(page, Date.now());
    }
  });
  // A passthrough to musicbrainz.org, which the fixture's release groups are
  // unknown to. Left alone it fails at a moment that varies from run to run and
  // the release page is caught either side of it; release.spec.ts mocks it too.
  await page.route('**/api/v1/albums/*/editions', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        items: [
          {
            musicbrainzReleaseId: 'f1000000-0000-4000-8000-000000000003',
            title: 'Signal Fires',
            status: 'Official',
            country: 'XW',
            releaseDate: '2023-09-08'
          }
        ]
      })
    })
  );
});

const ids = {
  artist: 'a0000000-0000-4000-8000-000000000002', // Vela Nine: incomplete, a transfer under way
  release: 'b0000000-0000-4000-8000-000000000003', // Signal Fires: 2 of 4 tracks owned
  playlist: '40000000-0000-4000-8000-000000000001' // Late night drive
};

/** Waits until the page has stopped changing: the web font is in, every image
 * has decoded, and nothing says it is still loading. */
async function settle(page: Page) {
  await page.evaluate(() => document.fonts.ready);
  await expect(page.locator('[aria-busy="true"]')).toHaveCount(0);
  // A lazy image below the fold never loads in a window that does not scroll,
  // and the full-page screenshot would catch it half-way.
  await page.evaluate(() => {
    for (const image of Array.from(document.images)) image.loading = 'eager';
  });
  await page.waitForFunction(() => Array.from(document.images).every((image) => image.complete));
  await printsSettled(page);
}

/** When each page last asked the API anything, so settling can wait out a
 * refetch as well as a repaint. The event stream stays open and is left out. */
const lastRead = new WeakMap<Page, number>();

/** The duotone print behind a page header repaints on its own schedule: it
 * waits up to 600 ms for its pictures, paints in a frame with room in it, and
 * paints again when a refetch hands it covers or inks. This polls a small copy
 * of every print canvas until it has not changed, and the API has not been
 * asked anything, for 1.5 s. */
async function printsSettled(page: Page) {
  let last = '';
  let quietSince = Date.now();
  const deadline = Date.now() + 15_000;
  while (Date.now() < deadline) {
    await page.waitForTimeout(250);
    const now = await page.evaluate(() => {
      const small = document.createElement('canvas');
      small.width = 32;
      small.height = 32;
      const g = small.getContext('2d', { willReadFrequently: true })!;
      return Array.from(document.querySelectorAll<HTMLCanvasElement>('.print canvas'))
        .map((canvas) => {
          if (!canvas.width || !canvas.height) return `${canvas.style.zIndex}:empty`;
          g.clearRect(0, 0, 32, 32);
          g.drawImage(canvas, 0, 0, 32, 32);
          const pixels = g.getImageData(0, 0, 32, 32).data.join(',');
          return `${canvas.style.zIndex}:${canvas.width}x${canvas.height}:${pixels}`;
        })
        .join('|');
    });
    if (now !== last) quietSince = Date.now();
    last = now;
    const since = Math.max(quietSince, lastRead.get(page) ?? 0);
    if (Date.now() - since >= 1500) return;
  }
}

async function shoot(page: Page, name: string, mask: Locator[] = []) {
  await settle(page);
  await expect(page).toHaveScreenshot(`${name}.png`, { fullPage: true, mask });
}

/** What changes with the calendar rather than with the code: the fixture's rows
 * are hours and days before now(), so a chart bucketed by day or month, and a
 * date written out, read differently tomorrow. These are painted over. */
function calendar(page: Page): Locator[] {
  const downloaded = page
    .locator('section[data-panel]')
    .filter({ has: page.getByRole('heading', { name: 'Downloaded' }) });
  return [
    downloaded.locator('svg'),
    downloaded.locator('svg + div'),
    page.getByLabel('Library files over the last year'),
    page.getByText(/^updated \d/)
  ];
}

const pages: {
  name: string;
  path: string;
  ready: (page: Page) => Promise<void>;
  /** More to paint over than the calendar. */
  mask?: (page: Page) => Locator[];
}[] = [
  {
    name: 'overview',
    path: '/',
    ready: (page) => expect(page.getByText('No listens yet')).toHaveCount(6)
  },
  {
    name: 'artists',
    path: '/artists',
    ready: (page) =>
      expect(
        page.getByRole('link', {
          name: new RegExp(seeded.artists.complete.name)
        })
      ).toBeVisible()
  },
  {
    name: 'artist',
    path: `/artists/${ids.artist}`,
    ready: (page) =>
      expect(
        page.getByRole('heading', {
          level: 1,
          name: seeded.artists.incomplete.name
        })
      ).toBeVisible()
  },
  {
    name: 'release',
    path: `/releases/${ids.release}`,
    // The sources below the track list arrive in a second read, after the
    // heading, and lengthen the page when they do.
    ready: (page) => expect(page.getByText('1 open request')).toBeVisible(),
  },
  {
    name: 'playlists',
    path: '/playlists',
    ready: (page) => expect(page.getByText(seeded.playlist.name).first()).toBeVisible()
  },
  {
    name: 'playlist',
    path: `/playlists/${ids.playlist}`,
    ready: (page) =>
      expect(page.getByRole('heading', { level: 1, name: seeded.playlist.name })).toBeVisible()
  },
  {
    name: 'downloads',
    path: '/downloads',
    ready: (page) => expect(page.getByText(seeded.download.username).first()).toBeVisible()
  },
  {
    name: 'downloads-wanted',
    path: '/downloads?tab=wanted',
    ready: (page) => expect(page.getByText(seeded.wanted.looking.name).first()).toBeVisible()
  },
  {
    name: 'review',
    path: '/review',
    ready: (page) => expect(page.getByRole('heading', { level: 1, name: 'Review' })).toBeVisible()
  },
  {
    name: 'library',
    path: '/library',
    ready: (page) => expect(page.getByRole('heading', { level: 1, name: 'Library' })).toBeVisible()
  },
  {
    name: 'settings',
    path: '/settings/sources',
    ready: (page) => expect(page.getByRole('heading', { level: 1, name: 'Settings' })).toBeVisible()
  }
];

for (const { name, path, ready, mask } of pages) {
  test(`${name} at 1440 px`, async ({ page }) => {
    await page.goto(path);
    await ready(page);
    await shoot(page, name, [...calendar(page), ...(mask?.(page) ?? [])]);
  });
}

// 753 px is the narrowest window the live audit checks, and the width where
// the review bar and the overview grid have broken before.
for (const name of ['overview', 'artist', 'release', 'review', 'downloads']) {
  const { path, ready, mask } = pages.find((p) => p.name === name)!;
  test(`${name} at 753 px`, async ({ page }) => {
    await page.setViewportSize({ width: 753, height: 900 });
    await page.goto(path);
    await ready(page);
    await shoot(page, `${name}-753`, [...calendar(page), ...(mask?.(page) ?? [])]);
  });
}

test('search popup', async ({ page }) => {
  await page.goto('/artists');
  await expect(
    page.getByRole('link', { name: new RegExp(seeded.artists.complete.name) })
  ).toBeVisible();
  // Typing on any page opens the search; nothing has to be focused first.
  await page.keyboard.type('a');
  await expect(
    page.getByRole('link', { name: new RegExp(seeded.artists.complete.name) }).first()
  ).toBeVisible();
  await shoot(page, 'search');
});
