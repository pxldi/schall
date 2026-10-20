import { describe, expect, it } from 'vitest';
import { clampInks, contrast, HOUSE_INKS, inksFromPixels, luminance } from './inks';

// A picture as a canvas returns it: RGBA, one entry per pixel.
function picture(...areas: [colour: [number, number, number], pixels: number][]) {
  const data: number[] = [];
  for (const [[r, g, b], n] of areas) for (let i = 0; i < n; i++) data.push(r, g, b, 255);
  return data;
}

describe('clampInks', () => {
  it('keeps the house inks when nothing is given', () => {
    expect(clampInks()).toEqual(clampInks(HOUSE_INKS));
  });

  it('darkens a dark ink and lightens a light ink until text reads on either', () => {
    const { dark, light } = clampInks({ dark: '#6a4a8a', light: '#3a7a9a' });
    expect(luminance(dark)).toBeLessThanOrEqual(0.035);
    expect(luminance(light)).toBeGreaterThanOrEqual(0.42);
    expect(contrast(dark, light)).toBeGreaterThanOrEqual(4.5);
  });

  it('swaps a pair that arrives the wrong way round', () => {
    const { dark, light } = clampInks({ dark: '#ffe0c0', light: '#101010' });
    expect(luminance(dark)).toBeLessThan(luminance(light));
  });
});

describe('inksFromPixels', () => {
  it('takes the most saturated colour as the light ink, even when it is a small part', () => {
    // A black cover with a gold detail prints in gold.
    const inks = inksFromPixels(picture([[10, 10, 12], 900], [[220, 170, 40], 100]))!;
    const [r, g, b] = [1, 3, 5].map((i) => parseInt(inks.light.slice(i, i + 2), 16));
    expect(r).toBeGreaterThan(b);
    expect(g).toBeGreaterThan(b);
    expect(luminance(inks.dark)).toBeLessThanOrEqual(0.035);
  });

  it('falls back to the lightest colour on a grey cover', () => {
    const inks = inksFromPixels(picture([[30, 30, 30], 500], [[200, 200, 200], 500]))!;
    const [r, g, b] = [1, 3, 5].map((i) => parseInt(inks.light.slice(i, i + 2), 16));
    expect(Math.max(r, g, b) - Math.min(r, g, b)).toBeLessThan(6);
    expect(luminance(inks.light)).toBeGreaterThanOrEqual(0.42);
  });

  it('ignores a speck under 2% of the picture', () => {
    const inks = inksFromPixels(picture([[20, 40, 120], 990], [[255, 0, 0], 10]))!;
    expect(inks.palette).toHaveLength(1);
  });

  it('reads nothing from a transparent picture', () => {
    expect(inksFromPixels([0, 0, 0, 0, 0, 0, 0, 0])).toBeUndefined();
  });
});
