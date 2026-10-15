<script lang="ts">
  import Icon from './Icon.svelte';
  import Button from './Button.svelte';
  import type { IconName } from '$lib/icons';
  import type { AcquiredCopy, ReviewItem } from '$lib/api';
  import { clock, namesDiffer, verdict, type CopyColumn } from '$lib/review';
  import { relativeTime } from '$lib/utils';

  // What was wished for beside every copy, one row per thing that can agree
  // or differ, so a reader compares by reading across a line instead of
  // holding one card in their head while they read the next (ADR Duoton,
  // halftone tables). A row only one side can speak to shows a dash on the
  // other: an absent value is silence, not agreement.

  let {
    want,
    columns,
    chosen,
    deciding,
    onchoose,
    onplay,
    onaccept
  }: {
    want: ReviewItem;
    columns: CopyColumn[];
    chosen: number;
    deciding: boolean;
    onchoose: (index: number) => void;
    onplay: (index: number) => void;
    onaccept: (index: number) => void;
  } = $props();

  type Mark = 'ok' | 'bad' | 'between' | 'none' | 'plain';
  type Cell = { text: string; mark: Mark; tip?: string };
  type Row = { key: string; label: string; icon: IconName; wished: Cell; copies: Cell[] };

  const plain = (text: string | null | undefined, tip?: string): Cell => ({
    text: text?.trim() || '—',
    mark: text?.trim() ? 'plain' : 'none',
    tip
  });
  const judged = (copy: AcquiredCopy, label: string, text: string | undefined): Cell => {
    const said = verdict(copy, label);
    if (!text?.trim()) return { text: '—', mark: 'none', tip: 'Not in the file' };
    return {
      text,
      mark: said === 'ok' ? 'ok' : said === 'bad' ? 'bad' : 'plain',
      tip: said === 'ok' ? 'Agrees' : said === 'bad' ? 'Differs' : undefined
    };
  };
  const named = (fileSide: string | undefined, wished: string | undefined): Cell =>
    !fileSide?.trim()
      ? { text: '—', mark: 'none' }
      : namesDiffer(fileSide, wished)
        ? { text: fileSide, mark: 'bad', tip: 'Differs' }
        : { text: fileSide, mark: 'ok', tip: 'Agrees' };

  function acoustid(copy: AcquiredCopy): Cell {
    const acoustic = copy.evidence?.acoustic;
    if (!acoustic) return { text: '—', mark: 'none', tip: 'Not asked' };
    if (acoustic.unavailable) return { text: 'Unreachable', mark: 'none', tip: acoustic.unavailable };
    if (acoustic.agrees === true) return { text: 'Same song', mark: 'ok' };
    if (acoustic.agrees === false) return { text: 'Other song', mark: 'bad', tip: 'AcoustID names another recording' };
    if ((acoustic.clusters?.[0]?.recordingIds?.length ?? 0) > 1) {
      return { text: 'Also other songs', mark: 'between', tip: 'The same audio belongs to more than one recording' };
    }
    if ((acoustic.recordingIds?.length ?? 0) === 0) return { text: 'Unknown', mark: 'none', tip: 'AcoustID knows no recording for this audio' };
    return { text: 'Too weak', mark: 'between', tip: 'AcoustID answered, but not clearly enough to count' };
  }

  function sample(copy: AcquiredCopy): Cell {
    const anchor = copy.evidence?.anchor;
    const by = anchor?.label ? `From ${anchor.label}` : undefined;
    if (!anchor || anchor.unavailable) return { text: '—', mark: 'none', tip: anchor?.unavailable };
    if (anchor.agrees === true) return { text: 'Same', mark: 'ok', tip: by };
    if (anchor.agrees === false) return { text: 'Different', mark: 'bad', tip: by };
    if (anchor.measured) return { text: 'In between', mark: 'between', tip: by };
    return { text: 'Not compared', mark: 'none', tip: by };
  }

  const LOSSLESS = new Set(['FLAC', 'WAV', 'ALAC', 'AIFF', 'APE']);
  function file(copy: AcquiredCopy): Cell {
    const dot = copy.name.lastIndexOf('.');
    const ext = dot > 0 ? copy.name.slice(dot + 1).toUpperCase() : '';
    const rate = copy.evidence?.bitRate ?? 0;
    const format = ext && !LOSSLESS.has(ext) && rate > 0 ? `${ext} ${rate}` : ext;
    const bytes = copy.sizeBytes ?? copy.evidence?.sizeBytes ?? 0;
    const size = bytes ? (bytes >= 1e9 ? `${(bytes / 1e9).toFixed(1)} GB` : `${(bytes / 1e6).toFixed(1)} MB`) : '';
    const damaged = copy.evidence?.acoustic?.damaged;
    const transcode = copy.evidence?.audio?.transcodeSuspected;
    const text = [format, size].filter(Boolean).join(' · ') || '—';
    if (damaged) return { text, mark: 'bad', tip: `Damaged: ${damaged}` };
    if (transcode) return { text, mark: 'bad', tip: 'Looks like a transcode' };
    return { text, mark: 'plain', tip: copy.name };
  }

  const target = $derived(want.target);
  const sampleSource = $derived(
    columns.map(({ copy }) => copy.evidence?.anchor?.source).find(Boolean)
  );
  const noSample = $derived(
    target.anchorUnavailable
      ? target.anchorNextAttemptAt
        ? plain(`Retry ${relativeTime(target.anchorNextAttemptAt)}`, target.anchorUnavailable)
        : { text: 'None', mark: 'none' as Mark, tip: target.anchorUnavailable }
      : sampleSource
        ? plain('Yes', `From ${sampleSource}`)
        : plain('')
  );

  const rows = $derived.by(() => {
    const list: Row[] = [];
    const per = (pick: (column: CopyColumn) => Cell) => columns.map(pick);
    list.push({
      key: 'length',
      label: 'Length',
      icon: 'waiting',
      wished: plain(target.durationMs ? clock(target.durationMs / 1000) : ''),
      copies: per(({ copy }) => {
        const ms = copy.evidence?.observed?.durationMs;
        return judged(copy, 'duration', ms ? clock(ms / 1000) : undefined);
      })
    });
    list.push({
      key: 'title',
      label: 'Title',
      icon: 'track',
      wished: plain(target.title),
      copies: per(({ copy, filed }) =>
        filed ? named(want.fileTitle, target.title) : judged(copy, 'title', copy.evidence?.observed?.title)
      )
    });
    list.push({
      key: 'artist',
      label: 'Artist',
      icon: 'artist',
      wished: plain(target.artist),
      copies: per(({ copy, filed }) =>
        filed ? named(want.fileArtist, target.artist) : judged(copy, 'artist', copy.evidence?.observed?.artist)
      )
    });
    list.push({
      key: 'album',
      label: 'Album',
      icon: 'release',
      wished: plain(target.album),
      copies: per(({ copy, filed }) =>
        filed ? plain(want.fileReleaseTitle) : judged(copy, 'album', copy.evidence?.observed?.album)
      )
    });
    if (target.isrc || columns.some(({ copy }) => copy.evidence?.observed?.isrc)) {
      list.push({
        key: 'isrc',
        label: 'ISRC',
        icon: 'search',
        wished: plain(target.isrc),
        copies: per(({ copy }) => judged(copy, 'ISRC', copy.evidence?.observed?.isrc))
      });
    }
    if (!columns.every(({ filed }) => filed)) {
      list.push({
        key: 'acoustid',
        label: 'AcoustID',
        icon: 'fingerprint',
        wished: { text: '', mark: 'none' },
        copies: per(({ copy, filed }) => (filed ? plain('') : acoustid(copy)))
      });
      list.push({
        key: 'sample',
        label: 'Sample',
        icon: 'search-check',
        wished: noSample,
        copies: per(({ copy, filed }) => (filed ? plain('') : sample(copy)))
      });
    }
    if (columns.some(({ copy }) => copy.evidence?.witness?.measured)) {
      list.push({
        key: 'witness',
        label: 'Your file',
        icon: 'library',
        wished: { text: '', mark: 'none' },
        copies: per(({ copy }) =>
          copy.evidence?.witness?.agrees
            ? { text: 'Same audio', mark: 'ok', tip: copy.evidence.witness.path }
            : { text: '—', mark: 'none' }
        )
      });
    }
    list.push({
      key: 'file',
      label: 'File',
      icon: 'download',
      wished: { text: '', mark: 'none' },
      copies: per(({ copy }) => file(copy))
    });
    return list;
  });

  const ICONS: Partial<Record<Mark, IconName>> = { ok: 'check', bad: 'close', between: 'alert' };
  const TONES: Record<Mark, string> = {
    ok: 'text-ink',
    bad: 'text-fail',
    between: 'text-decide',
    none: 'text-ink-4',
    plain: 'text-ink'
  };
  const MARK_TONES: Partial<Record<Mark, string>> = { ok: 'text-ok', bad: 'text-fail', between: 'text-decide' };

  /** Roves the checked column with the arrow keys, as a radio group does. */
  function onRadioKeydown(event: KeyboardEvent) {
    if (!['ArrowLeft', 'ArrowRight', 'ArrowUp', 'ArrowDown'].includes(event.key)) return;
    const target = event.target as HTMLElement;
    if (!target.matches('[role="radio"]')) return;
    event.preventDefault();
    const radios = Array.from(
      (event.currentTarget as HTMLElement).querySelectorAll<HTMLElement>('[role="radio"]')
    );
    const from = Math.max(radios.indexOf(target), 0);
    const delta = event.key === 'ArrowLeft' || event.key === 'ArrowUp' ? -1 : 1;
    const next = (((from + delta) % radios.length) + radios.length) % radios.length;
    onchoose(next);
    radios[next]?.focus();
  }
</script>

{#snippet cell(value: Cell, on: boolean)}
  <td
    class="max-w-[16rem] border-b border-line-thin px-3.5 py-2 align-top {on ? 'col-on' : ''}"
    title={value.tip}
  >
    <span class="flex min-w-0 items-center gap-1.5 {TONES[value.mark]}">
      {#if ICONS[value.mark]}
        <Icon
          name={ICONS[value.mark]!}
          size={12}
          class="shrink-0 {MARK_TONES[value.mark]}"
          aria-label={value.mark === 'ok' ? 'agrees' : value.mark === 'bad' ? 'differs' : 'unsure'}
        />
      {/if}
      <span class="truncate" aria-invalid={value.mark === 'bad' ? 'true' : undefined}>{value.text}</span>
    </span>
  </td>
{/snippet}

<div class="overflow-x-auto" role="radiogroup" aria-label="Copies" tabindex="-1" onkeydown={onRadioKeydown}>
  <table class="copy-table w-full border-separate border-spacing-0 text-quiet-meta">
    <thead>
      <tr>
        <th class="w-28 border-b border-line-regular"></th>
        <th class="wished border-b border-line-regular px-3.5 py-2.5 text-left align-bottom font-normal">
          <span class="flex items-center gap-1.5 font-semibold text-ink-2">
            <Icon name="want" size={14} />
            Wished
          </span>
        </th>
        {#each columns as column, index (column.copy.id)}
          {@const on = index === chosen}
          <th
            class="min-w-[11rem] border-b border-line-regular px-3.5 py-2.5 text-left align-bottom font-normal {on
              ? 'col-on'
              : ''}"
          >
            <span class="flex items-center gap-2.5">
              <button
                type="button"
                onclick={() => onplay(index)}
                aria-label={`Play ${column.heading}`}
                title="Play"
                class="flex size-8 shrink-0 items-center justify-center rounded-full border border-line-regular bg-surface-thick text-ink transition hover:bg-surface-ultra"
              >
                <Icon name="play" size={14} />
              </button>
              <span
                role="radio"
                aria-checked={on}
                aria-label={column.heading}
                tabindex={on ? 0 : -1}
                onclick={() => onchoose(index)}
                onkeydown={(event) => {
                  if (event.key === ' ' || event.key === 'Enter') {
                    event.preventDefault();
                    onchoose(index);
                  }
                }}
                class="flex min-w-0 cursor-pointer flex-col"
              >
                <span class="font-semibold {on ? 'text-ink' : 'text-ink-2'}">{column.heading}</span>
                <span class="flex items-center gap-1 truncate text-meta text-ink-4">
                  <Icon name={column.filed ? 'library' : 'download'} size={11} />
                  {column.filed
                    ? (want.fileTitle ? `as “${want.fileTitle}”` : 'in your library')
                    : (column.copy.origin?.label ?? column.copy.username)}
                  {#if column.rips > 1}<span class="numeric">· {column.rips} rips</span>{/if}
                </span>
              </span>
            </span>
          </th>
        {/each}
      </tr>
    </thead>
    <tbody>
      {#each rows as row (row.key)}
        <tr>
          <th scope="row" class="border-b border-line-thin py-2 pr-3 text-left font-normal text-ink-3">
            <span class="flex items-center gap-2">
              <Icon name={row.icon} size={14} />
              {row.label}
            </span>
          </th>
          <td class="wished max-w-[16rem] border-b border-line-thin px-3.5 py-2 align-top" title={row.wished.tip}>
            <span class="block truncate {row.wished.mark === 'none' ? 'text-ink-4' : 'text-ink-2'}">
              {row.wished.text}
            </span>
          </td>
          {#each row.copies as value, index (index)}
            {@render cell(value, index === chosen)}
          {/each}
        </tr>
      {/each}
    </tbody>
    <tfoot>
      <tr>
        <td></td>
        <td class="wished"></td>
        {#each columns as column, index (column.copy.id)}
          <td class="px-3.5 pt-3 pb-1 {index === chosen ? 'col-on' : ''}">
            <Button
              variant={index === chosen && !column.doubted ? 'primary' : 'outline'}
              disabled={deciding}
              aria-label={`Accept ${column.heading}`}
              onclick={() => onaccept(index)}
            >
              <Icon name="check" size={14} />
              Accept
            </Button>
          </td>
        {/each}
      </tr>
    </tfoot>
  </table>
</div>

<style>
  /* What was wished for is the measure, not an answer: a dashed frame. */
  .wished {
    border-left: 1px dashed var(--color-line-thick);
    border-right: 1px dashed var(--color-line-thick);
  }
  /* The chosen copy, washed in the page's light ink. Which column is chosen
     is a place, not a state, so it takes the chrome ink. */
  .col-on {
    background-color: color-mix(in srgb, var(--color-duo-light) 7%, transparent);
  }
  /* Every other row printed with a fine dot screen (ADR Duoton, halftone tables). */
  .copy-table tbody tr:nth-child(odd) > * {
    background-image: radial-gradient(circle, rgb(255 255 255 / 0.035) 1px, transparent 1.3px);
    background-size: 4px 4px;
  }
</style>
