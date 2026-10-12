import { afterEach, describe, expect, it } from 'vitest';
import { cleanup, render } from '@testing-library/svelte';
import Icon from './Icon.svelte';

afterEach(cleanup);

describe('Icon', () => {
  it('is hidden from assistive technology', () => {
    const { container } = render(Icon, { name: 'search' });
    expect(container.querySelector('svg')?.getAttribute('aria-hidden')).toBe('true');
  });

  it('draws one line in the text colour and no offset copy under it', () => {
    const { container } = render(Icon, { name: 'search' });
    const layers = container.querySelectorAll('svg > g');
    expect(layers).toHaveLength(1);
    expect(layers[0].getAttribute('stroke')).toBe('currentColor');
    expect(container.querySelector('[transform]')).toBeNull();
  });

  it('draws at the size it is asked for, in rem', () => {
    const { container } = render(Icon, { name: 'search', size: 'lg' });
    expect(container.querySelector('svg')?.getAttribute('width')).toBe('1.25rem');
  });
});
