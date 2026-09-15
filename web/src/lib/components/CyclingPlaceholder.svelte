<script module lang="ts">
  // Fifteen names, in the order design/field.html fixes them. The test each one
  // has to pass is recognition — a reader who does not know the name learns
  // nothing from seeing it — and past that they are spread across genre and era
  // so that no two neighbours sit in the same one. Mozart and Beyoncé in the
  // same set is the whole argument: the field takes anybody. The order is fixed
  // rather than shuffled, so two people looking at the same screen see the same
  // word, and every name is written the way it would be typed.
  export const ARTIST_NAMES = [
    'Amadeus Mozart',
    'The Beatles',
    'Beyoncé',
    'Bob Marley',
    'Taylor Swift',
    'Miles Davis',
    'Radiohead',
    'Nina Simone',
    'Daft Punk',
    'Johnny Cash',
    'Kendrick Lamar',
    'Fleetwood Mac',
    'Aretha Franklin',
    'Queen',
    'Dolly Parton'
  ];

  // One name takes one beat of the reading step, `--motion-read`: it sweeps in,
  // is held long enough to be read, goes out, and leaves the box empty before
  // the next one starts. The keyframes in styles.css cut that beat up and are
  // written against the fifteen names above, so the whole cycle is the count of
  // names times the step. The step itself is not repeated here — the stylesheet
  // owns it, and a second copy of it in a script is how the two drift apart.
</script>

<script lang="ts">
  import type { Snippet } from 'svelte';
  import { cn } from '$lib/utils';

  type Props = {
    // The field itself, rendered inside the wrapper so that the ghost behind it
    // shares its positioning context. It takes `.field` and, if it carries a
    // trailing state glyph, `.field.trailing` — the same variant passed here, so
    // that both layers get their metrics from the component.
    children: Snippet;
    // What the field currently holds. One character ends the cycle for good.
    value?: string;
    // The field reserves room for a trailing state glyph, so the ghost does too.
    trailing?: boolean;
    names?: string[];
    // Has this field ever been typed into? Bindable because it has to outlive
    // the box: a cycle that started again when the panel was reopened would be
    // moving in front of somebody who is deciding what to search for.
    typed?: boolean;
    class?: string;
  };

  let {
    children,
    value = '',
    trailing = false,
    names = ARTIST_NAMES,
    typed = $bindable(false),
    class: className
  }: Props = $props();

  // The state is "has this field ever been typed into", not "is it empty": the
  // first keystroke ends the cycle, and clearing the field does not start it
  // again.
  $effect(() => {
    if (value !== '') typed = true;
  });

  const cycle = $derived(`calc(var(--motion-read) * ${names.length})`);
</script>

<span
  class={cn('ph-wrap', typed && 'typed', className)}
  data-typed={typed ? 'true' : 'false'}
  data-trailing={trailing ? 'true' : 'false'}
>
  {@render children()}
  <!-- Decorative, and never the field's accessible name: a word that changes
       every 3.2 seconds under somebody reading it is the worst thing this could
       do. The name is the field's own visible label, which does not change. -->
  {#if !typed}
    <span
      class={cn('ph', trailing && 'trailing')}
      data-trailing={trailing ? 'true' : 'false'}
      aria-hidden="true"
      style="--ph-cycle: {cycle}"
    >
      {#each names as name, index (index)}
        <span style="--ph-index: {index}">{name}</span>
      {/each}
    </span>
  {/if}
</span>
