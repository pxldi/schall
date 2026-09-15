import { expect, test } from '@playwright/test';

// What a page draws when a request fails. Every failure used to be drawn in the
// words the server sent, and the server writes for somebody reading the Go
// source: "artist not found" is a correct sentence and the wrong one to hand a
// person who is in the middle of something.
//
// Nothing in the seeded database fails, and a component test cannot see this
// either — the sentence is chosen from the status and the problem document, both
// of which are made by the API and read by the browser. So the failure is made
// here, by answering one request with a problem document instead of the answer,
// and the assertion is on what the page then puts on the screen.

test('a failed request is drawn in written words, with the server’s own behind a disclosure', async ({
  page
}) => {
  // The artists list, answered with a refusal the map has a sentence for. The
  // route is intercepted rather than the database changed: the fixture is
  // read-only by rule, and every other test is reading it at the same time.
  await page.route('**/api/v1/artists*', (route) =>
    route.fulfill({
      status: 503,
      contentType: 'application/problem+json',
      body: JSON.stringify({ title: 'library is unavailable', status: 503 })
    })
  );

  await page.goto('/artists');

  // The sentence a reader gets.
  await expect(page.getByText('Schall could not read the library.')).toBeVisible();
  // And not the sentence the server sent, which is a Go sentinel.
  await expect(page.getByText('library is unavailable', { exact: true })).toBeHidden();

  // The server's words are not deleted, they are one press away — which is what
  // makes a bug report possible without making it the first thing read.
  const disclosure = page.getByText('What the server said');
  await expect(disclosure).toBeVisible();
  await disclosure.click();
  await expect(page.getByText('library is unavailable')).toBeVisible();
});

test('a failure nobody wrote a sentence for still reaches written words', async ({ page }) => {
  await page.route('**/api/v1/artists*', (route) =>
    route.fulfill({
      status: 500,
      contentType: 'application/problem+json',
      body: JSON.stringify({ title: 'a reason nobody anticipated', status: 500 })
    })
  );

  await page.goto('/artists');

  await expect(page.getByText('Schall could not finish that.')).toBeVisible();
  await page.getByText('What the server said').click();
  await expect(page.getByText('a reason nobody anticipated')).toBeVisible();
});

// An address that names nothing. Until this test existed there was no
// `+error.svelte` in the project at all, so SvelteKit drew its own default: the
// number `404` and the words `Not Found`, in the browser's own font, outside
// the application frame, with no way back. design-plan 3.6 is the rule it
// broke — nothing is ever a dead end.
test('an address that names nothing is answered inside the application, with a way back', async ({
  page
}) => {
  const response = await page.goto('/nothing-answers-to-this');

  // Still a 404 to anything reading the status. What changes is what a person
  // sees.
  expect(response?.status()).toBe(404);

  // Drawn inside the shell: the rail is on screen, so the reader is still
  // somewhere rather than nowhere.
  await expect(page.getByRole('navigation', { name: 'Sections', exact: true })).toBeVisible();

  await expect(page.getByRole('heading', { name: 'That address is not part of Schall' })).toBeVisible();
  // The address is named, because the ordinary cause is a typed or stale link
  // and the reader cannot check one they are not shown.
  await expect(page.getByText('/nothing-answers-to-this')).toBeVisible();

  // And the way out is a control, not a sentence about pressing Back.
  await page.getByRole('link', { name: 'Go to Overview' }).click();
  await expect(page).toHaveURL(/\/$/);
  await expect(page.getByRole('heading', { name: 'Listens', level: 2 })).toBeVisible();
});
