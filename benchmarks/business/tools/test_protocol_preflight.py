"""验证运行协议缺项在进程启动、构建和证据写入之前失败。"""

import copy
import json
import pathlib
import tempfile
import unittest
from types import SimpleNamespace
from unittest import mock

import experiment
import run


class ProtocolPreflightTests(unittest.TestCase):
    def test_missing_runtime_fields_fail_before_side_effects(self):
        original = json.loads((run.ROOT / "contracts/protocol.json").read_text())
        paths = [("cooldown_seconds",), ("request_timeout_seconds",), ("cpu_affinity",),
                 ("cpu_affinity", "load"), ("cpu_affinity", "controller"), ("ports",),
                 ("ports", "postgres"), ("ports", "fpm"), ("ports", "rpc"),
                 ("postgres",), ("postgres", "work_mem"), ("redis_maxmemory",),
                 ("gateway_workers",), ("gateway_connections",), ("gateway_upstream_keepalive",)]
        for mode in ("verify", "pilot", "measure"):
            for keys in paths:
                with self.subTest(mode=mode, field=keys), tempfile.TemporaryDirectory() as directory:
                    protocol = copy.deepcopy(original)
                    target = protocol
                    for key in keys[:-1]:
                        target = target[key]
                    del target[keys[-1]]
                    root = pathlib.Path(directory)
                    source = root / "protocol.json"
                    source.write_text(json.dumps(protocol))
                    arguments = SimpleNamespace(protocol=source, mode=mode, variants=None,
                                                output=root / "output", runtime=root / "runtime",
                                                work=root / "work", resume=False)
                    with mock.patch.object(run, "Environment") as environment, \
                            mock.patch.object(run, "identity") as identity, \
                            mock.patch.object(run.subprocess, "check_output", return_value="") as command:
                        with self.assertRaisesRegex(ValueError, keys[-1]):
                            run.execute(arguments)
                        environment.assert_not_called()
                        identity.assert_not_called()
                        command.assert_not_called()
                    self.assertFalse(arguments.output.exists())

    def test_cooldown_is_required_finite_and_nonnegative(self):
        original = json.loads((run.ROOT / "contracts/protocol.json").read_text())
        for value in (None, True, -1, float("inf"), float("nan"), "1"):
            with self.subTest(value=value):
                protocol = dict(original, cooldown_seconds=value)
                with self.assertRaisesRegex(ValueError, "cooldown_seconds"):
                    experiment.validate_protocol(protocol)
        for value in (0, 0.5, 1):
            experiment.validate_protocol(dict(original, cooldown_seconds=value))
