import { Select as SelectPrimitive } from 'bits-ui';

import Content from './SelectContent.svelte';
import GroupHeading from './SelectGroupHeading.svelte';
import Item from './SelectItem.svelte';
import Trigger from './SelectTrigger.svelte';

// `Root` holds the value and the open state. It is where `type="single"` or
// `type="multiple"` is said, and where `items` is handed in — that list is what
// lets a reader type a letter while the list is shut and land on a choice, the
// way a native `<select>` does.
const Root = SelectPrimitive.Root;
const Group = SelectPrimitive.Group;

export {
  Root,
  Trigger,
  Content,
  Item,
  Group,
  GroupHeading,
  //
  Root as Select,
  Trigger as SelectTrigger,
  Content as SelectContent,
  Item as SelectItem,
  Group as SelectGroup,
  GroupHeading as SelectGroupHeading
};
