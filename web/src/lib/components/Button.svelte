<script lang="ts">
  import type { HTMLAnchorAttributes, HTMLButtonAttributes } from 'svelte/elements';
  import { ariaKey, useKeys } from '$lib/keys.svelte';
  import { cn } from '$lib/utils';
  import Icon from './Icon.svelte';
  import Keycap from './Keycap.svelte';

  type Shape = {
    // `primary` is drawn inverted and marks the one action the screen exists
    // to offer. `outline` is every other action: a raised fill and a 1px rule.
    // `danger` destroys something and says so in the fail colour with its
    // glyph. `ghost` is only a word until it is hovered.
    variant?: 'primary' | 'outline' | 'danger' | 'ghost';
    // `md` is the page's own action, `sm` a control beside a list, `xs` a
    // button inside a row.
    size?: 'md' | 'sm' | 'xs';
    // A square button holding one glyph. It carries its label as `title` and
    // `aria-label` instead.
    icon?: boolean;
    // The fingertip taken as padding instead of as an overlay, for a button
    // whose ancestor hides its overflow and would clip `.tap`'s box.
    tall?: boolean;
    // The key that presses this button (`KeyboardEvent.key`, lowercase). The
    // button registers it with $lib/keys while it is mounted, draws its cap,
    // and names it in the key strip with `verb`.
    key?: string;
    /** What the key strip says the key does. Defaults to the button's label. */
    verb?: string;
  };

  // Given an href this is an <a>. A link has no `disabled`, so asking for both
  // is a type error.
  type Props = Shape &
    ((HTMLButtonAttributes & { href?: never }) | (HTMLAnchorAttributes & { href: string }));

  let {
    class: className,
    children,
    variant = 'primary',
    size = 'md',
    icon = false,
    tall = false,
    key,
    verb,
    href,
    ...props
  }: Props = $props();

  const sizes = {
    md: { box: 'h-[1.875rem] text-body', pad: 'px-3', square: 'w-[1.875rem]' },
    sm: { box: 'h-7 text-meta', pad: 'px-2.5', square: 'w-7' },
    xs: { box: 'h-6 text-meta', pad: 'px-2', square: 'w-6' }
  };

  const variants = {
    primary: 'inverted border border-transparent font-semibold',
    outline: 'border border-rule-strong bg-raised font-medium text-ink hover:border-ink-4',
    danger: 'border border-rule-strong bg-raised font-medium text-fail hover:border-fail',
    ghost: 'border border-transparent font-medium text-ink-2 hover:bg-raised hover:text-ink'
  };

  const classes = $derived(
    cn(
      // `press` gives 3% under a press. `tap` is the fingertip under a coarse
      // pointer. A label is one to three words and never wraps.
      'press inline-flex items-center justify-center gap-2 whitespace-nowrap transition',
      'disabled:pointer-events-none disabled:opacity-50',
      tall ? 'tap-tall pointer-coarse:h-auto pointer-coarse:min-h-11' : 'tap',
      sizes[size].box,
      icon ? `${sizes[size].square} shrink-0` : sizes[size].pad,
      tall && icon && 'pointer-coarse:w-auto pointer-coarse:min-w-11',
      variants[variant],
      className
    )
  );

  const anchor = $derived({ ...props } as HTMLAnchorAttributes);
  const button = $derived({ ...props } as HTMLButtonAttributes);
  const label = $derived(
    verb ?? (typeof props['aria-label'] === 'string' ? props['aria-label'] : typeof props.title === 'string' ? props.title : 'press')
  );

  let element = $state<HTMLElement>();
  let pressing = $state(false);

  useKeys(() =>
    key
      ? [
          {
            keys: [key],
            verb: label.toLocaleLowerCase(),
            enabled: () => !button.disabled,
            fromControls: key !== 'enter' && key !== ' ',
            run: () => {
              pressing = false;
              requestAnimationFrame(() => (pressing = true));
              element?.click();
            }
          }
        ]
      : []
  );
</script>

{#snippet inside()}
  {#if variant === 'danger' && !icon}
    <Icon name="alert" size="sm" />
  {/if}
  {@render children?.()}
  {#if key && !icon}
    <Keycap {key} class="-mr-1" />
  {/if}
{/snippet}

<!-- An href given at all makes this a link, empty or not, so it keeps its
     role and its keyboard while its address is still being built. -->
{#if href !== undefined}
  <a
    bind:this={element}
    {href}
    class={classes}
    data-variant={variant}
    data-size={size}
    data-echo-press={pressing ? '' : undefined}
    onanimationend={() => (pressing = false)}
    aria-keyshortcuts={key ? ariaKey(key) : undefined}
    {...anchor}
  >
    {@render inside()}
  </a>
{:else}
  <button
    bind:this={element}
    class={classes}
    data-variant={variant}
    data-size={size}
    data-echo-press={pressing ? '' : undefined}
    onanimationend={() => (pressing = false)}
    aria-keyshortcuts={key ? ariaKey(key) : undefined}
    {...button}
  >
    {@render inside()}
  </button>
{/if}
