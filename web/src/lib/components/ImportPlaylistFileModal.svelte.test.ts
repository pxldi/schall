import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { tick } from 'svelte';
import { QueryClient } from '@tanstack/svelte-query';
import { api, type PlaylistFilePreview } from '$lib/api';
import ImportPlaylistFileModal from '$lib/components/ImportPlaylistFileModal.svelte';

// The file is read twice, deliberately: once for the preview, once for the
// import. The import must be given the file itself and never the preview the
// browser was shown — that is the whole reason a second request exists.

beforeAll(() => {
  if (!Element.prototype.animate) {
    Element.prototype.animate = function () {
      const animation = {
        onfinish: null as (() => void) | null,
        currentTime: 0,
        playbackRate: 1,
        startTime: 0,
        effect: { getComputedTiming: () => ({ duration: 0 }) },
        play() {},
        pause() {},
        cancel() {},
        finish() {
          this.onfinish?.();
        }
      };
      setTimeout(() => animation.onfinish?.(), 0);
      return animation as unknown as Animation;
    };
  }
});

beforeEach(() => {
  const heading = document.createElement('h1');
  heading.textContent = 'Playlists';
  document.body.append(heading);
});

afterEach(() => {
  cleanup();
  document.body.innerHTML = '';
  document.body.style.overflow = '';
  document.body.style.paddingRight = '';
  vi.restoreAllMocks();
});

function mount() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } }
  });
  return render(ImportPlaylistFileModal, {
    props: { open: true },
    context: new Map<string, unknown>([['$$_queryClient', client]])
  });
}

function samplePreview(): PlaylistFilePreview {
  return {
    name: 'Evening',
    format: 'csv',
    columns: ['title', 'artist', 'isrc'],
    rowCount: 2,
    rows: [
      { position: 1, artist: '$NOT', title: 'Motorola', album: '', durationMs: 0, isrc: 'QM42K1858315', path: '' },
      { position: 2, artist: 'BONES', title: 'HDMI', album: '', durationMs: 0, isrc: '', path: '' }
    ],
    skipped: []
  };
}

async function chooseFile(file: File) {
  const input = document.querySelector('input#playlist-file') as HTMLInputElement;
  await fireEvent.change(input, { target: { files: [file] } });
}

describe('ImportPlaylistFileModal', () => {
  it('reads the chosen file and shows what it found', async () => {
    vi.spyOn(api, 'previewPlaylistFile').mockResolvedValue(samplePreview());
    mount();
    await tick();

    const file = new File(['a,b'], 'Evening.csv', { type: 'text/csv' });
    await chooseFile(file);

    await waitFor(() => expect(api.previewPlaylistFile).toHaveBeenCalledWith(file));
    await waitFor(() => expect(screen.getByText(/2 songs read from Evening.csv/)).toBeTruthy());
    expect((document.querySelector('#playlist-file-name') as HTMLInputElement).value).toBe(
      'Evening'
    );
  });

  it('imports the file itself with the confirmed name, not the preview', async () => {
    vi.spyOn(api, 'previewPlaylistFile').mockResolvedValue(samplePreview());
    vi.spyOn(api, 'importPlaylistFile').mockResolvedValue({
      id: 'p1',
      source: 'file',
      sourceId: 'Evening.csv',
      name: 'Evening songs',
      description: '',
      ownerName: '',
      trackCount: 2,
      entryCount: 2,
      ownedCount: 0,
      importedAt: '2026-08-28T00:00:00Z',
      createdAt: '2026-08-28T00:00:00Z'
    });
    mount();
    await tick();

    const file = new File(['a,b'], 'Evening.csv', { type: 'text/csv' });
    await chooseFile(file);
    await waitFor(() => expect(screen.getByText(/songs read from/)).toBeTruthy());

    const nameField = document.querySelector('#playlist-file-name') as HTMLInputElement;
    await fireEvent.input(nameField, { target: { value: 'Evening songs' } });

    await fireEvent.click(screen.getByRole('button', { name: 'Import' }));

    await waitFor(() =>
      expect(api.importPlaylistFile).toHaveBeenCalledWith(file, 'Evening songs')
    );
  });

  it('closes itself once the import lands', async () => {
    vi.spyOn(api, 'previewPlaylistFile').mockResolvedValue(samplePreview());
    vi.spyOn(api, 'importPlaylistFile').mockResolvedValue({
      id: 'p1',
      source: 'file',
      sourceId: 'Evening.csv',
      name: 'Evening',
      description: '',
      ownerName: '',
      trackCount: 2,
      entryCount: 2,
      ownedCount: 0,
      importedAt: '2026-08-28T00:00:00Z',
      createdAt: '2026-08-28T00:00:00Z'
    });
    mount();
    await tick();
    await chooseFile(new File(['a,b'], 'Evening.csv', { type: 'text/csv' }));
    await waitFor(() => expect(screen.getByText(/songs read from/)).toBeTruthy());

    await fireEvent.click(screen.getByRole('button', { name: 'Import' }));

    await waitFor(() => expect(document.body.style.overflow).toBe(''));
  });

  it('stays open with the failure shown, when the file cannot be read', async () => {
    vi.spyOn(api, 'previewPlaylistFile').mockRejectedValue(
      new Error('this file cannot be read as a playlist')
    );
    mount();
    await tick();
    await chooseFile(new File(['x'], 'notes.txt', { type: 'text/plain' }));

    await waitFor(() =>
      expect(screen.getByText('this file cannot be read as a playlist')).toBeTruthy()
    );
    expect(document.querySelector('[role="dialog"]')).not.toBeNull();
  });
});
