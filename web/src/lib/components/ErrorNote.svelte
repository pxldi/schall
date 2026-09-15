<script lang="ts">
  import { onDestroy } from 'svelte';
  import { cn } from '$lib/utils';
  import { describeError, isAuthError } from '$lib/errors';
  import StatusBadge from '$lib/components/StatusBadge.svelte';

  // The one place a failure is drawn. Eighty-three sites used to print
  // `error.message`, which is the server's own sentence — written for somebody
  // reading the Go source and shown to somebody in the middle of a task.
  //
  // What a reader gets here is what `CLAUDE.md` asks for on screen: what
  // happened to the thing in front of them, what to do about it naming a
  // control they can see, and — only if they open it — the words the server
  // used, which is what makes a bug report possible. `$lib/errors` decides the
  // words; this decides how they sit.

  let {
    // Anything a failed request threw, or the text a failed job left behind.
    error,
    // A second sentence this screen can say better than the map can, because it
    // knows which button was pressed. Replaces the written action, never the
    // raw words.
    action: written,
    // What this screen says when the sentence names nothing — a connection card
    // that knows the reader is looking at slskd can say so where the catch-all
    // could only say something did not finish. It stands in for the two
    // sentences that carry no subject, the catch-all and the one a 500 gets,
    // and never against a sentence written for this failure.
    fallback,
    // How this screen asks again, where asking again is the whole answer. Given
    // one, the note retries by itself rather than printing a button that
    // repeats what just failed. Never given for anything that writes: a save or
    // a download is asked for by a person, and the button they pressed is the
    // control to name.
    retry,
    // Drop the tinted band and draw the words alone. For the places that
    // already have a container of their own and would otherwise be two
    // surfaces deep: the failure row inside the command palette, the band a
    // settings card draws around several reasons at once. The words, the
    // action and the disclosure are the same ones; only the frame is not
    // drawn twice.
    bare = false,
    border = true,
    wrap = false,
    class: className
  }: {
    error: unknown;
    action?: string;
    fallback?: string;
    retry?: () => void;
    bare?: boolean;
    border?: boolean;
    wrap?: boolean;
    class?: string;
  } = $props();

  const notice = $derived(describeError(error));
  const sentence = $derived(
    notice.subjectless && fallback ? fallback : notice.sentence
  );
  const action = $derived(written ?? notice.action);

  // Twice, four seconds apart and then twelve. A server coming back from a
  // restart is back inside that window; one that is not is not helped by asking
  // faster, and the peer Schall talks to bans callers that do.
  const waits = [4000, 12000];

  let attempts = $state(0);
  let timer: ReturnType<typeof setTimeout> | undefined;

  // A session the forward-auth no longer accepts never comes back by asking
  // again: retrying repeats the same unauthenticated request. Checked
  // directly, not through `notice.transient`, so this holds even if a status
  // is ever given `transient: true` for another reason.
  const authError = $derived(isAuthError(error));

  const retrying = $derived(
    Boolean(retry) && !authError && notice.transient && attempts < waits.length
  );

  function stop() {
    if (timer !== undefined) clearTimeout(timer);
    timer = undefined;
  }

  $effect(() => {
    // Reading these makes the schedule follow the failure: a new one starts the
    // count again, and a request that succeeded unmounts the note.
    const again = retry;
    const wait = waits[attempts];
    if (!again || authError || !notice.transient || wait === undefined) return;
    timer = setTimeout(() => {
      attempts += 1;
      again();
    }, wait);
    return stop;
  });

  onDestroy(stop);
</script>

{#snippet words()}
  <span class="text-meta leading-relaxed text-ink">{sentence}</span>
  {#if retrying}
    <!-- Nobody is handed something they can only press retry on: where asking
         again is the only thing that would help, the page asks. -->
    <span class="text-meta leading-relaxed text-ink-2">Trying again.</span>
  {:else if action}
    <span class="text-meta leading-relaxed text-ink-2">{action}</span>
  {:else if notice.transient}
    <span class="text-meta leading-relaxed text-ink-2">Reload the page to ask again.</span>
  {/if}
  {#if notice.raw && notice.raw !== sentence}
    <!-- The explanation is not deleted, it moves behind a disclosure. Closed,
         because a person reading an error is not enrolling in a course; there,
         because a bug report needs the server's own words. -->
    <details class="w-full">
      <summary
        class="cursor-pointer text-meta text-ink-3 transition-colors select-none hover:text-ink-2"
      >
        What the server said
      </summary>
      <p
        class="mt-1 font-mono text-micro leading-[1.5] break-words text-ink-2"
        data-monospace="true"
        data-tone="quiet"
        data-truncated="false"
      >
        {notice.raw}
      </p>
    </details>
  {/if}
{/snippet}

{#if bare}
  <div
    role="status"
    data-frame="bare"
    class={cn('flex w-full flex-col items-start gap-1.5', className)}
  >
    {@render words()}
  </div>
{:else}
  <!-- The mark keeps its place at the head of the band, and the sentences stack
       beside it rather than under it: two lines and a disclosure are still one
       message, and a mark centred against three lines reads as a bullet. -->
  <StatusBadge role={notice.role} {border} {wrap} class={cn('items-start', className)}>
    <div class="flex min-w-0 flex-col gap-1.5">
      {@render words()}
    </div>
  </StatusBadge>
{/if}
