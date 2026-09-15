import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, setup, waitFor } from '@testing-library/svelte';
import { QueryClient, QueryClientProvider } from '@tanstack/svelte-query';
import type { LabelList, LabelListItem } from '$lib/api';

// The labels list page: what a followed label offers to do, and what it sends
// when a reader does it. Named without the leading `+`, like the other
// colocated route tests.

vi.mock('$app/navigation', () => ({
  afterNavigate: () => {},
  goto: vi.fn(),
  replaceState: vi.fn()
}));

const { default: LabelsPage } = await import('./+page.svelte');

function label(overrides: Partial<LabelListItem> = {}): LabelListItem {
  return {
    id: 'l1',
    musicbrainzId: '46f0f4cd-8aab-4b33-b698-f459faf64190',
    name: 'Warp Records',
    type: 'Original Production',
    country: 'GB',
    monitorLevel: 'main',
    followed: true,
    followedAt: '2026-01-01T00:00:00Z',
    lastRefreshedAt: '2026-08-01T00:00:00Z',
    refreshStatus: 'completed',
    releaseCount: 10,
    ownedReleaseCount: 4,
    trackCount: 80,
    ownedTrackCount: 30,
    ...overrides
  };
}

function answering(list: LabelList) {
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), 'http://localhost');
      const body = (value: unknown, status = 200) =>
        new Response(JSON.stringify(value), { status });
      if (url.pathname === '/api/v1/labels' && (!init || init.method === undefined)) {
        return body(list);
      }
      if (url.pathname.endsWith('/follow') && init?.method === 'DELETE') {
        return body({ ...list.items[0], followed: false, followedAt: null });
      }
      return body({});
    })
  );
}

function opened() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(LabelsPage, undefined, { wrapper: QueryClientProvider, wrapperProps: { client } });
}

beforeAll(() => {
  if (!Element.prototype.animate) {
    Element.prototype.animate = function () {
      const animation = {
        onfinish: null as (() => void) | null,
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

beforeAll(setup);

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe('the labels list', () => {
  it('fills its pending shell with a fixed screen-filling count', () => {
    vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>(() => {})));
    opened();

    const status = screen.getByRole('status', { name: 'Loading labels' });
    expect(status.querySelectorAll('li[aria-hidden="true"]')).toHaveLength(60);
  });

  it('names the room in a hidden level-1 heading', () => {
    vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>(() => {})));
    opened();

    expect(screen.getByRole('heading', { level: 1, name: 'Labels' })).toBeTruthy();
  });

  it('names each followed label and where it is from', async () => {
    answering({ items: [label()], total: 1, followedCount: 1 });
    opened();

    expect(await screen.findByText('Warp Records')).toBeTruthy();
    expect(screen.getByText('Original Production · GB')).toBeTruthy();
  });

  it('offers a link to follow a label when none is followed', async () => {
    answering({ items: [], total: 0, followedCount: 0 });
    opened();

    expect(await screen.findByText('No labels followed')).toBeTruthy();
    expect(screen.getAllByRole('button', { name: 'Follow label' })).toHaveLength(1);
  });

  it('sends the label named in the URL when unfollowing', async () => {
    answering({ items: [label()], total: 1, followedCount: 1 });
    opened();

    await screen.findByText('Warp Records');
    await fireEvent.click(screen.getByRole('button', { name: /Unfollow/ }));
    await fireEvent.click(screen.getByRole('button', { name: 'Unfollow' }));

    await waitFor(() =>
      expect(fetch).toHaveBeenCalledWith(
        '/api/v1/labels/l1/follow',
        expect.objectContaining({ method: 'DELETE' })
      )
    );
  });
});
