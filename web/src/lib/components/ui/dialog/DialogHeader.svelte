<script lang="ts">
  import type { Snippet } from 'svelte';
  import type { HTMLAttributes } from 'svelte/elements';
  import { cn } from '$lib/utils';

  // The band at the top of the panel: what this dialog is about. The right
  // padding leaves room for the close control in the corner.
  let {
    class: className,
    children,
    ...rest
  }: HTMLAttributes<HTMLDivElement> & { children?: Snippet } = $props();
</script>

<div
  class={cn(
    'flex flex-col gap-1 border-b border-line-thin px-4 py-3 pr-12',
    className
  )}
  {...rest}
>
  {@render children?.()}
</div>
