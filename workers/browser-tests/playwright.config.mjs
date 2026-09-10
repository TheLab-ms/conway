import { defineConfig } from '@playwright/test';
import { fileURLToPath } from 'node:url';

export default defineConfig({
    testDir: '.',
    testMatch: '*.spec.mjs',
    fullyParallel: false,
    workers: 1,
    retries: 0,
    timeout: 60_000,
    expect: { timeout: 8_000 },
    outputDir: './test-results',
    reporter: [['list']],
    use: {
        baseURL: 'http://127.0.0.1:8799',
        browserName: 'chromium',
        trace: 'off',
        screenshot: 'only-on-failure',
        launchOptions: process.env.CHROME ? { executablePath: process.env.CHROME } : {},
    },
    webServer: {
        command: 'node browser-tests/run.mjs',
        cwd: fileURLToPath(new URL('../', import.meta.url)),
        url: 'http://127.0.0.1:8799/api/config',
        reuseExistingServer: false,
        timeout: 120_000,
        gracefulShutdown: { signal: 'SIGTERM', timeout: 5_000 },
    },
});
