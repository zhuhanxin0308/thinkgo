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

    def build(self, *flags):
        environment = {**os.environ, "GOFLAGS": "", "GOWORK": "off", "CGO_ENABLED": "1",
                       "GOOS": "linux", "GOARCH": "amd64", "GOAMD64": "v1"}
        for command in ("thinkgo", "load"):
            subprocess.run([self.go_tool, "build", "-trimpath", "-buildvcs=true", *flags, "-o",
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

    def test_historical_controls_use_their_own_framework_after_build_worktree_removal(self):
        before = self.git("rev-parse", "HEAD")
        subtree = provenance.BUSINESS_GO_PATH
        current_framework = self.framework.with_name("framework_current.go")
        self.framework.rename(current_framework)
        current_framework.write_text('package framework\n\nconst Message = "current framework"\n', encoding="utf-8")
        marker = self.root / "go/cmd/thinkgo/marker.go"
        marker.write_text('package main\n\nfunc init() { println("shared benchmark") }\n', encoding="utf-8")
        self.git("add", "-A")
        self.git("commit", "-qm", "test: prepare current framework and shared benchmark")
        candidate = self.git("rev-parse", "HEAD")
        checkout = self.directory / "historical-worktree"
        self.git("worktree", "add", "-q", "-b", "fixture-control-build", str(checkout), before)
        self.git("-C", str(checkout), "checkout", candidate, "--", subtree)
        self.git("-C", str(checkout), "commit", "-qm", "test: splice benchmark onto historical framework")
        spliced = self.git("-C", str(checkout), "rev-parse", "HEAD")
        control_root = checkout / "benchmarks/business"
        with mock.patch.object(self, "root", control_root):
            self.build()
        controls = {"thinkgo-before": before, "thinkgo-before-direct": spliced}
        current = next(variant for variant in self.protocol["variants"] if variant["id"] == "thinkgo")
        for name, revision in controls.items():
            shutil.copy2(control_root / "bin/thinkgo", self.root / "bin" / name)
            self.protocol["variants"].append(
                {**current, "id": name, "command": "thinkgo", "source_revision": revision})
        shutil.rmtree(control_root / "bin")
        self.git("worktree", "remove", str(checkout))
        self.assertFalse(checkout.exists())
        self.protocol_path.write_text(json.dumps(self.protocol), encoding="utf-8")
        self.git("add", "benchmarks/business/contracts/protocol.json")
        self.git("commit", "-qm", "test: record historical control revisions")
        self.build()
        records = self.identity()["go_build_provenance"]
        shared_tree = self.git("rev-parse", "HEAD:" + subtree)
        self.assertNotEqual(shared_tree, self.git("rev-parse", before + ":" + subtree))
        for name, revision in controls.items():
            with self.subTest(name=name):
                record = records[name]
                self.assertEqual(record["source_commit"], revision)
                self.assertEqual(record["source_tree"], self.git("rev-parse", revision + "^{tree}"))
                self.assertEqual(record["benchmark_go_tree"], shared_tree)
        self.assertEqual(records["thinkgo-before"]["build_commit"], spliced)
        self.assertEqual(records["thinkgo-before"]["build_tree"], self.git("rev-parse", spliced + "^{tree}"))
        self.assertNotIn("build_commit", records["thinkgo-before-direct"])

    def assert_measurement_gate(self, rejected=None):
        class ReachedPreflight(Exception):
            pass

        output = self.directory / "output"
        arguments = SimpleNamespace(mode="measure", variants="", protocol=self.protocol_path,
                                    runtime=self.environment.runtime, work=self.directory / "work", output=output,
                                    resume=False, go_tool=self.go_tool)
        self.environment.dataset = json.loads((ROOT / "contracts/dataset.json").read_text(encoding="utf-8"))
        self.environment.url = "http://127.0.0.1:1"
        # 只替换外部副作用边界，来源采集和负载参数构建仍执行真实路径。
        self.environment.command = mock.Mock(side_effect=ReachedPreflight)
        self.environment.start = mock.Mock(side_effect=AssertionError("不得启动服务"))
        with mock.patch.object(run, "ROOT", self.root), mock.patch.object(run, "Environment", return_value=self.environment):
            if rejected is None:
                with self.assertRaises(ReachedPreflight):
                    run.execute(arguments)
                self.environment.command.assert_called_once()
                command = self.environment.command.call_args.args[0]
                self.assertEqual(command[0], str(self.root / "bin/load"))
                self.assertIn("-check-config", command)
            else:
                with self.assertRaisesRegex(ValueError, rejected):
                    run.execute(arguments)
                self.environment.command.assert_not_called()
        self.environment.start.assert_not_called()
        self.assertFalse(output.exists())

    def ignored_input(self, relative, content):
        path = self.repository / relative
        path.parent.mkdir(parents=True, exist_ok=True)
        with self.repository.joinpath(".git/info/exclude").open("a", encoding="utf-8") as excluded:
            excluded.write("\n/" + relative + "\n")
        path.write_text(content, encoding="utf-8")
        self.assertEqual(self.git("check-ignore", "--", relative), relative)

    def test_ignored_compiled_framework_source_is_rejected_before_load_or_evidence(self):
        binary = self.root / "bin/load"
        before = run.file_digest(binary)
        self.ignored_input("ignored_behavior.go", 'package framework\n\nfunc init() { println("ignored fixture input") }\n')
        self.build()
        self.assertNotEqual(run.file_digest(binary), before)
        self.assertEqual(self.git("status", "--porcelain", "--untracked-files=all"), "")
        settings = provenance.parse_build_info(subprocess.check_output(
            [self.go_tool, "version", "-m", str(binary)], text=True))["settings"]
        self.assertEqual(settings["vcs.revision"], self.git("rev-parse", "HEAD"))
        self.assertEqual(settings["vcs.modified"], "false")
        self.assert_measurement_gate(r"ignored_behavior\.go")

    def test_ignored_input_selected_by_binary_build_tag_is_rejected(self):
        self.ignored_input("active_tag.go", '//go:build thinkgo_fixture_active\n\npackage framework\n\n'
                           'func init() { println("tagged fixture input") }\n')
        self.build("-tags=thinkgo_fixture_active")
        self.assert_measurement_gate(r"active_tag\.go")

    def test_ignored_file_selected_by_go_embed_is_rejected(self):
        assets = self.repository / "assets"
        assets.mkdir()
        (assets / "base.txt").write_text("committed payload\n", encoding="utf-8")
        (self.repository / "embedded.go").write_text(
            'package framework\n\nimport "embed"\n\n//go:embed assets/*.txt\nvar assets embed.FS\n\n'
            'func init() { if _, err := assets.ReadDir("assets"); err != nil { panic(err) } }\n',
            encoding="utf-8")
        self.git("add", "assets/base.txt", "embedded.go")
        self.git("commit", "-qm", "test: add committed embed fixture")
        self.ignored_input("assets/local.txt", "local payload\n")
        self.build()
        self.assert_measurement_gate(r"assets/local\.txt")

    def test_assume_unchanged_framework_bytes_are_rejected(self):
        self.git("update-index", "--assume-unchanged", "framework.go")
        self.framework.write_text('package framework\n\nconst Message = "uncommitted framework bytes"\n', encoding="utf-8")
        self.build()
        self.assertEqual(self.git("status", "--porcelain", "--untracked-files=all"), "")
        self.assert_measurement_gate(r"字节不符.*framework\.go")

    def test_cgo_replays_binary_configuration_and_checks_selected_c_source(self):
        bridge = self.repository / "bridge.go"
        bridge.write_text('package framework\n\n/*\nint fixture_value(void);\n*/\nimport "C"\n\n'
                          'func init() { if C.fixture_value() != 7 { panic("fixture value") } }\n', encoding="utf-8")
        self.git("add", "bridge.go")
        self.git("commit", "-qm", "test: link framework fixture through cgo")
        self.ignored_input("local_value.c", "int fixture_value(void) { return 7; }\n")
        self.build()
        with mock.patch.dict(os.environ, {"CGO_ENABLED": "0"}):
            self.assert_measurement_gate(r"local_value\.c")

    def test_ignored_dependency_go_sum_is_not_a_build_input(self):
        # 依赖模块的校验和文件不参与嵌套主模块的构建。
        self.ignored_input("go.sum", "this is not a valid checksum record\n")
        self.build()
        self.assertEqual(self.git("status", "--porcelain", "--untracked-files=all"), "")
        self.assert_measurement_gate()

    def test_versioned_module_cache_inside_ignored_repository_directory_is_allowed(self):
        import zipfile

        module = "example.org/dependency"
        version = "v1.0.0"
        manifest = f"module {module}\n\ngo 1.20\n"
        proxy = self.directory / "proxy"
        versions = proxy / module / "@v"
        versions.mkdir(parents=True)
        (versions / "list").write_text(version + "\n", encoding="utf-8")
        (versions / (version + ".info")).write_text(
            json.dumps({"Version": version, "Time": "2020-01-01T00:00:00Z"}), encoding="utf-8")
        (versions / (version + ".mod")).write_text(manifest, encoding="utf-8")
        with zipfile.ZipFile(versions / (version + ".zip"), "w") as archive:
            prefix = f"{module}@{version}/"
            archive.writestr(prefix + "go.mod", manifest)
            archive.writestr(prefix + "value.go", 'package dependency\n\nconst Value = "cached dependency"\n')
        go_dir = self.root / "go"
        with (go_dir / "go.mod").open("a", encoding="utf-8") as go_mod:
            go_mod.write(f"\nrequire {module} {version}\n")
        (go_dir / "cmd/load/dependency.go").write_text(
            f'package main\n\nimport "{module}"\n\nfunc init() {{ println(dependency.Value) }}\n', encoding="utf-8")
        cache = self.repository / "runtime-module-cache"
        with (self.repository / ".gitignore").open("a", encoding="utf-8") as ignored:
            ignored.write("/runtime-module-cache/\n")
        with mock.patch.dict(os.environ, {"GOPROXY": proxy.as_uri(), "GOSUMDB": "off", "GONOPROXY": "none",
                                          "GOMODCACHE": str(cache), "GOFLAGS": "", "GOWORK": "off"}):
            subprocess.run([self.go_tool, "mod", "tidy"], cwd=go_dir, check=True, capture_output=True)
            self.assertTrue((go_dir / "go.sum").is_file())
            self.git("add", ".gitignore", "benchmarks/business/go")
            self.git("commit", "-qm", "test: use a versioned module from an isolated proxy")
            self.build()
            cached = cache / f"{module}@{version}" / "value.go"
            self.assertTrue(cached.is_file())
            relative = cached.relative_to(self.repository).as_posix()
            self.assertEqual(self.git("check-ignore", "--", relative), relative)
            self.assertEqual(self.git("status", "--porcelain", "--untracked-files=all"), "")
            self.assert_measurement_gate()

    def test_binary_using_an_external_workspace_replacement_is_rejected(self):
        external = self.directory / "external-framework"
        external.mkdir()
        shutil.copy2(self.repository / "go.mod", external / "go.mod")
        (external / "framework.go").write_text(
            'package framework\n\nconst Message = "alternate framework"\n', encoding="utf-8")
        version = self.protocol["build_provenance"]["go_version"][2:]
        workfile = self.directory / "go.work"
        workfile.write_text(f"go {version}\n\nuse {json.dumps(str(self.root / 'go'))}\n\n"
                            f"replace example.org/framework => {json.dumps(str(external))}\n", encoding="utf-8")
        # 仅改变 load 的真实依赖，避免同一提交分组误用第一个命令的构建信息。
        environment = {**os.environ, "GOWORK": str(workfile), "GOFLAGS": "", "GOTOOLCHAIN": "local",
                       "CGO_ENABLED": "1", "GOOS": "linux", "GOARCH": "amd64", "GOAMD64": "v1"}
        binary = self.root / "bin/load"
        subprocess.run([self.go_tool, "build", "-trimpath", "-mod=readonly", "-buildvcs=true",
                        "-o", str(binary), "./cmd/load"], cwd=self.root / "go",
                       env=environment, check=True, capture_output=True)
        raw = subprocess.check_output([self.go_tool, "version", "-m", str(binary)], text=True)
        self.assertIn("\t=>\t" + str(external) + "\t(devel)", raw)
        settings = provenance.parse_build_info(raw)["settings"]
        self.assertEqual(settings["vcs.revision"], self.git("rev-parse", "HEAD"))
        self.assertEqual(settings["vcs.modified"], "false")
        self.assertEqual(self.git("status", "--porcelain", "--untracked-files=all"), "")
        self.assert_measurement_gate(r"模块|替换")

    def test_unreferenced_local_module_is_not_a_build_input(self):
        module = "example.org/unreferenced"
        with (self.root / "go/go.mod").open("a", encoding="utf-8") as go_mod:
            go_mod.write(f"\nrequire {module} v0.0.0\nreplace {module} => ../../../unreferenced-module\n")
        self.git("add", "benchmarks/business/go/go.mod")
        self.git("commit", "-qm", "test: declare an unused local replacement")
        self.assertFalse((self.repository / "unreferenced-module").exists())
        self.build()
        before = run.file_digest(self.root / "bin/load")
        version = self.protocol["build_provenance"]["go_version"][2:]
        self.ignored_input("unreferenced-module/go.mod", f"module {module}\n\ngo {version}\n")
        self.build()
        self.assertEqual(run.file_digest(self.root / "bin/load"), before)
        self.assertEqual(self.git("status", "--porcelain", "--untracked-files=all"), "")
        self.assert_measurement_gate()

    def test_ignored_files_outside_the_build_are_allowed(self):
        paths = {
            "local/runtime.log": "local runtime output\n",
            "inactive_tag.go": "//go:build thinkgo_fixture_inactive\n\npackage framework\n",
            "inactive_windows.go": "package framework\n",
        }
        for path, content in paths.items():
            self.ignored_input(path, content)
        self.assertEqual(self.git("status", "--porcelain", "--untracked-files=all"), "")
        with mock.patch.dict(os.environ, {"GOFLAGS": "-tags=thinkgo_fixture_inactive", "GOOS": "windows"}):
            self.assert_measurement_gate()


if __name__ == "__main__":
    unittest.main()
