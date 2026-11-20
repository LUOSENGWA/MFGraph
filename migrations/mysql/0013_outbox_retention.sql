-- Backlog diagnostics and bounded deletion use different orderings from
-- commit-order delivery. Retention is based on publication time.
ALTER TABLE outbox_events ADD KEY idx_outbox_status_created (status, created_at_us, event_id);
ALTER TABLE outbox_events ADD KEY idx_outbox_published (status, published_at_us, event_id);
