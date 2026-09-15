import { afterEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { tick } from 'svelte';
import { QueryClient } from '@tanstack/svelte-query';
import { api, type MintedPhone, type Phone } from '$lib/api';
import PhoneSettings from '$lib/components/PhoneSettings.svelte';

// Settings → Phone. The token is shown once and stored only as a hash, so what
// matters here is that the name typed is what was sent, that the plain token
// and the words about it are on screen after minting, and that Remove sends the
// phone's id.

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

const pixel: Phone = {
  id: '6b3f2c11-9d3a-4a2e-8f21-2c5a9b0e7d13',
  name: 'Pixel',
  createdBy: 'leo',
  createdAt: '2026-09-01T10:00:00Z',
  lastUsedAt: null
};

const minted: MintedPhone = {
  ...pixel,
  token: 'schall_QUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVowMTIz',
  server: 'https://schall.example.com'
};

function mount() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } }
  });
  return render(PhoneSettings, {
    context: new Map<string, unknown>([['$$_queryClient', client]])
  });
}

describe('PhoneSettings', () => {
  it('lists the phones with when each was added and last used', async () => {
    vi.spyOn(api, 'phones').mockResolvedValue({
      items: [pixel, { ...pixel, id: 'b1', name: 'Tablet', lastUsedAt: new Date().toISOString() }]
    });
    mount();

    await screen.findByText('Pixel');
    expect(screen.getByText('Tablet')).toBeTruthy();
    expect(screen.getAllByText(/^added /).length).toBe(2);
    expect(screen.getByText('never used')).toBeTruthy();
    expect(screen.getByText(/^used /)).toBeTruthy();
  });

  it('sends the typed name and then shows the token once', async () => {
    vi.spyOn(api, 'phones').mockResolvedValue({ items: [] });
    const add = vi.spyOn(api, 'addPhone').mockResolvedValue(minted);
    mount();
    await tick();

    await fireEvent.input(screen.getByLabelText('Name'), { target: { value: '  Pixel  ' } });
    await fireEvent.click(screen.getByRole('button', { name: 'Add phone' }));

    await waitFor(() => expect(add).toHaveBeenCalledWith('Pixel'));
    await screen.findByText(minted.token);
    expect(screen.getByText('Shown once. Scan it or copy it now.')).toBeTruthy();
    // The code the app scans carries the address and the token together.
    await waitFor(() => expect(screen.getByRole('img', { name: 'Sign-in code for Pixel' })).toBeTruthy());
  });

  it('copies the token to the clipboard', async () => {
    vi.spyOn(api, 'phones').mockResolvedValue({ items: [] });
    vi.spyOn(api, 'addPhone').mockResolvedValue(minted);
    const write = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: { writeText: write }
    });
    mount();
    await tick();

    await fireEvent.input(screen.getByLabelText('Name'), { target: { value: 'Pixel' } });
    await fireEvent.click(screen.getByRole('button', { name: 'Add phone' }));
    const copy = await screen.findByRole('button', { name: 'Copy' });
    await fireEvent.click(copy);

    expect(write).toHaveBeenCalledWith(minted.token);
    await screen.findByRole('button', { name: 'Copied' });
  });

  it('asks for nothing until a name is typed', async () => {
    vi.spyOn(api, 'phones').mockResolvedValue({ items: [] });
    mount();
    await tick();

    expect((screen.getByRole('button', { name: 'Add phone' }) as HTMLButtonElement).disabled).toBe(
      true
    );
  });

  it('removes a phone by its id', async () => {
    vi.spyOn(api, 'phones').mockResolvedValue({ items: [pixel] });
    const remove = vi.spyOn(api, 'removePhone').mockResolvedValue(undefined);
    mount();

    await screen.findByText('Pixel');
    await fireEvent.click(screen.getByRole('button', { name: 'Remove' }));

    await waitFor(() => expect(remove).toHaveBeenCalledWith(pixel.id));
  });
});
