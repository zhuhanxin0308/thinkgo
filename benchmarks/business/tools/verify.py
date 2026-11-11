"""通过外部 HTTP 与真实数据库状态验收各框架的同一业务契约。"""

from __future__ import annotations

import concurrent.futures
import hashlib
import hmac
import http.client
import json
import threading
import time
import urllib.parse
import uuid
from dataclasses import dataclass


TOKEN_LIFETIME_SECONDS = 3600
CONCURRENT_REPLAYS = 8
CATALOG_PAGE_SIZE = 2
CATALOG_PAGES_TO_VERIFY = 2
CARD_FIELDS = {"id", "name", "price_cents", "category", "stock"}
DETAIL_FIELDS = CARD_FIELDS | {"description", "specs"}
ORDER_FIELDS = {"order_id", "total_cents", "status", "items"}
ORDER_ITEM_FIELDS = {"product_id", "quantity", "unit_price_cents"}
COMMENT_FIELDS = {"comment_id", "ticket_id", "version", "status"}
ERROR_CODES = {401: "unauthorized", 403: "forbidden", 404: "not_found",
               409: "conflict", 422: "validation_error"}


def authenticate(secret, tenant_id, user_id, expires_at=None):
    """生成合同规定的 Bearer 凭证，正式验收仍由每个应用逐请求校验。"""
    expires_at = int(time.time()) + TOKEN_LIFETIME_SECONDS if expires_at is None else expires_at
    claims = f"{tenant_id}.{user_id}.{expires_at}"
    signature = hmac.new(secret.encode(), claims.encode(), hashlib.sha256).hexdigest()
    return f"Bearer {claims}.{signature}"


@dataclass(frozen=True)
class Response:
    status: int
    headers: dict
    body: object


class Client:
    """请求只能发往本机；不读取代理配置，也不跟随重定向。"""

    def __init__(self, url, timeout):
        parsed = urllib.parse.urlsplit(url)
        if (parsed.scheme != "http" or parsed.hostname not in {"127.0.0.1", "::1"} or
                parsed.username is not None or parsed.password is not None or
                parsed.path not in {"", "/"} or parsed.query or parsed.fragment):
            raise ValueError("验收 URL 必须是本机 HTTP 根地址")
        self.host, self.port, self.timeout = parsed.hostname, parsed.port, timeout

    def request(self, method, path, *, token=None, body=None, key=None, raw=None, extra_headers=None):
        if not path.startswith("/") or path.startswith("//") or "\r" in path or "\n" in path:
            raise ValueError("请求路径必须是本机绝对路径")
        headers = {"Accept": "application/json"}
        payload = raw
        if body is not None:
            payload = json.dumps(body, ensure_ascii=False, separators=(",", ":")).encode()
        if payload is not None:
            headers["Content-Type"] = "application/json"
        if token is not None:
            headers["Authorization"] = token
        if key is not None:
            headers["Idempotency-Key"] = key
        if extra_headers:
            headers.update(extra_headers)
        connection = http.client.HTTPConnection(self.host, self.port, timeout=self.timeout)
        try:
            connection.request(method, path, body=payload, headers=headers)
            response = connection.getresponse()
            encoded = response.read()
            try:
                document = json.loads(encoded)
            except (ValueError, UnicodeDecodeError):
                document = None
            return Response(response.status, {key.lower(): value for key, value in response.getheaders()},
                            document)
        finally:
            connection.close()


def require(condition, message):
    if not condition:
        raise AssertionError(message)


def expect(response, status, fields=None):
    require(response.status == status, f"HTTP 状态预期 {status}，实际 {response.status}: {response.body}")
    require(response.headers.get("content-type", "").split(";", 1)[0] == "application/json",
            "响应 Content-Type 不是 application/json")
    require(isinstance(response.body, dict), "响应不是 JSON 对象")
    if fields is not None:
        require(set(response.body) == fields, f"响应字段不符: {sorted(response.body)}")
    return response.body


def expect_error(response, status):
    document = expect(response, status, {"error"})
    error = document["error"]
    require(isinstance(error, dict) and set(error) == {"code", "message"}, "错误响应字段不符")
    require(error["code"] == ERROR_CODES[status] and isinstance(error["message"], str) and
            bool(error["message"]), "业务错误代码或提示不符")


class Verifier:
    def __init__(self, environment, secret):
        self.environment = environment
        self.dataset = environment.dataset
        self.secret = secret
        self.client = Client(environment.url, self.dataset["request_timeout_seconds"])
        self.token = authenticate(secret, 1, 1)
        self.checks = []
        self.fixtures = {}

    def run(self, name, action):
        try:
            evidence = action()
        except (AssertionError, ValueError, KeyError, TypeError, IndexError, OSError,
                http.client.HTTPException, RuntimeError) as error:
            self.checks.append({"name": name, "passed": False, "error": str(error)})
            return False
        result = {"name": name, "passed": True}
        if evidence is not None:
            result["evidence"] = evidence
        self.checks.append(result)
        return True

    def sql(self, query):
        return json.loads(self.environment.sql(query))

    def request(self, method, path, **kwargs):
        kwargs.setdefault("token", self.token)
        return self.client.request(method, path, **kwargs)

    def fresh(self):
        counts = self.sql("SELECT json_build_object('orders',(SELECT count(*) FROM bench_orders),"
                          "'order_items',(SELECT count(*) FROM bench_order_items),"
                          "'audit',(SELECT count(*) FROM bench_order_audit))")
        require(all(value == 0 for value in counts.values()), "验收必须从无订单的全新数据库开始")
        return counts

    def load_fixtures(self):
        # 只读取用于断言的种子记录；业务 SQL 仍只在被测应用执行。
        self.fixtures = self.sql("""SELECT json_build_object(
            'products',(SELECT json_agg(p) FROM (SELECT id,price_cents,stock FROM bench_products
                         WHERE tenant_id=1 AND active ORDER BY id LIMIT 4) p),
            'other_product',(SELECT min(id) FROM bench_products WHERE tenant_id=2 AND active),
            'inactive_product',(SELECT min(id) FROM bench_products WHERE tenant_id=1 AND NOT active),
            'ticket',(SELECT row_to_json(t) FROM (SELECT id,version FROM bench_tickets
                      WHERE tenant_id=1 AND owner_id=1 AND status='open' ORDER BY id LIMIT 1) t),
            'unowned_ticket',(SELECT min(id) FROM bench_tickets WHERE tenant_id=1 AND owner_id<>1),
            'other_ticket',(SELECT min(id) FROM bench_tickets WHERE tenant_id=2))""")
        require(len(self.fixtures["products"]) == 4 and self.fixtures["ticket"] is not None,
                "种子数据缺少验收所需记录")

    def authentication(self):
        health = expect(self.request("GET", "/healthz", token=None), 200, {"status"})
        require(health["status"] == "ok", "健康检查状态不符")
        routes = [("GET", "/api/catalog"), ("GET", "/api/products/1"),
                  ("POST", "/api/orders"), ("POST", "/api/tickets/1/comments")]
        for method, path in routes:
            expect_error(self.request(method, path, token=None), 401)
        expired = authenticate(self.secret, 1, 1, expires_at=int(time.time()) - 1)
        tampered = self.token[:-1] + ("0" if self.token[-1] != "0" else "1")
        wrong_tenant = authenticate(self.secret, 2, 1)
        unknown = authenticate(self.secret, self.dataset["tenant_count"] + 1, 1)
        for token in [expired, tampered, wrong_tenant, unknown]:
            expect_error(self.request("GET", "/api/catalog", token=token), 401)
        return {"protected_routes": len(routes), "invalid_credentials": 4}

    def catalog_samples(self, tenant):
        # 独立读取预期记录及一条前瞻记录，不要求每类都存在固定数量的活动商品。
        limit = CATALOG_PAGE_SIZE * CATALOG_PAGES_TO_VERIFY + 1
        return self.sql(f"""SELECT COALESCE(json_agg(sample), '[]'::json) FROM (
            SELECT p.id,p.name,p.price_cents,c.name AS category,p.stock
            FROM bench_products p JOIN bench_categories c
              ON c.tenant_id=p.tenant_id AND c.id=p.category_id
            WHERE p.tenant_id = {int(tenant)} AND p.category_id=1 AND p.active
            ORDER BY p.price_cents,p.id LIMIT {limit}) sample""")

    def verify_catalog_page(self, page, expected, offset):
        items = expected[offset:offset + CATALOG_PAGE_SIZE]
        require(page["items"] == items, "目录分页记录与种子数据不符")
        cursor = None
        if len(expected) > offset + CATALOG_PAGE_SIZE:
            cursor = {key: items[-1][key] for key in ("price_cents", "id")}
        require(page["next_cursor"] == cursor, "目录游标与剩余记录不符")

    def catalog(self):
        page_size = CATALOG_PAGE_SIZE
        expected = self.catalog_samples(1)
        first = expect(self.request("GET", f"/api/catalog?category_id=1&limit={page_size}"),
                       200, {"items", "next_cursor"})
        self.verify_catalog_page(first, expected, 0)
        cursor = first["next_cursor"]
        combined = list(first["items"])
        pages = 1
        if cursor is not None:
            next_path = (f"/api/catalog?category_id=1&limit={page_size}&cursor_price={cursor['price_cents']}"
                         f"&cursor_id={cursor['id']}")
            second = expect(self.request("GET", next_path), 200, {"items", "next_cursor"})
            self.verify_catalog_page(second, expected, page_size)
            combined.extend(second["items"])
            pages += 1
        if first["items"]:
            price = first["items"][0]["price_cents"]
            filtered = expect(self.request("GET", f"/api/catalog?category_id=1&min_price={price}&max_price={price}"),
                              200, {"items", "next_cursor"})
            require(filtered["items"] and all(item["price_cents"] == price for item in filtered["items"]),
                    "价格过滤不准确")
        empty = expect(self.request("GET", "/api/catalog?min_price=0&max_price=0"),
                       200, {"items", "next_cursor"})
        require(empty == {"items": [], "next_cursor": None}, "空目录页面与结束游标不符")
        other = authenticate(self.secret, 2, self.dataset["users_per_tenant"] + 1)
        other_page = expect(self.request("GET", "/api/catalog?limit=2", token=other), 200, {"items", "next_cursor"})
        self.verify_catalog_page(other_page, self.catalog_samples(2), 0)
        invalid = ["internal_cost=1", "limit=0", f"limit={self.dataset['maximum_page_size'] + 1}",
                   "limit=1&limit=2", "min_price=-1", "cursor_price=1", "category_id=0", "limit=01",
                   "cursor_price=0", "cursor_id=0", "limit=%GG", "limit=2&", "&limit=2"]
        for query in invalid:
            expect_error(self.request("GET", "/api/catalog?" + query), 422)
        return {"pages": pages, "ordered_unique_products": len(combined), "invalid_queries": len(invalid)}

    def detail(self):
        product = self.fixtures["products"][0]
        path = f"/api/products/{product['id']}"
        first_response = self.request("GET", path)
        first = expect(first_response, 200, DETAIL_FIELDS)
        require(first_response.headers.get("x-benchmark-cache") == "miss", "首次详情没有回源")
        require(first["id"] == product["id"] and first["price_cents"] == product["price_cents"] and
                first["stock"] == product["stock"], "详情与种子数据库不符")
        require(len(first["specs"]) == self.dataset["specifications_per_product"] and
                all(set(spec) == {"name", "value"} for spec in first["specs"]), "商品规格字段或数量不符")
        second_response = self.request("GET", path)
        require(expect(second_response, 200, DETAIL_FIELDS) == first and
                second_response.headers.get("x-benchmark-cache") == "hit", "缓存命中内容不一致")
        other = authenticate(self.secret, 2, self.dataset["users_per_tenant"] + 1)
        expect_error(self.request("GET", path, token=other), 404)
        for identifier in [self.fixtures["other_product"], self.fixtures["inactive_product"]]:
            expect_error(self.request("GET", f"/api/products/{identifier}"), 404)
        expect_error(self.request("GET", "/api/products/0"), 422)
        return {"product_id": product["id"], "cache_sequence": ["miss", "hit"]}

    def order_snapshot(self, product_ids):
        identifiers = ",".join(str(int(identifier)) for identifier in product_ids)
        return self.sql("SELECT json_build_object('orders',(SELECT count(*) FROM bench_orders),"
                        "'items',(SELECT count(*) FROM bench_order_items),"
                        "'audit',(SELECT count(*) FROM bench_order_audit),"
                        "'stocks',(SELECT json_object_agg(id,stock) FROM bench_products WHERE id IN (" +
                        identifiers + ")))")

    def order(self):
        products = self.fixtures["products"][:2]
        inputs = [{"product_id": product["id"], "quantity": index + 2} for index, product in enumerate(products)]
        for product in products:
            expect(self.request("GET", f"/api/products/{product['id']}"), 200, DETAIL_FIELDS)
        key = uuid.uuid4().hex
        created = expect(self.request("POST", "/api/orders", key=key, body={"items": list(reversed(inputs))}),
                         201, ORDER_FIELDS)
        require(isinstance(created["order_id"], int) and created["order_id"] > 0 and
                created["status"] == "created", "新订单标识或状态不符")
        expected_items = [dict(item, unit_price_cents=product["price_cents"])
                          for item, product in zip(inputs, products)]
        require(created["items"] == expected_items and all(set(item) == ORDER_ITEM_FIELDS for item in created["items"]),
                "订单商品、数量、排序或成交单价不符")
        expected_total = sum(item["quantity"] * item["unit_price_cents"] for item in expected_items)
        require(created["total_cents"] == expected_total, "订单整数总额不符")
        for product, item in zip(products, inputs):
            response = self.request("GET", f"/api/products/{product['id']}")
            detail = expect(response, 200, DETAIL_FIELDS)
            require(response.headers.get("x-benchmark-cache") == "miss" and
                    detail["stock"] == product["stock"] - item["quantity"], "库存扣减或缓存失效错误")
        before_replay = self.order_snapshot([product["id"] for product in products])
        require(before_replay == {"orders": 1, "items": len(inputs), "audit": 1,
                "stocks": {str(product["id"]): product["stock"] - item["quantity"]
                           for product, item in zip(products, inputs)}}, "订单最终数据库状态不符合一次提交")
        replayed = expect(self.request("POST", "/api/orders", key=key, body={"items": inputs}), 200, ORDER_FIELDS)
        require(replayed == created, "幂等重放未返回同一订单")
        changed = {"items": [dict(inputs[0], quantity=1), inputs[1]]}
        expect_error(self.request("POST", "/api/orders", key=key, body=changed), 409)
        require(self.order_snapshot([product["id"] for product in products]) == before_replay,
                "重放或幂等参数冲突产生了数据库副作用")
        stored = self.sql(f"""SELECT json_build_object('orders',count(*),'total',max(o.total_cents),
            'audit_total',max(a.total_cents),'audit_user',max(a.user_id)) FROM bench_orders o
            JOIN bench_order_audit a ON a.order_id=o.id WHERE o.id={created['order_id']}""")
        require(stored == {"orders": 1, "total": expected_total, "audit_total": expected_total, "audit_user": 1},
                "订单提交与审计记录不一致")
        return {"order_id": created["order_id"], "total_cents": expected_total, "items": len(inputs)}

    def concurrent_order(self):
        product = self.fixtures["products"][2]
        before = self.order_snapshot([product["id"]])
        key = uuid.uuid4().hex
        body = {"items": [{"product_id": product["id"], "quantity": 1}]}
        barrier = threading.Barrier(CONCURRENT_REPLAYS)

        def submit(_):
            barrier.wait(timeout=self.dataset["request_timeout_seconds"])
            return self.request("POST", "/api/orders", key=key, body=body)

        with concurrent.futures.ThreadPoolExecutor(max_workers=CONCURRENT_REPLAYS) as executor:
            responses = list(executor.map(submit, range(CONCURRENT_REPLAYS)))
        statuses = [response.status for response in responses]
        require(statuses.count(201) == 1 and statuses.count(200) == CONCURRENT_REPLAYS - 1,
                f"并发重放应只有一次新建: {statuses}")
        order = expect(responses[0], responses[0].status, ORDER_FIELDS)
        for response in responses[1:]:
            require(expect(response, response.status, ORDER_FIELDS) == order, "并发重放返回不同订单")
        after = self.order_snapshot([product["id"]])
        require(after["orders"] == before["orders"] + 1 and after["items"] == before["items"] + 1 and
                after["audit"] == before["audit"] + 1 and
                after["stocks"][str(product["id"])] == before["stocks"][str(product["id"])] - 1,
                "并发重放产生了重复提交或重复扣库存")
        return {"requests": CONCURRENT_REPLAYS, "created": 1, "replays": CONCURRENT_REPLAYS - 1}

    def rollback(self):
        first, unavailable = self.fixtures["products"][0], self.fixtures["products"][3]
        identifiers = [first["id"], unavailable["id"]]
        before = self.order_snapshot(identifiers)
        bad = {"items": [{"product_id": first["id"], "quantity": 1},
                         {"product_id": self.fixtures["other_product"], "quantity": 1}]}
        expect_error(self.request("POST", "/api/orders", key=uuid.uuid4().hex, body=bad), 404)
        require(self.order_snapshot(identifiers) == before, "跨租户商品失败未回滚先前库存和订单写入")
        # 制造正常的缺货业务状态，检查后恢复独立验收数据库中的原库存。
        self.environment.sql(f"UPDATE bench_products SET stock=0 WHERE id={unavailable['id']}")
        try:
            empty_before = self.order_snapshot(identifiers)
            bad["items"][1]["product_id"] = unavailable["id"]
            expect_error(self.request("POST", "/api/orders", key=uuid.uuid4().hex, body=bad), 409)
            require(self.order_snapshot(identifiers) == empty_before, "缺货失败未完整回滚事务")
        finally:
            self.environment.sql(f"UPDATE bench_products SET stock={before['stocks'][str(unavailable['id'])]} "
                                 f"WHERE id={unavailable['id']}")
        return {"rollback_cases": ["foreign_tenant_product", "insufficient_stock"]}

    def input_boundaries(self):
        product_id = self.fixtures["products"][0]["id"]
        before = self.order_snapshot([product_id])
        valid = {"items": [{"product_id": product_id, "quantity": 1}]}
        expect_error(self.request("POST", "/api/orders", key="invalid", body=valid), 422)
        invalid = [{"items": []}, {"items": [{"product_id": product_id, "quantity": 0}]},
                   {"items": valid["items"] * 2},
                   {"items": [{"product_id": product_id, "quantity": self.dataset["maximum_item_quantity"] + 1}]},
                   {"items": valid["items"], "tenant_id": 2},
                   {"items": [{"product_id": product_id, "quantity": 1, "price": 1}]}]
        for body in invalid:
            expect_error(self.request("POST", "/api/orders", key=uuid.uuid4().hex, body=body), 422)
        ticket_path = f"/api/tickets/{self.fixtures['ticket']['id']}/comments"
        ticket_before = self.ticket_state(self.fixtures["ticket"]["id"])
        # 重复字段的两个值都满足业务长度，确保拒绝来自成员唯一性而非短文本校验。
        invalid_documents = [b"{", b"null", b"{}{}", b'{"body":1}',
                             b'{"body":"first-valid-comment-body","body":"second-valid-comment-body"}',
                             b'{"body":"valid-comment-body","extra":true}',
                             b'{"Body":"valid-comment-body"}']
        for raw in invalid_documents:
            expect_error(self.request("POST", ticket_path, raw=raw), 422)
        invalid_media = [
            {"Content-Type": "text/plain"}, {"Content-Type": "application/json; charset=gbk"},
            {"Content-Encoding": "gzip"}, {"Content-Type": "application/json; extra=1"},
            {"Content-Type": "application/json; charset=utf-8; charset=utf-8"},
            {"Content-Type": "application/json; charset=\"\""}, {"Content-Type": "application/json;"},
        ]
        for headers in invalid_media:
            expect_error(self.request("POST", ticket_path, body={"body": "valid-comment-body"},
                                      extra_headers=headers), 422)
        oversized = self.request("POST", ticket_path, raw=b"x" * (self.dataset["maximum_body_bytes"] + 1))
        require(oversized.status in {413, 422}, "超出合同上限的请求体没有被拒绝")
        require(self.order_snapshot([product_id]) == before, "参数校验失败产生订单副作用")
        require(self.ticket_state(self.fixtures["ticket"]["id"]) == ticket_before,
                "非法请求体仍然写入了工单评论")
        return {"invalid_order_inputs": len(invalid) + 1, "invalid_json_bodies": len(invalid_documents),
                "invalid_media": len(invalid_media), "oversized_rejected": True}

    def ticket_state(self, ticket_id):
        return self.sql(f"""SELECT json_build_object('version',version,'status',status,
            'comments',(SELECT count(*) FROM bench_comments WHERE ticket_id={ticket_id}))
            FROM bench_tickets WHERE id={ticket_id}""")

    def ticket(self):
        ticket = self.fixtures["ticket"]
        path = f"/api/tickets/{ticket['id']}/comments"
        before = self.ticket_state(ticket["id"])
        body = "已核对商品与订单信息，继续跟踪售后处理进度。"
        created = expect(self.request("POST", path, body={"body": "  " + body + "  "}), 201, COMMENT_FIELDS)
        require(created["ticket_id"] == ticket["id"] and created["version"] == before["version"] + 1 and
                created["status"] == "open" and isinstance(created["comment_id"], int) and
                created["comment_id"] > 0, "工单评论标识或版本不符")
        after = self.ticket_state(ticket["id"])
        require(after == {"version": before["version"] + 1, "status": "open", "comments": before["comments"] + 1},
                "工单版本与评论写入不一致")
        stored = self.sql(f"SELECT json_build_object('body',body,'user_id',user_id) FROM bench_comments "
                          f"WHERE id={created['comment_id']}")
        require(stored == {"body": body, "user_id": 1}, "评论内容规范化或所有者错误")
        for identifier, status in [(self.fixtures["unowned_ticket"], 403), (self.fixtures["other_ticket"], 404)]:
            state = self.ticket_state(identifier)
            expect_error(self.request("POST", f"/api/tickets/{identifier}/comments", body={"body": body}), status)
            require(self.ticket_state(identifier) == state, "未授权评论修改了目标工单")
        expect_error(self.request("POST", path, body={"body": "太短"}), 422)
        require(self.ticket_state(ticket["id"]) == after, "短评论校验失败仍修改了工单")
        self.environment.sql(f"UPDATE bench_tickets SET status='closed' WHERE id={ticket['id']}")
        closed = self.ticket_state(ticket["id"])
        expect_error(self.request("POST", path, body={"body": body}), 409)
        require(self.ticket_state(ticket["id"]) == closed, "已关闭工单仍被追加评论")
        return {"ticket_id": ticket["id"], "version": created["version"], "ownership_checked": True,
                "closed_checked": True}


def verify_contract(environment, secret):
    """应用已经启动且数据库刚重置；失败会保留逐项证据并阻止结果标为通过。"""
    verifier = Verifier(environment, secret)
    if verifier.run("fresh_database", verifier.fresh) and verifier.run("seed_fixtures", verifier.load_fixtures):
        for name, action in [("authentication", verifier.authentication), ("catalog", verifier.catalog),
                             ("detail_cache_tenant", verifier.detail), ("order_commit_replay", verifier.order),
                             ("concurrent_idempotency", verifier.concurrent_order), ("transaction_rollback", verifier.rollback),
                             ("input_boundaries", verifier.input_boundaries), ("ticket_permissions_version", verifier.ticket)]:
            verifier.run(name, action)
    return {"passed": all(check["passed"] for check in verifier.checks), "checks": verifier.checks}


def preheat(environment, secret):
    """预热每租户规定数量的热点，并用第二次读取验证 Redis 命中。"""
    dataset = environment.dataset
    count = dataset["tenant_count"] * dataset["hot_products_per_tenant"]
    products = json.loads(environment.sql("SELECT json_agg(p ORDER BY tenant_id,id) FROM ("
                         "SELECT tenant_id,id,row_number() OVER(PARTITION BY tenant_id ORDER BY id) AS rank "
                         "FROM bench_products WHERE active) p WHERE rank<=" + str(dataset["hot_products_per_tenant"])))
    require(isinstance(products, list) and len(products) == count, "热点商品数不符合数据合同")
    client = Client(environment.url, dataset["request_timeout_seconds"])
    tokens = {tenant: authenticate(secret, tenant, (tenant - 1) * dataset["users_per_tenant"] + 1)
              for tenant in range(1, dataset["tenant_count"] + 1)}
    initial = {"hit": 0, "miss": 0}
    workers = dataset["database_connection_budget"]

    def read(product):
        response = client.request("GET", f"/api/products/{product['id']}", token=tokens[product["tenant_id"]])
        detail = expect(response, 200)
        require(detail.get("id") == product["id"], "热点详情标识错误")
        state = response.headers.get("x-benchmark-cache")
        require(state in initial, "缓存状态响应头缺失")
        return state

    # 在共同连接预算内并发预热；确认整轮完成后再开始第二轮命中验证。
    with concurrent.futures.ThreadPoolExecutor(max_workers=workers) as executor:
        for state in executor.map(read, products):
            initial[state] += 1
        for state in executor.map(read, products):
            require(state == "hit", "热点预热后仍未命中 Redis")
    return {"passed": True, "hot_products": count, "requests": count * 2,
            "initial_cache": initial, "verified_hits": count, "workers": workers}
