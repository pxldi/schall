<script lang="ts" module>
  // A short list of actions that are one decision at different scopes: the same
  // answer, applied to a narrow thing or a wide one. Three controls on a row
  // would read as three separate actions, so the scopes go behind one trigger.
  //
  // `design/menu.html` is what this draws, and every value below is that card's.

  export interface MenuItem {
    /** The decision, in the words the reader chooses by. Never the scope. */
    label: string;
    /**
     * The thing being ruled out, written under the label — "Marconi Union"
     * under "Anything by this artist". It is the scope made concrete, never a
     * description of what the item does. A refused item says why here instead.
     */
    detail?: string;
    /**
     * Refused. The item stays in the list rather than vanishing, so the set of
     * scopes is the same length on every row, and `detail` says why.
     */
    disabled?: boolean;
    /** What choosing it does. It runs once the menu has closed. */
    onchoose: () => void;
  }
</script>

<script lang="ts">
  import { tick } from 'svelte';
  import { ChevronDown, Ellipsis } from '@lucide/svelte';
  import { cn } from '$lib/utils';
  import Button from './Button.svelte';

  let {
    label,
    heading,
    items,
    trigger = 'overflow',
    class: className
  }: {
    /** The decision itself — "Not interested". It names the trigger and the
     * panel, and it is never spelled out three ways with the scopes in it. */
    label: string;
    /** The line above the items, saying what the list is choosing between. */
    heading: string;
    items: MenuItem[];
    /** `overflow` is the three-dot square that sits at the end of a row;
     * `labelled` is the 32px button that carries the words. The panel grows
     * away from the page edge its trigger is nearest, so the first opens with
     * right edges aligned and the second with left. */
    trigger?: 'overflow' | 'labelled';
    class?: string;
  } = $props();

  // How near the bottom of the window the trigger has to be before the panel
  // opens upwards instead. Nothing else about it changes.
  const flipRoom = 200;

  let open = $state(false);
  // Where the keyboard caret is. A pointer resting on another item does not
  // move it: the ring says which item Enter will take, and hover never draws
  // one.
  let at = $state(0);
  let above = $state(false);

  // Handles, not content. `root` is the anchor the panel is positioned against
  // and the region a press counts as inside; the trigger is found under it
  // rather than bound, because it is a component and what is needed is its
  // element.
  let root = $state<HTMLElement | null>(null);
  let panel = $state<HTMLElement | null>(null);
  let itemEls = $state<(HTMLButtonElement | null)[]>([]);

  function triggerButton() {
    return root?.querySelector<HTMLElement>('[aria-haspopup="menu"]') ?? null;
  }

  // The index of the next item that can be chosen, `by` steps from `from`.
  // Movement wraps and steps over refused items; -1 means there are none.
  function stepTo(from: number, by: number) {
    const count = items.length;
    for (let taken = 1; taken <= count; taken++) {
      const index = (((from + by * taken) % count) + count) % count;
      if (!items[index].disabled) return index;
    }
    return -1;
  }

  const firstItem = () => stepTo(-1, 1);
  const lastItem = () => stepTo(items.length, -1);

  async function show(index: number) {
    // Measured before the panel exists, off the trigger, because the answer is
    // about the trigger's place on the page.
    const box = triggerButton()?.getBoundingClientRect();
    above = box !== undefined && window.innerHeight - box.bottom < flipRoom;
    open = true;
    await tick();
    focusAt(index);
  }

  // Closing leaves focus on the trigger, which is still there because nothing
  // was chosen. A press somewhere else is the exception the browser settles on
  // its own: this runs on pointerdown, before the pressed thing takes focus, so
  // a press on a control keeps it and a press on the page comes back here.
  function hide(restore = true) {
    if (!open) return;
    open = false;
    at = 0;
    if (!restore) return;
    const back = triggerButton();
    if (back?.isConnected) back.focus();
  }

  function focusAt(index: number) {
    if (index < 0) {
      panel?.focus();
      return;
    }
    at = index;
    itemEls[index]?.focus();
  }

  async function choose(item: MenuItem) {
    if (item.disabled) return;
    open = false;
    item.onchoose();
    // The trigger usually leaves the page with the row the choice removed, and
    // then where focus lands is the page's to say. It is only taken back when
    // the trigger is still standing after the choice has been applied.
    await tick();
    const back = triggerButton();
    if (back?.isConnected) back.focus();
  }

  function toggle() {
    if (open) hide();
    else void show(firstItem());
  }

  // One tab stop. Enter, Space and Down open on the first item; Up opens on the
  // last. The presses are taken here rather than left to become a click, so
  // that opening on the last item is possible at all.
  function onTriggerKeydown(event: KeyboardEvent) {
    if (open) return;
    switch (event.key) {
      case 'Enter':
      case ' ':
      case 'ArrowDown':
        event.preventDefault();
        void show(firstItem());
        return;
      case 'ArrowUp':
        event.preventDefault();
        void show(lastItem());
    }
  }

  // Enter and Space are the button's own: an item is a button, and pressing one
  // is a press. What is left is movement and the two ways out.
  function onMenuKeydown(event: KeyboardEvent) {
    switch (event.key) {
      case 'Escape':
        event.preventDefault();
        hide();
        return;
      case 'ArrowDown':
        event.preventDefault();
        focusAt(stepTo(at, 1));
        return;
      case 'ArrowUp':
        event.preventDefault();
        focusAt(stepTo(at, -1));
        return;
      case 'Home':
        event.preventDefault();
        focusAt(firstItem());
        return;
      case 'End':
        event.preventDefault();
        focusAt(lastItem());
        return;
      case 'Tab':
        // Not taken. The menu closes, focus goes back to the trigger, and the
        // press then carries on through the page from there.
        hide();
    }
  }

  // Nothing behind the panel is blocked, so a press outside it reaches the page
  // as it always would and closes the menu on the way.
  function onWindowPointerdown(event: PointerEvent) {
    if (!open || !root) return;
    if (!root.contains(event.target as Node)) hide();
  }

  function onWindowResize() {
    if (open) hide();
  }

  // The floating surface: Schall's whole glass budget, spent here because a
  // menu is one of the surfaces that floats above the page rather than sits
  // in it. It is the material the command palette and the one modal are made
  // of, at the smaller radius.
  const frosted = 'bg-[rgba(32,34,39,0.55)] backdrop-blur-[14px]';

  // Rest, hover, keyboard focus, refused. Hover is a surface; focus is that
  // same surface and the one ring, inset — so a pointer resting on one item
  // while the caret sits on another cannot be read as two carets.
  const itemBase =
    'flex w-full items-start gap-2 rounded-control text-left font-medium text-ink ' +
    'transition-[background] hover:bg-surface-thick ' +
    'focus-visible:bg-surface-thick focus-visible:outline-offset-[-2px] ' +
    'aria-disabled:pointer-events-none aria-disabled:opacity-50';
</script>

<svelte:window onpointerdown={onWindowPointerdown} onresize={onWindowResize} />

<span bind:this={root} class={cn('relative inline-flex', className)}>
  {#if trigger === 'labelled'}
    <!-- The chevron is the only mark that says a panel follows; a labelled
         trigger without one reads as a button that acts on press. -->
    <Button
      variant="outline"
      aria-haspopup="menu"
      aria-expanded={open}
      onclick={toggle}
      onkeydown={onTriggerKeydown}
      class="aria-expanded:border-line-thick aria-expanded:bg-surface-thick aria-expanded:text-ink"
    >
      {label}
      <ChevronDown size={13} class={cn('transition-transform', open && 'rotate-180')} />
    </Button>
  {:else}
    <!-- 24px is the row's content height at row.html's 8px padding, so the
         trigger column costs the row no height. It is quiet at rest and lifts
         with the row, but it is never revealed by hover: a control that only
         exists under a pointer cannot be tabbed to. The row it sits in carries
         `group` for the lift. -->
    <Button
      variant="ghost"
      size="xs"
      icon
      title={label}
      aria-label={label}
      aria-haspopup="menu"
      aria-expanded={open}
      onclick={toggle}
      onkeydown={onTriggerKeydown}
      class="text-ink-4 group-hover:text-ink-2 aria-expanded:bg-surface-thick aria-expanded:text-ink"
    >
      <Ellipsis size={14} />
    </Button>
  {/if}

  {#if open}
    <!-- 4px below the trigger, and above it instead when the trigger is near
         the foot of the window. There is no scrim: this is a menu, not a
         modal, and the page behind it is neither dimmed nor blocked. -->
    <div
      bind:this={panel}
      role="menu"
      aria-label={label}
      tabindex="-1"
      data-placement={above ? 'top' : 'bottom'}
      data-side={trigger === 'labelled' ? 'left' : 'right'}
      onkeydown={onMenuKeydown}
      class={cn(
        'menu-in absolute z-[2] w-[17rem] max-w-full overflow-hidden rounded-panel border border-[rgba(232,233,231,0.09)] p-1',
        frosted,
        above ? 'bottom-[calc(100%+4px)]' : 'top-[calc(100%+4px)]',
        trigger === 'labelled' ? 'left-0' : 'right-0'
      )}
    >
      <span class="label block px-2.5 pb-1.5 pt-2">{heading}</span>
      {#each items as item, index (index)}
        <button
          bind:this={itemEls[index]}
          type="button"
          role="menuitem"
          tabindex="-1"
          aria-disabled={item.disabled || undefined}
          onclick={() => void choose(item)}
          class={cn(itemBase, 'px-2.5 py-[7px] text-body leading-[1.35]')}
        >
          <span class="min-w-0">
            {item.label}
            {#if item.detail}
              <span class="mt-0.5 block text-micro font-normal leading-[1.4] text-ink-3">
                {item.detail}
              </span>
            {/if}
          </span>
        </button>
      {/each}
    </div>
  {/if}
</span>

<style>
  /* The panel opens over the surface step — no scale, no bounce, and nothing
     at all for a reader who asked for less motion. There is no way out to
     animate: a menu that lingers after a choice is a menu the reader is still
     being shown.

     It was drawn at 120ms, which was a fourth duration the scale did not
     have. 200ms is what the drawer, the modal and the palette all take now, so
     every surface in the application opens at one speed. */
  .menu-in {
    animation: menu-in var(--motion-surface) var(--motion-ease-arrive);
  }

  @keyframes menu-in {
    from {
      opacity: 0;
    }
    to {
      opacity: 1;
    }
  }

  @media (prefers-reduced-motion: reduce) {
    .menu-in {
      animation: none;
    }
  }
</style>
