SELECT r.order_id, a.attempt_id, a.attempt_no, a.config_revision,
       e.event_id, e.sequence, e.kind, e.occurred_at, e.clock_domain, e.precision_ms,
       CASE WHEN e.kind IN ('request-created', 'replay-started')
                  AND e.sequence = 1 AND e.occurred_at = a.started_at
            THEN 1 ELSE 0 END AS elapsed_anchor,
       e.artifact, e.artifact_sha256, e.byte_start, e.byte_end,
       CASE WHEN e.kind = 'routing-blocked' THEN 'fail'
            WHEN e.kind IN ('request-created', 'replay-started', 'publication-acknowledged',
                            'receiver-received', 'route-resolved', 'assignment-accepted') THEN 'pass'
            ELSE NULL END AS status,
       NULL AS evidence_ref, w.generation AS source_generation,
       'events' AS source_collection, e.event_id AS source_record_key
FROM requests r
JOIN attempts a ON a.request_id = r.request_id
JOIN events e ON e.attempt_id = a.attempt_id
CROSS JOIN workspace_state w
WHERE r.order_id IN (@order_id, @comparison_order_id)
ORDER BY r.order_id, a.attempt_no, e.sequence
