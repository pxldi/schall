import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { LibraryFile } from '$lib/api';
import {
  calendarDate,
  clockTime,
  durationInWords,
  elapsedInWords,
  fileFormat,
  fileStanding,
  formatDuration,
  matchMethodName,
  relativeTime
} from '$lib/utils';

// Writing the address bar is SvelteKit's to do, and the router is the boundary
// here: what these check is when it is asked and when it is deliberately not.
// The stub also collects navigation callbacks, because starting the application
// is the thing keepInUrl waits for.
const routed = vi.hoisted(() => ({
  replaceState: vi.fn(),
  listeners: [] as ((navigation: unknown) => void)[]
}));

vi.mock('$app/navigation', () => ({
  replaceState: routed.replaceState,
  afterNavigate: (callback: (navigation: unknown) => void) => routed.listeners.push(callback)
}));

let keepInUrl: typeof import('$lib/utils').keepInUrl;
let start: () => void;

beforeEach(async () => {
  vi.resetModules();
  routed.replaceState.mockClear();
  routed.listeners.length = 0;
  ({ keepInUrl } = await import('$lib/utils'));
  const { trackNavigation } = await import('$lib/navigation.svelte');
  trackNavigation();
  start = () => {
    for (const listener of routed.listeners) {
      listener({ type: 'enter', from: null, to: { url: new URL('http://localhost/artists') } });
    }
  };
});

describe('keepInUrl', () => {
  // The callers are effects, and on the first screen of a session that effect
  // runs before SvelteKit has started its router. Writing then throws, and the
  // throw takes the router's start-up down with it — after which no link in the
  // application is intercepted and every navigation is a full page load.
  it('writes nothing until the application has started', () => {
    keepInUrl('/artists', { sort: 'name', offset: 40 }, { sort: 'name', offset: 0 });

    expect(routed.replaceState).not.toHaveBeenCalled();
  });

  it('writes the address the moment the application has started', () => {
    start();

    keepInUrl('/artists', { sort: 'name', offset: 40 }, { sort: 'name', offset: 0 });

    expect(routed.replaceState).toHaveBeenCalledWith('/artists?offset=40', {});
  });

  it('leaves out the values that are already at their default', () => {
    start();

    keepInUrl('/artists', { sort: 'added', offset: 0 }, { sort: 'name', offset: 0 });

    expect(routed.replaceState).toHaveBeenCalledWith('/artists?sort=added', {});
  });

  it('writes a clean address for a page whose values are all at their default', () => {
    start();

    keepInUrl('/downloads', { tab: 'peers' }, { tab: 'peers' });

    expect(routed.replaceState).toHaveBeenCalledWith('/downloads', {});
  });
});

function file(overrides: Partial<LibraryFile> = {}): LibraryFile {
  return {
    id: 'file-1',
    path: '/music/Squarepusher/My Red Hot Car.wav',
    sizeBytes: 42_000_000,
    modifiedAt: '2026-01-01T00:00:00Z',
    matchStatus: 'unmatched',
    matchCandidateCount: 0,
    mappingManual: false,
    resolutionStatus: 'pending',
    identityManual: false,
    identityCandidateCount: 0,
    ...overrides
  };
}

describe('fileStanding', () => {
  it('says a matched file is verified', () => {
    expect(fileStanding(file({ matchStatus: 'matched' }))).toEqual({
      text: 'Verified',
      state: 'ok'
    });
  });

  // The number stored beside an ambiguous file counts whatever was in
  // contention, tracks in one case and rival files in another, so saying it
  // would be saying something that is only sometimes true.
  it('says an ambiguous file has more than one answer, without counting them', () => {
    const standing = fileStanding(file({ matchStatus: 'ambiguous', matchCandidateCount: 3 }));

    expect(standing).toEqual({
      text: 'More than one catalogue track fits this file',
      state: 'decide'
    });
    expect(standing.text).not.toContain('3');
  });

  // The case that reads as success until the second half is said: the providers
  // named the recording, and the catalogue still has nowhere to put it.
  it('says an identified file has no catalogue track for it', () => {
    expect(
      fileStanding(
        file({
          resolutionStatus: 'resolved',
          identityTitle: 'Courtship Unleashed',
          identityArtist: 'Hadone'
        })
      )
    ).toEqual({
      text: 'Identified as Courtship Unleashed · Hadone — no catalogue track for it',
      state: 'idle'
    });
  });

  it('counts the recordings that could be a file nobody has decided about', () => {
    expect(fileStanding(file({ resolutionStatus: 'needs_review', identityCandidateCount: 3 })).text)
      .toBe('3 recordings could be this file');
    expect(fileStanding(file({ resolutionStatus: 'needs_review', identityCandidateCount: 1 })).text)
      .toBe('1 recording could be this file');
  });

  // The grader never records a review without the candidates it is between, so
  // this is the shape that says something went wrong rather than one anybody
  // should see. It still has to read as a sentence: "0 recordings could be this
  // file" is the row claiming the opposite of the state it is in.
  it('does not count a review that has no candidates stored against it', () => {
    expect(fileStanding(file({ resolutionStatus: 'needs_review', identityCandidateCount: 0 }))).toEqual(
      { text: 'Waiting on a decision about what this is', state: 'decide' }
    );
  });

  it('reads a contradiction as one, rather than as a file waiting on something', () => {
    expect(fileStanding(file({ resolutionStatus: 'conflict' }))).toEqual({
      text: 'The evidence disagrees about what this file is',
      state: 'fail'
    });
  });

  // The tagless file: nothing but its audio could name it, nothing has, and it
  // is still the user's music.
  it('says a file with no external identity is owned music all the same', () => {
    expect(fileStanding(file({ resolutionStatus: 'local_only' }))).toEqual({
      text: 'Owned music with no external identity',
      state: 'idle'
    });
  });

  it('separates a file nobody has asked about yet from one nobody could answer', () => {
    expect(fileStanding(file({ resolutionStatus: 'pending' })).text).toBe('Waiting to be looked up');
    expect(fileStanding(file({ resolutionStatus: 'failed' }))).toEqual({
      text: 'MusicBrainz could not be reached',
      state: 'fail'
    });
  });

  it('tells the two kinds of second copy apart', () => {
    expect(fileStanding(file({ matchStatus: 'duplicate' })).text).toBe(
      'Second copy of a track already held'
    );
    expect(
      fileStanding(file({ matchStatus: 'duplicate', duplicateKeptAt: '2026-01-02T00:00:00Z' })).text
    ).toBe('Second copy, kept on purpose');
  });
});

// A relative time is written once and read in two places at once: a subtitle
// that says how long ago something happened, and a countdown to the attempt a
// job is waiting to make. The grammar differs on purpose — backwards never
// shows seconds beside minutes, forwards does under ten minutes — because a
// countdown that only moves once a minute reads as a stopped one, and that is
// the whole difference between a job waiting on purpose and a job that is
// stuck.
describe('relativeTime', () => {
  const at = new Date('2026-08-07T14:22:18Z').getTime();

  function ago(seconds: number) {
    return relativeTime(new Date(at - seconds * 1000).toISOString(), at);
  }

  function until(seconds: number) {
    return relativeTime(new Date(at + seconds * 1000).toISOString(), at);
  }

  it('reads the last few seconds as just now rather than as a figure', () => {
    expect(ago(2)).toBe('just now');
  });

  it('counts back in one unit at a time', () => {
    expect(ago(6)).toBe('6s ago');
    expect(ago(3 * 60)).toBe('3m ago');
    expect(ago(82 * 60)).toBe('1h 22m ago');
    expect(ago(3 * 24 * 3600)).toBe('3d ago');
  });

  it('counts down to the second while the wait is short enough to watch', () => {
    expect(until(10)).toBe('in 10s');
    expect(until(4 * 60 + 12)).toBe('in 4m 12s');
  });

  it('stops counting seconds once the wait is longer than ten minutes', () => {
    expect(until(37 * 60)).toBe('in 37m');
  });

  it('says nothing at all about a time that is not there', () => {
    expect(relativeTime(null, at)).toBe('');
    expect(relativeTime('not a time', at)).toBe('');
  });
});

// The one place an elapsed time appears inside a sentence rather than in a
// figure column. `4s ago` mid-line reads as a stray measurement.
describe('elapsedInWords', () => {
  const at = new Date('2026-08-07T14:22:18Z').getTime();

  it('writes the elapsed time out', () => {
    expect(elapsedInWords(new Date(at - 4000).toISOString(), at)).toBe('4 seconds ago');
    expect(elapsedInWords(new Date(at - 60_000).toISOString(), at)).toBe('1 minute ago');
    expect(elapsedInWords(new Date(at - 12 * 60_000).toISOString(), at)).toBe('12 minutes ago');
  });

  it('has something to say even when it was handed nothing', () => {
    expect(elapsedInWords(null, at)).toBe('a moment ago');
  });
});

// The same buckets, for a sentence that already has its own "for" or "since"
// and does not want a second "ago" tacked on.
describe('durationInWords', () => {
  it('writes the span out without "ago"', () => {
    expect(durationInWords(4000)).toBe('4 seconds');
    expect(durationInWords(60_000)).toBe('1 minute');
    expect(durationInWords(12 * 60_000)).toBe('12 minutes');
    expect(durationInWords(60 * 60_000)).toBe('1 hour');
    expect(durationInWords(2 * 60 * 60_000)).toBe('2 hours');
  });

  it('never goes negative for a moment that has not quite arrived yet', () => {
    expect(durationInWords(-500)).toBe('0 seconds');
  });
});

describe('clockTime', () => {
  it('is empty rather than Invalid Date for a time that is not there', () => {
    expect(clockTime(null)).toBe('');
    expect(clockTime('not a time')).toBe('');
  });
});

// The library table's Format column, and the duplicates list's, read the same
// thing off the same place: the end of the file's name. Nothing stores the
// codec, the bit depth or the sample rate, so this is the whole of what can be
// said, and saying more from here would be inventing it.
describe('fileFormat', () => {
  it('names the kind of file', () => {
    expect(fileFormat('/music/Talk Talk/Laughing Stock/01 Myrrhman.flac')).toBe('FLAC');
    expect(fileFormat('/music/Unsorted/rehearsal.MP3')).toBe('MP3');
  });

  it('says nothing about a file whose name ends in nothing', () => {
    expect(fileFormat('/music/Unsorted/rehearsal')).toBe('');
  });

  // A folder with a full stop in its name, holding a file with no suffix, would
  // otherwise put four words in a column three letters wide.
  it('does not mistake a sentence for an extension', () => {
    expect(fileFormat('/music/Live at the Fillmore. Second night/track one')).toBe('');
  });

  // A name that is only a suffix — the dotfiles a scan can meet — is a hidden
  // file rather than a file of that kind.
  it('does not read a leading dot as an extension', () => {
    expect(fileFormat('/music/.flac')).toBe('');
  });
});

// The database's own word for the rule that proved a match, drawn in a row a
// person reads. The column is written the way a column is written — lower case,
// underscores — and the row it lands in is a sentence.
describe('matchMethodName', () => {
  it('spells the two names and the abbreviation the way they are written', () => {
    expect(matchMethodName('musicbrainz_recording_id')).toBe('MusicBrainz recording ID');
    expect(matchMethodName('isrc')).toBe('ISRC');
  });

  // A rule added to the Go and not to the map still has to read as something.
  // Losing the underscores is what the row did before this function existed, so
  // that is what it falls back to rather than to a blank or to the raw column.
  it('falls back to the words of a rule it has no name for', () => {
    expect(matchMethodName('some_new_rule')).toBe('some new rule');
  });

  it('says what it means when nothing recorded the rule', () => {
    expect(matchMethodName(undefined)).toBe('a rule Schall no longer records');
  });
});

// A date old enough that counting the days has stopped being useful. What it
// must never do is print `8/12/2026`, which is two different days depending on
// where it is read.
describe('calendarDate', () => {
  it('writes the month as a word', () => {
    expect(calendarDate('2026-08-12T11:47:00Z')).toMatch(/[A-Za-z]{3,}/);
    expect(calendarDate('2026-08-12T11:47:00Z')).not.toContain('/');
  });

  it('is empty rather than Invalid Date for a date that is not there', () => {
    expect(calendarDate(null)).toBe('');
    expect(calendarDate('not a date')).toBe('');
  });
});

// A track's length, drawn in a column beside other lengths. The two file lists
// each wrote this out for themselves; it is one function now, so these are the
// cases both of them relied on.
describe('formatDuration', () => {
  it('writes minutes and seconds with the seconds padded', () => {
    expect(formatDuration(125000)).toBe('2:05');
  });

  it('rounds to the nearest second rather than truncating', () => {
    expect(formatDuration(59600)).toBe('1:00');
  });

  it('draws a dash for a file whose length nothing recorded', () => {
    expect(formatDuration(undefined)).toBe('—');
    expect(formatDuration(0)).toBe('—');
  });
});
