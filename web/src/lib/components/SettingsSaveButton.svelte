<script lang="ts">
  import { Check, LoaderCircle, Save } from '@lucide/svelte';
  import Button from '$lib/components/Button.svelte';
  import { motionMs } from '$lib/motion.svelte';

  // Every card on the settings page ends its form with this: an outline
  // button that submits, swaps its icon for a spinner while its own mutation
  // is in flight, and refuses a second press meanwhile. The same three lines
  // were written out by hand on ten cards. `disabled` carries whatever else a
  // card wants to refuse on beyond its own pending state, such as an empty
  // required field.
  //
  // `variant` defaults to the outline every card but Sources still uses.
  // Sources draws Save as the one accent button in each of its forms, next to
  // an outline Test — the source's own form is the single action a person
  // came to that section to take.
  //
  // `saved` says the mutation this button submits last succeeded. It flashes
  // a check for one read-length beat (`--motion-read`) and then falls back to
  // the plain label, rather than sitting there until the next press.
  //
  // `dirty` says whether the form holds anything different from what was
  // loaded. False disables the button, so an unchanged form cannot be
  // resubmitted; undefined — a card with no loaded baseline to compare
  // against — leaves the button exactly as pending and disabled decide it.
  let {
    pending = false,
    disabled = false,
    saved = false,
    dirty,
    variant = 'outline'
  }: {
    pending?: boolean;
    disabled?: boolean;
    saved?: boolean;
    dirty?: boolean;
    variant?: 'outline' | 'primary';
  } = $props();

  let showSaved = $state(false);

  $effect(() => {
    if (!saved) {
      showSaved = false;
      return;
    }
    showSaved = true;
    // motionMs returns 0 for a reader who has asked for less motion, which
    // clears the check on the next tick instead of holding it — the plain
    // label is what stays on screen for that reader.
    const timer = setTimeout(() => (showSaved = false), motionMs('read'));
    return () => clearTimeout(timer);
  });

  const nothingToSave = $derived(dirty === false && !pending);
</script>

<Button type="submit" {variant} disabled={pending || disabled || nothingToSave}>
  {#if pending}
    <LoaderCircle size={13} class="animate-spin" />
  {:else if showSaved}
    <Check size={13} strokeWidth={2.4} />
  {:else}
    <Save size={13} strokeWidth={2.2} />
  {/if}
  {showSaved && !pending ? 'Saved' : 'Save'}
</Button>
