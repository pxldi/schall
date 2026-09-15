<script lang="ts">
  import type { Snippet } from 'svelte';
  import { Check } from '@lucide/svelte';

  let {
    number,
    title,
    complete = false,
    active = false,
    children,
    action
  }: {
    number: string;
    title: string;
    complete?: boolean;
    /** The one step the user can act on now. Everything after it is shown but
     * held back, so the order is visible without being clickable out of turn. */
    active?: boolean;
    children: Snippet;
    action?: Snippet;
  } = $props();
</script>

<div
  class="flex flex-wrap items-center gap-x-3 gap-y-2 rounded-row px-3.5 py-3 {active
    ? 'border border-line-regular bg-surface-regular'
    : 'border border-line-thin'}"
>
  <span class:complete class="step-number">
    {#if complete}<Check size={13} strokeWidth={3} />{:else}{number}{/if}
  </span>

  <span class="flex min-w-0 flex-1 flex-col gap-1">
    <span
      class="text-body font-semibold {complete || active
        ? 'text-ink'
        : 'text-ink-2'}">{title}</span
    >
    <span class="text-meta text-ink-3">{@render children()}</span>
  </span>

  {#if action}{@render action()}{/if}
</div>
