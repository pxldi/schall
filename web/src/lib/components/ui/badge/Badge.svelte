<script lang="ts" module>
  // The roles a badge may carry. Five of them are the states the API reports;
  // `done` is the sixth, for a thing that is finished rather than good (a
  // manual decision, a resolved conflict, a merged recording), because green
  // says the wrong word about all three.
  export type BadgeRole = 'ok' | 'idle' | 'decide' | 'busy' | 'fail' | 'done';
</script>

<script lang="ts">
  import type { Snippet } from 'svelte';
  import type { HTMLAttributes } from 'svelte/elements';
  import { cn } from '$lib/utils';
  import Icon from '$lib/components/Icon.svelte';
  import { STATE_GLYPH, STATE_TEXT } from '$lib/states';

  // A badge is how a state gets a name: the small inline tag beside a heading,
  // in a row that has room for words, or under a title. A square with a 1px
  // rule, the role's colour on the words, and the role's glyph in front so the
  // colour is never the only thing saying which state it is.
  //
  // It draws what `Chip.svelte` draws today, and the two live side by side
  // until the screens are moved over one at a time.
  let {
    // Left out on purpose for the roleless badge, which carries information
    // that is not a state: a reason a row is being suggested, a count, a
    // format. It has no hue, so it has no glyph either.
    role,
    // A badge that draws its own glyph (a spinner while a search runs, a tick
    // on a copy that arrived) says the same thing the role glyph says, and two
    // marks in front of two words is a badge saying it twice.
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
</script>

<span
  data-role={role}
  class={cn(
    // The words stay at the meta step; a state nobody can read is a missing
    // one.
    'inline-flex items-center gap-1.5 border border-rule px-1.5 py-[2px] text-meta font-normal',
    role ? STATE_TEXT[role] : 'text-ink-2',
    className
  )}
  {...rest}
>
  <!-- The glyph is drawing and not words. The badge's own label says the state
       out loud beside it, so a screen reader has nothing to gain from the mark
       and something to lose from announcing it. -->
  {#if role && dot}
    <Icon name={STATE_GLYPH[role]} size="sm" class={role === 'busy' ? 'animate-spin' : ''} />
  {/if}
  {@render children?.()}
</span>
