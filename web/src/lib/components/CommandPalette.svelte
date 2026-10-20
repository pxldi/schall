<script lang="ts">
  import { createQuery } from '@tanstack/svelte-query';
  import { goto } from '$app/navigation';
  import { toStore } from 'svelte/store';
  import { fade } from 'svelte/transition';
  import { motionMs } from '$lib/motion.svelte';
  import { api } from '$lib/api';
  import { cn } from '$lib/utils';
  import { highlight, paletteCategories, paletteRows, type PaletteRole } from '$lib/palette';
  import ErrorNote from '$lib/components/ErrorNote.svelte';
  import Cover from '$lib/components/Cover.svelte';
  import Icon from '$lib/components/Icon.svelte';

  // One search field, opened by typing on any page, answering across the five
  // categories Schall holds at once. It is a front door to browsing that already
  // exists: every row is a link to the page that owns the thing, and the palette
  // never grows a filter, a sort, a second page of results or an action of its
  // own. When the answer is bigger than five, the way forward is out of the
  // palette and into that category's own list with the query pre-filled.
  //
  // It is not a dialog. Nothing behind it is blocked: the page dims so
  // the panel is the one thing to read, but presses go through the dim to
  // the page, and a press outside the panel closes it.

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

  // The panel keeps the height of the last list it drew while the next answer
  // is on its way. The rows themselves go, because a stale list under a newer
  // query must never show, but the panel no longer drops to one line and grows
  // back on every word typed.
  let body = $state<HTMLElement | null>(null);
  let heldHeight = $state(0);
  $effect(() => {
    if (!typed.trim()) heldHeight = 0;
    else if (!waiting && rows.length > 0 && body) heldHeight = body.offsetHeight;
  });

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

  // The mark is the thing's own state, in the state's colour beside its glyph.
  // Selection is drawn inverted and state is coloured: colour reports what a
  // thing is, and "the arrow keys are here" is not a state.
  const markTones: Record<PaletteRole, string> = {
    ok: 'text-ok',
    idle: 'text-idle',
    busy: 'text-busy',
    decide: 'text-decide',
    fail: 'text-fail'
  };

  const rowClass =
    'relative grid grid-cols-[2.25rem_minmax(0,1fr)_minmax(0,132px)] items-center gap-x-3 ' +
    ' border-b border-rule px-[1.125rem] py-2 text-inherit no-underline ' +
    'transition-[background] last:border-b-0';

  // The keyboard selection is the inverted row. A row under the pointer takes
  // the raised fill instead, and the pointer moves the selection onto itself,
  // so the two never show at once.
  const chosenClass = 'inverted';
  const idleClass = 'hover:bg-raised';

  function summon() {
    opener = document.activeElement;
    open = true;
    // The field does not exist yet on the frame the palette opens.
    queueMicrotask(() => {
      box?.focus();
      // A palette opened by typing holds that first character; the caret goes
      // after it so the next key continues the word.
      box?.setSelectionRange(typed.length, typed.length);
    });
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
  // two marks on it would be saying enter goes to one of two places. Only a
  // pointer that moved counts. Results drawn under a resting pointer, or
  // scrolled under it by the arrow keys, make the browser report the pointer
  // over a new row without it having moved, and Enter then opened that row
  // instead of the top one.
  function selectRow(id: string) {
    if (!pointerMoved) return;
    const at = entries.findIndex((entry) => entry.id === id);
    if (at >= 0) selected = at;
  }

  // Where the pointer last was, and whether the move being handled changed
  // that. Read on the window before any row sees the event. Plain, not $state.
  let pointerAt: { x: number; y: number } | null = null;
  let pointerMoved = false;

  $effect(() => {
    const record = (event: PointerEvent) => {
      pointerMoved =
        pointerAt !== null && (pointerAt.x !== event.screenX || pointerAt.y !== event.screenY);
      pointerAt = { x: event.screenX, y: event.screenY };
    };
    window.addEventListener('pointermove', record, { capture: true });
    return () => window.removeEventListener('pointermove', record, { capture: true });
  });

  function move(by: number) {
    if (entries.length === 0) return;
    // The list does not wrap at either end.
    selected = Math.min(Math.max(selected + by, 0), entries.length - 1);
  }

  // The search button in the shell, and the 404 page's Search button. It is an event rather than a prop because the palette is
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

  // Typing anywhere opens search with what was typed, so there is no shortcut
  // to learn. A key is left alone when something else could be its reader: a
  // field, an open dialog or menu, a modifier held for the browser, or a page
  // shortcut that took the key first (the review queue's j, k and 1–9 call
  // preventDefault). That last check waits a task, because the page's own
  // window listener may run after this one.
  const typingTargets =
    'input, textarea, select, [contenteditable]:not([contenteditable="false"]), [role="dialog"], dialog[open], [role="menu"], [role="listbox"]';

  function startsSearch(event: KeyboardEvent): boolean {
    if (event.defaultPrevented || event.isComposing) return false;
    if (event.metaKey || event.ctrlKey || event.altKey) return false;
    // A space opens nothing, but once the palette is open or opening it is
    // part of the query like any other key.
    if (event.key.length !== 1) return false;
    if (event.key.trim() === '' && !open && waitingKeys === 0) return false;
    const target = event.target;
    if (target instanceof Element && target.closest(typingTargets)) return false;
    return document.querySelector('[role="dialog"][aria-modal="true"], dialog[open]') === null;
  }

  // Keys that have been pressed but not yet read. Plain, not $state.
  let waitingKeys = 0;

  // Every key typed while the palette opens is kept, in order. Each key waits
  // its own task, and keys pressed before the first of those ran used to
  // overwrite one another, while keys pressed after the palette opened but
  // before the field held focus were dropped: "souly" typed fast arrived as "s".
  function onWindowKeydown(event: KeyboardEvent) {
    if (!startsSearch(event)) return;
    if (open) {
      // The field is not focused yet, or focus moved to a result: the key is
      // still meant for the query.
      event.preventDefault();
      append(event.key);
      return;
    }
    waitingKeys++;
    setTimeout(() => {
      waitingKeys--;
      if (event.defaultPrevented) return;
      if (open) append(event.key);
      else if (event.key.trim() !== '') {
        typed += event.key;
        summon();
      }
    });
  }

  function append(key: string) {
    typed += key;
    queueMicrotask(() => {
      box?.focus();
      box?.setSelectionRange(typed.length, typed.length);
    });
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
    class="scrim pointer-events-none z-50"
    transition:fade={{ duration: motionMs('surface') }}
  ></div>
  <div
    class="pointer-events-none fixed inset-0 z-50 flex justify-center px-[1.125rem] pb-[26px] pt-[90px]"
  >
    <aside
      bind:this={panel}
      aria-label="Search Schall"
      out:fade={{ duration: motionMs('state') }}
      class="overlay-panel pointer-events-auto relative flex max-h-[70vh] w-[620px] max-w-full flex-col self-start overflow-hidden"
    >
      <div
        class={cn(
          'palette-row grid h-[3.25rem] shrink-0 grid-cols-[1.25rem_minmax(0,1fr)_1.5rem] items-center gap-x-3 px-[1.125rem]',
          hasBody && 'border-b border-rule'
        )}
      >
        <span class="grid place-items-center text-ink-3"><Icon name="search" size="lg" /></span>
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
          class="palette-field min-w-0 border-0 bg-transparent p-0 text-lead font-medium text-ink placeholder:text-ink-4 focus:outline-none"
        />
      </div>

      {#if hasBody}
        <div
          class="min-h-0 flex-1 overflow-y-auto py-2"
          bind:this={body}
          style:min-height={waiting && heldHeight ? `${heldHeight}px` : undefined}
        >
          {#if waiting}
            <p class="flex items-center gap-2 px-3 py-3.5 text-meta leading-[1.6] text-ink-4">
              <Icon name="busy" size="sm" class="animate-spin" />
              Searching…
            </p>
          {:else if failed}
            <!-- A failed query is reported here rather than in a toast: the
                 palette is open, it is under the eye, and it is the thing that
                 failed. Closing it closes the error with it. -->
            <div
              class="mx-3 my-1.5 flex items-start gap-2 border border-rule bg-panel px-[11px] py-[9px]"
            >
              <Icon name="alert" size="sm" class="mt-px shrink-0 text-fail" />
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
              Nothing matches <span class="text-ink">{query}</span>.
            </p>
          {:else}
            <div id="palette-results" role="listbox" aria-label="Search results" tabindex="-1">
              {#each categories as category, group (category.key)}
                <!-- A category that answered with nothing is absent, not listed
                     as empty. The one that is present is still drawn: the
                     category of a result is never implied by being the only
                     one. -->
                <div role="group" aria-label={category.label} class={cn(group > 0 && 'mt-[14px]')}>
                  <div class="mb-0.5 border-b border-rule px-[1.125rem] pb-1.5">
                    <span
                      class="label inline-flex items-center gap-1.5"
                      ><Icon name={category.icon} size={12} />{category.label}</span
                    >
                  </div>

                  {#each category.rows as row (row.id)}
                    {@const chosen = current?.id === row.id}
                    <a
                      id={row.id}
                      href={row.href}
                      role="option"
                      aria-selected={chosen}
                      class={cn(rowClass, chosen ? chosenClass : idleClass)}
                      onclick={(event) => {
                        event.preventDefault();
                        leaveFor(row.href);
                      }}
                      onpointermove={() => selectRow(row.id)}
                    >
                      <span aria-hidden="true" class="relative size-9">
                        {#if row.cover}
                          <Cover
                            src={row.cover.src}
                            seed={row.cover.seed}
                            class="size-9 object-cover"
                          />
                        {/if}
                        <!-- The thing's state as an icon, pinned to the
                             picture's corner on a solid chip so it reads on
                             any cover, or standing alone where there is no
                             picture. A row in no state has no mark. -->
                        {#if row.mark}
                          <span
                            class={cn(
                              'absolute',
                              row.cover ? 'palette-badge -bottom-1 -right-1' : 'inset-0 m-auto size-5'
                            )}
                          >
                            <span
                              class={cn(
                                'grid place-items-center',
                                row.cover ? 'size-4' : 'size-5',
                                markTones[row.role]
                              )}
                            >
                              <Icon
                                name={row.mark.icon}
                                size="sm"
                                class={row.cover ? 'size-2.5' : 'size-3.5'}
                              />
                            </span>
                          </span>
                        {/if}
                      </span>
                      <span class="flex min-w-0 flex-col gap-0.5">
                        <span class="truncate text-body text-ink"
                          >{#each highlight(row.title, query) as piece, part (part)}<span
                              class={cn(piece.hit && 'font-semibold text-ink')}>{piece.text}</span
                            >{/each}{#if row.mark}<span class="sr-only">, {row.mark.label}</span
                            >{/if}</span
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
                      <span class="numeric min-w-0 truncate text-right text-meta text-ink-2"
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
                        'flex items-center justify-between border-b border-rule px-3 py-2 text-body font-medium text-ink-3 no-underline transition-[background] last:border-b-0',
                        chosen ? chosenClass : 'hover:bg-raised hover:text-ink'

                      )}
                      onclick={(event) => {
                        event.preventDefault();
                        leaveFor(href);
                      }}
                      onpointermove={() => selectRow(viewAllId)}
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
          class="flex shrink-0 flex-wrap items-center gap-5 border-t border-rule px-[1.125rem] py-2.5 text-meta text-ink-3"
        >
          <span class="inline-flex items-center gap-1.5 leading-none"
            ><span class="inline-flex"
              ><Icon name="arrow-up" size="sm" /><Icon name="arrow-down" size="sm" /></span
            >move</span
          >
          <span class="inline-flex items-center gap-1.5 leading-none"
            ><Icon name="enter" size="sm" />open</span
          >
          <span class="inline-flex items-center gap-1.5 leading-none"
            ><kbd class="keycap">Esc</kbd>close</span
          >
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
        class="tap absolute right-[1.125rem] top-[0.875rem] grid size-6 place-items-center text-ink-4 transition hover:bg-raised hover:text-ink"
      >
        <Icon name="close" size="sm" />
      </button>
    </aside>
  </div>
{/if}

<style>
  /* The state badge sits on the picture, on a solid ground chip so it reads on
     any cover. */
  .palette-badge {
    background-color: var(--color-ground);
  }

  /* The focus ring goes round the whole search row, inside the panel. Drawn
     on the field itself it sat 2px outside a box flush with the panel's top
     edge, where the panel's clipping cut it and it ran over the column beside
     the field. */
  .palette-field:focus-visible {
    outline: none;
  }

  .palette-row:has(.palette-field:focus-visible) {
    outline: 2px solid var(--color-ink);
    outline-offset: -2px;
  }
</style>
