import { afterEach, describe, expect, it } from 'vitest';
import { cleanup, render, screen } from '@testing-library/svelte';
import { createRawSnippet } from 'svelte';
import StateTag from './StateTag.svelte';

// The plain hairline tag a table row draws for its own state — no dot, no
// tint, unlike `Chip.svelte`. Worth testing: the label reads, `title` passes
// through for a truncated word, and the tone a caller asks for is the one
// that lands.

afterEach(cleanup);

// What a page writes between the tags arrives as a compiled snippet, which a
// test calling `render` has no template to produce — so the label is built the
// way Svelte builds one from markup it did not compile.
function label(text: string) {
  return createRawSnippet(() => ({ render: () => `<span>${text}</span>` }));
}

describe('StateTag', () => {
  it('shows its label', () => {
    render(StateTag, { children: label('unidentified') });

    expect(screen.getByText('unidentified')).toBeTruthy();
  });

  it('passes a title through for a longer summary', () => {
    render(StateTag, {
      tone: 'broken',
      title: 'FLAC header did not parse',
      children: label('unreadable')
    });

    expect(screen.getByTitle('FLAC header did not parse').textContent).toBe('unreadable');
  });

  it('defaults to the neutral tone', () => {
    render(StateTag, { children: label('held twice') });

    expect(screen.getByText('held twice').closest('[data-tone]')?.getAttribute('data-tone')).toBe(
      'neutral'
    );
  });

  it('carries the tone a caller asks for', () => {
    render(StateTag, { tone: 'attention', children: label('needs review') });

    expect(
      screen.getByText('needs review').closest('[data-tone]')?.getAttribute('data-tone')
    ).toBe('attention');
  });
});
