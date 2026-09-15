import { afterEach, describe, expect, it } from 'vitest';
import { cleanup, render, screen } from '@testing-library/svelte';
import { createRawSnippet } from 'svelte';
import Button from '$lib/components/Button.svelte';

// Every action in the application is this component, and half a dozen of them
// go somewhere rather than doing something. What is worth holding is that the
// element changes with the href while the shape does not: a link that is not
// announced as a link is unreachable by keyboard the way a link is, and a link
// that looks unlike the button beside it is a second design.

afterEach(cleanup);

describe('Button', () => {
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
    expect(screen.queryByRole('button')).toBeNull();
  });

  // An address built from a row that has not arrived can come out empty, and
  // the element has to follow the prop rather than its value: swapping to a
  // <button> would take the role and the keyboard away from something that
  // still looks like a link. The assertion is on the tag because an empty href
  // is not enough for the accessibility tree to name it one either.
  it('stays a link when the address it was given is empty', () => {
    render(Button, { href: '', children: label('Find missing') });

    expect(screen.getByText('Find missing').closest('a')).toBeTruthy();
    expect(screen.queryByRole('button')).toBeNull();
  });

  it('wears the same variant either way', () => {
    render(Button, { variant: 'outline', size: 'sm', children: label('Rescan') });
    render(Button, { href: '/downloads', variant: 'outline', size: 'sm', children: label('Open') });

    const pressed = screen.getByRole('button', { name: 'Rescan' });
    const followed = screen.getByRole('link', { name: 'Open' });

    expect(followed.getAttribute('data-variant')).toBe(pressed.getAttribute('data-variant'));
  });

  // The corner follows the height, and only the in-row size differs.
  // `button.html` gives 8px to the two sizes that stand on their own and 6px to
  // the one that sits inside a row.
  it('rounds the in-row size more tightly than the ones that stand alone', () => {
    render(Button, { size: 'xs', children: label('Retry') });
    render(Button, { size: 'sm', children: label('Test') });
    render(Button, { size: 'md', children: label('Rescan') });

    expect(screen.getByRole('button', { name: 'Retry' }).getAttribute('data-size')).toBe('xs');
    expect(screen.getByRole('button', { name: 'Test' }).getAttribute('data-size')).toBe('sm');
    expect(screen.getByRole('button', { name: 'Rescan' }).getAttribute('data-size')).toBe('md');
  });

  it('refuses a press when it is disabled', () => {
    render(Button, { disabled: true, children: label('Rescan') });

    expect((screen.getByRole('button', { name: 'Rescan' }) as HTMLButtonElement).disabled).toBe(
      true
    );
  });
});

// What a page writes between the tags arrives as a compiled snippet, which a
// test calling `render` has no template to produce — so the label is built the
// way Svelte builds one from markup it did not compile.
function label(text: string) {
  return createRawSnippet(() => ({ render: () => `<span>${text}</span>` }));
}
