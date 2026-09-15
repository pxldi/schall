import { afterEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen } from '@testing-library/svelte';
import Pager from '$lib/components/Pager.svelte';

// Four pages paginate through this one component, and what it sends is an
// offset rather than a page number — arithmetic nobody reading the call site
// sees. The clamp at the first page and the silence on a short list are the two
// things that would break without anything on screen looking wrong.

afterEach(cleanup);

describe('Pager', () => {
  it('draws nothing for a list that fits on one page', () => {
    render(Pager, { total: 12, offset: 0, pageSize: 25, onchange: vi.fn() });

    expect(screen.queryByRole('button', { name: 'Next' })).toBeNull();
  });

  it('says which rows of the list are on screen', () => {
    render(Pager, { total: 90, offset: 25, pageSize: 25, onchange: vi.fn() });

    expect(screen.getByText('26–50 of 90')).toBeTruthy();
  });

  it('counts the last page up to the total rather than past it', () => {
    render(Pager, { total: 90, offset: 75, pageSize: 25, onchange: vi.fn() });

    expect(screen.getByText('76–90 of 90')).toBeTruthy();
  });

  it('sends the next page when Next is pressed', async () => {
    const onchange = vi.fn();
    render(Pager, { total: 90, offset: 25, pageSize: 25, onchange });

    await fireEvent.click(screen.getByRole('button', { name: 'Next' }));

    expect(onchange).toHaveBeenCalledWith(50);
  });

  it('sends the previous page when Previous is pressed', async () => {
    const onchange = vi.fn();
    render(Pager, { total: 90, offset: 50, pageSize: 25, onchange });

    await fireEvent.click(screen.getByRole('button', { name: 'Previous' }));

    expect(onchange).toHaveBeenCalledWith(25);
  });

  it('sends the first page when going back from a partial one', async () => {
    const onchange = vi.fn();
    render(Pager, { total: 90, offset: 10, pageSize: 25, onchange });

    await fireEvent.click(screen.getByRole('button', { name: 'Previous' }));

    expect(onchange).toHaveBeenCalledWith(0);
  });

  // Refusing the press is the browser's to enforce, so what is asserted is the
  // state it reads: a synthetic click reaches a listener on a disabled button
  // where a person's press would never have got that far.
  it('offers no way back from the first page', () => {
    render(Pager, { total: 90, offset: 0, pageSize: 25, onchange: vi.fn() });

    const previous = screen.getByRole('button', { name: 'Previous' }) as HTMLButtonElement;

    expect(previous.disabled).toBe(true);
  });

  it('offers no way on from the last page', () => {
    render(Pager, { total: 90, offset: 75, pageSize: 25, onchange: vi.fn() });

    const next = screen.getByRole('button', { name: 'Next' }) as HTMLButtonElement;

    expect(next.disabled).toBe(true);
  });

  it('offers a way on from a page that is not the last', () => {
    render(Pager, { total: 90, offset: 0, pageSize: 25, onchange: vi.fn() });

    const next = screen.getByRole('button', { name: 'Next' }) as HTMLButtonElement;

    expect(next.disabled).toBe(false);
  });
});

describe('the two ends of a long list', () => {
  // 3,538 files at 25 a page is 142 pages. Previous and Next alone meant a
  // reader on page 90 had no way to know where they were and no way back except
  // ninety presses.
  it('says which page of how many, not only which rows', () => {
    render(Pager, { props: { total: 3538, offset: 2225, pageSize: 25, onchange: () => {} } });

    expect(screen.getByText('90')).toBeTruthy();
    expect(screen.getByText('142')).toBeTruthy();
  });

  it('goes to the last page in one press', async () => {
    const onchange = vi.fn();
    render(Pager, { props: { total: 3538, offset: 0, pageSize: 25, onchange } });

    await fireEvent.click(screen.getByRole('button', { name: 'Last page' }));

    expect(onchange).toHaveBeenCalledWith(3525);
  });

  it('goes back to the first in one press', async () => {
    const onchange = vi.fn();
    render(Pager, { props: { total: 3538, offset: 2225, pageSize: 25, onchange } });

    await fireEvent.click(screen.getByRole('button', { name: 'First page' }));

    expect(onchange).toHaveBeenCalledWith(0);
  });

  // Both ends are already underfoot on the page they lead to.
  it('offers no way to where the reader already is', () => {
    render(Pager, { props: { total: 60, offset: 0, pageSize: 25, onchange: () => {} } });

    expect(screen.getByRole('button', { name: 'First page' })).toHaveProperty('disabled', true);
    expect(screen.getByRole('button', { name: 'Last page' })).toHaveProperty('disabled', false);
  });
})
