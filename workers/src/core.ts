import type { Env, Member, Settings } from './types';
import { body, hash, HttpError, json, memberView, now, randomToken, requireLeader, requireMember, settings } from './http';
import { mutateMember, mutateDiscount } from './integrations';

type Input = Record<string, unknown>;
function text(value: unknown, key: string, max = 200, empty = true): string {
    if (typeof value !== 'string' || value.length > max || (!empty && !value.trim())) throw new HttpError(400, `Invalid ${key}`);
    return value.trim();
}
function integer(value: unknown, key: string, max = Number.MAX_SAFE_INTEGER): number {
    if (!Number.isSafeInteger(value) || Number(value) < 1 || Number(value) > max) throw new HttpError(400, `Invalid ${key}`);
    return Number(value);
}
function flag(value: unknown, key: string): number {
    if (![0, 1, true, false].includes(value as number)) throw new HttpError(400, `Invalid ${key}`);
    return value ? 1 : 0;
}
function offset(url: URL): number {
    const value = url.searchParams.get('offset') || '0';
    if (!/^\d{1,8}$/.test(value)) throw new HttpError(400, 'Invalid pagination offset');
    return Number(value);
}
function kiosk(request: Request, env: Env): void {
    const ip = request.headers.get('CF-Connecting-IP');
    if (
        !ip ||
        !env.KIOSK_IPS?.split(',')
            .map((s) => s.trim())
            .filter(Boolean)
            .includes(ip)
    ) {
        throw new HttpError(403, 'Fob enrollment is only available at the makerspace kiosk');
    }
}
async function audit(env: Env, actor: number, member: number | null, event: string, details: unknown): Promise<void> {
    await env.DB.prepare('INSERT INTO member_events(actor,member,event,details) VALUES(?,?,?,?)').bind(actor, member, event, JSON.stringify(details)).run();
}

function editable(input: Input, admin: boolean): Record<string, string | number | null> {
    const fields: Record<string, string | number | null> = {};
    const strings: Record<string, number> = {
        name: 200,
        ...(admin ? { email: 254, admin_notes: 10000 } : {}),
    };
    const flags = admin ? ['leadership', 'non_billable', 'confirmed', 'bill_annually'] : [];
    for (const [key, value] of Object.entries(input)) {
        if (key in strings) fields[key] = text(value, key, strings[key], key !== 'email');
        else if (flags.includes(key)) fields[key] = flag(value, key);
        else if (admin && ['name_override', 'discord_user_id', 'discount_type', 'discount_status'].includes(key)) {
            fields[key] = value === null || value === '' ? null : text(value, key, 200);
        } else if (admin && ['fob_id', 'root_family_member'].includes(key)) {
            fields[key] = value === null ? null : integer(value, key, key === 'fob_id' ? 0xffffffff : Number.MAX_SAFE_INTEGER);
        } else throw new HttpError(400, `Field cannot be edited: ${key}`);
    }
    if (typeof fields.email === 'string') {
        fields.email = fields.email.toLowerCase();
        if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(fields.email)) throw new HttpError(400, 'Invalid contact email');
    }
    if (fields.discord_user_id && !/^\d{5,25}$/.test(String(fields.discord_user_id))) throw new HttpError(400, 'Invalid Discord ID');
    if (fields.discount_status && !['requested', 'approved'].includes(String(fields.discount_status))) throw new HttpError(400, 'Invalid discount status');
    return fields;
}

async function validateDiscount(env: Env, fields: Record<string, string | number | null>, previous?: Member): Promise<void> {
    const type = fields.discount_type === undefined ? previous?.discount_type : fields.discount_type;
    const status = fields.discount_status === undefined ? previous?.discount_status : fields.discount_status;
    if (fields.discount_type && fields.discount_type !== previous?.discount_type && !(await settings(env)).discounts.some((d) => d.id === fields.discount_type))
        throw new HttpError(400, 'Unknown discount type');
    if (!type && status) throw new HttpError(400, 'A discount type is required');
    if (fields.discount_type === null) {
        fields.discount_status = null;
        fields.discount_request_id = null;
    } else if (status === 'requested' && (previous?.discount_status !== 'requested' || previous.discount_type !== type)) fields.discount_request_id = randomToken();
}

function validateSettings(input: Input, previous: Settings): Settings {
    const result = { ...previous };
    const strings = ['site_name', 'monthly_price_id', 'yearly_price_id'] as const;
    const allowed = new Set<string>([...strings, 'discounts', 'version']);
    for (const key of Object.keys(input)) if (!allowed.has(key)) throw new HttpError(400, `Unknown setting: ${key}`);
    for (const key of strings)
        if (key in input) {
            result[key] = text(input[key], key, 200, key !== 'site_name');
            if (key.endsWith('price_id') && result[key] && !/^price_[A-Za-z0-9_]+$/.test(result[key])) throw new HttpError(400, `Invalid ${key}`);
        }
    if ('discounts' in input) {
        if (!Array.isArray(input.discounts) || input.discounts.length > 50) throw new HttpError(400, 'Invalid discounts');
        result.discounts = input.discounts.map((v: Input) => {
            if (!v || typeof v !== 'object') throw new HttpError(400, 'Invalid discount');
            const id = text(v.id, 'discount ID', 50, false),
                label = text(v.label, 'discount label', 200, false),
                coupon_id = text(v.coupon_id ?? '', 'coupon ID', 100);
            if (!/^[A-Za-z0-9_-]+$/.test(id) || (coupon_id && !/^[A-Za-z0-9_-]+$/.test(coupon_id))) throw new HttpError(400, 'Invalid discount or coupon ID');
            return { id, label, coupon_id };
        });
        if (new Set(result.discounts.map((v) => v.id)).size !== result.discounts.length) throw new HttpError(400, 'Duplicate discounts');
    }
    return result;
}

export async function coreRoute(request: Request, env: Env): Promise<Response | null> {
    const url = new URL(request.url),
        path = url.pathname,
        method = request.method;
    if (path === '/api/config' && method === 'GET') {
        const cfg = await settings(env);
        const waiver = await env.DB.prepare('SELECT version,content,agreements FROM waiver_versions WHERE version=?')
            .bind(cfg.waiver_version)
            .first<{ version: number; content: string; agreements: string }>();
        return json({
            site_name: cfg.site_name,
            discounts: cfg.discounts.map(({ id, label }) => ({ id, label })),
            waiver: waiver ? { ...waiver, agreements: JSON.parse(waiver.agreements) } : null,
            stripe_enabled: Boolean(env.STRIPE_SECRET_KEY),
        });
    }
    if (path.startsWith('/api/kiosk/claims')) {
        kiosk(request, env);
        if (path === '/api/kiosk/claims' && method === 'POST') {
            const input = await body<Input>(request),
                fob = integer(input.fob_id, 'fob ID', 0xffffffff);
            const count = await env.DB.prepare('SELECT count(*) n FROM enrollment_claims WHERE created > ?')
                .bind(now() - 60)
                .first<{ n: number }>();
            if (count && count.n >= 30) throw new HttpError(429, 'Too many scans; wait a minute');
            const token = randomToken(),
                expires = now() + 300;
            await env.DB.prepare('INSERT INTO enrollment_claims(token_hash,fob_id,created,expires) VALUES(?,?,?,?)')
                .bind(await hash(token), fob, now(), expires)
                .run();
            return json({ token, expires, url: `${env.SITE_URL}/fobs/bind?token=${token}` }, 201);
        }
        const claimPath = /^\/api\/kiosk\/claims\/([a-f0-9]{64})$/.exec(path);
        if (claimPath && method === 'GET') {
            const claim = await env.DB.prepare('SELECT claimed_by,expires FROM enrollment_claims WHERE token_hash=?')
                .bind(await hash(claimPath[1]))
                .first<{ claimed_by: number | null; expires: number }>();
            if (!claim) throw new HttpError(404, 'Enrollment claim not found');
            return json({ claimed: claim.claimed_by !== null, expires: claim.expires });
        }
        throw new HttpError(404, 'Not found');
    }
    if (path.startsWith('/api/admin/')) return adminRoute(request, env);
    if (!['/api/member', '/api/profile', '/api/waiver', '/api/discount', '/api/fobs/bind'].includes(path)) return null;
    const member = await requireMember(request, env);
    if (path === '/api/member' && method === 'GET') return json(memberView(member));
    if (path === '/api/profile' && method === 'PATCH') {
        const fields = editable(await body<Input>(request), false),
            keys = Object.keys(fields);
        if (!keys.length) throw new HttpError(400, 'No fields supplied');
        const results = await env.DB.batch([
            env.DB.prepare(`UPDATE members SET ${keys.map((k) => `${k}=?`).join(',')} WHERE id=?`).bind(...keys.map((k) => fields[k]), member.id),
            env.DB.prepare('SELECT * FROM members WHERE id=?').bind(member.id),
        ]);
        return json(memberView(results[1].results[0] as unknown as Member));
    }
    if (path === '/api/waiver' && method === 'POST') {
        const input = await body<Input>(request),
            version = integer(input.version, 'waiver version'),
            name = text(input.name, 'signed name', 200, false);
        const current = await env.DB.prepare(
            "SELECT agreements FROM waiver_versions WHERE version=? AND version=(SELECT json_extract(data,'$.waiver_version') FROM settings WHERE id=1)",
        )
            .bind(version)
            .first<{ agreements: string }>();
        if (!current) throw new HttpError(409, 'Waiver changed; reload and read the current version');
        const agreements = JSON.parse(current.agreements) as string[];
        if (!Array.isArray(input.agreements) || input.agreements.length !== agreements.length || input.agreements.some((v) => v !== true))
            throw new HttpError(400, 'All waiver agreements are required');
        const result = await env.DB.prepare(
            `INSERT INTO waivers(version,name,email) SELECT ?,?,? WHERE ?=(SELECT json_extract(data,'$.waiver_version') FROM settings WHERE id=1) ON CONFLICT(email,version) DO NOTHING RETURNING id`,
        )
            .bind(version, name, member.email, version)
            .first<{ id: number }>();
        if (!result && !(await env.DB.prepare('SELECT id FROM waivers WHERE version=? AND email=?').bind(version, member.email).first()))
            throw new HttpError(409, 'Waiver changed; reload and read the current version');
        return json({ signed: true, version });
    }
    if (path === '/api/discount' && method === 'POST') {
        const input = await body<Input>(request),
            type = text(input.discount_type, 'discount type', 50, false);
        if (!(await settings(env)).discounts.some((d) => d.id === type)) throw new HttpError(400, 'Unknown discount');
        return mutateDiscount(env, member.id, type, member.version);
    }
    if (path === '/api/discount' && method === 'DELETE') {
        return mutateDiscount(env, member.id, null, member.version);
    }
    if (path === '/api/fobs/bind' && method === 'POST') {
        const input = await body<Input>(request);
        if (typeof input.token !== 'string' || !/^[a-f0-9]{64}$/.test(input.token)) throw new HttpError(400, 'Invalid enrollment claim');
        const tokenHash = await hash(input.token);
        // The claim and fob change commit together. A unique-fob failure rolls consumption back.
        const results = await env.DB.batch([
            env.DB.prepare('UPDATE enrollment_claims SET claimed_by=? WHERE token_hash=? AND claimed_by IS NULL AND expires>? RETURNING fob_id').bind(member.id, tokenHash, now()),
            env.DB.prepare('UPDATE members SET fob_id=(SELECT fob_id FROM enrollment_claims WHERE token_hash=?) WHERE id=? AND changes()=1').bind(tokenHash, member.id),
        ]);
        if (!results[0].results.length) throw new HttpError(409, 'Enrollment claim expired or already used');
        return json({ bound: true });
    }
    throw new HttpError(405, 'Method not allowed');
}

async function adminRoute(request: Request, env: Env): Promise<Response> {
    const leader = await requireLeader(request, env),
        url = new URL(request.url),
        path = url.pathname,
        method = request.method;
    if (path === '/api/admin/config') {
        const cfg = await settings(env);
        if (method === 'GET') return json(cfg);
        if (method === 'PUT') {
            const input = await body<Input>(request),
                version = integer(input.version, 'settings version');
            const next = validateSettings(input, cfg);
            delete (next as Partial<Settings>).version;
            const result = await env.DB.prepare('UPDATE settings SET version=version+1,data=? WHERE id=1 AND version=? RETURNING version')
                .bind(JSON.stringify(next), version)
                .first();
            if (!result) throw new HttpError(409, 'Settings changed; reload before saving');
            await audit(env, leader.id, null, 'SettingsUpdated', { fields: Object.keys(input) });
            return json(await settings(env));
        }
    }
    if (path === '/api/admin/waiver' && method === 'PUT') {
        const input = await body<Input>(request),
            content = text(input.content, 'waiver content', 60000, false);
        if (!Array.isArray(input.agreements) || input.agreements.length < 1 || input.agreements.length > 30) throw new HttpError(400, 'Provide 1 to 30 waiver agreements');
        const agreements = input.agreements.map((v) => text(v, 'agreement', 2000, false));
        const result = await env.DB.batch([
            env.DB.prepare('INSERT INTO waiver_versions(version,content,agreements) SELECT COALESCE(MAX(version),0)+1,?,? FROM waiver_versions RETURNING version').bind(
                content,
                JSON.stringify(agreements),
            ),
            env.DB.prepare("UPDATE settings SET version=version+1,data=json_set(data,'$.waiver_version',(SELECT MAX(version) FROM waiver_versions)) WHERE id=1"),
            env.DB.prepare("INSERT INTO member_events(actor,event,details) VALUES(?,'WaiverPublished','New version published')").bind(leader.id),
        ]);
        return json(result[0].results[0], 201);
    }
    if (path === '/api/admin/members' && method === 'GET') {
        const search = (url.searchParams.get('search') || '').slice(0, 200),
            status = url.searchParams.get('status') || '';
        const where = `WHERE (instr(lower(name),lower(?))>0 OR instr(lower(email),lower(?))>0 OR instr(lower(discord_username),lower(?))>0 OR CAST(id AS TEXT)=? OR discord_user_id=?) AND (?='' OR access_status=?)`;
        const args = [search, search, search, search, search, status, status];
        const [list, count] = await env.DB.batch([
            env.DB.prepare(`SELECT * FROM members ${where} ORDER BY id DESC LIMIT 50 OFFSET ?`).bind(...args, offset(url)),
            env.DB.prepare(`SELECT count(*) total FROM members ${where}`).bind(...args),
        ]);
        return json({ members: list.results, total: (count.results[0] as { total: number }).total });
    }
    if (path === '/api/admin/members' && method === 'POST') {
        const fields = editable(await body<Input>(request), true);
        if (!fields.email) throw new HttpError(400, 'Contact email is required');
        await validateDiscount(env, fields);
        const keys = Object.keys(fields);
        const row = await env.DB.prepare(`INSERT INTO members(${keys.join(',')}) VALUES(${keys.map(() => '?').join(',')}) RETURNING id`)
            .bind(...keys.map((k) => fields[k]))
            .first<{ id: number }>();
        await audit(env, leader.id, row!.id, 'AdminMemberCreated', { fields: keys });
        return json(row, 201);
    }
    const match = /^\/api\/admin\/members\/(\d+)(\/approve)?$/.exec(path);
    if (match) {
        const id = integer(Number(match[1]), 'member ID');
        const member = await env.DB.prepare('SELECT * FROM members WHERE id=?').bind(id).first<Member>();
        if (!member) throw new HttpError(404, 'Member not found');
        if (match[2] && method === 'POST') {
            const input = await body<Input>(request),
                requestID = text(input.request_id, 'request ID', 64, false);
            const root = input.root_family_member === undefined || input.root_family_member === null ? member.root_family_member : integer(input.root_family_member, 'family root');
            if (member.discount_type === 'family' && root === null) throw new HttpError(400, 'Family approval requires a primary member');
            const result = await env.DB.prepare(
                "UPDATE members SET discount_status='approved',root_family_member=? WHERE id=? AND discount_status='requested' AND discount_request_id=? RETURNING id",
            )
                .bind(root, id, requestID)
                .first();
            if (!result) throw new HttpError(409, 'This discount request is no longer pending');
            await audit(env, leader.id, id, 'DiscountApproved', {
                request_id: requestID,
                root_family_member: root,
            });
            return json({ approved: true });
        }
        if (!match[2] && method === 'GET') return json(member);
        if (!match[2] && method === 'PATCH') {
            const { version, ...input } = await body<Input>(request);
            const expectedVersion = integer(version, 'member version');
            const fields = editable(input, true);
            await validateDiscount(env, fields, member);
            const keys = Object.keys(fields);
            if (!keys.length) throw new HttpError(400, 'No fields supplied');
            return mutateMember(env, id, fields, leader.id, expectedVersion);
        }
        if (!match[2] && method === 'DELETE') {
            const input = await body<Input>(request);
            return mutateMember(env, id, null, leader.id, integer(input.version, 'member version'));
        }
    }
    if (path === '/api/admin/events' && method === 'GET') {
        const memberID = url.searchParams.get('member') ? integer(Number(url.searchParams.get('member')), 'member ID') : null;
        return json({
            events: (
                await env.DB.prepare('SELECT * FROM member_events WHERE (? IS NULL OR member=?) ORDER BY id DESC LIMIT 50 OFFSET ?').bind(memberID, memberID, offset(url)).all()
            ).results,
        });
    }
    if (path === '/api/admin/swipes' && method === 'GET')
        return json({
            swipes: (await env.DB.prepare('SELECT * FROM fob_swipes ORDER BY timestamp DESC,uid LIMIT 50 OFFSET ?').bind(offset(url)).all()).results,
        });
    if (path === '/api/admin/jobs' && method === 'GET')
        return json({
            jobs: (
                await env.DB.prepare("SELECT * FROM jobs ORDER BY CASE status WHEN 'dead' THEN 0 WHEN 'processing' THEN 1 WHEN 'pending' THEN 2 ELSE 3 END,id DESC LIMIT 100").all()
            ).results,
        });
    const retry = /^\/api\/admin\/jobs\/(\d+)\/retry$/.exec(path);
    if (retry && method === 'POST') {
        const id = integer(Number(retry[1]), 'job ID');
        const row = await env.DB.prepare(
            "UPDATE jobs SET status='pending',attempts=0,available_at=?,lease_until=0,completed=NULL,last_error='' WHERE id=? AND status='dead' RETURNING id",
        )
            .bind(now(), id)
            .first();
        if (!row) throw new HttpError(409, 'Only dead jobs can be retried');
        await audit(env, leader.id, null, 'JobRetryRequested', { job_id: id });
        return json({ retried: true });
    }
    if (path === '/api/admin/export' && method === 'GET') {
        const fields = ['id', 'name', 'email', 'discord_user_id', 'payment_status', 'access_status', 'fob_id', 'root_family_member', 'discount_type'];
        const rows = await env.DB.prepare(`SELECT ${fields.join(',')} FROM members ORDER BY id`).all<Record<string, unknown>>();
        const cell = (value: unknown) => {
            let s = String(value ?? '');
            if (/^[=+@\-\t\r\n]/.test(s)) s = "'" + s;
            return '"' + s.replaceAll('"', '""') + '"';
        };
        return new Response([fields.join(','), ...rows.results.map((row) => fields.map((key) => cell(row[key])).join(','))].join('\r\n'), {
            headers: {
                'Content-Type': 'text/csv; charset=utf-8',
                'Content-Disposition': 'attachment; filename="conway-members.csv"',
                'Cache-Control': 'no-store',
            },
        });
    }
    throw new HttpError(404, 'Not found');
}
