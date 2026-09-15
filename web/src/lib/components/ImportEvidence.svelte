<script lang="ts">
  import { AlertTriangle, Check, FileQuestion, Undo2, UserRoundCheck } from '@lucide/svelte';
  import type {
    ImportDecision,
    ImportEvidence,
    ImportFileEvidence,
    ImportTags
  } from '$lib/api';
  import Button from '$lib/components/Button.svelte';
  import * as Select from '$lib/components/ui/select';

  let {
    evidence,
    decisions,
    pending = false,
    onresolve,
    onwithdraw
  }: {
    evidence: ImportEvidence;
    decisions: ImportDecision[];
    pending?: boolean;
    onresolve: (fileName: string, trackId: string) => void;
    onwithdraw: (decisionId: string) => void;
  } = $props();

  const files = $derived(evidence.files ?? []);
  const unmatched = $derived(evidence.unmatchedTracks ?? []);
  const problems = $derived(evidence.problems ?? []);
  // Files that disagree are the reason review exists, so they lead. Matching
  // files stay visible below them: a release is judged as a whole.
  const ordered = $derived([
    ...files.filter((file) => file.problems.length),
    ...files.filter((file) => !file.problems.length)
  ]);

  // Every catalogue track the evidence mentions, in playing order. A file can
  // only be resolved to a track of the release it was requested for, so this
  // is the whole set of answers review can give.
  const catalogue = $derived.by(() => {
    const byId = new Map<string, ImportTags>();
    for (const tags of [...files.map((file) => file.expected), ...unmatched]) {
      if (tags?.trackId) byId.set(tags.trackId, tags);
    }
    return [...byId.values()].sort(
      (left, right) =>
        (left.discNumber ?? 1) - (right.discNumber ?? 1) ||
        (left.trackNumber ?? 0) - (right.trackNumber ?? 0)
    );
  });

  let chosen = $state<Record<string, string>>({});

  function decisionFor(file: ImportFileEvidence) {
    return decisions.find((decision) => decision.fileName === file.name);
  }

  // The picker opens on the answer the evidence came closest to, so the common
  // case is confirming a judgement rather than hunting for it in a list.
  function selection(file: ImportFileEvidence) {
    return (
      chosen[file.name] ||
      file.expected?.trackId ||
      file.candidates?.find((candidate) => !candidate.takenBy)?.track.trackId ||
      catalogue[0]?.trackId ||
      ''
    );
  }

  function duration(value?: number) {
    if (!value) return '';
    const total = Math.round(value / 1000);
    return `${Math.floor(total / 60)}:${String(total % 60).padStart(2, '0')}`;
  }

  function position(tags?: ImportTags, fallback = '') {
    if (!tags?.trackNumber) return fallback;
    return `${tags.discNumber ?? 1}-${tags.trackNumber}`;
  }

  function describe(tags: ImportTags | undefined, missing: string) {
    if (!tags) return null;
    const context = [tags.artist, tags.album].filter(Boolean).join(' — ');
    return { title: tags.title || missing, context, length: duration(tags.durationMs) };
  }

  function trackLabel(tags: ImportTags) {
    const length = duration(tags.durationMs);
    return `${position(tags)} · ${tags.title}${length ? ` · ${length}` : ''}`;
  }

  // The same tracks written as choices. The picker needs the list twice: once
  // to draw the rows, once to answer a letter typed while the list is shut.
  const catalogueChoices = $derived(
    catalogue.map((track) => ({ value: track.trackId ?? '', label: trackLabel(track) }))
  );
</script>

<div class="mt-3 border-t border-line-regular pt-3">
  <p class="text-meta font-semibold uppercase tracking-[0.18em] text-decide/70">
    What validation compared
  </p>

  {#if problems.length}
    <ul class="mt-2 space-y-1">
      {#each problems as problem}
        <li class="flex items-start gap-2 text-meta text-decide/90">
          <AlertTriangle size={12} class="mt-0.5 shrink-0" />
          <span>{problem}</span>
        </li>
      {/each}
    </ul>
  {/if}

  <div class="mt-3 overflow-x-auto">
    <table class="w-full min-w-[34rem] border-separate border-spacing-y-1 text-left text-meta">
      <thead class="text-micro uppercase tracking-[0.14em] text-ink-3">
        <tr>
          <!-- This column was drawn as a glyph and a colour, with no heading and
               no name. A reader using a screen reader met a column of nothing,
               and a reader who cannot tell the two greens apart met one tick
               standing for two different things. It is named now, and so is
               every mark in it. -->
          <th class="w-24 pb-1 font-medium">State</th>
          <th class="pb-1 font-medium">Downloaded file</th>
          <th class="pb-1 font-medium">Catalogue track</th>
        </tr>
      </thead>
      <tbody>
        {#each ordered as file (file.name)}
          {@const observed = describe(file.observed, 'No title tag')}
          {@const expected = describe(file.expected, '')}
          {@const decision = decisionFor(file)}
          <tr class="align-top">
            <td class="pr-2 pt-1">
              <!-- Three states, three different marks and three words. The two
                   that used to share a tick and differ only by hue are now a
                   tick and a person with a tick, so nobody has to see the
                   difference in colour to see the difference. -->
              {#if decision}
                <span class="flex items-center gap-1.5 text-busy">
                  <UserRoundCheck size={13} class="shrink-0" />
                  <span>Resolved</span>
                </span>
              {:else if file.problems.length}
                <span class="flex items-center gap-1.5 text-decide">
                  <AlertTriangle size={13} class="shrink-0" />
                  <span>Disagrees</span>
                </span>
              {:else}
                <span class="flex items-center gap-1.5 text-ok">
                  <Check size={13} class="shrink-0" />
                  <span>Agrees</span>
                </span>
              {/if}
            </td>
            <td class="pr-4">
              <p class="truncate font-medium {file.problems.length ? 'text-decide' : 'text-ink-2'}">
                {file.name}
              </p>
              {#if observed}
                <p class="text-ink-3">
                  {#if file.position}<span class="text-ink-4">{file.position}</span> · {/if}
                  {observed.title}
                  {#if observed.length}· {observed.length}{/if}
                </p>
                {#if observed.context}<p class="truncate text-ink-4">{observed.context}</p>{/if}
              {:else}
                <p class="text-ink-4">No readable tags</p>
              {/if}
            </td>
            <td>
              {#if expected}
                <p class="font-medium text-ink-2">
                  <span class="text-ink-4">{position(file.expected)}</span>
                  · {expected.title}
                </p>
                {#if expected.length}<p class="text-ink-4">{expected.length}</p>{/if}
                {#if file.match}
                  <p class="text-ok/70">{file.match.summary}</p>
                  {#each file.match.notes ?? [] as note}
                    <p class="text-ink-4">{note}</p>
                  {/each}
                {/if}
              {:else if file.candidates?.length}
                <!-- Matching would not choose between these; review can. -->
                <p class="text-ink-3">Could be</p>
                <ul class="space-y-0.5">
                  {#each file.candidates as candidate}
                    <li>
                      <span class="text-ink-2">{trackLabel(candidate.track)}</span>
                      {#if candidate.differs.length}
                        <span class="text-decide/70">
                          · {candidate.differs.join(', ')}
                          {candidate.differs.length === 1 ? 'disagrees' : 'disagree'}
                        </span>
                      {/if}
                      {#if candidate.takenBy}
                        <span class="text-ink-4"> · already matched by {candidate.takenBy}</span>
                      {/if}
                    </li>
                  {/each}
                </ul>
              {:else}
                <p class="flex items-center gap-1.5 text-ink-4">
                  <FileQuestion size={12} /> No track of this edition
                </p>
              {/if}
            </td>
          </tr>
          {#if file.problems.length && !decision}
            <tr>
              <td></td>
              <td colspan="2" class="pb-1">
                <ul class="space-y-0.5 text-decide/80">
                  {#each file.problems as problem}<li>{problem}</li>{/each}
                </ul>
                {#if file.resolvable && catalogue.length}
                  <div class="mt-1.5 flex flex-wrap items-center gap-2">
                    <label class="text-ink-3" for={`resolve-${file.name}`}>This file is</label>
                    <Select.Root
                      type="single"
                      items={catalogueChoices}
                      value={selection(file)}
                      onValueChange={(next) => (chosen[file.name] = next)}
                    >
                      <!-- The evidence rows are dense and this control sits in
                           one of them, so it keeps the row's size and its
                           quieter ink rather than a form field's. -->
                      <Select.Trigger
                        id={`resolve-${file.name}`}
                        class="h-auto w-auto max-w-[18rem] px-2 py-1 text-meta text-ink-2"
                      >
                        {catalogueChoices.find((choice) => choice.value === selection(file))?.label}
                      </Select.Trigger>
                      <Select.Content>
                        {#each catalogueChoices as choice (choice.value)}
                          <Select.Item value={choice.value} label={choice.label}>
                            {choice.label}
                          </Select.Item>
                        {/each}
                      </Select.Content>
                    </Select.Root>
                    <Button
                      variant="ghost"
                      tall
                      disabled={pending || !selection(file)}
                      onclick={() => onresolve(file.name, selection(file))}
                    >
                      Resolve
                    </Button>
                  </div>
                {:else if !file.resolvable}
                  <p class="mt-1 text-ink-4">
                    Not a judgement call. Download the file again.
                  </p>
                {/if}
              </td>
            </tr>
          {/if}
          {#if decision}
            <tr>
              <td></td>
              <td colspan="2" class="pb-1">
                <div class="flex flex-wrap items-center gap-2 text-busy/90">
                  <span>
                    Resolved as “{decision.trackTitle}”{decision.note ? ` — ${decision.note}` : ''}.
                    Validate again to apply it.
                  </span>
                  <button
                    class="flex items-center gap-1 text-ink-3 transition hover:text-ink-2"
                    disabled={pending}
                    onclick={() => onwithdraw(decision.id)}
                  >
                    <Undo2 size={12} /> Undo
                  </button>
                </div>
              </td>
            </tr>
          {/if}
        {/each}
      </tbody>
    </table>
  </div>

  {#if unmatched.length}
    <div class="mt-3 border-t border-line-regular pt-2">
      <p class="text-micro uppercase tracking-[0.14em] text-ink-3">
        Catalogue tracks no file claimed
      </p>
      <ul class="mt-1 space-y-0.5 text-meta text-ink-3">
        {#each unmatched as track}
          <li>
            <span class="text-ink-4">{position(track)}</span>
            · {track.title}
            {#if track.durationMs}· {duration(track.durationMs)}{/if}
          </li>
        {/each}
      </ul>
    </div>
  {/if}
</div>
