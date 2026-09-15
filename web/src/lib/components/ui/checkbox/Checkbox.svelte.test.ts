import { afterEach, describe, expect, it } from 'vitest';
import { cleanup, fireEvent, render, screen } from '@testing-library/svelte';
import { Checkbox } from '$lib/components/ui/checkbox';

// The box is drawn by Bits UI as a button that says what it is with
// `data-state`, not as a native input, so the two things worth holding are that
// it still answers a keyboard the way a checkbox does and that the mark inside
// it follows the state. Colour alone is never the state.

afterEach(cleanup);

describe('ui/Checkbox', () => {
  it('is announced as a checkbox', () => {
    render(Checkbox, { 'aria-label': 'Only matched files' });

    expect(screen.getByRole('checkbox', { name: 'Only matched files' })).toBeTruthy();
  });

  it('turns on and off again with the space bar', async () => {
    render(Checkbox, { 'aria-label': 'Only matched files' });
    const box = screen.getByRole('checkbox', { name: 'Only matched files' });

    expect(box.getAttribute('data-state')).toBe('unchecked');

    await fireEvent.keyDown(box, { key: ' ' });
    await fireEvent.keyUp(box, { key: ' ' });
    expect(box.getAttribute('data-state')).toBe('checked');

    await fireEvent.click(box);
    expect(box.getAttribute('data-state')).toBe('unchecked');
  });

  it('carries a mark when it is on, so the colour is never the only answer', async () => {
    const { container } = render(Checkbox, { 'aria-label': 'Keep' });

    expect(container.querySelector('[aria-hidden="true"]')).toBeNull();

    await fireEvent.click(screen.getByRole('checkbox', { name: 'Keep' }));

    expect(container.querySelector('[aria-hidden="true"]')).toBeTruthy();
  });

  it('refuses a press when it is disabled', async () => {
    render(Checkbox, { 'aria-label': 'Keep', disabled: true });
    const box = screen.getByRole('checkbox', { name: 'Keep' });

    await fireEvent.click(box);

    expect(box.getAttribute('data-state')).toBe('unchecked');
  });
});
