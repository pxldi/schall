import { afterEach, describe, expect, it } from 'vitest';
import { cleanup, render, screen } from '@testing-library/svelte';
import { createRawSnippet } from 'svelte';
import { Button } from '$lib/components/ui/button';

// What is worth holding here is that the element follows the href while the
// shape does not: a link that is not announced as a link is unreachable by
// keyboard the way a link is, and a link that looks unlike the button beside it
// is a second design.

afterEach(cleanup);

describe('ui/Button', () => {
  it('is a button when it does something', () => {
    render(Button, { children: label('Rescan') });

    expect(screen.getByRole('button', { name: 'Rescan' })).toBeTruthy();
    expect(screen.queryByRole('link')).toBeNull();
  });

  it('is a link when it goes somewhere', () => {
    render(Button, { href: '/library?status=missing', children: label('Find missing') });

    const link = screen.getByRole('link', { name: 'Find missing' });

    expect(link.tagName).toBe('A');
    expect(link.getAttribute('href')).toBe('/library?status=missing');
  });

  it('wears the same variant either way', () => {
    render(Button, { variant: 'danger', size: 'sm', children: label('Delete') });
    render(Button, { href: '/downloads', variant: 'danger', size: 'sm', children: label('Open') });

    expect(screen.getByRole('link', { name: 'Open' }).getAttribute('data-variant')).toBe(
      screen.getByRole('button', { name: 'Delete' }).getAttribute('data-variant')
    );
  });

  it('refuses a press when it is disabled', () => {
    render(Button, { disabled: true, children: label('Rescan') });

    const button = screen.getByRole('button', { name: 'Rescan' }) as HTMLButtonElement;

    expect(button.disabled).toBe(true);
  });
});

// What a page writes between the tags arrives as a compiled snippet, which a
// test calling `render` has no template to produce — so the label is built the
// way Svelte builds one from markup it did not compile.
function label(text: string) {
  return createRawSnippet(() => ({ render: () => `<span>${text}</span>` }));
}
