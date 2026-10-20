import { expect, test } from '@playwright/test';

// A page is printed in its own two inks (ADR Duoton). They have to change in
// the frame the page appears in: an eased change started only after the new
// page's first frame had been drawn, which on a page of covers was a second
// after the page itself.

test("a release's inks are on screen in the first frame that shows the release", async ({ page }) => {
  await page.goto('/');
  await expect(page.getByRole('heading', { level: 1, name: 'Overview' })).toBeVisible();
  const release = '/releases/b0000000-0000-4000-8000-000000000005';
  const inks = await page.evaluate(
    async (id) => (await (await fetch(`/api/v1/albums/${id}`)).json()).inks.dark as string,
    release.split('/').pop()!
  );
  expect(
    await page.evaluate(() => document.documentElement.style.getPropertyValue('--color-duo-dark'))
  ).not.toBe(inks);

  // Every frame until the release's title shows, what the page's dark ink is.
  await page.evaluate(() => {
    const w = window as unknown as { firstFrame?: string };
    const tick = () => {
      if (document.querySelector('h1')?.textContent?.includes('Harrow Bell')) {
        w.firstFrame = getComputedStyle(document.documentElement).getPropertyValue('--color-duo-dark').trim();
        return;
      }
      requestAnimationFrame(tick);
    };
    requestAnimationFrame(tick);
  });
  await page.locator(`a[href="${release}"]`).first().click();
  await expect(page.getByRole('heading', { level: 1, name: 'Harrow Bell' })).toBeVisible();

  const hex = (rgb: string) =>
    '#' +
    (rgb.match(/\d+/g) ?? [])
      .slice(0, 3)
      .map((n) => Number(n).toString(16).padStart(2, '0'))
      .join('');
  const first = await page.evaluate(() => (window as unknown as { firstFrame?: string }).firstFrame ?? '');
  expect(first.startsWith('#') ? first : hex(first)).toBe(inks);
});
