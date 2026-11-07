"""外部契约验收器的边界测试；真实服务验收由显式环境变量启用。"""

import hashlib
import hmac
import http.server
import json
import os
import pathlib
import threading
import unittest
from unittest import mock

import verify


class LocalHandler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path == "/redirect":
            self.send_response(302)
            self.send_header("Location", "https://example.invalid/")
            self.end_headers()
            return
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(json.dumps({"path": self.path}).encode())

    def do_POST(self):
        size = int(self.headers.get("Content-Length", "0"))
        body = self.rfile.read(size)
        self.send_response(422)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(json.dumps({"body": body.decode(), "key": self.headers.get("Idempotency-Key"),
                                    "authorization": self.headers.get("Authorization"),
                                    "content_type": self.headers.get("Content-Type"),
                                    "content_encoding": self.headers.get("Content-Encoding")}).encode())

    def log_message(self, *_):
        pass


class ClientTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), LocalHandler)
        cls.thread = threading.Thread(target=cls.server.serve_forever, daemon=True)
        cls.thread.start()

    @classmethod
    def tearDownClass(cls):
        cls.server.shutdown()
        cls.server.server_close()
        cls.thread.join()

    def setUp(self):
        self.client = verify.Client(f"http://127.0.0.1:{self.server.server_port}", 2)

    def test_token_has_exact_claims_and_hmac(self):
        header = verify.authenticate("测试密钥", 2, 1001, expires_at=2000000000)
        claims = "2.1001.2000000000"
        signature = hmac.new("测试密钥".encode(), claims.encode(), hashlib.sha256).hexdigest()
        self.assertEqual(header, f"Bearer {claims}.{signature}")
        with mock.patch("verify.time.time", return_value=100):
            self.assertIn(f".3600.", verify.authenticate("key", 1, 1, expires_at=3600))
            self.assertIn(f".{100 + verify.TOKEN_LIFETIME_SECONDS}.", verify.authenticate("key", 1, 1))

    def test_client_roundtrip_preserves_json_and_headers(self):
        response = self.client.request("POST", "/comments", token="Bearer fixture", key="abc",
                                       body={"body": "中文评论"})
        self.assertEqual(response.status, 422)
        self.assertEqual(json.loads(response.body["body"]), {"body": "中文评论"})
        self.assertEqual(response.body["key"], "abc")
        self.assertEqual(response.body["authorization"], "Bearer fixture")
        self.assertEqual(self.client.request("GET", "/healthz").body, {"path": "/healthz"})

    def test_raw_body_and_redirect_are_not_reinterpreted(self):
        response = self.client.request("POST", "/comments", raw=b"{")
        self.assertEqual(response.body["body"], "{")
        redirect = self.client.request("GET", "/redirect")
        self.assertEqual(redirect.status, 302)
        self.assertIsNone(redirect.body)

    def test_media_headers_can_be_verified_without_changing_the_body(self):
        result = self.client.request("POST", "/comments", raw=b'{"body":"valid-comment-body"}',
                                     extra_headers={"Content-Type": "text/plain", "Content-Encoding": "gzip"})
        self.assertEqual(result.body["content_type"], "text/plain")
        self.assertEqual(result.body["content_encoding"], "gzip")

    def test_only_loopback_root_urls_and_local_paths_are_allowed(self):
        for url in ["https://127.0.0.1", "http://example.invalid", "http://user@127.0.0.1",
                    "http://127.0.0.1/prefix", "http://127.0.0.1?x=1", "http://127.0.0.1/#x"]:
            with self.subTest(url=url), self.assertRaises(ValueError):
                verify.Client(url, 1)
        for path in ["http://example.invalid", "//example.invalid", "/bad\npath"]:
            with self.subTest(path=path), self.assertRaises(ValueError):
                self.client.request("GET", path)


class AssertionTests(unittest.TestCase):
    def test_response_checks_exact_whitelist_and_json_media_type(self):
        good = verify.Response(200, {"content-type": "application/json; charset=utf-8"}, {"id": 1})
        self.assertEqual(verify.expect(good, 200, {"id"}), {"id": 1})
        cases = [verify.Response(201, good.headers, good.body),
                 verify.Response(200, {}, good.body),
                 verify.Response(200, good.headers, {"id": 1, "internal_cost": 2}),
                 verify.Response(200, good.headers, None)]
        for response in cases:
            with self.subTest(response=response), self.assertRaises(AssertionError):
                verify.expect(response, 200, {"id"})

    def test_errors_have_only_public_fields(self):
        response = verify.Response(401, {"content-type": "application/json"},
                                   {"error": {"code": "unauthorized", "message": "凭证无效"}})
        verify.expect_error(response, 401)
        response.body["error"]["trace"] = "internal"
        with self.assertRaises(AssertionError):
            verify.expect_error(response, 401)

    def test_failure_is_recorded_without_becoming_success(self):
        environment = mock.Mock(url="http://127.0.0.1:18081", dataset={"request_timeout_seconds": 5})
        verifier = verify.Verifier(environment, "secret")
        verifier.run("success", lambda: {"rows": 2})
        verifier.run("failed", lambda: verify.require(False, "断言失败"))
        self.assertEqual(verifier.checks[0], {"name": "success", "passed": True, "evidence": {"rows": 2}})
        self.assertEqual(verifier.checks[1], {"name": "failed", "passed": False, "error": "断言失败"})

    def test_malformed_response_shape_is_a_failed_check(self):
        environment = mock.Mock(url="http://127.0.0.1:18081", dataset={"request_timeout_seconds": 5})
        verifier = verify.Verifier(environment, "secret")
        self.assertFalse(verifier.run("missing_field", lambda: {}["required"]))
        self.assertFalse(verifier.run("wrong_type", lambda: len(None)))
        self.assertTrue(all(not check["passed"] for check in verifier.checks))

    def test_stale_database_fails_before_http_mutations(self):
        environment = mock.Mock(url="http://127.0.0.1:18081", dataset={"request_timeout_seconds": 5})
        environment.sql.return_value = '{"orders":1,"order_items":1,"audit":1}'
        with mock.patch.object(verify.Client, "request") as request:
            result = verify.verify_contract(environment, "secret")
        self.assertFalse(result["passed"])
        self.assertEqual(len(result["checks"]), 1)
        request.assert_not_called()

    def test_preheat_verifies_all_hot_products_on_second_pass(self):
        environment = mock.Mock(url="http://127.0.0.1:18081", dataset={
            "request_timeout_seconds": 5, "tenant_count": 2, "hot_products_per_tenant": 2,
            "users_per_tenant": 1000, "database_connection_budget": 2})
        environment.sql.return_value = '[{"tenant_id":1,"id":1},{"tenant_id":1,"id":2},' \
                                       '{"tenant_id":2,"id":101},{"tenant_id":2,"id":102}]'
        visited = set()
        wave = threading.Barrier(2)

        def request(_, path, **kwargs):
            wave.wait(timeout=2)
            state = "hit" if path in visited else "miss"
            visited.add(path)
            return verify.Response(200, {"content-type": "application/json", "x-benchmark-cache": state},
                                   {"id": int(path.rsplit("/", 1)[1])})

        with mock.patch.object(verify.Client, "request", side_effect=request) as calls:
            result = verify.preheat(environment, "secret")
        self.assertEqual(result["hot_products"], 4)
        self.assertEqual(result["verified_hits"], 4)
        self.assertEqual(result["initial_cache"], {"hit": 0, "miss": 4})
        self.assertEqual(calls.call_count, 8)
        def always_miss(_, path, **kwargs):
            return verify.Response(200, {"content-type": "application/json", "x-benchmark-cache": "miss"},
                                   {"id": int(path.rsplit("/", 1)[1])})

        with mock.patch.object(verify.Client, "request", side_effect=always_miss):
            with self.assertRaisesRegex(AssertionError, "热点预热后仍未命中"):
                verify.preheat(environment, "secret")


@unittest.skipUnless(os.getenv("BENCH_VERIFY_INTEGRATION") == "1", "需要显式启动真实基准依赖")
class IntegrationTests(unittest.TestCase):
    def test_full_http_contract_and_cache(self):
        from native import Environment

        root = pathlib.Path(__file__).resolve().parents[1]
        runtime = pathlib.Path(os.environ["BENCH_RUNTIME"])
        protocol = json.loads((root / "contracts/protocol.json").read_text())
        environment = Environment(root, runtime, runtime / "run/verify-agent", protocol)
        secret = "local-contract-verification-secret"
        try:
            environment.start()
            environment.reset()
            variant = next(item for item in protocol["variants"] if item["id"] == "thinkgo")
            environment.start_app(variant, "bench:verify-agent", secret)
            result = verify.verify_contract(environment, secret)
            self.assertTrue(result["passed"], json.dumps(result, ensure_ascii=False, indent=2))
            warmed = verify.preheat(environment, secret)
            self.assertEqual(warmed["verified_hits"], environment.dataset["tenant_count"] *
                             environment.dataset["hot_products_per_tenant"])
        finally:
            environment.close()


if __name__ == "__main__":
    unittest.main()
