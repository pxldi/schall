// `expo prebuild` regenerates android/ and forgets the gradle.properties it
// had. This runs prebuild and writes the limits back. The limits exist
// because the stock build (Kotlin daemon, four ABIs, gradle's 4 GiB heap)
// exceeds the 6 GiB the build box has and takes everything down with it.
import { spawnSync } from 'node:child_process';
import { readFileSync, writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = join(dirname(fileURLToPath(import.meta.url)), '..');
const result = spawnSync('npx', ['expo', 'prebuild', '--platform', 'android', '--no-install'], {
  cwd: root,
  stdio: 'inherit'
});
if (result.status !== 0) process.exit(result.status ?? 1);

const limits = {
  'org.gradle.jvmargs': '-Xmx2g -XX:MaxMetaspaceSize=512m',
  'kotlin.daemon.jvmargs': '-Xmx1g',
  'kotlin.compiler.execution.strategy': 'in-process',
  'org.gradle.workers.max': '2',
  'org.gradle.daemon': 'false',
  'org.gradle.parallel': 'false',
  // The owner's phone is arm64. One ABI is a quarter of the native work.
  reactNativeArchitectures: 'arm64-v8a'
};
const file = join(root, 'android', 'gradle.properties');
let text = readFileSync(file, 'utf8');
for (const [key, value] of Object.entries(limits)) {
  const line = `${key}=${value}`;
  const pattern = new RegExp(`^${key.replace(/\./g, '\\.')}=.*$`, 'm');
  text = pattern.test(text) ? text.replace(pattern, line) : `${text.trimEnd()}\n${line}\n`;
}
writeFileSync(file, text);
console.log('gradle.properties: memory limits applied');
