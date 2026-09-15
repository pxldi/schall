import { describe, expect, it } from 'vitest';
import type { AcquiredCopy, AcquisitionTarget, DownloadRequest, ImportEvidence, ReviewItem } from '$lib/api';
import {
  ASKS,
  candidateRow,
  clock,
  groupCopies,
  isCreditOnlyStopped,
  questionsFrom,
  rankCopies,
  tierOf,
  verdict,
  waitingLabel
} from '$lib/review';

function target(overrides: Partial<AcquisitionTarget> = {}): AcquisitionTarget {
  return {
    id: 'target-1',
    origin: 'playlist',
    artist: 'Talk Talk',
    title: 'Ascension Day',
    album: 'Laughing Stock',
    durationMs: 360_000,
    isrc: null,
    recordingId: null,
    status: 'held',
    summary: 'held for review',
    attempts: 1,
    anchorAttempts: 0,
    ...overrides
  };
}

function want(overrides: Partial<ReviewItem> = {}): ReviewItem {
  return {
    kind: 'copies',
    target: target(),
    copies: [],
    candidates: [],
    ruledOut: 0,
    ...overrides
  };
}

const evidence: ImportEvidence = { files: [], unmatchedTracks: [], problems: [] };

function download(overrides: Partial<DownloadRequest> = {}): DownloadRequest {
  return {
    id: 'download-1',
    provider: 'slskd',
    username: 'peer',
    directory: 'Music\\Talk Talk\\Laughing Stock',
    status: 'completed',
    fileCount: 6,
    expectedTrackCount: 6,
    totalSizeBytes: 300_000_000,
    format: 'flac',
    score: 0,
    reasons: [],
    files: [],
    progress: {
      transferCount: 6,
      completedCount: 6,
      failedCount: 0,
      queuedCount: 0,
      transferredBytes: 300_000_000
    },
    startable: false,
    retryableCount: 0,
    requestedAt: '2026-01-01T00:00:00Z',
    startedAt: '2026-01-01T00:01:00Z',
    cancelledAt: null,
    importStatus: 'needs_review',
    importedAt: null,
    importReviews: [],
    importDecisions: [],
    revalidatable: true,
    ...overrides
  };
}

function copy(overrides: Partial<AcquiredCopy> = {}): AcquiredCopy {
  return {
    id: 'copy-1',
    provider: 'slskd',
    username: 'peer',
    path: 'Music\\03 After the Flood.flac',
    name: '03 After the Flood.flac',
    verdict: 'held',
    decidedBy: 'schall',
    summary: 'held',
    decidedAt: '2026-01-01T00:00:00Z',
    ...overrides
  };
}

describe('questionsFrom', () => {
  it('asks nothing when neither source has anything waiting', () => {
    expect(questionsFrom({ wants: [], imports: [] })).toEqual([]);
  });

  it('asks which recording was meant for a want awaiting resolution', () => {
    const questions = questionsFrom({ wants: [want({ kind: 'resolution' })], imports: [] });

    expect(questions[0].kind).toBe('version');
  });

  it('asks whether a copy is the recording for a want awaiting copies', () => {
    const questions = questionsFrom({ wants: [want({ kind: 'copies' })], imports: [] });

    expect(questions[0].kind).toBe('downloaded');
  });

  // A stopped want is the same downloaded question, whichever of the two
  // reasons it stopped for: a settled file already filed, or a disagreement on
  // artist credit alone. The page tells the two apart with `isCreditOnlyStopped`;
  // this list does not need to.
  it('asks the downloaded question for a want that stopped', () => {
    const questions = questionsFrom({ wants: [want({ kind: 'stopped' })], imports: [] });

    expect(questions[0].kind).toBe('downloaded');
  });

  it('skips an import that recorded no evidence', () => {
    const questions = questionsFrom({ wants: [], imports: [download()] });

    expect(questions).toEqual([]);
  });

  it('asks about an import that recorded evidence', () => {
    const questions = questionsFrom({
      wants: [],
      imports: [download({ importEvidence: evidence })]
    });

    expect(questions.map((question) => question.kind)).toEqual(['folder']);
  });

  it('follows each source in turn rather than interleaving them', () => {
    const questions = questionsFrom({
      wants: [want(), want({ kind: 'resolution' })],
      imports: [download({ importEvidence: evidence })]
    });

    expect(questions.map((question) => question.kind)).toEqual(['downloaded', 'version', 'folder']);
  });

  it('names a want with no title Untitled', () => {
    const questions = questionsFrom({ wants: [want({ target: target({ title: '' }) })], imports: [] });

    expect(questions[0].title).toBe('Untitled');
  });

  it('names an import by its last folder when the request names no entry', () => {
    const questions = questionsFrom({
      wants: [],
      imports: [download({ directory: 'Music/Talk Talk/Laughing Stock', importEvidence: evidence })]
    });

    expect(questions[0].title).toBe('Laughing Stock');
  });

  it('describes a want by its artist and album', () => {
    const questions = questionsFrom({
      wants: [want({ target: target({ artist: 'Talk Talk', album: 'Laughing Stock' }) })],
      imports: []
    });

    expect(questions[0].detail).toBe('Talk Talk · Laughing Stock');
  });

  it('names the question after the want it holds', () => {
    const held = want({ target: target({ id: 'shared' }) });

    const questions = questionsFrom({ wants: [held], imports: [] });

    expect(questions[0].id).toBe('want:shared');
    expect(questions[0].want).toBe(held);
  });
});

describe('isCreditOnlyStopped', () => {
  const creditOnly = copy({
    verdict: 'discarded_tags',
    evidence: { name: 'x', agrees: [], differs: ['artist'], problems: [] }
  });

  it('is false for a want still asking about copies', () => {
    expect(isCreditOnlyStopped(want({ kind: 'copies', copies: [creditOnly] }))).toBe(false);
  });

  it('is true for a stopped want whose copies all disagreed on credit alone', () => {
    expect(isCreditOnlyStopped(want({ kind: 'stopped', copies: [creditOnly] }))).toBe(true);
  });

  it('is false for a stopped want with a settled, filed copy', () => {
    const filed = copy({ verdict: 'accepted', libraryFileId: 'file-1' });

    expect(isCreditOnlyStopped(want({ kind: 'stopped', copies: [filed] }))).toBe(false);
  });

  it('is false for a stopped want with no copies at all', () => {
    expect(isCreditOnlyStopped(want({ kind: 'stopped', copies: [] }))).toBe(false);
  });

  it('is false when nothing is asked about', () => {
    expect(isCreditOnlyStopped(undefined)).toBe(false);
  });
});

describe('verdict', () => {
  it('reads a field the grader listed as agreeing as ok', () => {
    const graded = copy({
      evidence: { name: 'x', agrees: ['ISRC'], differs: [], problems: [] }
    });

    expect(verdict(graded, 'ISRC')).toBe('ok');
  });

  it('reads a field the grader listed as differing as bad', () => {
    const graded = copy({
      evidence: { name: 'x', agrees: [], differs: ['title'], problems: [] }
    });

    expect(verdict(graded, 'title')).toBe('bad');
  });

  it('reads a field in neither list as undecided', () => {
    expect(
      verdict(copy({ evidence: { name: 'x', agrees: [], differs: [], problems: [] } }), 'artist')
    ).toBe('none');
  });

  it('reads a verdict carrying a parenthetical as being about that field', () => {
    const graded = copy({
      evidence: { name: 'x', agrees: [], differs: ['duration (3s out)'], problems: [] }
    });

    expect(verdict(graded, 'duration')).toBe('bad');
  });

  it('does not read one field as the verdict on a longer one it prefixes', () => {
    const graded = copy({
      evidence: { name: 'x', agrees: ['title and duration'], differs: [], problems: [] }
    });

    expect(verdict(graded, 'title')).toBe('none');
  });

  it('reads a copy with no evidence at all as undecided', () => {
    expect(verdict(copy(), 'ISRC')).toBe('none');
  });
});

describe('tierOf', () => {
  it('names an agreeing recording id the strongest evidence', () => {
    const graded = copy({
      evidence: { name: 'x', agrees: ['MusicBrainz recording ID'], differs: [], problems: [] }
    });

    expect(tierOf(graded)).toBe('Recording ID');
  });

  it('names an agreeing ISRC above the tags it was read from', () => {
    const graded = copy({
      evidence: {
        name: 'x',
        agrees: ['ISRC'],
        differs: [],
        problems: [],
        observed: { artist: 'Talk Talk', title: 'Ascension Day' }
      }
    });

    expect(tierOf(graded)).toBe('ISRC');
  });

  it('names title and duration together a tier of their own', () => {
    const graded = copy({
      evidence: { name: 'x', agrees: ['title', 'duration'], differs: [], problems: [] }
    });

    expect(tierOf(graded)).toBe('Title + duration');
  });

  it('does not name a title agreeing alone as title and duration', () => {
    const graded = copy({
      evidence: {
        name: 'x',
        agrees: ['title'],
        differs: [],
        problems: [],
        observed: { title: 'Ascension Day' }
      }
    });

    expect(tierOf(graded)).toBe('Tags only');
  });

  it('names a copy whose tags say nothing as unidentified audio', () => {
    const graded = copy({ evidence: { name: 'x', agrees: [], differs: [], problems: [] } });

    expect(tierOf(graded)).toBe('Audio unidentified');
  });

  it('treats a blank tag as saying nothing', () => {
    const graded = copy({
      evidence: { name: 'x', agrees: [], differs: [], problems: [], observed: { artist: '   ' } }
    });

    expect(tierOf(graded)).toBe('Audio unidentified');
  });
});

describe('rankCopies', () => {
  it('puts the stronger evidence first however good the other copy is', () => {
    const identified = copy({
      id: 'identified',
      evidence: { name: 'x', agrees: ['ISRC'], differs: [], problems: [], bitRate: 128 }
    });
    const pristine = copy({
      id: 'pristine',
      evidence: { name: 'y', agrees: [], differs: [], problems: [], bitRate: 1411 }
    });

    expect(rankCopies([pristine, identified]).map((entry) => entry.id)).toEqual([
      'identified',
      'pristine'
    ]);
  });

  it('puts the better copy first within one tier', () => {
    const lossy = copy({
      id: 'lossy',
      evidence: { name: 'x', agrees: ['ISRC'], differs: [], problems: [], bitRate: 128 }
    });
    const lossless = copy({
      id: 'lossless',
      evidence: { name: 'y', agrees: ['ISRC'], differs: [], problems: [], bitRate: 1411 }
    });

    expect(rankCopies([lossy, lossless]).map((entry) => entry.id)).toEqual(['lossless', 'lossy']);
  });

  it('leaves the list it was given alone', () => {
    const first = copy({ id: 'first' });
    const second = copy({
      id: 'second',
      evidence: { name: 'y', agrees: ['ISRC'], differs: [], problems: [] }
    });
    const given = [first, second];

    rankCopies(given);

    expect(given.map((entry) => entry.id)).toEqual(['first', 'second']);
  });
});

describe('clock', () => {
  it('pads the seconds to two digits', () => {
    expect(clock(65)).toBe('1:05');
  });

  it('drops the part of a second it cannot show', () => {
    expect(clock(65.9)).toBe('1:05');
  });

  it('reads a negative length as no length', () => {
    expect(clock(-1)).toBe('0:00');
  });

  it('reads a length that is not a number as no length', () => {
    expect(clock(Number.NaN)).toBe('0:00');
  });
});

// A length that differs, as both times at once.
//
// It used to be drawn as the copy's own length with the grader's measurement
// beside it — "3:12" and "53s out" — and a reader had to subtract to learn what
// the other side said. Nine characters say the whole disagreement.
describe('a length the grader called a disagreement, on a candidate recording', () => {
  it('reads as both times at once', () => {
    const row = candidateRow(
      {
        recordingId: 'rec-1',
        trackTitle: 'Ascension Day',
        artistName: 'Talk Talk',
        releaseTitle: 'Laughing Stock',
        durationMs: 192_000,
        isrc: null,
        agrees: ['title'],
        differs: ['duration (53 seconds out)']
      } as never,
      245_000
    );

    expect(row.cells.length.value).toBe('3:12 vs 4:05');
  });

  it('stays one time where nothing said the lengths disagree', () => {
    const row = candidateRow(
      {
        recordingId: 'rec-1',
        trackTitle: 'Ascension Day',
        artistName: 'Talk Talk',
        durationMs: 192_000,
        isrc: null,
        agrees: ['duration'],
        differs: []
      } as never,
      245_000
    );

    expect(row.cells.length.value).toBe('3:12');
  });
});

// The owner reversed the question style on 2026-08-14. A sentence with a
// question mark on it was the widest thing on the header and printed eight
// times down the waiting list on the first screen.
describe('what is being decided', () => {
  it('is named rather than asked', () => {
    for (const words of Object.values(ASKS)) {
      expect(words).not.toContain('?');
    }
  });
});

describe('groupCopies', () => {
  const strong = { name: 'x', agrees: ['ISRC'], differs: [], problems: [], bitRate: 1411 };
  const weak = { name: 'y', agrees: [], differs: [], problems: [], bitRate: 128 };

  it('reads the copies of one master as one card, the best of them on it', () => {
    const modest = copy({ id: 'modest', evidence: weak });
    const best = copy({ id: 'best', evidence: strong });

    const grouped = groupCopies([modest, best], [{ copyIds: ['modest', 'best'], rips: 2 }]);

    expect(grouped).toHaveLength(1);
    expect(grouped[0].best.id).toBe('best');
    expect(grouped[0].others.map((entry) => entry.id)).toEqual(['modest']);
    expect(grouped[0].rips).toBe(2);
  });

  it('keeps two masters as two cards', () => {
    const first = copy({ id: 'first', evidence: strong });
    const second = copy({ id: 'second', evidence: strong });

    const grouped = groupCopies(
      [first, second],
      [
        { copyIds: ['first'], rips: 1 },
        { copyIds: ['second'], rips: 1 }
      ]
    );

    expect(grouped.map((entry) => entry.best.id)).toEqual(['first', 'second']);
  });

  it('shows one card per copy when nothing was measured', () => {
    const grouped = groupCopies([copy({ id: 'a' }), copy({ id: 'b' })], undefined);

    expect(grouped.map((entry) => entry.best.id)).toEqual(['a', 'b']);
    expect(grouped.every((entry) => entry.others.length === 0)).toBe(true);
  });

  it('keeps a copy no group named, rather than dropping it', () => {
    const named = copy({ id: 'named' });
    const forgotten = copy({ id: 'forgotten' });

    const grouped = groupCopies([named, forgotten], [{ copyIds: ['named'], rips: 1 }]);

    expect(grouped.map((entry) => entry.best.id)).toEqual(['named', 'forgotten']);
  });

  it('puts the card with the stronger evidence first', () => {
    const modest = copy({ id: 'modest', evidence: weak });
    const identified = copy({ id: 'identified', evidence: strong });

    const grouped = groupCopies(
      [modest, identified],
      [
        { copyIds: ['modest'], rips: 1 },
        { copyIds: ['identified'], rips: 1 }
      ]
    );

    expect(grouped.map((entry) => entry.best.id)).toEqual(['identified', 'modest']);
  });
});

describe('waitingLabel', () => {
  it('names what a want is still waiting for', () => {
    expect(waitingLabel(want({ waitingFor: 'musicbrainz' }))).toBe('Waiting on MusicBrainz');
    expect(waitingLabel(want({ waitingFor: 'library_identity' }))).toBe('Waiting on your library');
  });

  it('says nothing about a want whose evidence is in', () => {
    expect(waitingLabel(want())).toBe('');
    expect(waitingLabel(undefined)).toBe('');
  });
});
