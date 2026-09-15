import { redirect } from '@sveltejs/kit';

// The catalogue is a view of the library rather than a page beside it. This
// route stays so a bookmark, a shared link and every back arrow out of a release
// still land somewhere, and it carries the query across so a link to a filtered
// catalogue opens the list it named rather than the default one.
export function load({ url }) {
  redirect(307, `/library${url.search}`);
}
