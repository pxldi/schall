import { expect, test } from '@playwright/test';
import { seeded } from './seeded';

// Review holds three kinds of question now — a want's copies, a want's
// recording, a folder's odd files — and nothing else. The fixture carries one
// of the first two and none of the third, so this file checks the rail's
// reasons, each kind's own layout, and the actions each one draws.

test('the rail lists the open wants and counts them by reason', async ({ page }) => {
  await page.goto('/review');

  const queue = page.getByRole('navigation', { name: 'Review queue' });
  await expect(queue.locator('[data-question]')).toHaveCount(seeded.wants);

  await expect(page.getByRole('button', { name: `All ${seeded.wants}` })).toBeVisible();
  await expect(page.getByRole('button', { name: 'No audio check 1' })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Which version 1' })).toBeVisible();
});

test('a downloaded want compares each copy with the wish, with Skip, Wrong song, Remove and None to answer', async ({
  page
}) => {
  await page.goto('/review');

  const queue = page.getByRole('navigation', { name: 'Review queue' });
  await queue.getByRole('button').filter({ hasText: 'Evensong (single edit)' }).click();

  const heading = page.getByRole('heading', { name: 'Evensong (single edit)' });
  await expect(heading).toBeVisible();
  // Two copies arrived from two peers and neither was identified, so the
  // table has two copy columns beside the wish.
  await expect(page.getByRole('radio')).toHaveCount(2);
  await expect(page.getByRole('columnheader', { name: /Wished/ })).toBeVisible();
  await expect(page.getByRole('rowheader', { name: 'Length' })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Accept Copy 1' })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Accept Copy 2' })).toBeVisible();

  const header = page.getByRole('group', { name: 'Decide' });
  await expect(header.getByRole('button', { name: 'None of these' })).toBeVisible();
  await expect(header.getByRole('button', { name: 'Skip' })).toBeVisible();
  await expect(header.getByRole('button', { name: 'Wrong song' })).toBeVisible();
  await expect(header.getByRole('button', { name: 'Remove' })).toBeVisible();
  await expect(page.getByRole('slider', { name: 'Volume' })).toBeVisible();
});

test('a version want draws one row per candidate, with Skip, Remove and Use version to answer', async ({
  page
}) => {
  await page.goto('/review');

  const queue = page.getByRole('navigation', { name: 'Review queue' });
  await queue.getByRole('button').filter({ hasText: 'Watchfire' }).click();

  const heading = page.getByRole('heading', { name: 'Watchfire' });
  await expect(heading).toBeVisible();
  await expect(page.getByText('Recording', { exact: true })).toBeVisible();
  await expect(page.getByText('Release', { exact: true })).toBeVisible();
  await expect(page.getByText('Agrees', { exact: true })).toBeVisible();
  // The two candidates the entry fits.
  await expect(page.getByRole('radio')).toHaveCount(2);
  await expect(page.getByText('Signal Fires', { exact: true })).toBeVisible();
  await expect(page.getByText('Signal Fires (live)', { exact: true })).toBeVisible();

  const header = page.getByRole('group', { name: 'Decide' });
  await expect(header.getByRole('button', { name: 'Use version' })).toBeVisible();
  await expect(header.getByRole('button', { name: 'Skip' })).toBeVisible();
  await expect(header.getByRole('button', { name: 'Remove' })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Wrong song' })).not.toBeVisible();
  // A version question has no copy to play.
  await expect(page.getByRole('slider', { name: 'Volume' })).toHaveCount(0);
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

test('the answers stay on screen under a long queue', async ({ page }) => {
  // A real queue ran to 465 entries, and the rail grew the panel to 8,500 px
  // with the answers at its foot. The seeded queue is short, so the first page
  // is padded with copies of its own entries under new ids.
  await page.route('**/api/v1/review-queue?*', async (route) => {
    const response = await route.fetch();
    const body = await response.json();
    const items = Array.from({ length: 100 }, (_, index) => {
      const item = structuredClone(body.items[index % body.items.length]);
      if (index >= body.items.length) {
        item.target.id = `${item.target.id.slice(0, -4)}${String(index).padStart(4, '0')}`;
      }
      return item;
    });
    await route.fulfill({ response, json: { ...body, items, total: 100 } });
  });
  await page.setViewportSize({ width: 1500, height: 800 });
  await page.goto('/review');

  const queue = page.getByRole('navigation', { name: 'Review queue' });
  await expect.poll(() => queue.locator('[data-question]').count()).toBeGreaterThan(90);

  const skip = page.getByRole('group', { name: 'Decide' }).getByRole('button', { name: 'Skip' });
  await expect(skip).toBeInViewport();
  await page.mouse.wheel(0, 600);
  await expect(skip).toBeInViewport();
  const rail = await queue.boundingBox();
  expect(rail!.height).toBeLessThanOrEqual(800);
});

test('the answers never lie over the comparison in a narrow window', async ({ page }) => {
  await page.setViewportSize({ width: 753, height: 700 });
  await page.goto('/review');
  // The rail's list is hidden below 1024 px; the reason narrows to the one copy question.
  await page.getByRole('button', { name: /^No audio check/ }).click();

  const accept = page.getByRole('button', { name: 'Accept Copy 1' });
  await accept.scrollIntoViewIfNeeded();
  await expect(accept).toBeInViewport();
  const answers = (await page.getByRole('group', { name: 'Decide' }).boundingBox())!;
  const box = (await accept.boundingBox())!;
  // The sticky answer bar never lies over the button it is scrolled to.
  expect(box.y + box.height).toBeLessThanOrEqual(answers.y);
});
