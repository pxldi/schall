import { type ReactNode } from 'react';
import {
  ActivityIndicator,
  Pressable,
  StyleSheet,
  Text,
  View,
  type PressableProps,
  type StyleProp,
  type TextStyle,
  type ViewStyle
} from 'react-native';
import { theme, type, space } from '@/design/theme';

type Tone = 'ink' | 'ink2' | 'ink3' | 'ok' | 'fail' | 'decide' | 'busy' | 'idle' | 'done' | 'accent';

export function T({
  kind = 'body',
  tone = 'ink',
  style,
  children,
  numberOfLines
}: {
  kind?: keyof typeof type;
  tone?: Tone;
  style?: StyleProp<TextStyle>;
  children: ReactNode;
  numberOfLines?: number;
}) {
  return (
    <Text style={[type[kind], { color: theme[tone] }, style]} numberOfLines={numberOfLines}>
      {children}
    </Text>
  );
}

/** A fragment of state on a row: the verb-less word the boards draw in the
 * state colour. */
export function Tag({ tone = 'idle', children }: { tone?: Tone; children: ReactNode }) {
  return (
    <View style={[styles.tag, { borderColor: theme[tone] }]}>
      <T kind="micro" tone={tone}>
        {children}
      </T>
    </View>
  );
}

export function Button({
  variant = 'outline',
  tone,
  busy,
  children,
  style,
  ...rest
}: PressableProps & {
  variant?: 'solid' | 'outline' | 'quiet';
  tone?: 'fail';
  busy?: boolean;
  children: ReactNode;
  style?: StyleProp<ViewStyle>;
}) {
  const disabled = rest.disabled || busy;
  const border = tone === 'fail' ? theme.fail : variant === 'solid' ? theme.accent : theme.lineStrong;
  const ink = variant === 'solid' ? theme.accentInk : tone === 'fail' ? theme.fail : theme.ink;
  return (
    <Pressable
      {...rest}
      disabled={disabled}
      accessibilityRole="button"
      style={({ pressed }) => [
        styles.button,
        variant === 'solid' && { backgroundColor: theme.accent },
        variant !== 'quiet' && { borderColor: border, borderWidth: 1 },
        (pressed || disabled) && { opacity: 0.55 },
        style
      ]}
    >
      {busy ? <ActivityIndicator color={ink} /> : <Text style={[type.meta, styles.buttonText, { color: ink }]}>{children}</Text>}
    </Pressable>
  );
}

/** One row of a list: a title line, a second line, and what to do about it.
 * Rules between rows, never borders around them. */
export function Row({ children, onPress, style }: { children: ReactNode; onPress?: () => void; style?: StyleProp<ViewStyle> }) {
  const body = <View style={[styles.row, style]}>{children}</View>;
  if (!onPress) return body;
  return (
    <Pressable onPress={onPress} style={({ pressed }) => pressed && { backgroundColor: theme.surface }}>
      {body}
    </Pressable>
  );
}

export function Actions({ children }: { children: ReactNode }) {
  return <View style={styles.actions}>{children}</View>;
}

/** The place a list's error or emptiness is said, in one sentence. */
export function Notice({ tone = 'ink2', children }: { tone?: Tone; children: ReactNode }) {
  return (
    <View style={styles.notice}>
      <T kind="meta" tone={tone}>
        {children}
      </T>
    </View>
  );
}

export function Loading() {
  return (
    <View style={styles.notice}>
      <ActivityIndicator color={theme.ink3} />
    </View>
  );
}

/** The failure of one call, read from whatever was thrown. */
export function errorText(error: unknown): string {
  if (error instanceof Error) return error.message;
  return 'Something failed.';
}

/** The choice between piles at the top of a list. */
export function Segments<K extends string>({
  options,
  value,
  onChange
}: {
  options: { key: K; label: string; count?: number }[];
  value: K;
  onChange: (key: K) => void;
}) {
  return (
    <View style={styles.segments}>
      {options.map((option) => {
        const active = option.key === value;
        return (
          <Pressable
            key={option.key}
            onPress={() => onChange(option.key)}
            accessibilityRole="tab"
            accessibilityState={{ selected: active }}
            style={[styles.segment, active && styles.segmentActive]}
          >
            <T kind="meta" tone={active ? 'ink' : 'ink3'}>
              {option.label}
              {option.count !== undefined ? ` ${option.count}` : ''}
            </T>
          </Pressable>
        );
      })}
    </View>
  );
}

export function formatDuration(ms: number | null | undefined): string {
  if (!ms) return '–:––';
  const total = Math.round(ms / 1000);
  return `${Math.floor(total / 60)}:${String(total % 60).padStart(2, '0')}`;
}

export function formatBytes(bytes: number | undefined): string {
  if (!bytes) return '';
  if (bytes >= 1e9) return `${(bytes / 1e9).toFixed(1)} GB`;
  if (bytes >= 1e6) return `${(bytes / 1e6).toFixed(1)} MB`;
  return `${Math.round(bytes / 1e3)} kB`;
}

const styles = StyleSheet.create({
  tag: {
    borderWidth: 1,
    borderRadius: 3,
    paddingHorizontal: 6,
    paddingVertical: 2,
    alignSelf: 'flex-start'
  },
  button: {
    minHeight: 40,
    paddingHorizontal: space.l,
    borderRadius: 6,
    alignItems: 'center',
    justifyContent: 'center'
  },
  buttonText: { fontWeight: '600' },
  row: {
    paddingHorizontal: space.l,
    paddingVertical: space.m,
    borderBottomWidth: StyleSheet.hairlineWidth,
    borderBottomColor: theme.line,
    gap: space.xs
  },
  actions: { flexDirection: 'row', flexWrap: 'wrap', gap: space.s, marginTop: space.s },
  notice: { padding: space.l, alignItems: 'center' },
  segments: {
    flexDirection: 'row',
    borderBottomWidth: StyleSheet.hairlineWidth,
    borderBottomColor: theme.line,
    paddingHorizontal: space.s
  },
  segment: { paddingHorizontal: space.m, paddingVertical: space.m, borderBottomWidth: 2, borderBottomColor: 'transparent' },
  segmentActive: { borderBottomColor: theme.accent }
});
