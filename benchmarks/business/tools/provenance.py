"""核对实际 Go 构建信息，避免把不同源码或构建条件混入同机对照。"""

import hashlib
import pathlib
import re
import shlex
import subprocess


GIT_OBJECT = re.compile(r"[0-9a-f]{40}\Z")
SHA256 = re.compile(r"[0-9a-f]{64}\Z")
BUSINESS_GO_PATH = "benchmarks/business/go"


def require(condition, message):
    if not condition:
        raise ValueError(message)


def digest(path):
    with pathlib.Path(path).open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def parse_build_info(value):
    require(isinstance(value, str) and bool(value.strip()), "缺少实际 Go 构建信息")
    lines = value.splitlines()
    header = lines[0].rsplit(": ", 1)
    require(len(header) == 2 and re.fullmatch(r"go\d+\.\d+(?:\.\d+)?", header[1]), "Go 构建工具链标识无效")
    result = {"go_version": header[1], "settings": {}}
    for line in lines[1:]:
        fields = line.strip().split("\t")
        if fields[0] in {"path", "mod"}:
            require(len(fields) >= 2 and fields[0] not in result, "Go 构建包或模块标识缺失或重复")
            result[fields[0]] = fields[1]
        elif fields[0] == "build":
            require(len(fields) == 2, "Go 构建参数格式无效")
            decoded = shlex.split(fields[1])
            require(len(decoded) == 1 and "=" in decoded[0], "Go 构建参数格式无效")
            key, setting = decoded[0].split("=", 1)
            require(key and key not in result["settings"], "Go 构建参数为空或重复")
            result["settings"][key] = setting
    require(bool(result.get("path")) and bool(result.get("mod")), "Go 构建缺少包或模块标识")
    return result


def definitions(protocol):
    variants = {item["id"]: item for item in protocol["variants"] if item["kind"] == "go"}
    require("load" not in variants, "应用变体不能占用负载器标识 load")
    return {**variants, "load": {"id": "load", "command": "load"}}


def validate(protocol, records, binaries, current_commit):
    """归档重绘只核对已保存证据，不依赖原工作区或重新执行二进制。"""
    expected = definitions(protocol)
    require(isinstance(records, dict) and set(records) == set(expected), "Go 构建来源清单不完整或包含额外配置")
    require(isinstance(current_commit, str) and GIT_OBJECT.fullmatch(current_commit), "当前源码提交无效")
    require(isinstance(binaries, dict), "二进制摘要清单缺失")
    configuration = protocol["build_provenance"]
    shared = None
    shared_settings = None
    for identifier, variant in expected.items():
        record = records[identifier]
        require(isinstance(record, dict), "Go 构建来源记录必须为对象")
        for key in ("source_commit", "source_tree", "benchmark_go_tree"):
            require(isinstance(record.get(key), str) and GIT_OBJECT.fullmatch(record[key]), f"源码身份无效: {identifier}/{key}")
        checksum = record.get("binary_sha256")
        require(isinstance(checksum, str) and SHA256.fullmatch(checksum) and
                checksum == binaries.get(identifier), f"二进制摘要与实验清单不一致: {identifier}")
        revision = variant.get("source_revision", "HEAD")
        revision = current_commit if revision == "HEAD" else revision
        require(record["source_commit"] == revision, f"二进制源码提交不符合协议: {identifier}")
        info = parse_build_info(record.get("build_info"))
        settings = info["settings"]
        require(info["go_version"] == configuration["go_version"], f"Go 工具链不符合协议: {identifier}")
        require(info["mod"] == configuration["module"] and
                info["path"] == configuration["module"] + "/cmd/" + variant.get("command", identifier),
                f"Go 构建包或模块不符合协议: {identifier}")
        require(settings.get("vcs") == "git" and settings.get("vcs.revision") == revision and
                settings.get("vcs.modified") == "false", f"Go 构建未绑定清洁的指定源码: {identifier}")
        require(all(settings.get(key) == value for key, value in configuration["settings"].items()),
                f"Go 构建参数不符合协议: {identifier}")
        require(not set(configuration.get("absent_settings", [])) & settings.keys(),
                f"Go 构建包含协议禁止的测试插桩或其他参数: {identifier}")
        # 除 VCS 元数据外，实际嵌入构建信息的额外标签与编译参数也必须一致。
        comparable_settings = {key: value for key, value in settings.items() if not key.startswith("vcs.")}
        if shared_settings is None:
            shared_settings = comparable_settings
        require(comparable_settings == shared_settings, "各 Go 二进制的实际构建参数不一致")
        if shared is None:
            shared = record["benchmark_go_tree"]
        require(record["benchmark_go_tree"] == shared, "对照组与当前组的业务源码或模块锁不一致")


def collect(repository, root, protocol, binaries, go_tool):
    """从实际二进制与 Git 对象采集来源；不接受手工填写的构建收据。"""
    def git(reference):
        value = subprocess.check_output(["git", "rev-parse", "--verify", reference],
                                        cwd=repository, text=True).strip()
        require(GIT_OBJECT.fullmatch(value), "Git 源码对象标识无效")
        return value

    current_commit = git("HEAD^{commit}")
    records = {}
    for identifier, variant in definitions(protocol).items():
        revision = variant.get("source_revision", "HEAD")
        require(revision == "HEAD" or GIT_OBJECT.fullmatch(revision), "源码引用必须为 HEAD 或完整提交 SHA")
        commit = git(revision + "^{commit}")
        binary = pathlib.Path(root) / "bin" / identifier
        information = subprocess.check_output([str(go_tool), "version", "-m", str(binary)], text=True)
        # 在来源采集后复核字节，防止身份采集期间二进制被另一构建覆盖。
        require(digest(binary) == binaries[identifier], f"来源采集期间二进制已变化: {identifier}")
        records[identifier] = {"source_commit": commit, "source_tree": git(commit + "^{tree}"),
                               "benchmark_go_tree": git(commit + ":" + BUSINESS_GO_PATH),
                               "binary_sha256": binaries[identifier], "build_info": information}
    validate(protocol, records, binaries, current_commit)
    return records
