<script lang="ts">
  // The filter strip above a list. It existed five times as inline markup —
  // artists, releases, an artist's releases, downloads and library — and the
  // five had already drifted apart in height, text size and whether the count
  // was aligned to anything. One definition, so they cannot drift again.
  //
  // Three variants, chosen by what the strip is switching. `chips` (the
  // default) is the original look: separate pills with their own hairline,
  // for a view where every choice is a chip. `tabs` is for switching what the
  // page shows — no border, an accent underline on the active word, the way
  // the mast marks the active room. `filter` is for narrowing an already-
  // chosen view — one pill-shaped container, the active choice a filled
  // background, no accent. The accent is chrome: a selected tab may carry it
  // because it marks where you are, a selected filter value may not because
  // it marks nothing but a value.

  type Option = { value: string; name: string; count?: number };
  type Variant = 'tabs' | 'filter' | 'chips';

  let {
    options,
    value,
    onchange,
    label,
    pending = false,
    variant = 'chips'
  }: {
    options: Option[];
    value: string;
    onchange: (value: string) => void;
    // Names what the strip is filtering by, for anyone not reading the screen.
    label: string;
    // Whether the figures are still on their way. A strip that carries counts
    // has to say so while it does not have them yet: writing a zero there is
    // not a placeholder, it is a wrong answer, and the reader believes it for
    // as long as the request takes. A strip with no counts at all — Library's
    // three shapes — passes nothing and stays as it is.
    pending?: boolean;
    variant?: Variant;
  } = $props();

  const containerClass = $derived(
    variant === 'tabs'
      ? 'no-scrollbar flex w-fit max-w-full items-center gap-x-5 overflow-x-auto'
      : variant === 'filter'
        ? 'no-scrollbar inline-flex h-7 max-w-full items-center gap-0.5 overflow-x-auto rounded-full border border-line-thin p-0.5'
        : 'no-scrollbar flex w-fit max-w-full items-center gap-1.5 overflow-x-auto'
  );

  function buttonClass(selected: boolean): string {
    if (variant === 'tabs') {
      return `flex h-8 shrink-0 items-center gap-1.5 border-b-2 px-0 text-body transition ${
        selected ? 'border-accent text-ink' : 'border-transparent text-ink-2 hover:text-ink'
      }`;
    }
    if (variant === 'filter') {
      return `flex h-6 shrink-0 items-center gap-1.5 rounded-full px-2.5 text-body transition ${
        selected ? 'bg-surface-thick text-ink' : 'text-ink-2 hover:text-ink'
      }`;
    }
    return `flex h-7 shrink-0 items-center gap-1.5 rounded-full border px-2.5 text-body transition ${
      selected ? 'border-accent text-ink' : 'border-line-thin text-ink-2 hover:border-line-regular'
    }`;
  }

  // `filter` never wears the accent, so its count dims the same way its label
  // does instead of switching hue. `chips` and `tabs` both mark the selected
  // count in the accent, the way they mark the selected label.
  function countClass(selected: boolean): string {
    return variant === 'filter' ? (selected ? 'text-ink' : 'text-ink-3') : selected ? 'text-accent' : 'text-ink-3';
  }
</script>

<!-- A group of buttons, not a tablist.
     `role="tablist"` and `role="tab"` are a contract: a tab owns a panel named
     by `aria-controls`, the arrow keys move between tabs, and only one of them
     is in the tab order. None of that was true here and none of it should be —
     this strip narrows a list that is already on screen, it opens no panel, and
     the arrow keys belong to the page. A screen reader was being told to expect
     a shape the strip does not have.
     What it actually is: a set of buttons where one is on, which is what
     `aria-pressed` says. `variant="tabs"` looks like a tab strip, but the DOM
     underneath is the same buttons-in-a-group for the same reason.

     The strip is as wide as its words. It is a block inside a stacked column on
     most screens, so without `w-fit` it took the whole width of the page and six
     short words sat at the left end of a long empty bar — beside a strip in a
     row, which is content-width, the same control looked like two controls. -->
<div class="scroll-fade scroll-fade-surface min-w-0 max-w-full">
  <div class={containerClass} role="group" aria-label={label}>
  {#each options as option (option.value)}
    <button
      aria-pressed={value === option.value}
      onclick={() => onchange(option.value)}
      class={buttonClass(value === option.value)}
    >
      <!-- The word and the figure share a baseline. Centring them by box
           instead left the figure riding high, because mono and sans do not
           put their baselines in the same place inside a line box. -->
      <span class="flex items-baseline gap-1.5">
        <span>{option.name}</span>
        <!-- The room the figure will take, in the figure's own units. `ch` is
             the width of a `0`, and inside .numeric that is the width of every
             character, so `2ch` is exactly two digits whatever the type scale
             does later.

             Both branches are given the same box on purpose. The blank pill
             used to be `w-4` — 16px against the 6.75px a single digit actually
             occupies — so every count arriving pulled the whole strip 9px to
             the left, which is the shift that survived the first fix. Holding
             the width in the figure's units instead means the placeholder and
             the figure are the same size by construction rather than by a
             number somebody measured once.

             Two digits is the common case; a figure wider than that grows the
             pill once, which is a smaller lie than a nought that turns into
             forty. -->
        {#if option.count !== undefined}
          <span class="numeric min-w-[2ch] text-meta {countClass(value === option.value)}">
            {option.count.toLocaleString()}
          </span>
        {:else if pending}
          <span
            class="numeric inline-block h-2.5 w-[2ch] animate-pulse rounded-row bg-white/10 text-meta"
            aria-hidden="true"
          ></span>
        {/if}
      </span>
    </button>
    {/each}
  </div>
</div>
