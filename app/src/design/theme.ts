import { palette } from './tokens';

/** The roles the app draws with, named over the generated palette. The accent
 * is chrome and never state: a state colour says what a thing is, the accent
 * says where to press. */
export const theme = {
  ground: palette.ground,
  surface: palette['surface-thin'],
  surfaceRaised: palette['surface-regular'],
  line: palette['line-thin'],
  lineStrong: palette['line-regular'],
  ink: palette.ink,
  ink2: palette['ink-2'],
  ink3: palette['ink-3'],
  accent: palette.accent,
  accentInk: palette['accent-ink'],
  ok: palette.ok,
  fail: palette.fail,
  decide: palette.decide,
  busy: palette.busy,
  idle: palette.idle,
  done: palette.done
} as const;

export const type = {
  micro: { fontSize: 11, lineHeight: 14, letterSpacing: 1.1, textTransform: 'uppercase' as const },
  meta: { fontSize: 13, lineHeight: 18 },
  body: { fontSize: 15, lineHeight: 22 },
  lead: { fontSize: 19, lineHeight: 26, letterSpacing: -0.2 },
  display: { fontSize: 26, lineHeight: 30, letterSpacing: -0.6 }
} as const;

export const space = { xs: 4, s: 8, m: 12, l: 16, xl: 24 } as const;
