<script lang="ts">
  import type { Snippet } from 'svelte';
  import { Check, Circle } from '@lucide/svelte';
  import { cn } from '$lib/utils';
  import StateMark from '$lib/components/StateMark.svelte';

  // The panel that stands where a list would have been. An empty list is never
  // just empty: it is empty because nothing has been started, because the
  // filter excludes everything, because the work is finished, or because the
  // answer has not arrived yet — and design/empty.html settles both the shape
  // and which role names which reason.
  //
  // Nine call sites had written the panel out by hand and no two of them agreed:
  // `max-w-lg` beside `max-w-[512px]` for the same 512 pixels, `p-4` beside
  // `p-5`, `border-line-thin` beside `border-line-regular`. The card's values are the
  // ones here. It carries no fill (the 2026-09-07 surface review): a panel that
  // only names why a list is empty is a grouping container, not a working
  // surface.
  type Role = 'ok' | 'idle' | 'decide' | 'busy' | 'fail';

  let {
    // The reason, as a colour and a glyph. `idle` for nothing yet and nothing
    // matching, `ok` for nothing left to do, `busy` for still working.
    role = 'idle',
    // Names the reason rather than the absence. Left out where the panel holds
    // a single line that is already the whole message.
    heading,
    // A panel inside a section that has already used the page's `h2` takes an
    // `h3`, so the headings on a screen still read in order.
    level = 2,
    class: className,
    children
  }: {
    role?: Role;
    heading?: string;
    level?: 2 | 3;
    class?: string;
    children?: Snippet;
  } = $props();

  // The four bold mono characters design/chip.html enumerates. `ok` is the one
  // role whose mark is drawn rather than typed.
  const glyphs: Record<Role, string> = {
    ok: '',
    idle: '–',
    decide: '?',
    busy: '·',
    fail: '!'
  };
</script>

<div
  class={cn(
    'flex max-w-[512px] flex-col gap-3 rounded-panel border border-line-regular p-5',
    className
  )}
>
  {#if heading}
    <div class="flex items-center gap-2.5">
      <StateMark {role} size="panel">
        {#if role === 'ok'}
          <Check size={12} strokeWidth={3.2} />
        {:else if role === 'idle'}
          <Circle size={10} strokeWidth={2} />
        {:else}
          {glyphs[role]}
        {/if}
      </StateMark>
      <svelte:element this={`h${level}`} class="font-display text-lead font-bold text-ink">
        {heading}
      </svelte:element>
    </div>
  {/if}
  {@render children?.()}
</div>
