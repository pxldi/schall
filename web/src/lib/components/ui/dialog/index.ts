import { Dialog as DialogPrimitive } from 'bits-ui';

import Body from './DialogBody.svelte';
import Content from './DialogContent.svelte';
import Description from './DialogDescription.svelte';
import Footer from './DialogFooter.svelte';
import Header from './DialogHeader.svelte';
import Overlay from './DialogOverlay.svelte';
import Title from './DialogTitle.svelte';

// The parts Schall does not restyle come straight from Bits UI. `Root` holds
// the open state, `Trigger` is whatever opens the panel, and `Close` is
// anything inside it that shuts it — a footer button as often as the corner
// control.
const Root = DialogPrimitive.Root;
const Trigger = DialogPrimitive.Trigger;
const Close = DialogPrimitive.Close;

export {
  Root,
  Trigger,
  Close,
  Overlay,
  Content,
  Header,
  Body,
  Footer,
  Title,
  Description,
  //
  Root as Dialog,
  Trigger as DialogTrigger,
  Close as DialogClose,
  Overlay as DialogOverlay,
  Content as DialogContent,
  Header as DialogHeader,
  Body as DialogBody,
  Footer as DialogFooter,
  Title as DialogTitle,
  Description as DialogDescription
};
