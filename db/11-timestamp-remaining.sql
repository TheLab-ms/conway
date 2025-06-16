/* Change job_remaining_minutes to job_finished_timestamp */
DELETE FROM printer_events;
ALTER TABLE printer_events DROP COLUMN job_remaining_minutes;
ALTER TABLE printer_events ADD COLUMN job_finished_timestamp INTEGER;
