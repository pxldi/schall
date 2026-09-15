import { afterEach, beforeAll, describe, expect, it } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { shimTheMissingBrowser } from '../test-window';
import Fixture from './TooltipFixture.svelte';

// A tooltip is where the word is when a control has no room for one. The
// navigation rail collapsed to 72px is seven such controls, so two things
// decide whether this component is fit for that: the word has to appear for a
// pointer resting on the control, and it has to appear for a keyboard landing
// on it. The second is the one that is usually missed, and it is the one the
// rail cannot do without.
//
// It is drawn at the end of the document rather than beside its trigger, for
// the reason the popover is: the rail is a narrow box and a word wider than
// 72px drawn inside it is a word cut off at the rail's edge.

beforeAll(shimTheMissingBrowser);

afterEach(cleanup);

describe('ui/Tooltip', () => {
  it('says the word when a pointer rests on the control', async () => {
    render(Fixture);

    await fireEvent.pointerEnter(screen.getByRole('link', { name: 'Review' }));

    await waitFor(() => expect(screen.getAllByText('Review').length).toBeGreaterThan(0));
  });

  it('says the word when the keyboard lands on the control', async () => {
    render(Fixture);

    await fireEvent.focus(screen.getByRole('link', { name: 'Review' }));

    // The panel says what it is: a tooltip, described from the control that
    // opened it, rather than a box of text nothing points at.
    expect(await screen.findByRole('tooltip')).toHaveProperty('textContent', 'Review');
  });

  it('keeps the trigger the link it was', () => {
    render(Fixture);

    const trigger = screen.getByRole('link', { name: 'Review' });

    expect(trigger.tagName).toBe('A');
    expect(trigger.getAttribute('href')).toBe('/review');
  });

  it('draws the word outside the box its control sits in', async () => {
    const { container } = render(Fixture);

    await fireEvent.focus(screen.getByRole('link', { name: 'Review' }));

    await waitFor(() => {
      const drawn = screen.getAllByText('Review').find((element) => !container.contains(element));
      expect(drawn).toBeTruthy();
    });
  });
});
