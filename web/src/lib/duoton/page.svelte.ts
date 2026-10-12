import { onDestroy } from 'svelte';
import { clampInks, HOUSE_INKS, type Inks } from './inks';
import { inksOfPicture } from './paint';

// What the open page is printed with (ADR Duoton): which covers its print is
// made of, how the print is laid out, how tall it is, and the two inks
// everything else on the page takes. The shell draws the print and the top bar
// from this; a page only says what it wants through `usePagePrint`.

/** A picture in the print. A string is an album id, whose cover Schall serves.
 *  The object form takes any picture address (a recently added track's cover,
 *  an artist photo) or none, plus the seed a generated stand-in is drawn from.
 *  `inks` are the stored inks for that picture when the API has them. */
export type CoverRef = string | { src?: string | null; seed: string; inks?: Inks | null };

export interface PagePrint {
  covers?: CoverRef[];
  /** `strip` lays covers side by side (Overview, Artists, Playlists,
   *  Downloads). `single` prints the first cover large and screens a second
   *  over it (a release, an artist). Default `strip`. */
  mode?: 'strip' | 'single';
  /** The print's height in rem, top bar included. Default 15 for a strip and
   *  22 for a single print. */
  height?: number;
  /** The page's inks. Left out, they come from the first cover that has stored
   *  inks or a picture to read them from, and the house inks when none does. */
  inks?: Inks | null;
}

export interface ResolvedCover {
  src?: string;
  seed: string;
  inks?: Inks | null;
}

/** The address Schall serves an album's cover at. */
export function coverSrc(albumId: string): string {
  return `/api/v1/albums/${encodeURIComponent(albumId)}/cover`;
}

export function resolveCover(cover: CoverRef): ResolvedCover {
  if (typeof cover === 'string') return { src: coverSrc(cover), seed: cover };
  return { src: cover.src ?? undefined, seed: cover.seed, inks: cover.inks };
}

/** The print a page gets when it asks for nothing: a strip of generated art in
 *  the house inks, short enough to sit behind the top bar and a page header. */
export const DEFAULT_PRINT_HEIGHT = 9;

class DuotonPage {
  covers = $state<ResolvedCover[]>([]);
  mode = $state<'strip' | 'single'>('strip');
  height = $state(DEFAULT_PRINT_HEIGHT);
  inks = $state<Inks>(HOUSE_INKS);
  /** Whether a page has asked for a print, so the shell can tell a page that
   *  draws a hero from one that has not been moved over yet. */
  claimed = $state(false);
}

/** The open page's print and inks. Read it; write it only through
 *  `usePagePrint`. */
export const duoton = new DuotonPage();

let owner: object | undefined;
let generation = 0;

function apply(request: PagePrint | null | undefined) {
  const covers = (request?.covers ?? []).map(resolveCover);
  const mode = request?.mode ?? 'strip';
  duoton.covers = covers;
  duoton.mode = mode;
  duoton.height = request?.height ?? (request ? (mode === 'single' ? 22 : 15) : DEFAULT_PRINT_HEIGHT);
  duoton.claimed = !!request;

  const run = ++generation;
  const stored = request?.inks ?? covers.find((c) => c.inks)?.inks;
  if (stored) {
    duoton.inks = clampInks(stored);
    return;
  }
  const picture = covers.find((c) => c.src)?.src;
  if (!picture) {
    duoton.inks = HOUSE_INKS;
    return;
  }
  // The previous inks stay up while the cover is read, so changing page fades
  // from one pair to the next rather than flashing through the house inks.
  void inksOfPicture(picture).then((inks) => {
    if (run === generation) duoton.inks = inks ?? HOUSE_INKS;
  });
}

/** Sets the open page's print and inks. Call it once in a page's script; the
 *  getter is tracked, so the print follows the page's data as it arrives:
 *
 *    usePagePrint(() => ({ covers: [release.id], mode: 'single' }));
 *
 *  Return undefined while the data is loading. The print and inks already up
 *  stay until the page knows its own: resetting to the house inks for the
 *  loading moment drew every page in navy and peach first and then switched.
 *  The page's request is withdrawn when the page goes away. */
export function usePagePrint(get: () => PagePrint | null | undefined) {
  const token = {};
  $effect(() => {
    const request = get();
    owner = token;
    if (request === undefined) return;
    apply(request);
  });
  onDestroy(() => {
    if (owner !== token) return;
    owner = undefined;
    // The next page claims the print while it is set up, before this runs.
    // Only a page that never asks for one falls back to the default.
    setTimeout(() => {
      if (owner === undefined) apply(undefined);
    });
  });
}

/** The inks of one album or picture, for a page that wants a pair without a
 *  print (a card, a dialog). Undefined until they are known. */
export function inksFor(cover: () => CoverRef | null | undefined): { readonly current: Inks | undefined } {
  let current = $state<Inks | undefined>();
  $effect(() => {
    const ref = cover();
    if (!ref) {
      current = undefined;
      return;
    }
    const resolved = resolveCover(ref);
    if (resolved.inks) {
      current = clampInks(resolved.inks);
      return;
    }
    let live = true;
    if (resolved.src) void inksOfPicture(resolved.src).then((inks) => live && (current = inks));
    return () => {
      live = false;
    };
  });
  return {
    get current() {
      return current;
    }
  };
}
