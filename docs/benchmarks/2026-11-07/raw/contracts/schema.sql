-- 仅在本基准拥有的独立数据库中执行；金额统一使用最小货币单位整数。
CREATE TABLE bench_tenants (
    id integer PRIMARY KEY,
    name text NOT NULL
);

CREATE TABLE bench_users (
    id bigint PRIMARY KEY,
    tenant_id integer NOT NULL REFERENCES bench_tenants(id),
    name text NOT NULL,
    internal_email text NOT NULL,
    UNIQUE (tenant_id, id)
);

CREATE TABLE bench_categories (
    tenant_id integer NOT NULL REFERENCES bench_tenants(id),
    id integer NOT NULL,
    name text NOT NULL,
    PRIMARY KEY (tenant_id, id)
);

CREATE TABLE bench_products (
    id bigint PRIMARY KEY,
    tenant_id integer NOT NULL REFERENCES bench_tenants(id),
    category_id integer NOT NULL,
    name text NOT NULL,
    description text NOT NULL,
    price_cents bigint NOT NULL CHECK (price_cents > 0),
    stock bigint NOT NULL CHECK (stock >= 0),
    active boolean NOT NULL,
    internal_cost_cents bigint NOT NULL,
    FOREIGN KEY (tenant_id, category_id) REFERENCES bench_categories(tenant_id, id)
) WITH (fillfactor=90);

-- 等值租户/分类后执行价格范围和稳定游标扫描；库存不进入索引，允许原地更新。
CREATE INDEX bench_products_catalog_idx
    ON bench_products (tenant_id, category_id, price_cents, id)
    WHERE active=true;

CREATE TABLE bench_specifications (
    product_id bigint NOT NULL REFERENCES bench_products(id),
    position smallint NOT NULL,
    name text NOT NULL,
    value text NOT NULL,
    PRIMARY KEY (product_id, position)
);

CREATE TABLE bench_orders (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id integer NOT NULL REFERENCES bench_tenants(id),
    user_id bigint NOT NULL,
    idempotency_key uuid NOT NULL,
    total_cents bigint NOT NULL CHECK (total_cents >= 0),
    status text NOT NULL CHECK (status IN ('pending','created')),
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (tenant_id, user_id) REFERENCES bench_users(tenant_id, id),
    UNIQUE (tenant_id, user_id, idempotency_key)
);

CREATE TABLE bench_order_items (
    order_id bigint NOT NULL REFERENCES bench_orders(id),
    product_id bigint NOT NULL REFERENCES bench_products(id),
    quantity integer NOT NULL CHECK (quantity > 0),
    unit_price_cents bigint NOT NULL CHECK (unit_price_cents > 0),
    PRIMARY KEY (order_id, product_id)
);

CREATE TABLE bench_order_audit (
    order_id bigint PRIMARY KEY REFERENCES bench_orders(id),
    user_id bigint NOT NULL REFERENCES bench_users(id),
    total_cents bigint NOT NULL CHECK (total_cents > 0)
);

CREATE TABLE bench_tickets (
    id bigint PRIMARY KEY,
    tenant_id integer NOT NULL REFERENCES bench_tenants(id),
    owner_id bigint NOT NULL,
    title text NOT NULL,
    status text NOT NULL CHECK (status IN ('open','closed')),
    version bigint NOT NULL CHECK (version > 0),
    updated_at timestamptz NOT NULL,
    FOREIGN KEY (tenant_id, owner_id) REFERENCES bench_users(tenant_id, id)
) WITH (fillfactor=90);

CREATE TABLE bench_comments (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    ticket_id bigint NOT NULL REFERENCES bench_tickets(id),
    user_id bigint NOT NULL REFERENCES bench_users(id),
    body text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- 评论时间线只维护一条业务索引，避免为读基准增加无关写入代价。
CREATE INDEX bench_comments_timeline_idx ON bench_comments(ticket_id,id);
