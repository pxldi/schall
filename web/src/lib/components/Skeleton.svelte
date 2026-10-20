<script lang="ts">
  import { cn } from '$lib/utils';

  // A placeholder for one thing that has not arrived yet: a line of text, a
  // cover, an artist's photograph. A flat raised block that breathes
  // (`.skeleton` in styles.css). Size it with classes, the way the thing it
  // stands for will be sized.
  //
  //   <Skeleton class="h-4 w-2/3" />
  //   <Skeleton shape="cover" class="size-12" />
  //
  // It is decoration and empty, so assistive technology passes over it.
  // Whatever holds a set of them says it is loading, once, with `aria-busy` and
  // a label; forty boxes each saying so would be read out forty times.

  let {
    shape = 'line',
    class: className
  }: {
    /** `line` a run of text, `block` a panel or a card, `cover` a square picture,
     *  `round` a photograph (square like every picture). */
    shape?: 'line' | 'block' | 'cover' | 'round';
    class?: string;
  } = $props();

  const shapes = {
    line: 'h-4',
    block: '',
    cover: 'aspect-square',
    round: 'aspect-square'
  };
</script>

<span class={cn('skeleton', shapes[shape], className)}></span>
