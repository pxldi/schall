<script lang="ts">
  import type { Snippet } from 'svelte';

  // The band of controls directly under a page's name, and the only place a
  // filter, a search box or a sort order is allowed to live.
  //
  // It exists because the band above it stopped taking them. A page name is set
  // in the display face at 24px and sits on its baseline; a field is a 34px box
  // and sits on its edges. Putting the two on one line means neither is aligned
  // to anything, and no amount of nudging fixes it, because they are two
  // different kinds of object. So they are two bands: one says which page this
  // is, one holds what the reader can turn.
  //
  // Sticky, because the list it labels is long and the answer to "which of these
  // am I looking at?" should not scroll away from the things themselves. It sits
  // under the mobile header, which is `z-30` and taller, and over the rows,
  // which are not raised at all.
  //
  // **One rail per page sticks, and it is the outer one.** A page can carry a
  // second rail — Library picks a shape in the first and filters within it in
  // the second — and two rails both pinned to `top: 0` would be drawn on top of
  // each other. The inner one passes `sticky={false}` and scrolls away, which is
  // right anyway: the question it answers is narrower than the one above it.

  // `width` is 'full' everywhere except the one page whose table sits in
  // `layout-width` below it: at 1440px and wider, a full-width rail and a
  // centred, capped table do not share a left edge. 'layout' wraps the same
  // children in that table's own box, so the two line up at every width.
  let {
    children,
    label,
    sticky = true,
    width = 'full'
  }: { children: Snippet; label?: string; sticky?: boolean; width?: 'full' | 'layout' } =
    $props();
</script>

<div
  class="border-b border-line-thin {sticky ? 'sticky top-0 z-3 bg-ground' : 'bg-surface-thin'}"
  role={label ? 'group' : undefined}
  aria-label={label}
>
  <div
    class="flex flex-wrap items-center gap-x-4 gap-y-2 py-2.5 {width === 'layout'
      ? 'layout-width px-3'
      : 'px-4 sm:px-6'}"
  >
    {@render children()}
  </div>
</div>
