import { afterEach, describe, expect, it } from 'vitest';
import { cleanup, render, screen } from '@testing-library/svelte';
import { createRawSnippet, tick } from 'svelte';
import CyclingPlaceholder, { ARTIST_NAMES } from '$lib/components/CyclingPlaceholder.svelte';

// A placeholder that moves is only allowed to move while the field is empty and
// has never been typed into. What is worth holding is that rule and the two
// things that make it a placeholder rather than an advertisement: it is not
// announced, and it never takes the pointer away from the control it sits
// behind.

afterEach(cleanup);

function field() {
  return createRawSnippet(() => ({
    render: () => `<input class="field w-full" aria-label="Artist name" />`
  }));
}

function ghost(container: HTMLElement) {
  return container.querySelector('.ph');
}

describe('CyclingPlaceholder', () => {
  it('shows the whole set, in the order the card fixes', () => {
    const { container } = render(CyclingPlaceholder, { children: field() });
    const names = [...ghost(container)!.querySelectorAll('span')].map((span) => span.textContent);

    expect(names).toEqual(ARTIST_NAMES);
    expect(ARTIST_NAMES.length).toBe(15);
  });

  it('is decorative: not announced, and not in the way of the field', () => {
    const { container } = render(CyclingPlaceholder, { children: field() });

    expect(ghost(container)!.getAttribute('aria-hidden')).toBe('true');
    // The accessible name is the field's own label, which does not change.
    expect(screen.getByLabelText('Artist name')).toBeTruthy();
  });

  // One beat of the reading step a name, so the cycle is the count times that:
  // the delays and the keyframes are both written against the total. The step
  // is named rather than measured here, because the stylesheet owns the figure
  // and this component only counts names.
  it('runs one cycle over the set it was given', () => {
    const { container } = render(CyclingPlaceholder, { children: field() });

    expect(ghost(container)!.getAttribute('style')).toContain(
      '--ph-cycle: calc(var(--motion-read) * 15)'
    );
    expect(
      [...ghost(container)!.querySelectorAll('span')].map((span) => span.getAttribute('style'))
    ).toEqual(ARTIST_NAMES.map((_, index) => `--ph-index: ${index};`));
  });

  it('takes another set of names, and the cycle follows it', () => {
    const { container } = render(CyclingPlaceholder, {
      children: field(),
      names: ['Talk Talk', 'Broadcast', 'Stereolab']
    });

    expect([...ghost(container)!.querySelectorAll('span')].map((s) => s.textContent)).toEqual([
      'Talk Talk',
      'Broadcast',
      'Stereolab'
    ]);
    expect(ghost(container)!.getAttribute('style')).toContain(
      '--ph-cycle: calc(var(--motion-read) * 3)'
    );
  });

  it('is gone the moment the field holds anything', async () => {
    const { container } = render(CyclingPlaceholder, { children: field(), value: 'a' });
    await tick();

    expect(ghost(container)).toBeNull();
    expect(container.querySelector('.ph-wrap')!.getAttribute('data-typed')).toBe('true');
  });

  // The state is "has this field ever been typed into", not "is it empty". A
  // user who typed and then cleared is deciding what to search for, and that is
  // the worst possible moment to start something moving in the box.
  it('stays gone once the field is cleared again', async () => {
    const { container, rerender } = render(CyclingPlaceholder, { children: field(), value: '' });
    await rerender({ value: 'a' });
    await tick();
    await rerender({ value: '' });
    await tick();

    expect(ghost(container)).toBeNull();
  });

  // Both layers take the trailing variant's room from the component, so the
  // ghost stops short of the state glyph exactly where the value does.
  it('reserves the room for the trailing glyph on the ghost too', () => {
    const plain = render(CyclingPlaceholder, { children: field() });
    expect(ghost(plain.container)!.getAttribute('data-trailing')).toBe('false');
    cleanup();

    const trailing = render(CyclingPlaceholder, { children: field(), trailing: true });
    expect(ghost(trailing.container)!.getAttribute('data-trailing')).toBe('true');
  });
});
