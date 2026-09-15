import { afterEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen } from '@testing-library/svelte';

import Segmented from '$lib/components/Segmented.svelte';

// The filter strip above a list. Each choice can carry a figure saying how many
// things it would show, and those figures arrive from the server after the strip
// is already on screen. What the strip does in that gap is the whole of this
// file: a figure it does not have is not a figure of nought.

afterEach(cleanup);

function strip(options: { value: string; name: string; count?: number }[], pending = false) {
  return render(Segmented, {
    props: { options, value: '', onchange: () => {}, label: 'How complete an artist is', pending }
  });
}

describe('a strip whose figures have not arrived', () => {
  it('writes no figure at all rather than a nought', () => {
    strip(
      [
        { value: '', name: 'All' },
        { value: 'incomplete', name: 'Incomplete' }
      ],
      true
    );

    // The complaint this exists for: the strip read `All 0  Incomplete 0` for as
    // long as the request took, then jumped to `All 40  Incomplete 33`. Nobody
    // can tell that first reading from a collection that is genuinely empty.
    expect(screen.queryByText('0')).toBeNull();
  });

  it('holds the room the figure will take, so the strip does not jump', () => {
    const { container } = strip([{ value: '', name: 'All' }], true);

    expect(container.querySelectorAll('.animate-pulse')).toHaveLength(1);
  });

  it('says nothing to a screen reader about a figure that does not exist yet', () => {
    const { container } = strip([{ value: '', name: 'All' }], true);

    expect(container.querySelector('.animate-pulse')?.getAttribute('aria-hidden')).toBe('true');
  });
});

describe('a strip that never carries figures', () => {
  // Library filters by the shape of a thing rather than by how much of it there
  // is, so its three choices pass no count and are not waiting for one.
  it('draws no placeholder, because nothing is on its way', () => {
    const { container } = strip([
      { value: 'releases', name: 'Releases' },
      { value: 'files', name: 'Files' }
    ]);

    expect(container.querySelectorAll('.animate-pulse')).toHaveLength(0);
  });
});

describe('a strip whose figures have arrived', () => {
  it('writes each figure beside the choice it counts', () => {
    strip([
      { value: '', name: 'All', count: 40 },
      { value: 'attention', name: 'Needs attention', count: 2 }
    ]);

    expect(screen.getByText('40')).toBeTruthy();
    expect(screen.getByText('2')).toBeTruthy();
  });

  // A pile that is genuinely empty says so. The placeholder is only for a figure
  // nobody has yet, and a nought that arrived is an answer.
  it('writes a nought that the server actually sent', () => {
    strip([{ value: 'attention', name: 'Needs attention', count: 0 }], true);

    expect(screen.getByText('0')).toBeTruthy();
  });
});

// The three roles share one DOM shape: a group of buttons, the selected one
// marked with `aria-pressed`, a press sending its value. Only the look
// differs between `tabs`, `filter` and the default `chips`.
describe.each(['tabs', 'filter'] as const)('a strip drawn as %s', (variant) => {
  const options = [
    { value: '', name: 'All' },
    { value: 'followed', name: 'Followed' }
  ];

  it('marks the selected option with aria-pressed', () => {
    render(Segmented, {
      props: { options, value: 'followed', onchange: () => {}, label: 'Scope', variant }
    });

    expect(screen.getByRole('button', { name: 'Followed' }).getAttribute('aria-pressed')).toBe(
      'true'
    );
    expect(screen.getByRole('button', { name: 'All' }).getAttribute('aria-pressed')).toBe('false');
  });

  it('sends the value of the option pressed', async () => {
    const onchange = vi.fn();
    render(Segmented, {
      props: { options, value: '', onchange, label: 'Scope', variant }
    });

    await fireEvent.click(screen.getByRole('button', { name: 'Followed' }));

    expect(onchange).toHaveBeenCalledWith('followed');
  });
});
