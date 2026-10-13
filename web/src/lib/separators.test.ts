import { describe, expect, it } from 'vitest';
import { lineStarts } from '$lib/separators';

describe('lineStarts', () => {
  // "Made by Schall · 108 songs" on one line, "5 h 54 min" and the owned bar
  // wrapped below at 753 px. Every one of them showed a leading dot.
  it('marks the first item and each item that wrapped back to the left', () => {
    expect(lineStarts([13, 120, 13, 13])).toEqual([true, false, true, true]);
  });

  it('marks only the first item of a row that fits on one line', () => {
    expect(lineStarts([13, 120, 210])).toEqual([true, false, false]);
  });
});
