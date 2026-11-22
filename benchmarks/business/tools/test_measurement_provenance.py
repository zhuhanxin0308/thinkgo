"""正式测量必须把真实二进制和整个框架工作树绑定到可重建提交。"""

import copy
import hashlib
import json
import os
import pathlib
import shutil
import subprocess
import tempfile
import unittest
from types import SimpleNamespace
from unittest import mock

import experiment
import provenance
import run


ROOT = pathlib.Path(__file__).resolve().parents[1]
DEFAULT_PROTOCOL = ROOT / "contracts/protocol.json"
COMPARISON_PROTOCOL = ROOT / "contracts/protocol-log-close.json"


class MeasurementRepositoryFixture:
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.directory = pathlib.Path(temporary.name)
        self.repository = self.directory / "repository"
        self.root = self.repository / "benchmarks/business"
        (self.root / "contracts").mkdir(parents=True)
        self.protocol = experiment.load_protocol(DEFAULT_PROTOCOL)
        self.protocol["build_provenance"] = experiment.load_protocol(COMPARISON_PROTOCOL)["build_provenance"]
        self.protocol["build_provenance"]["require_for_nonmeasurement"] = False
        self.protocol_path = self.root / "contracts/protocol.json"
        self.protocol_path.write_text(json.dumps(self.protocol))
        self.framework = self.repository / "framework.go"
        self.framework.write_text("package framework\n")
        self.git("init", "-q")
        self.git("config", "user.name", "ThinkGo Test")
        self.git("config", "user.email", "test@example.invalid")
        self.git("add", ".")
        self.git("commit", "-qm", "test: prepare clean source")

    def git(self, *arguments):
        return subprocess.check_output(["git", *arguments], cwd=self.repository, text=True).strip()

    def execute(self):
        arguments = SimpleNamespace(mode="measure", variants="", protocol=self.protocol_path,
                                    runtime="runtime", work="work", output=self.directory / "output", resume=False)
        with mock.patch.object(run, "ROOT", self.root), \
                mock.patch.object(run, "Environment", side_effect=AssertionError("不得创建运行环境")) as environment:
            try:
                run.execute(arguments)
            finally:
                environment.assert_not_called()
                self.assertFalse(arguments.output.exists())


class MeasurementSourceGateTests(MeasurementRepositoryFixture, unittest.TestCase):
    def test_snapshot_archives_binary_empty_and_non_utf8_paths(self):
        contents = b"\x00\xff" * (provenance.BLOB_READ_CHUNK_BYTES // 2 + 1)
        binary = self.repository / "二进制 snapshot.bin"
        binary.write_bytes(contents)
        (self.repository / "same-blob.bin").write_bytes(contents)
        empty = self.repository / "empty.txt"
        empty.write_bytes(b"")
        raw_name = b"non-utf8-\xff.txt"
        raw_path = os.fsencode(self.repository) + b"/" + raw_name
        with open(raw_path, "wb") as file:
            file.write(b"source with a non-UTF-8 path")
        self.git("add", ".")
        self.git("commit", "-qm", "test: binary and filename snapshot cases")
        snapshot = provenance.committed_sources(self.repository, self.git("rev-parse", "HEAD"))
        self.assertEqual(snapshot[binary.name], hashlib.sha256(contents).hexdigest())
        self.assertEqual(snapshot["same-blob.bin"], snapshot[binary.name])
        self.assertEqual(snapshot[empty.name], hashlib.sha256(b"").hexdigest())
        self.assertIn(os.fsdecode(raw_name), snapshot)
        manifest = self.directory / "snapshot.json"
        run.write_json(manifest, snapshot)
        self.assertEqual(json.loads(manifest.read_text()), snapshot)

    def test_default_protocol_declares_the_same_build_contract_as_explicit_comparison(self):
        self.assertEqual(experiment.load_protocol(DEFAULT_PROTOCOL).get("build_provenance"),
                         self.protocol["build_provenance"])

    def test_root_dirty_benchmark_clean_is_rejected_before_environment_or_evidence(self):
        for staged in (False, True):
            with self.subTest(staged=staged):
                self.framework.write_text("package framework\n// 根框架尚未提交的普通改动。\n")
                if staged:
                    self.git("add", "framework.go")
                self.assertEqual(self.git("status", "--porcelain", "--", "benchmarks/business"), "")
                with self.assertRaisesRegex(ValueError, "未提交"):
                    self.execute()

    def test_untracked_root_source_is_rejected(self):
        (self.repository / "extra.go").write_text("package framework\n")
        with self.assertRaisesRegex(ValueError, "未提交"):
            self.execute()

    def test_git_display_preferences_cannot_hide_untracked_source(self):
        self.git("config", "status.showUntrackedFiles", "no")
        (self.repository / "extra.go").write_text("package framework\n")
        self.assertEqual(self.git("status", "--porcelain"), "")
        with self.assertRaisesRegex(ValueError, "未提交"):
            self.execute()

    def test_formal_custom_protocol_cannot_omit_build_provenance(self):
        self.protocol.pop("build_provenance")
        self.protocol_path.write_text(json.dumps(self.protocol))
        self.git("add", ".")
        self.git("commit", "-qm", "test: legacy protocol without provenance")
        with self.assertRaisesRegex(ValueError, "build_provenance"):
            self.execute()


class MeasurementBinaryIdentityTests(MeasurementRepositoryFixture, unittest.TestCase):
    """使用隔离 Git 仓库和真实 Go 二进制；不启动负载器或数据库服务。"""

    def setUp(self):
        super().setUp()
        self.go_tool = os.environ.get("THINKGO_TEST_GO") or shutil.which("go")
        if not self.go_tool:
            self.skipTest("需要 Go 编译器验证真实 VCS 元数据")
        version = subprocess.check_output([self.go_tool, "version"], text=True, timeout=10)
        expected = self.protocol["build_provenance"]["go_version"]
        if not version.startswith("go version " + expected + " "):
            self.skipTest(f"需要协议指定的 {expected}")
        self.protocol["variants"] = [variant for variant in self.protocol["variants"]
                                     if variant["id"] in {"thinkgo", "laravel-fpm"}]
        self.protocol_path.write_text(json.dumps(self.protocol))
        self.repository.joinpath(".gitignore").write_text("/benchmarks/business/bin/\n")
        self.repository.joinpath("go.mod").write_text("module example.org/framework\n\ngo " + expected[2:] + "\n")
        self.framework.write_text('package framework\n\nconst Message = "clean"\n')
        module = self.protocol["build_provenance"]["module"]
        go_root = self.root / "go"
        go_root.mkdir()
        (go_root / "go.mod").write_text(f"module {module}\n\ngo {expected[2:]}\n\n"
                                       "require example.org/framework v0.0.0\n\n"
                                       "replace example.org/framework => ../../..\n")
        for command in ("thinkgo", "load"):
            target = go_root / "cmd" / command / "main.go"
            target.parent.mkdir(parents=True)
            target.write_text('package main\n\nimport ("fmt"; "example.org/framework")\n\n'
                              'func main() { fmt.Println(framework.Message) }\n')
        self.git("add", ".")
        self.git("commit", "-qm", "test: add reproducible binary fixtures")
        self.environment = SimpleNamespace(root=self.root, runtime=self.directory / "runtime")
        self.environment.binary = lambda component, name: self.environment.runtime / component / name
        for name in ("postgresql/postgres", "redis/redis-server", "nginx/sbin/nginx", "php/php",
                     "php/sbin/php-fpm", "roadrunner/rr", "php/etc/php.ini"):
            path = self.environment.runtime / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(b"runtime fixture, never executed")
        self.build()

    def build(self):
        environment = {**os.environ, "GOFLAGS": "", "GOWORK": "off", "CGO_ENABLED": "1",
                       "GOOS": "linux", "GOARCH": "amd64", "GOAMD64": "v1"}
        for command in ("thinkgo", "load"):
            subprocess.run([self.go_tool, "build", "-trimpath", "-buildvcs=true", "-o",
                            str(self.root / "bin" / command), "./cmd/" + command],
                           cwd=self.root / "go", env=environment, check=True, capture_output=True)

    def identity(self, protocol=None):
        return run.identity(self.environment, protocol or self.protocol, "measure", go_tool=self.go_tool)

    def test_clean_binary_archives_root_tree_source_digest_and_raw_vcs_evidence(self):
        identity = self.identity()
        self.assertEqual(identity.get("git_tree"), self.git("rev-parse", "HEAD^{tree}"))
        self.assertEqual(identity["sources_sha256"].get("framework.go"), run.file_digest(self.framework))
        records = identity["go_build_provenance"]
        self.assertEqual(set(records), {"thinkgo", "load"})
        for record in records.values():
            self.assertEqual(record["source_commit"], self.git("rev-parse", "HEAD"))
            self.assertEqual(record["source_tree"], identity["git_tree"])
            self.assertIn("vcs.modified=false", record["build_info"])
        manifest = self.directory / "archive/manifest.json"
        run.write_json(manifest, identity)
        self.assertEqual(json.loads(manifest.read_text()), identity)

    def test_formal_source_digests_come_from_fixed_git_objects(self):
        digest = run.file_digest
        expected = digest(self.framework)

        def editor_race(path):
            path = pathlib.Path(path)
            if path != self.framework:
                return digest(path)
            saved = path.read_bytes()
            try:
                path.write_text('package framework\n\nconst Message = "temporary edit"\n')
                return digest(path)
            finally:
                path.write_bytes(saved)

        with mock.patch.object(run, "file_digest", side_effect=editor_race):
            identity = self.identity()
        self.assertEqual(self.git("status", "--porcelain"), "")
        self.assertEqual(identity["sources_sha256"]["framework.go"], expected)

    def test_nonformal_modes_keep_worktree_experiments_available(self):
        self.framework.unlink()
        for mode in ("verify", "pilot"):
            with self.subTest(mode=mode):
                identity = run.identity(self.environment, self.protocol, mode, go_tool=self.go_tool)
                self.assertNotIn("go_build_provenance", identity)
                self.assertNotIn("framework.go", identity["sources_sha256"])
                self.assertIn("benchmarks/business/go/go.mod", identity["sources_sha256"])

    def test_clean_worktree_cannot_accept_binary_from_previous_commit(self):
        self.framework.write_text('package framework\n\nconst Message = "committed change"\n')
        self.git("add", ".")
        self.git("commit", "-qm", "test: move source beyond built binary")
        with self.assertRaisesRegex(ValueError, "清洁的指定源码"):
            self.identity()

    def test_clean_worktree_cannot_accept_binary_built_from_dirty_framework(self):
        original = self.framework.read_text()
        self.framework.write_text('package framework\n\nconst Message = "dirty build"\n')
        self.build()
        self.framework.write_text(original)
        self.assertEqual(self.git("status", "--porcelain"), "")
        with self.assertRaisesRegex(ValueError, "清洁的指定源码"):
            self.identity()

    def test_identity_cannot_bypass_provenance_with_legacy_protocol(self):
        protocol = copy.deepcopy(self.protocol)
        protocol.pop("build_provenance")
        with self.assertRaisesRegex(ValueError, "build_provenance"):
            self.identity(protocol)

    def test_source_changes_during_collection_are_rejected_before_archiving(self):
        collect = provenance.collect

        def collect_then_change(*arguments):
            records = collect(*arguments)
            self.framework.write_text('package framework\n\nconst Message = "concurrent change"\n')
            return records

        with mock.patch.object(provenance, "collect", side_effect=collect_then_change), \
                self.assertRaisesRegex(ValueError, "未提交"):
            self.identity()

    def test_clean_head_change_during_collection_cannot_mix_source_identities(self):
        collect = provenance.collect

        def collect_then_commit(*arguments):
            records = collect(*arguments)
            self.git("commit", "--allow-empty", "-qm", "test: concurrent HEAD change")
            return records

        with mock.patch.object(provenance, "collect", side_effect=collect_then_commit), \
                self.assertRaisesRegex(ValueError, "源码提交已变化"):
            self.identity()


if __name__ == "__main__":
    unittest.main()
