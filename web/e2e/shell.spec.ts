import { expect, test } from '@playwright/test';

// The shell is the chrome every screen is drawn inside: one 208px mast down
// the left edge holding the wordmark across the top, the seven destinations
// as icon-and-name rows, and search at the foot, beside one full-width
// scrolling column of content. Below the breakpoint five tabs move to a fixed
// bar along the bottom, with the other four destinations in More.
//
// The component tests can drive most of that in a document built in Node.
// What they cannot do is ask a real browser what width it is drawn at, which
// is the one thing that decides which of the two navs a reader can see.

/** The seven, in the order the mast draws them. */
const seven = ['Overview', 'Artists', 'Playlists', 'Downloads', 'Review', 'Library', 'Settings'];
const five = ['Overview', 'Artists', 'Review', 'Search', 'More'];
const four = ['Playlists', 'Downloads', 'Library', 'Settings'];

test.describe('the mast', () => {
  test('names every destination down the left edge on a wide screen', async ({ page }) => {
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

  test('marks the open destination current with the accent edge', async ({ page }) => {
    await page.goto('/review');

    const top = page.getByRole('navigation', { name: 'Sections' });
    await expect(top.getByRole('link', { name: 'Review' })).toHaveAttribute(
      'aria-current',
      'page'
    );
  });

  // The settings categories are listed once, in the section rail beside the
  // content. The mast does not list them a second time.
  test('the settings categories are named once, not on the mast', async ({ page }) => {
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

// A phone: narrower than the breakpoint the mast appears at.
// Five tabs are in the bottom bar; the other four destinations are in More.
test.describe('the phone bar', () => {
  test.use({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true });

  test('names five tabs along the bottom, by icon and label', async ({ page }) => {
    await page.goto('/');

    const bottom = page.getByRole('navigation', { name: 'Sections, compact' });
    for (const name of five) {
      await expect(
        bottom.getByRole(
          name === 'Overview' || name === 'Artists' || name === 'Review' ? 'link' : 'button',
          { name }
        )
      ).toBeVisible();
    }
  });

  test('marks the open destination current', async ({ page }) => {
    await page.goto('/review');

    const bottom = page.getByRole('navigation', { name: 'Sections, compact' });
    await expect(bottom.getByRole('link', { name: 'Review' })).toHaveAttribute(
      'aria-current',
      'page'
    );
  });

  test('opens the other destinations and carries More active after navigation', async ({ page }) => {
    await page.goto('/');

    const bottom = page.getByRole('navigation', { name: 'Sections, compact' });
    await bottom.getByRole('button', { name: 'More' }).click();

    const sheet = page.getByRole('dialog', { name: 'More' });
    for (const name of four) {
      await expect(sheet.getByRole('link', { name })).toBeVisible();
    }
    await expect(bottom.getByRole('button', { name: 'More' })).toHaveAttribute(
      'aria-expanded',
      'true'
    );

    await sheet.getByRole('link', { name: 'Library' }).click();
    await expect(page).toHaveURL(/\/library$/);
    await expect(page.getByRole('dialog', { name: 'More' })).toHaveCount(0);
    await expect(bottom.getByRole('button', { name: 'More' })).toHaveAttribute(
      'aria-current',
      'page'
    );
  });
});
