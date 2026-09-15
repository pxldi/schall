import { expect, test } from '@playwright/test';

// The page about one record. An edition is one pressing of it — the 2011
// reissue, the Japanese one with the extra track — and Schall works from
// exactly one: the track list on screen, and every want counted against it,
// come from the pressing selected here. The pressing in use names itself in
// one line; every pressing MusicBrainz knows of is a "Change" press away,
// because choosing between them is a comparison and belongs on one screen.
//
// **This spec mocks one endpoint, and it is the only mocked thing in the
// browser suite.** `/api/v1/albums/{id}/editions` is a passthrough to
// musicbrainz.org, and the fixture deliberately names no release group
// MusicBrainz knows, so against real data the endpoint can only fail. Mocking
// it is what lets the browser say whether three pressings are behind "Change"
// without anything else being pressed. Everything else here is the real
// application over the real fixture.

const editions = {
  items: [
    {
      musicbrainzReleaseId: 'f1000000-0000-4000-8000-000000000005',
      title: 'Harrow Bell',
      status: 'Official',
      country: 'XW',
      releaseDate: '2025-02-14'
    },
    {
      musicbrainzReleaseId: 'f1000000-0000-4000-8000-0000000000a1',
      title: 'Harrow Bell (Japanese edition)',
      status: 'Official',
      country: 'JP',
      releaseDate: '2025-03-01'
    },
    {
      // A pressing MusicBrainz knows almost nothing about. Its three facts keep
      // their places and each says it is missing, because a blank in a column
      // of dates reads as a page that failed to draw.
      musicbrainzReleaseId: 'f1000000-0000-4000-8000-0000000000a2',
      title: 'Harrow Bell (promo)'
    }
  ]
};

async function openHarrowBell(page: import('@playwright/test').Page) {
  await page.route('**/api/v1/albums/*/editions', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(editions) })
  );
  await page.goto('/library');
  await page.getByText('Harrow Bell', { exact: true }).first().click();
  await expect(page).toHaveURL(/\/releases\/[0-9a-f-]{36}$/);
}

test('every pressing is behind "Change", not on screen unopened', async ({ page }) => {
  await openHarrowBell(page);

  // Nothing pressed yet: the comparison is fetched (so reopening it is
  // instant) but not on screen.
  await expect(page.locator('[data-edition]').first()).toBeHidden();

  await page.getByRole('button', { name: 'Change' }).click();

  // Now every pressing is, with no further press.
  await expect(page.locator('[data-edition]')).toHaveCount(3);
  await expect(page.locator('[data-edition]').first()).toBeVisible();
  await expect(page.getByText('Harrow Bell (Japanese edition)')).toBeVisible();
  await expect(page.getByText('Harrow Bell (promo)')).toBeVisible();

  // The pressing Schall is working from names itself, and it is the only one
  // that does.
  await expect(page.getByText('Selected')).toHaveCount(1);
  // Every other pressing carries the control that would switch to it.
  await expect(page.getByRole('button', { name: 'Use this' })).toHaveCount(2);
});

test('a pressing MusicBrainz has no facts for says so in each place', async ({ page }) => {
  await openHarrowBell(page);
  await page.getByRole('button', { name: 'Change' }).click();

  const promo = page.locator('[data-edition]').filter({ hasText: 'Harrow Bell (promo)' });
  await expect(promo.getByText('Not recorded')).toHaveCount(3);
});

test('the owned bar counts what the library holds of the record', async ({ page }) => {
  await openHarrowBell(page);

  // The fixture holds all three tracks of this record.
  await expect(page.getByLabel('3 of 3 tracks in your library')).toBeVisible();
});
