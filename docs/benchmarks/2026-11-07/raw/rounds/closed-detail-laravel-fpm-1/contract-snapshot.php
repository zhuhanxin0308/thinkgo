<?php
declare(strict_types=1);
// 由共同 JSON 合同生成的不可变配置，禁止手工修改。
return array (
  'schema_version' => 1,
  'source_sha256' => 
  array (
    'dataset.json' => 'a602bafa91ec018a7c6d0c0abf5463531a625a80cdcda24595495edc8c396e65',
    'queries.json' => '207f257f2aff367a1017285326dd2c87958cf50f90613d28de3c21e3fa99b1c9',
  ),
  'dataset' => 
  array (
    'schema_version' => 1,
    'seed' => 20261107,
    'tenant_count' => 4,
    'users_per_tenant' => 1000,
    'products_per_tenant' => 25000,
    'categories_per_tenant' => 32,
    'tickets_per_tenant' => 5000,
    'specifications_per_product' => 3,
    'initial_comments_per_ticket' => 3,
    'initial_stock' => 1000000,
    'inactive_product_modulus' => 10,
    'hot_products_per_tenant' => 100,
    'cache_ttl_seconds' => 3600,
    'cache_hit_weight' => 90,
    'catalog_page_size' => 20,
    'maximum_page_size' => 50,
    'maximum_price_cents' => 100000000,
    'maximum_order_items' => 8,
    'maximum_item_quantity' => 10,
    'minimum_comment_characters' => 16,
    'maximum_comment_characters' => 2000,
    'maximum_body_bytes' => 16384,
    'database_connection_budget' => 24,
    'redis_connection_budget' => 24,
    'request_timeout_seconds' => 5,
    'mixed_weights' => 
    array (
      'catalog' => 40,
      'detail' => 30,
      'order' => 20,
      'ticket' => 10,
    ),
  ),
  'queries' => 
  array (
    'catalog' => 'SELECT p.id,p.name,p.price_cents,c.name,p.stock FROM bench_products p JOIN bench_categories c ON c.tenant_id=p.tenant_id AND c.id=p.category_id WHERE p.tenant_id=? AND p.category_id=? AND p.active=true AND p.price_cents>=? AND p.price_cents<=? AND (p.price_cents,p.id)>(?,?) ORDER BY p.price_cents,p.id LIMIT ?',
    'product' => 'SELECT p.id,p.name,p.price_cents,p.stock,c.name,p.description FROM bench_products p JOIN bench_categories c ON c.tenant_id=p.tenant_id AND c.id=p.category_id WHERE p.tenant_id=? AND p.id=? AND p.active=true',
    'specifications' => 'SELECT name,value FROM bench_specifications WHERE product_id=? ORDER BY position LIMIT ?',
    'reserve_order' => 'INSERT INTO bench_orders(tenant_id,user_id,idempotency_key,total_cents,status) VALUES(?,?,?::uuid,0,\'pending\') ON CONFLICT(tenant_id,user_id,idempotency_key) DO NOTHING RETURNING id',
    'existing_order' => 'SELECT id,total_cents,status FROM bench_orders WHERE tenant_id=? AND user_id=? AND idempotency_key=?::uuid',
    'existing_order_items' => 'SELECT product_id,quantity,unit_price_cents FROM bench_order_items WHERE order_id=? ORDER BY product_id',
    'lock_product' => 'SELECT price_cents,stock FROM bench_products WHERE tenant_id=? AND id=? AND active=true FOR UPDATE',
    'deduct_stock' => 'UPDATE bench_products SET stock=stock-? WHERE tenant_id=? AND id=? AND stock>=?',
    'insert_order_item' => 'INSERT INTO bench_order_items(order_id,product_id,quantity,unit_price_cents) VALUES(?,?,?,?)',
    'finish_order' => 'UPDATE bench_orders SET total_cents=?,status=\'created\' WHERE id=? AND status=\'pending\'',
    'order_audit' => 'INSERT INTO bench_order_audit(order_id,user_id,total_cents) VALUES(?,?,?)',
    'lock_ticket' => 'SELECT owner_id,status,version FROM bench_tickets WHERE tenant_id=? AND id=? FOR UPDATE',
    'insert_comment' => 'INSERT INTO bench_comments(ticket_id,user_id,body) VALUES(?,?,?) RETURNING id',
    'update_ticket' => 'UPDATE bench_tickets SET version=version+1,updated_at=CURRENT_TIMESTAMP WHERE tenant_id=? AND id=? RETURNING version',
  ),
);
