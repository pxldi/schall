import { redirect } from '@sveltejs/kit';

// Settings has no page of its own any more; it has five. Somebody arriving at
// /settings — from a bookmark, from a link built before the categories were
// named — lands on Sources, where the connection status of slskd, Navidrome,
// Spotify and ListenBrainz is the first thing on the page.
//
// The search is carried across rather than dropped. Spotify's consent screen
// sends the browser back with ?spotify= saying how it went, and that notice is
// read on the Sources page. The server addresses that page directly now, so
// this only has to catch a link made before it did — an authorization already
// in flight, or a bookmark somebody kept.
export function load({ url }) {
  redirect(307, `/settings/sources${url.search}`);
}
