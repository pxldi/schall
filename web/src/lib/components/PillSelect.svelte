<script lang="ts">
  // The square picker beside a board's view strip: scope, sort, genre, monitor
  // level. The same 28px height as `Segmented.svelte`, so a row that mixes the
  // two reads as one row.
  //
  // A native `<select>` under a styled shell, not the `ui/select` picker: that
  // one is a 34px field-height control built for a form. A control this short has
  // no room for `ui/select`'s own trigger chrome, and a native element already
  // gives keyboard support and a platform list for free.
  //
  // `quiet` drops the border for an ordering or a preference rather than a
  // narrowing: sort and genre are not filters, they do not change what is in
  // the list, only how it reads, so they carry no chrome of their own. A
  // `prefix` word in front names the control.

  type Option = { value: string; name: string };

  let {
    options,
    value,
    onchange,
    label,
    quiet = false,
    prefix,
    class: className = ''
  }: {
    // A width, for a list whose longest option would make the closed control
    // wider than the room it has. The chosen name is cut with an ellipsis.
    class?: string;
    options: Option[];
    value: string;
    onchange: (value: string) => void;
    // There is no visible label beside the control: the chosen option's own name
    // is the only text shown — so screen readers need one passed in.
    label: string;
    quiet?: boolean;
    // The word shown before a quiet select, e.g. "Sort" or "Genre". Ignored
    // when `quiet` is false.
    prefix?: string;
  } = $props();
</script>

<div class="relative inline-flex h-7 shrink-0 items-center gap-1.5 {className}">
  {#if quiet && prefix}
    <span class="text-body text-ink-4">{prefix}</span>
  {/if}
  <select
    aria-label={label}
    onchange={(event) => onchange(event.currentTarget.value)}
    class={quiet
      ? 'h-7 min-w-0 appearance-none truncate bg-transparent py-0 pl-0 pr-4 text-body text-ink-2 outline-none transition hover:text-ink'
      : 'h-full min-w-0 w-full appearance-none truncate border border-rule bg-raised py-0 pl-2.5 pr-6 text-body text-ink-2 outline-none transition hover:border-rule-strong hover:text-ink'}
  >
    {#each options as option (option.value)}
      <option value={option.value} selected={option.value === value}>{option.name}</option>
    {/each}
  </select>
  <!-- The chevron a native select would have drawn itself, before
       `appearance: none` took it away. Decorative: the select already says
       "this opens a list" to anyone using a screen reader. -->
  <svg
    aria-hidden="true"
    class="pointer-events-none absolute {quiet ? 'right-0.5' : 'right-2.5'} text-ink-3"
    width="10"
    height="10"
    viewBox="0 0 16 16"
    fill="none"
    stroke="currentColor"
    stroke-width="1.8"
  >
    <path d="M3.5 6l4.5 4.5L12.5 6" />
  </svg>
</div>
