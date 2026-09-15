import { expect, test } from '@playwright/test';
import { seeded } from './seeded';

// Review holds three kinds of question now — a want's copies, a want's
// recording, a folder's odd files — and nothing else. The fixture carries one
// of the first two and none of the third, so this file checks the rail's
// counts, each kind's own layout, and the footer each one draws.

test('the rail lists the open wants and the chips count each kind', async ({ page }) => {
  await page.goto('/review');

  const queue = page.getByRole('navigation', { name: 'Review queue' });
  await expect(queue.locator('[data-question]')).toHaveCount(seeded.wants);

  await expect(page.getByRole('button', { name: `All ${seeded.wants}` })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Downloaded 1' })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Version 1' })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Folder 0' })).toBeVisible();
});

test('a downloaded want draws one card per copy, with Skip, Remove from Wishlist, None of these and Accept copy', async ({
  page
}) => {
  await page.goto('/review');

  const queue = page.getByRole('navigation', { name: 'Review queue' });
  await queue.getByRole('button').filter({ hasText: 'Evensong (single edit)' }).click();

  await expect(page.getByRole('heading', { name: 'Evensong (single edit)' })).toBeVisible();
  // Two copies arrived from two peers and neither was identified, so two
  // cards are drawn — the peers themselves are not named on the card.
  await expect(page.getByRole('radio')).toHaveCount(2);
  await expect(page.getByText('Copy 1')).toBeVisible();
  await expect(page.getByText('Copy 2')).toBeVisible();
  await expect(page.getByText('Length', { exact: true }).first()).toBeVisible();
  await expect(page.getByText('Format', { exact: true }).first()).toBeVisible();

  const footer = page.locator('footer');
  await expect(footer.getByRole('button', { name: 'Skip' })).toBeVisible();
  await expect(footer.getByRole('button', { name: 'Remove from Wishlist' })).toBeVisible();
  await expect(footer.getByRole('button', { name: 'None of these' })).toBeVisible();
  await expect(footer.getByRole('button', { name: 'Accept copy' })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Wrong song' })).toBeVisible();
});

test('a version want draws one row per candidate, with Skip, Remove from Wishlist and Use recording', async ({
  page
}) => {
  await page.goto('/review');

  const queue = page.getByRole('navigation', { name: 'Review queue' });
  await queue.getByRole('button').filter({ hasText: 'Watchfire' }).click();

  await expect(page.getByRole('heading', { name: 'Watchfire' })).toBeVisible();
  await expect(page.getByText('Recording', { exact: true })).toBeVisible();
  await expect(page.getByText('Release', { exact: true })).toBeVisible();
  await expect(page.getByText('Fits the entry', { exact: true })).toBeVisible();
  // The two candidates the entry fits.
  await expect(page.getByRole('radio')).toHaveCount(2);
  await expect(page.getByText('Signal Fires', { exact: true })).toBeVisible();
  await expect(page.getByText('Signal Fires (live)', { exact: true })).toBeVisible();

  const footer = page.locator('footer');
  await expect(footer.getByRole('button', { name: 'Skip' })).toBeVisible();
  await expect(footer.getByRole('button', { name: 'Remove from Wishlist' })).toBeVisible();
  await expect(footer.getByRole('button', { name: 'Use recording' })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Wrong song' })).not.toBeVisible();
});

test('the artist a want is waiting on is badged on their card', async ({ page }) => {
  await page.goto('/artists');

  const { waiting } = seeded.artists;
  const card = page.getByRole('link', { name: new RegExp(waiting.name) });
  await expect(card).toContainText('review');
  // And the same artist is what "Needs attention" narrows to.
  await page
    .getByRole('group', { name: 'How complete an artist is' })
    .getByRole('button', { name: /^Needs attention/ })
    .click();
  await expect(page.getByRole('link', { name: new RegExp(waiting.name) })).toBeVisible();
});
