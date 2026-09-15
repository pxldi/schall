<script lang="ts">
  import { createQuery } from '@tanstack/svelte-query';
  import { goto } from '$app/navigation';
  import { toStore } from 'svelte/store';
  import { Check, LoaderCircle, Search, TriangleAlert, X } from '@lucide/svelte';
  import { api } from '$lib/api';
  import { cn } from '$lib/utils';
  import { highlight, paletteCategories, paletteRows, type PaletteRole } from '$lib/palette';
  import ErrorNote from '$lib/components/ErrorNote.svelte';

  // One search field, summoned from anywhere with ⌘K, answering across the five
  // categories Schall holds at once. It is a front door to browsing that already
  // exists: every row is a link to the page that owns the thing, and the palette
  // never grows a filter, a sort, a second page of results or an action of its
  // own. When the answer is bigger than five, the way forward is out of the
  // palette and into that category's own list with the query pre-filled.
  //
  // It is not a dialog. Nothing behind it is blocked and nothing is dimmed — the
  // frosting does that work, so the page stays visible through the blur and the
  // panel reads as something resting on the page rather than a screen thrown
  // over it. The layer catches presses on the panel and nowhere else.

  // How long a keystroke waits before the question is asked. Long enough that
  // typing a word is one search rather than five, short enough that stopping
  // typing feels like the answer was already there.
  const settleMs = 150;

  let open = $state(false);
  // What is in the box, and what has actually been asked. They differ for as
  // long as a keystroke is settling, and while they differ the panel says it is
  // searching: a stale list under a newer query is the one thing a palette must
  // never show.
  let typed = $state('');
  let asked = $state('');
  let selected = $state(0);
  let box = $state<HTMLInputElement | null>(null);
  let panel = $state<HTMLElement | null>(null);
  // Where the keyboard was when the palette was summoned, so dismissing it is
  // a return rather than a drop to the body. Plain, not $state — nothing in
  // the template reads it.
  let opener: Element | null = null;

  $effect(() => {
    const next = typed.trim();
    if (next === asked) return;
    const timer = setTimeout(() => (asked = next), settleMs);
    return () => clearTimeout(timer);
  });

  // The key carries the query, so an answer can only ever be drawn under the
  // query it answered. The event stream invalidates it when the collection
  // changes; there is no interval, because a palette nobody has open is not
  // waiting for anything.
  const results = createQuery(
    toStore(() => ({
      queryKey: ['global-search', asked],
      queryFn: () => api.search(asked),
      enabled: open && asked.length > 0
    }))
  );

  const settling = $derived(typed.trim() !== asked);
  const answer = $derived(settling ? undefined : $results.data);
  const query = $derived(answer?.query ?? '');
  const categories = $derived(answer ? paletteCategories(answer, query) : []);
  const rows = $derived(paletteRows(categories));

  // The arrow keys move through the results and, for a category the panel
  // could not fit in full, one more stop after them: the row that leads to
  // the rest. It used to be a jump Tab made to whichever category the
  // selection sat in; a row means the same escape is reachable the way every
  // other row is, and Tab is free to leave the field.
  interface NavEntry {
    id: string;
    href: string;
  }
  const entries = $derived(
    categories.flatMap((category): NavEntry[] => [
      ...category.rows.map((row): NavEntry => ({ id: row.id, href: row.href })),
      ...(category.allHref
        ? [{ id: `palette-view-all-${category.key}`, href: category.allHref }]
        : [])
    ])
  );
  const current = $derived(entries[selected]);

  // Asked, and the answer is not back: either the keystroke is still settling or
  // the request is in flight. One waiting line for the whole panel, not a
  // skeleton per category — which categories will answer is not known until they
  // do, and five stacks of grey bars that mostly resolve to nothing is a worse
  // guess than a sentence.
  const waiting = $derived(typed.trim().length > 0 && (settling || $results.isFetching));
  const failed = $derived(!waiting && $results.isError);
  const nothing = $derived(!waiting && !failed && answer !== undefined && rows.length === 0);
  // Summoned and empty, the palette is one line high: the glyph, the placeholder
  // and the way out. No opening blurb, no recent searches, no suggestions, and
  // no line explaining that a query is needed — until there is one there is
  // nothing true to say.
  const hasBody = $derived(waiting || failed || nothing || rows.length > 0);

  // A new answer is read from the top: it is a different list, so where the last
  // one was being looked at means nothing.
  $effect(() => {
    void entries;
    selected = 0;
  });

  // Keeping the selection in view is the panel's job, because the keys that move
  // it are pressed in a field that never scrolls.
  $effect(() => {
    const id = current?.id;
    if (id) document.getElementById(id)?.scrollIntoView({ block: 'nearest' });
  });

  // The mark is the thing's own state, tinted the way every other list in Schall
  // tints one. Achromatic selection, coloured state: colour reports what a thing
  // is, and "the arrow keys are here" is not a state.
  const markTones: Record<PaletteRole, string> = {
    ok: 'bg-ok/14 text-ok',
    idle: 'bg-idle/14 text-idle',
    decide: 'bg-decide/14 text-decide',
    fail: 'bg-fail/14 text-fail'
  };

  const markGlyphs: Record<Exclude<PaletteRole, 'ok'>, string> = {
    idle: '–',
    decide: '?',
    fail: '!'
  };

  const rowClass =
    'relative grid grid-cols-[16px_minmax(0,1fr)_minmax(0,132px)] items-center gap-x-3 ' +
    'rounded-row border-b border-line-thin px-3 py-2 text-inherit no-underline ' +
    'transition-[background] last:border-b-0 hover:bg-surface-thick';

  // The keyboard selection mark: --raise-2, one step above the hover tint, plus
  // a 2px --ink-2 bar at the leading edge. Not the accent, which is chrome and
  // never encodes state, and not the focus ring, which would say keystrokes go
  // to the row when they are still going to the field.
  const chosenClass =
    "bg-surface-thick before:absolute before:inset-y-1 before:left-0 before:w-0.5 " +
    "before:rounded-full before:bg-ink-2 before:content-['']";

  function summon() {
    opener = document.activeElement;
    open = true;
    // The field does not exist yet on the frame the palette opens.
    queueMicrotask(() => box?.focus());
  }

  function dismiss() {
    open = false;
    typed = '';
    asked = '';
    selected = 0;
    // Falls back to the body on its own: the field this focus was on is
    // about to leave the document, and a browser moves focus to the body
    // when the element holding it is removed.
    if (opener instanceof HTMLElement && opener.isConnected) opener.focus();
    opener = null;
  }

  function leaveFor(href: string) {
    dismiss();
    void goto(href);
  }

  // The pointer moves the same one selection the arrow keys do; a palette with
  // two marks on it would be saying enter goes to one of two places.
  function selectRow(id: string) {
    const at = entries.findIndex((entry) => entry.id === id);
    if (at >= 0) selected = at;
  }

  function move(by: number) {
    if (entries.length === 0) return;
    // The list does not wrap at either end.
    selected = Math.min(Math.max(selected + by, 0), entries.length - 1);
  }

  // The button in the sidebar, which is how somebody who does not know the key
  // opens this. It is an event rather than a prop because the palette is
  // mounted by the root layout and the button lives inside the shell, and
  // neither is the other's parent.
  $effect(() => {
    const summoned = () => {
      if (open) box?.select();
      else summon();
    };
    window.addEventListener('schall:search', summoned);
    return () => window.removeEventListener('schall:search', summoned);
  });

  // ⌘K from anywhere. The palette is reachable from every page, so the listener
  // is on the window rather than on any one of them.
  function onWindowKeydown(event: KeyboardEvent) {
    if (!(event.metaKey || event.ctrlKey) || event.key.toLowerCase() !== 'k') return;
    event.preventDefault();
    if (open) box?.select();
    else summon();
  }

  function onFieldKeydown(event: KeyboardEvent) {
    switch (event.key) {
      case 'Escape':
        event.preventDefault();
        // The first esc with text in the field clears the field; the second
        // closes.
        if (typed) {
          typed = '';
          asked = '';
        } else {
          dismiss();
        }
        return;
      case 'ArrowDown':
        event.preventDefault();
        move(1);
        return;
      case 'ArrowUp':
        event.preventDefault();
        move(-1);
        return;
      case 'Enter':
        event.preventDefault();
        if (current) leaveFor(current.href);
        return;
      // Tab is not handled here: it moves focus in the ordinary order, out of
      // the field and into the results, the same as it would in any other
      // list of links. The escape a category's own Tab press used to reach is
      // now the "View all results" row at the end of that category's rows.
    }
  }

  // Nothing behind the palette is blocked, so a press outside it reaches the
  // page as it always would. It closes the palette as well, which is what a
  // press somewhere else means.
  function onWindowPointerdown(event: PointerEvent) {
    if (!open || !panel) return;
    if (!panel.contains(event.target as Node)) dismiss();
  }
</script>

<svelte:window onkeydown={onWindowKeydown} onpointerdown={onWindowPointerdown} />

{#if open}
  <div
    class="pointer-events-none fixed inset-0 z-50 flex justify-center px-[18px] pb-[26px] pt-[62px]"
  >
    <aside
      bind:this={panel}
      aria-label="Search Schall"
      class="palette-panel pointer-events-auto relative flex max-h-full w-[560px] max-w-full flex-col self-start overflow-hidden rounded-panel border border-[rgba(232,233,231,0.09)] bg-[rgba(32,34,39,0.55)]"
    >
      <div
        class={cn(
          'grid h-[34px] shrink-0 grid-cols-[16px_minmax(0,1fr)_28px] items-center gap-x-3 px-3',
          hasBody && 'border-b border-line-thin'
        )}
      >
        <span class="text-ink-4"><Search size={14} /></span>
        <input
          bind:this={box}
          bind:value={typed}
          onkeydown={onFieldKeydown}
          type="text"
          autocomplete="off"
          spellcheck="false"
          placeholder="Search Schall"
          role="combobox"
          aria-label="Search Schall"
          aria-expanded={rows.length > 0}
          aria-controls="palette-results"
          aria-activedescendant={current?.id ?? undefined}
          aria-autocomplete="list"
          class="min-w-0 border-0 bg-transparent p-0 font-mono text-body font-medium text-ink placeholder:text-ink-4 focus:outline-none"
        />
      </div>

      {#if hasBody}
        <div class="min-h-0 flex-1 overflow-y-auto py-2">
          {#if waiting}
            <p class="flex items-center gap-2 px-3 py-3.5 text-meta leading-[1.6] text-ink-4">
              <LoaderCircle size={12} class="animate-spin" />
              Searching five categories…
            </p>
          {:else if failed}
            <!-- A failed query is reported here rather than in a toast: the
                 palette is open, it is under the eye, and it is the thing that
                 failed. Closing it closes the error with it. -->
            <div
              class="mx-3 my-1.5 flex items-start gap-2 rounded-row border border-line-regular bg-fail/14 px-[11px] py-[9px]"
            >
              <TriangleAlert size={12} class="mt-px shrink-0 text-fail" />
              <!-- The button that said Try again did one thing: ask the search
                   the question that had just failed. The palette asks it now,
                   twice, and says it is asking. -->
              <ErrorNote bare error={$results.error} retry={() => void $results.refetch()} />
            </div>
          {:else if nothing}
            <!-- The second sentence explained which rows the search covers. That
                 is how the palette works rather than what the reader can do
                 with it, so it went the way of every other line of
                 documentation that had wandered onto a screen. -->
            <p class="px-3 py-3.5 text-meta leading-[1.6] text-ink-3">
              Nothing matches <span class="text-ink">{query}</span> in your library or the artists
              you follow.
            </p>
          {:else}
            <div id="palette-results" role="listbox" aria-label="Search results" tabindex="-1">
              {#each categories as category, group (category.key)}
                <!-- A category that answered with nothing is absent, not listed
                     as empty. The one that is present is still drawn: the
                     category of a result is never implied by being the only
                     one. -->
                <div role="group" aria-label={category.label} class={cn(group > 0 && 'mt-[14px]')}>
                  <div class="mb-0.5 border-b border-line-thin px-3 pb-1.5">
                    <span
                      class="text-[0.625rem] font-semibold uppercase tracking-[0.08em] text-ink-3"
                      >{category.label}</span
                    >
                  </div>

                  {#each category.rows as row (row.id)}
                    {@const chosen = current?.id === row.id}
                    <a
                      id={row.id}
                      href={row.href}
                      role="option"
                      aria-selected={chosen}
                      class={cn(rowClass, chosen && chosenClass)}
                      onclick={(event) => {
                        event.preventDefault();
                        leaveFor(row.href);
                      }}
                      onmouseenter={() => selectRow(row.id)}
                    >
                      <span
                        aria-hidden="true"
                        class={cn('grid size-4 place-items-center rounded-row', markTones[row.role])}
                      >
                        {#if row.role === 'ok'}
                          <Check size={10} strokeWidth={3.2} />
                        {:else}
                          <span class="font-mono text-micro font-bold leading-none"
                            >{markGlyphs[row.role]}</span
                          >
                        {/if}
                      </span>
                      <span class="flex min-w-0 flex-col gap-0.5">
                        <span class="truncate text-body text-ink"
                          >{#each highlight(row.title, query) as piece, part (part)}<span
                              class={cn(piece.hit && 'font-semibold text-ink')}>{piece.text}</span
                            >{/each}</span
                        >
                        {#if row.subtitle}
                          <span class="truncate text-meta text-ink-3"
                            >{#each highlight(row.subtitle, query) as piece, part (part)}<span
                                class={cn(piece.hit && 'font-semibold text-ink')}>{piece.text}</span
                              >{/each}</span
                          >
                        {/if}
                      </span>
                      <!-- The one figure this category is counted in, in a
                           column the eye can run down. Never a second title. -->
                      <span class="min-w-0 truncate text-right text-meta text-ink-2"
                        >{row.meta}</span
                      >
                    </a>
                  {/each}

                  {#if category.allHref}
                    {@const href = category.allHref}
                    {@const viewAllId = `palette-view-all-${category.key}`}
                    {@const chosen = current?.id === viewAllId}
                    <!-- The row a category that could not fit gets, in place of
                         the header link Tab used to reach: the arrow keys land
                         on it like any other result and Enter opens it the
                         same way. -->
                    <a
                      id={viewAllId}
                      {href}
                      role="option"
                      aria-selected={chosen}
                      class={cn(
                        'flex items-center justify-between rounded-row border-b border-line-thin px-3 py-2 text-body font-medium text-ink-3 no-underline transition-[background] last:border-b-0 hover:bg-surface-thick hover:text-ink',
                        chosen && chosenClass
                      )}
                      onclick={(event) => {
                        event.preventDefault();
                        leaveFor(href);
                      }}
                      onmouseenter={() => selectRow(viewAllId)}
                    >
                      <span>View all results in {category.allLabel}</span>
                      <span class="font-mono text-micro text-ink-4">{category.total}</span>
                    </a>
                  {/if}
                </div>
              {/each}
            </div>
          {/if}
        </div>

        <!-- Always present, so the keys are learnable by looking. -->
        <div
          class="flex shrink-0 flex-wrap gap-3 border-t border-line-thin px-3 py-[7px] font-mono text-micro text-ink-4"
        >
          <span>↑↓ move</span>
          <span>↵ open</span>
          <span>esc close</span>
        </div>
      {/if}

      <!-- Placed last, after the field and the results, so Tab reaches it in
           the ordinary order rather than jumping there right out of the
           field. Positioned over the header row's own right edge, which is
           where a close control has always sat in Schall. Also the only way
           to close the palette by touch, which had none before. -->
      <button
        type="button"
        onclick={dismiss}
        aria-label="Close search"
        class="tap absolute right-3 top-[5px] grid size-6 place-items-center rounded-control text-ink-4 transition hover:bg-surface-thick hover:text-ink"
      >
        <X size={14} />
      </button>
    </aside>
  </div>
{/if}

<style>
  /* The glass this layer shares with every other floating surface in Schall:
     `rgba(32,34,39,0.55)` over a 14px backdrop blur, behind a
     `rgba(232,233,231,0.09)` hairline. The page behind is present and
     unreadable, which is why the palette needs no scrim. Entry is a plain
     fade on the surface step, the one every surface in the application opens
     at — no slide, no scale, no bounce. It was 120ms, which was a duration the
     scale did not have.

     The fill and the border are written as utilities on the element, in the
     same class names the menu and the modal use, so the three surfaces cannot
     drift apart. What is left here is the blur and the entry, neither of
     which a utility can carry. */
  .palette-panel {
    backdrop-filter: blur(14px);
    -webkit-backdrop-filter: blur(14px);
    animation: palette-in var(--motion-surface) var(--motion-ease-arrive);
  }

  @keyframes palette-in {
    from {
      opacity: 0;
    }
    to {
      opacity: 1;
    }
  }

  @media (prefers-reduced-motion: reduce) {
    .palette-panel {
      animation: none;
    }
  }
</style>
