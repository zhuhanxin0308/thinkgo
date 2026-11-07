"""可恢复的真实业务基准入口；所有网络服务位于同一执行进程树。"""

from __future__ import annotations

import argparse
import datetime
import gzip
import hashlib
import json
import os
import pathlib
import platform
import resource
import secrets
import subprocess
import tempfile
import time

import audit
from native import Environment, Process, read_php_contract_snapshot


ROOT = pathlib.Path(__file__).resolve().parents[1]
PHP_CONTRACT_ARTIFACT = "contract-snapshot.php"
PHP_FRAMEWORKS = frozenset({"ThinkPHP", "Laravel"})


def now():
    return datetime.datetime.now(datetime.timezone.utc).isoformat()


def file_digest(path):
    digest = hashlib.sha256()
    with pathlib.Path(path).open("rb") as source:
        for block in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def write_json(path, value):
    path = pathlib.Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    compressed = path.suffix == ".gz"
    data = (json.dumps(value, ensure_ascii=False, sort_keys=True,
                       indent=None if compressed else 2, allow_nan=False) + "\n").encode("utf-8")
    if compressed:
        data = gzip.compress(data, mtime=0)
    publish_bytes(path, data)


def publish_bytes(path, data):
    path = pathlib.Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = None
    try:
        with tempfile.NamedTemporaryFile(mode="wb", dir=path.parent,
                                         prefix=".evidence-", delete=False) as output:
            temporary = pathlib.Path(output.name)
            output.write(data)
            output.flush()
            os.fsync(output.fileno())
        os.link(temporary, path)
    finally:
        if temporary is not None:
            temporary.unlink(missing_ok=True)


def preserve_bytes(path, data):
    path = pathlib.Path(path)
    if path.exists():
        if path.read_bytes() != data:
            raise ValueError(f"已有合同证据与本次源码不一致: {path}")
    else:
        publish_bytes(path, data)


def ensure_document(path, collect):
    path = pathlib.Path(path)
    if path.exists():
        return json.loads(path.read_text())
    value = collect()
    write_json(path, value)
    return value


def load_command(root, protocol, dataset, url, run_id, scenario, duration, output, rate):
    arguments = [root / "bin/load", "-url", url, "-scenario", scenario, "-rate", rate,
                 "-concurrency", protocol["closed_concurrency"], "-max-inflight", protocol["open_max_inflight"],
                 "-duration", f"{duration}s", "-timeout", f"{protocol['request_timeout_seconds']}s",
                 "-seed", protocol["seed"], "-run-id", run_id, "-secret-env", "BENCH_SECRET", "-output", output,
                 "-tenant-count", dataset["tenant_count"], "-users-per-tenant", dataset["users_per_tenant"],
                 "-products-per-tenant", dataset["products_per_tenant"], "-tickets-per-tenant", dataset["tickets_per_tenant"],
                 "-categories", dataset["categories_per_tenant"]]
    return list(map(str, arguments))


def read_report(path):
    with gzip.open(path, "rt", encoding="utf-8") as source:
        return json.load(source)


def progress(output, event, **fields):
    record = {"time": now(), "event": event, **fields}
    with (output / "progress.jsonl").open("a", encoding="utf-8") as stream:
        stream.write(json.dumps(record, ensure_ascii=False, sort_keys=True) + "\n")
        stream.flush()
    print(json.dumps(record, ensure_ascii=False), flush=True)


def identity(environment, protocol, mode):
    root = environment.root
    repository = root.parents[1]
    git = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=repository, text=True).strip()
    files = subprocess.check_output(["git", "ls-files", "-z", "--", "benchmarks/business"], cwd=repository).split(b"\0")
    source = {}
    for name in files:
        if name:
            path = repository / os.fsdecode(name)
            source[os.fsdecode(name)] = file_digest(path)
    binaries = {variant["id"]: file_digest(root / "bin" / variant["id"])
                for variant in protocol["variants"] if variant["kind"] == "go"}
    for name, path in {
        "load": root / "bin/load", "postgres": environment.binary("postgresql", "postgres"),
        "redis": environment.binary("redis", "redis-server"), "nginx": environment.runtime / "nginx/sbin/nginx",
        "php": environment.binary("php", "php"), "php-fpm": environment.runtime / "php/sbin/php-fpm",
        "roadrunner": environment.runtime / "roadrunner/rr",
    }.items():
        binaries[name] = file_digest(path)
    configuration_files = [environment.runtime / "php/etc/php.ini", *sorted((environment.runtime / "php/etc/conf.d").glob("*.ini"))]
    runtime_configuration = {str(path.relative_to(environment.runtime)): file_digest(path) for path in configuration_files}
    cpu = pathlib.Path("/proc/cpuinfo").read_text()
    model = next((line.split(":", 1)[1].strip() for line in cpu.splitlines() if line.startswith("model name")), "unknown")
    memory = pathlib.Path("/proc/meminfo").read_text().splitlines()
    total_memory = next(line.split(":", 1)[1].strip() for line in memory if line.startswith("MemTotal:"))
    return {"schema_version": 1, "mode": mode, "git_commit": git, "protocol": protocol,
            "sources_sha256": source, "binaries_sha256": binaries, "runtime_configuration_sha256": runtime_configuration,
            "go_runtime_environment": {key: os.getenv(key) for key in ["GOGC", "GOMEMLIMIT", "GODEBUG", "CGO_ENABLED", "GOTRACEBACK"]},
            "host": {"kernel": platform.release(), "machine": platform.machine(), "cpu_model": model,
                     "visible_cpus": sorted(os.sched_getaffinity(0)), "python": platform.python_version(),
                     "cgroup_quota": "unavailable", "exclusive_cpu_allocation": False,
                     "total_memory": total_memory,
                     "cgroup_membership": pathlib.Path("/proc/self/cgroup").read_text()}}


def run_load(environment, variables, path, run_id, scenario, duration, rate, measured):
    from experiment import check_load_report
    from resources import Sampler

    command = load_command(environment.root, environment.protocol, environment.dataset, environment.url,
                           run_id, scenario, duration, path, rate)
    variables = dict(variables)
    variables["GOMAXPROCS"] = str(len(environment.protocol["cpu_affinity"]["load"]))
    before = resource.getrusage(resource.RUSAGE_CHILDREN)
    controller_before = resource.getrusage(resource.RUSAGE_SELF)
    child = Process(command, environment.root, variables, path.with_suffix(".console.txt"),
                    environment.protocol["cpu_affinity"]["load"])
    sampler = None
    usage = None
    try:
        if measured:
            sampler = Sampler({"application": [item.pid for item in environment.application.values()],
                               **{name: [process.pid] for name, process in environment.infrastructure.items()},
                               "load": [child.pid]}, environment.protocol["sample_interval_seconds"])
            sampler.start()
        status = child.child.wait(timeout=duration + environment.protocol["request_timeout_seconds"] + 30)
        if status:
            raise RuntimeError(f"负载进程失败({status}): {path.with_suffix('.console.txt').read_text()}")
    finally:
        if sampler is not None:
            usage = sampler.stop()
        child.stop()
    after = resource.getrusage(resource.RUSAGE_CHILDREN)
    controller_after = resource.getrusage(resource.RUSAGE_SELF)
    report = read_report(path)
    checked = check_load_report(report)
    if usage is not None:
        usage["load_wait4_cpu_seconds"] = (after.ru_utime - before.ru_utime) + (after.ru_stime - before.ru_stime)
        usage["controller_cpu_seconds"] = ((controller_after.ru_utime - controller_before.ru_utime)
                                            + (controller_after.ru_stime - controller_before.ru_stime))
        usage["controller_affinity"] = sorted(os.sched_getaffinity(0))
        usage["scope"] = "负载进程完整调用期间，包括初始化、发送窗口和排空；应用在测量前已预热。"
    return report, checked, usage, command


def clear_cache(environment):
    environment.command([environment.binary("redis", "redis-cli"), "-h", "127.0.0.1", "-p",
                         environment.protocol["ports"]["redis"], "FLUSHDB"])


def validate_artifacts(directory, record):
    if record.get("valid") is not True or not record.get("artifacts_sha256"):
        raise ValueError("已保存轮次缺少有效审计或证据清单")
    for name, expected in record["artifacts_sha256"].items():
        if pathlib.Path(name).name != name:
            raise ValueError("证据清单只能引用本轮目录内的文件")
        path = directory / name
        if not path.is_file() or file_digest(path) != expected:
            raise ValueError(f"证据文件缺失或摘要不一致: {path}")
    metadata = record.get("php_contract_snapshot")
    if record.get("framework") in PHP_FRAMEWORKS:
        read_php_contract_snapshot(metadata, directory.parents[1] / "contracts",
                                   snapshot=directory / PHP_CONTRACT_ARTIFACT)
        if record["artifacts_sha256"].get(PHP_CONTRACT_ARTIFACT) != metadata["file_sha256"]:
            raise ValueError("PHP 合同快照未被本轮证据清单正确绑定")
    elif metadata is not None or PHP_CONTRACT_ARTIFACT in record["artifacts_sha256"]:
        raise ValueError("非 PHP 轮次不能携带 PHP 合同快照")


def preserve_php_contract_snapshot(environment, directory, variant):
    """冻结已验证的原始快照字节，禁止覆盖任何已有轮次证据。"""
    metadata = environment.php_contract_snapshot
    if variant["kind"] == "go":
        if metadata is not None:
            raise ValueError("Go 轮次残留 PHP 合同快照")
        return None
    # 以实验已经保存的合同核对来源，防止源码变化后生成新的混合证据。
    data = read_php_contract_snapshot(metadata, directory.parents[1] / "contracts")
    publish_bytes(directory / PHP_CONTRACT_ARTIFACT, data)
    return {**metadata, "source_sha256": dict(metadata["source_sha256"])}


def run_round(environment, output, item, variant, secret, rate, mode):
    from verify import preheat

    directory = output / "rounds" / item["id"]
    result_path = directory / "round.json"
    if result_path.exists():
        result = json.loads(result_path.read_text())
        validate_artifacts(directory, result)
        if any(result.get(key) != value for key, value in item.items()):
            raise ValueError("已保存轮次与预定矩阵不一致")
        return result
    if directory.exists():
        failed = output / "incomplete"
        failed.mkdir(exist_ok=True)
        directory.rename(failed / (item["id"] + "-" + str(time.time_ns())))
    directory.mkdir(parents=True)
    protocol = environment.protocol
    environment.reset()
    namespace = item["id"]
    variables = environment.start_app(variant, namespace, secret)
    started = now()
    try:
        php_contract_snapshot = preserve_php_contract_snapshot(environment, directory, variant)
        initial_preheat = preheat(environment, secret)
        warm_seconds = 1 if mode == "pilot" else protocol["warmup_seconds"]
        seconds = 3 if mode == "pilot" else protocol["measurement_seconds"]
        warmup, warmcheck, _, warmcommand = run_load(
            environment, variables, directory / "warmup.json.gz", namespace + "-warmup", item["scenario"],
            warm_seconds, protocol["warmup_rate"], False)
        if warmcheck["unsuccessful"]:
            raise RuntimeError(f"预热业务未全部成功，拒绝进入计时: {warmcheck}")
        clear_cache(environment)
        measured_preheat = preheat(environment, secret)
        environment.sql("SELECT pg_stat_statements_reset()")
        report, checked, usage, command = run_load(
            environment, variables, directory / "load.json.gz", namespace + "-measured", item["scenario"],
            seconds, rate, True)
        queries = audit.query_statistics(environment)
        integrity = audit.capture(environment, [warmup, report])
        write_json(directory / "resources.json.gz", usage)
        write_json(directory / "queries.json.gz", queries)
        write_json(directory / "audit.json", integrity)
        result = {**item, "framework": variant["framework"], "version": variant["version"], "engine": variant["engine"],
                  "started_at": started, "finished_at": now(), "mode": mode, "rate": rate,
                  "valid": integrity["valid"], "success_rps": report["success_rps"], "latency": {
                      key: report["latency"][key] for key in ["p50_ms", "p95_ms", "p99_ms", "max_ms", "mean_ms"]},
                  "totals": report["totals"], "cache_hit_ratio": report["cache_hit_ratio"],
                  "window_seconds": report["window_seconds"], "elapsed_seconds": report["elapsed_seconds"],
                  "load_check": checked, "initial_preheat": initial_preheat, "measured_preheat": measured_preheat,
                  "php_contract_snapshot": php_contract_snapshot,
                  "commands": {"warmup": warmcommand, "measurement": command},
                  "artifacts_sha256": {path.name: file_digest(path) for path in directory.iterdir() if path.is_file()}}
        write_json(result_path, result)
        if not integrity["valid"]:
            raise RuntimeError(f"业务审计失败，保留证据并停止: {integrity['failures']}")
        return result
    finally:
        environment.stop_app()


def verify_all(environment, output, protocol, secret):
    from verify import verify_contract

    folder = output / "verification"
    folder.mkdir(exist_ok=True)
    for variant in protocol["variants"]:
        path = folder / (variant["id"] + ".json")
        if path.exists():
            if not json.loads(path.read_text()).get("passed"):
                raise RuntimeError(f"已有验收失败证据，拒绝跳过: {path}")
            continue
        progress(output, "verification_start", variant=variant["id"])
        environment.reset()
        environment.start_app(variant, "verify-" + variant["id"], secret)
        try:
            result = verify_contract(environment, secret)
            write_json(path, result)
            if not result.get("passed"):
                raise RuntimeError(f"外部HTTP业务验收失败: {path}")
        finally:
            environment.stop_app()
        progress(output, "verification_pass", variant=variant["id"])


def execute(arguments):
    from experiment import build_schedule, choose_rates, validate_protocol

    protocol = json.loads((ROOT / "contracts/protocol.json").read_text())
    validate_protocol(protocol)
    if arguments.mode == "measure":
        changed = subprocess.check_output(["git", "status", "--porcelain", "--", "benchmarks/business"],
                                          cwd=ROOT.parents[1], text=True)
        if changed.strip():
            raise ValueError("正式测量要求基准代码已提交，当前仍有未提交变更:\n" + changed)
    if arguments.variants:
        if arguments.mode == "measure":
            raise ValueError("正式矩阵必须包含协议中的全部变体")
        selected = set(arguments.variants.split(","))
        protocol["variants"] = [variant for variant in protocol["variants"] if variant["id"] in selected]
        if {variant["id"] for variant in protocol["variants"]} != selected:
            raise ValueError("指定了未知框架变体")
    output = pathlib.Path(arguments.output).resolve()
    output.mkdir(parents=True, exist_ok=True)
    environment = Environment(ROOT, arguments.runtime, arguments.work, protocol)
    manifest = identity(environment, protocol, arguments.mode)
    manifest_path = output / "manifest.json"
    if manifest_path.exists():
        previous = json.loads(manifest_path.read_text())
        if not arguments.resume or previous != manifest:
            raise ValueError("已有结果不允许覆盖；恢复要求显式--resume且源码、二进制、协议与环境完全一致")
    else:
        write_json(manifest_path, manifest)
    for name in ["dataset.json", "queries.json", "schema.sql", "seed.sql", "protocol.json"]:
        preserve_bytes(output / "contracts" / name, (ROOT / "contracts" / name).read_bytes())
    if (output / "index.json").exists():
        index = json.loads((output / "index.json").read_text())
        for record in index["rounds"]:
            validate_artifacts(output / "rounds" / record["id"], record)
        progress(output, "already_complete", rounds=len(index["rounds"]))
        return
    secret = secrets.token_hex(32)
    progress(output, "session_start", mode=arguments.mode)
    try:
        environment.start()
        os.sched_setaffinity(0, protocol["cpu_affinity"]["controller"])
        environment.reset()
        metadata = {
            "database-settings.json": lambda: audit.database_settings(environment),
            "query-plans.json": lambda: audit.query_plans(environment),
            "seed-counts.json": lambda: environment.seed_counts,
            "schema-metrics.json": lambda: audit.schema_metrics(environment),
        }
        for name, collect in metadata.items():
            ensure_document(output / name, collect)
        verify_all(environment, output, protocol, secret)
        if arguments.mode == "verify":
            progress(output, "verification_complete")
            return
        variants = {variant["id"]: variant for variant in protocol["variants"]}
        samples = []
        for model in ["closed", "open"]:
            rates = choose_rates(protocol, samples) if model == "open" else {}
            if model == "open" and not (output / "arrival-rates.json").exists():
                write_json(output / "arrival-rates.json", rates)
            schedule = build_schedule(protocol, model)
            if arguments.mode == "pilot":
                schedule = [item for item in schedule if item["repeat"] == 1]
            for index, item in enumerate(schedule):
                progress(output, "round_start", id=item["id"], number=index + 1, total=len(schedule))
                rate = rates.get(item["scenario"], 0)
                result = run_round(environment, output, item, variants[item["variant"]], secret, rate, arguments.mode)
                samples.append(result)
                progress(output, "round_complete", id=item["id"], success_rps=result["success_rps"],
                         p99_ms=result["latency"]["p99_ms"], unsuccessful=result["load_check"]["unsuccessful"])
                time.sleep(protocol["cooldown_seconds"])
            if arguments.mode == "pilot":
                # 冒烟轮只证明链路可执行，不用于校准正式开放负载或产出排名。
                break
        write_json(output / "index.json", {"completed_at": now(), "mode": arguments.mode, "rounds": samples})
        progress(output, "experiment_complete", rounds=len(samples))
    finally:
        environment.close()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--runtime", required=True, help="原生运行依赖根目录")
    parser.add_argument("--work", required=True, help="本实验专属的运行目录")
    parser.add_argument("--output", required=True, help="新结果目录，已有证据不覆盖")
    parser.add_argument("--mode", choices=["verify", "pilot", "measure"], default="verify")
    parser.add_argument("--variants", default="", help="只用于验收或冒烟的逗号分隔变体ID")
    parser.add_argument("--resume", action="store_true")
    execute(parser.parse_args())


if __name__ == "__main__":
    main()
