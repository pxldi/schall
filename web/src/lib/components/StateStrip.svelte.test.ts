import { afterEach, describe, expect, it } from 'vitest';
import { cleanup, render, screen } from '@testing-library/svelte';
import StateStrip from './StateStrip.svelte';

// One dot per row in list order, and a legend that says the counts in words.

afterEach(cleanup);

const item = (n: number, label: string, role: 'ok' | 'busy' | 'idle' | 'fail') => ({
  id: `e${n}`,
  title: `Song ${n}`,
  position: n,
  state: { label, role }
});

describe('StateStrip', () => {
  it('draws a dot per row in order and counts each state in words', () => {
    render(StateStrip, {
      items: [item(1, 'complete', 'ok'), item(2, 'resolving', 'idle'), item(3, 'complete', 'ok'), item(4, 'needs review', 'fail')]
    });

    const dots = document.querySelectorAll('button[data-role]');
    expect([...dots].map((dot) => dot.getAttribute('title'))).toEqual([
      '1. Song 1 · complete',
      '2. Song 2 · resolving',
      '3. Song 3 · complete',
      '4. Song 4 · needs review'
    ]);
    const legend = screen.getByText('4 songs:').closest('p')!;
    expect(legend.textContent?.replace(/\s+/g, ' ').trim()).toBe('4 songs: 2 complete 1 needs review 1 resolving');
  });
});
