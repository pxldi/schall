<script lang="ts">
  import { inksFor } from '$lib/duoton';
  import Cover from '$lib/components/Cover.svelte';
  import GeneratedCover from '$lib/components/GeneratedCover.svelte';
  import Print from '$lib/components/Print.svelte';

  // The square on an artist's card (ADR Duoton). A photo of the artist when
  // one is cached; otherwise a print of their own covers in their own inks, so
  // a page of artists reads as their music rather than as a wall of initials.
  // An artist with neither gets generated art seeded from their id.
  //
  // The print waits until the card is near the screen. A collection holds
  // hundreds of artists, and reading every cover's inks on load would make the
  // first screen wait for the last one.

  let {
    id,
    hasImage,
    coverAlbumIds = []
  }: { id: string; hasImage: boolean; coverAlbumIds?: string[] } =
    $props();

  let box = $state<HTMLElement>();
  let near = $state(false);

  $effect(() => {
    if (!box || near) return;
    if (typeof IntersectionObserver === 'undefined') {
      near = true;
      return;
    }
    const observer = new IntersectionObserver(
      ([entry]) => {
        if (entry.isIntersecting) {
          near = true;
          observer.disconnect();
        }
      },
      { rootMargin: '600px' }
    );
    observer.observe(box);
    return () => observer.disconnect();
  });

  const printed = $derived(!hasImage && coverAlbumIds.length > 0);
  // The card's own inks, from its first cover. The print is drawn once they
  // are known, so it never flashes through the page's inks first.
  const inks = inksFor(() => (near && printed ? coverAlbumIds[0] : undefined));
</script>

<span bind:this={box} class="absolute inset-0 block bg-surface-regular">
  {#if hasImage}
    <Cover src={`/api/v1/artists/${id}/image`} seed={id} class="size-full object-cover" />
  {:else if printed}
    {#if near && inks.current}
      <Print covers={coverAlbumIds} mode="single" inks={inks.current} class="size-full" />
    {/if}
  {:else if near}
    <GeneratedCover seed={id} class="size-full" />
  {/if}
</span>
