<script lang="ts">
  import type { Snippet } from 'svelte';
  import { changed } from '$lib/motion.svelte';

  // The state mark is the 16px cell that leads a row, a notice or a heading. It
  // is the chip of design/chip.html with the word taken away, for the one place
  // there is no room for a label: a tint of the role at 14% behind that role's
  // colour, holding one glyph.
  //
  // It was written out by hand at 27 call sites, in one string, byte for byte
  // the same at every one of them.
  type Role = 'ok' | 'idle' | 'decide' | 'busy' | 'fail';

  let {
    role,
    // Two sizes and no others. `row` is the 16px cell above. `panel` is the
    // 20px one design/empty.html asks for at the head of an empty panel — "one
    // step larger than a row's" — where the mark sits beside a 15px heading
    // rather than beside a line of body text, and the row size reads as a
    // speck next to it.
    size = 'row',
    // The glyph, which the caller owns because the word it stands for is the
    // caller's too. chip.html enumerates the whole set: a tick for ok, and the
    // bold mono characters `–` idle, `?` decide, `·` busy, `!` fail.
    children
  }: {
    role: Role;
    size?: 'row' | 'panel';
    children?: Snippet;
  } = $props();

  // Written out per role rather than built from the role, because Tailwind
  // reads class names out of the source as literal text and never sees a name
  // that a template string assembled at runtime.
  const tints: Record<Role, string> = {
    ok: 'bg-ok/14 text-ok',
    idle: 'bg-idle/14 text-idle',
    decide: 'bg-decide/14 text-decide',
    busy: 'bg-busy/14 text-busy',
    fail: 'bg-fail/14 text-fail'
  };

  // A file that was being looked up and is now a question, a job that was
  // running and has now failed: the mark is the smallest thing on the row and
  // it is the thing that changed. It turns over once, on the arriving step,
  // and the tint eases across on the state step underneath.
  //
  // Never on the first draw. A library list is a thousand of these and it
  // arrives all at once whenever somebody opens the page; a thousand marks
  // popping is the whole reason a scale is worth having.
  const turned = changed(() => role);
</script>

<!-- The glyph is drawing and not words. `–`, `?`, `·` and `!` stand for a state
     that every call site also writes out beside the mark, so a screen reader
     that reads punctuation aloud was announcing a stray character before the
     message and adding nothing. `aria-hidden` takes the mark out of what is
     read and leaves it on screen. -->
<span
  aria-hidden="true"
  class="numeric grid shrink-0 place-items-center rounded-row text-micro font-bold transition {size ===
  'panel'
    ? 'size-5'
    : 'size-4'} {tints[role]}"
  class:turn={turned.now}
  onanimationend={turned.settle}
>
  {@render children?.()}
</span>
