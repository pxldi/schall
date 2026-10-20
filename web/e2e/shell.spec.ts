import { expect, test } from '@playwright/test';

// The shell is the chrome every screen is drawn inside (ADR 0042): a 40px
// strip across the top with the wordmark, the seven destinations each with
// its number key, what is running and search, and a key strip along the foot.
// There is no phone form of it.
//
// The component tests can drive most of that in a document built in Node.
// What they cannot do is ask a real browser what width it is drawn at, which
// is the one thing that decides which of the two navs a reader can see.

/** The seven, in the order the strip draws them. */
const seven = ['Overview', 'Artists', 'Playlists', 'Downloads', 'Review', 'Library', 'Settings'];

test.describe('the top strip', () => {
  test('names every destination across the top on a wide screen', async ({ page }) => {
    await page.goto('/');

    const top = page.getByRole('navigation', { name: 'Sections' });
    for (const name of seven) {
      await expect(top.getByRole('link', { name })).toBeVisible();
    }
  });

  test('the keyboard alone reaches a destination', async ({ page }) => {
    await page.goto('/');

    const top = page.getByRole('navigation', { name: 'Sections' });
    await top.getByRole('link', { name: 'Overview' }).focus();
    await page.keyboard.press('Tab');
    await expect(top.getByRole('link', { name: 'Artists' })).toBeFocused();
    await page.keyboard.press('Enter');
    await expect(page).toHaveURL(/\/artists$/);
  });

  test('marks the open destination current', async ({ page }) => {
    await page.goto('/review');

    const top = page.getByRole('navigation', { name: 'Sections' });
    await expect(top.getByRole('link', { name: 'Review' })).toHaveAttribute(
      'aria-current',
      'page'
    );
  });

  // The settings categories are listed once, in the tab strip under the title.
  // The top strip does not list them a second time.
  test('the settings categories are named once, not on the strip', async ({ page }) => {
    await page.goto('/settings/sources');

    const top = page.getByRole('navigation', { name: 'Sections' });
    await expect(top.getByRole('link', { name: 'Sources' })).toHaveCount(0);
    await expect(top.getByRole('link', { name: 'Settings' })).toHaveAttribute(
      'aria-current',
      'page'
    );
    await expect(page.getByRole('navigation', { name: 'Settings sections' })).toBeVisible();
  });

  // Sources, Library, Automation, Phone and Jobs — grouped by what each card
  // configures rather than by service name. Matching folded into Library;
  // Phone is where a phone is given a token.
  test('the settings categories are listed in order', async ({ page }) => {
    await page.goto('/settings/sources');
    const rail = page.getByRole('navigation', { name: 'Settings sections' });

    await expect(rail.getByRole('link')).toHaveText([
      'Sources',
      'Library',
      'Automation',
      'Phone',
      'Jobs'
    ]);
  });
});

