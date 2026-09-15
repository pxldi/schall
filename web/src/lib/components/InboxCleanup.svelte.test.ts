import { afterEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { tick } from 'svelte';
import { QueryClient } from '@tanstack/svelte-query';
import { api, type InboxCleanup } from '$lib/api';
import InboxCleanupBlock from '$lib/components/InboxCleanup.svelte';

// Settings → Library → Clean the inbox. Deleting is irreversible, so the only
// thing this block promises is that the counting comes first: the button that
// deletes does not exist until a dry run has answered, and what each button
// sends is the whole contract with the server.

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

const counted: InboxCleanup = {
  id: '3f1c9a11-9d3a-4a2e-8f21-2c5a9b0e7d13',
  requestedBy: 'leo',
  requestedAt: '2026-09-10T10:00:00Z',
  dryRun: true,
  status: 'finished',
  startedAt: '2026-09-10T10:00:01Z',
  finishedAt: '2026-09-10T10:04:00Z',
  classes: [
    { class: 'imported', files: 12_000, bytes: 600_000_000, failed: 0, deletes: true },
    { class: 'refused', files: 400, bytes: 20_000_000, failed: 0, deletes: true },
    { class: 'settled', files: 30, bytes: 1_000_000, failed: 0, deletes: true },
    { class: 'unknown', files: 570, bytes: 30_000_000, failed: 0, deletes: true },
    { class: 'kept_question', files: 8, bytes: 500_000, failed: 0, deletes: false },
    { class: 'kept_open', files: 1, bytes: 50_000, failed: 0, deletes: false },
    { class: 'kept_recent', files: 2, bytes: 90_000, failed: 0, deletes: false },
    { class: 'kept_unconfirmed', files: 0, bytes: 0, failed: 0, deletes: false },
    { class: 'kept_undecided', files: 0, bytes: 0, failed: 0, deletes: false },
    { class: 'kept_unsafe_path', files: 0, bytes: 0, failed: 0, deletes: false }
  ],
  deleteFiles: 13_000,
  deleteBytes: 651_000_000,
  keptFiles: 11,
  keptBytes: 640_000,
  failedFiles: 0
};

function mount() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } }
  });
  return render(InboxCleanupBlock, {
    context: new Map<string, unknown>([['$$_queryClient', client]])
  });
}

describe('InboxCleanup', () => {
  it('counts what can go before anything can be deleted', async () => {
    vi.spyOn(api, 'inboxCleanups').mockResolvedValue({ items: [] });
    const clean = vi.spyOn(api, 'cleanInbox').mockResolvedValue({ ...counted, status: 'queued' });
    mount();
    await tick();

    expect(screen.queryByRole('button', { name: /^Delete/ })).toBeNull();

    await fireEvent.click(screen.getByRole('button', { name: 'Count what can go' }));

    await waitFor(() => expect(clean).toHaveBeenCalledWith(true));
  });

  it('names what a deletion would take, and sends the deletion once confirmed', async () => {
    vi.spyOn(api, 'inboxCleanups').mockResolvedValue({ items: [counted] });
    const clean = vi.spyOn(api, 'cleanInbox').mockResolvedValue({ ...counted, dryRun: false });
    mount();

    const remove = await screen.findByRole('button', { name: /^Delete 13,000 files/ });
    expect(screen.getByText('Already imported')).toBeTruthy();
    expect(screen.getByText('Nothing recorded them')).toBeTruthy();

    await fireEvent.click(remove);

    // Nothing is sent by revealing the confirmation.
    expect(clean).not.toHaveBeenCalled();
    expect(screen.getByText(/This cannot be undone/)).toBeTruthy();

    await fireEvent.click(screen.getByRole('button', { name: 'Delete' }));

    await waitFor(() => expect(clean).toHaveBeenCalledWith(false));
  });

  it('offers no deletion when a real pass was the last thing that ran', async () => {
    vi.spyOn(api, 'inboxCleanups').mockResolvedValue({
      items: [{ ...counted, dryRun: false }]
    });
    mount();

    await screen.findByText(/13,000 files .* deleted/);
    expect(screen.queryByRole('button', { name: /^Delete/ })).toBeNull();
  });

  it('says how many files could not be removed, and counts them out of the total', async () => {
    vi.spyOn(api, 'inboxCleanups').mockResolvedValue({
      items: [
        {
          ...counted,
          dryRun: false,
          status: 'failed',
          failedFiles: 3,
          error: '3 file(s) could not be removed from the download inbox; they are still there'
        }
      ]
    });
    mount();

    await screen.findByText(/could not be removed/);
    expect(screen.getByText(/12,997 files .* deleted/)).toBeTruthy();
  });
});
