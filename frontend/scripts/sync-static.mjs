import { copyFileSync, existsSync, mkdirSync, readdirSync, rmSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = dirname(fileURLToPath(import.meta.url));
const frontendRoot = resolve(__dirname, '..');
const outDir = resolve(frontendRoot, 'out');
const targetDir = resolve(frontendRoot, '..', 'static', 'admin');

// Walk manually instead of using fs.cpSync: on Node 24 / Windows a recursive
// cpSync of this tree kills the process (exit 127) without a catchable error.
function copyDir(src, dst) {
  mkdirSync(dst, { recursive: true });
  for (const entry of readdirSync(src, { withFileTypes: true })) {
    const from = join(src, entry.name);
    const to = join(dst, entry.name);
    if (entry.isDirectory()) {
      copyDir(from, to);
    } else {
      copyFileSync(from, to);
    }
  }
}

if (!existsSync(outDir)) {
  throw new Error(`Next export output not found: ${outDir}`);
}

rmSync(targetDir, { recursive: true, force: true });
copyDir(outDir, targetDir);
console.log(`Synced static admin build to ${targetDir}`);
