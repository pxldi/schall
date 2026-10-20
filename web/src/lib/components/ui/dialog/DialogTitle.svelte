<script lang="ts">
  import { Dialog as DialogPrimitive } from 'bits-ui';
  import { cn } from '$lib/utils';

  // What the dialog is about, in one line. It names the panel to a screen
  // reader as well as to a reader, which is why every dialog has one.
  let { class: className, ...rest }: DialogPrimitive.TitleProps = $props();
</script>

<DialogPrimitive.Title
  class={cn('font-display text-dense-lead font-semibold text-ink', className)}
  {...rest}
/>
