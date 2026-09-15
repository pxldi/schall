<script lang="ts">
  import { createMutation, createQuery, useQueryClient } from '@tanstack/svelte-query';
  import QRCode from 'qrcode';
  import { api, type MintedPhone } from '$lib/api';
  import { calendarDate, relativeTime } from '$lib/utils';
  import Button from '$lib/components/Button.svelte';
  import Card from '$lib/components/Card.svelte';
  import ErrorNote from '$lib/components/ErrorNote.svelte';
  import FormGrid from '$lib/components/FormGrid.svelte';

  // Where a phone gets its credential. The browser is signed in through
  // Authentik and mints a token here; the phone sends that token instead.
  // Only the hash is stored, so the plain token is on screen once and can
  // never be read back — which is why the QR code is drawn beside it rather
  // than offered later.

  const queryClient = useQueryClient();
  const phones = createQuery({ queryKey: ['phones'], queryFn: api.phones });

  let name = $state('');
  let minted = $state<MintedPhone | null>(null);
  let qr = $state('');
  let copied = $state(false);

  const add = createMutation({
    mutationFn: () => api.addPhone(name.trim()),
    onSuccess: (phone) => {
      minted = phone;
      copied = false;
      name = '';
      queryClient.invalidateQueries({ queryKey: ['phones'] });
    }
  });

  const remove = createMutation({
    mutationFn: (id: string) => api.removePhone(id),
    onSuccess: (_, id) => {
      if (minted?.id === id) minted = null;
      queryClient.invalidateQueries({ queryKey: ['phones'] });
    }
  });

  // What the app reads off the code: where to ask, and what to say when it
  // asks. Drawn here rather than fetched, so a token never leaves this page
  // for a picture service.
  const payload = $derived(
    minted ? JSON.stringify({ server: minted.server, token: minted.token }) : ''
  );

  $effect(() => {
    const carried = payload;
    if (!carried) {
      qr = '';
      return;
    }
    let live = true;
    QRCode.toString(carried, { type: 'svg', margin: 0 })
      .then((svg) => {
        if (live) qr = svg;
      })
      .catch(() => {
        if (live) qr = '';
      });
    return () => {
      live = false;
    };
  });

  async function copyToken() {
    if (!minted) return;
    await navigator.clipboard?.writeText(minted.token);
    copied = true;
  }
</script>

<div class="flex max-w-[760px] flex-col gap-3.5">
  <Card>
    <h2 class="font-display text-lead font-bold text-ink">Phone</h2>

    <FormGrid
      onsubmit={(event) => {
        event.preventDefault();
        $add.mutate();
      }}
    >
      <label for="phone-name" class="text-body text-ink-2">Name</label>
      <div class="flex flex-wrap items-center gap-2.5">
        <input
          id="phone-name"
          type="text"
          bind:value={name}
          placeholder="Pixel"
          disabled={$add.isPending}
          class="field min-w-52 flex-1"
        />
        <Button type="submit" disabled={!name.trim() || $add.isPending}>Add phone</Button>
      </div>
    </FormGrid>

    {#if $add.isError}
      <ErrorNote error={$add.error} action="Press Add phone again." />
    {/if}

    {#if minted}
      <!-- A tint rather than a border: a card draws the only frame here. -->
      <div class="flex flex-col gap-3 rounded-row bg-surface-thick p-3">
        <span class="text-meta text-ink">Shown once. Scan it or copy it now.</span>
        <div class="flex flex-wrap items-start gap-4">
          <div
            role="img"
            aria-label="Sign-in code for {minted.name}"
            class="size-40 shrink-0 rounded-row bg-white p-2 text-ink [&>svg]:size-full"
          >
            {@html qr}
          </div>
          <div class="flex min-w-0 flex-1 flex-col items-start gap-2">
            <code
              class="w-full font-mono text-micro leading-[1.5] break-all text-ink"
              data-monospace="true">{minted.token}</code
            >
            <Button variant="outline" size="sm" onclick={copyToken}>
              {copied ? 'Copied' : 'Copy'}
            </Button>
          </div>
        </div>
      </div>
    {/if}

    {#if $remove.isError}
      <ErrorNote error={$remove.error} action="Press Remove again." />
    {/if}

    {#if $phones.isError}
      <ErrorNote error={$phones.error} retry={() => $phones.refetch()} />
    {:else if ($phones.data?.items ?? []).length === 0}
      {#if !$phones.isPending}
        <span class="text-meta text-ink-3">No phone has a token yet.</span>
      {/if}
    {:else}
      <!-- Hairlines and space, the shape the other settings lists use. -->
      <div class="flex flex-col">
        {#each $phones.data?.items ?? [] as phone (phone.id)}
          <!-- Below `sm` the name, the two dates and Remove do not fit on one
               line, so the name takes a line of its own (`basis-full` forces
               the wrap) and the dates wrap onto the next. `sm` and up are the
               original single row. -->
          <div class="flex flex-wrap items-center gap-x-2.5 gap-y-1 border-b border-line-thin py-2">
            <span class="min-w-0 grow shrink basis-full truncate text-body text-ink sm:basis-0">
              {phone.name}
            </span>
            <span class="shrink-0 text-meta text-ink-3">added {calendarDate(phone.createdAt)}</span>
            <span class="shrink-0 text-meta text-ink-3">
              {phone.lastUsedAt ? `used ${relativeTime(phone.lastUsedAt)}` : 'never used'}
            </span>
            <Button
              variant="ghost"
              size="sm"
              tall
              class="ml-auto sm:ml-0"
              disabled={$remove.isPending}
              onclick={() => $remove.mutate(phone.id)}
            >
              Remove
            </Button>
          </div>
        {/each}
      </div>
    {/if}
  </Card>
</div>
