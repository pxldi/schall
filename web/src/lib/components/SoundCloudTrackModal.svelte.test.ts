import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { tick } from 'svelte';
import { QueryClient } from '@tanstack/svelte-query';
import { api, type SoundCloudTrack } from '$lib/api';
import SoundCloudTrackModal from '$lib/components/SoundCloudTrackModal.svelte';

// The dialog that names a file by its page on SoundCloud. The decision it makes
// is permanent, so its whole shape is: look up, show what came back, confirm.
// Nothing is written until the second press.

// jsdom implements no Web Animations API, and Svelte drives transitions through
// it. Without this the fade never finishes and nothing is ever removed.
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

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

const fileId = '2f0c2b0e-4d3f-4a0a-9c4a-3f9b6a1c7d21';

const aboEdit: SoundCloudTrack = {
  externalId: 'soundcloud:293',
  title: 'PinkPantheress - Illegal (Abo Edit) by Abo',
  trackTitle: 'PinkPantheress - Illegal (Abo Edit)',
  uploader: 'Abo',
  uploaderUrl: 'https://soundcloud.com/abo',
  artworkUrl: 'https://i1.sndcdn.com/artworks-abc-t500x500.jpg',
  permalink: 'https://soundcloud.com/abo/illegal',
  suggested: { artist: 'PinkPantheress', title: 'Illegal (Abo Edit)', remixer: 'Abo' }
};

function mount() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } }
  });
  return render(SoundCloudTrackModal, {
    props: { open: true, fileId, path: '/music/uploads/illegal.flac' },
    context: new Map<string, unknown>([['$$_queryClient', client]])
  });
}

function field() {
  return document.querySelector('input#soundcloud-url') as HTMLInputElement;
}

function panel() {
  return document.querySelector('[role="dialog"]');
}

async function lookUp(track: SoundCloudTrack = aboEdit) {
  const look = vi.spyOn(api, 'soundCloudTrack').mockResolvedValue(track);
  mount();
  await tick();
  await fireEvent.input(field(), { target: { value: track.permalink } });
  await fireEvent.click(screen.getByRole('button', { name: 'Look up' }));
  await screen.findByRole('button', { name: 'Confirm' });
  return look;
}

describe('SoundCloudTrackModal', () => {
  it('sends the pasted address and shows what came back before anything is decided', async () => {
    const name = vi.spyOn(api, 'nameFromSoundCloud');
    const look = await lookUp();

    expect(look).toHaveBeenCalledWith('https://soundcloud.com/abo/illegal');
    // The whole title as SoundCloud sent it, and the name the file will take.
    expect(screen.getByTitle(aboEdit.title)).toBeTruthy();
    expect(screen.getByText('Uploaded by Abo')).toBeTruthy();
    // The split Schall read off the title, in fields the reader can correct.
    expect((screen.getByLabelText('Artist') as HTMLInputElement).value).toBe('PinkPantheress');
    expect((screen.getByLabelText('Track') as HTMLInputElement).value).toBe('Illegal (Abo Edit)');
    expect((screen.getByLabelText('Remixer') as HTMLInputElement).value).toBe('Abo');
    const artwork = document.querySelector('img') as HTMLImageElement;
    expect(artwork.src).toBe(aboEdit.artworkUrl);
    // Looking is a read. Nothing has been written.
    expect(name).not.toHaveBeenCalled();
  });

  it('names the file only when the reader confirms what they were shown', async () => {
    const name = vi.spyOn(api, 'nameFromSoundCloud').mockResolvedValue({
      track: aboEdit,
      named: { artist: 'PinkPantheress', title: 'Illegal (Abo Edit)', remixer: 'Abo' },
      artistId: '5d0b0a11-4f0c-4a2e-8d1a-1c8f0a3b7e42',
      summary: 'Recorded by hand as a SoundCloud track.',
      pictured: true
    });
    await lookUp();

    await fireEvent.click(screen.getByRole('button', { name: 'Confirm' }));
    await waitFor(() =>
      expect(name).toHaveBeenCalledWith(fileId, 'https://soundcloud.com/abo/illegal', {
        artist: 'PinkPantheress',
        title: 'Illegal (Abo Edit)',
        remixer: 'Abo'
      })
    );
  });

  it('sends the correction rather than the suggestion', async () => {
    const name = vi.spyOn(api, 'nameFromSoundCloud').mockResolvedValue({
      track: aboEdit,
      named: { artist: 'Pink Pantheress', title: 'Illegal', remixer: 'Abo' },
      artistId: '5d0b0a11-4f0c-4a2e-8d1a-1c8f0a3b7e42',
      summary: 'Recorded by hand as a SoundCloud track.',
      pictured: true
    });
    await lookUp();

    await fireEvent.input(screen.getByLabelText('Artist'), {
      target: { value: 'Pink Pantheress' }
    });
    await fireEvent.input(screen.getByLabelText('Track'), { target: { value: 'Illegal' } });
    await fireEvent.click(screen.getByRole('button', { name: 'Confirm' }));

    await waitFor(() =>
      expect(name).toHaveBeenCalledWith(fileId, 'https://soundcloud.com/abo/illegal', {
        artist: 'Pink Pantheress',
        title: 'Illegal',
        remixer: 'Abo'
      })
    );
  });

  it('will not confirm a track with no artist', async () => {
    await lookUp();

    await fireEvent.input(screen.getByLabelText('Artist'), { target: { value: '  ' } });
    await tick();

    expect((screen.getByRole('button', { name: 'Confirm' }) as HTMLButtonElement).disabled).toBe(
      true
    );
  });

  it('takes the answer off the screen when the address is edited', async () => {
    await lookUp();

    await fireEvent.input(field(), { target: { value: 'https://soundcloud.com/abo/other' } });
    await tick();

    // What is on screen has to be what the button will act on, so the button
    // goes back to offering a look-up.
    expect(screen.queryByRole('button', { name: 'Confirm' })).toBeNull();
    expect(screen.getByRole('button', { name: 'Look up' })).toBeTruthy();
    expect(screen.queryByText('Uploaded by Abo')).toBeNull();
    expect(screen.queryByLabelText('Artist')).toBeNull();
  });

  it('says what happened to the link and what to do about it', async () => {
    vi.spyOn(api, 'soundCloudTrack').mockRejectedValue(
      Object.assign(new Error('That link is not a SoundCloud track'), {
        status: 400,
        title: 'That link is not a SoundCloud track',
        details: ['Open the track on SoundCloud and copy the link from the address bar.']
      })
    );
    mount();
    await tick();
    await fireEvent.input(field(), { target: { value: 'https://soundcloud.com/abo' } });
    await fireEvent.click(screen.getByRole('button', { name: 'Look up' }));

    expect(await screen.findByText(/not a SoundCloud track/)).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Confirm' })).toBeNull();
  });

  it('offers nothing to press until an address has been typed', async () => {
    mount();
    await tick();
    const look = screen.getByRole('button', { name: 'Look up' }) as HTMLButtonElement;
    expect(look.disabled).toBe(true);
  });

  it('cycles inside the panel and never reaches the page behind', async () => {
    mount();
    await tick();

    const stops = [
      ...panel()!.querySelectorAll<HTMLElement>('button:not([disabled]), input:not([disabled])')
    ];
    const first = stops[0];
    const last = stops[stops.length - 1];

    last.focus();
    await fireEvent.keyDown(window, { key: 'Tab' });
    expect(document.activeElement).toBe(first);

    await fireEvent.keyDown(window, { key: 'Tab', shiftKey: true });
    expect(document.activeElement).toBe(last);
  });
});
