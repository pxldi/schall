import { afterEach, describe, expect, it, vi } from 'vitest';
import { cleanup, render, screen, within } from '@testing-library/svelte';
import type { ImportDecision, ImportEvidence, ImportFileEvidence } from '$lib/api';
import Evidence from './ImportEvidence.svelte';

// This panel is what a person reads before deciding what a downloaded folder
// belongs to. Its first column says what became of each file, and it used to say
// it with a glyph and a colour and nothing else: no heading, and no name on any
// of the three marks.
//
// Two things were wrong with that. A reader using a screen reader met a column
// with no name, holding cells with no names. And two of the three states were
// the same tick in two different greens — resolved by a person, and agreeing on
// its own — so telling them apart meant seeing the difference between two
// colours. The project's rule is that no state may be identifiable by hue alone.

afterEach(cleanup);

function file(overrides: Partial<ImportFileEvidence> = {}): ImportFileEvidence {
  return {
    name: 'track-01.flac',
    position: '1-1',
    problems: [],
    resolvable: true,
    observed: { artist: 'Vela Nine', title: 'Signal Fires', durationMs: 181_000 },
    expected: {
      artist: 'Vela Nine',
      title: 'Signal Fires',
      durationMs: 181_000,
      trackId: 'track-1',
      discNumber: 1,
      trackNumber: 1
    },
    candidates: [],
    ...overrides
  };
}

function evidence(files: ImportFileEvidence[]): ImportEvidence {
  return { files, unmatchedTracks: [], problems: [] };
}

function draw(files: ImportFileEvidence[], decisions: ImportDecision[] = []) {
  return render(Evidence, {
    props: {
      evidence: evidence(files),
      decisions,
      onresolve: vi.fn(),
      onwithdraw: vi.fn()
    }
  });
}

/** The row for a file, found by the file's own name. */
function row(name: string) {
  return screen.getByText(name).closest('tr')!;
}

describe('ImportEvidence, the column that says what became of a file', () => {
  it('gives the column a name', () => {
    draw([file()]);

    const headings = screen.getAllByRole('columnheader').map((cell) => cell.textContent?.trim());

    expect(headings[0]).toBe('State');
  });

  it('names a file nothing disagreed with', () => {
    draw([file()]);

    expect(within(row('track-01.flac')).getByText('Agrees')).toBeTruthy();
  });

  it('names a file that is the reason review exists', () => {
    draw([file({ problems: ['The title does not agree.'] })]);

    expect(within(row('track-01.flac')).getByText('Disagrees')).toBeTruthy();
  });

  it('names a file a person has already settled', () => {
    draw(
      [file({ problems: ['The title does not agree.'] })],
      [
        {
          id: 'decision-1',
          fileName: 'track-01.flac',
          trackId: 'track-1',
          trackTitle: 'Signal Fires',
          decidedAt: '2026-08-02T10:00:00Z'
        }
      ]
    );

    expect(within(row('track-01.flac')).getByText('Resolved')).toBeTruthy();
  });

  // The point of the fix, said as a test rather than as a comment: the three
  // states are three different words, so none of them needs a colour to be
  // read. Two of them used to be one tick in two greens.
  it('tells the three states apart without anybody seeing a colour', () => {
    const { unmount } = draw([file()]);
    const agreeing = within(row('track-01.flac')).getByText('Agrees').textContent;
    unmount();

    draw(
      [file({ problems: ['The title does not agree.'] })],
      [
        {
          id: 'decision-1',
          fileName: 'track-01.flac',
          trackId: 'track-1',
          trackTitle: 'Signal Fires',
          decidedAt: '2026-08-02T10:00:00Z'
        }
      ]
    );
    const resolved = within(row('track-01.flac')).getByText('Resolved').textContent;

    expect(agreeing).not.toBe(resolved);
  });
});
