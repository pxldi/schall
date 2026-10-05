import { describe, expect, it } from 'vitest';
import { screenName } from './screen-name';

describe('screenName', () => {
  it.each([
    ['/', 'Overview'],
    ['/artists', 'Artists'],
    ['/artists/talk-talk', 'Artist'],
    ['/artists/labels', 'Labels'],
    ['/artists/labels/4ad', 'Label'],
    ['/releases/laughing-stock', 'Release'],
    ['/releases/sources/12', 'Sources'],
    ['/playlists/weekly', 'Playlist'],
    ['/library?scope=followed&sort=title', 'Library'],
    ['/downloads?view=open', 'Downloads'],
    ['/settings/jobs', 'Settings'],
    ['/somewhere-new', 'Back']
  ])('calls %s %s', (href, name) => {
    expect(screenName(href)).toBe(name);
  });
});
