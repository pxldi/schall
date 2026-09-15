import { redirect } from '@sveltejs/kit';

// System held one card, a health readout mixing library facts and connection
// facts under one heading. It is split now: the connection rows moved to
// Sources, the library rows to Library. This route stays only so a link built
// before the split — the button on a stuck-jobs error, a bookmark — still
// lands somewhere rather than 404ing.
export function load({ url }) {
  redirect(307, `/settings/sources${url.search}`);
}
