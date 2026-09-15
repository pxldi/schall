import { afterEach, describe, expect, it } from 'vitest';
import { cleanup, render, screen } from '@testing-library/svelte';
import { Input } from '$lib/components/ui/input';

// The one thing this component must never do is draw a field of its own. Every
// metric — the height, the inset fill, the mono face, the placeholder — belongs
// to the `.field` class in the stylesheet, and a copy of any of them here is a
// second field that drifts from the first the next time either is touched.

afterEach(cleanup);

describe('ui/Input', () => {
  it('asks the stylesheet for the field rather than drawing one', () => {
    render(Input, { placeholder: 'Search the library' });

    expect(screen.getByPlaceholderText('Search the library').getAttribute('data-field')).toBe('true');
  });

  it('takes the room for a trailing glyph from the component, never the call site', () => {
    render(Input, { placeholder: 'Waiting', trailing: true });

    expect(screen.getByPlaceholderText('Waiting').getAttribute('data-trailing')).toBe('true');
  });

  it('passes an attribute of the element straight through', () => {
    render(Input, { placeholder: 'Refused', disabled: true });

    expect((screen.getByPlaceholderText('Refused') as HTMLInputElement).disabled).toBe(true);
  });
});
