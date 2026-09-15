<script lang="ts">
  import { Select as SelectPrimitive } from 'bits-ui';
  import { cn } from '$lib/utils';

  // The line above a group of choices, saying what the group has in common —
  // "Sources", "Formats". It is tied to its group, so a screen reader announces
  // it once when the reader arrives at the group rather than on every row.
  let { class: className, ...rest }: SelectPrimitive.GroupHeadingProps = $props();
</script>

<SelectPrimitive.GroupHeading class={cn('label block px-2.5 pb-1 pt-1.5', className)} {...rest} />
