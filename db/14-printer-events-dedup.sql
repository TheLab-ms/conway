CREATE UNIQUE INDEX printer_events_uniq_idx ON printer_events(printer_name, COALESCE(job_finished_timestamp, '1970-01-01 00:00:00'), error_code);
