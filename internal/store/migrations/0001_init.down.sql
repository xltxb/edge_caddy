DROP TABLE IF EXISTS settings, audit_logs, traffic_samples, events,
  pki_cas, cert_nodes, certs, dns_weights, baseline, deploy_results, deploys,
  config_drafts, global_policies, access_rules, proxy_routes,
  enroll_tokens, edge_nodes, sessions, users CASCADE;
DROP TYPE IF EXISTS event_kind, op_result, deploy_state, rule_type, block_mode, node_status;
