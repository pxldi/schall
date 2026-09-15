<script lang="ts">
  import type { HTMLFormAttributes } from 'svelte/elements';

  // The two-column form grid that every settings form is set in: a 180px label
  // column and the control beside it, 10px between rows and 20px between the
  // columns, as design/field.html settles it. Below the small breakpoint the
  // grid collapses to one column and each label sits above its own control.
  //
  // It holds the whole form rather than one line of it. A label and its control
  // are two children of the same grid, not a box around the pair, because a box
  // per line would draw a border where a column already says the same thing.
  // Five forms on the settings page had written the grid out by hand.
  //
  // The one column needs a rhythm the two columns do not. Side by side, a label
  // and its own control are on the same line and the pairing is drawn by the
  // column; stacked, the only thing left saying which box a label belongs to is
  // the space around it. With one gap for every row the slskd form was three
  // labels and three boxes evenly spaced, and nothing said which was whose.
  //
  // So below the small breakpoint the gap inside a pair is 6px and the gap
  // between one pair and the next is 18px. It is written as a margin on every
  // label after the first, and the labels are the odd children: this grid is
  // filled a cell at a time, label then control, and a form with no label for a
  // row passes an empty cell rather than skipping one. Both are undone at the
  // small breakpoint, where the rows are pairs again.
  //
  // The one column is named rather than left to itself. A column nobody sizes
  // is sized to the longest thing in it, and one hint here — "best first —
  // anything not listed keeps its usual place below" — is longer than a phone
  // is wide, so on a phone the settings page could be pushed sideways and the
  // right-hand end of every card lay off the screen. Naming it `minmax(0,1fr)`
  // makes the column the width of the form and the sentence wrap inside it.

  let { children, ...props }: HTMLFormAttributes = $props();
</script>

<form
  class="grid grid-cols-[minmax(0,1fr)] items-center gap-x-5 gap-y-1.5 [&>*:nth-child(2n+3)]:mt-3 sm:gap-y-2.5 sm:grid-cols-[180px_minmax(0,1fr)] sm:[&>*:nth-child(2n+3)]:mt-0"
  {...props}
>
  {@render children?.()}
</form>
