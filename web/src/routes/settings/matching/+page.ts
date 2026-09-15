import { redirect } from '@sveltejs/kit';

// Matching held one card, verifying imports by audio, and a category for one
// card is not a category. It folded into Library, next to the other settings
// that decide what the collection keeps. This route stays so a bookmark or a
// link built before the fold still lands somewhere.
export function load({ url }) {
  redirect(307, `/settings/library${url.search}`);
}
