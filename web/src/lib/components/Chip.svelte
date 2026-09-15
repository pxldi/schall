<script lang="ts">
  import type { Snippet } from 'svelte';
  import type { HTMLAttributes } from 'svelte/elements';
  import { cn } from '$lib/utils';

  // A chip is how a state gets a name. It is the small inline pill that sits
  // beside a heading, in a row that has room for words, or under a title: a
  // tint of the role at 14%, that role's own colour on the text, 6px corners,
  // and a 5px dot in front so the colour is never the only thing saying which
  // state it is. design/chip.html draws it.
  //
  // Twenty-six call sites had written the same string out by hand, drifting a
  // pixel at a time as they were copied.
  //
  // This is not StatusBadge, and the two are not one component with a prop.
  // StatusBadge is the band that reports a failure at the thing it belongs to —
  // 12px corners, 12px of padding, a 16px state mark inside it, a full sentence
  // in ordinary ink, and no `ok` role at all, because Schall confirms success by
  // the interface changing rather than by a message about it. This is a word in
  // a 76px column. Folding either into the other would redraw the other's every
  // call site.
  type Role = 'ok' | 'idle' | 'decide' | 'busy' | 'fail';

  let {
    // Left out on purpose for the sixth form chip.html names: the roleless
    // chip, for information that is not a state — a reason a row is being
    // suggested, a count, a format. It carries no hue, so it carries no dot
    // either: a grey dot reads as a sixth role, and grey is already `idle`.
    role,
    // A chip that draws its own glyph — a spinner while a search runs, a tick
    // on a copy that arrived — says the same thing the dot says, and two marks
    // in front of two words is a chip saying it twice.
    dot = true,
    class: className,
    children,
    // A chip whose words are cut short carries the whole of them on `title`,
    // and a chip standing in for a longer summary says so the same way. Both
    // are ordinary attributes of the span and are passed straight through.
    ...rest
  }: HTMLAttributes<HTMLSpanElement> & {
    role?: Role;
    dot?: boolean;
    class?: string;
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

  const dots: Record<Role, string> = {
    ok: 'bg-ok',
    idle: 'bg-idle',
    decide: 'bg-decide',
    busy: 'bg-busy',
    fail: 'bg-fail'
  };
</script>

<span
  data-role={role}
  class={cn(
    // The box was px-2 py-[3px] and the words were font-medium. Beside a card
    // heading that made the chip the loudest thing in the row: a tinted box
    // wins against plain text at any size, so it has to give the size back.
    // The words stay at --text-meta, because a state nobody can read is not a
    // quieter state, it is a missing one.
    'inline-flex items-center gap-1.5 rounded-row px-1.5 py-[2px] text-meta font-normal',
    role ? tints[role] : 'bg-surface-thick text-ink-2',
    className
  )}
  {...rest}
>
  <!-- The dot is drawing and not words. The chip's own label says the state out
       loud beside it, so a screen reader has nothing to gain from the mark and
       something to lose from announcing it. -->
  {#if role && dot}
    <span aria-hidden="true" class="size-[5px] shrink-0 rounded-full {dots[role]}"></span>
  {/if}
  {@render children?.()}
</span>
