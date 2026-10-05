import { afterEach, describe, expect, it } from 'vitest';
import { cleanup, render } from '@testing-library/svelte';
import { Search } from '@lucide/svelte';
import Icon from './Icon.svelte';

afterEach(cleanup);

describe('Icon', () => {
  it('is hidden from assistive technology', () => {
    const { container } = render(Icon, { icon: Search });
    expect(container.querySelector('svg')?.getAttribute('aria-hidden')).toBe('true');
  });

  it('draws an icon by its name', () => {
    const { container } = render(Icon, { name: 'search' });
    expect(container.querySelector('svg')).not.toBeNull();
  });

  it('draws at the size it is asked for, in rem', () => {
    const { container } = render(Icon, { icon: Search, size: 'lg' });
    expect(container.querySelector('svg')?.getAttribute('width')).toBe('1.25rem');
  });
});
