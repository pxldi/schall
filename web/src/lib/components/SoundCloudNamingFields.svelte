<script lang="ts">
  import type { SoundCloudNamingFields as Naming, SoundCloudTrack } from '$lib/api';

  // What SoundCloud said about a track, and what a file will be called from
  // it: the split Schall read off the title, shown so the person can correct
  // it before anything is written. Shared by the naming dialog on a file's
  // own row in Library and by the upload form that offers the same lookup
  // before the file has landed.
  let { track, named = $bindable() }: { track: SoundCloudTrack; named: Naming } = $props();
</script>

<!-- The whole title as SoundCloud sent it, and who uploaded it. -->
<div class="flex gap-3">
  {#if track.artworkUrl}
    <img
      src={track.artworkUrl}
      alt=""
      width="64"
      height="64"
      class="size-16 flex-none rounded-row object-cover"
    />
  {/if}
  <div class="min-w-0">
    <p class="truncate text-body text-ink" title={track.title}>{track.title}</p>
    <p class="mt-0.5 text-meta text-ink-3">
      Uploaded by {track.uploader}
    </p>
  </div>
</div>

<!-- SoundCloud states one name, the uploader's, and puts everything else in
     the title. Schall splits the title and shows what it made of it; the
     person corrects it before anything is written. -->
<div class="grid gap-3">
  <div>
    <label for="soundcloud-artist" class="label">Artist</label>
    <input
      id="soundcloud-artist"
      class="field mt-2 w-full"
      bind:value={named.artist}
      autocomplete="off"
      maxlength="300"
    />
  </div>
  <div>
    <label for="soundcloud-track" class="label">Track</label>
    <input
      id="soundcloud-track"
      class="field mt-2 w-full"
      bind:value={named.title}
      autocomplete="off"
      maxlength="300"
    />
  </div>
  <div>
    <label for="soundcloud-remixer" class="label">Remixer</label>
    <input
      id="soundcloud-remixer"
      class="field mt-2 w-full"
      bind:value={named.remixer}
      placeholder="Nobody"
      autocomplete="off"
      maxlength="300"
    />
  </div>
</div>
