<script lang="ts">
  import type { Snippet } from 'svelte';
  import type { HTMLAttributes } from 'svelte/elements';
  import { cn } from '$lib/utils';

  // The small state tag a table row draws in its own column: "unidentified",
  // "held twice", "no track list". A square box with a 1px rule around a word,
  // nothing else.
  //
  // This is not `Chip.svelte`. That one leads with the role's glyph and reports
  // a status beside a heading or a card. This tag is plain, with no glyph,
  // because a table column of them sits sixty rows tall, and sixty glyphs down
  // a column is louder than the rows it is labelling.
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
    neutral: 'border-rule text-ink-2',
    attention: 'border-rule-strong text-ink',
    broken: 'border-fail text-fail',
    warn: 'border-decide text-decide'
  };
</script>

<span
  data-tone={tone}
  {title}
  class={cn(
    'inline-flex h-5 items-center whitespace-nowrap border px-2 text-meta',
    tones[tone],
    className
  )}
  {...rest}
>
  {@render children()}
</span>
