import type { Env } from './types';
import { csrf, HttpError, json } from './http';
import { authRoute } from './auth';
import { coreRoute } from './core';
import { integrationRoute, processJob, runJobs, scheduled } from './integrations';
export { MembershipCoordinator } from './integrations';

async function route(request: Request, env: Env): Promise<Response> {
  const path = new URL(request.url).pathname;
  if (path.startsWith('/api/') && !['GET', 'HEAD', 'OPTIONS'].includes(request.method)) await csrf(request, env);
  const response = await authRoute(request, env) || await integrationRoute(request, env) || await coreRoute(request, env);
  if (response) return response;
  if (path.startsWith('/api/') || path.startsWith('/webhooks/') || path.startsWith('/discord/') || path.startsWith('/login/') || path.startsWith('/oauth2/')) throw new HttpError(404, 'Not found');
  if (!['GET', 'HEAD'].includes(request.method)) throw new HttpError(405, 'Method not allowed');
  return env.ASSETS.fetch(request);
}

export default {
  async fetch(request: Request, env: Env, ctx: ExecutionContext): Promise<Response> {
    let response: Response;
    try {
      response = await route(request, env);
      if (request.method !== 'GET' && request.method !== 'HEAD' && response.ok) ctx.waitUntil(runJobs(env));
    } catch (error) {
      if (error instanceof HttpError) response = json({ error: error.message }, error.status);
      else if (error instanceof Error && /constraint failed|Family root|Cannot remove last linked leader/i.test(error.message)) {
        response = json({ error: 'Conflicting member identity, fob, family relationship, or last leadership account. Review the record before retrying.' }, 409);
      } else {
        console.error('Request failed', error instanceof Error ? error.name : 'UnknownError');
        response = json({ error: 'The operation could not be completed. Please try again.' }, 500);
      }
      if (request.method === 'GET' && new URL(request.url).pathname.startsWith('/login/')) {
        const escape = (value: string) => value.replace(/[&<>"']/g, character => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[character]!);
        const message = error instanceof HttpError ? error.message : 'Discord sign-in could not be completed. Please try again.';
        response = new Response(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Sign-in unavailable | Conway</title><link rel="stylesheet" href="/styles.css"></head><body><main class="panel"><h1>Sign-in unavailable</h1><p>${escape(message)}</p><a href="/login">Return to sign in</a></main></body></html>`, { status: response.status, headers: { 'Content-Type': 'text/html; charset=utf-8', 'Cache-Control': 'no-store' } });
      }
    }
    const headers = new Headers(response.headers);
    headers.set('X-Content-Type-Options', 'nosniff');
    headers.set('Referrer-Policy', 'no-referrer');
    headers.set('Content-Security-Policy', "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; font-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'");
    headers.set('Permissions-Policy', 'camera=(), microphone=(), geolocation=()');
    if (new URL(request.url).protocol === 'https:') headers.set('Strict-Transport-Security', 'max-age=31536000');
    return new Response(response.body, { status: response.status, statusText: response.statusText, headers });
  },
  async queue(batch: MessageBatch<unknown>, env: Env): Promise<void> {
    for (const message of batch.messages) {
      const value = message.body as { id?: unknown } | null;
      if (!value || typeof value.id !== 'number' || !Number.isSafeInteger(value.id) || value.id < 1) { message.ack(); continue; }
      try { await processJob(env, value.id); message.ack(); }
      catch { message.retry(); }
    }
  },
  async scheduled(_event: ScheduledController, env: Env): Promise<void> { await scheduled(env); },
} satisfies ExportedHandler<Env>;
