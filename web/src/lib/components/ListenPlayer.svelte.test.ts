import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, render, setup } from '@testing-library/svelte';
import { flushSync } from 'svelte';
import ListenPlayer from '$lib/components/ListenPlayer.svelte';
import { setVolume } from '$lib/preview.svelte';

// The player of listening mode. Moving the volume is a change of level and
// nothing else: the copy that is playing has to keep playing where it is.

let load: ReturnType<typeof vi.fn>;

beforeAll(setup);

beforeEach(() => {
  load = vi.fn();
  Object.assign(HTMLMediaElement.prototype, {
    play: vi.fn().mockResolvedValue(undefined),
    pause: vi.fn(),
    load
  });
});

afterEach(() => {
  cleanup();
});

describe('the volume', () => {
  it('changes the level without loading the copy again', () => {
    setVolume(0.5);
    render(ListenPlayer, { src: '/copy-a.flac', name: 'Evensong', autoplay: false });
    flushSync();
    const loads = load.mock.calls.length;

    setVolume(0.2);
    flushSync();

    expect(load.mock.calls.length).toBe(loads);
    expect(document.querySelector('audio')!.volume).toBe(0.2);
  });
});
