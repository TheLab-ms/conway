-- Deduplicate waivers by keeping the earliest record for each unique email+version combination
WITH duplicates AS (
    SELECT id
    FROM waivers w1
    WHERE EXISTS (
        SELECT 1 FROM waivers w2
        WHERE w2.email = w1.email
          AND w2.version = w1.version
          AND w2.id < w1.id
    )
)
DELETE FROM waivers WHERE id IN (SELECT id FROM duplicates);

-- Add a unique index to prevent future duplicates
CREATE UNIQUE INDEX IF NOT EXISTS waivers_email_version_uidx ON waivers(email, version);
