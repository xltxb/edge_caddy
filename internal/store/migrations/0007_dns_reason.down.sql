ALTER TABLE edge_nodes
  DROP COLUMN dns_changed_at,
  DROP COLUMN dns_actor,
  DROP COLUMN dns_reason;
DROP TYPE IF EXISTS dns_change_reason;
