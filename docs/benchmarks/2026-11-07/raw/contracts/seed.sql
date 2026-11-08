-- 数据规模由 dataset.json 通过 psql -v 传入；全部数据均为合成数据。
INSERT INTO bench_tenants
SELECT id, '演示租户-' || id FROM generate_series(1,:tenant_count) AS id;

INSERT INTO bench_users
SELECT (t-1)*:users_per_tenant+u,t,'客户-'||t||'-'||u,
       'synthetic-'||t||'-'||u||'@example.invalid'
FROM generate_series(1,:tenant_count) AS t
CROSS JOIN generate_series(1,:users_per_tenant) AS u;

INSERT INTO bench_categories
SELECT t,c,'分类-'||c FROM generate_series(1,:tenant_count) AS t
CROSS JOIN generate_series(1,:categories_per_tenant) AS c;

INSERT INTO bench_products
SELECT (t-1)*:products_per_tenant+p,t,((p-1)%:categories_per_tenant)+1,
       '通用商品-'||t||'-'||p,
       repeat('用于真实目录、库存与交易流程的合成商品说明。',12),
       100+((p::bigint*7919+:seed)%199900),:initial_stock,
       (p%:inactive_product_modulus)<>0,
       50+((p::bigint*3571+:seed)%90000)
FROM generate_series(1,:tenant_count) AS t
CROSS JOIN generate_series(1,:products_per_tenant) AS p;

INSERT INTO bench_specifications
SELECT id,s,'规格-'||s,'值-'||((id+s)%97)
FROM bench_products CROSS JOIN generate_series(1,:specifications_per_product) AS s;

INSERT INTO bench_tickets
SELECT (t-1)*:tickets_per_tenant+k,t,
       (t-1)*:users_per_tenant+((k-1)%:users_per_tenant)+1,
       '售后工单-'||t||'-'||k,'open',:initial_comments_per_ticket,
       TIMESTAMPTZ '2026-01-01 00:00:00+00'
FROM generate_series(1,:tenant_count) AS t
CROSS JOIN generate_series(1,:tickets_per_tenant) AS k;

INSERT INTO bench_comments(ticket_id,user_id,body,created_at)
SELECT id,owner_id,'初始处理记录-'||s||'：核对订单与商品信息，等待后续处理。',
       TIMESTAMPTZ '2026-01-01 00:00:00+00'
FROM bench_tickets CROSS JOIN generate_series(1,:initial_comments_per_ticket) AS s;

ANALYZE;
