import { fileURLToPath } from 'node:url';
import { cloudflareTest, readD1Migrations } from '@cloudflare/vitest-pool-workers';
import { defineConfig } from 'vitest/config';

export default defineConfig(async () => {
    const migrations = await readD1Migrations(fileURLToPath(new URL('./migrations', import.meta.url)));
    return {
        plugins: [
            cloudflareTest({
                wrangler: { configPath: './wrangler.jsonc' },
                miniflare: {
                    compatibilityDate: '2026-08-15',
                    compatibilityFlags: ['nodejs_compat'],
                    d1Databases: ['DB'],
                    bindings: {
                        SITE_URL: 'https://members.example.test',
                        AUTOMATION_ENABLED: 'false',
                        KIOSK_IPS: '192.0.2.10',
                        DISCORD_CLIENT_ID: 'test-client',
                        DISCORD_CLIENT_SECRET: 'test-secret',
                        TEST_MIGRATIONS: migrations,
                    },
                },
            }),
        ],
        test: {
            include: ['test/**/*.test.ts'],
            setupFiles: ['./test/setup.ts'],
            fileParallelism: false,
        },
    };
});
