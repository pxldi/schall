import { describe, expect, it } from 'vitest';
import type { Playlist, PlaylistEntry } from '$lib/api';
import { entryState, oncePerCover, ownedOf } from '$lib/playlists';

// The two things a reader asked out loud about this table: what the difference
// between owned and acquired is, and whether an entry that has said 'resolving'
// for an hour is doing anything. Neither is answerable from the label alone,
// which is what these are about.

function entry(fields: Partial<PlaylistEntry> = {}): PlaylistEntry {
  return {
    id: 'e1',
    position: 1,
    artist: 'BONES',
    title: 'HDMI',
    album: 'PaidProgramming',
    ...fields
  };
}

describe('entryState', () => {
  it('reads music Schall fetched as held, the same as music that was there all along', () => {
    const acquired = entryState(entry({ ownedFileId: 'f1', targetStatus: 'acquired' }));

    expect(acquired.label).toBe('complete');
    expect(acquired.label).toBe(entryState(entry({ ownedFileId: 'f1' })).label);
  });

  it('says acquired only while a proven copy has not reached the library', () => {
    const state = entryState(entry({ targetStatus: 'acquired' }));

    expect(state.label).toBe('importing');
  });

  it('carries the want sentence for an entry that is still unanswered', () => {
    const state = entryState(
      entry({
        targetStatus: 'unresolved',
        targetSummary: 'No recording MusicBrainz knows of matches this entry yet. It will be asked about again.'
      })
    );

    expect(state.note).toBe(
      'No recording MusicBrainz knows of matches this entry yet. It will be asked about again.'
    );
  });

  it('tells an entry nothing has been asked about yet from one nothing was found for', () => {
    const unasked = entryState(
      entry({
        targetStatus: 'unresolved',
        targetSummary: 'Waiting to be resolved to a MusicBrainz recording.'
      })
    );
    const unfound = entryState(
      entry({
        targetStatus: 'unresolved',
        targetSummary: 'No recording MusicBrainz knows of matches this entry yet. It will be asked about again.'
      })
    );

    expect(unasked.label).toBe(unfound.label);
    expect(unasked.note).not.toBe(unfound.note);
  });

  it('says nothing under a row whose music the user has', () => {
    const state = entryState(
      entry({ ownedFileId: 'f1', targetStatus: 'acquired', targetSummary: 'In your library.' })
    );

    expect(state.note).toBe('');
  });

  it('says nothing under an entry that never made a want', () => {
    const state = entryState(entry());

    expect(state.label).toBe('not on wishlist');
    expect(state.note).toBe('');
  });
});

describe('ownedOf', () => {
  it('counts the rows that show as held, so the header cannot contradict them', () => {
    const entries = [
      entry({ id: 'a', ownedFileId: 'f1' }),
      entry({ id: 'b', ownedFileId: 'f2', targetStatus: 'acquired' }),
      entry({ id: 'c', targetStatus: 'unresolved' }),
      entry({ id: 'd' })
    ];

    expect(ownedOf(entries)).toBe(
      entries.filter((row) => entryState(row).label === 'complete').length
    );
    expect(ownedOf(entries)).toBe(2);
  });

  it('reads a want Schall has asked about and is waiting on as waiting', () => {
    const state = entryState(entry({ targetStatus: 'unresolved' }));

    expect(state.label).toBe('waiting');
  });

  it('says why a want holding an unanswered copy needs review', () => {
    const state = entryState(
      entry({
        targetStatus: 'unresolved',
        copyWaiting: true,
        targetSummary: 'No recording MusicBrainz knows of matches this entry yet. It will be asked about again.'
      })
    );

    expect(state.note).toBe('Copy arrived. Listen to decide.');
  });

  it('reads a want holding an unanswered copy as needing review', () => {
    const state = entryState(entry({ targetStatus: 'pending', copyWaiting: true }));

    expect(state.label).toBe('needs review');
  });
});

describe('oncePerCover', () => {
  const list = (id: string, coverReleaseId: string | null) =>
    ({ id, coverReleaseId }) as Playlist;

  it('draws a release two lists open on once', () => {
    const kept = oncePerCover([list('a', 'r1'), list('b', 'r1'), list('c', 'r2')]);

    expect(kept.map((playlist) => playlist.id)).toEqual(['a', 'c']);
  });

  it('keeps every list that has no release yet', () => {
    const kept = oncePerCover([list('a', null), list('b', null)]);

    expect(kept.map((playlist) => playlist.id)).toEqual(['a', 'b']);
  });
});
