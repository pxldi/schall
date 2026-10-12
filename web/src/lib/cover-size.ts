/** The widths the API makes a stored cover at on request (`?size=`). */
export const COVER_WIDTHS = [128, 256] as const;

/** The address of a picture at a smaller width, for a picture the API serves.
 *  Any other address is returned as it is. */
export function coverAt(src: string, width: (typeof COVER_WIDTHS)[number]): string {
  if (!src.startsWith('/api/')) return src;
  return `${src}${src.includes('?') ? '&' : '?'}size=${width}`;
}
