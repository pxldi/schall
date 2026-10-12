// What a back button calls the screen at an address. Sections use the names in
// the top bar; one thing in a section is named by its kind, because the button
// has no title to hand and a word fits beside the arrow.
const sections: [RegExp, string][] = [
  [/^\/$/, 'Overview'],
  [/^\/artists\/labels\/[^/]+/, 'Label'],
  [/^\/artists\/labels/, 'Labels'],
  [/^\/artists\/[^/]+/, 'Artist'],
  [/^\/artists/, 'Artists'],
  [/^\/releases\/sources\//, 'Sources'],
  [/^\/releases\/[^/]+/, 'Release'],
  [/^\/releases/, 'Releases'],
  [/^\/playlists\/[^/]+/, 'Playlist'],
  [/^\/playlists/, 'Playlists'],
  [/^\/downloads/, 'Downloads'],
  [/^\/review/, 'Review'],
  [/^\/library/, 'Library'],
  [/^\/settings/, 'Settings']
];

export function screenName(href: string): string {
  const path = href.split(/[?#]/)[0];
  return sections.find(([pattern]) => pattern.test(path))?.[1] ?? 'Back';
}

// The screens that are one thing rather than a section. Their back button
// carries the thing's own name when the tab title gave one.
const things = new Set(['Artist', 'Release', 'Playlist', 'Label']);

/** The back button's word for a screen: its own name from the tab title
 *  ("Kendrick Lamar · Schall") for one artist, release, playlist or label,
 *  otherwise the section's name. */
export function screenLabel(href: string, title: string | null): string {
  const kind = screenName(href);
  if (!title || !things.has(kind)) return kind;
  const name = title.replace(/\s*·\s*Schall$/, '').trim();
  return name && name !== 'Schall' && name !== kind ? name : kind;
}
