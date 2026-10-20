<script lang="ts">
  import type { Snippet } from 'svelte';
  import type { HTMLAttributes } from 'svelte/elements';
  import { STATE_GLYPH, STATE_TEXT, type StateRole } from '$lib/states';
  import { cn } from '$lib/utils';
  import Icon from './Icon.svelte';

  // A state with a name, in a row that has room for the word: the role's glyph
  // in the role's colour, then the word. A square 1px rule and no fill
  // (ADR 0042). Left without a role it is information that is not a state (a
  // reason, a count, a format) and carries no glyph.
  //
  // A chip whose words are cut short carries the whole of them on `title`,
  // passed straight through with every other span attribute.

  let {
    role,
    // A chip that draws its own glyph (a spinner while a search runs) says
    // the same thing the role glyph says, so it turns that one off.
    dot = true,
    class: className,
    children,
    ...rest
  }: HTMLAttributes<HTMLSpanElement> & {
    role?: StateRole;
    dot?: boolean;
    class?: string;
    children?: Snippet;
  } = $props();
</script>

<span
  data-role={role}
  class={cn(
    'inline-flex h-5 items-center gap-1 whitespace-nowrap border border-rule px-1.5 text-meta',
    role ? STATE_TEXT[role] : 'text-ink-2',
    className
  )}
  {...rest}
>
  {#if role && dot}
    <Icon name={STATE_GLYPH[role]} size={12} class={role === 'busy' ? 'animate-spin' : ''} />
  {/if}
  {@render children?.()}
</span>
