<script lang="ts">
  import { Checkbox as CheckboxPrimitive } from 'bits-ui';
  import { cn } from '$lib/utils';

  // The one boolean in Schall. A 24px target that carries a small tick when it is on:
  // colour alone is not a state, here or anywhere.
  //
  // The box itself is the `.check` class the stylesheet already draws, asked
  // for rather than copied, so the size, the corner, the inset fill and the
  // border are the ones every existing checkbox has. What `.check` cannot do
  // here is the tick: its rules key off `:checked`, which only a native input
  // has, and Bits UI draws a `<button>` that says what it is with
  // `data-state`. So the on state and the tick are written against
  // `data-state` instead, in the same colours — the accent for the fill, the
  // accent's own ink for the mark.
  //
  // The tick is a mask rather than a drawn glyph, which is what `.check` does
  // too. A mask takes its colour from the box behind it, so the mark and the
  // fill can never disagree about which accent they are.
  let {
    checked = $bindable(false),
    indeterminate = $bindable(false),
    class: className,
    ...rest
    // The box draws itself and holds nothing, so the primitive's `children` is
    // taken away from the caller: what goes inside is the tick, and the words
    // belong to the label beside it.
  }: Omit<CheckboxPrimitive.RootProps, 'children' | 'child'> = $props();

  // The same two figures `.check` uses: an 11px mark centred in a 24px target.
  const tick =
    "url(\"data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 24 24' fill='none' stroke='%23000' stroke-width='4' stroke-linecap='round' stroke-linejoin='round'%3E%3Cpath d='M20 6 9 17l-5-5'/%3E%3C/svg%3E\")";

  // Indeterminate is a bar rather than a tick: some of what this box stands for
  // is on. It is drawn at the same 11px so the two marks are the same weight.
  const bar =
    "url(\"data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 24 24' fill='none' stroke='%23000' stroke-width='4' stroke-linecap='round'%3E%3Cpath d='M5 12h14'/%3E%3C/svg%3E\")";

  // Written as one style string rather than as `style:` directives, because the
  // vendor-prefixed property Safari still needs cannot be named by one.
  function mask(image: string) {
    return [
      `mask-image:${image}`,
      `-webkit-mask-image:${image}`,
      'mask-size:contain',
      '-webkit-mask-size:contain',
      'mask-repeat:no-repeat',
      '-webkit-mask-repeat:no-repeat',
      'mask-position:center',
      '-webkit-mask-position:center'
    ].join(';');
  }
</script>

<CheckboxPrimitive.Root
  bind:checked
  bind:indeterminate
  class={cn(
    'check grid place-items-center',
    'enabled:hover:border-line-live',
    'data-[state=checked]:border-accent data-[state=checked]:bg-accent',
    'data-[state=indeterminate]:border-accent data-[state=indeterminate]:bg-accent',
    'disabled:opacity-50',
    className
  )}
  {...rest}
>
  {#snippet children({ checked: on, indeterminate: partly })}
    <!-- Drawing, and the label beside the box is what says the state out loud,
         so there is nothing here for a screen reader to gain and a repeated
         word for it to lose. -->
    {#if partly || on}
      <span
        aria-hidden="true"
        class="size-[11px] bg-accent-ink"
        style={mask(partly ? bar : tick)}
      ></span>
    {/if}
  {/snippet}
</CheckboxPrimitive.Root>
