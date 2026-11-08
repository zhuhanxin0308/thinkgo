"""本地服务编排的配置和资源所有权约束。"""

import copy
import hashlib
import json
import os
import pathlib
import secrets
import signal
import subprocess
import tempfile
import unittest
import urllib.error
from unittest import mock

import native
from native import Environment, postgres_config, redis_config, gateway_config, fpm_config, octane_command


ROOT = pathlib.Path(__file__).resolve().parents[1]


class FakeClock:
    """让启动期限测试不依赖真实睡眠或宿主调度。"""

    def __init__(self):
        self.now = 0.0
        self.sleeps = []

    def monotonic(self):
        return self.now

    def sleep(self, duration):
        self.sleeps.append(duration)
        self.now += duration


class ReadinessWaitTest(unittest.TestCase):
    def setUp(self):
        self.clock = FakeClock()
        self.process = mock.Mock(command=["owned-service"])
        mock.patch.object(native.time, "monotonic", self.clock.monotonic).start()
        mock.patch.object(native.time, "sleep", self.clock.sleep).start()
        self.addCleanup(mock.patch.stopall)

    def test_wait_retries_only_not_ready_and_checks_before_and_after_each_probe(self):
        probe = mock.Mock(side_effect=[native.ServiceNotReady("recovery"), False, True])
        native.wait_for(probe, self.process)
        self.assertEqual(probe.call_count, 3)
        self.assertEqual(self.process.check.call_count, 6)
        self.assertEqual(self.clock.sleeps, [native.READINESS_POLL_SECONDS] * 2)
        for call in probe.call_args_list:
            self.assertEqual(call.kwargs, {"timeout": native.READINESS_PROBE_SECONDS})

    def test_probe_budget_is_capped_by_remaining_deadline(self):
        budgets = []

        def stalled_probe(*, timeout):
            budgets.append(timeout)
            self.clock.now += timeout
            raise native.ServiceNotReady("last connection timeout")

        with mock.patch.object(native, "STARTUP_SECONDS", 0.35), \
                mock.patch.object(native, "READINESS_PROBE_SECONDS", 0.2):
            with self.assertRaisesRegex(TimeoutError, "last connection timeout"):
                native.wait_for(stalled_probe, self.process)
        self.assertEqual(len(budgets), 2)
        self.assertAlmostEqual(budgets[0], 0.2)
        self.assertAlmostEqual(budgets[1], 0.05)
        self.assertAlmostEqual(self.clock.now, 0.35)
        self.assertEqual(self.clock.sleeps, [native.READINESS_POLL_SECONDS])

    def test_sleep_is_capped_and_expired_deadline_starts_no_more_probe(self):
        def unready(*, timeout):
            self.clock.now += 0.11
            return False

        probe = mock.Mock(side_effect=unready)
        with mock.patch.object(native, "STARTUP_SECONDS", 0.15):
            with self.assertRaises(TimeoutError):
                native.wait_for(probe, self.process)
        probe.assert_called_once()
        self.assertAlmostEqual(self.clock.sleeps[0], 0.04)
        self.assertAlmostEqual(self.clock.now, 0.15)

    def test_success_after_deadline_is_not_accepted(self):
        # 模拟分段传输等回调超出预算的情形，只验证返回后拒绝迟到成功。
        def late_success(*, timeout):
            self.clock.now += timeout + 0.01
            return True

        with mock.patch.object(native, "STARTUP_SECONDS", 0.1):
            with self.assertRaises(TimeoutError):
                native.wait_for(late_success, self.process)
        self.assertEqual(self.process.check.call_count, 2)
        self.assertEqual(self.clock.sleeps, [])

    def test_process_exit_before_or_after_probe_is_not_retried(self):
        for before, outcome in [(True, True), (False, True), (False, native.ServiceNotReady("recovery"))]:
            with self.subTest(before=before, outcome=outcome):
                failed = RuntimeError("owned postgres exited: 7")
                self.process.check.side_effect = [failed] if before else [None, failed]
                probe = mock.Mock(side_effect=outcome) if isinstance(outcome, Exception) else mock.Mock(return_value=outcome)
                with self.assertRaises(RuntimeError) as caught:
                    native.wait_for(probe, self.process)
                self.assertIs(caught.exception, failed)
                self.assertEqual(probe.call_count, 0 if before else 1)
                self.assertEqual(self.clock.sleeps, [])

    def test_fatal_probe_errors_and_interrupt_are_not_swallowed(self):
        for error in [FileNotFoundError("missing psql"), PermissionError("psql denied"),
                      ValueError("invalid readiness output"), RuntimeError("invalid command"), KeyboardInterrupt()]:
            with self.subTest(error=type(error).__name__):
                self.process.reset_mock()
                probe = mock.Mock(side_effect=error)
                with self.assertRaises(type(error)) as caught:
                    native.wait_for(probe, self.process)
                self.assertIs(caught.exception, error)
                probe.assert_called_once()
                self.assertEqual(self.process.check.call_count, 2)
                self.assertEqual(self.clock.sleeps, [])

    def test_fatal_probe_error_survives_simultaneous_process_exit(self):
        for error in [FileNotFoundError("missing psql"), PermissionError("psql denied"),
                      ValueError("invalid readiness output"), KeyboardInterrupt()]:
            with self.subTest(error=type(error).__name__):
                self.process.check.side_effect = [None, RuntimeError("owned postgres exited: 7")]
                probe = mock.Mock(side_effect=error)
                with self.assertRaises(type(error)) as caught:
                    native.wait_for(probe, self.process)
                self.assertIs(caught.exception, error)
                self.assertIn("owned postgres exited: 7", "\n".join(caught.exception.__notes__))
                probe.assert_called_once()
                self.assertEqual(self.clock.sleeps, [])

    def test_tcp_probe_forwards_timeout_and_only_retries_network_unavailability(self):
        with mock.patch.object(native.socket, "create_connection") as connect:
            self.assertTrue(native.tcp_ready(12345, timeout=0.25))
            connect.assert_called_once_with(("127.0.0.1", 12345), timeout=0.25)
        for error in [ConnectionRefusedError("not listening"), TimeoutError("socket timeout")]:
            with self.subTest(error=type(error).__name__), \
                    mock.patch.object(native.socket, "create_connection", side_effect=error):
                with self.assertRaises(native.ServiceNotReady):
                    native.tcp_ready(12345, timeout=0.25)
        with mock.patch.object(native.socket, "create_connection", side_effect=PermissionError("denied")):
            with self.assertRaises(PermissionError):
                native.tcp_ready(12345, timeout=0.25)


class NativeTest(unittest.TestCase):
    def setUp(self):
        self.protocol = json.loads((ROOT / "contracts/protocol.json").read_text())
        self.dataset = json.loads((ROOT / "contracts/dataset.json").read_text())

    def test_database_keeps_real_durability_and_local_tcp(self):
        value = postgres_config(self.protocol)
        for setting in ["fsync = on", "synchronous_commit = on", "full_page_writes = on",
                        "unix_socket_directories = ''", "listen_addresses = '127.0.0.1'",
                        "shared_preload_libraries = 'pg_stat_statements'"]:
            self.assertIn(setting, value)
        self.assertIn("save \"\"", redis_config(self.protocol))
        self.assertIn("appendonly no", redis_config(self.protocol))

    def test_gateway_uses_native_protocol_without_response_cache(self):
        with tempfile.TemporaryDirectory() as directory:
            work = pathlib.Path(directory)
            go = gateway_config(self.protocol, self.dataset, work, ROOT, {"kind": "go"})
            php = gateway_config(self.protocol, self.dataset, work, ROOT, {"kind": "fpm", "app": "laravel"})
            self.assertIn("proxy_http_version 1.1", go)
            self.assertIn("fastcgi_pass 127.0.0.1:19000", php)
            self.assertIn("HTTP_AUTHORIZATION $http_authorization", php)
            self.assertIn("PATH_INFO $uri", php)
            self.assertNotIn("fastcgi_cache", php)
            self.assertNotIn("proxy_cache", go)
            self.assertIn("access_log off", go)

    def test_fpm_budget_and_production_flags_are_explicit(self):
        value = fpm_config(self.protocol, self.dataset, pathlib.Path("/tmp/owned"))
        for setting in ["pm = static", "pm.max_children = 24", "clear_env = no",
                        "php_admin_flag[display_errors] = off", "php_admin_value[opcache.jit] = 0"]:
            self.assertIn(setting, value)

    def test_existing_unowned_directory_is_never_reused(self):
        with tempfile.TemporaryDirectory() as directory:
            work = pathlib.Path(directory)
            (work / "postgres").mkdir()
            environment = Environment(ROOT, work, work, self.protocol)
            with self.assertRaises(ValueError):
                environment.prepare()
            self.assertTrue((work / "postgres").is_dir())

    def test_octane_uses_official_server_and_shared_worker_budget(self):
        command = octane_command(pathlib.Path("/runtime"), self.protocol, self.dataset)
        self.assertEqual(command[0], "/runtime/php/bin/php")
        for option in ["--server=roadrunner", "--workers=24", "--max-requests=0", "--rpc-port=16081"]:
            self.assertIn(option, command)


class PostgresStartupTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.folder = pathlib.Path(self.temporary.name)
        self.protocol = json.loads((ROOT / "contracts/protocol.json").read_text())
        self.environment = Environment(ROOT, self.folder / "runtime", self.folder / "work", self.protocol)
        self.addCleanup(self.environment.close)
        dataset = self.environment.dataset
        tenants = dataset["tenant_count"]
        self.counts = {"tenants": tenants, "users": tenants * dataset["users_per_tenant"],
                       "products": tenants * dataset["products_per_tenant"],
                       "specifications": tenants * dataset["products_per_tenant"] * dataset["specifications_per_product"],
                       "tickets": tenants * dataset["tickets_per_tenant"],
                       "comments": tenants * dataset["tickets_per_tenant"] * dataset["initial_comments_per_ticket"],
                       "orders": 0}

    def test_start_waits_for_writable_sql_before_template_queries(self):
        # 端口始终已开放，数据库依次经历拒绝连接、只读恢复和可写就绪。
        ready = False
        responses = iter([(2, "", "FATAL: database system is not yet accepting connections"),
                          (0, "f\n", ""), (0, "t\n", "")])

        def probe(command, **options):
            nonlocal ready
            code, stdout, stderr = next(responses)
            ready = stdout == "t\n"
            self.assertEqual(options["env"]["PGDATABASE"], "postgres")
            self.assertEqual(options["env"]["PGPORT"], str(self.protocol["ports"]["postgres"]))
            self.assertIn("-w", command)
            self.assertIn("-XAtq", command)
            self.assertIn("ON_ERROR_STOP=1", command)
            self.assertIn("pg_is_in_recovery()", command[-1])
            self.assertIn("transaction_read_only", command[-1])
            return subprocess.CompletedProcess(command, code, stdout, stderr)

        def template_query(query, database):
            self.assertTrue(ready, "TCP 已开放但 SQL 尚未就绪时不得查询模板")
            if "pg_database" in query:
                self.assertEqual(database, "postgres")
                return "1"
            return json.dumps(self.counts)

        postgres = mock.Mock(command=["postgres"])
        redis = mock.Mock(command=["redis"])
        with mock.patch.object(self.environment, "prepare"), \
                mock.patch.object(native, "Process", side_effect=[postgres, redis]), \
                mock.patch.object(native, "tcp_ready", return_value=True), \
                mock.patch.object(native.subprocess, "run", side_effect=probe) as command, \
                mock.patch.object(native.time, "sleep"), \
                mock.patch.object(self.environment, "sql", side_effect=template_query) as sql:
            self.environment.start()
        self.assertEqual(command.call_count, 3)
        self.assertEqual(sql.call_count, 2)
        self.assertEqual(self.environment.seed_counts, self.counts)

    def test_postgres_probe_retries_connection_loss_timeout_and_read_only_state(self):
        responses = [subprocess.CompletedProcess(["psql"], 2, "", "FATAL: recovery in progress"),
                     subprocess.TimeoutExpired(["psql"], 0.25, stderr="handshake stalled"),
                     subprocess.CompletedProcess(["psql"], 0, "f\n", "")]
        for response in responses:
            options = {"side_effect": response} if isinstance(response, Exception) else {"return_value": response}
            with self.subTest(response=response), mock.patch.object(native.subprocess, "run", **options) as command:
                with self.assertRaises(native.ServiceNotReady):
                    self.environment.postgres_ready(timeout=0.25)
                self.assertEqual(command.call_args.kwargs["timeout"], 0.25)

    def test_postgres_probe_preserves_fatal_errors_through_waiter(self):
        invalid = [subprocess.CompletedProcess(["psql"], code, "", "invalid command or SQL") for code in [1, 3, -9]]
        invalid.extend(subprocess.CompletedProcess(["psql"], 0, output, "") for output in ["", "true", "t\nf", "NULL"])
        invalid.extend([FileNotFoundError("missing psql"), PermissionError("psql denied")])
        for response in invalid:
            options = {"side_effect": response} if isinstance(response, Exception) else {"return_value": response}
            expected = type(response) if isinstance(response, Exception) else (RuntimeError if response.returncode else ValueError)
            with self.subTest(response=response), mock.patch.object(native.subprocess, "run", **options) as command, \
                    mock.patch.object(native.time, "sleep") as sleep:
                with self.assertRaises(expected):
                    native.wait_for(self.environment.postgres_ready, mock.Mock(command=["postgres"]))
                command.assert_called_once()
                sleep.assert_not_called()

    def test_failed_postgres_start_keeps_owned_process_for_normal_cleanup(self):
        postgres = mock.Mock(command=["postgres"])
        with mock.patch.object(self.environment, "prepare"), \
                mock.patch.object(native, "Process", return_value=postgres) as process, \
                mock.patch.object(self.environment, "postgres_ready", side_effect=PermissionError("psql denied")), \
                mock.patch.object(self.environment, "sql") as sql:
            with self.assertRaises(PermissionError):
                self.environment.start()
            self.assertIs(self.environment.infrastructure["postgres"], postgres)
            process.assert_called_once()
            sql.assert_not_called()
            self.environment.close()
        postgres.stop.assert_called_once()
        self.assertEqual(self.environment.infrastructure, {})

    def test_health_probe_forwards_budget_and_classifies_unready_responses(self):
        with mock.patch.object(native.urllib.request, "build_opener") as build:
            opener = build.return_value
            response = opener.open.return_value.__enter__.return_value
            response.status = 200
            response.read.return_value = b'{"status":"ok"}'
            self.assertTrue(self.environment.healthy(timeout=0.25))
            opener.open.assert_called_once_with(self.environment.url + "/healthz", timeout=0.25)
            response.status = 503
            self.assertFalse(self.environment.healthy(timeout=0.25))
            response.status = 200
            response.read.return_value = b"not json"
            with self.assertRaises(native.ServiceNotReady):
                self.environment.healthy(timeout=0.25)
            opener.open.side_effect = urllib.error.URLError("connection refused")
            with self.assertRaises(native.ServiceNotReady):
                self.environment.healthy(timeout=0.25)
            opener.open.side_effect = PermissionError("denied")
            with self.assertRaises(PermissionError):
                self.environment.healthy(timeout=0.25)

    def test_wrapped_http_file_and_permission_errors_are_not_retried(self):
        clock = FakeClock()
        for error in [FileNotFoundError("missing transport"), PermissionError("network denied")]:
            with self.subTest(error=type(error).__name__), \
                    mock.patch.object(native.urllib.request, "build_opener") as build, \
                    mock.patch.object(native.time, "monotonic", clock.monotonic), \
                    mock.patch.object(native.time, "sleep", clock.sleep):
                build.return_value.open.side_effect = urllib.error.URLError(error)
                with self.assertRaises(type(error)) as caught:
                    native.wait_for(self.environment.healthy, mock.Mock(command=["nginx"]))
                self.assertIs(caught.exception, error)
                build.return_value.open.assert_called_once()
                self.assertEqual(clock.sleeps, [])


@unittest.skipUnless(os.getenv("BENCH_READINESS_INTEGRATION") == "1", "需要显式启用测后启动恢复验收")
class ReadinessIntegrationTest(unittest.TestCase):
    def test_recovery_and_go_fpm_octane_http_contracts(self):
        from verify import verify_contract

        runtime = pathlib.Path(os.environ["BENCH_RUNTIME"])
        work = pathlib.Path(os.environ["BENCH_READINESS_WORK"])
        evidence = pathlib.Path(os.environ["BENCH_READINESS_EVIDENCE"])
        self.assertFalse(evidence.exists(), "测后验收证据不得覆盖")
        protocol = json.loads((ROOT / "contracts/protocol.json").read_text())
        secret = secrets.token_hex(32)
        environment = Environment(ROOT, runtime, work, protocol)
        try:
            environment.start()
            self.assertEqual(environment.sql("SELECT 1", "postgres"), "1")
            # 仅中断本测试专属数据库，真实验证下次启动会等待崩溃恢复完成。
            postgres = environment.infrastructure["postgres"]
            os.killpg(postgres.pid, signal.SIGKILL)
            crash_status = postgres.child.wait(timeout=native.STOP_SECONDS)
            self.assertEqual(crash_status, -signal.SIGKILL)
        finally:
            environment.close()
        environment = Environment(ROOT, runtime, work, protocol)
        checks = []
        try:
            environment.start()
            self.assertTrue(environment.postgres_ready())
            for identity in ["thinkgo", "thinkphp-fpm", "laravel-fpm", "laravel-octane"]:
                with self.subTest(variant=identity):
                    variant = next(item for item in protocol["variants"] if item["id"] == identity)
                    environment.reset()
                    try:
                        environment.start_app(variant, "post-measurement-readiness-" + identity, secret)
                        result = verify_contract(environment, secret)
                        self.assertTrue(result["passed"], json.dumps(result, ensure_ascii=False))
                        checks.append({"variant": identity, "kind": variant["kind"], "result": result})
                    finally:
                        environment.stop_app()
        finally:
            environment.close()
        record = {"scope": "post-measurement-startup-readiness", "crash_exit_status": crash_status,
                  "restart_sql_ready": True, "checks": checks,
                  "source_sha256": {name: hashlib.sha256((ROOT / "tools" / name).read_bytes()).hexdigest()
                                    for name in ["native.py", "test_native.py", "test_run.py"]}}
        with evidence.open("x") as output:
            json.dump(record, output, ensure_ascii=False, indent=2)
            output.write("\n")
        with evidence.with_suffix(".postgres.log").open("xb") as output:
            output.write((work / "logs/postgres.log").read_bytes())


class PhpSnapshotTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.folder = pathlib.Path(self.temporary.name)
        self.root = self.folder / "root"
        contracts = self.root / "contracts"
        contracts.mkdir(parents=True)
        for name in ["dataset.json", "queries.json"]:
            (contracts / name).write_bytes((ROOT / "contracts" / name).read_bytes())
        self.protocol = json.loads((ROOT / "contracts/protocol.json").read_text())
        self.environment = Environment(self.root, self.folder / "runtime", self.folder / "work", self.protocol)
        (self.environment.work / "config/contracts").mkdir(parents=True)
        (self.environment.work / "logs").mkdir()
        self.source = b"<?php\nreturn ['schema_version' => 1];\n"
        source_digests = {name: hashlib.sha256((contracts / name).read_bytes()).hexdigest()
                          for name in ["dataset.json", "queries.json"]}
        content_digest = hashlib.sha256(b"thinkgo-contract-v1\0" + "".join(source_digests.values()).encode("ascii")).hexdigest()
        self.path = self.environment.work / "config/contracts" / f"contract-{content_digest}.php"
        self.path.write_bytes(self.source)
        self.metadata = {"schema_version": 1, "path": str(self.path),
                         "file_sha256": hashlib.sha256(self.source).hexdigest(),
                         "content_sha256": content_digest,
                         "source_sha256": source_digests}
        self.variants = [variant for variant in self.protocol["variants"] if variant["kind"] != "go"]

    def command_result(self, command, **options):
        # 每一步都在调用时检查环境，防止可变字典掩盖注入时机错误。
        if pathlib.Path(command[1]).name == "compile-contract.php":
            self.assertNotIn("BENCH_CONTRACT_SNAPSHOT", options["environment"])
            self.assertEqual(list(map(str, command)), [str(self.environment.binary("php", "php")),
                             str(self.root / "php/shared/compile-contract.php"),
                             str(self.root / "contracts"), str(self.environment.work / "config/contracts")])
            return json.dumps(self.metadata)
        self.assertEqual(options["environment"]["BENCH_CONTRACT_SNAPSHOT"], str(self.path))
        return ""

    def test_three_php_modes_compile_before_caches_and_share_process_environment(self):
        for variant in self.variants:
            with self.subTest(variant=variant["id"]), \
                    mock.patch.object(self.environment, "command", side_effect=self.command_result) as command, \
                    mock.patch.object(native, "Process") as process, mock.patch.object(native, "wait_for"):
                variables = self.environment.start_app(variant, variant["id"], "private-secret")
                self.assertEqual(command.call_count, 3)
                expected = ["config:cache", "route:cache"] if variant["app"] == "laravel" else ["optimize:config", "optimize:route"]
                self.assertEqual([call.args[0][-1] for call in command.call_args_list[1:]], expected)
                self.assertEqual(self.environment.php_contract_snapshot, self.metadata)
                self.assertEqual(variables["BENCH_CONTRACT_SNAPSHOT"], str(self.path))
                for call in process.call_args_list:
                    self.assertIs(call.args[2], variables)
                    self.assertEqual(call.args[2]["BENCH_CONTRACT_SNAPSHOT"], str(self.path))
                self.environment.stop_app()

    def test_go_start_clears_prior_php_metadata_and_inherited_snapshot_path(self):
        self.environment.php_contract_snapshot = self.metadata
        self.environment.base_env["BENCH_CONTRACT_SNAPSHOT"] = str(self.path)
        go = next(variant for variant in self.protocol["variants"] if variant["kind"] == "go")
        with mock.patch.object(native, "Process"), mock.patch.object(native, "wait_for"), \
                mock.patch.object(self.environment, "prepare_php") as prepare:
            variables = self.environment.start_app(go, "go-round", "private-secret")
        self.assertIsNone(self.environment.php_contract_snapshot)
        self.assertNotIn("BENCH_CONTRACT_SNAPSHOT", variables)
        prepare.assert_not_called()

    def test_compiler_failure_stops_before_caches_or_processes(self):
        with mock.patch.object(self.environment, "command", side_effect=RuntimeError("compiler failed")) as command, \
                mock.patch.object(native, "Process") as process:
            with self.assertRaisesRegex(RuntimeError, "compiler failed"):
                self.environment.start_app(self.variants[0], "failed-round", "private-secret")
        self.assertEqual(command.call_count, 1)
        process.assert_not_called()
        self.assertIsNone(self.environment.php_contract_snapshot)

    def test_invalid_compiler_output_stops_before_framework_caches(self):
        invalid = ["not json", json.dumps([]), json.dumps(None), json.dumps({})]
        for field, value in [("schema_version", True), ("schema_version", 2), ("path", "relative.php"),
                             ("path", None), ("file_sha256", None), ("file_sha256", "invalid"), ("file_sha256", "0" * 64),
                             ("content_sha256", "0" * 64), ("BENCH_SECRET", "private-secret")]:
            metadata = copy.deepcopy(self.metadata)
            metadata[field] = value
            invalid.append(json.dumps(metadata))
        for source in [None, [], {}, {**self.metadata["source_sha256"], "secret": "private-secret"},
                       {**self.metadata["source_sha256"], "dataset.json": "0" * 64}]:
            metadata = copy.deepcopy(self.metadata)
            metadata["source_sha256"] = source
            invalid.append(json.dumps(metadata))
        for document in invalid:
            with self.subTest(document=document), \
                    mock.patch.object(self.environment, "command", return_value=document) as command:
                with self.assertRaises(ValueError):
                    self.environment.prepare_php(self.variants[0], {})
                self.assertEqual(command.call_count, 1)

    def test_snapshot_outside_compiler_directory_is_rejected(self):
        outside = self.folder / self.path.name
        outside.write_bytes(self.source)
        metadata = {**self.metadata, "path": str(outside)}
        with mock.patch.object(self.environment, "command", return_value=json.dumps(metadata)) as command:
            with self.assertRaises(ValueError):
                self.environment.prepare_php(self.variants[0], {})
        self.assertEqual(command.call_count, 1)

    def test_content_digest_must_match_source_hashes_even_when_filename_matches(self):
        content_digest = "0" * 64
        path = self.path.with_name(f"contract-{content_digest}.php")
        path.write_bytes(self.source)
        metadata = {**self.metadata, "path": str(path), "content_sha256": content_digest}
        with mock.patch.object(self.environment, "command", return_value=json.dumps(metadata)) as command:
            with self.assertRaises(ValueError):
                self.environment.prepare_php(self.variants[0], {})
        self.assertEqual(command.call_count, 1)

    def test_missing_snapshot_or_changed_sources_stop_preparation(self):
        for changed in [self.path, self.root / "contracts/queries.json"]:
            with self.subTest(path=changed):
                original = changed.read_bytes()
                changed.unlink()
                try:
                    with mock.patch.object(self.environment, "command", return_value=json.dumps(self.metadata)) as command:
                        with self.assertRaises(ValueError):
                            self.environment.prepare_php(self.variants[0], {})
                        self.assertEqual(command.call_count, 1)
                finally:
                    changed.write_bytes(original)


if __name__ == "__main__":
    unittest.main()
