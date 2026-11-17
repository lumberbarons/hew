// Starts the daemon for the end-to-end specs: builds it (with the UI already
// built into ../dist), makes an empty git checkout, and serves a local tracker
// in it. Everything lives in a temp directory that is removed on exit.
import { execFileSync, spawn } from 'node:child_process';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';

const daemonDir = resolve(import.meta.dirname, '..', '..');
const work = mkdtempSync(join(tmpdir(), 'hew-e2e-'));
const repo = join(work, 'repo');
const bin = join(work, 'hewd');

// Isolate from the developer's git configuration and environment.
const env = { ...process.env, GIT_CONFIG_GLOBAL: '/dev/null', GIT_CONFIG_SYSTEM: '/dev/null' };
for (const k of Object.keys(env))
  if (k.startsWith('GIT_') && !k.startsWith('GIT_CONFIG_')) delete env[k];

execFileSync('git', ['init', '--initial-branch=main', repo], { env, stdio: 'ignore' });
execFileSync('go', ['build', '-o', bin, './cmd/hewd'], { cwd: daemonDir, stdio: 'inherit' });

const child = spawn(
  bin,
  ['--dir', repo, '--init-local', '--addr', `127.0.0.1:${process.env.PORT ?? '17777'}`],
  { env, stdio: 'inherit' },
);

const cleanup = () => rmSync(work, { recursive: true, force: true });
const stop = () => {
  child.kill('SIGTERM');
};
process.on('SIGTERM', stop);
process.on('SIGINT', stop);
child.on('exit', (code) => {
  cleanup();
  process.exit(code ?? 0);
});
