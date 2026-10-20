import { afterEach, describe, expect, it } from 'vitest';
import { cleanup, render, screen } from '@testing-library/svelte';
import { createRawSnippet } from 'svelte';
import { Badge } from '$lib/components/ui/badge';

// No state in Schall may be identifiable by hue alone. A badge with a role
// carries a dot in front of its words, and the words say the state out loud, so
// a reader who cannot tell the six tints apart still reads the state.

afterEach(cleanup);

describe('ui/Badge', () => {
  it('marks a role with a dot as well as a tint', () => {
    const { container } = render(Badge, { role: 'fail', children: label('Failed') });

    expect(container.querySelector('[aria-hidden="true"]')).toBeTruthy();
    expect(screen.getByText('Failed')).toBeTruthy();
  });

  it('draws no dot where there is no role, because grey is already a role', () => {
    const { container } = render(Badge, { children: label('FLAC') });

    expect(container.querySelector('[aria-hidden="true"]')).toBeNull();
  });

  it('drops the dot for a badge that draws a glyph of its own', () => {
    const { container } = render(Badge, { role: 'busy', dot: false, children: label('Searching') });

    expect(container.querySelector('[aria-hidden="true"]')).toBeNull();
  });

  it('gives finished its own colour rather than borrowing success', () => {
    render(Badge, { role: 'done', children: label('Decided') });
    render(Badge, { role: 'ok', children: label('Verified') });

    // The words arrive wrapped in a span of their own, so the badge is the
    // element above them.
    const finished = screen.getByText('Decided').parentElement!;
    const good = screen.getByText('Verified').parentElement!;

    expect(finished.getAttribute('data-role')).toBe('done');
    expect(good.getAttribute('data-role')).toBe('ok');
  });
});

function label(text: string) {
  return createRawSnippet(() => ({ render: () => `<span>${text}</span>` }));
}
