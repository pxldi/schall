<script lang="ts">
  import type { HTMLInputAttributes } from 'svelte/elements';
  import { cn } from '$lib/utils';

  // A text box, and nothing more than the `.field` class the stylesheet already
  // draws. The class is asked for rather than copied: it carries the one
  // control height every form in the product shares, the inset fill that says a
  // field is a well rather than a raise, the mono face every value is set in,
  // and the sans placeholder that names what goes in the box. Restating any of
  // those here would be a second field that drifts from the first one.
  //
  // `trailing` is the room for the one glyph a field may carry, which is always
  // state — a spinner while a lookup answers, a tick once a candidate is
  // chosen. The room belongs to the component and never to a `pr-8` written
  // beside it. There is no leading form, on purpose.
  let {
    class: className,
    value = $bindable(),
    trailing = false,
    ...props
  }: HTMLInputAttributes & { trailing?: boolean } = $props();
</script>

<input
  bind:value
  data-field="true"
  data-trailing={trailing ? 'true' : undefined}
  class={cn('field w-full', trailing && 'trailing', className)}
  {...props}
/>
