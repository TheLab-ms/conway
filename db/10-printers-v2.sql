DELETE FROM printer_events;
ALTER TABLE printer_events DROP COLUMN job_finished_at;
ALTER TABLE printer_events ADD COLUMN job_remaining_minutes;
