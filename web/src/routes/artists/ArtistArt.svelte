<script lang="ts">
  import { coverSrc } from '$lib/cover-size';
  import Cover from '$lib/components/Cover.svelte';

  // The square on an artist's card. A photo of the artist when one is cached,
  // otherwise the artist's first cover, otherwise the flat placeholder seeded
  // from their id. Both pictures are square, with the cover corner.

  let {
    id,
    hasImage,
    coverAlbumIds = []
  }: { id: string; hasImage: boolean; coverAlbumIds?: string[] } = $props();

  const src = $derived(
    hasImage
      ? `/api/v1/artists/${id}/image`
      : coverAlbumIds.length > 0
        ? coverSrc(coverAlbumIds[0])
        : undefined
  );
</script>

<span class="absolute inset-0 block bg-panel">
  <Cover {src} seed={id} class="size-full object-cover" />
</span>
