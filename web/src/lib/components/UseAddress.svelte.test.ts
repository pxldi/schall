import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, setup, waitFor } from '@testing-library/svelte';
import { QueryClient, QueryClientProvider } from '@tanstack/svelte-query';
import UseAddress from '$lib/components/UseAddress.svelte';

const targetId = '20000000-0000-4000-8000-000000000001';
const address = 'https://soundcloud.com/artist/track';

function opened() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    UseAddress,
    {
      targetId,
      entryTitle: 'Entry song',
      entryArtist: 'Entry artist',
      entryDurationMs: 180_000
    },
    { wrapper: QueryClientProvider, wrapperProps: { client } }
  );
}

function sourceResponse() {
  return {
    source: 'soundcloud',
    externalId: 'soundcloud:123',
    url: address,
    title: 'Source song',
    artist: 'Source artist',
    uploader: 'Source artist',
    durationMs: 187_000
  };
}

beforeAll(setup);

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe('Use address', () => {
  it('shows the lookup title, artist, length and a material length difference', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => new Response(JSON.stringify(sourceResponse()), { status: 200 }))
    );

    opened();
    await fireEvent.click(screen.getByText('Use address'));
    const input = await screen.findByRole('textbox', { name: 'Track address' });
    await fireEvent.input(input, {
      target: { value: address }
    });
    await fireEvent.click(screen.getByRole('button', { name: 'Look up' }));

    expect(await screen.findByText('Source song · Source artist')).toBeTruthy();
    expect(screen.getByText('3:07 · length differs by more than 5 seconds')).toBeTruthy();
  });

  it('sends the confirmed address and external ID to the target', async () => {
    const sent: { path: string; init?: RequestInit }[] = [];
    vi.stubGlobal(
      'fetch',
      vi.fn(async (path: string, init?: RequestInit) => {
        sent.push({ path, init });
        return new Response(JSON.stringify(sourceResponse()), { status: 200 });
      })
    );

    opened();
    await fireEvent.click(screen.getByText('Use address'));
    const input = await screen.findByRole('textbox', { name: 'Track address' });
    await fireEvent.input(input, {
      target: { value: address }
    });
    await fireEvent.click(screen.getByRole('button', { name: 'Look up' }));
    await screen.findByText('Source song · Source artist');
    const useButtons = screen.getAllByRole('button', { name: 'Use address' });
    await fireEvent.click(useButtons[useButtons.length - 1]);

    await waitFor(() => expect(sent).toHaveLength(2));
    expect(sent[1].path).toBe(`/api/v1/acquisition-targets/${targetId}/source`);
    expect(JSON.parse(sent[1].init?.body as string)).toEqual({
      url: address,
      externalId: 'soundcloud:123'
    });
  });
});
