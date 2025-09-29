/* Prior to this change, some of the event triggers were not idempotent which causes noise */

DROP TRIGGER IF EXISTS members_confirmed_email;
DROP TRIGGER IF EXISTS members_discount_type_update;
DROP TRIGGER IF EXISTS members_access_status_update;
DROP TRIGGER IF EXISTS members_leadership_set;
DROP TRIGGER IF EXISTS members_leadership_unset;
DROP TRIGGER IF EXISTS members_non_billable_added;
DROP TRIGGER IF EXISTS members_non_billable_removed;
DROP TRIGGER IF EXISTS members_fob_changed;
DROP TRIGGER IF EXISTS waiver_signed;

CREATE TRIGGER IF NOT EXISTS members_confirmed_email AFTER UPDATE OF confirmed ON members WHEN OLD.confirmed = 0 AND NEW.confirmed = 1
BEGIN
INSERT INTO member_events (member, event, details) VALUES (NEW.id, 'EmailConfirmed', 'Email address confirmed');
END;

CREATE TRIGGER IF NOT EXISTS members_discount_type_update AFTER UPDATE OF discount_type ON members
BEGIN
INSERT INTO member_events (member, event, details) VALUES (NEW.id, 'DiscountTypeModified', 'Discount changed from "' || COALESCE(OLD.discount_type, 'NULL') || '" to "' || COALESCE(NEW.discount_type, 'NULL') || '"');
END;

CREATE TRIGGER IF NOT EXISTS members_access_status_update AFTER UPDATE ON members WHEN OLD.access_status != NEW.access_status
BEGIN
INSERT INTO member_events (member, event, details) VALUES (NEW.id, 'AccessStatusChanged', 'Building access status changed from "' || COALESCE(OLD.access_status, 'NULL') || '" to "' || COALESCE(NEW.access_status, 'NULL') || '"');
END;

CREATE TRIGGER IF NOT EXISTS members_leadership_set AFTER UPDATE OF leadership ON members WHEN NEW.leadership = 1 AND OLD.leadership = 0
BEGIN
INSERT INTO member_events (member, event, details) VALUES (NEW.id, 'LeadershipStatusAdded', 'Designated as leadership');
END;

CREATE TRIGGER IF NOT EXISTS members_leadership_unset AFTER UPDATE OF leadership ON members WHEN NEW.leadership = 0 AND OLD.leadership = 1
BEGIN
INSERT INTO member_events (member, event, details) VALUES (NEW.id, 'LeadershipStatusRemoved', 'No longer designated as leadership');
END;

CREATE TRIGGER IF NOT EXISTS members_non_billable_added AFTER UPDATE OF non_billable ON members WHEN NEW.non_billable IS true AND OLD.non_billable IS false
BEGIN
INSERT INTO member_events (member, event, details) VALUES (NEW.id, 'NonBillableStatusAdded', 'The member has been marked as non-billable');
END;

CREATE TRIGGER IF NOT EXISTS members_non_billable_removed AFTER UPDATE OF non_billable ON members WHEN NEW.non_billable IS false AND OLD.non_billable IS true
BEGIN
INSERT INTO member_events (member, event, details) VALUES (NEW.id, 'NonBillableStatusRemoved', 'The member is no longer marked as non-billable');
END;

CREATE TRIGGER IF NOT EXISTS members_fob_changed AFTER UPDATE OF fob_id ON members WHEN OLD.fob_id != NEW.fob_id
BEGIN
INSERT INTO member_events (member, event, details) VALUES (NEW.id, 'FobChanged', 'The fob ID changed from ' || COALESCE(OLD.fob_id, 'NULL') || ' to ' || COALESCE(NEW.fob_id, 'NULL'));
END;

CREATE TRIGGER IF NOT EXISTS waiver_signed AFTER UPDATE OF waiver ON members WHEN OLD.waiver IS NULL AND NEW.waiver IS NOT NULL
BEGIN
INSERT INTO member_events (member, event, details) VALUES (NEW.id, 'WaiverSigned', 'Waiver signed');
END;
