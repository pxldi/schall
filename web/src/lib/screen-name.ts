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
