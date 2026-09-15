import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen } from '@testing-library/svelte';
import type { DuplicateCopy, DuplicateRecording } from '$lib/api';
import KeepOneCopyDialog from '$lib/components/KeepOneCopyDialog.svelte';

// The one confirm before Schall deletes music on purpose. What is held here is
// what the person reads before pressing: every file that goes, by path and by
// size, and the one that stays. A dialog that named the wrong file would be the
// worst bug this feature could have.

// jsdom implements no Web Animations API and Svelte drives transitions through
// it, so the fade would never finish.
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

function copy(overrides: Partial<DuplicateCopy> & { id: string; path: string }): DuplicateCopy {
  return { sizeBytes: 1_000_000, reason: 'identified as this recording', ...overrides };
}

const keeper = copy({
  id: 'keeper',
  path: '/music/Massive Attack/Mezzanine/06 Teardrop.flac',
  sizeBytes: 41_000_000
});

function recording(copies: DuplicateCopy[]): DuplicateRecording {
  return {
    recordingId: 'recording-1',
    title: 'Teardrop',
    artist: 'Massive Attack',
    copies,
    verdict: '',
    decided: false
  };
}

function open(copies: DuplicateCopy[], props: Record<string, unknown> = {}) {
  const onconfirm = vi.fn();
  const oncancel = vi.fn();
  render(KeepOneCopyDialog, {
    props: { recording: recording(copies), keeper, onconfirm, oncancel, ...props }
  });
  return { onconfirm, oncancel };
}

describe('KeepOneCopyDialog', () => {
  it('names every file that goes and the one that stays', () => {
    open([
      keeper,
      copy({ id: 'spare', path: '/music/singles/Teardrop.mp3', sizeBytes: 7_300_000 })
    ]);

    expect(screen.getByText('/music/Massive Attack/Mezzanine/06 Teardrop.flac')).toBeTruthy();
    expect(screen.getByText('/music/singles/Teardrop.mp3')).toBeTruthy();
    expect(screen.getByText('Stays')).toBeTruthy();
    expect(screen.getByText('Deleted')).toBeTruthy();
  });

  it('says how many copies go and that it cannot be undone', () => {
    open([
      keeper,
      copy({ id: 'spare-a', path: '/music/singles/Teardrop.mp3' }),
      copy({ id: 'spare-b', path: '/music/rips/teardrop.mp3' })
    ]);

    expect(screen.getByText(/2 copies of Teardrop · Massive Attack will be deleted/)).toBeTruthy();
    expect(screen.getByText(/cannot be undone/)).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Delete 2 copies' })).toBeTruthy();
  });

  it('names the one copy in the singular', () => {
    open([keeper, copy({ id: 'spare', path: '/music/singles/Teardrop.mp3' })]);

    expect(screen.getByRole('button', { name: 'Delete the other copy' })).toBeTruthy();
  });

  it('leaves out a copy that is also a copy of other music, and says so', () => {
    open([
      keeper,
      copy({ id: 'spare', path: '/music/singles/Teardrop.mp3' }),
      copy({ id: 'shared', path: '/music/comps/Trip Hop/04 Teardrop.mp3', answersOther: true })
    ]);

    expect(screen.queryByText('/music/comps/Trip Hop/04 Teardrop.mp3')).toBeNull();
    expect(screen.getByText(/1 copy stays as well/)).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Delete the other copy' })).toBeTruthy();
  });

  it('offers nothing when every other copy is also a copy of other music', () => {
    open([
      keeper,
      copy({ id: 'shared', path: '/music/comps/Trip Hop/04 Teardrop.mp3', answersOther: true })
    ]);

    expect(
      screen.getByRole('button', { name: 'Nothing to delete' }).hasAttribute('disabled')
    ).toBe(true);
  });

  it('dims every way out and back while the delete is running', () => {
    open([keeper, copy({ id: 'spare', path: '/music/singles/Teardrop.mp3' })], { busy: true });

    expect(screen.getByRole('button', { name: /Deleting/ }).hasAttribute('disabled')).toBe(true);
    expect(screen.getByRole('button', { name: 'Cancel' }).hasAttribute('disabled')).toBe(true);
    expect(screen.getByRole('button', { name: 'Close' }).hasAttribute('disabled')).toBe(true);
  });

  it('stays put on Escape while the delete is running', async () => {
    const { oncancel } = open(
      [keeper, copy({ id: 'spare', path: '/music/singles/Teardrop.mp3' })],
      { busy: true }
    );

    await fireEvent.keyDown(window, { key: 'Escape' });
    expect(oncancel).not.toHaveBeenCalled();
  });

  it('deletes only when the confirm is pressed', async () => {
    const { onconfirm, oncancel } = open([
      keeper,
      copy({ id: 'spare', path: '/music/singles/Teardrop.mp3' })
    ]);

    await fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(oncancel).toHaveBeenCalledTimes(1);
    expect(onconfirm).not.toHaveBeenCalled();

    await fireEvent.click(screen.getByRole('button', { name: 'Delete the other copy' }));
    expect(onconfirm).toHaveBeenCalledTimes(1);
  });

  it('takes focus itself, so the first key pressed is not the irreversible one', () => {
    open([keeper, copy({ id: 'spare', path: '/music/singles/Teardrop.mp3' })]);

    expect(document.activeElement?.getAttribute('role')).toBe('dialog');
  });

  it('locks the page behind it while it is open', () => {
    open([keeper, copy({ id: 'spare', path: '/music/singles/Teardrop.mp3' })]);

    expect(document.body.style.overflow).toBe('hidden');
    cleanup();
    expect(document.body.style.overflow).toBe('');
  });

  it('leaves on Escape while nothing is running', async () => {
    const { oncancel } = open([keeper, copy({ id: 'spare', path: '/music/singles/Teardrop.mp3' })]);

    await fireEvent.keyDown(window, { key: 'Escape' });
    expect(oncancel).toHaveBeenCalledTimes(1);
  });
});
