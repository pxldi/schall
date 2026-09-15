import { afterEach, beforeAll, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor, setup, within } from '@testing-library/svelte';
import { QueryClient, QueryClientProvider } from '@tanstack/svelte-query';
import UploadResolvePass from '$lib/components/UploadResolvePass.svelte';
import type { LibraryFile, UploadFile } from '$lib/api';

beforeAll(setup);
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

const firstID = '1bebfa12-0000-4000-8000-000000000001';
const secondID = '1bebfa12-0000-4000-8000-000000000002';

function file(id: string, name: string): LibraryFile {
  return {
    id,
    path: `/music/Uploads/${name}`,
    sizeBytes: 42,
    modifiedAt: '2026-08-30T10:00:00Z',
    matchStatus: 'ambiguous',
    matchCandidateCount: 1,
    mappingManual: false,
    resolutionStatus: 'resolved',
    identityManual: false,
    identityCandidateCount: 0
  };
}

function files(): UploadFile[] {
  return [
    { name: '01.flac', state: 'waiting', file: file(firstID, '01.flac') },
    { name: '02.flac', state: 'waiting', file: file(secondID, '02.flac') },
    { name: '03.flac', state: 'imported', file: { ...file('1bebfa12-0000-4000-8000-000000000003', '03.flac'), matchStatus: 'matched' } },
    { name: '05.flac', state: 'set_aside', file: file('1bebfa12-0000-4000-8000-000000000005', '05.flac') },
    { name: '04.flac', state: 'failed' }
  ];
}

it('lists importing, imported, waiting, set-aside, and failed file states', () => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(UploadResolvePass, {
    props: {
      files: [{ name: '06.flac', state: 'importing' }, ...files()]
    }
  }, { wrapper: QueryClientProvider, wrapperProps: { client } });

  expect(screen.getByText('01.flac')).toBeTruthy();
  expect(screen.getAllByText('Waiting for an answer')).toHaveLength(2);
  expect(screen.getByText('Imported')).toBeTruthy();
  expect(screen.getAllByText('Set aside')).toHaveLength(2);
  expect(screen.getByText('Importing automatically')).toBeTruthy();
  const failedRow = screen.getByText('04.flac').parentElement;
  expect(failedRow).toBeTruthy();
  expect(within(failedRow!).getByText('Import failed')).toBeTruthy();
  expect(within(failedRow!).queryByText('Importing automatically')).toBeNull();
});

it('moves to the next waiting file after a manual match or setting one aside', async () => {
  const asked = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url.includes('/matches')) {
      return new Response(
        JSON.stringify({
          items: [
            {
              trackId: '7bebfa12-0000-4000-8000-000000000007',
              title: 'Illegal',
              albumTitle: 'Single',
              artistName: 'PinkPantheress',
              discNumber: 1,
              trackNumber: 1,
              method: 'catalogue_search',
              confidence: 1,
              alreadyMapped: false,
              manuallyMapped: false
            }
          ]
        })
      );
    }
    if (url.endsWith(`/library/files/${firstID}/match`)) {
      expect(init?.method).toBe('POST');
      expect(init?.body).toBe(
        JSON.stringify({ trackId: '7bebfa12-0000-4000-8000-000000000007', reassign: false })
      );
      return new Response(null, { status: 204 });
    }
    if (url.endsWith(`/library/files/${secondID}/set-aside`)) {
      expect(init?.method).toBe('POST');
      return new Response(null, { status: 204 });
    }
    throw new Error(`unexpected request: ${url}`);
  });
  vi.stubGlobal('fetch', asked);

  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(UploadResolvePass, { props: { files: files() } }, {
    wrapper: QueryClientProvider,
    wrapperProps: { client }
  });
  expect(
    screen.getByRole('button', { name: 'Match by hand' }).getAttribute('aria-pressed')
  ).toBe('false');
  await fireEvent.click(screen.getByRole('button', { name: 'Match by hand' }));
  expect(
    screen.getByRole('button', { name: 'Match by hand' }).getAttribute('aria-pressed')
  ).toBe('true');
  await screen.findByRole('button', { name: /Illegal/ });
  await fireEvent.click(screen.getByRole('button', { name: /Illegal/ }));

  await waitFor(() => expect(screen.getByText('02.flac needs an answer')).toBeTruthy());
  expect(screen.queryByText('01.flac needs an answer')).toBeNull();

  expect(screen.getByText('Set aside keeps 02.flac unmatched until you choose Return to review in Library.')).toBeTruthy();
  const setAside = screen.getByRole('button', { name: 'Set aside' });
  expect(setAside.getAttribute('aria-pressed')).toBeNull();
  expect((setAside as HTMLButtonElement).disabled).toBe(false);
  expect(asked).toHaveBeenCalledTimes(2);
  await fireEvent.click(setAside);
  await waitFor(() => expect(screen.getByText('No more files need an answer in this pass.')).toBeTruthy());
  expect(asked).toHaveBeenCalledTimes(3);
});

it('sets aside the current file and advances without writing a decision', async () => {
  const asked = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url.includes('/matches')) return new Response(JSON.stringify({ items: [] }));
    if (url.endsWith(`/library/files/${firstID}/set-aside`)) {
      expect(init?.method).toBe('POST');
      return new Response(null, { status: 204 });
    }
    if (url.endsWith(`/library/files/${secondID}/set-aside`)) {
      expect(init?.method).toBe('POST');
      return new Response(null, { status: 204 });
    }
    throw new Error(`unexpected request: ${url}`);
  });
  vi.stubGlobal('fetch', asked);

  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(UploadResolvePass, { props: { files: files() } }, {
    wrapper: QueryClientProvider,
    wrapperProps: { client }
  });

  await waitFor(() => expect(screen.getByText('01.flac needs an answer')).toBeTruthy());
  expect(screen.getByText('Set aside keeps 01.flac unmatched until you choose Return to review in Library.')).toBeTruthy();
  const setAside = screen.getByRole('button', { name: 'Set aside' });
  expect(setAside.getAttribute('aria-pressed')).toBeNull();
  expect((setAside as HTMLButtonElement).disabled).toBe(false);

  await fireEvent.click(setAside);

  await waitFor(() => expect(screen.getByText('02.flac needs an answer')).toBeTruthy());
  expect(screen.queryByText('01.flac needs an answer')).toBeNull();
  expect(asked.mock.calls.filter(([, init]) => init?.method)).toHaveLength(1);
});
