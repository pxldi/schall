<script lang="ts">
  import { allIcons, type IconName } from '$lib/icons';
  import { cn } from '$lib/utils';

  // Every icon in Schall is drawn here, by name, from the Misprint set in
  // `$lib/icons` (ADR Duoton): one sharp 2px line in the text colour, so an
  // icon always takes the colour of the words beside it.
  //
  // Text glyphs ("->", "↑↓", "×") are not icons and are not drawn anywhere a
  // control or a direction is meant.
  //
  // An icon beside a word is decoration and is hidden from assistive
  // technology. An icon that stands alone in a control gets its name from the
  // control (`aria-label` on the button). An icon that is itself the only
  // statement of something (a tick meaning "agrees") takes `aria-label`.

  let {
    name,
    size = 'md',
    class: className,
    'aria-label': label
  }: {
    name: IconName;
    /** `sm` inside a chip or a row (14px at the 16px design root), `md` beside
     *  body text and in buttons (16px), `lg` at the head of an empty panel
     *  (20px). A number is screen pixels, as the old icon library took it, so
     *  the call sites written for it keep their size. */
    size?: 'sm' | 'md' | 'lg' | number;
    class?: string;
    'aria-label'?: string;
  } = $props();

  // The named steps are rem, so an icon follows the root font-size like the
  // text beside it.
  const steps = { sm: '0.875rem', md: '1rem', lg: '1.25rem' };
  const length = $derived(typeof size === 'number' ? `${size}px` : steps[size]);
  const parts = $derived(allIcons[name]);
</script>

<svg
  viewBox="0 0 24 24"
  width={length}
  height={length}
  role={label ? 'img' : undefined}
  aria-label={label}
  aria-hidden={label ? undefined : 'true'}
  focusable="false"
  data-icon={name}
  class={cn('shrink-0 overflow-visible', className)}
>
  <g fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="square" stroke-linejoin="miter">
    {#each parts as d, i (i)}
      <path {d} />
    {/each}
  </g>
</svg>
