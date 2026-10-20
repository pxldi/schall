<script lang="ts" module>
  // The roles a badge may carry. Five of them are the states the API reports;
  // `done` is the sixth, for a thing that is finished rather than good — a
  // manual decision, a resolved conflict, a merged recording — because green
  // says the wrong word about all three.
  export type BadgeRole = 'ok' | 'idle' | 'decide' | 'busy' | 'fail' | 'done';
</script>

<script lang="ts">
  import type { Snippet } from 'svelte';
  import type { HTMLAttributes } from 'svelte/elements';
  import { cn } from '$lib/utils';

  // A badge is how a state gets a name: the small inline pill beside a heading,
  // in a row that has room for words, or under a title. A tint of the role at
  // 14%, that role's own colour on the words, 6px corners, and a 5px dot in
  // front so the colour is never the only thing saying which state it is.
  //
  // It draws what `Chip.svelte` draws today, and the two live side by side
  // until the screens are moved over one at a time.
  let {
    // Left out on purpose for the roleless badge, which carries information
    // that is not a state — a reason a row is being suggested, a count, a
    // format. It has no hue, so it has no dot either: a grey dot reads as a
    // seventh role, and grey is already `idle`.
    role,
    // A badge that draws its own glyph — a spinner while a search runs, a tick
    // on a copy that arrived — says the same thing the dot says, and two marks
    // in front of two words is a badge saying it twice.
    dot = true,
    class: className,
    children,
    // A badge whose words are cut short carries the whole of them on `title`.
    // That and anything like it is an ordinary attribute of the span.
    ...rest
  }: HTMLAttributes<HTMLSpanElement> & {
    role?: BadgeRole;
    dot?: boolean;
    class?: string;
    children?: Snippet;
  } = $props();

  // Written out per role rather than built from the role, because Tailwind
  // reads class names out of the source as literal text and never sees a name
  // that a template string assembled at runtime.
  const tints: Record<BadgeRole, string> = {
    ok: 'bg-ok/14 text-ok',
    idle: 'bg-idle/14 text-idle',
    decide: 'bg-decide/14 text-decide',
    busy: 'bg-busy/14 text-busy',
    fail: 'bg-fail/14 text-fail',
    done: 'bg-done/14 text-done'
  };

  const dots: Record<BadgeRole, string> = {
    ok: 'bg-ok',
    idle: 'bg-idle',
    decide: 'bg-decide',
    busy: 'bg-busy',
    fail: 'bg-fail',
    done: 'bg-done'
  };
</script>

<span
  data-role={role}
  class={cn(
    // The words stay at the dense meta step. A tinted box wins against plain
    // text at any size, so beside a card heading the badge has to give the size
    // back — but a state nobody can read is not a quieter state, it is a
    // missing one.
    'inline-flex items-center gap-1.5 rounded-row px-1.5 py-[2px] text-dense-meta font-normal',
    role ? tints[role] : 'bg-surface-thick text-ink-2',
    className
  )}
  {...rest}
>
  <!-- The dot is drawing and not words. The badge's own label says the state
       out loud beside it, so a screen reader has nothing to gain from the mark
       and something to lose from announcing it. -->
  {#if role && dot}
    <span aria-hidden="true" class="size-[5px] shrink-0 rounded-full {dots[role]}"></span>
  {/if}
  {@render children?.()}
</span>
