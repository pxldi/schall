import { afterEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen } from '@testing-library/svelte';
import PillSelect from './PillSelect.svelte';

// The pill-shaped picker beside a board's view chips. Worth testing: every
// option renders, the current one is the one shown as selected, and choosing
// another sends its value.

afterEach(cleanup);

const options = [
  { value: 'library', name: 'Library' },
  { value: 'everything', name: 'Everything' }
];

describe('PillSelect', () => {
  it('renders every option', () => {
    render(PillSelect, { options, value: 'library', onchange: () => {}, label: 'Scope' });

    expect(screen.getByRole('option', { name: 'Library' })).toBeTruthy();
    expect(screen.getByRole('option', { name: 'Everything' })).toBeTruthy();
  });

  it('shows the current value as selected', () => {
    render(PillSelect, { options, value: 'everything', onchange: () => {}, label: 'Scope' });

    const select = screen.getByLabelText('Scope') as HTMLSelectElement;
    expect(select.value).toBe('everything');
  });

  it('sends the chosen value', async () => {
    const onchange = vi.fn();
    render(PillSelect, { options, value: 'library', onchange, label: 'Scope' });

    const select = screen.getByLabelText('Scope') as HTMLSelectElement;
    await fireEvent.change(select, { target: { value: 'everything' } });

    expect(onchange).toHaveBeenCalledWith('everything');
  });
});

describe('a quiet select', () => {
  it('shows its prefix word', () => {
    render(PillSelect, {
      options,
      value: 'library',
      onchange: () => {},
      label: 'Sort by',
      quiet: true,
      prefix: 'Sort'
    });

    expect(screen.getByText('Sort')).toBeTruthy();
  });

  it('keeps the full label for a screen reader', () => {
    render(PillSelect, {
      options,
      value: 'library',
      onchange: () => {},
      label: 'Sort by',
      quiet: true,
      prefix: 'Sort'
    });

    expect(screen.getByLabelText('Sort by')).toBeTruthy();
  });
});
