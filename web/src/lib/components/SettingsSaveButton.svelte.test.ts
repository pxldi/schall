import { afterEach, describe, expect, it } from 'vitest';
import { cleanup, render, screen } from '@testing-library/svelte';
import SettingsSaveButton from '$lib/components/SettingsSaveButton.svelte';

// Ten cards on the settings page hand-rolled this button: a submit that
// disables itself while its own mutation runs and swaps its icon for a
// spinner meanwhile. Assert the behaviour that survived the extraction.
// A class name is not the contract.

afterEach(cleanup);

describe('SettingsSaveButton', () => {
  it('submits the form it sits in', () => {
    render(SettingsSaveButton, {});

    const button = screen.getByRole('button', { name: 'Save' }) as HTMLButtonElement;
    expect(button.type).toBe('submit');
    expect(button.disabled).toBe(false);
  });

  it('refuses a press while its mutation is pending', () => {
    render(SettingsSaveButton, { pending: true });

    expect((screen.getByRole('button', { name: 'Save' }) as HTMLButtonElement).disabled).toBe(
      true
    );
  });

  // A card that needs more than "not already saving" to allow a press — the
  // library layout card refuses an empty template — passes that in beside the
  // pending state rather than computing it here, so the card keeps owning the
  // rule.
  it('refuses a press when the card disables it for its own reason', () => {
    render(SettingsSaveButton, { disabled: true });

    expect((screen.getByRole('button', { name: 'Save' }) as HTMLButtonElement).disabled).toBe(
      true
    );
  });

  it('allows a press once neither condition holds', () => {
    render(SettingsSaveButton, { pending: false, disabled: false });

    expect((screen.getByRole('button', { name: 'Save' }) as HTMLButtonElement).disabled).toBe(
      false
    );
  });

  // Sources draws Save as its one accent action; every other card keeps the
  // outline default.
  it('draws the outline by default and the accent when a card asks for it', () => {
    render(SettingsSaveButton, {});
    expect(screen.getByRole('button', { name: 'Save' }).dataset.variant).toBe('outline');

    cleanup();
    render(SettingsSaveButton, { variant: 'primary' });
    expect(screen.getByRole('button', { name: 'Save' }).dataset.variant).toBe('primary');
  });

  it('shows Saved once the mutation it submits has succeeded', () => {
    render(SettingsSaveButton, { saved: true });

    expect(screen.getByRole('button', { name: 'Saved' }).textContent?.trim()).toBe('Saved');
  });

  // dirty is false only once a form has a loaded baseline to compare against
  // and nothing in it differs. Left undefined — no baseline — the button is
  // never disabled by dirty alone.
  it('refuses a press when the form has nothing changed to save', () => {
    render(SettingsSaveButton, { dirty: false });

    expect((screen.getByRole('button', { name: 'Save' }) as HTMLButtonElement).disabled).toBe(
      true
    );
  });

  it('allows a press once the form holds something to save', () => {
    render(SettingsSaveButton, { dirty: true });

    expect((screen.getByRole('button', { name: 'Save' }) as HTMLButtonElement).disabled).toBe(
      false
    );
  });
});
