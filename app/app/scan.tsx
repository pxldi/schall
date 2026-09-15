import { useRef, useState } from 'react';
import { StyleSheet, View } from 'react-native';
import { CameraView, useCameraPermissions, type BarcodeScanningResult } from 'expo-camera';
import { useRouter } from 'expo-router';
import { theme, space } from '@/design/theme';
import { Button, T } from '@/ui/primitives';

/** Reads the QR code Settings → Phone draws: `{"server":"…","token":"…"}`.
 * The first code that parses ends the scan; anything else is ignored and the
 * camera keeps looking. */
export default function Scan() {
  const router = useRouter();
  const [permission, requestPermission] = useCameraPermissions();
  const [rejected, setRejected] = useState(false);
  const done = useRef(false);

  function onScan(result: BarcodeScanningResult) {
    if (done.current) return;
    try {
      const parsed = JSON.parse(result.data) as { server?: unknown; token?: unknown };
      if (typeof parsed.server !== 'string' || typeof parsed.token !== 'string') throw new Error('not ours');
      done.current = true;
      router.replace({ pathname: '/sign-in', params: { server: parsed.server, token: parsed.token } });
    } catch {
      setRejected(true);
    }
  }

  if (!permission) return <View style={styles.screen} />;
  if (!permission.granted) {
    return (
      <View style={[styles.screen, styles.ask]}>
        <T kind="body" tone="ink2">
          Schall needs the camera to read the code.
        </T>
        <Button variant="solid" onPress={requestPermission}>
          Allow camera
        </Button>
      </View>
    );
  }

  return (
    <View style={styles.screen}>
      <CameraView style={styles.camera} barcodeScannerSettings={{ barcodeTypes: ['qr'] }} onBarcodeScanned={onScan} />
      <View style={styles.hint}>
        <T kind="meta" tone={rejected ? 'fail' : 'ink2'}>
          {rejected ? 'That code is not a Schall phone code.' : 'Point at the code under Settings → Phone.'}
        </T>
      </View>
    </View>
  );
}

const styles = StyleSheet.create({
  screen: { flex: 1, backgroundColor: theme.ground },
  ask: { padding: space.xl, gap: space.l, justifyContent: 'center' },
  camera: { flex: 1 },
  hint: { padding: space.l, alignItems: 'center' }
});
