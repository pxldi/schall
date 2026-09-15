<script lang="ts">
  import type { Snippet } from 'svelte';
  import type { HTMLAttributes } from 'svelte/elements';
  import { cn } from '$lib/utils';

  // The small state tag a table row draws in its own column: "unidentified",
  // "held twice", "no track list". A hairline box around a word, nothing else.
  //
  // This is not `Chip.svelte`. That one carries a tint fill and a dot and
  // reports a status role beside a heading or a card. This tag is plain —
  // border only, no fill, no dot — because a table column of them sits sixty
  // rows tall, and sixty tints down a column is louder than the rows it is
  // labelling.
  type Tone = 'neutral' | 'attention' | 'broken' | 'warn';

  let {
    tone = 'neutral',
    title,
    children,
    class: className,
    // A tag that stands in for a status word a row can change under a reader
    // — Downloads reads its own state this way — passes `aria-live` through
    // like any other span attribute, so the change is announced without a
    // second element existing only to carry it.
    ...rest
  }: HTMLAttributes<HTMLSpanElement> & {
    tone?: Tone;
    title?: string;
    class?: string;
    children: Snippet;
  } = $props();

  // There is no `warn` colour in the stylesheet — the boards call for one but
  // `styles.css` never grew one. `decide` is the closest existing role: both
  // stand for a row that needs a person's attention rather than a failure.
  const tones: Record<Tone, string> = {
    neutral: 'border-line-thin text-ink-2',
    attention: 'border-accent text-accent',
    broken: 'border-fail text-fail',
    warn: 'border-decide text-decide'
  };
</script>

<span
  data-tone={tone}
  {title}
  class={cn(
    'inline-flex h-5 items-center whitespace-nowrap rounded-row px-1.5 text-meta bg-surface-thin border',
    tones[tone],
    className
  )}
  {...rest}
>
  {@render children()}
</span>
