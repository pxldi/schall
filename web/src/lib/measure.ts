// Reports an element's size whenever it changes, for charts drawn in screen
// pixels. The test window has no ResizeObserver; there the size is read once
// and the chart falls back to its plain size.
export function measure(node: HTMLElement, onsize: (width: number, height: number) => void) {
  let report = onsize;
  const update = () => report(node.clientWidth, node.clientHeight);
  const observer = typeof ResizeObserver === 'undefined' ? null : new ResizeObserver(update);
  observer?.observe(node);
  update();
  return {
    update(next: (width: number, height: number) => void) {
      report = next;
    },
    destroy() {
      observer?.disconnect();
    }
  };
}
