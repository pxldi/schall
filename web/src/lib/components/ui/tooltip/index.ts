import { Tooltip as TooltipPrimitive } from 'bits-ui';

import Content from './TooltipContent.svelte';

// `Provider` holds the delay every tooltip under it waits before it opens, and
// the shorter grace period that lets a reader move along a row of controls
// without waiting again for each one.
const Provider = TooltipPrimitive.Provider;
const Root = TooltipPrimitive.Root;
const Trigger = TooltipPrimitive.Trigger;

export {
  Provider,
  Root,
  Trigger,
  Content,
  //
  Provider as TooltipProvider,
  Root as Tooltip,
  Trigger as TooltipTrigger,
  Content as TooltipContent
};
