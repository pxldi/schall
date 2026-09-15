// The application never runs in Node, so the project has no Node types — the
// same reasoning vite.config.ts states for its one `process` declaration.
// `errors.sentinels.test.ts` reads the Go source off the disk to check that
// every sentinel the interface writes a sentence for is still in it, and it is
// the only file here that touches a file system.
//
// Declared rather than installed: three functions with the shapes this one test
// uses is a smaller thing to keep true than the whole of @types/node.

declare module 'node:fs' {
  interface Dirent {
    name: string;
    isDirectory(): boolean;
  }
  export function readdirSync(
    path: string,
    options: { withFileTypes: true }
  ): Dirent[];
  export function readFileSync(path: string, encoding: 'utf8'): string;
}

declare module 'node:path' {
  export function join(...parts: string[]): string;
  export function dirname(path: string): string;
}

declare module 'node:url' {
  export function fileURLToPath(url: string): string;
}
