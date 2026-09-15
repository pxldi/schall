import { Tabs } from 'expo-router';
import { Text } from 'react-native';
import { theme, type } from '@/design/theme';

/** Five tabs, the "decide and watch" set. No icon set is loaded yet: the label
 * is the tab, in the meta size, and the accent underlines the active one. */
function Label({ children, focused }: { children: string; focused: boolean }) {
  return <Text style={[type.meta, { color: focused ? theme.ink : theme.ink3, fontWeight: focused ? '600' : '400' }]}>{children}</Text>;
}

export default function TabLayout() {
  return (
    <Tabs
      screenOptions={{
        headerStyle: { backgroundColor: theme.ground },
        headerTintColor: theme.ink,
        headerShadowVisible: false,
        headerTitleStyle: type.lead,
        sceneStyle: { backgroundColor: theme.ground },
        tabBarStyle: { backgroundColor: theme.ground, borderTopColor: theme.line, height: 56 },
        tabBarShowLabel: true,
        tabBarIcon: () => null,
        tabBarIconStyle: { display: 'none' },
        tabBarLabelPosition: 'beside-icon',
        tabBarLabelStyle: { ...type.meta }
      }}
    >
      <Tabs.Screen name="index" options={{ title: 'Review', tabBarLabel: ({ focused }) => <Label focused={focused}>Review</Label> }} />
      <Tabs.Screen name="downloads" options={{ title: 'Downloads', tabBarLabel: ({ focused }) => <Label focused={focused}>Downloads</Label> }} />
      <Tabs.Screen name="wants" options={{ title: 'Wants', tabBarLabel: ({ focused }) => <Label focused={focused}>Wants</Label> }} />
      <Tabs.Screen name="search" options={{ title: 'Search', tabBarLabel: ({ focused }) => <Label focused={focused}>Search</Label> }} />
      <Tabs.Screen name="settings" options={{ title: 'Settings', tabBarLabel: ({ focused }) => <Label focused={focused}>Settings</Label> }} />
    </Tabs>
  );
}
