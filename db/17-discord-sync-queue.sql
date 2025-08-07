ALTER TABLE members ADD COLUMN discord_last_synced INTEGER;

CREATE TRIGGER IF NOT EXISTS discord_sync_on_user_id_change AFTER UPDATE OF discord_user_id ON members WHEN NEW.discord_user_id IS NOT NULL AND (OLD.discord_user_id IS NULL OR OLD.discord_user_id != NEW.discord_user_id)
BEGIN
    UPDATE members SET discord_last_synced = NULL WHERE id = NEW.id;
END;

CREATE TRIGGER IF NOT EXISTS discord_sync_on_payment_affecting_change AFTER UPDATE ON members 
WHEN NEW.discord_user_id IS NOT NULL AND (
    (OLD.confirmed != NEW.confirmed) OR
    (OLD.stripe_subscription_state != NEW.stripe_subscription_state) OR
    (OLD.paypal_subscription_id != NEW.paypal_subscription_id) OR
    (OLD.non_billable != NEW.non_billable)
)
BEGIN
    UPDATE members SET discord_last_synced = NULL WHERE id = NEW.id;
END;