"""对照实验必须把每个实际二进制绑定到可核验的源码与构建条件。"""

import copy
import pathlib
import tempfile
import unittest
from unittest import mock

import provenance


CURRENT = "a" * 40
BEFORE = "b" * 40
CURRENT_TREE = "c" * 40
BEFORE_TREE = "d" * 40
SHARED_TREE = "e" * 40
MODULE = "example.org/project/benchmarks/business"


def protocol_fixture():
    return {"build_provenance": {"go_version": "go1.27.3", "module": MODULE,
            "absent_settings": ["-race", "-cover", "-asan", "-msan"],
            "settings": {"-trimpath": "true", "CGO_ENABLED": "1", "GOAMD64": "v1"}},
            "variants": [
                {"id": "thinkgo", "kind": "go"},
                {"id": "thinkgo-before", "kind": "go", "command": "thinkgo", "source_revision": BEFORE},
                {"id": "laravel-fpm", "kind": "fpm"},
            ]}


def build_info(revision, command="thinkgo"):
    return (f"/runtime/{command}: go1.27.3\n"
            f"\tpath\t{MODULE}/cmd/{command}\n"
            f"\tmod\t{MODULE}\t(devel)\t\n"
            "\tbuild\t-trimpath=true\n\tbuild\tCGO_ENABLED=1\n\tbuild\tGOAMD64=v1\n"
            f"\tbuild\tvcs=git\n\tbuild\tvcs.revision={revision}\n"
            "\tbuild\tvcs.modified=false\n")


class ProvenanceTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = pathlib.Path(self.temporary.name)
        self.protocol = protocol_fixture()
        self.binaries = {"thinkgo": "1" * 64, "thinkgo-before": "2" * 64, "load": "3" * 64}
        self.outputs = {
            "thinkgo": build_info(CURRENT), "thinkgo-before": build_info(BEFORE),
            "load": build_info(CURRENT, "load"),
        }

    def command(self, arguments, **kwargs):
        if arguments[0] == "go-tool":
            return self.outputs[pathlib.Path(arguments[-1]).name]
        reference = arguments[-1]
        if reference.endswith(":benchmarks/business/go"):
            return SHARED_TREE + "\n"
        if reference.endswith("^{tree}"):
            return (BEFORE_TREE if reference.startswith(BEFORE) else CURRENT_TREE) + "\n"
        return (BEFORE if reference.startswith(BEFORE) else CURRENT) + "\n"

    def collect(self):
        with mock.patch.object(provenance.subprocess, "check_output", side_effect=self.command), \
                mock.patch.object(provenance, "digest", side_effect=lambda path: self.binaries[pathlib.Path(path).name]):
            return provenance.collect(self.root, self.root, self.protocol, self.binaries, "go-tool")

    def test_actual_build_info_records_both_commits_and_shared_business_tree(self):
        records = self.collect()
        self.assertEqual(set(records), {"thinkgo", "thinkgo-before", "load"})
        self.assertEqual(records["thinkgo"]["source_commit"], CURRENT)
        self.assertEqual(records["thinkgo-before"]["source_commit"], BEFORE)
        self.assertEqual(records["thinkgo-before"]["source_tree"], BEFORE_TREE)
        self.assertEqual(records["thinkgo"]["benchmark_go_tree"], SHARED_TREE)
        self.assertEqual(records["load"]["build_info"], self.outputs["load"])
        provenance.validate(self.protocol, records, self.binaries, CURRENT)

    def test_rejects_wrong_revision_dirty_build_toolchain_settings_or_command(self):
        changes = [
            lambda value: value.replace(CURRENT, BEFORE),
            lambda value: value.replace("vcs.modified=false", "vcs.modified=true"),
            lambda value: value.replace("go1.27.3", "go1.26.10"),
            lambda value: value.replace("CGO_ENABLED=1", "CGO_ENABLED=0"),
            lambda value: value.replace("/cmd/thinkgo", "/cmd/gin"),
            lambda value: value.replace("\tbuild\tvcs=git\n", ""),
            lambda value: value + "\tbuild\tvcs.modified=false\n",
            lambda value: value + "\tbuild\t-tags=extra\n",
        ]
        original = self.outputs["thinkgo"]
        for change in changes:
            with self.subTest(change=change):
                self.outputs["thinkgo"] = change(original)
                with self.assertRaises(ValueError):
                    self.collect()

    def test_rejects_different_business_source_between_control_and_candidate(self):
        original = self.command
        def changed(arguments, **kwargs):
            if arguments[-1] == BEFORE + ":benchmarks/business/go":
                return "f" * 40 + "\n"
            return original(arguments, **kwargs)
        with mock.patch.object(provenance.subprocess, "check_output", side_effect=changed), \
                mock.patch.object(provenance, "digest", side_effect=lambda path: self.binaries[pathlib.Path(path).name]):
            with self.assertRaisesRegex(ValueError, "业务源码"):
                provenance.collect(self.root, self.root, self.protocol, self.binaries, "go-tool")

    def test_rejects_binary_changed_after_manifest_hashing(self):
        with mock.patch.object(provenance.subprocess, "check_output", side_effect=self.command), \
                mock.patch.object(provenance, "digest", return_value="9" * 64):
            with self.assertRaisesRegex(ValueError, "二进制"):
                provenance.collect(self.root, self.root, self.protocol, self.binaries, "go-tool")

    def test_saved_provenance_cannot_disagree_with_manifest_or_raw_build_info(self):
        original = self.collect()
        changes = [
            lambda records: records.pop("load"),
            lambda records: records["thinkgo"].update(binary_sha256="0" * 64),
            lambda records: records["thinkgo"].update(source_commit=BEFORE),
            lambda records: records["thinkgo"].update(source_tree="invalid"),
            lambda records: records["thinkgo-before"].update(benchmark_go_tree="f" * 40),
            lambda records: records["thinkgo"].update(build_info=build_info(BEFORE)),
        ]
        for change in changes:
            records = copy.deepcopy(original)
            change(records)
            with self.subTest(change=change), self.assertRaises(ValueError):
                provenance.validate(self.protocol, records, self.binaries, CURRENT)

    def test_parser_rejects_missing_or_ambiguous_metadata(self):
        for value in ["", "not-build-info", build_info(CURRENT).replace("\tpath\t", "\tunknown\t"),
                      build_info(CURRENT) + f"\tpath\t{MODULE}/cmd/thinkgo\n"]:
            with self.subTest(value=value), self.assertRaises(ValueError):
                provenance.parse_build_info(value)

    def test_configuration_rejects_reserved_loader_name_and_unsafe_revision(self):
        self.protocol["variants"][0]["id"] = "load"
        with self.assertRaisesRegex(ValueError, "负载器"):
            self.collect()
        self.protocol = protocol_fixture()
        self.protocol["variants"][0]["source_revision"] = "main~1"
        with self.assertRaisesRegex(ValueError, "源码引用"):
            self.collect()

    def test_matching_test_instrumentation_is_not_a_production_measurement(self):
        original = dict(self.outputs)
        for setting in self.protocol["build_provenance"]["absent_settings"]:
            self.outputs = {name: value + f"\tbuild\t{setting}=true\n" for name, value in original.items()}
            with self.subTest(setting=setting), self.assertRaisesRegex(ValueError, "禁止"):
                self.collect()

    def test_digest_reads_actual_file_bytes(self):
        path = self.root / "binary"
        path.write_bytes(b"abc")
        self.assertEqual(provenance.digest(path), "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad")


if __name__ == "__main__":
    unittest.main()
