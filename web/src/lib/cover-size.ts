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

/** The address Schall serves an album's cover at. */
export function coverSrc(albumId: string): string {
  return `/api/v1/albums/${encodeURIComponent(albumId)}/cover`;
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
