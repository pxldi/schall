<script lang="ts">
  import { createMutation, createQuery, useQueryClient } from '@tanstack/svelte-query';
  import { Check, LoaderCircle } from '@lucide/svelte';
  import { api, transcodeBitrates, type ImportSettings, type TranscodeBitrate } from '$lib/api';
  import Button from '$lib/components/Button.svelte';
  import Card from '$lib/components/Card.svelte';
  import Chip from '$lib/components/Chip.svelte';
  import ErrorNote from '$lib/components/ErrorNote.svelte';
  import * as Select from '$lib/components/ui/select';

  // Transcode after import: the setting that writes a lossless import,
  // or a lossy one above a chosen rate, into the library as MP3 instead of as
  // it arrived. It runs strictly after a file is proven and imported — every
  // identification is made on the bytes that arrived, and this never runs
  // before or instead of that — and it is off until somebody turns it on.
  //
  // This card is its own component rather than more of SettingsSections.svelte
  // because that file is already thousands of lines; it reads and writes the
  // same 'import-settings' query the retention and AcoustID cards on the
  // Library screen do, so the three stay in step with each other and with the
  // server's "saved as a whole" policy.

  const queryClient = useQueryClient();

  const imports = createQuery({
    queryKey: ['import-settings'],
    queryFn: api.importSettings
  });

  const sweep = createQuery({
    queryKey: ['library-transcode'],
    queryFn: api.transcodeSweep,
    // A running pass announces itself over the event stream; this is the
    // fallback for a stream that never arrived.
    refetchInterval: (query) =>
      ['queued', 'running'].includes(query.state.data?.status ?? '') ? 15_000 : false
  });

  const transcoderPresent = $derived($imports.data ? Boolean($imports.data.transcoderPresent) : true);
  const enabled = $derived(Boolean($imports.data?.transcodeEnabled));
  const bitrate = $derived<TranscodeBitrate>($imports.data?.transcodeBitrate ?? '320');
  const when = $derived<ImportSettings['transcodeWhen']>($imports.data?.transcodeWhen ?? 'lossless');

  // Saved as a whole, the same way the retention and AcoustID cards save it:
  // whatever this card did not just change is carried through from what is
  // already stored, so pressing a control here cannot reset a policy set on
  // one of those two cards.
  const savePolicy = createMutation({
    mutationFn: (input: {
      enabled?: boolean;
      bitrate?: TranscodeBitrate;
      when?: ImportSettings['transcodeWhen'];
    }) =>
      api.saveImportSettings(
        $imports.data?.sourceRetention ?? 'keep',
        { enabled: $imports.data?.acoustidEnabled },
        {
          enabled: input.enabled ?? enabled,
          target: 'mp3',
          bitrate: input.bitrate ?? bitrate,
          when: input.when ?? when
        }
      ),
    onSuccess: (data: ImportSettings) => {
      queryClient.setQueryData(['import-settings'], data);
    }
  });

  const queueSweep = createMutation({
    mutationFn: api.queueTranscodeSweep,
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['library-transcode'] });
    }
  });

  const sweeping = $derived(['queued', 'running'].includes($sweep.data?.status ?? ''));
  const eligible = $derived($sweep.data?.eligible ?? 0);
  const sweepDone = $derived(
    ($sweep.data?.transcoded ?? 0) + ($sweep.data?.skipped ?? 0) + ($sweep.data?.failed ?? 0)
  );

  const bitrateChoices = transcodeBitrates.map((value) => ({
    value,
    label: value === 'V0' ? 'V0 (variable, ~245 kbps)' : `${value} kbps`
  }));
</script>

{#if $imports.isError}
  <!-- A fetch failure is not a report that ffmpeg is missing: without data
       `transcoderPresent` reads false and would otherwise draw that message
       for the wrong reason. -->
  <ErrorNote error={$imports.error} retry={() => $imports.refetch()} />
{:else}
  <Card>
    <div class="flex flex-wrap items-center gap-2.5">
      <h2 class="font-display text-lead font-bold text-ink">Transcode after import</h2>
      {#if enabled}
        <Chip role="ok">on</Chip>
      {:else}
        <Chip role="idle">off</Chip>
      {/if}
      <label
        class="ml-auto flex cursor-pointer items-center gap-2 text-meta font-medium text-ink-2"
      >
        <input
          type="checkbox"
          checked={enabled}
          disabled={$imports.isPending || ((!transcoderPresent && !enabled) || $savePolicy.isPending)}
          onchange={(event) => $savePolicy.mutate({ enabled: event.currentTarget.checked })}
          class="check cursor-pointer"
        />
        enabled
      </label>
    </div>

    <span class="text-meta text-ink-3">
      Runs after a file is proven and imported; the original is deleted only once the MP3 is
      written and checked.
    </span>

    {#if !transcoderPresent}
      <span class="text-meta text-ink-3">
        {$imports.data?.transcoderDetail ?? 'ffmpeg could not be found'}, so nothing can be
        shrunk. Install ffmpeg to use this setting.
      </span>
    {/if}

    {#if $savePolicy.isError}
      <ErrorNote error={$savePolicy.error} />
    {/if}

    <div class="flex flex-col">
      <button
        type="button"
        class="group flex items-start gap-2.5 border-b border-line-thin py-2.5 text-left"
        disabled={$imports.isPending || $savePolicy.isPending}
        onclick={() => $savePolicy.mutate({ when: 'lossless' })}
      >
        <span class="mt-px grid size-6 shrink-0 place-items-center">
          <span
            class="grid size-[15px] place-items-center rounded-tight transition {when === 'lossless'
              ? 'bg-accent'
              : 'border border-line-thick group-hover:border-line-live'}"
          >
            {#if when === 'lossless'}<Check
                size={10}
                strokeWidth={3.6}
                class="text-accent-ink"
              />{/if}
          </span>
        </span>
        <span class="flex min-w-0 flex-col gap-1">
          <span class="text-body font-medium {when === 'lossless' ? 'text-ink' : 'text-ink-2'}">
            Lossless files only
          </span>
          <span class="text-meta text-ink-3">
            FLAC, WAV, ALAC, AIFF and the like — nothing already lossy is re-encoded
          </span>
        </span>
      </button>
      <button
        type="button"
        class="group flex items-start gap-2.5 py-2.5 text-left"
        disabled={$imports.isPending || $savePolicy.isPending}
        onclick={() => $savePolicy.mutate({ when: 'above_target' })}
      >
        <span class="mt-px grid size-6 shrink-0 place-items-center">
          <span
            class="grid size-[15px] place-items-center rounded-tight transition {when === 'above_target'
              ? 'bg-accent'
              : 'border border-line-thick group-hover:border-line-live'}"
          >
            {#if when === 'above_target'}<Check
                size={10}
                strokeWidth={3.6}
                class="text-accent-ink"
              />{/if}
          </span>
        </span>
        <span class="flex min-w-0 flex-col gap-1">
          <span
            class="text-body font-medium {when === 'above_target' ? 'text-ink' : 'text-ink-2'}"
          >
            Lossless files, and lossy ones above the target rate
          </span>
          <span class="text-meta text-ink-3">
            also re-encodes a lossy file — MP3, AAC, Opus — whose own bit rate is higher than the
            quality below
          </span>
        </span>
      </button>
    </div>

    <div class="flex flex-wrap items-center gap-2.5">
      <label for="transcode-bitrate" class="text-meta font-medium text-ink">MP3 quality</label>
      <Select.Root
        type="single"
        items={bitrateChoices}
        value={bitrate}
        onValueChange={(next) => $savePolicy.mutate({ bitrate: next as TranscodeBitrate })}
      >
        <Select.Trigger id="transcode-bitrate" class="w-52" disabled={$imports.isPending}>
          {bitrateChoices.find((choice) => choice.value === bitrate)?.label}
        </Select.Trigger>
        <Select.Content>
          {#each bitrateChoices as choice (choice.value)}
            <Select.Item value={choice.value} label={choice.label}>{choice.label}</Select.Item>
          {/each}
        </Select.Content>
      </Select.Root>
    </div>
  </Card>

  <Card>
    <div class="flex flex-wrap items-center gap-2.5">
      <h2 class="font-display text-lead font-bold text-ink">Transcode the library</h2>
      <Button
        variant="outline"
        size="sm"
        class="ml-auto"
        disabled={$imports.isPending ||
          $sweep.isPending ||
          sweeping ||
          $queueSweep.isPending ||
          !enabled ||
          !transcoderPresent ||
          eligible === 0}
        onclick={() => $queueSweep.mutate()}
      >
        {#if sweeping || $queueSweep.isPending}<LoaderCircle size={12} class="animate-spin" />{/if}
        Transcode the library
      </Button>
    </div>

    <span class="text-meta text-ink-3">
      Shrinks the files already in the library, the same way, in small batches that pick up where
      they left off.
    </span>

    <span class="text-meta text-ink-3">
      {#if !enabled}
        turn re-encoding on above to use this
      {:else if eligible === 0}
        no files in the library need to be shrunk under the current setting
      {:else}
        {eligible.toLocaleString()} files are eligible and would be shrunk
      {/if}
    </span>

    {#if $sweep.data && $sweep.data.status !== 'idle'}
      <span class="numeric text-meta text-ink-3">
        {$sweep.data.transcoded.toLocaleString()} shrunk · {$sweep.data.skipped.toLocaleString()} left
        alone · {$sweep.data.failed.toLocaleString()} could not be shrunk
      </span>
    {/if}

    {#if sweeping}
      <span class="numeric text-meta text-ink-3">
        {sweepDone.toLocaleString()} of {eligible.toLocaleString()} files
      </span>
    {/if}

    {#if $queueSweep.isError}
      <ErrorNote error={$queueSweep.error} />
    {/if}
  </Card>
{/if}
