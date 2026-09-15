import { afterEach, describe, expect, it } from 'vitest';
import { cleanup, render, screen } from '@testing-library/svelte';
import TranscodeChip from './TranscodeChip.svelte';

// The chip that says a copy's sound was squeezed before it reached the container
// it is in. Two words on the row; the measurement behind them sits in a
// disclosure, so somebody weighing two copies can read it and everybody else
// does not have to.

afterEach(cleanup);

describe('TranscodeChip', () => {
  it('names the suspicion in the row', () => {
    render(TranscodeChip, { cutoffHz: 15750 });

    expect(screen.getByText('Likely transcode')).toBeTruthy();
  });

  it('keeps the measurement behind the disclosure', () => {
    const { container } = render(TranscodeChip, { cutoffHz: 15750, encoder: 'LAME3.100' });

    const disclosure = container.querySelector('details');
    expect(disclosure?.open).toBe(false);
    expect(disclosure?.textContent).toContain('15.8 kHz');
    expect(disclosure?.textContent).toContain('LAME3.100');
  });

  // The cutoff can be absent — a copy measured before the number was stored, or
  // one whose evidence carries only the verdict. The chip still reads as a
  // sentence rather than as a gap where a figure should be.
  it('reads without a cutoff', () => {
    const { container } = render(TranscodeChip, {});

    expect(screen.getByText('Likely transcode')).toBeTruthy();
    expect(container.querySelector('details')?.textContent).toContain(
      'stops where a lossy encoder would have stopped it'
    );
  });
});
