import { expect, test } from '@playwright/test';
import { seeded } from './seeded';

// A transfer under way and a want list, read from the API and shown on their
// pages. The mast carries no counts (#554), so the pages are where both are
// checked.

test('a transfer under way is on the downloads page', async ({ page }) => {
  await page.goto('/downloads');

  await expect(page).toHaveTitle(/Downloads/);
  await expect(page.getByText(seeded.download.album).first()).toBeVisible();
  await expect(page.getByText(seeded.download.username).first()).toBeVisible();
});

test('the artist a transfer belongs to says so on their card', async ({ page }) => {
  await page.goto('/artists');

  const { incomplete } = seeded.artists;
  await expect(page.getByRole('link', { name: new RegExp(incomplete.name) })).toContainText(
    'downloading'
  );
});

test('a want list states how much of it the library holds', async ({ page }) => {
  await page.goto('/playlists');

  const { name, entries, owned } = seeded.playlist;
  const row = page.locator('li').filter({ hasText: name }).last();
  await expect(page.getByText(name, { exact: true })).toBeVisible();
  await expect(row).toContainText(`${owned} of ${entries}`);
});

// A want between being asked for and either arriving or raising a question is
// on no other screen: Review holds the ones with a question, the transfers list
// the ones with a download. What is checked here is that a reader who pressed
// "Want it" can find out what became of it without knowing where to look.
test('a want that is simply being looked for is on the downloads screen', async ({ page }) => {
  await page.goto('/downloads');

  await page.getByRole('button', { name: /^Wishlist/ }).click();

  await expect(page.getByText(seeded.wanted.looking.name, { exact: true })).toBeVisible();
  // What has been tried for it, which is the other half of the answer.
  await expect(page.getByText(/3 copies looked at/)).toBeVisible();
});

// Stopping is a decision about pursuit, and a decision the user made is theirs
// to take back. Nothing is pressed here: the fixture is applied once for the
// whole run and taking one back would change what the tests beside it read.
test('a want somebody stopped is kept where the stop can be taken back', async ({ page }) => {
  await page.goto('/downloads?tab=wanted');

  await page.getByRole('button', { name: /^Not wanted/ }).click();

  await expect(page.getByText(seeded.wanted.stopped.name, { exact: true })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Look again' })).toBeVisible();
});

test('opening a want list shows what it wants and what it already has', async ({ page }) => {
  await page.goto('/playlists');
  await page.getByText(seeded.playlist.name, { exact: true }).click();

  await expect(page).toHaveURL(/\/playlists\/[0-9a-f-]{36}$/);
  await expect(page.getByRole('heading', { name: seeded.playlist.name })).toBeVisible();
  // Two entries the library holds, two it does not.
  for (const entry of ['Glass', 'Sea Lavender', 'Watchfire', 'Smoke Column']) {
    await expect(page.getByText(entry, { exact: true }).first()).toBeVisible();
  }
});
