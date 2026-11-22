"""核对实际 Go 构建信息，避免把不同源码或构建条件混入同机对照。"""

import hashlib
import pathlib
import re
import shlex
import subprocess


GIT_OBJECT = re.compile(r"[0-9a-f]{40}\Z")
SHA256 = re.compile(r"[0-9a-f]{64}\Z")
BUSINESS_GO_PATH = "benchmarks/business/go"
BLOB_READ_CHUNK_BYTES = 1024 * 1024


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


def require_measurement_sources(repository, protocol):
    """正式测量不能省略来源合同，也不能把框架根模块的工作树改动归入 HEAD。"""
    require("build_provenance" in protocol, "正式测量必须声明 build_provenance 构建来源合同")
    changed = subprocess.check_output(["git", "status", "--porcelain", "--untracked-files=all"],
                                      cwd=repository, text=True)
    require(not changed.strip(), "正式测量要求框架及基准源码全部已提交，当前仍有未提交变更:\n" + changed)


def committed_sources(repository, commit):
    """从固定提交的不可变对象流归档摘要，编辑器临时改动和还原不会混入测量身份。"""
    require(isinstance(commit, str) and GIT_OBJECT.fullmatch(commit), "源码快照提交无效")
    listing = subprocess.check_output(["git", "ls-tree", "-r", "--full-tree", "-z", commit], cwd=repository)
    entries = []
    for entry in listing.split(b"\0"):
        if not entry:
            continue
        metadata, name = entry.split(b"\t", 1)
        _, kind, object_id = metadata.split()
        require(kind == b"blob", "正式源码快照不支持尚未归档的子模块")
        entries.append((name.decode("utf-8", errors="surrogateescape"), object_id))
    result, checksums = {}, {}
    # 单个持久 cat-file 进程逐对象交替读写，避免逐文件启动进程或一次缓冲整个仓库。
    with subprocess.Popen(["git", "cat-file", "--batch"], cwd=repository,
                          stdin=subprocess.PIPE, stdout=subprocess.PIPE) as process:
        for name, object_id in entries:
            if object_id not in checksums:
                process.stdin.write(object_id + b"\n")
                process.stdin.flush()
                header = process.stdout.readline().split()
                require(len(header) == 3 and header[:2] == [object_id, b"blob"] and header[2].isdigit(),
                        "Git 源码对象缺失或类型不匹配")
                remaining = int(header[2])
                checksum = hashlib.sha256()
                while remaining:
                    data = process.stdout.read(min(remaining, BLOB_READ_CHUNK_BYTES))
                    require(bool(data), "Git 源码对象读取被截断")
                    checksum.update(data)
                    remaining -= len(data)
                require(process.stdout.read(1) == b"\n", "Git 源码对象边界无效")
                checksums[object_id] = checksum.hexdigest()
            result[name] = checksums[object_id]
        process.stdin.close()
        require(process.wait() == 0, "Git 源码快照读取失败")
    return result


def validate(protocol, records, binaries, current_commit, *, current_tree=None):
    """归档重绘只核对已保存证据，不依赖原工作区或重新执行二进制。"""
    expected = definitions(protocol)
    require(isinstance(records, dict) and set(records) == set(expected), "Go 构建来源清单不完整或包含额外配置")
    require(isinstance(current_commit, str) and GIT_OBJECT.fullmatch(current_commit), "当前源码提交无效")
    if current_tree is not None:
        require(isinstance(current_tree, str) and GIT_OBJECT.fullmatch(current_tree), "当前源码树无效")
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
        explicit_control = revision != "HEAD"
        revision = current_commit if revision == "HEAD" else revision
        require(record["source_commit"] == revision, f"二进制源码提交不符合协议: {identifier}")
        if current_tree is not None and revision == current_commit:
            require(record["source_tree"] == current_tree, f"源码树与实验清单不一致: {identifier}")
        build_commit = revision
        if "build_commit" in record or "build_tree" in record:
            require(explicit_control and isinstance(record.get("build_commit"), str) and
                    GIT_OBJECT.fullmatch(record["build_commit"]) and
                    isinstance(record.get("build_tree"), str) and
                    GIT_OBJECT.fullmatch(record["build_tree"]),
                    f"对照组构建提交身份无效: {identifier}")
            build_commit = record["build_commit"]
        info = parse_build_info(record.get("build_info"))
        settings = info["settings"]
        require(info["go_version"] == configuration["go_version"], f"Go 工具链不符合协议: {identifier}")
        require(info["mod"] == configuration["module"] and
                info["path"] == configuration["module"] + "/cmd/" + variant.get("command", identifier),
                f"Go 构建包或模块不符合协议: {identifier}")
        require(settings.get("vcs") == "git" and settings.get("vcs.revision") == build_commit and
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
        parsed = parse_build_info(information)
        vcs_revision = parsed["settings"].get("vcs.revision", "")
        build_commit = commit
        extra = {}
        if revision != "HEAD" and GIT_OBJECT.fullmatch(vcs_revision) and vcs_revision != commit:
            build_commit = git(vcs_revision + "^{commit}")
            changed = subprocess.check_output(
                ["git", "diff-tree", "-r", "--name-only", "--no-commit-id", "-z", commit, build_commit],
                cwd=repository,
            ).split(b"\0")
            outside = [
                path.decode("utf-8", errors="replace")
                for path in changed
                if path and not path.decode("utf-8", errors="replace").startswith(BUSINESS_GO_PATH + "/")
            ]
            require(not outside, f"对照组构建提交修改了框架源码: {identifier}: {outside[:3]}")
            extra = {"build_commit": build_commit, "build_tree": git(build_commit + "^{tree}")}
        records[identifier] = {"source_commit": commit, "source_tree": git(commit + "^{tree}"),
                               "benchmark_go_tree": git(build_commit + ":" + BUSINESS_GO_PATH),
                               "binary_sha256": binaries[identifier], "build_info": information, **extra}
    validate(protocol, records, binaries, current_commit)
    return records
