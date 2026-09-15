<script lang="ts">
  import { Check, Library, X } from '@lucide/svelte';
  import type { DuplicateEvidence } from '$lib/api';
  import Button from '$lib/components/Button.svelte';

  let {
    evidence,
    pending = false,
    onconfirm,
    oncancel
  }: {
    evidence: DuplicateEvidence;
    pending?: boolean;
    onconfirm: () => void;
    oncancel: () => void;
  } = $props();

  const files = $derived(evidence.files ?? []);
  // Owned files are the settled half of the case and lead. Unresolved files
  // follow, because they are the ones the user can still do something about.
  const owned = $derived(files.filter((file) => file.state === 'owned'));
  const unresolved = $derived(files.filter((file) => file.state === 'unresolved'));
</script>

<div class="rounded-panel border border-line-regular bg-decide/14 px-5 py-4">
  <div class="flex items-start gap-3">
    <Library size={17} class="mt-0.5 shrink-0 text-decide" />
    <div class="min-w-0">
      <p class="text-body font-semibold text-decide">This release may already be in the library</p>
      <p class="mt-1 text-body text-ink-2">{evidence.summary}</p>
    </div>
  </div>

  <!-- The files are listed on hairlines rather than in boxes: this panel is
       already a bordered surface, and a bordered row inside it would be the
       second border deep. -->
  {#if unresolved.length}
    <p class="label mt-4">Not resolved</p>
    <div class="mt-1">
      {#each unresolved as file (file.id)}
        <div class="border-b border-line-thin px-1 py-2 last:border-b-0">
          <p class="truncate text-body text-ink">{file.path}</p>
          <p class="mt-0.5 text-meta text-ink-3">
            {file.tags ? `${file.tags} — ` : ''}{file.evidence}
          </p>
        </div>
      {/each}
    </div>
  {/if}

  {#if owned.length}
    <p class="label mt-4">Already owned</p>
    <div class="mt-1 max-h-56 overflow-auto">
      {#each owned as file (file.id)}
        <div class="border-b border-line-thin px-1 py-2 last:border-b-0">
          <p class="truncate text-body text-ink">{file.path}</p>
          <p class="mt-0.5 text-meta text-ink-3">{file.evidence}</p>
        </div>
      {/each}
    </div>
  {/if}

  <div class="mt-4 flex flex-wrap items-center gap-3 border-t border-line-thin pt-4">
    <Button disabled={pending} onclick={onconfirm}>
      <Check size={15} /> Download anyway
    </Button>
    <Button variant="ghost" disabled={pending} onclick={oncancel}>
      <X size={15} /> Keep what I have
    </Button>
    <a href="/library" class="text-meta font-semibold text-ink-3 transition hover:text-ink">
      Open Library
    </a>
  </div>
</div>
