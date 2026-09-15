import { afterEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render } from '@testing-library/svelte';
import { tick } from 'svelte';

import Cover from '$lib/components/Cover.svelte';

// A cover or an artist photograph. These tests pin its loading transition and
// the fallback it uses when the source has no picture.

afterEach(cleanup);

let sourceNumber = 0;

type CoverTestProps = {
  src?: string;
  fallback?: string;
  eager?: boolean;
  onmissing?: () => void;
  class?: string;
};

function draw(props: CoverTestProps = {}) {
  const src = props.src ?? `/api/v1/albums/abc/cover-${sourceNumber++}`;
  const { container } = render(Cover, {
    props: { src, ...props }
  });
  return { container, image: () => container.querySelector('img') };
}

describe('a picture that has not arrived', () => {
  it('is drawn transparent, so it cannot flash in at full opacity', () => {
    const { image } = draw();

    expect(image()?.getAttribute('data-state')).toBe('loading');
  });

  it('waits for its own bytes rather than for the page', async () => {
    const { image } = draw();

    await fireEvent.load(image()!);

    expect(image()?.getAttribute('data-state')).toBe('shown');
  });

  it('is left to the browser to fetch when it is below the fold', () => {
    expect(draw().image()?.getAttribute('loading')).toBe('lazy');
  });

  // The one picture a page is about is not deferred: on the release and artist
  // pages it is the first thing anybody looks at.
  it('is fetched at once when the page is about it', () => {
    expect(draw({ eager: true }).image()?.getAttribute('loading')).toBe('eager');
  });
});

describe('a picture that does not exist', () => {
  // A release nobody has pictured answers 404. What is behind the frame — the
  // hatched square, the artist's initials — is the real answer.
  it('takes itself down rather than leaving a broken frame', async () => {
    const { image } = draw();

    await fireEvent.error(image()!);

    expect(image()).toBeNull();
  });

  it('says so once, so a caption about the picture can go too', async () => {
    const onmissing = vi.fn();
    const { image } = draw({ onmissing });

    await fireEvent.error(image()!);

    expect(onmissing).toHaveBeenCalledTimes(1);
  });

  it('keeps a designed fallback and remembers the missing source', async () => {
    const src = '/api/v1/albums/missing/cover';
    const first = draw({ src, fallback: '♪' });
    await fireEvent.error(first.image()!);
    await tick();

    expect(first.container.textContent).toContain('♪');
    expect(draw({ src, fallback: '♪' }).image()).toBeNull();
  });
});
