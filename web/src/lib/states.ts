import type { IconName } from '$lib/icons';

// The six state roles and the glyph each one is drawn with (ADR 0042). A
// state colour never appears without its glyph, so a reader who cannot tell
// the hues apart still reads the state.

export type StateRole = 'ok' | 'idle' | 'decide' | 'busy' | 'fail' | 'done';

export const STATE_GLYPH: Record<StateRole, IconName> = {
  ok: 'check',
  idle: 'minus',
  decide: 'alert',
  busy: 'busy',
  fail: 'close',
  done: 'checklist'
};

// Written out per role, because Tailwind reads class names from the source.
export const STATE_TEXT: Record<StateRole, string> = {
  ok: 'text-ok',
  idle: 'text-idle',
  decide: 'text-decide',
  busy: 'text-busy',
  fail: 'text-fail',
  done: 'text-done'
};
