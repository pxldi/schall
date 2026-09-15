import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, within } from '@testing-library/svelte';
import type { AcquiredCopy } from '$lib/api';
import { listening } from '$lib/preview.svelte';
import CopyCard from '$lib/components/CopyCard.svelte';

// One copy, played and compared. jsdom implements no media element and no
// real layout, so play/pause/load are stubs and a seek's target rectangle is
// written on by hand — the same technique AudioPreview's own test uses.

function copy(overrides: Partial<AcquiredCopy> = {}): AcquiredCopy {
  return {
    id: 'copy-1',
    provider: 'slskd',
    username: 'peer',
    path: '/incoming/evensong.flac',
    name: 'evensong.flac',
    sizeBytes: 11_300_000,
    verdict: 'held',
    decidedBy: 'schall',
    summary: 'held',
    decidedAt: '2026-01-01T00:00:00Z',
    evidence: {
      name: 'evensong.flac',
      observed: { artist: 'Kestrel Grove', title: 'Evensong', durationMs: 281_000, album: 'Nine Lanterns' },
      wanted: { artist: 'Kestrel Grove', title: 'Evensong', durationMs: 281_000, album: 'Nine Lanterns' },
      agrees: ['release'],
      differs: [],
      problems: [],
      bitRate: 320,
      sizeBytes: 11_300_000
    },
    ...overrides
  };
}

beforeEach(() => {
  HTMLMediaElement.prototype.play = vi.fn().mockResolvedValue(undefined);
  HTMLMediaElement.prototype.pause = vi.fn();
  HTMLMediaElement.prototype.load = vi.fn();
  listening.volume = 0.7;
  listening.sounding = '';
  vi.stubGlobal(
    'fetch',
    vi.fn(async () => new Response(null, { status: 404 }))
  );
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

function arrives(audio: HTMLAudioElement, seconds: number) {
  Object.defineProperty(audio, 'duration', { value: seconds, configurable: true });
  return fireEvent.loadedMetadata(audio);
}

/** jsdom lays nothing out, so a slider's own width is written on before a
 * click is asked to seek by a fraction of it. */
function widened(element: Element, width: number) {
  vi.spyOn(element, 'getBoundingClientRect').mockReturnValue({
    width,
    left: 0,
    right: width,
    top: 0,
    bottom: 10,
    height: 10,
    x: 0,
    y: 0,
    toJSON: () => ''
  } as DOMRect);
}

describe('the facts a card states', () => {
  it('shows no length glyph when the grader has no verdict', () => {
    render(CopyCard, {
      props: {
        heading: 'Copy 1',
        copy: copy({
          evidence: { ...copy().evidence!, observed: { durationMs: 214_000 }, wanted: { durationMs: 214_500 }, agrees: [], differs: [] }
        }),
        wantedMs: 214_500,
        selected: false,
        onselect: () => {}
      }
    });

    expect(screen.getByText('3:34')).toBeTruthy();
    expect(screen.queryByLabelText('agrees')).toBeNull();
    expect(screen.queryByLabelText('differs')).toBeNull();
  });

  it('shows a tick when the grader says the length agrees', () => {
    render(CopyCard, {
      props: {
        heading: 'Copy 1',
        copy: copy({ evidence: { ...copy().evidence!, observed: { durationMs: 214_000 }, wanted: { durationMs: 214_500 }, agrees: ['duration'], differs: [] } }),
        wantedMs: 214_500,
        selected: false,
        onselect: () => {}
      }
    });

    expect(screen.getByLabelText('agrees')).toBeTruthy();
  });

  it('shows a cross and wanted length when the grader says the length differs', () => {
    render(CopyCard, {
      props: {
        heading: 'Copy 1',
        copy: copy({ evidence: { ...copy().evidence!, observed: { durationMs: 211_500 }, wanted: { durationMs: 214_500 }, agrees: [], differs: ['duration (3 s out)'] } }),
        wantedMs: 214_500,
        selected: false,
        onselect: () => {}
      }
    });

    expect(screen.getByText('3:31, wanted 3:34')).toBeTruthy();
    expect(screen.getByLabelText('differs')).toBeTruthy();
  });

  it('marks a length more than a second off in fail colour, naming what was wanted', () => {
    render(CopyCard, {
      props: {
        heading: 'Copy 1',
        copy: copy({ evidence: { ...copy().evidence!, observed: { durationMs: 278_000 }, agrees: [], differs: ['duration (3 s out)'] } }),
        wantedMs: 281_000,
        selected: false,
        onselect: () => {}
      }
    });

    const length = screen.getByText('4:38, wanted 4:41');
    expect(screen.getByLabelText('differs')).toBeTruthy();
  });

  it('reads the format from the extension and bit rate', () => {
    render(CopyCard, {
      props: {
        heading: 'Copy 1',
        copy: copy({ name: 'evensong.mp3' }),
        wantedMs: 281_000,
        selected: false,
        onselect: () => {}
      }
    });

    expect(screen.getByText('MP3 320')).toBeTruthy();
  });

  it('names a lossless format with no bit rate attached', () => {
    render(CopyCard, {
      props: {
        heading: 'Copy 1',
        copy: copy({ name: 'evensong.flac' }),
        wantedMs: 281_000,
        selected: false,
        onselect: () => {}
      }
    });

    expect(screen.getByText('FLAC')).toBeTruthy();
  });

  it('shows the album as none, in ink-2, when the copy has no album tag', () => {
    render(CopyCard, {
      props: {
        heading: 'Copy 1',
        copy: copy({ evidence: { ...copy().evidence!, observed: { durationMs: 281_000 } } }),
        wantedMs: 281_000,
        selected: false,
        onselect: () => {}
      }
    });

    const none = screen.getByText('none');
    expect(none).toBeTruthy();
  });

  it('marks an album difference with a cross', () => {
    render(CopyCard, {
      props: {
        heading: 'Copy 1',
        copy: copy({ evidence: { ...copy().evidence!, agrees: [], differs: ['album'] } }),
        wantedMs: 281_000,
        selected: false,
        onselect: () => {}
      }
    });

    expect(screen.getByText('Nine Lanterns')).toBeTruthy();
    expect(screen.getByLabelText('differs')).toBeTruthy();
  });

  it('carries a credit row in fail colour only where the filed credit differs from the want', () => {
    render(CopyCard, {
      props: {
        heading: 'In your library',
        copy: copy(),
        wantedMs: 281_000,
        credit: {
          fileArtist: 'Kestrel Grove feat. A Second Voice',
          fileTitle: 'Evensong',
          wantedArtist: 'Kestrel Grove',
          wantedTitle: 'Evensong'
        },
        selected: true,
        onselect: () => {}
      }
    });

    expect(screen.queryByText('Title')).toBeNull();
    const credit = screen.getByText('Kestrel Grove feat. A Second Voice');
    expect(credit.getAttribute('aria-invalid')).toBe('true');
  });
});

describe('the card as a radio', () => {
  it('keeps the heading as the accessible name once the label is a small chip', () => {
    render(CopyCard, {
      props: {
        heading: 'Copy 1',
        copy: copy(),
        wantedMs: 281_000,
        selected: false,
        onselect: () => {}
      }
    });

    expect(screen.getByRole('radio', { name: 'Copy 1' })).toBeTruthy();
  });

  it('selects on Enter, the same as Space', async () => {
    const onselect = vi.fn();
    render(CopyCard, {
      props: { heading: 'Copy 1', copy: copy(), wantedMs: 281_000, selected: false, onselect }
    });

    await fireEvent.keyDown(screen.getByRole('radio', { name: 'Copy 1' }), { key: 'Enter' });

    expect(onselect).toHaveBeenCalledOnce();
  });

  it('is the only focus stop when unselected, and a focus stop when selected', () => {
    const { rerender } = render(CopyCard, {
      props: { heading: 'Copy 1', copy: copy(), wantedMs: 281_000, selected: false, onselect: () => {} }
    });

    expect(screen.getByRole('radio', { name: 'Copy 1' }).getAttribute('tabindex')).toBe('-1');

    rerender({ heading: 'Copy 1', copy: copy(), wantedMs: 281_000, selected: true, onselect: () => {} });

    expect(screen.getByRole('radio', { name: 'Copy 1' }).getAttribute('tabindex')).toBe('0');
  });

  it('keeps the Play control outside the radio, as a sibling, so it never selects', () => {
    render(CopyCard, {
      props: { heading: 'Copy 1', copy: copy(), wantedMs: 281_000, selected: false, onselect: () => {} }
    });

    const radio = screen.getByRole('radio', { name: 'Copy 1' });
    const play = screen.getByRole('button', { name: 'Play Copy 1' });
    expect(radio.contains(play)).toBe(false);
  });
});

describe('the player', () => {
  function draw(overrides: Partial<AcquiredCopy> = {}) {
    const { container } = render(CopyCard, {
      props: {
        heading: 'Copy 1',
        copy: copy(overrides),
        wantedMs: 281_000,
        selected: false,
        onselect: () => {}
      }
    });
    return { container, audio: () => container.querySelector('audio') as HTMLAudioElement };
  }

  it('plays from the beginning rather than from the middle of the file', async () => {
    const { audio } = draw();

    await fireEvent.click(screen.getByRole('button', { name: 'Play Copy 1' }));
    await arrives(audio(), 200);

    expect(audio().currentTime).toBe(0);
  });

  it('turns into a pause control once it is sounding', async () => {
    const { audio } = draw();

    await fireEvent.click(screen.getByRole('button', { name: 'Play Copy 1' }));
    await fireEvent.play(audio());

    expect(screen.getByRole('button', { name: 'Pause Copy 1' })).toBeTruthy();
  });

  it('seeks to the fraction of the waveform that was clicked', async () => {
    const { audio } = draw();

    await fireEvent.click(screen.getByRole('button', { name: 'Play Copy 1' }));
    await arrives(audio(), 200);
    const slider = screen.getByRole('slider', { name: 'Seek' });
    widened(slider, 200);

    await fireEvent.click(slider, { clientX: 100 });

    expect(audio().currentTime).toBe(100);
  });

  it('applies the shared volume to its own audio element', async () => {
    listening.volume = 0.3;
    const { audio } = draw();

    await fireEvent.click(screen.getByRole('button', { name: 'Play Copy 1' }));

    expect(audio().volume).toBeCloseTo(0.3);
  });

  it('draws a flat line once the waveform 404s', async () => {
    const { container } = draw();

    await vi.waitFor(() => {
      const svg = container.querySelector('[role="slider"] svg');
      expect(svg?.querySelectorAll('rect').length).toBe(2);
    });
  });
});

describe('two cards, one player', () => {
  it('stops the first once the second starts', async () => {
    const first = render(CopyCard, {
      props: { heading: 'Copy 1', copy: copy({ id: 'copy-1' }), wantedMs: 281_000, selected: false, onselect: () => {} }
    });
    const second = render(CopyCard, {
      props: { heading: 'Copy 2', copy: copy({ id: 'copy-2' }), wantedMs: 281_000, selected: false, onselect: () => {} }
    });

    await fireEvent.click(within(first.container).getByRole('button', { name: 'Play Copy 1' }));
    await fireEvent.play(first.container.querySelector('audio')!);
    expect(within(first.container).getByRole('button', { name: 'Pause Copy 1' })).toBeTruthy();

    await fireEvent.click(within(second.container).getByRole('button', { name: 'Play Copy 2' }));

    expect(within(first.container).getByRole('button', { name: 'Play Copy 1' })).toBeTruthy();
  });

  it('pauses and clears a card when it is unmounted', async () => {
    const card = render(CopyCard, {
      props: { heading: 'Copy 1', copy: copy(), wantedMs: 281_000, selected: false, onselect: () => {} }
    });
    const audio = card.container.querySelector('audio')!;

    await fireEvent.click(screen.getByRole('button', { name: 'Play Copy 1' }));
    await fireEvent.play(audio);
    expect(listening.sounding).toBe(audio.getAttribute('src'));

    card.unmount();

    expect(HTMLMediaElement.prototype.pause).toHaveBeenCalled();
    expect(listening.sounding).toBe('');
  });
});
