import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen } from '@testing-library/svelte';

import AudioPreview from '$lib/components/AudioPreview.svelte';
import { listening } from '$lib/preview.svelte';

// The player that stands wherever a permanent decision is asked about a file.
// What is pinned here is the part that is not decoration: it opens in the middle
// of the track, one volume serves the page, and two players never sound at once.

// jsdom implements no media element, so play and load are stubs. Everything the
// component decides — where playback starts, what the button says, which player
// falls silent — is decided before the audio would make any sound.
beforeEach(() => {
  HTMLMediaElement.prototype.play = vi.fn().mockResolvedValue(undefined);
  HTMLMediaElement.prototype.pause = vi.fn();
  HTMLMediaElement.prototype.load = vi.fn();
  listening.volume = 0.7;
  listening.sounding = '';
});

afterEach(cleanup);

function draw(props: Record<string, unknown> = {}) {
  const { container } = render(AudioPreview, {
    props: { src: '/api/v1/library/files/one/audio', ...props }
  });
  return {
    container,
    audio: () => container.querySelector('audio') as HTMLAudioElement
  };
}

/** jsdom will not tell an element how long it is, so the length is written on
 * before the metadata event that reads it. */
function arrives(audio: HTMLAudioElement, seconds: number) {
  Object.defineProperty(audio, 'duration', { value: seconds, configurable: true });
  return fireEvent.loadedMetadata(audio);
}

describe('a file being decided about', () => {
  it('exposes a keyboard slider and seeks with the arrow keys', async () => {
    const { audio } = draw();
    const slider = screen.getByRole('slider', { name: 'Seek' });

    expect(slider.getAttribute('tabindex')).toBe('0');
    expect(slider.getAttribute('aria-valuemin')).toBe('0');
    expect(slider.getAttribute('aria-valuemax')).toBe('0');

    await fireEvent.click(screen.getByLabelText('Play this file from the middle'));
    await arrives(audio(), 200);
    await fireEvent.keyDown(slider, { key: 'ArrowRight' });

    expect(audio().currentTime).toBe(105);
    expect(slider.getAttribute('aria-valuenow')).toBe('105');
  });

  it('starts in the middle of the track, where a copy says what it is', async () => {
    const { audio } = draw();

    await fireEvent.click(screen.getByLabelText('Play this file from the middle'));
    await arrives(audio(), 200);

    expect(audio().currentTime).toBe(100);
  });

  it('is fetched from the source it was given, not before it is asked for', async () => {
    const { audio } = draw();

    expect(audio().getAttribute('preload')).toBe('none');

    await fireEvent.click(screen.getByLabelText('Play this file from the middle'));

    expect(audio().src).toContain('/api/v1/library/files/one/audio');
  });

  it('offers to pause once it is sounding', async () => {
    const { audio } = draw();

    await fireEvent.click(screen.getByLabelText('Play this file from the middle'));
    await fireEvent.play(audio());

    expect(screen.getByLabelText('Pause this file')).toBeTruthy();
  });

  it('says so plainly when it cannot be played', async () => {
    const { audio } = draw();

    await fireEvent.click(screen.getByLabelText('Play this file from the middle'));
    await fireEvent.error(audio());

    expect(screen.getByText('Schall could not play this file.')).toBeTruthy();
  });
});

describe('the volume', () => {
  it('is the one the page already had, so a second player does not blast', () => {
    listening.volume = 0.2;

    const { audio } = draw();
    const slider = screen.getByLabelText('Volume') as HTMLInputElement;

    expect(slider.value).toBe('20');
    expect(audio()).toBeTruthy();
  });

  it('is kept for every other player once it is moved', async () => {
    draw();

    await fireEvent.input(screen.getByLabelText('Volume'), { target: { value: '30' } });

    expect(listening.volume).toBeCloseTo(0.3);
  });

  it('is drawn once where two players share it', () => {
    draw({ volume: false });

    expect(screen.queryByLabelText('Volume')).toBeNull();
  });
});

describe('two players on one page', () => {
  it('falls silent when the other one starts, so a comparison is one at a time', async () => {
    const { audio } = draw();

    await fireEvent.click(screen.getByLabelText('Play this file from the middle'));
    await fireEvent.play(audio());
    expect(screen.getByLabelText('Pause this file')).toBeTruthy();

    // What the second player does when it starts.
    listening.sounding = '/api/v1/library/files/two/audio';
    await Promise.resolve();

    expect(screen.getByLabelText('Play this file from the middle')).toBeTruthy();
  });
});
