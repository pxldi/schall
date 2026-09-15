import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, setup, waitFor } from '@testing-library/svelte';
import { QueryClient, QueryClientProvider } from '@tanstack/svelte-query';
import type { ImportSettings, TranscodeSweep } from '$lib/api';
import { letTheScrollbarComeBack, shimTheMissingBrowser } from './ui/test-window';

// Transcode after import: an optional setting that re-encodes a
// lossless import (or a chosen loud one) to MP3, strictly after a file is
// proven and imported, plus a button that runs the same shrink over the
// library already on disc. Nothing here decides what a file is — it never
// reaches out to any endpoint but the two boundaries stubbed below.
vi.mock('$app/state', () => ({
  page: { params: {}, url: new URL('http://localhost/settings/library') }
}));

const { default: TranscodeSettings } = await import('$lib/components/TranscodeSettings.svelte');

function settings(overrides: Partial<ImportSettings> = {}): ImportSettings {
  return {
    sourceRetention: 'keep',
    inboxWritable: true,
    acoustidKeySet: false,
    acoustidEnabled: false,
    transcodeEnabled: false,
    transcodeTarget: 'mp3',
    transcodeBitrate: '320',
    transcodeWhen: 'lossless',
    transcoderPresent: true,
    ...overrides
  };
}

function sweep(overrides: Partial<TranscodeSweep> = {}): TranscodeSweep {
  return {
    status: 'idle',
    transcoded: 0,
    skipped: 0,
    failed: 0,
    eligible: 0,
    ...overrides
  };
}

let storedSettings: ImportSettings = settings();
let storedSweep: TranscodeSweep = sweep();
/** What was PUT to the import settings, decoded, so a test can say what was
 * saved rather than which field was clicked. */
let saved: Record<string, unknown> | null = null;
/** Whether the sweep endpoint was ever POSTed to. */
let sweepQueued = false;

function answering() {
  saved = null;
  sweepQueued = false;
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), 'http://localhost');
      if (url.pathname === '/api/v1/settings/imports') {
        if (init?.method === 'PUT') {
          saved = JSON.parse(String(init.body));
          return new Response(JSON.stringify(storedSettings), { status: 200 });
        }
        return new Response(JSON.stringify(storedSettings), { status: 200 });
      }
      if (url.pathname === '/api/v1/library/transcode') {
        if (init?.method === 'POST') {
          sweepQueued = true;
          return new Response(
            JSON.stringify({ jobId: 'job-1', status: 'queued', createdAt: '2026-08-01T00:00:00Z' }),
            { status: 202 }
          );
        }
        return new Response(JSON.stringify(storedSweep), { status: 200 });
      }
      return new Response(JSON.stringify({}), { status: 200 });
    })
  );
}

function opened() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(TranscodeSettings, {}, { wrapper: QueryClientProvider, wrapperProps: { client } });
}

beforeAll(setup);
beforeAll(shimTheMissingBrowser);

beforeEach(() => {
  storedSettings = settings();
  storedSweep = sweep();
  answering();
});

afterEach(async () => {
  cleanup();
  vi.unstubAllGlobals();
  await letTheScrollbarComeBack();
});

describe('the setting', () => {
  it('is named plainly and starts off', async () => {
    opened();

    expect(await screen.findByText('Transcode after import')).toBeTruthy();
    expect(screen.getByText('off')).toBeTruthy();
  });

  it('says when it runs and when the original is deleted', async () => {
    opened();

    expect(
      await screen.findByText(
        'Runs after a file is proven and imported; the original is deleted only once the MP3 is written and checked.',
        { exact: false }
      )
    ).toBeTruthy();
  });

  it('turns re-encoding on with the target format and the stored quality', async () => {
    opened();

    const toggle = (await screen.findByLabelText('enabled')) as HTMLInputElement;
    await waitFor(() => expect(toggle.disabled).toBe(false));
    await fireEvent.click(toggle);

    await waitFor(() => expect(saved).not.toBeNull());
    expect(saved).toMatchObject({
      transcodeEnabled: true,
      transcodeTarget: 'mp3',
      transcodeBitrate: '320',
      transcodeWhen: 'lossless'
    });
  });

  it('sends which files are re-encoded when that choice is pressed', async () => {
    opened();

    const choice = await screen.findByText(
      'Lossless files, and lossy ones above the target rate'
    );
    const choiceButton = choice.closest('button') as HTMLButtonElement;
    await waitFor(() => expect(choiceButton.disabled).toBe(false));
    await fireEvent.click(choiceButton);

    await waitFor(() => expect(saved).not.toBeNull());
    expect((saved as Record<string, unknown>).transcodeWhen).toBe('above_target');
  });

  it('sends the quality chosen from the MP3 picker', async () => {
    opened();

    const trigger = (await screen.findByLabelText('MP3 quality')) as HTMLButtonElement;
    await waitFor(() => expect(trigger.disabled).toBe(false));
    trigger.focus();
    await fireEvent.keyDown(trigger, { key: 'Enter' });
    await screen.findByRole('listbox');

    // Typeahead jumps to the choice starting with the letter typed, the same
    // way a keyboard picks anything from this list (ui/select's own tests).
    await fireEvent.keyDown(document.activeElement ?? document.body, { key: 'v' });
    await fireEvent.keyDown(document.activeElement ?? document.body, { key: 'Enter' });

    await waitFor(() => expect(saved).not.toBeNull());
    expect((saved as Record<string, unknown>).transcodeBitrate).toBe('V0');
  });

  it('keeps the retention policy in the request when only this card changes', async () => {
    storedSettings = settings({ sourceRetention: 'delete', acoustidEnabled: true });
    opened();

    const toggle = (await screen.findByLabelText('enabled')) as HTMLInputElement;
    await waitFor(() => expect(toggle.disabled).toBe(false));
    await fireEvent.click(toggle);

    await waitFor(() => expect(saved).not.toBeNull());
    expect(saved).toMatchObject({ sourceRetention: 'delete', acoustidEnabled: true });
  });

  it('shows the ffmpeg-missing state plainly when the API reports it', async () => {
    storedSettings = settings({ transcoderPresent: false, transcoderDetail: 'ffmpeg could not be found' });
    opened();

    expect(await screen.findByText(/ffmpeg could not be found/)).toBeTruthy();
    expect(screen.getByText(/Install ffmpeg to use this setting/)).toBeTruthy();
  });

  it('says nothing about a missing encoder once one is found', async () => {
    opened();

    await screen.findByText('Transcode after import');
    expect(screen.queryByText(/Install ffmpeg to use this setting/)).toBeNull();
  });

  it('shows the request failed rather than blaming ffmpeg when settings cannot be read', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => new Response(JSON.stringify({ title: 'settings are unavailable' }), { status: 503 }))
    );
    opened();

    expect(await screen.findByText('Schall could not read the settings.')).toBeTruthy();
    expect(screen.queryByText(/ffmpeg could not be found/)).toBeNull();
    expect(screen.queryByText(/Install ffmpeg to use this setting/)).toBeNull();
  });

  it('shows the failure beside the control when saving is refused', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = new URL(String(input), 'http://localhost');
        if (url.pathname === '/api/v1/settings/imports' && init?.method === 'PUT') {
          return new Response(JSON.stringify({ title: 'library is unavailable' }), { status: 503 });
        }
        if (url.pathname === '/api/v1/settings/imports') {
          return new Response(JSON.stringify(storedSettings), { status: 200 });
        }
        if (url.pathname === '/api/v1/library/transcode') {
          return new Response(JSON.stringify(storedSweep), { status: 200 });
        }
        return new Response(JSON.stringify({}), { status: 200 });
      })
    );
    opened();

    const toggle = (await screen.findByLabelText('enabled')) as HTMLInputElement;
    await waitFor(() => expect(toggle.disabled).toBe(false));
    await fireEvent.click(toggle);

    expect(await screen.findByText('Schall could not read the library.')).toBeTruthy();
  });
});

describe('transcoding the existing library', () => {
  it('shows how many files are eligible', async () => {
    storedSettings = settings({ transcodeEnabled: true });
    storedSweep = sweep({ eligible: 42 });
    opened();

    expect(await screen.findByText(/42 files are eligible and would be shrunk/)).toBeTruthy();
  });

  it('posts to the sweep endpoint when the button is pressed', async () => {
    storedSettings = settings({ transcodeEnabled: true });
    storedSweep = sweep({ eligible: 5 });
    opened();

    const button = (await screen.findByRole('button', { name: /Transcode the library/ })) as HTMLButtonElement;
    await waitFor(() => expect(button.disabled).toBe(false));
    await waitFor(() => expect(button).not.toHaveProperty('disabled', true));
    await fireEvent.click(button);

    await waitFor(() => expect(sweepQueued).toBe(true));
  });

  it('refuses to run while the setting itself is off', async () => {
    storedSettings = settings({ transcodeEnabled: false });
    storedSweep = sweep({ eligible: 5 });
    opened();

    const button = (await screen.findByRole('button', {
      name: /Transcode the library/
    })) as HTMLButtonElement;
    expect(button.disabled).toBe(true);
    expect(await screen.findByText('turn re-encoding on above to use this')).toBeTruthy();
  });

  it('shows progress while a pass is running', async () => {
    storedSettings = settings({ transcodeEnabled: true });
    storedSweep = sweep({ status: 'running', eligible: 10, transcoded: 3 });
    opened();

    expect(await screen.findByText(/3 of 10 files/)).toBeTruthy();
  });

  it('shows the failure when queuing a sweep is refused', async () => {
    storedSettings = settings({ transcodeEnabled: true });
    storedSweep = sweep({ eligible: 5 });
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = new URL(String(input), 'http://localhost');
        if (url.pathname === '/api/v1/settings/imports') {
          return new Response(JSON.stringify(storedSettings), { status: 200 });
        }
        if (url.pathname === '/api/v1/library/transcode') {
          if (init?.method === 'POST') {
            return new Response(JSON.stringify({ title: 'library is unavailable' }), { status: 503 });
          }
          return new Response(JSON.stringify(storedSweep), { status: 200 });
        }
        return new Response(JSON.stringify({}), { status: 200 });
      })
    );
    opened();

    const button = await screen.findByRole('button', { name: /Transcode the library/ });
    await waitFor(() => expect(button).not.toHaveProperty('disabled', true));
    await fireEvent.click(button);

    expect(await screen.findByText('Schall could not read the library.')).toBeTruthy();
  });
});
