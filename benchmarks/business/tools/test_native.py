"""本地服务编排的配置和资源所有权约束。"""

import copy
import hashlib
import json
import pathlib
import tempfile
import unittest
from unittest import mock

import native
from native import Environment, postgres_config, redis_config, gateway_config, fpm_config, octane_command


ROOT = pathlib.Path(__file__).resolve().parents[1]


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
