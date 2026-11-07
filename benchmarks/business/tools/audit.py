"""数据库业务不变量与查询证据，不把失败请求当成吞吐收益。"""

import json


INVARIANTS = (
    "pending_orders", "missing_audits", "invalid_totals", "negative_stock",
    "inventory_mismatches", "ticket_version_mismatches", "cross_tenant_items",
)


def assess(record, confirmed_orders, confirmed_comments):
    if confirmed_orders < 0 or confirmed_comments < 0:
        raise ValueError("客户端确认数不能为负")
    failures = {name: record[name] for name in INVARIANTS if record[name] != 0}
    unknown_orders = record["orders"] - confirmed_orders
    unknown_comments = record["new_comments"] - confirmed_comments
    if unknown_orders < 0:
        failures["missing_confirmed_orders"] = -unknown_orders
    if unknown_comments < 0:
        failures["missing_confirmed_comments"] = -unknown_comments
    return {"valid": not failures, "failures": failures, "database": record,
            "confirmed_orders": confirmed_orders, "confirmed_comments": confirmed_comments,
            "unconfirmed_orders": unknown_orders, "unconfirmed_comments": unknown_comments}


def capture(environment, reports):
    dataset = environment.dataset
    initial_comments = dataset["tenant_count"] * dataset["tickets_per_tenant"] * dataset["initial_comments_per_ticket"]
    initial_stock = dataset["initial_stock"]
    # 以完整表关系核验库存、金额和版本，不依赖应用返回内容自证正确。
    query = f"""
WITH sold AS (SELECT product_id, sum(quantity) AS quantity FROM bench_order_items GROUP BY product_id),
amounts AS (SELECT order_id, sum(quantity * unit_price_cents) AS total FROM bench_order_items GROUP BY order_id),
comment_counts AS (SELECT ticket_id, count(*) AS total FROM bench_comments GROUP BY ticket_id)
SELECT json_build_object(
 'orders', (SELECT count(*) FROM bench_orders),
 'audit_rows', (SELECT count(*) FROM bench_order_audit),
 'new_comments', (SELECT count(*) - {initial_comments} FROM bench_comments),
 'pending_orders', (SELECT count(*) FROM bench_orders WHERE status <> 'created'),
 'missing_audits', (SELECT count(*) FROM bench_orders o LEFT JOIN bench_order_audit a ON a.order_id=o.id
                    WHERE a.order_id IS NULL OR a.total_cents<>o.total_cents OR a.user_id<>o.user_id),
 'invalid_totals', (SELECT count(*) FROM bench_orders o LEFT JOIN amounts a ON a.order_id=o.id
                    WHERE a.total IS NULL OR a.total<>o.total_cents),
 'negative_stock', (SELECT count(*) FROM bench_products WHERE stock<0),
 'inventory_mismatches', (SELECT count(*) FROM bench_products p LEFT JOIN sold s ON s.product_id=p.id
                          WHERE p.stock <> {initial_stock}-coalesce(s.quantity,0)),
 'ticket_version_mismatches', (SELECT count(*) FROM bench_tickets t LEFT JOIN comment_counts c ON c.ticket_id=t.id
                               WHERE t.version<>coalesce(c.total,0)),
 'cross_tenant_items', (SELECT count(*) FROM bench_order_items i JOIN bench_orders o ON o.id=i.order_id
                        JOIN bench_products p ON p.id=i.product_id WHERE o.tenant_id<>p.tenant_id)
)"""
    record = json.loads(environment.sql(query))
    confirmed = {"order": 0, "ticket": 0}
    for report in reports:
        for scenario in confirmed:
            confirmed[scenario] += report.get("scenarios", {}).get(scenario, {}).get("totals", {}).get("success", 0)
    return assess(record, confirmed["order"], confirmed["ticket"])


def query_statistics(environment):
    query = """SELECT coalesce(json_agg(row_to_json(s)), '[]'::json) FROM (
SELECT queryid::text, toplevel, query, calls, rows, total_exec_time, mean_exec_time,
       shared_blks_hit, shared_blks_read, shared_blks_dirtied, shared_blks_written,
       temp_blks_read, temp_blks_written, wal_records, wal_bytes::text
FROM pg_stat_statements
WHERE dbid=(SELECT oid FROM pg_database WHERE datname=current_database())
  AND query LIKE '%bench_%' AND query NOT LIKE '%pg_stat_statements%'
ORDER BY calls DESC, queryid
) s"""
    return json.loads(environment.sql(query))


def database_settings(environment):
    query = """SELECT json_object_agg(name, json_build_object('setting',setting,'unit',unit))
FROM pg_settings WHERE name IN
('server_version','fsync','synchronous_commit','full_page_writes','shared_buffers','work_mem',
 'max_connections','checkpoint_timeout','max_wal_size','track_io_timing','shared_preload_libraries')"""
    return json.loads(environment.sql(query))


def query_plans(environment):
    dataset = environment.dataset
    queries = json.loads((environment.root / "contracts/queries.json").read_text())
    parameters = {"catalog": [1, 1, 0, dataset["maximum_price_cents"], 0, 0, dataset["catalog_page_size"] + 1],
                  "product": [1, 1], "specifications": [1, dataset["specifications_per_product"]]}
    # 只对只读检索执行 EXPLAIN ANALYZE；锁和写事务由真实业务验收及轮后审计证明。
    plans = {}
    for name, values in parameters.items():
        query = queries[name]
        for value in values:
            query = query.replace("?", str(value), 1)
        plans[name] = json.loads(environment.sql("EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) " + query))
    return plans


def schema_metrics(environment):
    query = """SELECT json_build_object(
 'tables', (SELECT json_agg(row_to_json(t)) FROM (
   SELECT relname, n_live_tup, pg_table_size(relid) AS table_bytes, pg_indexes_size(relid) AS indexes_bytes
   FROM pg_stat_user_tables WHERE relname LIKE 'bench_%' ORDER BY relname) t),
 'indexes', (SELECT json_agg(row_to_json(i)) FROM (
   SELECT tablename, indexname, indexdef, pg_relation_size(indexname::regclass) AS bytes
   FROM pg_indexes WHERE schemaname='public' AND tablename LIKE 'bench_%' ORDER BY tablename,indexname) i)
)"""
    return json.loads(environment.sql(query))
