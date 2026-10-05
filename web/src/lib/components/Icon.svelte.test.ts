import { afterEach, describe, expect, it } from 'vitest';
import { cleanup, render } from '@testing-library/svelte';
import Icon from './Icon.svelte';

afterEach(cleanup);

describe('Icon', () => {
  it('is hidden from assistive technology', () => {
    const { container } = render(Icon, { name: 'search' });
    expect(container.querySelector('svg')?.getAttribute('aria-hidden')).toBe('true');
  });

  it('draws the key line and the offset plate from the same shapes', () => {
    const { container } = render(Icon, { name: 'search' });
    const [plate, key] = container.querySelectorAll('svg > g');
    expect(plate.getAttribute('transform')).toBe('translate(1.6 1.6)');
    expect(plate.querySelectorAll('path')).toHaveLength(key.querySelectorAll('path').length);
  });

  it('draws at the size it is asked for, in rem', () => {
    const { container } = render(Icon, { name: 'search', size: 'lg' });
    expect(container.querySelector('svg')?.getAttribute('width')).toBe('1.25rem');
  });
});
