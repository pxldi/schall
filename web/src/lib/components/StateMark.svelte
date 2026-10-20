<script lang="ts">
  import type { Snippet } from 'svelte';
  import { changed } from '$lib/motion.svelte';
  import { STATE_GLYPH } from '$lib/states';
  import Icon from './Icon.svelte';

  // The state mark is the 16px cell that leads a row, a notice or a heading:
  // the role's own glyph in the role's colour, with no fill (ADR 0042). Every
  // role has one glyph everywhere, so what a caller passes inside is not drawn. The words for the state are written beside it by the
  // caller, so the mark itself is hidden from assistive technology.
  type Role = 'ok' | 'idle' | 'decide' | 'busy' | 'fail' | 'done';

  let {
    role,
    // `row` beside a line of text, `panel` at the head of an empty panel.
    size = 'row'
  }: {
    role: Role;
    size?: 'row' | 'panel';
    children?: Snippet;
  } = $props();

  // Written out per role, because Tailwind reads class names from the source.
  const tints: Record<Role, string> = {
    ok: 'text-ok',
    idle: 'text-idle',
    decide: 'text-decide',
    busy: 'text-busy',
    fail: 'text-fail',
    done: 'text-done'
  };

  // It turns over once when the role changes, never on the first draw.
  const turned = changed(() => role);
</script>

<span
  aria-hidden="true"
  class="grid shrink-0 place-items-center transition {size ===
  'panel'
    ? 'size-5'
    : 'size-4'} {tints[role]}"
  class:turn={turned.now}
  onanimationend={turned.settle}
>
  <Icon name={STATE_GLYPH[role]} size={size === 'panel' ? 16 : 13} class={role === 'busy' ? 'animate-spin' : ''} />
</span>
