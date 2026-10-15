/** The widths the API makes a stored cover at on request (`?size=`). */
export const COVER_WIDTHS = [128, 256] as const;

/** The address of a picture at a smaller width, for a picture the API serves.
 *  Any other address is returned as it is. */
export function coverAt(src: string, width: (typeof COVER_WIDTHS)[number]): string {
  if (!src.startsWith('/api/')) return src;
  return `${src}${src.includes('?') ? '&' : '?'}size=${width}`;
}

/** The smallest stored width that covers `needed` pixels, or undefined when
 *  only the full 500 pixel picture does. */
export function widthFor(needed: number): (typeof COVER_WIDTHS)[number] | undefined {
  return COVER_WIDTHS.find((width) => width >= needed);
}

/** The width a print (ADR Duoton) needs its pictures at, for a box of the
 *  given CSS size. A print softens its covers on a half-size canvas, so a
 *  strip tile or a card needs half its width; a wide hero also draws the first
 *  cover whole and sharp, as a square the height of the print. */
export function printPictureWidth(
  mode: 'strip' | 'single',
  count: number,
  width: number,
  height: number
): (typeof COVER_WIDTHS)[number] | undefined {
  if (mode === 'strip') {
    return widthFor(Math.max(Math.ceil(width / 2 / Math.max(count, 1)), Math.ceil(height / 2)));
  }
  return widthFor(width > height * 1.4 ? height : Math.ceil(width / 2));
}

// Pictures the server answered 404 for, by their address without a size. A
// cover with no picture was asked for three times on one page (128, 256 and
// 500 pixels) by the grid, the print and the hero.
const missingCovers = new Set<string>();

function base(src: string): string {
  return src.replace(/[?&]size=\d+$/, '');
}

/** Whether a picture is known to be missing, at any size. */
export function coverMissing(src: string): boolean {
  return missingCovers.has(base(src));
}

/** Remembers that a picture is missing, at every size. */
export function markCoverMissing(src: string): void {
  missingCovers.add(base(src));
}
