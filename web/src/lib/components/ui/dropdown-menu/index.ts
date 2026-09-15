import { DropdownMenu as MenuPrimitive } from 'bits-ui';

import CheckboxItem from './DropdownMenuCheckboxItem.svelte';
import Content from './DropdownMenuContent.svelte';
import GroupHeading from './DropdownMenuGroupHeading.svelte';
import Item from './DropdownMenuItem.svelte';
import Separator from './DropdownMenuSeparator.svelte';
import SubContent from './DropdownMenuSubContent.svelte';
import SubTrigger from './DropdownMenuSubTrigger.svelte';

// The parts that carry no drawing of their own come straight from Bits UI.
// `Group` ties a heading to the items it introduces, and `Sub` holds the open
// state of one nested list.
const Root = MenuPrimitive.Root;
const Trigger = MenuPrimitive.Trigger;
const Group = MenuPrimitive.Group;
const Sub = MenuPrimitive.Sub;

export {
  Root,
  Trigger,
  Group,
  GroupHeading,
  Content,
  Item,
  CheckboxItem,
  Separator,
  Sub,
  SubTrigger,
  SubContent,
  //
  Root as DropdownMenu,
  Trigger as DropdownMenuTrigger,
  Group as DropdownMenuGroup,
  GroupHeading as DropdownMenuGroupHeading,
  Content as DropdownMenuContent,
  Item as DropdownMenuItem,
  CheckboxItem as DropdownMenuCheckboxItem,
  Separator as DropdownMenuSeparator,
  Sub as DropdownMenuSub,
  SubTrigger as DropdownMenuSubTrigger,
  SubContent as DropdownMenuSubContent
};
