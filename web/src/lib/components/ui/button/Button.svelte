<script lang="ts" module>
  // The four things a button can be. A variant is what the control says about
  // itself, never what state anything is in.
  //
  //   primary  the one action the screen exists to offer. It is the accent, so
  //            exactly one control per view may wear it.
  //   quiet    an action that is offered rather than urged. An outline.
  //   danger   an action that destroys something or cannot be undone. It is
  //            tinted rather than filled, because the accent is the only colour
  //            in the product with a matching ink to fill against.
  //   ghost    an action that is only a word. No box until it is hovered.
  export type ButtonVariant = 'primary' | 'quiet' | 'danger' | 'ghost';

  // Three heights and no more. `md` is the page's own action, `sm` a control
  // beside a list, `xs` a button that lives inside a row.
  export type ButtonSize = 'md' | 'sm' | 'xs';
</script>

<script lang="ts">
  import type { HTMLAnchorAttributes, HTMLButtonAttributes } from 'svelte/elements';
  import { cn } from '$lib/utils';

  type Shape = {
    variant?: ButtonVariant;
    size?: ButtonSize;
    // A square button holding one glyph. It carries the label a text button
    // would have shown as its `title` and `aria-label` instead.
    icon?: boolean;
  };

  // Given an href this is an <a>. The two are separate types rather than one
  // with an optional href because a link is not a button: it has no `disabled`,
  // and a control that looks refused and still navigates is worse than either.
  type Props = Shape &
    ((HTMLButtonAttributes & { href?: never }) | (HTMLAnchorAttributes & { href: string }));

  let {
    class: className,
    children,
    variant = 'primary',
    size = 'md',
    icon = false,
    href,
    ...props
  }: Props = $props();

  // The corner follows the height: 8px for the two sizes that stand on their
  // own, 6px for the one that sits inside a row. An 8px radius on a 24px
  // control reads as a different component beside the 32px button it is meant
  // to be a smaller version of.
  //
  // Every size names a step of the dense scale by name, so it takes the
  // leading and the tracking with it. All nine components in this batch are
  // chrome or table furniture, which is what `dense` is for.
  const sizes: Record<ButtonSize, { box: string; pad: string; square: string; radius: string }> = {
    md: { box: 'h-8 text-dense-body', pad: 'px-3', square: 'w-8', radius: 'rounded-control' },
    sm: { box: 'h-7 text-dense-meta', pad: 'px-2.5', square: 'w-7', radius: 'rounded-control' },
    xs: { box: 'h-6 text-dense-meta', pad: 'px-2', square: 'w-6', radius: 'rounded-row' }
  };

  // Written out per variant rather than built from it, because Tailwind reads
  // class names out of the source as literal text and never sees a name a
  // template string assembled at runtime.
  const variants: Record<ButtonVariant, string> = {
    primary: 'bg-accent font-semibold text-accent-ink hover:bg-accent-soft',
    quiet: 'border border-line-regular font-medium text-ink hover:border-line-thick',
    danger: 'border border-fail/40 bg-fail/14 font-medium text-fail hover:bg-fail/24',
    ghost: 'font-medium text-ink-2 hover:bg-surface-thick hover:text-ink'
  };

  // One class list for both elements, so a link and a button of the same
  // variant are the same object on screen.
  const classes = $derived(
    cn(
      // `press` is the press motion the stylesheet draws: the control gives 3%
      // under a press and comes back. It answers the press and never waits for
      // it. `tap` is the fingertip — an invisible 44px box centred over the
      // drawing where the pointer is coarse, so the drawing keeps its size and
      // a mouse sees no change at all.
      //
      // A label is one to three words and is never a sentence, so it never
      // wraps. Left free to wrap it does, and a control nobody can read at a
      // glance is a control nobody presses.
      'press tap inline-flex items-center justify-center gap-1.5 whitespace-nowrap transition',
      'disabled:pointer-events-none disabled:opacity-50',
      sizes[size].radius,
      sizes[size].box,
      icon ? `${sizes[size].square} shrink-0` : sizes[size].pad,
      variants[variant],
      className
    )
  );

  // The rest arrives as the union it was declared as, and the href is what
  // decided which half of it the caller filled in. TypeScript cannot follow
  // that from here, so each branch names the half its own element takes.
  const anchor = $derived({ ...props } as HTMLAnchorAttributes);
  const button = $derived({ ...props } as HTMLButtonAttributes);
</script>

<!-- An href given at all makes this a link, empty or not. An address built from
     a row that has not arrived can come out empty, and a control that stopped
     being a link because of it would take its role and its keyboard with it
     while still looking like the one beside it. -->
{#if href !== undefined}
  <a {href} class={classes} data-variant={variant} data-size={size} {...anchor}>
    {@render children?.()}
  </a>
{:else}
  <button class={classes} data-variant={variant} data-size={size} {...button}>
    {@render children?.()}
  </button>
{/if}
