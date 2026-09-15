import type { GlobalSearchResults } from './api';
import { formatBytes } from './utils';

// What the palette shows, worked out away from the component that draws it.
//
// The palette is a front door to browsing that already exists: every row is a
// link to the page that owns the thing, and nothing here decides anything. A
// row's state mark is the state its own page would draw, read off the fields
// the search already answers with — never a grading invented for the palette.

// The four states a palette row can be in. 'busy' is absent on purpose: nothing
// a search answers with is in progress.
export type PaletteRole = 'ok' | 'idle' | 'decide' | 'fail';

export interface PaletteRow {
  /** The element id the input points at with aria-activedescendant. */
  id: string;
  title: string;
  subtitle: string;
  /** The one figure the row's kind is counted in. Never a second title. */
  meta: string;
  role: PaletteRole;
  href: string;
}

export type PaletteKey = 'artists' | 'releases' | 'tracks' | 'files' | 'playlists';

export interface PaletteCategory {
  key: PaletteKey;
  label: string;
  rows: PaletteRow[];
  /** How many answered, which is more than `rows` whenever the limit bit. */
  total: number;
  /** The page the rest of them are on, and what that page is called. Absent
   * when the category fits: there is nothing more to see. */
  allHref?: string;
  allLabel?: string;
}

// One piece of a title, and whether it is the part that answered the query.
export interface PalettePiece {
  text: string;
  hit: boolean;
}

// Where each kind sends its escape, and what that page is called there. Two
// kinds land on the same page: a catalogue track has no list of its own and is
// read on the release that holds it, which is what the releases view is.
const escapes: Record<PaletteKey, { label: string; path: (query: string) => string }> = {
  artists: { label: 'Artists', path: (query) => `/artists?q=${encodeURIComponent(query)}` },
  releases: {
    label: 'Releases',
    path: (query) => `/library?view=releases&q=${encodeURIComponent(query)}`
  },
  tracks: {
    label: 'Releases',
    path: (query) => `/library?view=releases&q=${encodeURIComponent(query)}`
  },
  files: {
    label: 'Library',
    path: (query) => `/library?view=files&q=${encodeURIComponent(query)}`
  },
  // The playlists page has no search box of its own yet, so the escape goes to
  // the list and carries nothing. It is a short list; the query would have
  // nowhere to land.
  playlists: { label: 'Playlists', path: () => '/playlists' }
};

const headings: Record<PaletteKey, string> = {
  artists: 'Artists',
  releases: 'Releases',
  tracks: 'Tracks',
  files: 'Files',
  playlists: 'Playlists'
};

// minutesSeconds is how long a recording is, in the form every other list in
// Schall writes it. A length nobody knows is not written as 0:00, which would
// claim silence.
export function minutesSeconds(milliseconds?: number) {
  if (!milliseconds || milliseconds < 0) return '';
  const seconds = Math.round(milliseconds / 1000);
  return `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, '0')}`;
}

// The two halves of a path: what the file is called, and where it sits. A file
// has no page of its own, so the row is a link into the library list with the
// whole path as its search — which reaches exactly this one file.
function fileParts(path: string) {
  const cut = path.lastIndexOf('/');
  if (cut < 1) return { name: path, folder: '' };
  return { name: path.slice(cut + 1), folder: path.slice(0, cut) };
}

// A count with the word it is counted in, singular when it is one.
function counted(count: number, noun: string) {
  return `${count} ${count === 1 ? noun : `${noun}s`}`;
}

function dayAndMonth(iso: string | null) {
  if (!iso) return '';
  const when = new Date(iso);
  if (Number.isNaN(when.getTime())) return '';
  return when.toLocaleDateString(undefined, { day: 'numeric', month: 'long' });
}

function joined(...parts: (string | undefined | false)[]) {
  return parts.filter(Boolean).join(' · ');
}

// paletteCategories is the answer in the order the palette reads it: broadest
// thing to narrowest, always the same five, and never reordered by how well
// anything scored. A category that answered with nothing is left out entirely —
// an empty heading is a row of noise between the query and the answer.
export function paletteCategories(
  results: GlobalSearchResults,
  query: string
): PaletteCategory[] {
  const categories: PaletteCategory[] = [
    {
      key: 'artists',
      label: headings.artists,
      total: results.totals.artists,
      rows: results.artists.map((artist) => ({
        id: `palette-artists-${artist.id}`,
        title: artist.name,
        // Followed is the claim the artists page files them under. The count of
        // releases owned is said beside it only for a followed artist: for one
        // the library merely has, being in the library is the whole sentence.
        //
        // Neither line says "held". That word is the review queue's name for a
        // copy nobody could identify either way, and using it here for having
        // something meant one word carried two meanings, one of them a decision.
        subtitle: artist.followed
          ? joined('Following', artist.releaseCount > 0 && counted(artist.releaseCount, 'release') + ' owned')
          : 'In your library, not followed',
        meta: counted(artist.fileCount, 'file'),
        role: artist.followed ? 'ok' : 'idle',
        href: `/artists/${artist.id}`
      }))
    },
    {
      key: 'releases',
      label: headings.releases,
      total: results.totals.releases,
      rows: results.releases.map((release) => ({
        id: `palette-releases-${release.id}`,
        title: release.title,
        subtitle: joined(
          release.artistName,
          release.albumType && capitalised(release.albumType),
          release.releaseDate?.slice(0, 4)
        ),
        meta: `${release.ownedTrackCount} / ${release.trackCount}`,
        // The rule the releases browser reads a release by: owned when every
        // track the library counts is here, and idle otherwise. A release that
        // is merely incomplete is not waiting on a decision.
        role:
          release.trackCount > 0 && release.ownedTrackCount === release.trackCount
            ? 'ok'
            : 'idle',
        href: `/releases/${release.id}`
      }))
    },
    {
      key: 'tracks',
      label: headings.tracks,
      total: results.totals.tracks,
      rows: results.tracks.map((track) => ({
        id: `palette-tracks-${track.id}`,
        title: track.title,
        subtitle: joined(track.artistName, track.albumTitle),
        meta: minutesSeconds(track.durationMs),
        role: track.owned ? 'ok' : 'idle',
        // A catalogue track has no page; it is a line on the release.
        href: `/releases/${track.albumId}`
      }))
    },
    {
      key: 'files',
      label: headings.files,
      total: results.totals.files,
      rows: results.files.map((file) => {
        const { name, folder } = fileParts(file.path);
        return {
          id: `palette-files-${file.id}`,
          title: name,
          subtitle: folder,
          meta: formatBytes(file.sizeBytes),
          role: fileRole(file.matchStatus, file.missing),
          href: `/library?view=files&q=${encodeURIComponent(file.path)}`
        };
      })
    },
    {
      key: 'playlists',
      label: headings.playlists,
      total: results.totals.playlists,
      rows: results.playlists.map((playlist) => ({
        id: `palette-playlists-${playlist.id}`,
        title: playlist.name,
        subtitle: joined(
          playlist.ownerName,
          playlist.importedAt ? `Updated ${dayAndMonth(playlist.importedAt)}` : 'Not imported yet'
        ),
        meta: counted(playlist.trackCount, 'track'),
        // A playlist is not in a state. Nothing is waiting on it and nothing is
        // wrong with it, so it takes the mark for having none.
        role: 'idle',
        href: `/playlists/${playlist.id}`
      }))
    }
  ];

  return categories
    .filter((category) => category.rows.length > 0)
    .map((category) =>
      category.total > category.rows.length
        ? {
            ...category,
            allHref: escapes[category.key].path(query),
            allLabel: escapes[category.key].label
          }
        : category
    );
}

// A file's state, said the way the library list says it, out of the two fields
// a search result carries. A file the last scan could not find is the one that
// leads nowhere, so it is the one that reads as a failure.
function fileRole(matchStatus: string, missing: boolean): PaletteRole {
  if (missing) return 'fail';
  if (matchStatus === 'matched') return 'ok';
  if (matchStatus === 'ambiguous') return 'decide';
  return 'idle';
}

function capitalised(value: string) {
  return value.charAt(0).toUpperCase() + value.slice(1);
}

// paletteRows flattens the panel into the one list the arrow keys move through.
// Headings are not in it: they are not results and cannot be opened.
export function paletteRows(categories: PaletteCategory[]) {
  return categories.flatMap((category) =>
    category.rows.map((row) => ({ row, category }))
  );
}

// highlight splits a line into the part that answered the query and the parts
// that did not. The query is matched literally — it is what somebody typed, not
// a pattern — and case is ignored, the same way the search itself ignores it.
export function highlight(text: string, query: string): PalettePiece[] {
  const needle = query.trim();
  if (!needle) return [{ text, hit: false }];

  const pieces: PalettePiece[] = [];
  const haystack = text.toLowerCase();
  const lowered = needle.toLowerCase();
  let from = 0;

  for (;;) {
    const at = haystack.indexOf(lowered, from);
    if (at < 0) break;
    if (at > from) pieces.push({ text: text.slice(from, at), hit: false });
    pieces.push({ text: text.slice(at, at + needle.length), hit: true });
    from = at + needle.length;
  }
  if (from < text.length) pieces.push({ text: text.slice(from), hit: false });
  return pieces;
}
