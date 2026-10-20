<script lang="ts">
  import { glide } from '$lib/motion.svelte';

  // The strip of choices above a list: which view, which filter. One
  // definition for every screen, so the strips cannot drift apart.
  //
  // Flat tabs in a 1px rule. The current choice is drawn inverted, by one
  // block that slides from the old choice to the new one (ADR 0042). The
  // `variant` names what the strip switches and no longer changes its look.

  // `count: null` says the option has no figure to wait for, so no placeholder
  // is drawn for it while the others load; `undefined` means not yet known.
  type Option = { value: string; name: string; count?: number | null };
  type Variant = 'tabs' | 'filter' | 'chips';

  let {
    options,
    value,
    onchange,
    label,
    pending = false,
    variant: _variant = 'chips'
  }: {
    options: Option[];
    value: string;
    onchange: (value: string) => void;
    // Names what the strip is filtering by, for anyone not reading the screen.
    label: string;
    // Whether the figures are still on their way. A zero written before the
    // count arrives is a wrong answer, so the strip holds a blank instead.
    pending?: boolean;
    variant?: Variant;
  } = $props();
</script>

<!-- A group of buttons where one is on, which is what `aria-pressed` says. It
     opens no panel and the arrow keys belong to the page, so it is not a
     tablist. -->
<div class="no-scrollbar max-w-full overflow-x-auto">
  <div
    class="relative inline-flex h-7 items-stretch border border-rule"
    role="group"
    aria-label={label}
    use:glide={{ axis: 'x', key: value }}
  >
    {#each options as option (option.value)}
      {@const selected = value === option.value}
      <button
        aria-pressed={selected}
        data-cursor={selected}
        onclick={() => onchange(option.value)}
        class="relative flex shrink-0 items-center gap-1.5 border-r border-rule px-3 text-body transition last:border-r-0 {selected
          ? 'on-ink font-medium'
          : 'text-ink-3 hover:bg-raised hover:text-ink'}"
      >
        <span class="flex items-baseline gap-1.5">
          <span>{option.name}</span>
          <!-- The placeholder holds the room two digits take, in the figure's
               own units, so the strip does not jump when the count lands. -->
          {#if option.count !== undefined && option.count !== null}
            <span class="mono min-w-[2ch] text-micro {selected ? 'text-ink' : 'text-ink-4'}">
              {option.count.toLocaleString()}
            </span>
          {:else if pending && option.count !== null}
            <span class="mono inline-block h-2.5 w-[2ch] animate-pulse bg-raised text-micro" aria-hidden="true"
            ></span>
          {/if}
        </span>
      </button>
    {/each}
  </div>
</div>
