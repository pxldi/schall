import { describe, expect, it } from 'vitest';
import { coverMissing, markCoverMissing, printPictureWidth } from './cover-size';

// A print softens its covers on a canvas half its size, so it asks for the
// smallest stored width that still covers what it draws.
describe('the width a print asks its covers at', () => {
  it('is 128 for a strip of five across a wide page', () => {
    expect(printPictureWidth('strip', 5, 1200, 144)).toBe(128);
  });

  it('is 256 for a strip whose tiles are wider than 128', () => {
    expect(printPictureWidth('strip', 2, 1000, 144)).toBe(256);
  });

  it('is 128 for a card', () => {
    expect(printPictureWidth('single', 1, 220, 180)).toBe(128);
  });

  it('is the height of a wide hero, which draws its cover whole', () => {
    expect(printPictureWidth('single', 2, 1400, 240)).toBe(256);
  });

  it('is the stored picture when a hero is taller than 256', () => {
    expect(printPictureWidth('single', 1, 1400, 320)).toBeUndefined();
  });
});

describe('a missing cover', () => {
  it('is known missing at every size once one size failed', () => {
    markCoverMissing('/api/v1/albums/gone/cover?size=128');
    expect(coverMissing('/api/v1/albums/gone/cover')).toBe(true);
    expect(coverMissing('/api/v1/albums/gone/cover?size=256')).toBe(true);
    expect(coverMissing('/api/v1/albums/here/cover')).toBe(false);
  });
});
