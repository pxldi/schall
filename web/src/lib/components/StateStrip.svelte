<script lang="ts">
  import type { EntryRole } from '$lib/playlists';

  // A list's rows as one dot each, in list order, coloured by their state
  // (ADR Duoton, halftone tables). It shows where the gaps in a long list are
  // before anybody scrolls; the legend under it carries the counts in words,
  // so the dots themselves are hidden from assistive technology. A dot opens
  // its row.

  let {
    items,
    label = 'songs'
  }: {
    items: { id: string; title: string; position: number; state: { label: string; role: EntryRole } }[];
    /** What the dots are, for the legend's sentence. */
    label?: string;
  } = $props();

  const ORDER: EntryRole[] = ['ok', 'decide', 'fail', 'busy', 'idle'];

  // One legend entry per label, in role order, so "complete" leads and the
  // quiet states trail.
  const legend = $derived.by(() => {
    const counts = new Map<string, { label: string; role: EntryRole; count: number }>();
    for (const item of items) {
      const entry = counts.get(item.state.label) ?? { ...item.state, count: 0 };
      entry.count++;
      counts.set(item.state.label, entry);
    }
    return [...counts.values()].sort((a, b) => ORDER.indexOf(a.role) - ORDER.indexOf(b.role) || b.count - a.count);
  });

  function open(id: string) {
    const row = document.getElementById(`entry-${id}`);
    row?.scrollIntoView({ block: 'center', behavior: 'smooth' });
  }
</script>

<div class="flex flex-col gap-2.5">
  <div class="flex flex-wrap gap-[3px]" aria-hidden="true">
    {#each items as item (item.id)}
      <button
        type="button"
        tabindex="-1"
        class="strip-dot"
        data-role={item.state.role}
        title="{item.position}. {item.title} · {item.state.label}"
        onclick={() => open(item.id)}
      ></button>
    {/each}
  </div>
  <p class="flex flex-wrap items-center gap-x-4 gap-y-1 text-meta text-ink-3">
    <span class="sr-only">{items.length.toLocaleString()} {label}:</span>
    {#each legend as entry (entry.label)}
      <span class="inline-flex items-center gap-1.5">
        <span class="strip-dot" data-role={entry.role} aria-hidden="true"></span>
        <span class="numeric font-semibold text-ink">{entry.count.toLocaleString()}</span>
        {entry.label}
      </span>
    {/each}
  </p>
</div>

<style>
  .strip-dot {
    width: 7px;
    height: 7px;
    border-radius: 9999px;
    background: var(--color-line-thick);
    transition: transform var(--motion-state) var(--motion-ease);
  }
  button.strip-dot:hover {
    transform: scale(1.6);
  }
  .strip-dot[data-role='ok'] {
    background: var(--color-ok);
  }
  .strip-dot[data-role='busy'] {
    background: var(--color-busy);
  }
  .strip-dot[data-role='decide'],
  .strip-dot[data-role='fail'] {
    background: var(--color-decide);
  }
</style>
