import { afterEach, describe, expect, it } from 'vitest';
import { cleanup, render } from '@testing-library/svelte';
import { flushSync } from 'svelte';

import { duoton } from './page.svelte';
import { HOUSE_INKS } from './inks';
import PrintPage from './testing/PrintPage.svelte';

afterEach(cleanup);

const inked = { covers: [{ seed: 'dummy', inks: { dark: '#0d1420', light: '#f2b48c' } }] };

describe('a page that is still loading', () => {
  it('keeps the inks already up instead of drawing the house inks first', async () => {
    const first = render(PrintPage, { props: { print: () => inked } });
    flushSync();
    expect(duoton.inks.dark).toBe('#0d1420');

    first.unmount();
    render(PrintPage, { props: { print: () => undefined } });
    flushSync();
    await new Promise((resolve) => setTimeout(resolve));

    expect(duoton.inks.dark).toBe('#0d1420');
    expect(duoton.inks.dark).not.toBe(HOUSE_INKS.dark);
  });

  it('falls back to the house inks when no page asks for a print', async () => {
    const page = render(PrintPage, { props: { print: () => inked } });
    flushSync();
    page.unmount();
    await new Promise((resolve) => setTimeout(resolve));

    expect(duoton.inks).toEqual(HOUSE_INKS);
  });
});
