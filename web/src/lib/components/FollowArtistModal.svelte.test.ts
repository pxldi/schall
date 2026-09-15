import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { tick } from 'svelte';
import { QueryClient } from '@tanstack/svelte-query';
import { api, type ArtistSearchResult } from '$lib/api';
import FollowArtistModal from '$lib/components/FollowArtistModal.svelte';

// The only overlay Schall has. Its material is in the diff and its behaviour is
// here: what the design card called "specified, not verified" — initial focus,
// the trap, returning focus, the scroll lock and the refusal while a follow is
// in flight — is exactly what this file holds it to.

// jsdom implements no Web Animations API, and Svelte drives transitions through
// it. Without this the fade out never finishes and nothing is ever removed.
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

let opener: HTMLButtonElement;
let heading: HTMLHeadingElement;

beforeEach(() => {
  heading = document.createElement('h1');
  heading.textContent = 'Artists';
  document.body.append(heading);

  // The control the panel was opened from, focused as a real one would be.
  opener = document.createElement('button');
  opener.textContent = 'Follow artist';
  document.body.append(opener);
  opener.focus();
});

afterEach(() => {
  cleanup();
  opener.remove();
  heading.remove();
  document.body.style.overflow = '';
  document.body.style.paddingRight = '';
  vi.restoreAllMocks();
});

function panel() {
  return document.querySelector('[role="dialog"]');
}

function field() {
  return document.querySelector('input#artist-name') as HTMLInputElement;
}

function scrim() {
  return document.querySelector('[role="presentation"]') as HTMLElement;
}

function mount(props: { open: boolean } = { open: true }) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } }
  });
  return render(FollowArtistModal, {
    props,
    context: new Map<string, unknown>([['$$_queryClient', client]])
  });
}

// Opens the panel, searches, and picks the one candidate MusicBrainz answers
// with, which is the only way a follow becomes possible.
async function pickCandidate() {
  const result: ArtistSearchResult = {
    musicbrainzId: 'b7ffd2af-418f-4be2-bdd1-22f8b48613da',
    name: 'Talk Talk',
    sortName: 'Talk Talk',
    score: 100
  };
  vi.spyOn(api, 'searchArtists').mockResolvedValue({ items: [result] });

  const rendered = mount();
  await tick();
  await fireEvent.input(field(), { target: { value: 'talk talk' } });

  const candidate = await screen.findByRole('button', { name: /Talk Talk/ }, { timeout: 2000 });
  await fireEvent.click(candidate);
  return rendered;
}

describe('FollowArtistModal, the field', () => {
  it('carries no glyph in front of the value', () => {
    mount();
    const wrap = document.querySelector('.ph-wrap') as HTMLElement;

    // Idle: nothing is being looked up and nothing has been chosen, so the field
    // holds no glyph at all. The trailing one is state and arrives with it.
    expect(wrap.querySelectorAll('svg').length).toBe(0);
    expect((document.querySelector('.ph-wrap') as HTMLElement).getAttribute('data-trailing')).toBe(
      'true'
    );
  });

  // The room for the trailing glyph belongs to the component: `pr-8` at the call
  // site was beaten by the unlayered .field and reserved nothing.
  it('leaves the inset of the field to the component', () => {
    mount();

    expect(field().getAttribute('data-inset')).toBe('component');
  });

  it('offers names rather than an instruction', () => {
    mount();

    expect(field().hasAttribute('placeholder')).toBe(false);
    expect(document.querySelector('.ph')?.textContent).toContain('Amadeus Mozart');
  });

  // The Search button is already refused until the field holds two characters,
  // so a line under the field saying so told the reader what the control was
  // telling them, one line lower and in smaller type.
  it('does not restate the limit the controls already carry', () => {
    mount();

    expect(screen.queryByText('Two characters or more')).toBeNull();
  });
});

describe('FollowArtistModal, the layer', () => {
  // The scrim is the elevation and the border is the edge.
  it('darkens the page rather than casting a shadow', () => {
    mount();

    expect(scrim().getAttribute('data-scrim')).toBe('dark-blurred');
    expect(panel()!.getAttribute('data-shadow')).toBe('none');
  });

  // The header and the footer are pinned so the way out is reachable at any
  // height, which only holds if there is exactly one scrolling region.
  it('scrolls in one place, the body', () => {
    mount();

    expect(panel()!.querySelectorAll('.overflow-y-auto').length).toBe(1);
  });

  // It never grows past the viewport with the page scrollbar as its rescue: the
  // page behind is locked, so that scrollbar is not there to use.
  it('takes the height it is left and no more', () => {
    mount();

    expect(panel()!.getAttribute('data-height')).toBe('constrained');
  });

  it('locks the page behind it, and gives it back', async () => {
    const { rerender } = mount();
    await tick();

    expect(document.body.style.overflow).toBe('hidden');

    await rerender({ open: false });
    await tick();

    expect(document.body.style.overflow).toBe('');
  });
});

describe('FollowArtistModal, focus', () => {
  // The task starts with typing.
  it('opens with the field focused', async () => {
    mount();
    await tick();

    expect(document.activeElement).toBe(field());
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

  it('gives focus back to the control that opened it', async () => {
    const { rerender } = mount();
    await tick();

    await rerender({ open: false });
    await tick();

    expect(document.activeElement).toBe(opener);
  });

  // The row it stood on is followed and re-rendered, so the control is gone.
  it('gives focus to the page header when the opener has gone', async () => {
    const { rerender } = mount();
    await tick();
    opener.remove();

    await rerender({ open: false });
    await tick();

    expect(document.activeElement).toBe(heading);
    expect(heading.getAttribute('tabindex')).toBe('-1');
  });
});

describe('FollowArtistModal, leaving', () => {
  it('closes on Escape', async () => {
    mount();
    await tick();

    await fireEvent.keyDown(window, { key: 'Escape' });
    await tick();

    expect(document.body.style.overflow).toBe('');
  });

  it('closes when the scrim is pressed', async () => {
    mount();
    await tick();

    await fireEvent.mouseDown(scrim());
    await tick();

    expect(document.body.style.overflow).toBe('');
  });

  // A press that starts inside the panel and ends on the scrim was a selection
  // being dragged, not a dismissal: it follows mousedown, not mouseup.
  it('stays open when the press started inside the panel', async () => {
    mount();
    await tick();

    await fireEvent.mouseDown(panel()!);
    await tick();

    expect(document.body.style.overflow).toBe('hidden');
  });
});

describe('FollowArtistModal, in flight', () => {
  it('refuses every way out at once, and says so with the controls', async () => {
    vi.spyOn(api, 'followArtist').mockReturnValue(new Promise(() => {}));
    await pickCandidate();

    await fireEvent.click(screen.getByRole('button', { name: 'Follow' }));

    const primary = (await screen.findByRole('button', {
      name: /Following/
    })) as HTMLButtonElement;
    const close = screen.getByRole('button', { name: 'Close' }) as HTMLButtonElement;
    const cancel = screen.getByRole('button', { name: 'Cancel' }) as HTMLButtonElement;

    // Every way out is dimmed together, which is what makes a dead Escape key
    // read as a refusal rather than a bug.
    expect(close.disabled).toBe(true);
    expect(cancel.disabled).toBe(true);
    expect(primary.disabled).toBe(true);

    await fireEvent.keyDown(window, { key: 'Escape' });
    await fireEvent.mouseDown(scrim());
    await tick();

    expect(panel()).not.toBeNull();
    expect(document.body.style.overflow).toBe('hidden');
  });

  // The refusal is about leaving, not about reading.
  it('leaves the field alone while it refuses to be left', async () => {
    vi.spyOn(api, 'followArtist').mockReturnValue(new Promise(() => {}));
    await pickCandidate();

    await fireEvent.click(screen.getByRole('button', { name: 'Follow' }));
    await screen.findByRole('button', { name: /Following/ });

    expect(field().disabled).toBe(false);
  });

  it('closes itself once the follow lands', async () => {
    vi.spyOn(api, 'followArtist').mockResolvedValue({
      id: 'a1',
      musicbrainzId: 'b7ffd2af-418f-4be2-bdd1-22f8b48613da',
      name: 'Talk Talk',
      sortName: 'Talk Talk',
      releaseCount: 0,
      createdAt: '2026-08-07T00:00:00Z'
    } as never);
    await pickCandidate();

    await fireEvent.click(screen.getByRole('button', { name: 'Follow' }));

    await waitFor(() => expect(document.body.style.overflow).toBe(''));
  });

  // There is no row and no form elsewhere to attach the failure to.
  it('stays open with the failure in the panel', async () => {
    vi.spyOn(api, 'followArtist').mockRejectedValue(new Error('MusicBrainz did not answer'));
    await pickCandidate();

    await fireEvent.click(screen.getByRole('button', { name: 'Follow' }));

    await waitFor(() => expect(screen.getByText('MusicBrainz did not answer')).toBeTruthy());
    expect(panel()).not.toBeNull();
    expect(document.body.style.overflow).toBe('hidden');
  });
});

// Following an artist keeps them complete from here. Their back catalogue is
// the other decision, and this is the one moment both are in front of the
// reader.
describe('FollowArtistModal, want what is missing', () => {
  const followed = {
    id: 'a1',
    musicbrainzId: 'b7ffd2af-418f-4be2-bdd1-22f8b48613da',
    name: 'Talk Talk',
    sortName: 'Talk Talk'
  };

  it('offers it unchecked', async () => {
    await pickCandidate();

    const box = screen.getByLabelText("Want what's missing") as HTMLInputElement;
    expect(box.checked).toBe(false);
  });

  it('asks for nothing beyond the follow when it is left alone', async () => {
    vi.spyOn(api, 'followArtist').mockResolvedValue(followed as never);
    const standing = vi.spyOn(api, 'setArtistWantMissing');
    await pickCandidate();

    await fireEvent.click(screen.getByRole('button', { name: 'Follow' }));

    await waitFor(() => expect(api.followArtist).toHaveBeenCalled());
    expect(standing).not.toHaveBeenCalled();
  });

  it('sets the standing want on the artist that was just followed', async () => {
    vi.spyOn(api, 'followArtist').mockResolvedValue(followed as never);
    const standing = vi
      .spyOn(api, 'setArtistWantMissing')
      .mockResolvedValue({} as never);
    await pickCandidate();

    await fireEvent.click(screen.getByLabelText("Want what's missing"));
    await fireEvent.click(screen.getByRole('button', { name: 'Follow' }));

    await waitFor(() => expect(standing).toHaveBeenCalledWith('a1', true));
  });

  // The follow landed. Saying so is the difference between a note the reader
  // can act on and one that reads as the follow having failed.
  it('says the artist is followed when only the standing want failed', async () => {
    vi.spyOn(api, 'followArtist').mockResolvedValue(followed as never);
    vi.spyOn(api, 'setArtistWantMissing').mockRejectedValue(new Error('wants are unavailable'));
    await pickCandidate();

    await fireEvent.click(screen.getByLabelText("Want what's missing"));
    await fireEvent.click(screen.getByRole('button', { name: 'Follow' }));

    await waitFor(() => expect(screen.getByText('wants are unavailable')).toBeTruthy());
    expect(
      screen.getByText('The artist is followed. Press Want missing on their page.')
    ).toBeTruthy();
  });
});

// Searching is the one thing in this panel that talks to MusicBrainz, and it is
// driven by a 300ms debounce: a person who types a name, changes their mind and
// types another has sent two searches well inside one answer's lifetime. What
// they must get back is the artists of the name in the box.
describe('FollowArtistModal, a second name typed while the first is still being looked up', () => {
  function artist(name: string, musicbrainzId: string): ArtistSearchResult {
    return { musicbrainzId, name, sortName: name, score: 100 };
  }

  it('answers with the artists of the name in the box', async () => {
    let answerTalkTalk: (found: { items: ArtistSearchResult[] }) => void = () => {};
    const talkTalk = new Promise<{ items: ArtistSearchResult[] }>((resolve) => {
      answerTalkTalk = resolve;
    });
    vi.spyOn(api, 'searchArtists').mockImplementation((query: string) =>
      query === 'talk talk'
        ? talkTalk
        : Promise.resolve({
            items: [artist('Portishead', 'e6bba15c-0cf8-4d24-b74f-b6b8d1a1ba4a')]
          })
    );

    mount();
    await tick();

    await fireEvent.input(field(), { target: { value: 'talk talk' } });
    await waitFor(() => expect(api.searchArtists).toHaveBeenCalledWith('talk talk'));

    // The first look-up is still out. Under one query key for every name, this
    // second one joined it and settled on Talk Talk.
    await fireEvent.input(field(), { target: { value: 'portishead' } });

    expect(
      await screen.findByRole('button', { name: /Portishead/ }, { timeout: 2000 })
    ).toBeTruthy();

    // And the answer to the abandoned name, arriving late, does not take the
    // list back.
    answerTalkTalk({ items: [artist('Talk Talk', 'b7ffd2af-418f-4be2-bdd1-22f8b48613da')] });
    await tick();

    expect(screen.queryByRole('button', { name: /Talk Talk/ })).toBeNull();
  });
});
