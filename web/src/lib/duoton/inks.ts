// The two inks a page is printed in (ADR Duoton), and the arithmetic that keeps
// them legible. Pure functions only: no canvas, no DOM, so they run in tests and
// on the server alike.

/** A page's pair of inks, as #rrggbb. `palette` is the cover's main colours,
 *  most of the picture first, when they are known. */
export interface Inks {
  dark: string;
  light: string;
  palette?: string[];
}

/** The inks a page uses when nothing on it has a cover. */
export const HOUSE_INKS: Inks = { dark: '#13204a', light: '#ffb59e' };

type RGB = [number, number, number];

export function hexToRgb(hex: string): RGB {
  let h = hex.replace('#', '');
  if (h.length === 3) h = [...h].map((c) => c + c).join('');
  const n = parseInt(h, 16);
  return [(n >> 16) & 255, (n >> 8) & 255, n & 255];
}

export function rgbToHex(rgb: readonly number[]): string {
  return (
    '#' +
    rgb
      .map((v) =>
        Math.max(0, Math.min(255, Math.round(v)))
          .toString(16)
          .padStart(2, '0')
      )
      .join('')
  );
}

/** WCAG relative luminance, 0 for black and 1 for white. */
export function luminance(hex: string): number {
  const [r, g, b] = hexToRgb(hex).map((v) => {
    const s = v / 255;
    return s <= 0.03928 ? s / 12.92 : Math.pow((s + 0.055) / 1.055, 2.4);
  });
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
}

export function contrast(a: string, b: string): number {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x);
  return (hi + 0.05) / (lo + 0.05);
}

export function mix(a: string, b: string, t: number): string {
  const x = hexToRgb(a);
  const y = hexToRgb(b);
  return rgbToHex(x.map((v, i) => v + (y[i] - v) * t));
}

/** HSL saturation and lightness, both 0 to 1. */
export function saturationLightness([r, g, b]: RGB): [number, number] {
  const max = Math.max(r, g, b) / 255;
  const min = Math.min(r, g, b) / 255;
  const l = (max + min) / 2;
  if (max === min) return [0, l];
  const d = max - min;
  return [l > 0.5 ? d / (2 - max - min) : d / (max + min), l];
}

const DARK_CEILING = 0.035;
const LIGHT_FLOOR = 0.42;

/** Clamps a pair so text stays legible on any cover: the dark ink is darkened
 *  until its luminance is at most 0.035, the light ink lightened until it is at
 *  least 0.42 (ADR Duoton). The pair is swapped first if it arrives reversed. A
 *  missing ink takes the house one. The clamp is what guarantees contrast, so
 *  nobody has to check covers by hand. */
export function clampInks(inks?: Partial<Inks> | null): Inks {
  let dark = inks?.dark || HOUSE_INKS.dark;
  let light = inks?.light || HOUSE_INKS.light;
  if (luminance(dark) > luminance(light)) [dark, light] = [light, dark];
  for (let n = 0; luminance(dark) > DARK_CEILING && n < 12; n++) dark = mix(dark, '#000000', 0.18);
  for (let n = 0; luminance(light) < LIGHT_FLOOR && n < 12; n++) light = mix(light, '#ffffff', 0.16);
  return inks?.palette ? { dark, light, palette: inks.palette } : { dark, light };
}

interface Cluster {
  sum: [number, number, number];
  count: number;
}

/** Reads a pair of inks out of a cover's pixels (RGBA, as a canvas returns
 *  them). The light ink is the most saturated cover colour with lightness
 *  between 0.22 and 0.9; when none qualifies it is the cover's lightest main
 *  colour. The dark ink is the darkest main colour. Both are then clamped.
 *  Returns undefined when there are no opaque pixels to read. */
export function inksFromPixels(data: ArrayLike<number>): Inks | undefined {
  // Colours are bucketed at 5 bits a channel, then the buckets are merged
  // greedily into at most eight clusters, largest first.
  const buckets = new Map<number, Cluster>();
  let total = 0;
  for (let i = 0; i + 3 < data.length; i += 4) {
    if (data[i + 3] < 128) continue;
    const r = data[i];
    const g = data[i + 1];
    const b = data[i + 2];
    const key = ((r >> 3) << 10) | ((g >> 3) << 5) | (b >> 3);
    const bucket = buckets.get(key) ?? { sum: [0, 0, 0], count: 0 };
    bucket.sum[0] += r;
    bucket.sum[1] += g;
    bucket.sum[2] += b;
    bucket.count++;
    buckets.set(key, bucket);
    total++;
  }
  if (!total) return undefined;

  const clusters: Cluster[] = [];
  const mean = (c: Cluster): RGB => [c.sum[0] / c.count, c.sum[1] / c.count, c.sum[2] / c.count];
  for (const bucket of [...buckets.values()].sort((a, b) => b.count - a.count)) {
    const colour = mean(bucket);
    const near = clusters.find((c) => {
      const m = mean(c);
      return Math.hypot(m[0] - colour[0], m[1] - colour[1], m[2] - colour[2]) < 48;
    });
    if (near) {
      near.sum[0] += bucket.sum[0];
      near.sum[1] += bucket.sum[1];
      near.sum[2] += bucket.sum[2];
      near.count += bucket.count;
    } else if (clusters.length < 8) {
      clusters.push({ sum: [...bucket.sum], count: bucket.count });
    }
  }

  // A colour has to cover 2% of the picture to count, so a speck never inks a page.
  const main = clusters
    .filter((c) => c.count / total >= 0.02)
    .sort((a, b) => b.count - a.count)
    .map((c) => ({ rgb: mean(c), hex: rgbToHex(mean(c)) }));
  if (!main.length) return undefined;

  const vivid = main
    .map((c) => ({ ...c, sl: saturationLightness(c.rgb) }))
    .filter((c) => c.sl[0] >= 0.35 && c.sl[1] >= 0.22 && c.sl[1] <= 0.9)
    .sort((a, b) => b.sl[0] - a.sl[0]);
  const byLuminance = [...main].sort((a, b) => luminance(a.hex) - luminance(b.hex));
  const light = vivid[0]?.hex ?? byLuminance[byLuminance.length - 1].hex;
  const dark = byLuminance.find((c) => c.hex !== light)?.hex ?? mix(light, '#000000', 0.8);

  return clampInks({ dark, light, palette: main.slice(0, 5).map((c) => c.hex) });
}
