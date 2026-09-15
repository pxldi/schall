<script lang="ts">
  import { ExternalLink } from '@lucide/svelte';
  import Button from '$lib/components/Button.svelte';
  import type { MusicBrainzSeed } from '$lib/api';

  // The one thing that changes the outcome for an entry MusicBrainz has no
  // recording for. Schall cannot acquire such an entry however often it asks
  // again, so this is not a retry: it opens MusicBrainz's release editor with
  // everything Schall knows already filled in, for a person to check and submit.
  //
  // It is a form rather than a link because the release editor reads its seed
  // out of a request body and takes nothing from a query string. Nothing is sent
  // anywhere until the button is pressed, and what is sent then is sent by the
  // browser to MusicBrainz, never by Schall.
  let { seed }: { seed: MusicBrainzSeed } = $props();
</script>

<form
  method="post"
  action={seed.url}
  target="_blank"
  rel="noopener noreferrer"
  class="inline-flex"
>
  <!-- Keyed by position rather than by name. The release editor has fields that
       may legitimately be seeded more than once, and a repeated key is a page
       that throws; nothing here reorders, so position is the honest key. -->
  {#each seed.fields as field, index (index)}
    <input type="hidden" name={field.name} value={field.value} />
  {/each}
  <Button
    type="submit"
    variant="ghost"
    size="xs"
    title="Opens MusicBrainz's editor in a new tab, filled in for you."
  >
    Add to MusicBrainz
    <ExternalLink size={11} />
  </Button>
</form>
