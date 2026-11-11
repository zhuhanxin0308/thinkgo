"""目录验收必须以实际种子记录为准，覆盖短页、空页及错误响应。"""

import copy
import json
import unittest
import urllib.parse
from unittest import mock

import verify


class CatalogVerificationTests(unittest.TestCase):
    def verifier(self, count, corrupt=None):
        dataset = {"request_timeout_seconds": 5, "products_per_tenant": 200,
                   "users_per_tenant": 1000, "maximum_page_size": 100}
        environment = mock.Mock(url="http://127.0.0.1:18081", dataset=dataset)
        rows = [{"id": index + 1, "name": f"商品-{index}", "price_cents": 100 + index,
                 "category": "分类-1", "stock": 50} for index in range(count)]
        other_rows = [dict(row, id=row["id"] + dataset["products_per_tenant"]) for row in rows]
        environment.sql.side_effect = lambda query: json.dumps(
            (other_rows if "p.tenant_id = 2" in query else rows)[:5])
        verifier = verify.Verifier(environment, "secret")

        def request(method, path, **kwargs):
            self.assertEqual(method, "GET")
            query = urllib.parse.parse_qs(urllib.parse.urlsplit(path).query)
            # 只为本组成功合同提供业务响应，其余输入对应既有非法查询验收。
            valid = (query == {"category_id": ["1"], "limit": ["2"]} or
                     query == {"limit": ["2"]} or
                     query == {"min_price": ["0"], "max_price": ["0"]} or
                     set(query) == {"category_id", "min_price", "max_price"} or
                     set(query) == {"category_id", "limit", "cursor_price", "cursor_id"})
            if not valid or path.endswith("&") or "?&" in path:
                return verify.Response(422, {"content-type": "application/json"},
                                       {"error": {"code": "validation_error", "message": "参数无效"}})
            selected = other_rows if kwargs.get("token", "").startswith("Bearer 2.") else rows
            if "min_price" in query:
                selected = [row for row in selected if int(query["min_price"][0]) <= row["price_cents"] <= int(query["max_price"][0])]
            if "cursor_id" in query:
                cursor = (int(query["cursor_price"][0]), int(query["cursor_id"][0]))
                selected = [row for row in selected if (row["price_cents"], row["id"]) > cursor]
            limit = int(query.get("limit", ["20"])[0])
            page = copy.deepcopy(selected[:limit])
            cursor = {key: page[-1][key] for key in ("price_cents", "id")} if len(selected) > limit else None
            body = {"items": page, "next_cursor": cursor}
            if corrupt is not None and query == {"category_id": ["1"], "limit": ["2"]}:
                corrupt(body)
            return verify.Response(200, {"content-type": "application/json"}, body)

        verifier.request = request
        return verifier

    def test_sparse_and_full_categories(self):
        for count in (0, 1, 2, 3, 4, 5, 8):
            with self.subTest(count=count):
                result = self.verifier(count).catalog()
                self.assertEqual(result["ordered_unique_products"], min(count, 4))

    def test_wrong_content_and_cursor_still_fail(self):
        mutations = {
            "提前结束": lambda body: body.update(next_cursor=None),
            "错误游标": lambda body: body["next_cursor"].update(id=999),
            "额外字段": lambda body: body["items"][0].update(internal_cost=1),
            "错误分类": lambda body: body["items"][0].update(category="分类-2"),
            "错误租户": lambda body: body["items"][0].update(id=201),
            "缺少商品": lambda body: body["items"].pop(),
            "重复商品": lambda body: body["items"].__setitem__(1, body["items"][0]),
            "顺序错误": lambda body: body["items"].reverse(),
        }
        for name, mutation in mutations.items():
            with self.subTest(name=name), self.assertRaises(AssertionError):
                self.verifier(5, mutation).catalog()


if __name__ == "__main__":
    unittest.main()
