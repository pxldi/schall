import { afterEach, describe, expect, it } from 'vitest';
import { cleanup, render, waitFor } from '@testing-library/svelte';
import { createRawSnippet } from 'svelte';

import Settle from '$lib/components/Settle.svelte';

// The wrapper that crossfades a skeleton into the answer it stood for. What is
// pinned here is which of the two states is on the page and that the skeleton
// cannot catch a click meant for the content arriving over it — the fade
// itself is timing, and timing is the stylesheet's.

afterEach(cleanup);

const placeholder = createRawSnippet(() => ({
  render: () => '<p>pulsing boxes</p>'
}));
const children = createRawSnippet(() => ({
  render: () => '<p>the answer</p>'
}));

function draw(pending: boolean) {
  return render(Settle, { props: { pending, placeholder, children } });
}

describe('while the page is waiting', () => {
  it('draws the skeleton and not the content', () => {
    const { queryByText } = draw(true);

    expect(queryByText('pulsing boxes')).not.toBeNull();
    expect(queryByText('the answer')).toBeNull();
  });

  it('keeps the skeleton out of the way of pointers', () => {
    const { getByText } = draw(true);

    expect(getByText('pulsing boxes').parentElement?.className).toContain('pointer-events-none');
  });
});

describe('once the answer lands', () => {
  it('draws the content and takes the skeleton down', async () => {
    const { queryByText, rerender } = draw(true);

    await rerender({ pending: false });

    expect(queryByText('the answer')).not.toBeNull();
    await waitFor(() => expect(queryByText('pulsing boxes')).toBeNull());
  });
});
