<script lang="ts">
  import type { Snippet } from 'svelte';

  // A card is a page section you can act on as a unit: a hairline and 16px of
  // room inside it, as design/section.html draws it. It groups settings
  // rather than working on them, so it carries no fill of its own (the
  // 2026-09-07 surface review) — the wash of white it used to draw put a
  // settings section on the same footing as an open dialog or a selected
  // card, which it is not. The settings page is built from sixteen of them —
  // health, slskd, formats, upgrade, Spotify, Navidrome, ListenBrainz, weekly
  // playlist, new releases, notifications, library layout, file tags,
  // transcoding (two cards), retention, AcoustID — and each had the same
  // string written out by hand.
  //
  // Inside a card, groups go back to a heading and space. section.html's rule
  // is never two borders deep, so anything this holds draws no border of its
  // own; a tint is what marks a distinct surface in here.

  let { children }: { children?: Snippet } = $props();
</script>

<!-- A card arrives on the arriving step, once, when the page first draws it —
     which on every screen that has cards is the moment its skeleton is replaced
     by real content. The nine on the settings page arrive together rather than
     one behind another: they are one page appearing, not nine things happening,
     and a stagger would say the opposite. -->
<section
  class="rise flex flex-col gap-3.5 rounded-card border border-line-thin p-4"
>
  {@render children?.()}
</section>
