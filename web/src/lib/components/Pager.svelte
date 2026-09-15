<script lang="ts">
  import { ChevronFirst, ChevronLast } from '@lucide/svelte';
  import Button from '$lib/components/Button.svelte';

  // Four pages paginate and four had written this out by hand, agreeing on the
  // shape and disagreeing on the padding. It sits on the list's inset with
  // 12px all round, so the gap under the last row matches the gap above it.

  let {
    total,
    offset,
    pageSize,
    onchange
  }: {
    total: number;
    offset: number;
    pageSize: number;
    onchange: (offset: number) => void;
  } = $props();

  // Which page of how many, said in the terms a reader thinks in. The range of
  // rows says what is under the pointer; the page number says where that is in
  // the list, which is the thing Previous and Next alone could never answer.
  // Three thousand five hundred files at twenty-five a page is a hundred and
  // forty-two pages, and a reader who had walked to page ninety had no way to
  // know it and no way back except ninety presses.
  const pages = $derived(Math.max(1, Math.ceil(total / pageSize)));
  const at = $derived(Math.floor(offset / pageSize) + 1);
  const lastOffset = $derived((pages - 1) * pageSize);
</script>

<!-- Nothing is drawn for a list that fits on one page. The range would only
     repeat the total the page header already carries, and two dead buttons
     under a short list are chrome in the way. -->
{#if total > pageSize}
  <div class="flex flex-wrap items-center justify-between gap-x-4 gap-y-2 border-t border-line-thin p-3">
    <span class="text-meta text-ink-3">
      <span class="numeric">
        {offset + 1}–{Math.min(offset + pageSize, total)} of {total.toLocaleString()}
      </span>
      <span class="text-ink-4">·</span>
      page <span class="numeric">{at.toLocaleString()}</span> of
      <span class="numeric">{pages.toLocaleString()}</span>
    </span>
    <div class="flex gap-2">
      <!-- The two ends of the list, which Previous and Next cannot reach from
           the middle of a long one at any price a person will pay. -->
      <Button
        variant="ghost"
        size="sm"
        icon
        title="First page"
        aria-label="First page"
        disabled={offset === 0}
        onclick={() => onchange(0)}
      >
        <ChevronFirst size={14} />
      </Button>
      <Button
        variant="outline"
        size="sm"
        disabled={offset === 0}
        onclick={() => onchange(Math.max(0, offset - pageSize))}
      >
        Previous
      </Button>
      <Button
        variant="outline"
        size="sm"
        disabled={offset + pageSize >= total}
        onclick={() => onchange(offset + pageSize)}
      >
        Next
      </Button>
      <Button
        variant="ghost"
        size="sm"
        icon
        title="Last page"
        aria-label="Last page"
        disabled={offset + pageSize >= total}
        onclick={() => onchange(lastOffset)}
      >
        <ChevronLast size={14} />
      </Button>
    </div>
  </div>
{/if}
