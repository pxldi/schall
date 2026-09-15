import { expect, test } from '@playwright/test';
import { seeded } from './seeded';

// The artist grid is where narrowing a list is decided by the database rather
// than in the browser, and where the address bar is meant to be a record of what
// the reader narrowed it to. Neither is visible to a component test: one needs
// the API to answer, and the other needs a reload.

// The root used to redirect here and now holds the overview, so what the root
// does is asserted in overview.spec.ts.

test('a card states how much of that artist the library holds', async ({ page }) => {
  await page.goto('/artists');

  const { complete, incomplete, unfetched } = seeded.artists;
  await expect(
    page.getByTitle(`${complete.name} — ${complete.owned} of ${complete.releases} releases owned`)
  ).toBeVisible();
  await expect(
    page.getByTitle(
      `${incomplete.name} — ${incomplete.owned} of ${incomplete.releases} releases owned`
    )
  ).toBeVisible();
  // An artist nothing has been fetched for has no fraction to state, and saying
  // "0 of 0" would read as a finished answer to a question nobody has asked.
  await expect(page.getByTitle(`${unfetched.name} — discography not fetched`)).toBeVisible();
});

test('the completeness strip counts over the catalogue, not over the page', async ({ page }) => {
  await page.goto('/artists');

  // A group of buttons rather than tabs: nothing here is a tab. A tab owns a
  // panel and answers the arrow keys; this strip narrows one list that is
  // already on the page, and each button says whether it is the one pressed.
  const strip = page.getByRole('group', { name: 'How complete an artist is' });
  await expect(strip.getByRole('button', { name: `All ${seeded.completeness.all}` })).toBeVisible();
  await expect(
    strip.getByRole('button', { name: `Complete ${seeded.completeness.complete}` })
  ).toBeVisible();
  await expect(
    strip.getByRole('button', { name: `Incomplete ${seeded.completeness.incomplete}` })
  ).toBeVisible();
});

test('narrowing to the incomplete artists survives a reload', async ({ page }) => {
  await page.goto('/artists');

  await page
    .getByRole('group', { name: 'How complete an artist is' })
    .getByRole('button', { name: /^Incomplete/ })
    .click();

  const { complete, incomplete } = seeded.artists;
  await expect(page.getByRole('link', { name: new RegExp(incomplete.name) })).toBeVisible();
  await expect(page.getByRole('link', { name: new RegExp(complete.name) })).toHaveCount(0);
  await expect(page).toHaveURL(/completeness=incomplete/);

  // The address bar follows the list rather than decorating it: a reload of the
  // recorded URL has to open the list it named.
  await page.reload();
  await expect(page.getByRole('link', { name: new RegExp(incomplete.name) })).toBeVisible();
  await expect(page.getByRole('link', { name: new RegExp(complete.name) })).toHaveCount(0);
});

test('searching asks the database rather than filtering the page', async ({ page }) => {
  await page.goto('/artists');

  await page.getByRole('textbox', { name: 'Search artists' }).fill('vela');
  await page.keyboard.press('Enter');

  const { complete, incomplete } = seeded.artists;
  await expect(page.getByRole('link', { name: new RegExp(incomplete.name) })).toBeVisible();
  await expect(page.getByRole('link', { name: new RegExp(complete.name) })).toHaveCount(0);
  await expect(page).toHaveURL(/q=vela/);
});

test('scope separates the artists you followed from the ones the library holds', async ({
  page
}) => {
  await page.goto('/artists');

  // Scope is a view chip now, not a native picker: a group of buttons where one
  // is pressed, named by the words the reader sees.
  await page
    .getByRole('group', { name: 'Which artists to show' })
    .getByRole('button', { name: /^In your library/ })
    .click();

  const { held, complete } = seeded.artists;
  await expect(page.getByRole('link', { name: new RegExp(held.name) })).toBeVisible();
  await expect(page.getByRole('link', { name: new RegExp(complete.name) })).toHaveCount(0);
});

// The press that lands while the first list is still on its way. The list query
// is keyed by what was narrowed, so this is a different question from the one in
// flight; keyed by the page alone, the two would share an answer and the reader
// would be left looking at everyone under a scope they had chosen.
test('narrowing while the first list is still in flight asks for the narrowed one', async ({
  page
}) => {
  let releaseFirst = () => {};
  const firstAnswered = new Promise<void>((resolve) => {
    releaseFirst = resolve;
  });
  let firstSeen = false;
  await page.route('**/api/v1/artists?*', async (route) => {
    if (!firstSeen) {
      firstSeen = true;
      await firstAnswered;
    }
    await route.continue();
  });

  const firstRequest = page.waitForRequest('**/api/v1/artists?*');
  await page.goto('/artists');
  await firstRequest;

  await page
    .getByRole('group', { name: 'Which artists to show' })
    .getByRole('button', { name: /^In your library/ })
    .click();
  releaseFirst();

  const { held, complete } = seeded.artists;
  await expect(page.getByRole('link', { name: new RegExp(held.name) })).toBeVisible();
  await expect(page.getByRole('link', { name: new RegExp(complete.name) })).toHaveCount(0);
});

test('an artist page lists the releases the catalogue holds for them', async ({ page }) => {
  await page.goto('/artists');

  const { complete } = seeded.artists;
  await page
    .getByTitle(`${complete.name} — ${complete.owned} of ${complete.releases} releases owned`)
    .click();

  await expect(page).toHaveURL(/\/artists\/[0-9a-f-]{36}$/);
  await expect(page.getByRole('heading', { name: complete.name })).toBeVisible();
  for (const release of seeded.releases.complete) {
    await expect(page.getByText(release, { exact: true }).first()).toBeVisible();
  }
});
