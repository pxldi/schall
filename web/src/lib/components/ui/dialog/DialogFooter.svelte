<script lang="ts">
  import type { Snippet } from 'svelte';
  import type { HTMLAttributes } from 'svelte/elements';
  import { cn } from '$lib/utils';

  // The band at the bottom: the controls that answer the dialog. They sit at
  // the right on a wide panel and stack on a narrow one, with the action the
  // panel exists for last, which is where a reader's hand ends up.
  let {
    class: className,
    children,
    ...rest
  }: HTMLAttributes<HTMLDivElement> & { children?: Snippet } = $props();
</script>

<div
  class={cn(
    'flex flex-col-reverse gap-2 border-t border-line-thin px-4 py-3 sm:flex-row sm:justify-end',
    className
  )}
  {...rest}
>
  {@render children?.()}
</div>
