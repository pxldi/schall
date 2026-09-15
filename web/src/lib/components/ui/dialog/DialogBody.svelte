<script lang="ts">
  import type { Snippet } from 'svelte';
  import type { HTMLAttributes } from 'svelte/elements';
  import { cn } from '$lib/utils';

  // What the dialog holds between its two bands. It is the part that scrolls
  // when there is more of it than the window has room for, so the header and
  // the answer controls stay where the reader last saw them.
  let {
    class: className,
    children,
    ...rest
  }: HTMLAttributes<HTMLDivElement> & { children?: Snippet } = $props();
</script>

<div class={cn('min-h-0 flex-1 overflow-y-auto px-4 py-3', className)} {...rest}>
  {@render children?.()}
</div>
