import { clsx, type ClassValue } from 'clsx';
import { extendTailwindMerge } from 'tailwind-merge';
import { replaceState } from '$app/navigation';
import { applicationHasStarted } from './navigation.svelte';
import type { DownloadRequest, LibraryFile, WantedTracks } from './api';

// `cn` joins class names for a component whose caller may override them. It
// does two things in order. `clsx` drops the ones that are switched off, and
// `tailwind-merge` settles the arguments: given two utilities that set the same
// property, the last one wins, so `<Button class="px-4">` beats the padding the
// button writes for itself. Every component under `lib/components/ui/` is built
// on it.
//
// Settling an argument means knowing which utilities are about the same
// property, and tailwind-merge knows that from a table of Tailwind's own class
// names. `text-` is on that table twice: once for a size and once for a colour.
// It tells them apart by looking at what follows — `text-sm` and `text-lg` are
// sizes because `sm` and `lg` are sizes it knows, and anything else is read as a
// colour.
//
// Schall names its type steps `text-dense-body`, `text-quiet-lead` and so on.
// None of those is a size tailwind-merge has heard of, so all of them were read
// as colours — and a colour written before another colour is a colour that
// loses. `cn('text-dense-body', 'text-ink-3')` returned `text-ink-3` alone. The
// class was not overridden or warned about; it was deleted, and the element drew
// at whatever size it inherited. Every component in `lib/components/ui/` that
// set a size and a colour together lost the size, silently, at every call site.
//
// The two lists below put the type steps back on the table as font sizes, which
// is what they are. `text-ink-3` is still a colour and still settles against
// other colours; `text-dense-body` now settles against other sizes, and the two
// stop arguing with each other because they are no longer the same property.
//
// A new step has to be added here as well as to `styles.css`. That is the cost
// of naming sizes in words instead of in Tailwind's letters, and it is worth
// paying: `dense` and `quiet` say which reader a step is for, which `sm` and
// `lg` cannot.
const typeSteps = {
  dense: ['micro', 'meta', 'body', 'lead', 'display'],
  quiet: ['meta', 'body', 'lead', 'display']
};

const twMerge = extendTailwindMerge({
  // `extend` and not `override`: Tailwind's own `text-sm` and `text-lg` are
  // still written at call sites across the application, and overriding the
  // group would take them off the table to make room for ours.
  extend: {
    classGroups: {
      'font-size': [
        {
          text: [
            ...typeSteps.dense.map((step) => `dense-${step}`),
            ...typeSteps.quiet.map((step) => `quiet-${step}`),
            // The four names the type steps were called before the scales were
            // split in two. They are aliases in `styles.css` and are still
            // written at call sites across the application, so they belong on
            // the same list for the same reason.
            'micro',
            'meta',
            'body',
            'lead'
          ]
        }
      ]
    }
  }
});

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}

// Has the reader asked for less motion? A stylesheet answers this for itself,
// but anything JavaScript times — a transition duration handed to Svelte — has
// to ask. Read at the moment it matters rather than held in a variable, so a
// system setting changed while the tab is open is honoured the next time
// something would have moved, and so that a document without matchMedia (a test
// environment, a server render) simply says no.
export function reducedMotion() {
  return typeof window !== 'undefined' && typeof window.matchMedia === 'function'
    ? window.matchMedia('(prefers-reduced-motion: reduce)').matches
    : false;
}

// downloadName says what a download is for. A request is about a release
// somebody chose a folder for, or about one wanted recording — so this reads the
// release when there is one and the entry that was asked for when there is not,
// rather than leaving a row that says nothing.
export function downloadName(request: DownloadRequest) {
  const artist = request.artistName ?? request.entryArtist ?? '';
  const title = request.albumTitle ?? request.entryTitle ?? '';
  if (artist && title) return `${artist} — ${title}`;
  return artist || title || 'Unnamed download';
}

// wantedSummary says what one press came to, in the order somebody reads it:
// what is new, what was already asked for, and what the catalogue holds no
// recording for and therefore cannot ask for at all. A bare "3 wanted" would
// read as the rest having failed, so every outcome that happened is named.
export function wantedSummary(result: WantedTracks) {
  const parts = [result.wanted === 1 ? '1 track wanted' : `${result.wanted} tracks wanted`];
  if (result.alreadyWanted) parts.push(`${result.alreadyWanted} already wanted`);
  if (result.unresolvable) {
    parts.push(
      result.unresolvable === 1
        ? '1 track has no recording to look for'
        : `${result.unresolvable} tracks have no recording to look for`
    );
  }
  return parts.join(' · ');
}

// The five states a row can be in. They are the chip colours the file lists
// already use, minus the busy one: nothing about a file that is sitting on disk
// is in progress.
export type FileState = 'ok' | 'idle' | 'decide' | 'fail';

// fileStanding says where one file stands and, when it is nowhere, why.
//
// A file the catalogue has no place for is an ordinary outcome rather than a
// failure — matching proves or it refuses — but it is only an honest one if the
// refusal is readable. Which refusal it was is the whole of what this says:
// nothing in the catalogue answers to the file, several things do and nobody may
// choose, or nothing has established what the recording is in the first place.
//
// Placement and identity are two questions, and the placement one is answered
// first because it is the one the reader is looking at the list to ask. A file
// can be identified beyond doubt and still have nowhere in the catalogue to sit,
// which is exactly the case that reads as success — "Identified as …" — until
// the second half of the sentence is said out loud.
export function fileStanding(file: LibraryFile): { text: string; state: FileState } {
  if (file.matchStatus === 'matched') return { text: 'Verified', state: 'ok' };
  // A second file for a track another file already holds. Matching is finished
  // with it — the track is taken and a manual decision is permanent — so it
  // reads as what it is rather than as a question. What is left is whether to
  // keep two copies, which is asked in Review and answered here once somebody
  // has said yes.
  if (file.matchStatus === 'duplicate') {
    return {
      text: file.duplicateKeptAt ? 'Second copy, kept on purpose' : 'Second copy of a track already held',
      state: 'idle'
    };
  }
  // The count stored beside an ambiguous file counts whatever was in contention,
  // which is tracks in one case and rival files in another, so the number is not
  // said. What the reader needs from it is that there is more than one answer and
  // that picking between them is theirs.
  if (file.matchStatus === 'ambiguous') {
    return { text: 'More than one catalogue track fits this file', state: 'decide' };
  }
  switch (file.resolutionStatus) {
    case 'resolved': {
      // Identified, and still unmatched: the catalogue holds no track for that
      // recording, because a track that held it would have been claimed by the
      // identity itself.
      const named = [file.identityTitle, file.identityArtist].filter(Boolean).join(' · ');
      return {
        text: named
          ? `Identified as ${named} — no catalogue track for it`
          : 'Identified — no catalogue track for it',
        state: 'idle'
      };
    }
    case 'needs_review': {
      const count = file.identityCandidateCount;
      if (count < 1) return { text: 'Waiting on a decision about what this is', state: 'decide' };
      return {
        text: count === 1 ? '1 recording could be this file' : `${count} recordings could be this file`,
        state: 'decide'
      };
    }
    case 'conflict':
      return { text: 'The evidence disagrees about what this file is', state: 'fail' };
    // Said the same way whether the grader concluded it or somebody did: either
    // way there is no recording to place the file by, and the file is owned
    // music regardless.
    case 'local_only':
      return { text: 'Owned music with no external identity', state: 'idle' };
    // Named by its page on another service instead of by a recording. Nothing
    // is outstanding: somebody said what this is.
    case 'source': {
      const named = [file.identityTitle, file.identityArtist].filter(Boolean).join(' · ');
      return {
        text: named ? `From SoundCloud — ${named}` : 'From SoundCloud',
        state: 'ok'
      };
    }
    case 'failed':
      return { text: 'MusicBrainz could not be reached', state: 'fail' };
    default:
      return { text: 'Waiting to be looked up', state: 'idle' };
  }
}

// How a file came to answer a track. The value is the database's own word for
// the rule that proved it — `musicbrainz_recording_id`, `isrc`, `manual`,
// `tags` — written the way a column is written: lower case, underscores, and
// nothing capitalised. Drawn straight into the row that read as
// `Matched by musicbrainz recording id`, which spells two proper names wrong
// and one abbreviation wrong in five words.
//
// The named rules are spelled out. Anything else falls back to the underscores
// becoming spaces, which is what the row did before and is still readable for a
// rule nobody has written a name for — a rule added to the Go and not to this
// map degrades to the old wording rather than to nothing.
const matchMethodNames: Record<string, string> = {
  musicbrainz_recording_id: 'MusicBrainz recording ID',
  resolved_identity: 'the identity already on file',
  isrc: 'ISRC',
  manual: 'a manual decision',
  tags: 'tags',
  tag_file: 'tags',
  album_disc_track: 'album, disc and track number',
  album_disc_track_untimed: 'album, disc and track number',
  verified_download: 'a verified download',
  catalogue_search: 'a catalogue search',
  file_id: 'the file it was imported as'
};

export function matchMethodName(method: string | undefined): string {
  if (!method) return 'a rule Schall no longer records';
  return matchMethodNames[method] ?? method.replaceAll('_', ' ');
}

// A browsing page keeps its scope, search, filter, sort and place in the
// address bar, so that reloading it or sending somebody the link opens the list
// that was being read rather than the default one. The four readers below take
// a parameter that may be missing or nonsense — a link is not a form, and one
// carrying a value from an older build should still open a page — and the
// writer records where the reader ended up.

// urlChoice reads a parameter that may only be one of a known set.
export function urlChoice<T extends string>(
  url: URL,
  key: string,
  allowed: readonly T[],
  fallback: T
): T {
  const value = url.searchParams.get(key);
  return allowed.includes(value as T) ? (value as T) : fallback;
}

// urlText reads a free parameter — a search, which is whatever was typed.
export function urlText(url: URL, key: string) {
  return (url.searchParams.get(key) ?? '').trim();
}

// urlCount reads the offset a pager holds. Anything that is not a whole number
// of rows is the first page.
export function urlCount(url: URL, key: string) {
  const value = Number(url.searchParams.get(key));
  return Number.isInteger(value) && value >= 0 ? value : 0;
}

// urlId reads a parameter that names one row for the server to filter by. The
// API refuses an id that is not one — a 422 rather than an empty page, because
// from an application a malformed id is a mistake — so anything that is not an
// id is read here as no filter at all, and the link still opens the page it
// names instead of an error.
const idShape = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

export function urlId(url: URL, key: string) {
  const value = (url.searchParams.get(key) ?? '').trim();
  return idShape.test(value) ? value : '';
}

// keepInUrl writes the state back, leaving out everything already at its
// default so an untouched page keeps a clean address. It replaces rather than
// pushes: this is a record of where the reader is, not a place they navigated
// to, and a back button that undid filter changes one at a time would stop
// being the way off the page.
//
// Every caller does this from an effect, and on the first screen of a session
// that effect runs while SvelteKit is still rendering it — before the router
// has been started, which SvelteKit does only once that render has finished.
// Writing then takes the router's start-up down with it, so the write waits for
// the application to be running. Waiting costs nothing: the effect runs again
// the moment it is, and writes what it would have written then.
export function keepInUrl(
  pathname: string,
  values: Record<string, string | number>,
  defaults: Record<string, string | number>
) {
  if (!applicationHasStarted()) return;
  const params = new URLSearchParams();
  for (const [key, value] of Object.entries(values)) {
    if (value !== defaults[key]) params.set(key, String(value));
  }
  const query = params.toString();
  replaceState(query ? `${pathname}?${query}` : pathname, {});
}

// When something happened or is due, in the compact grammar the jobs view sets
// its row subtitles and its sweep pulse in: `6s ago`, `1h 22m ago`, `in 10s`,
// `in 4m 12s`, `in 37m`.
//
// It is written once here rather than beside the view because a relative time
// is the kind of thing every page eventually needs and every page otherwise
// writes slightly differently. The settings page's health panel still has one
// of its own — it says `never` for a missing value and rounds where this
// truncates — and folding it in would change that panel's copy without a card
// asking for it.
//
// `now` is a parameter so that a ticking clock can drive re-rendering and a
// test can name the moment it is asking about, rather than either racing the
// wall clock.
export function relativeTime(value: string | null | undefined, now = Date.now()): string {
  if (!value) return '';
  const at = new Date(value).getTime();
  if (!Number.isFinite(at)) return '';
  const seconds = Math.round((at - now) / 1000);
  if (seconds >= 0) return `in ${ahead(seconds)}`;
  // `just now` is already a whole answer; `just now ago` is not one.
  const back = behind(-seconds);
  return back === 'just now' ? back : `${back} ago`;
}

// Backwards never shows seconds beside minutes: how long ago something happened
// is read to decide whether to worry about it, and `12m 41s ago` invites a
// precision the answer does not have.
function behind(seconds: number): string {
  if (seconds < 5) return 'just now';
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return hours * 60 === minutes ? `${hours}h` : `${hours}h ${minutes % 60}m`;
  return `${Math.floor(hours / 24)}d`;
}

// Forwards does, under ten minutes, because a countdown that only moves once a
// minute reads as a stopped one — which is the whole difference this view has
// to carry between a job waiting on purpose and a job that is stuck.
function ahead(seconds: number): string {
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 10) return seconds % 60 === 0 ? `${minutes}m` : `${minutes}m ${seconds % 60}s`;
  if (minutes < 60) return `${minutes}m`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return hours * 60 === minutes ? `${hours}h` : `${hours}h ${minutes % 60}m`;
  return `${Math.floor(hours / 24)}d`;
}

// A span of time written out in words, for a sentence that supplies its own
// preposition — "signed out of Soulseek for `durationInWords(ms)`" — rather
// than one that means "ago". elapsedInWords is this with "ago" appended.
export function durationInWords(ms: number): string {
  const seconds = Math.max(0, Math.round(ms / 1000));
  if (seconds < 60) return `${seconds} seconds`;
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) return minutes === 1 ? '1 minute' : `${minutes} minutes`;
  const hours = Math.round(minutes / 60);
  return hours === 1 ? '1 hour' : `${hours} hours`;
}

// The same elapsed time written out, for the one place it appears inside a
// sentence rather than in a figure column. `4 seconds ago` reads as prose;
// `4s ago` in the middle of a line reads as a stray measurement.
export function elapsedInWords(value: string | null | undefined, now = Date.now()): string {
  if (!value) return 'a moment ago';
  const ms = now - new Date(value).getTime();
  if (Math.round(ms / 1000) < 2) return 'a moment ago';
  return `${durationInWords(ms)} ago`;
}

// A calendar date, for a fact old enough that "how long ago" has stopped being
// the useful answer. `relativeTime` counts in days and never stops, so a
// playlist followed in March reads as `168d ago`, which nobody converts.
//
// The day and the month, and no year: everything Schall dates this way happened
// inside the collection's own recent past, and a year on every line is a column
// of `2026` that says nothing. No slashes either — `8/12/2026` is the twelfth of
// August in one country and the eighth of December in the next, and Schall is
// read in both.
export function calendarDate(value: string | Date | null | undefined): string {
  if (!value) return '';
  const at = value instanceof Date ? value : new Date(value);
  if (!Number.isFinite(at.getTime())) return '';
  return at.toLocaleDateString(undefined, { day: 'numeric', month: 'long' });
}

// The clock time under a relative one, because "7m ago" answers the question
// and the clock time settles the argument.
export function clockTime(value: string | null | undefined): string {
  if (!value) return '';
  const at = new Date(value);
  if (!Number.isFinite(at.getTime())) return '';
  return at.toLocaleTimeString(undefined, {
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
    hour12: false
  });
}

// A track's length in the shape a listing shows it: minutes, a colon, and
// seconds padded to two digits. What is stored is milliseconds, and a file
// whose length nothing recorded draws a dash — `0:00` would be a claim about
// the audio that nobody made.
export function formatDuration(duration?: number) {
  if (!duration) return '—';
  const seconds = Math.round(duration / 1000);
  return `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, '0')}`;
}

export function formatBytes(bytes: number) {
  if (bytes < 1024) return `${bytes} B`;
  const units = ['KB', 'MB', 'GB', 'TB'];
  let value = bytes / 1024;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit++;
  }
  return `${value.toFixed(value >= 10 ? 1 : 2)} ${units[unit]}`;
}

// What kind of audio file this is, read off the end of its name.
//
// The scanner records a file's path, its size, its length and its tags. It does
// not record the codec, the bit depth or the sample rate, so the only place the
// format is written down is the extension somebody's file manager put there.
// That is enough to say FLAC or MP3 and it is not enough to say anything more —
// no `16/44.1`, no bit rate — and saying more from here would be inventing it.
//
// The same rule the duplicates list already follows: `DuplicateCopy.format` is
// the server reading the same extension off the same path.
//
// An empty answer is a file whose name ends in nothing, which is a real file on
// a real disk. The caller draws the absence rather than a guess.
export function fileFormat(path: string): string {
  const name = path.split('/').pop() ?? '';
  const dot = name.lastIndexOf('.');
  if (dot < 1) return '';
  const extension = name.slice(dot + 1);
  // Anything long enough to be a sentence is not an extension. A folder called
  // "Live at the Fillmore. Second night" holding a file with no suffix would
  // otherwise put four words in a column three letters wide.
  if (!/^[a-z0-9]{1,5}$/i.test(extension)) return '';
  return extension.toUpperCase();
}
