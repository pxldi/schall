// The shared layer of the Duoton interface (ADR Duoton). Pages import from here.
export { HOUSE_INKS, clampInks, contrast, inksFromPixels, luminance, mix, type Inks } from './inks';
export { drawGenerated, inksOfPicture, paintPrint } from './paint';
export {
  coverSrc,
  duoton,
  inksFor,
  resolveCover,
  usePagePrint,
  type CoverRef,
  type PagePrint
} from './page.svelte';
export { useNewestPrint } from './newest.svelte';
