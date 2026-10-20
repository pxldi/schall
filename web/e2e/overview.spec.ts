import { expect, test } from '@playwright/test';
import { seeded } from './seeded';

// The Overview is the page the address bar lands on: nine panels from one
// read. The fixture has no listening history, so the six listening panels say
// so and the three library panels draw the seeded collection.

test('the root is the overview and no longer sends anybody to the artists page', async ({
  page
}) => {
  await page.goto('/');

  await expect(page).toHaveURL(/\/$/);
  await expect(page.getByRole('link', { name: 'Overview' })).toBeVisible();
  await expect(page.getByRole('heading', { level: 1, name: 'Overview' })).toHaveCount(1);
});

test('the nine panels are named', async ({ page }) => {
  await page.goto('/');

  for (const title of [
    'Listens',
    'Most played',
    'Top artists',
    'When you listen',
    'Top albums',
    'Recently played',
    'Downloaded',
    'Library',
    'Recently added'
  ]) {
    await expect(page.getByRole('heading', { name: title, level: 2 })).toBeVisible();
  }
});

test('the library panel counts the seeded files', async ({ page }) => {
  await page.goto('/');

  await expect(page.getByText(/^[\d,.]+ files$/)).toContainText(String(seeded.files.total).slice(0, 1));
  await expect(page.getByText('No listens yet')).toHaveCount(6);
});
