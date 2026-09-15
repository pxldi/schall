import { redirect } from '@sveltejs/kit';

// Connections is now Sources: slskd, Navidrome, Spotify and ListenBrainz
// share this address because each has a connect-and-test cycle, not because
// they are all "connections". The route stays so a bookmark, a link left in
// another card, and the server's Spotify callback (which 307s here with
// ?spotify=... appended) still land somewhere.
export function load({ url }) {
  redirect(307, `/settings/sources${url.search}`);
}
