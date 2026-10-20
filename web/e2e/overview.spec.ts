import { expect, test } from '@playwright/test';
import { seeded } from './seeded';

// The Overview is the page the address bar lands on: nine sections from one
// read. The fixture has no listening history, so the six listening sections
// collapse to one line and the three library sections draw the seeded
// collection.

test('the root is the overview and no longer sends anybody to the artists page', async ({
  page
}) => {
  await page.goto('/');

  await expect(page).toHaveURL(/\/$/);
  await expect(page.getByRole('link', { name: 'Overview' })).toBeVisible();
  await expect(page.getByRole('heading', { level: 1, name: 'Overview' })).toHaveCount(1);
});

test('the sections are named and the empty listening ones collapse to one line', async ({ page }) => {
  await page.goto('/');

  for (const title of ['Recently added', 'Library', 'Downloaded', 'Listening']) {
    await expect(page.getByRole('heading', { name: title, level: 2 })).toBeVisible();
  }
  await expect(page.getByText('No listens yet')).toHaveCount(1);
  await expect(page.getByText('Listens arrive from Navidrome.')).toBeVisible();
  await expect(page.getByRole('heading', { name: 'Most played', level: 2 })).toHaveCount(0);
});

test('the library section counts the seeded files', async ({ page }) => {
  await page.goto('/');

  const library = page.locator('section').filter({ has: page.getByRole('heading', { name: 'Library', level: 2 }) });
  await expect(library.getByText('files', { exact: true })).toBeVisible();
  await expect(library.getByText(String(seeded.files.total), { exact: true })).toBeVisible();
});
