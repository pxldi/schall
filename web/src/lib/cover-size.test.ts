import { describe, expect, it } from 'vitest';
import { coverMissing, coverSrc, markCoverMissing } from './cover-size';

describe('the address of a cover', () => {
  it('escapes the album id', () => {
    expect(coverSrc('a/b')).toBe('/api/v1/albums/a%2Fb/cover');
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
