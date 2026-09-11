export interface Env {
    DB: D1Database;
    ASSETS: Fetcher;
    JOBS: Queue<{ id: number }>;
    COORDINATOR: DurableObjectNamespace;
    SITE_URL: string;
    EDGE_URL: string;
    EDGE_TOKEN: string;
    KIOSK_IPS: string;
    AUTOMATION_ENABLED: string;
    DISCORD_CLIENT_ID: string;
    DISCORD_CLIENT_SECRET: string;
    DISCORD_BOT_TOKEN: string;
    DISCORD_PUBLIC_KEY: string;
    DISCORD_GUILD_ID: string;
    DISCORD_ROLE_ID: string;
    DISCORD_LEADERSHIP_CHANNEL_ID: string;
    DISCORD_SIGNUP_NOTIFY_ENABLED: string;
    DISCORD_ACCESS_DENIED_ENABLED: string;
    STRIPE_SECRET_KEY: string;
    STRIPE_WEBHOOK_SECRET: string;
}
export interface Member {
    id: number;
    version: number;
    created: number;
    email: string;
    confirmed: number;
    name: string;
    name_override: string | null;
    admin_notes: string;
    identifier: string;
    payment_status: string | null;
    access_status: string;
    waiver: number | null;
    fob_id: number | null;
    fob_last_seen: number | null;
    leadership: number;
    non_billable: number;
    discount_type: string | null;
    discount_status: string | null;
    discount_request_id: string | null;
    bill_annually: number;
    root_family_member: number | null;
    root_family_member_active: number | null;
    stripe_customer_id: string | null;
    stripe_subscription_id: string | null;
    stripe_subscription_state: string | null;
    stripe_cancellation_reason: string | null;
    stripe_last_payment_error: string | null;
    paypal_subscription_id: string | null;
    paypal_price: number | null;
    discord_user_id: string | null;
    discord_username: string | null;
    discord_email: string | null;
    discord_last_synced: number | null;
}
export interface Settings {
    version: number;
    site_name: string;
    discounts: { id: string; label: string; coupon_id: string }[];
    monthly_price_id: string;
    yearly_price_id: string;
    waiver_version: number;
}
export interface Session {
    token_hash: string;
    member: number | null;
    csrf_token: string;
    expires: number;
}
