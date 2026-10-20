import { expect, test } from '@playwright/test';
import { seeded } from './seeded';

// The library is one page in two shapes — the catalogue it is meant to hold, and
// the files actually on the disk — and the second one is mounted only once it is
// asked for. What is being checked here is that switching between them keeps the
// address bar honest and that the file list narrows on the server.

test('the catalogue route still lands somewhere', async ({ page }) => {
  await page.goto('/releases');
  await expect(page).toHaveURL(/\/library/);
  // No heading: the mast highlight and the active view chip say where this is.
  await expect(page).toHaveTitle(/Library/);
  await expect(page.getByRole('button', { name: /^Releases/ })).toHaveAttribute(
    'aria-pressed',
    'true'
  );
});

test('the releases view lists what the catalogue holds', async ({ page }) => {
  await page.goto('/library');

  for (const title of [...seeded.releases.complete, ...seeded.releases.incomplete]) {
    await expect(page.getByText(title, { exact: true }).first()).toBeVisible();
  }
});

test('the files view counts what is on the disk', async ({ page }) => {
  await page.goto('/library?view=files');

  // The chip counts the whole pile; pressing it narrows to it, which the next
  // test covers. What is checked here is that the figure is the fixture's own.
  const all = page.getByRole('button', { name: /^All/ });
  await expect(all).toContainText(String(seeded.files.total));
  const unidentified = page.getByRole('button', { name: /^Unidentified/ });
  await expect(unidentified).toContainText(
    String(seeded.files.needsReview + seeded.files.conflict + seeded.files.ambiguous)
  );

  // The music folder the fixture scanned, stated rather than assumed. Exactly
  // "/music": every seeded file's path starts with it, and a substring match
  // would find seventeen of them. It sits behind the folder count, which opens
  // the disclosure that lists it.
  await page.getByRole('button', { name: /^\d+ folders?$/ }).click();
  await expect(page.getByTitle('/music', { exact: true })).toBeVisible();
});

test('narrowing the files by identity state asks the API', async ({ page }) => {
  await page.goto('/library?view=files');

  // A row states its title and carries its path, which is what tells two copies
  // of one recording apart.
  const owned = page.getByTitle('/music/Aurora Lake/2024 - Glass Harbour/01 Low Tide Signal.flac');
  const localOnly = page.getByTitle('/music/Unsorted/rehearsal room, second night.flac');
  await expect(owned).toBeVisible();

  // The picker is a native `<select>` styled to a pill, so its choice is made
  // the way any select's is: by the option's own name.
  await page.getByLabel('Filter by identity state').selectOption({ label: 'Local only' });

  await expect(localOnly).toBeVisible();
  await expect(owned).toHaveCount(0);
});

test('a release page states which edition it was ingested from and why', async ({ page }) => {
  await page.goto('/library');
  await page.getByText('Harrow Bell', { exact: true }).first().click();

  await expect(page).toHaveURL(/\/releases\/[0-9a-f-]{36}$/);
  await expect(page.getByRole('heading', { name: 'Harrow Bell' })).toBeVisible();
  // Which pressing this is, and why that one, is behind "Change": choosing
  // between pressings is a comparison and the full block list still holds it,
  // but most visits never question which pressing this is.
  //
  // The pressing in use is drawn from the release's own facts, so this holds
  // whether or not the list of the others reached musicbrainz.org — which it
  // does not here, because the fixture names no release group MusicBrainz
  // knows.
  await page.getByRole('button', { name: 'Change' }).click();
  await expect(
    page.getByText('chosen by hand: the digital edition matches the library')
  ).toBeVisible();
  // Its three tracks, all of them owned.
  for (const track of ['Harrow', 'Bell Tower', 'Evensong']) {
    await expect(page.getByText(track, { exact: true }).first()).toBeVisible();
  }
});

// How the library's tables are drawn. Two things here cannot be checked without
// a browser: what size the type actually comes out at, and whether the column
// names stay put while the rows run under them. Both are computed by a layout
// engine, and neither is visible in the markup.

/** The table on whichever library view is showing. The other two views are in
 *  the page and hidden, so the visible one is the one being asked about. */
function shownTable(page: import('@playwright/test').Page) {
  return page.locator('table:visible');
}

test('the column names stay put while the rows run under them', async ({ page }) => {
  await page.goto('/library?view=files');
  const table = shownTable(page);
  await expect(table.locator('tbody tr').first()).toBeVisible();

  // The table scrolls inside its own box rather than on the page. That is what
  // the column names are held against: a header sticks to the nearest thing
  // that scrolls, and if that thing is the whole document it slides away with
  // the rows.
  const box = table.locator('xpath=..');
  const header = table.locator('thead');
  const before = await header.boundingBox();
  const firstRowBefore = await table.locator('tbody tr').first().boundingBox();

  await box.evaluate((node) => node.scrollTo(0, 240));
  const scrolled = await box.evaluate((node) => node.scrollTop);
  // A box that did not move proves nothing about a header that did not move.
  expect(scrolled).toBeGreaterThan(0);

  const after = await header.boundingBox();
  const firstRowAfter = await table.locator('tbody tr').first().boundingBox();

  expect(Math.round(after!.y)).toBe(Math.round(before!.y));
  expect(firstRowAfter!.y).toBeLessThan(firstRowBefore!.y);
});

