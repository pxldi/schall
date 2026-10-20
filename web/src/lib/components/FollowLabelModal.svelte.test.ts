import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { tick } from 'svelte';
import { QueryClient } from '@tanstack/svelte-query';
import { api, type LabelSearchResult } from '$lib/api';
import FollowLabelModal from '$lib/components/FollowLabelModal.svelte';

// This is FollowArtistModal's shell reused for a different kind of entity —
// the focus trap, the scroll lock and the in-flight refusal are already tested
// there. What is new here is what gets sent: a label's MusicBrainz identifier
// and name, not an artist's.

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
  heading.textContent = 'Labels';
  document.body.append(heading);
});

afterEach(() => {
  cleanup();
  document.body.innerHTML = '';
  document.body.style.overflow = '';
  document.body.style.paddingRight = '';
  vi.restoreAllMocks();
});

function field() {
  return document.querySelector('input#label-name') as HTMLInputElement;
}

function mount() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } }
  });
  return render(FollowLabelModal, {
    props: { open: true },
    context: new Map<string, unknown>([['$$_queryClient', client]])
  });
}

async function pickCandidate() {
  const result: LabelSearchResult = {
    musicbrainzId: '46f0f4cd-8aab-4b33-b698-f459faf64190',
    name: 'Warp Records',
    type: 'Original Production',
    country: 'GB',
    score: 100
  };
  vi.spyOn(api, 'searchLabels').mockResolvedValue({ items: [result] });

  mount();
  await tick();
  await fireEvent.input(field(), { target: { value: 'warp' } });

  const candidate = await screen.findByRole('button', { name: /Warp Records/ }, { timeout: 2000 });
  await fireEvent.click(candidate);
}

describe('FollowLabelModal', () => {
  it('sends the chosen label’s MusicBrainz identifier and name', async () => {
    vi.spyOn(api, 'followLabel').mockResolvedValue({
      id: 'l1',
      musicbrainzId: '46f0f4cd-8aab-4b33-b698-f459faf64190',
      name: 'Warp Records',
      monitorLevel: 'main',
      followed: true,
      followedAt: '2026-08-27T00:00:00Z',
      lastRefreshedAt: null,
      refreshStatus: 'queued'
    });
    await pickCandidate();

    await fireEvent.click(screen.getByRole('button', { name: 'Follow' }));

    await waitFor(() =>
      expect(api.followLabel).toHaveBeenCalledWith(
        expect.objectContaining({
          musicbrainzId: '46f0f4cd-8aab-4b33-b698-f459faf64190',
          name: 'Warp Records'
        })
      )
    );
  });

  it('closes itself once the follow lands', async () => {
    vi.spyOn(api, 'followLabel').mockResolvedValue({
      id: 'l1',
      musicbrainzId: '46f0f4cd-8aab-4b33-b698-f459faf64190',
      name: 'Warp Records',
      monitorLevel: 'main',
      followed: true,
      followedAt: '2026-08-27T00:00:00Z',
      lastRefreshedAt: null,
      refreshStatus: 'queued'
    });
    await pickCandidate();

    await fireEvent.click(screen.getByRole('button', { name: 'Follow' }));

    await waitFor(() => expect(document.body.style.overflow).toBe(''));
  });

  it('stays open with the failure shown, when the follow is refused', async () => {
    vi.spyOn(api, 'followLabel').mockRejectedValue(new Error('MusicBrainz did not answer'));
    await pickCandidate();

    await fireEvent.click(screen.getByRole('button', { name: 'Follow' }));

    await waitFor(() => expect(screen.getByText('MusicBrainz did not answer')).toBeTruthy());
    expect(document.querySelector('[role="dialog"]')).not.toBeNull();
  });
});
