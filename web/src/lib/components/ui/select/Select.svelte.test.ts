import { afterEach, beforeAll, describe, expect, it } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { letTheScrollbarComeBack, shimTheMissingBrowser } from '../test-window';
import Fixture from './SelectFixture.svelte';

// A picker has to be usable without a pointer, which is the whole of what is
// tested here: it opens on a key, the arrow keys move down the list, typing a
// letter jumps to the choice that begins with it, and a choice made with the
// keyboard is the one that ends up in the control.
//
// The groups are named as well. A heading tied to its group is announced once
// as the reader arrives at the group, rather than repeated on every row.

beforeAll(shimTheMissingBrowser);

afterEach(async () => {
  cleanup();
  await letTheScrollbarComeBack();
});

const trigger = () => screen.getByLabelText('Format');

// The arrow keys are answered by whichever choice the keyboard is standing on,
// which moves as the list is walked, so each press is aimed where the keyboard
// actually is.
const press = (key: string) =>
  fireEvent.keyDown(document.activeElement ?? document.body, { key });

const highlighted = () => document.querySelector('[data-highlighted]')?.textContent?.trim();

// Opened the way a keyboard opens it. A list that only answers a pointer is a
// list half the readers of this product cannot use.
async function open() {
  trigger().focus();
  await fireEvent.keyDown(trigger(), { key: 'Enter' });
  return screen.findByRole('listbox');
}

describe('ui/Select', () => {
  it('opens the list on Enter', async () => {
    render(Fixture);

    expect(await open()).toBeTruthy();
  });

  it('stands the keyboard on the first choice as it opens', async () => {
    render(Fixture);
    await open();

    await waitFor(() => expect(highlighted()).toBe('FLAC'));
  });

  it('names each group once, above the choices in it', async () => {
    render(Fixture);
    await open();

    expect(screen.getByText('Lossless')).toBeTruthy();
    expect(screen.getByText('Lossy')).toBeTruthy();
    expect(screen.getAllByRole('group')).toHaveLength(2);
  });

  it('moves down the list on the down arrow', async () => {
    render(Fixture);
    await open();
    await waitFor(() => expect(highlighted()).toBe('FLAC'));

    await press('ArrowDown');
    await waitFor(() => expect(highlighted()).toBe('ALAC'));

    await press('ArrowDown');
    await waitFor(() => expect(highlighted()).toBe('MP3'));
  });

  it('jumps to a choice by its first letter', async () => {
    render(Fixture);
    await open();
    await waitFor(() => expect(highlighted()).toBe('FLAC'));

    await press('o');

    await waitFor(() => expect(highlighted()).toBe('Opus'));
  });

  it('puts the choice made with the keyboard into the control', async () => {
    render(Fixture);
    await open();
    await waitFor(() => expect(highlighted()).toBe('FLAC'));

    await press('ArrowDown');
    await waitFor(() => expect(highlighted()).toBe('ALAC'));
    await press('Enter');

    await waitFor(() => expect(screen.queryByRole('listbox')).toBeNull());
    await waitFor(() => expect(trigger().textContent).toContain('ALAC'));
  });

  it('draws the list outside the box its trigger sits in', async () => {
    const { container } = render(Fixture);

    const list = await open();

    expect(container.contains(list)).toBe(false);
    expect(document.body.contains(list)).toBe(true);
  });
});
