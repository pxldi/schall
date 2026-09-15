import { afterEach, describe, expect, it } from 'vitest';
import { cleanup, render, screen } from '@testing-library/svelte';
import Fixture from './TableFixture.svelte';

// Two decisions are worth holding. A sticky header has to be opaque, because
// every surface in this product is white laid over the ground and a see-through
// header shows the rows sliding under the column names. The header has a bottom
// rule and no vertical rules, so the columns stay open to the data below them.

afterEach(cleanup);

describe('ui/Table', () => {
  it('holds the column names at the top of the scrolling box', () => {
    const { container } = render(Fixture);
    const head = container.querySelector('thead')!;

    expect(head.getAttribute('data-sticky')).toBe('true');
  });

  it('fills the header with the one opaque colour there is', () => {
    const { container } = render(Fixture);

    expect(container.querySelector('thead')!.getAttribute('data-surface')).toBe('ground');
  });

  it('leaves the columns open to the data below them', () => {
    const { container } = render(Fixture);
    const headings = [...container.querySelectorAll('th')];
    const cells = [...container.querySelectorAll('td')];

    expect(headings.every((cell) => !cell.hasAttribute('data-divider'))).toBe(true);
    expect(cells.some((cell) => cell.hasAttribute('data-divider'))).toBe(false);
  });

  it('says which row is being acted on without borrowing a status colour', () => {
    render(Fixture);
    const row = screen.getByText('Sleepless').closest('tr')!;

    expect(row.getAttribute('aria-selected')).toBe('true');
    expect(row.getAttribute('data-selected')).toBe('');
  });

  it('sets a column of figures in the face that lines them up', () => {
    render(Fixture);

    expect(screen.getByText('2').getAttribute('data-numeric')).toBe('true');
  });
});
