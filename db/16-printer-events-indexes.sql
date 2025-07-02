CREATE INDEX IF NOT EXISTS printer_events_printer_name_timestamp_idx ON printer_events (printer_name, timestamp DESC);
