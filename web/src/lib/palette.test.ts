import { describe, expect, it } from 'vitest';
import type { GlobalSearchResults } from '$lib/api';
import { highlight, minutesSeconds, paletteCategories, paletteRows } from '$lib/palette';

// What the palette shows is decided here rather than in the component, because
// the two rules that matter — a fixed order and an escape that names how many it
// leads to — are read at a glance and would otherwise only be checkable by eye.

function answer(overrides: Partial<GlobalSearchResults> = {}): GlobalSearchResults {
  return {
    query: 'radiohead',
    limit: 5,
    artists: [],
    releases: [],
    tracks: [],
    files: [],
    playlists: [],
    totals: { artists: 0, releases: 0, tracks: 0, files: 0, playlists: 0 },
    ...overrides
  };
}

const artist = {
  id: 'a1',
  name: 'Radiohead',
  sortName: 'Radiohead',
  followed: true,
  fileCount: 148,
  releaseCount: 9
};

const release = {
  id: 'r1',
  title: 'Kid A',
  artistId: 'a1',
  artistName: 'Radiohead',
  albumType: 'album',
  releaseDate: '2000-10-02',
  trackCount: 10,
  ownedTrackCount: 8
};

const track = {
  id: 't1',
  title: 'Idioteque',
  discNumber: 1,
  durationMs: 309_000,
  albumId: 'r1',
  albumTitle: 'Kid A',
  artistId: 'a1',
  artistName: 'Radiohead',
  owned: true
};

const file = {
  id: 'f1',
  path: '/music/radiohead/kid-a/05 Idioteque.flac',
  sizeBytes: 37_748_736,
  matchStatus: 'matched',
  missing: false
};

const playlist = {
  id: 'p1',
  name: 'Late night Radiohead',
  source: 'manual',
  ownerName: '',
  trackCount: 22,
  importedAt: '2026-08-02T09:00:00Z'
};

const everything = answer({
  artists: [artist],
  releases: [release],
  tracks: [track],
  files: [file],
  playlists: [playlist],
  totals: { artists: 3, releases: 24, tracks: 118, files: 148, playlists: 1 }
});

describe('paletteCategories', () => {
  // A palette whose groups move between keystrokes cannot be used by muscle
  // memory, so the order never depends on what answered best.
  it('reads broadest thing to narrowest, always in the same order', () => {
    expect(paletteCategories(everything, 'radiohead').map((group) => group.key)).toEqual([
      'artists',
      'releases',
      'tracks',
      'files',
      'playlists'
    ]);
  });

  // An empty heading is a row of noise between the query and the answer, and
  // four of them would be most of the panel.
  it('leaves out a category that answered with nothing', () => {
    const thin = answer({ files: [file], totals: { ...everything.totals, files: 61 } });

    expect(paletteCategories(thin, 'quietstorm').map((group) => group.key)).toEqual(['files']);
  });

  it('says where the rest of a category is and how many are there', () => {
    const [artists] = paletteCategories(everything, 'radiohead');

    expect(artists.total).toBe(3);
    expect(artists.allLabel).toBe('Artists');
    expect(artists.allHref).toBe('/artists?q=radiohead');
  });

  // A category with nothing more to see has nowhere to escape to.
  it('offers no escape from a category that fits', () => {
    const [playlists] = paletteCategories(
      answer({ playlists: [playlist], totals: { ...everything.totals, playlists: 1 } }),
      'radiohead'
    );

    expect(playlists.allHref).toBeUndefined();
  });

  // A catalogue track has no list of its own; it is read on the release that
  // holds it, so its escape goes where releases are browsed.
  it('escapes out of tracks into the releases list', () => {
    const groups = paletteCategories(everything, 'radiohead');
    const tracks = groups.find((group) => group.key === 'tracks');

    expect(tracks?.allLabel).toBe('Releases');
    expect(tracks?.allHref).toBe('/library?view=releases&q=radiohead');
  });

  it('escapes out of files into the library', () => {
    const groups = paletteCategories(everything, 'radiohead');
    const files = groups.find((group) => group.key === 'files');

    expect(files?.allLabel).toBe('Library');
    expect(files?.allHref).toBe('/library?view=files&q=radiohead');
  });

  it('counts an artist in the files the library holds of them', () => {
    const [artists] = paletteCategories(everything, 'radiohead');

    expect(artists.rows[0].meta).toBe('148 files');
  });

  it('counts a release in the tracks of it that are owned', () => {
    const groups = paletteCategories(everything, 'radiohead');

    expect(groups.find((group) => group.key === 'releases')?.rows[0].meta).toBe('8 / 10');
  });

  it('counts a track in how long it is', () => {
    const groups = paletteCategories(everything, 'radiohead');

    expect(groups.find((group) => group.key === 'tracks')?.rows[0].meta).toBe('5:09');
  });

  it('counts a file in how big it is', () => {
    const groups = paletteCategories(everything, 'radiohead');

    expect(groups.find((group) => group.key === 'files')?.rows[0].meta).toBe('36.0 MB');
  });

  it('counts a playlist in the tracks on it', () => {
    const groups = paletteCategories(everything, 'radiohead');

    expect(groups.find((group) => group.key === 'playlists')?.rows[0].meta).toBe('22 tracks');
  });

  // A file has no page of its own, so the row leads into the library list
  // narrowed to the one path, which reaches exactly this file.
  it('links a file to the library list narrowed to its path', () => {
    const groups = paletteCategories(everything, 'radiohead');

    expect(groups.find((group) => group.key === 'files')?.rows[0].href).toBe(
      `/library?view=files&q=${encodeURIComponent(file.path)}`
    );
  });

  it('links a catalogue track to the release that holds it', () => {
    const groups = paletteCategories(everything, 'radiohead');

    expect(groups.find((group) => group.key === 'tracks')?.rows[0].href).toBe('/releases/r1');
  });

  // The mark is the thing's own state. A release the library does not hold
  // entirely is not waiting on a decision, so it is not marked as one.
  it('marks a release the library holds entirely as owned', () => {
    const complete = answer({
      releases: [{ ...release, ownedTrackCount: 10 }],
      totals: { ...everything.totals, releases: 1 }
    });

    expect(paletteCategories(complete, 'kid a')[0].rows[0].role).toBe('ok');
  });

  it('marks a release the library holds part of as idle', () => {
    const groups = paletteCategories(everything, 'radiohead');

    expect(groups.find((group) => group.key === 'releases')?.rows[0].role).toBe('idle');
  });

  // A file the last scan could not find leads nowhere, so it is the one that
  // reads as a failure rather than as an ordinary row.
  it('marks a file the library has lost as a failure', () => {
    const lost = answer({
      files: [{ ...file, missing: true }],
      totals: { ...everything.totals, files: 1 }
    });

    expect(paletteCategories(lost, 'idioteque')[0].rows[0].role).toBe('fail');
  });

  it('marks a track the library holds as owned', () => {
    const groups = paletteCategories(everything, 'radiohead');

    expect(groups.find((group) => group.key === 'tracks')?.rows[0].role).toBe('ok');
  });

  it('marks a track nothing holds as idle', () => {
    const missing = answer({
      tracks: [{ ...track, owned: false }],
      totals: { ...everything.totals, tracks: 1 }
    });

    expect(paletteCategories(missing, 'idioteque')[0].rows[0].role).toBe('idle');
  });

  it('says how much of a followed artist the library holds', () => {
    const [artists] = paletteCategories(everything, 'radiohead');

    expect(artists.rows[0].subtitle).toBe('Following · 9 releases owned');
  });

  // For an artist the library merely has, being in the library is the whole
  // sentence. It never reads "held": that word names a review decision state.
  it('says of an unfollowed artist only that they are in the library', () => {
    const held = answer({
      artists: [{ ...artist, followed: false }],
      totals: { ...everything.totals, artists: 1 }
    });

    expect(paletteCategories(held, 'radiohead')[0].rows[0].subtitle).toBe(
      'In your library, not followed'
    );
  });

  it('reads a file as its name over the folder it sits in', () => {
    const groups = paletteCategories(everything, 'radiohead');
    const row = groups.find((group) => group.key === 'files')?.rows[0];

    expect(row?.title).toBe('05 Idioteque.flac');
    expect(row?.subtitle).toBe('/music/radiohead/kid-a');
  });
});

describe('paletteRows', () => {
  // The arrow keys move through every result in the panel as one list, over the
  // headings between them: a heading is not a result and cannot be opened.
  it('flattens every category into the one list the keys move through', () => {
    const flat = paletteRows(paletteCategories(everything, 'radiohead'));

    expect(flat.map((entry) => entry.row.id)).toEqual([
      'palette-artists-a1',
      'palette-releases-r1',
      'palette-tracks-t1',
      'palette-files-f1',
      'palette-playlists-p1'
    ]);
  });

  it('keeps the category beside each row, so tab knows where to go', () => {
    const flat = paletteRows(paletteCategories(everything, 'radiohead'));

    expect(flat[2].category.key).toBe('tracks');
  });
});

describe('highlight', () => {
  it('marks the part of a line that answered the query', () => {
    expect(highlight('Late night Radiohead', 'radiohead')).toEqual([
      { text: 'Late night ', hit: false },
      { text: 'Radiohead', hit: true }
    ]);
  });

  it('marks every place the query appears', () => {
    expect(highlight('kid a kid b', 'kid').filter((piece) => piece.hit)).toHaveLength(2);
  });

  // A search box is not a pattern language here either: what was typed is
  // matched as the characters it is.
  it('matches a wildcard character as itself', () => {
    expect(highlight('50% Chance', '%')).toEqual([
      { text: '50', hit: false },
      { text: '%', hit: true },
      { text: ' Chance', hit: false }
    ]);
  });

  it('marks nothing when the query is not in the line', () => {
    expect(highlight('Kid A', 'portishead')).toEqual([{ text: 'Kid A', hit: false }]);
  });
});

describe('minutesSeconds', () => {
  it('writes a length the way every other list in Schall writes one', () => {
    expect(minutesSeconds(251_000)).toBe('4:11');
  });

  // A length nobody knows is not written as 0:00, which would claim silence.
  it('writes nothing for a length nobody knows', () => {
    expect(minutesSeconds(undefined)).toBe('');
  });
});
