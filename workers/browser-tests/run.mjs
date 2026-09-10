import { spawn } from 'node:child_process';
import { mkdir, readFile, rm, writeFile } from 'node:fs/promises';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import ts from 'typescript';

const root = fileURLToPath(new URL('../', import.meta.url));
const state = resolve(root, 'browser-tests/.state');
const wrangler = resolve(root, 'node_modules/wrangler/bin/wrangler.js');
const env = { ...process.env, CI: 'true', WRANGLER_SEND_METRICS: 'false', CLOUDFLARE_LOAD_DEV_VARS_FROM_DOT_ENV: 'false' };
let child;
let stopping = false;
for (const signal of ['SIGINT', 'SIGTERM']) {
    process.on(signal, () => {
        stopping = true;
        child?.kill(signal);
    });
}

async function run(args) {
    if (stopping) return;
    child = spawn(process.execPath, [wrangler, ...args], { cwd: root, env, stdio: 'inherit' });
    const code = await new Promise((resolve, reject) => {
        child.once('error', reject);
        child.once('exit', (code, signal) => resolve(signal && stopping ? 0 : (code ?? 1)));
    });
    child = undefined;
    if (code && !stopping) throw new Error(`Wrangler exited with status ${code}`);
}

async function config(path) {
    const parsed = ts.parseConfigFileTextToJson(path, await readFile(path, 'utf8'));
    if (parsed.error) throw new Error(`Invalid JSONC: ${path}`);
    return parsed.config;
}

// Wrangler JSONC has no native inheritance. Materialize the test overlay only in
// disposable state, retaining the real bindings and rebasing filesystem paths.
const overlayPath = resolve(root, 'browser-tests/wrangler.jsonc');
const { extends: basePath, ...overlay } = await config(overlayPath);
const base = await config(resolve(dirname(overlayPath), basePath));
const merged = {
    ...base,
    ...overlay,
    main: resolve(dirname(overlayPath), overlay.main),
    assets: { ...base.assets, directory: resolve(root, base.assets.directory) },
    d1_databases: overlay.d1_databases.map((db) => ({ ...db, migrations_dir: resolve(dirname(overlayPath), db.migrations_dir) })),
};
await rm(state, { recursive: true, force: true });
await mkdir(state, { recursive: true });
const generated = resolve(state, 'wrangler.json');
await writeFile(generated, JSON.stringify(merged, null, 2));
// Keep real local .dev.vars secrets out of this deliberately fake environment.
await writeFile(resolve(state, '.dev.vars'), '');
const common = ['--config', generated, '--local', '--persist-to', state];
await run(['d1', 'migrations', 'apply', 'conway-browser-tests', ...common]);
await run(['dev', ...common, '--ip', '127.0.0.1', '--port', '8799', '--inspector-port', '0']);
