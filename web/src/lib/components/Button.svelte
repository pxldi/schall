<script lang="ts">
  import type { HTMLAnchorAttributes, HTMLButtonAttributes } from 'svelte/elements';
  import { cn } from '$lib/utils';

  type Shape = {
    variant?: 'primary' | 'outline' | 'danger' | 'ghost';
    // Three heights and no more. `md` is the page's own action, `sm` a control
    // beside a list, `xs` a button that lives inside a row. Every page had been
    // reaching for `class="h-7 text-meta"` to say `sm`.
    size?: 'md' | 'sm' | 'xs';
    // A square button holding one glyph. It carries the label a text button
    // would have shown as its title and aria-label instead, which is what keeps
    // a list of rows from repeating the same two words down the page.
    icon?: boolean;
    // The fingertip taken as padding instead of as an overlay, for a button
    // whose ancestor hides its overflow. `.tap` centres an invisible 44px box
    // over the drawing, and a box is clipped by an ancestor that hides what
    // leaves it — a scrolling table, a `max-h` list, a rounded panel that clips
    // its own corners — which pulls the box straight back to the size it was
    // escaping. Padding is inside the box and is never clipped.
    //
    // The row gets taller by that padding on a coarse pointer, and it is meant
    // to. A list read with a thumb wants taller rows; a mouse sees no change at
    // all, because none of this leaves `pointer: coarse`.
    tall?: boolean;
  };

  // Given an href this is an <a>, and pages stop retyping the shape to link
  // somewhere. The two are separate types rather than one with an optional
  // href because a link is not a button: it has no `disabled`, and a component
  // that took one anyway would either drop it in silence or leave a control
  // that looks refused and still navigates. Asking for both is a type error,
  // which is the only place that answer is worth giving.
  type Props = Shape &
    ((HTMLButtonAttributes & { href?: never }) | (HTMLAnchorAttributes & { href: string }));

  let {
    class: className,
    children,
    variant = 'primary',
    size = 'md',
    icon = false,
    tall = false,
    href,
    ...props
  }: Props = $props();

  // The corner follows the height. `button.html` gives 8px to the two sizes that
  // stand on their own and 6px to the one that sits inside a row: an 8px radius
  // on a 24px control is a proportion the rest of the system never uses, and it
  // reads as a different component beside the 32px button it is meant to be a
  // smaller version of.
  const sizes = {
    md: { box: 'h-8 text-body', pad: 'px-3', square: 'w-8', radius: 'rounded-control' },
    sm: { box: 'h-7 text-meta', pad: 'px-2.5', square: 'w-7', radius: 'rounded-control' },
    xs: { box: 'h-6 text-meta', pad: 'px-2', square: 'w-6', radius: 'rounded-row' }
  };

  // The accent fills exactly one control per view: the action the screen exists
  // to offer. Everything else is an outline or a ghost, so that a page full of
  // buttons still points somewhere.
  const variants = {
    primary: 'bg-accent font-semibold text-accent-ink hover:bg-accent-soft',
    outline: 'border border-line-regular font-medium text-ink hover:border-line-thick',
    danger: 'border border-fail/40 bg-fail/14 font-medium text-fail hover:bg-fail/24',
    ghost: 'font-medium text-ink-2 hover:bg-surface-thick hover:text-ink'
  };

  // One class list for both elements, so that a link and a button of the same
  // variant are the same object on screen. The disabled rules are dead on an
  // <a>, which cannot be handed one.
  const classes = $derived(
    cn(
      // `press` is the only motion in the application a person makes happen
      // directly: the control gives 3% under a press and comes back. It answers
      // the press and never waits for it — whatever the button does has already
      // started — so it is safe on the answer bar of the review queue, where
      // nothing may stand between a key and a permanent decision.
      // `tap` is the fingertip. The three heights are 32, 28 and 24 pixels,
      // drawn for a mouse, which is accurate to the pixel; a finger covers
      // about 44. `tap` centres an invisible 44px box over the control where
      // the pointer is coarse and nowhere else, so the drawing keeps its size,
      // nothing in the layout moves, and a mouse sees no change at all.
      //
      // A control inside an ancestor that hides its overflow clips that box
      // back to the size it was escaping. `tap-tall` at the call site is the
      // answer there, because padding is inside the box and is not clipped.
      // A label is one to three words and is never a sentence, so it never
      // wraps. Left to wrap it does: `Cancel` in the actions column of a
      // download row was drawn as `Cance` over `l`, which is a control nobody
      // can read at a glance in the one place they are looking for it quickly.
      'press inline-flex items-center justify-center gap-1.5 whitespace-nowrap transition',
      'disabled:pointer-events-none disabled:opacity-50',
      // The drawn height is what `tall` has to let go of: a height set in
      // pixels swallows the padding rather than growing by it, so the height
      // becomes the floor and the padding does the rest. 44px is the figure
      // every touch platform publishes for a fingertip.
      tall ? 'tap-tall pointer-coarse:h-auto pointer-coarse:min-h-11' : 'tap',
      sizes[size].radius,
      sizes[size].box,
      icon ? `${sizes[size].square} shrink-0` : sizes[size].pad,
      // A square button is as narrow as it is short, so the same argument runs
      // sideways: the width is drawn and has to become a floor too.
      tall && icon && 'pointer-coarse:w-auto pointer-coarse:min-w-11',
      variants[variant],
      className
    )
  );

  // The rest arrives as the union it was declared as, and the href is what
  // decided which half of it the caller filled in. TypeScript cannot follow
  // that from here — a union of attributes belongs to neither element — so each
  // branch names the half its own element takes.
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
