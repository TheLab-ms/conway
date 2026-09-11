import { createScheduledController } from 'cloudflare:test';
import { env } from 'cloudflare:workers';
import { expect, it, vi } from 'vitest';
import { now } from '../src/http';
import worker from '../src/index';
import { login, member } from './fixtures';

it('scheduled cleanup removes expired D1 auth/claim/rate-limit records without external effects when automation is disabled', async () => {
    await member();
    await login(null, now() - 1);
    await login(null, now() + 3600);
    for (const [key, expires] of [
        ['expired', now() - 1],
        ['live', now() + 3600],
    ] as const) {
        await env.DB.batch([
            env.DB.prepare('INSERT INTO oauth_states(state_hash,browser_hash,return_to,expires) VALUES(?,?,?,?)').bind(key, key, '/billing', expires),
            env.DB.prepare('INSERT INTO enrollment_claims(token_hash,fob_id,created,expires) VALUES(?,42,?,?)').bind(key, now(), expires),
            env.DB.prepare('INSERT INTO rate_limits(key,count,expires) VALUES(?,1,?)').bind(key, expires),
        ]);
    }
    const external = vi.spyOn(globalThis, 'fetch').mockImplementation(async () => {
        throw new Error('Unexpected external request');
    });
    try {
        await worker.scheduled(createScheduledController(), env);
        for (const table of ['sessions', 'oauth_states', 'enrollment_claims', 'rate_limits']) {
            expect(await env.DB.prepare(`SELECT count(*) n FROM ${table}`).first('n')).toBe(1);
            expect(await env.DB.prepare(`SELECT min(expires) expires FROM ${table}`).first<number>('expires')).toBeGreaterThan(now());
        }
        expect(await env.DB.prepare("SELECT count(*) n FROM jobs WHERE status='pending'").first('n')).toBe(2);
        expect(external).not.toHaveBeenCalled();
    } finally {
        external.mockRestore();
    }
});
