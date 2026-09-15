<script lang="ts">
  import { Dialog as DialogPrimitive } from 'bits-ui';
  import { cn } from '$lib/utils';

  // The line under the title. Two sentences at most: what this is about, and
  // what the reader is being asked to do.
  let { class: className, ...rest }: DialogPrimitive.DescriptionProps = $props();
</script>

<DialogPrimitive.Description class={cn('text-dense-meta text-ink-3', className)} {...rest} />
