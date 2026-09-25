import { expect, test } from '@playwright/test';
import { seeded } from './seeded';

// Recommendations are music the library does not hold, suggested from what the
// connected listening account has played. A background sweep stores the list;
// the Recommended view on the Playlists page reads it and applies the five
// suppression rules as it reads. What is checked here is that the stored list
// reaches the screen, and that a rule the fixture triggers is visibly applied.
//
// Nothing here presses "Not interested" or "Want it". A dismissal is
// permanent, a want is a row the acquisition loop then acts on, and the fixture
// is applied once for the whole run.

test('the recommended view lists what the source suggested', async ({ page }) => {
  await page.goto('/playlists?view=recommended');

  for (const title of seeded.recommendations.shown) {
    await expect(page.getByText(title, { exact: true })).toBeVisible();
  }
});

// A suggestion nobody can act on is a list rather than a tool, so every row
// offers the want as well as the refusal. The press itself is not made here: it
// records a want the acquisition loop then acts on, and the fixture is applied
// once for the whole run.
test('every suggestion offers a way to want it', async ({ page }) => {
  await page.goto('/playlists?view=recommended');
  await expect(page.getByText(seeded.recommendations.shown[0], { exact: true })).toBeVisible();

  await expect(page.getByRole('button', { name: 'Want it' })).toHaveCount(
    seeded.recommendations.shown.length
  );
});

// Saying no is one decision at three widths, and the widest of them takes a
// whole artist off the list for ever. The menu is opened and read here and
// nothing is chosen: a dismissal is permanent and the fixture is applied once
// for the whole run.
test('a suggestion can be refused at the recording, the release or the artist', async ({
  page
}) => {
  await page.goto('/playlists?view=recommended');
  await expect(page.getByText(seeded.recommendations.shown[0], { exact: true })).toBeVisible();

  await page.getByRole('button', { name: 'Not interested' }).first().click();

  const scopes = page.getByRole('menu', { name: 'Not interested' }).getByRole('menuitem');
  await expect(scopes).toHaveCount(3);
  await expect(scopes.nth(0)).toContainText('This recording');
  await expect(scopes.nth(1)).toContainText('Anything from this release');
  await expect(scopes.nth(2)).toContainText('Anything by this artist');

  await page.keyboard.press('Escape');
  await expect(page.getByRole('menu', { name: 'Not interested' })).toHaveCount(0);
});

test('a suggestion the library already holds is not offered', async ({ page }) => {
  await page.goto('/playlists?view=recommended');
  await expect(page.getByText(seeded.recommendations.shown[0], { exact: true })).toBeVisible();

  await expect(page.getByText(seeded.recommendations.heldBack.title, { exact: true })).toHaveCount(
    0
  );
});

test('the view says how many suggestions were held back and why', async ({ page }) => {
  await page.goto('/playlists?view=recommended');

  await expect(
    page.getByText(`${seeded.recommendations.heldBack.count} already in your library`)
  ).toBeVisible();
});

test('the recommended view is reached from the playlists page', async ({ page }) => {
  await page.goto('/playlists');

  await page.getByRole('button', { name: 'Recommended' }).click();

  await expect(page.getByText(seeded.recommendations.shown[0], { exact: true })).toBeVisible();
});

// The view says so when the background sweep has stopped refreshing the list.
// The fixture holds a stored list and an empty job queue, which is an ordinary
// restored database rather than a sweep that stopped, so the line must not
// appear. Saying it here would put a fault on every seeded installation.
test('a stored list with no sweep behind it is not called stale', async ({ page }) => {
  await page.goto('/playlists?view=recommended');
  await expect(page.getByText(seeded.recommendations.shown[0], { exact: true })).toBeVisible();

  await expect(page.getByText('This list stopped refreshing')).toHaveCount(0);
});

// The own engine needs no ListenBrainz account (ADR 0039). The fixture has no
// account and no listens, so the Schall list is readable and says no sweep has
// run.
test('the Schall list is read without a ListenBrainz account', async ({ page }) => {
  await page.goto('/playlists?view=recommended');
  await expect(page.getByText(seeded.recommendations.shown[0], { exact: true })).toBeVisible();

  await page.getByRole('button', { name: 'Schall', exact: true }).click();

  await expect(page.getByText('No sweep has run yet')).toBeVisible();
  await expect(page.getByText(seeded.recommendations.shown[0], { exact: true })).toHaveCount(0);
});

test('the view a reader opened survives a reload', async ({ page }) => {
  await page.goto('/playlists');
  await page.getByRole('button', { name: 'Recommended' }).click();
  await expect(page).toHaveURL(/\?view=recommended$/);

  await page.reload();

  await expect(page.getByText(seeded.recommendations.shown[0], { exact: true })).toBeVisible();
});
