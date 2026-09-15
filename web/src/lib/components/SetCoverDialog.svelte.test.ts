import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { tick } from 'svelte';
import { QueryClient } from '@tanstack/svelte-query';
import { api } from '$lib/api';
import SetCoverDialog from '$lib/components/SetCoverDialog.svelte';

// The way a person puts a sleeve on a record Schall could not picture. Some
// records have no cover anywhere — the archives answer by identifier about the
// release they were asked about — so this is the end of that, and what it holds
// to is what it sends: the file they chose, or the address they typed, and
// never both.

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
  document.body.style.overflow = '';
  document.body.style.paddingRight = '';
  vi.restoreAllMocks();
});

const releaseID = 'a1b2c3d4-0000-4000-8000-000000000001';

function mount(props: Partial<{ open: boolean; settable: boolean }> = {}) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } }
  });
  return render(SetCoverDialog, {
    props: { open: true, releaseID, ...props },
    context: new Map<string, unknown>([['$$_queryClient', client]])
  });
}

function address() {
  return document.querySelector('input#cover-address') as HTMLInputElement;
}

function save() {
  return screen.getByRole('button', { name: 'Save' });
}

describe('SetCoverDialog, what it says', () => {
  it('names the task and the two ways into it', () => {
    mount();

    const panel = screen.getByRole('dialog');
    const title = document.getElementById(panel.getAttribute('aria-labelledby') ?? '');
    expect(title?.textContent).toBe('Set cover');
    expect(screen.getByRole('button', { name: /Choose image/ })).toBeTruthy();
    expect(screen.getByText('or drop one here')).toBeTruthy();
    expect(screen.getByLabelText('Image address')).toBeTruthy();
  });

  // Saving nothing is not an outcome, so the control says so before it is
  // pressed rather than after.
  it('refuses to save until a file or an address is given', async () => {
    mount();

    expect((save() as HTMLButtonElement).disabled).toBe(true);

    await fireEvent.input(address(), { target: { value: 'https://example.com/sleeve.png' } });
    expect((save() as HTMLButtonElement).disabled).toBe(false);
  });

  // A cover an archive gave is a cached answer and stays. Only a picture the
  // person set is theirs to take back, so only then is the control there.
  it('offers to take back a cover only when the person set one', () => {
    const withArchiveCover = mount({ settable: false });
    expect(screen.queryByRole('button', { name: 'Remove cover' })).toBeNull();
    withArchiveCover.unmount();

    mount({ settable: true });
    expect(screen.getByRole('button', { name: 'Remove cover' })).toBeTruthy();
  });
});

describe('SetCoverDialog, what it sends', () => {
  it('sends the address that was typed', async () => {
    const set = vi
      .spyOn(api, 'setReleaseCoverUrl')
      .mockResolvedValue({ source: 'user', contentType: 'image/png' });

    mount();
    await fireEvent.input(address(), { target: { value: '  https://example.com/sleeve.png  ' } });
    await fireEvent.click(save());

    await waitFor(() => expect(set).toHaveBeenCalledTimes(1));
    // Trimmed, because a pasted address arrives with whatever came with it.
    expect(set).toHaveBeenCalledWith(releaseID, 'https://example.com/sleeve.png');
  });

  it('sends the file that was chosen', async () => {
    const set = vi
      .spyOn(api, 'setReleaseCoverFile')
      .mockResolvedValue({ source: 'user', contentType: 'image/png' });

    mount();
    const file = new File(['a picture'], 'sleeve.png', { type: 'image/png' });
    const picker = document.querySelector('input#cover-file') as HTMLInputElement;
    await fireEvent.change(picker, { target: { files: [file] } });
    await tick();

    // The name of the file is the confirmation: nothing else on screen says
    // which picture is about to be kept.
    expect(screen.getByRole('button', { name: /sleeve\.png/ })).toBeTruthy();

    await fireEvent.click(save());
    await waitFor(() => expect(set).toHaveBeenCalledTimes(1));
    expect(set).toHaveBeenCalledWith(releaseID, file);
  });

  // A file and an address are two answers to one question. Choosing a file
  // after typing an address sends the file, and the address is put down.
  it('sends the file when a file was chosen after an address', async () => {
    const byFile = vi
      .spyOn(api, 'setReleaseCoverFile')
      .mockResolvedValue({ source: 'user', contentType: 'image/png' });
    const byAddress = vi.spyOn(api, 'setReleaseCoverUrl');

    mount();
    await fireEvent.input(address(), { target: { value: 'https://example.com/sleeve.png' } });
    const file = new File(['a picture'], 'sleeve.png', { type: 'image/png' });
    const picker = document.querySelector('input#cover-file') as HTMLInputElement;
    await fireEvent.change(picker, { target: { files: [file] } });
    await fireEvent.click(save());

    await waitFor(() => expect(byFile).toHaveBeenCalledTimes(1));
    expect(byAddress).not.toHaveBeenCalled();
    expect(address().value).toBe('');
  });

  it('asks for the cover to be taken back', async () => {
    const remove = vi.spyOn(api, 'removeReleaseCover').mockResolvedValue(undefined);

    mount({ settable: true });
    await fireEvent.click(screen.getByRole('button', { name: 'Remove cover' }));

    await waitFor(() => expect(remove).toHaveBeenCalledWith(releaseID));
  });

  // The refusal stays in the panel and the panel stays open: the address was
  // wrong, and the reader is the one who can fix it.
  it('keeps the panel open and says what happened when the address is refused', async () => {
    vi.spyOn(api, 'setReleaseCoverUrl').mockRejectedValue(
      new Error('Nothing at that address is an image Schall can read.')
    );

    mount();
    await fireEvent.input(address(), { target: { value: 'https://example.com/page' } });
    await fireEvent.click(save());

    await screen.findByText(/Nothing at that address is an image/);
    expect(screen.getByRole('dialog')).toBeTruthy();
  });
});
