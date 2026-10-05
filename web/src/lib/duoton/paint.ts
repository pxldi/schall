// Drawing for the Duoton print and the generated covers. Everything here takes a
// canvas context, so the components stay small and the drawing is in one place.
// Ported from the prototype the ADR was accepted on.

import { clampInks, hexToRgb, inksFromPixels, mix, type Inks } from './inks';

function hash(seed: string): number {
  let h = 2166136261;
  for (const c of seed) h = Math.imul(h ^ c.charCodeAt(0), 16777619);
  return h >>> 0;
}

/** A small seeded generator, so a release draws the same generated cover and
 *  the same grain on every visit. */
export function seeded(seed: string): () => number {
  let h = hash(seed);
  return () => {
    h = Math.imul(h ^ (h >>> 15), 2246822507);
    h = Math.imul(h ^ (h >>> 13), 3266489909);
    h ^= h >>> 16;
    return (h >>> 0) / 4294967296;
  };
}

/** Draws a stand-in cover for something with no art, so no page falls back to
 *  a grey square. With the cover's palette it is built from those colours in
 *  their order; without one it is a pattern in the two inks. The seed picks the
 *  pattern, so the same release always gets the same picture. */
export function drawGenerated(
  g: CanvasRenderingContext2D,
  w: number,
  h: number,
  seed: string,
  inks?: Partial<Inks> | null
) {
  const rnd = seeded(seed);
  const palette = inks?.palette;
  if (palette && palette.length > 1) {
    g.fillStyle = palette[0];
    g.fillRect(0, 0, w, h);
    const kind = Math.floor(rnd() * 3);
    palette.slice(1).forEach((c, i) => {
      g.fillStyle = c;
      const k = 1 - i / palette.length;
      if (kind === 0) g.fillRect(0, h * (1 - k * 0.55), w, h * k * 0.1 + 2);
      else if (kind === 1) {
        g.beginPath();
        g.arc(w * (0.3 + rnd() * 0.4), h * (0.35 + rnd() * 0.3), w * k * 0.32, 0, Math.PI * 2);
        g.fill();
      } else g.fillRect(w * rnd() * 0.6, h * rnd() * 0.6, w * k * 0.45, h * k * 0.45);
    });
    return;
  }

  const { dark, light } = clampInks(inks);
  g.fillStyle = mix(dark, light, 0.18);
  g.fillRect(0, 0, w, h);
  g.fillStyle = light;
  g.strokeStyle = light;
  const kind = Math.floor(rnd() * 4);
  if (kind === 0) {
    for (let i = 0; i < 7; i++) {
      g.globalAlpha = 0.25 + rnd() * 0.6;
      g.beginPath();
      g.arc(w * rnd(), h * rnd(), w * (0.05 + rnd() * 0.18), 0, Math.PI * 2);
      g.fill();
    }
  } else if (kind === 1) {
    g.lineWidth = w * 0.02;
    for (let i = 0; i < 9; i++) {
      g.globalAlpha = 0.3 + i * 0.07;
      g.beginPath();
      for (let x = 0; x <= w; x += 3) {
        const y = h * (0.15 + i * 0.09) + Math.sin((x / w) * 6 + i) * h * 0.03;
        if (x) g.lineTo(x, y);
        else g.moveTo(x, y);
      }
      g.stroke();
    }
  } else if (kind === 2) {
    const n = 4 + Math.floor(rnd() * 3);
    for (let i = 0; i < n; i++)
      for (let j = 0; j < n; j++) {
        g.globalAlpha = rnd() * 0.85;
        g.fillRect(
          w * (0.08 + (i * 0.84) / n),
          h * (0.08 + (j * 0.84) / n),
          (w * 0.84) / n - 3,
          (h * 0.84) / n - 3
        );
      }
  } else {
    g.lineWidth = w * 0.012;
    for (let i = 1; i < 12; i++) {
      g.globalAlpha = 1 - i * 0.075;
      g.beginPath();
      g.arc(w * 0.5, h * 1.05, w * i * 0.07, Math.PI, 0);
      g.stroke();
    }
  }
  g.globalAlpha = 1;
}

// ── Pictures ────────────────────────────────────────────────────────────────
// One <img> per address for the whole session. Loaded with CORS so a canvas it
// is drawn on stays readable; Schall serves its own covers, so that costs
// nothing, and a remote picture that refuses CORS falls back to generated art.

const pictures = new Map<string, Promise<HTMLImageElement | undefined>>();

export function loadPicture(src: string): Promise<HTMLImageElement | undefined> {
  let found = pictures.get(src);
  if (!found) {
    found = new Promise((resolve) => {
      if (typeof Image === 'undefined') return resolve(undefined);
      const image = new Image();
      image.crossOrigin = 'anonymous';
      image.decoding = 'async';
      image.onload = () => resolve(image.naturalWidth ? image : undefined);
      image.onerror = () => resolve(undefined);
      image.src = src;
    });
    pictures.set(src, found);
  }
  return found;
}

function context(canvas: HTMLCanvasElement, options?: CanvasRenderingContext2DSettings) {
  try {
    return canvas.getContext('2d', options) ?? undefined;
  } catch {
    // jsdom has no canvas and says so by throwing.
    return undefined;
  }
}

/** Whether this document can draw on a canvas at all. jsdom cannot. */
export function canPaint(): boolean {
  if (typeof document === 'undefined') return false;
  if (typeof navigator !== 'undefined' && /jsdom/i.test(navigator.userAgent)) return false;
  return true;
}

const extracted = new Map<string, Promise<Inks | undefined>>();

/** The inks of the picture at an address, read in the browser from a 48px copy.
 *  This stands in until the API carries each release's stored inks; a caller
 *  that has them passes them instead and this is never asked. */
export function inksOfPicture(src: string): Promise<Inks | undefined> {
  let found = extracted.get(src);
  if (!found) {
    found = loadPicture(src).then((image) => {
      if (!image || !canPaint()) return undefined;
      const canvas = document.createElement('canvas');
      canvas.width = canvas.height = 48;
      const g = context(canvas, { willReadFrequently: true });
      if (!g) return undefined;
      try {
        g.drawImage(image, 0, 0, 48, 48);
        return inksFromPixels(g.getImageData(0, 0, 48, 48).data);
      } catch {
        // A tainted canvas: the picture came from somewhere that refused CORS.
        return undefined;
      }
    });
    extracted.set(src, found);
  }
  return found;
}

// ── The print ───────────────────────────────────────────────────────────────

/** One picture in a print, already loaded, or a seed to generate one from. */
export interface PrintSource {
  image?: HTMLImageElement;
  seed: string;
  inks?: Partial<Inks> | null;
}

function square(source: PrintSource, size: number): HTMLCanvasElement {
  const canvas = document.createElement('canvas');
  canvas.width = canvas.height = Math.max(1, size);
  const g = context(canvas);
  if (!g) return canvas;
  if (source.image) {
    g.imageSmoothingQuality = 'high';
    g.drawImage(source.image, 0, 0, size, size);
  } else drawGenerated(g, size, size, source.seed, source.inks);
  return canvas;
}

/** Paints the print: the pictures drawn at size, softened (blur, 1.15
 *  contrast), then every pixel mapped between the two inks by its lightness,
 *  with light grain. `strip` lays the pictures side by side; `single` prints the first one
 *  large and screens a second over it when there is one. */
export function paintPrint(
  canvas: HTMLCanvasElement,
  sources: PrintSource[],
  mode: 'strip' | 'single',
  inks: Inks
) {
  const w = canvas.width;
  const h = canvas.height;
  const g = context(canvas, { willReadFrequently: true });
  if (!g || !w || !h || !sources.length) return;
  const dark = hexToRgb(inks.dark);
  const light = hexToRgb(inks.light);

  // The covers are laid out and softened on a half-size canvas that is never
  // read back, so the browser can filter it on the GPU, then drawn up to size.
  // The same `blur()` on the canvas that is read back runs on the CPU and cost
  // 120ms of every paint at page width, which held up each page change.
  const soft = document.createElement('canvas');
  soft.width = Math.max(1, Math.ceil(w / 2));
  soft.height = Math.max(1, Math.ceil(h / 2));
  const sg = context(soft);
  if (!sg) return;
  const sw = soft.width;
  const sh = soft.height;
  sg.filter = 'blur(0.6px) contrast(1.15)';
  if (mode === 'strip') {
    const s = Math.ceil(sw / sources.length);
    sources.forEach((source, i) => {
      // Each square is cropped to its middle band, so a short print shows the
      // centre of each cover rather than its top.
      const crop = Math.min(s, sh);
      sg.drawImage(square(source, s), 0, Math.max(0, (s - sh) / 2), s, crop, i * s, 0, s, sh);
    });
  } else {
    const s = sw;
    sg.drawImage(square(sources[0], s), 0, s * 0.2, s, s * (sh / sw), 0, 0, sw, sh);
    if (sources[1]) {
      sg.globalAlpha = 0.4;
      sg.globalCompositeOperation = 'screen';
      sg.drawImage(square(sources[1], s), 0, s * 0.4, s, s * (sh / sw), sw * 0.1, 0, sw, sh);
    }
  }
  g.clearRect(0, 0, w, h);
  g.imageSmoothingQuality = 'high';
  g.drawImage(soft, 0, 0, w, h);

  const image = g.getImageData(0, 0, w, h);
  const px = image.data;
  const lightness = (i: number) => (px[i] * 0.3 + px[i + 1] * 0.59 + px[i + 2] * 0.11) / 255;
  // The lightness range is read from a sample and stretched to fill both inks,
  // so a dim cover still prints from one ink to the other.
  let lo = 1;
  let hi = 0;
  for (let i = 0; i < px.length; i += 64) {
    const v = lightness(i);
    lo = Math.min(lo, v);
    hi = Math.max(hi, v);
  }
  const span = Math.max(0.15, hi - lo);
  const grain = seeded('grain');
  for (let i = 0; i < px.length; i += 4) {
    let v = (lightness(i) - lo) / span;
    v = Math.min(1, Math.max(0, v + (grain() - 0.5) * 0.06));
    px[i] = dark[0] + (light[0] - dark[0]) * v;
    px[i + 1] = dark[1] + (light[1] - dark[1]) * v;
    px[i + 2] = dark[2] + (light[2] - dark[2]) * v;
    px[i + 3] = 255;
  }
  g.putImageData(image, 0, 0);
}

/** Draws one cover-sized generated picture onto a canvas at its own size. */
export function paintGenerated(canvas: HTMLCanvasElement, seed: string, inks?: Partial<Inks> | null) {
  const g = context(canvas);
  if (!g) return;
  drawGenerated(g, canvas.width, canvas.height, seed, inks);
}
