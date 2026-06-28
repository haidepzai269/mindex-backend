-- SC-003: Average tool call response time (target: < 3s / 3000ms)
-- Excludes web_fetch and code_sandbox (spec explicitly exempts these)
SELECT
    tool_name,
    COUNT(*)                                AS total_calls,
    ROUND(AVG(duration_ms))                 AS avg_ms,
    ROUND(PERCENTILE_CONT(0.95) WITHIN GROUP (ORDER BY duration_ms)) AS p95_ms,
    ROUND(PERCENTILE_CONT(0.99) WITHIN GROUP (ORDER BY duration_ms)) AS p99_ms
FROM tool_calls
WHERE created_at >= NOW() - INTERVAL '24 hours'
  AND tool_name NOT IN ('web_fetch', 'code_sandbox')
GROUP BY tool_name
ORDER BY avg_ms DESC;

-- SC-004: Tool call success rate (target: >= 95%)
SELECT
    tool_name,
    COUNT(*)                                               AS total,
    COUNT(*) FILTER (WHERE status = 'success')             AS ok,
    COUNT(*) FILTER (WHERE status = 'error')               AS errors,
    COUNT(*) FILTER (WHERE status = 'timeout')             AS timeouts,
    ROUND(100.0 * COUNT(*) FILTER (WHERE status = 'success') / NULLIF(COUNT(*), 0), 2) AS success_pct
FROM tool_calls
WHERE created_at >= NOW() - INTERVAL '24 hours'
GROUP BY tool_name
ORDER BY success_pct ASC;

-- Overall success rate (single number)
SELECT
    ROUND(100.0 * COUNT(*) FILTER (WHERE status = 'success') / NULLIF(COUNT(*), 0), 2) AS overall_success_pct
FROM tool_calls
WHERE created_at >= NOW() - INTERVAL '24 hours';

-- Top errors (for debugging)
SELECT
    tool_name,
    status,
    error_message,
    COUNT(*) AS occurrences
FROM tool_calls
WHERE created_at >= NOW() - INTERVAL '24 hours'
  AND status != 'success'
GROUP BY tool_name, status, error_message
ORDER BY occurrences DESC
LIMIT 20;
