"""核验完整正式矩阵，再输出中位数、最小最大范围与可分享图表。"""

from __future__ import annotations

import argparse
import csv
import gzip
import hashlib
import itertools
import json
import math
import pathlib
import statistics
import textwrap
import zlib

from audit import assess
from experiment import COUNTERS, NANOSECONDS_PER_SECOND


SCENARIOS = ("catalog", "detail", "order", "ticket", "mixed")
MODELS = ("closed", "open")
SCENARIO_LABELS = {"catalog": "Catalog", "detail": "Product detail", "order": "Order transaction",
                   "ticket": "Ticket comment", "mixed": "Mixed business"}
REQUIRED_ARTIFACTS = {"load.json.gz", "resources.json.gz", "queries.json.gz", "audit.json"}
ERROR_FIELDS = ("status_errors", "transport_errors", "semantic_errors", "dropped", "not_scheduled")
ERROR_LABELS = ("Business / HTTP", "Transport", "Semantic", "Dropped", "Not scheduled")
ERROR_COLORS = ("#b54708", "#175cd3", "#b42318", "#7f56d9", "#667085")
LATENCY_FIELDS = ("p50_ms", "p95_ms", "p99_ms", "max_ms", "mean_ms")
MEBIBYTE = 1024 * 1024
MILLISECONDS_PER_SECOND = 1000
PNG_DPI = 180
AXIS_LIMIT_PADDING = 1.12
EMPTY_LOG_LIMITS = (1.0, 10.0)
VARIANT_COLORS = ("#175cd3", "#027a48", "#b54708", "#6941c6", "#026aa2", "#c11574",
                  "#4d7c0f", "#9e3c2f", "#475467")
ATTEMPT_REPORT_FIELDS = ("window_seconds", "elapsed_seconds", "drain_seconds", "success_rps",
                         "issued_success_rps", "elapsed_success_rps", "expected", "not_scheduled")
ATTEMPT_RESOURCE_FIELDS = ("application_cpu_seconds", "peak_pss_mib", "peak_rss_mib")
ATTEMPT_AUDIT_FIELDS = ("audit_valid", "unconfirmed_orders", "unconfirmed_comments")
ATTEMPT_CSV_FIELDS = (
    "attempt_id", "round_id", "variant", "framework", "label", "model", "scenario", "repeat", "rate",
    "evidence_directory", "archive_timestamp_ns", "included_in_aggregates", "round_evidence_complete",
    "termination_reason", "load_report_status", "resources_status", "queries_status", "audit_status",
    "started_at", "finished_at", "canceled", "measurement_requests_started", *ATTEMPT_REPORT_FIELDS,
    *COUNTERS, *LATENCY_FIELDS, "drop_reasons", *ATTEMPT_RESOURCE_FIELDS, *ATTEMPT_AUDIT_FIELDS,
    "available_artifacts", "missing_artifacts", "artifacts_sha256", "validation_errors",
)
DISCLOSURE_WRAP_COLUMNS = 158
DISCLOSURE_TOP = -0.015


def require(condition, message):
    if not condition:
        raise ValueError(message)


def read_json(path):
    path = pathlib.Path(path)
    encoded = gzip.decompress(path.read_bytes()) if path.suffix == ".gz" else path.read_bytes()

    def invalid_constant(value):
        raise ValueError(f"JSON 包含非有限数值: {value}")

    return json.loads(encoded, parse_constant=invalid_constant)


def digest(path):
    with pathlib.Path(path).open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def finite(value, name, *, positive=False):
    require(isinstance(value, (int, float)) and not isinstance(value, bool) and math.isfinite(value),
            f"{name} 必须是有限数值")
    require(value > 0 if positive else value >= 0, f"{name} 超出合法范围")
    return value


def label_variant(variant):
    if "label" in variant:
        label = variant["label"]
        require(isinstance(label, str) and bool(label.strip()) and label.isprintable(),
                "配置标签必须为非空的单行可显示文本")
        return label
    if variant["kind"] == "fpm":
        return variant["framework"] + " · PHP-FPM"
    if variant["kind"] in {"octane", "roadrunner"} or "Octane" in variant["engine"]:
        return variant["framework"] + " · Octane/RoadRunner"
    return variant["framework"]


def validate_artifacts(directory, item):
    hashes = item["artifacts_sha256"]
    require(REQUIRED_ARTIFACTS <= set(hashes), f"轮次缺少必需证据: {item['id']}")
    for name, expected in hashes.items():
        require(pathlib.Path(name).name == name and name not in {"", ".", "..", "round.json"},
                f"证据文件名不合法: {name}")
        artifact = directory / name
        require(artifact.resolve().parent == directory.resolve() and artifact.is_file(), f"证据文件缺失: {name}")
        require(digest(artifact) == expected, f"SHA256 不匹配: {item['id']}/{name}")


def validate_load(load, item, protocol):
    # 与运行器共用负载守恒验证；不由绘图代码另造吞吐统计口径。
    from experiment import check_load_report

    check_load_report(load)
    require(load["model"] == item["model"] and load["config"]["scenario"] == item["scenario"],
            "负载模型或场景与轮次不一致")
    require(not load["canceled"], "正式轮次不能包含取消的负载")
    require(load["config"]["rate_per_second"] == item["rate"], "负载与轮次到达率不一致")
    require(math.isclose(load["window_seconds"], protocol["measurement_seconds"], rel_tol=0, abs_tol=1e-9),
            "测量窗口与正式协议不一致")
    require(load["config"]["concurrency"] == protocol["closed_concurrency"] and
            load["config"]["max_inflight"] == protocol["open_max_inflight"], "负载并发预算与协议不一致")
    for field in ["success_rps", "totals", "cache_hit_ratio", "window_seconds", "elapsed_seconds"]:
        require(load[field] == item[field], f"轮次与原始负载字段不一致: {field}")
    require(item["latency"] == {key: load["latency"][key] for key in LATENCY_FIELDS},
            "轮次延迟与原始直方图摘要不一致")


def validate_audit(value, identifier):
    checked = assess(value["database"], value["confirmed_orders"], value["confirmed_comments"])
    require(value["valid"] is True and value["failures"] == {} and checked == value,
            f"业务审计无效或其标记与数据库不变量不一致: {identifier}")


def resource_metrics(value):
    application = value["groups"]["application"]
    memory_complete = (application["sample_count"] > 0 and application["missing_samples"] == 0 and
                       application["samples_with_processes"] == application["sample_count"])
    cpu = finite(application["cpu_seconds"], "application.cpu_seconds") if application["cpu_complete"] else None
    rss = finite(application["peak_rss_bytes"], "application.peak_rss_bytes") / MEBIBYTE if memory_complete else None
    pss = application["peak_pss_bytes"] if application["pss_complete"] and memory_complete else None
    if pss is not None:
        pss = finite(pss, "application.peak_pss_bytes") / MEBIBYTE
    return {"application_cpu_seconds": cpu, "peak_pss_mib": pss, "peak_rss_mib": rss,
            "cpu_complete": application["cpu_complete"], "memory_complete": memory_complete,
            "resource_missing_samples": application["missing_samples"],
            "resource_errors": json.dumps(application["errors"], ensure_ascii=False)}


def normalize_round(item, load, usage, integrity):
    counts = load["totals"]
    planned = counts["planned"] + load["not_scheduled"]
    cached = counts["cache_hits"] + counts["cache_misses"]
    metrics = resource_metrics(usage)
    row = {key: item[key] for key in ["id", "variant", "framework", "version", "engine", "scenario", "model", "repeat", "rate"]}
    row.update({key: counts[key] for key in counts})
    row.update(metrics)
    row.update({"mode": item["mode"], "success_rps": load["success_rps"],
                "p99_ms": load["latency"]["p99_ms"] if counts["completed"] else None,
                "p95_ms": load["latency"]["p95_ms"] if counts["completed"] else None,
                "p50_ms": load["latency"]["p50_ms"] if counts["completed"] else None,
                "cpu_ms_per_success": metrics["application_cpu_seconds"] * MILLISECONDS_PER_SECOND /
                    counts["success"] if counts["success"] and metrics["application_cpu_seconds"] is not None else None,
                "cache_hit_percent": 100 * counts["cache_hits"] / cached if cached else None,
                "window_seconds": load["window_seconds"], "elapsed_seconds": load["elapsed_seconds"],
                "not_scheduled": load["not_scheduled"], "unconfirmed_orders": integrity["unconfirmed_orders"],
                "unconfirmed_comments": integrity["unconfirmed_comments"]})
    for field in ERROR_FIELDS:
        row[field + "_percent"] = 100 * row[field] / planned if planned else None
    row["unsuccessful_percent"] = 100 * sum(row[field] for field in ERROR_FIELDS) / planned if planned else None
    return row


def attempt_artifact(directory, name, validate, attempt):
    """归档证据损坏也保留尝试；可读、完整和通过业务审计分别记录。"""
    if not (directory / name).exists():
        return None, "missing"
    try:
        value = read_json(directory / name)
    except (OSError, EOFError, ValueError, zlib.error) as error:
        attempt["validation_errors"][name] = type(error).__name__
        return None, "unreadable"
    try:
        validate(value)
    except (ValueError, KeyError, TypeError, OverflowError) as error:
        attempt["validation_errors"][name] = str(error)
        return value, "invalid"
    return value, "validated"


def observed_number(value, *, counter=False):
    # 未验证报告只摘录合法的原始数字；没有观测值不能补零或按公式推算。
    if counter and type(value) is not int:
        return None
    try:
        return finite(value, "归档观测值")
    except (ValueError, OverflowError):
        return None


def validate_attempt_identity(load, item, protocol):
    if not isinstance(load, dict):
        return False
    require("model" not in load or load["model"] == item["model"], "归档负载模型身份不一致")
    config = load.get("config")
    if not isinstance(config, dict):
        return False
    expected = {"run_id": item["id"] + "-measured", "scenario": item["scenario"],
                "rate_per_second": item["rate"], "concurrency": protocol["closed_concurrency"],
                "max_inflight": protocol["open_max_inflight"],
                "duration_ns": round(protocol["measurement_seconds"] * NANOSECONDS_PER_SECOND)}
    for field, value in expected.items():
        require(field not in config or config[field] == value, f"归档负载身份或协议不一致: {field}")
    return "model" in load and expected.keys() <= config.keys()


def interrupted_attempt(directory, item, protocol, base):
    from experiment import check_load_report

    attempt = dict.fromkeys(ATTEMPT_CSV_FIELDS)
    attempt.update(base, included_in_aggregates=False, round_evidence_complete=False,
                   validation_errors={})
    # 身份冲突必须中止发布；普通损坏则记录为不可验证，不能悄悄丢弃该次尝试。
    load, status = attempt_artifact(directory, "load.json.gz", lambda value: None, attempt)
    bound = validate_attempt_identity(load, item, protocol)
    if status == "validated":
        def validate_report(value):
            require(bound, "归档负载身份字段不完整")
            check_load_report(value)

        load, status = attempt_artifact(directory, "load.json.gz", validate_report, attempt)
    attempt["load_report_status"] = status
    if isinstance(load, dict):
        counts = load.get("totals") if isinstance(load.get("totals"), dict) else {}
        latency = load.get("latency") if isinstance(load.get("latency"), dict) else {}
        for field in COUNTERS:
            attempt[field] = observed_number(counts.get(field), counter=True)
        for field in ATTEMPT_REPORT_FIELDS:
            attempt[field] = observed_number(load.get(field), counter=field in {"expected", "not_scheduled"})
        for field in LATENCY_FIELDS:
            attempt[field] = observed_number(latency.get(field)) if attempt["completed"] else None
        for field in ("started_at", "finished_at"):
            value = load.get(field)
            attempt[field] = value if isinstance(value, str) and value.isprintable() else None
        attempt["canceled"] = load.get("canceled") if type(load.get("canceled")) is bool else None
        attempt["measurement_requests_started"] = attempt["started"] > 0 if attempt["started"] is not None else None
        reasons = load.get("drop_reasons")
        if isinstance(reasons, dict):
            attempt["drop_reasons"] = {key: observed_number(value, counter=True) for key, value in reasons.items()}
    usage, attempt["resources_status"] = attempt_artifact(directory, "resources.json.gz", resource_metrics, attempt)
    if attempt["resources_status"] == "validated":
        metrics = resource_metrics(usage)
        attempt.update({field: metrics[field] for field in ATTEMPT_RESOURCE_FIELDS})
    integrity, attempt["audit_status"] = attempt_artifact(
        directory, "audit.json", lambda value: validate_audit(value, item["id"]), attempt)
    if attempt["audit_status"] == "validated":
        attempt.update(audit_valid=True, unconfirmed_orders=integrity["unconfirmed_orders"],
                       unconfirmed_comments=integrity["unconfirmed_comments"])
    _, attempt["queries_status"] = attempt_artifact(
        directory, "queries.json.gz", lambda value: require(isinstance(value, list), "SQL 查询证据必须是列表"), attempt)
    return attempt


def load_incomplete_attempts(source, items, protocol):
    """只读恢复归档，区分计时前失败与正式测量尝试，不增加计划重复数。"""
    archive = source / "incomplete"
    interrupted, preparation = [], []
    if not archive.exists() and not archive.is_symlink():
        return interrupted, preparation
    require(archive.is_dir() and not archive.is_symlink(), "归档目录必须为本实验内的真实目录")
    by_id = {item["id"]: item for item in items}
    variants = {variant["id"]: variant for variant in protocol["variants"]}
    for directory in sorted(archive.iterdir()):
        require(directory.is_dir() and not directory.is_symlink(), f"归档目录越界或不是目录: {directory.name}")
        identifier, separator, stamp = directory.name.rpartition("-")
        require(separator and stamp.isascii() and stamp.isdigit() and identifier in by_id,
                f"未知计划轮次归档: {directory.name}")
        item = by_id[identifier]
        files = sorted(directory.iterdir())
        require(all(path.is_file() and not path.is_symlink() for path in files), "归档证据不能包含目录或链接")
        names = [path.name for path in files]
        require("round.json" not in names, "归档包含已结束轮次，不能当成未完成尝试")
        base = {key: item[key] for key in ("variant", "framework", "model", "scenario", "repeat", "rate")}
        base.update(attempt_id=directory.name, round_id=identifier, label=label_variant(variants[item["variant"]]),
                    evidence_directory=str(directory.relative_to(source)), archive_timestamp_ns=stamp,
                    available_artifacts=names, missing_artifacts=sorted((REQUIRED_ARTIFACTS | {"round.json"}) - set(names)),
                    artifacts_sha256={path.name: digest(path) for path in files})
        if {"load.json.gz", "load.json.console.txt"} & set(names):
            interrupted.append(interrupted_attempt(directory, item, protocol, base))
        else:
            preparation.append(dict(base, stage="pre_measurement"))
    return interrupted, preparation


def load_results(source, *, allow_synthetic=False):
    source = pathlib.Path(source).resolve()
    manifest, index = read_json(source / "manifest.json"), read_json(source / "index.json")
    mode = manifest["mode"]
    if mode == "synthetic":
        require(allow_synthetic, "synthetic 数据仅供显式启用的测试，不能作为正式结果")
    else:
        require(mode == "measure", "仅接受 measure 正式矩阵，不接收 pilot 或 verify")
    require(index["mode"] == mode, "manifest 与 index 的实验模式不一致")
    protocol = manifest["protocol"]
    from experiment import validate_protocol

    validate_protocol(protocol)
    if "build_provenance" in protocol:
        from provenance import validate

        validate(protocol, manifest.get("go_build_provenance"), manifest.get("binaries_sha256"),
                 manifest.get("git_commit"))
        require(read_json(source / "contracts/protocol.json") == protocol, "归档协议与实验清单不一致")
    require(set(protocol["scenarios"]) == set(SCENARIOS) and len(protocol["scenarios"]) == len(SCENARIOS),
            "协议必须包含全部五个业务场景")
    require(protocol["repetitions"] == 3, "正式图表要求三次独立重复")
    variants = protocol["variants"]
    variant_map = {variant["id"]: variant for variant in variants}
    require(len(variant_map) == len(variants) and bool(variants), "框架变体标识重复或为空")
    expected = set(itertools.product(variant_map, SCENARIOS, MODELS, range(1, protocol["repetitions"] + 1)))
    actual = [(item["variant"], item["scenario"], item["model"], item["repeat"]) for item in index["rounds"]]
    require(len(set(actual)) == len(actual), "矩阵存在重复轮次")
    require(set(actual) == expected, "矩阵不完整或包含额外轮次")
    identifiers = [item["id"] for item in index["rounds"]]
    require(len(set(identifiers)) == len(identifiers), "轮次标识重复")
    require({path.name for path in (source / "rounds").iterdir() if path.is_dir()} == set(identifiers),
            "rounds 目录与索引包含不同轮次")
    rows = []
    for item in index["rounds"]:
        identifier = item["id"]
        require(pathlib.Path(identifier).name == identifier, "轮次标识不能包含目录路径")
        directory = source / "rounds" / identifier
        require(directory.resolve().parent == (source / "rounds").resolve(), "轮次目录越界")
        require(read_json(directory / "round.json") == item, f"index 与 round.json 不一致: {identifier}")
        require(item["mode"] == mode and item["valid"] is True, f"轮次模式或有效性不符: {identifier}")
        variant = variant_map[item["variant"]]
        require(all(item[key] == variant[key] for key in ["framework", "version", "engine"]), "框架身份与协议不一致")
        validate_artifacts(directory, item)
        load = read_json(directory / "load.json.gz")
        validate_load(load, item, protocol)
        integrity = read_json(directory / "audit.json")
        validate_audit(integrity, identifier)
        require(isinstance(read_json(directory / "queries.json.gz"), list), "SQL 查询证据必须是列表")
        rows.append(normalize_round(item, load, read_json(directory / "resources.json.gz"), integrity))
    rates = read_json(source / "arrival-rates.json")
    require(set(rates) == set(SCENARIOS), "到达率场景集合与协议不一致")
    from experiment import choose_rates

    calibrated = choose_rates(protocol, [item for item in index["rounds"] if item["model"] == "closed"])
    for scenario in SCENARIOS:
        require(rates[scenario] == calibrated[scenario], f"开放到达率与闭环中位数校准不一致: {scenario}")
        require(all(row["rate"] == rates[scenario] for row in rows if row["model"] == "open" and
                    row["scenario"] == scenario), f"同场景未采用相同绝对到达率: {scenario}")
    require(all(row["rate"] == 0 for row in rows if row["model"] == "closed"), "闭环不能设定开放到达率")
    interrupted, preparation = load_incomplete_attempts(source, index["rounds"], protocol)
    for row in rows:
        identifiers = [attempt["attempt_id"] for attempt in interrupted if attempt["round_id"] == row["id"]]
        row.update(interrupted_attempt_count=len(identifiers), interrupted_attempt_ids=identifiers)
    return {"source": str(source), "mode": mode, "manifest": manifest, "protocol": protocol,
            "rounds": rows, "arrival_rates": rates,
            "interrupted_attempts": interrupted, "pre_measurement_incomplete_attempts": preparation,
            "evidence_sha256": {name: digest(source / name) for name in ["manifest.json", "index.json", "arrival-rates.json"]}}


def range_summary(values):
    present = [value for value in values if value is not None]
    if len(present) != len(values):
        return {"median": None, "min": None, "max": None, "n": len(present)}
    return {"median": statistics.median(present), "min": min(present), "max": max(present), "n": len(present)}


def summarize(result):
    rows, protocol = result["rounds"], result["protocol"]
    interrupted = result["interrupted_attempts"]
    memory = "pss" if all(row["peak_pss_mib"] is not None for row in rows) else "rss"
    metrics = ["success_rps", "p99_ms", "p95_ms", "p50_ms", "cpu_ms_per_success", "peak_memory_mib",
               "peak_pss_mib", "peak_rss_mib", "application_cpu_seconds", "cache_hit_percent",
               "unsuccessful_percent", "unconfirmed_orders", "unconfirmed_comments", *ERROR_FIELDS,
               *(field + "_percent" for field in ERROR_FIELDS)]
    groups = []
    for model, scenario, variant in itertools.product(MODELS, protocol["scenarios"], protocol["variants"]):
        selected = [dict(row, peak_memory_mib=row["peak_" + memory + "_mib"]) for row in rows
                    if row["model"] == model and row["scenario"] == scenario and row["variant"] == variant["id"]]
        groups.append({"model": model, "scenario": scenario, "variant": variant["id"],
                       "label": label_variant(variant), "rate": selected[0]["rate"],
                       "round_ids": [row["id"] for row in selected],
                       "interrupted_attempt_count": sum(row["interrupted_attempt_count"] for row in selected),
                       "interrupted_attempt_ids": [identifier for row in selected for identifier in row["interrupted_attempt_ids"]],
                       "interrupted_repeats": sorted(row["repeat"] for row in selected if row["interrupted_attempt_count"]),
                       "metrics": {metric: range_summary([row[metric] for row in selected]) for metric in metrics}})
    summary = {"schema_version": 1, "mode": result["mode"], "source": result["source"],
            "collection_git_commit": result["manifest"].get("git_commit"),
            "attempt_accounting": {"planned_round_count": len(protocol["variants"]) * len(SCENARIOS) * len(MODELS) * protocol["repetitions"],
                "completed_round_count": len(rows), "interrupted_measurement_attempt_count": len(interrupted),
                "pre_measurement_incomplete_count": len(result["pre_measurement_incomplete_attempts"])},
            "interrupted_attempts": interrupted,
            "pre_measurement_incomplete_attempts": result["pre_measurement_incomplete_attempts"],
            "evidence_sha256": result["evidence_sha256"], "repetitions": protocol["repetitions"],
            "scenarios": protocol["scenarios"], "closed_concurrency": protocol["closed_concurrency"],
            "variants": [dict(variant, label=label_variant(variant)) for variant in protocol["variants"]],
            "arrival_rates": result["arrival_rates"], "memory_metric": memory,
            "telemetry_warnings": [{key: row[key] for key in ["id", "cpu_complete", "memory_complete",
                "resource_missing_samples", "resource_errors"]} for row in rows
                if not row["cpu_complete"] or not row["memory_complete"]],
            "memory_note": "All rounds use sampled process-tree PSS." if memory == "pss" else
                "RSS fallback for every comparison: PSS is unavailable in at least one round; shared pages may be counted repeatedly.",
            "definitions": {"range": "Median and observed min/max of three independent repeats; no confidence interval.",
                "interrupted_attempts": "Archived incomplete measurement invocations are excluded from every aggregate; all completed audited rounds, including unsuccessful requests, remain. Counts cover retained archives only. Missing observations and audits are null, not zero. Invalid reports expose unverified observed numbers only; a console alone does not prove requests started. Archive timestamps record recovery, not failure time.",
                "success_rps": "Successful completions inside the measurement window / window seconds.",
                "p99_ms": "Per-run P99 from scheduled time to complete response body; all completed requests; dropped requests have no latency sample.",
                "cpu_ms_per_success": "Application process tree including Nginx CPU milliseconds during the full load invocation / successful completions including drain.",
                "peak_memory_mib": "Maximum sampled application process-tree memory including Nginx; startup before measurement is excluded.",
                "error_percent": "Each outcome count / (planned + not_scheduled); categories remain separate.",
                "cache_hit_percent": "cache_hits / (cache_hits + cache_misses); null means no cache-bearing responses."},
             "groups": groups}
    if "go_build_provenance" in result["manifest"]:
        # 数据表随图表携带来源，分享图表目录时仍可追溯修复前后的真实构建。
        summary["go_build_provenance"] = result["manifest"]["go_build_provenance"]
        summary["host"] = result["manifest"]["host"]
    return summary


def write_csv(path, rows, fields=None):
    fields = list(rows[0]) if fields is None else fields
    with pathlib.Path(path).open("x", newline="", encoding="utf-8") as stream:
        writer = csv.DictWriter(stream, fieldnames=fields)
        writer.writeheader()
        # 列表和映射仍采用 JSON 编码，便于独立下载 CSV 后准确恢复关联证据。
        writer.writerows({key: json.dumps(value, ensure_ascii=False, allow_nan=False) if isinstance(value, (dict, list)) else value
                         for key, value in row.items()} for row in rows)


def write_tables(destination, result, summary):
    with (destination / "summary.json").open("x", encoding="utf-8") as stream:
        json.dump(summary, stream, ensure_ascii=False, indent=2, allow_nan=False)
        stream.write("\n")
    write_csv(destination / "rounds.csv", [dict(row, memory_metric=summary["memory_metric"],
              peak_memory_mib=row["peak_" + summary["memory_metric"] + "_mib"]) for row in result["rounds"]])
    flattened = []
    for group in summary["groups"]:
        row = {key: group[key] for key in ["model", "scenario", "variant", "label", "rate",
               "interrupted_attempt_count", "interrupted_attempt_ids", "interrupted_repeats"]}
        row["memory_metric"] = summary["memory_metric"]
        for metric, values in group["metrics"].items():
            for statistic, value in values.items():
                row[metric + "_" + statistic] = value
        flattened.append(row)
    write_csv(destination / "summary.csv", flattened)
    write_csv(destination / "interrupted_attempts.csv", summary["interrupted_attempts"], ATTEMPT_CSV_FIELDS)


def plotting():
    # 使用无窗口后端，输出为可独立分享的 SVG 和高分辨率 PNG。
    import matplotlib

    matplotlib.use("Agg")
    from matplotlib import pyplot

    pyplot.rcParams.update({"font.family": "DejaVu Sans", "font.size": 10,
        "axes.titleweight": "bold", "axes.titlelocation": "left", "axes.labelcolor": "#344054",
        "text.color": "#101828", "xtick.color": "#475467", "ytick.color": "#344054",
        "axes.edgecolor": "#d0d5dd", "axes.spines.top": False, "axes.spines.right": False,
        "axes.spines.left": False, "svg.fonttype": "none", "savefig.facecolor": "white"})
    return pyplot


def figure_header(figure, title, summary, subtitle):
    figure.suptitle(title, x=0.055, y=0.985, ha="left", fontsize=22, fontweight="bold")
    figure.text(0.055, 1 - 0.72 / figure.get_figheight(), subtitle, ha="left", va="top",
                fontsize=10, color="#475467")
    if summary["mode"] == "synthetic":
        figure.text(0.98, 0.012, "SYNTHETIC TEST DATA — NOT BENCHMARK RESULTS", ha="right",
                    color="#b42318", fontsize=12, fontweight="bold")
    accounting = summary["attempt_accounting"]
    attempts = summary["interrupted_attempts"]
    lines = [f"Experiment disclosure: {len(attempts)} interrupted measurement attempt(s) excluded from medians; "
             f"{accounting['completed_round_count']}/{accounting['planned_round_count']} completed audited rounds retained."]
    if attempts:
        affected = dict.fromkeys(f"{item['label']} / {item['model']} / {item['scenario']} / repeat {item['repeat']}"
                                 for item in attempts)
        lines.extend(textwrap.wrap("Affected: " + "; ".join(affected), DISCLOSURE_WRAP_COLUMNS))
        missing = [f"{label}: {sum(item[field] != 'validated' for item in attempts)}"
                   for field, label in [("audit_status", "DB audit"), ("resources_status", "resources"),
                                        ("queries_status", "queries")]]
        lines.append("Missing or unverified evidence (attempts) — " + "; ".join(missing) + ".")
    lines.append("* marks an affected group. Observations, missing audits and all attempt records: interrupted_attempts.csv / summary.json.")
    # 页脚放在画布外的独立区域；tight 边界把它完整纳入 SVG/PNG，避免挤压表格、原有说明和水印。
    figure.text(0.055, DISCLOSURE_TOP, "\n".join(lines), ha="left", va="top", fontsize=9,
                color="#694126" if attempts else "#667085", linespacing=1.5)


def group_label(group):
    return group["label"] + (" *" if group["interrupted_attempt_count"] else "")


def save_figure(figure, destination, name, pyplot):
    for extension in ["svg", "png"]:
        metadata = {"Title": name, "Description": "Median and observed min/max; three independent repeats."}
        figure.savefig(destination / (name + "." + extension), dpi=PNG_DPI, bbox_inches="tight", metadata=metadata)
    pyplot.close(figure)


def note_panel(axis, summary, model, extra, axis_scale="linear"):
    axis.axis("off")
    text = ["READING THIS FIGURE", "", "Dot: median of 3 independent repeats",
            "Whisker: observed minimum to maximum", "No confidence interval or combined score", "",
            "Variant order follows the fixed protocol.",
            "Panel axes use independent " + ("logarithmic" if axis_scale == "log" else "linear") + " scales.", "",
            "Application scope includes Nginx.", extra]
    if model == "open":
        text.append("Each panel applies one shared offered rate.")
    axis.text(0.04, 0.98, "\n".join(text), va="top", fontsize=10, linespacing=1.25, color="#475467")


def draw_metric(summary, destination, metric, model, name, title, unit, note):
    from matplotlib.ticker import EngFormatter, LogLocator, NullFormatter

    pyplot = plotting()
    # 尾延迟跨数量级时保留慢轮，同时让常见延迟之间的差异可读。
    axis_scale = "log" if metric == "p99_ms" else "linear"
    count = len(summary["variants"])
    figure, axes = pyplot.subplots(3, 2, figsize=(18, max(12, 0.31 * count * 3 + 3)),
                                  gridspec_kw={"hspace": 0.50, "wspace": 0.48})
    figure.subplots_adjust(top=0.90, bottom=0.11, left=0.18, right=0.97)
    load = f"Closed loop · {summary['closed_concurrency']} concurrent clients" if model == "closed" else "Open loop · equal absolute arrival rates"
    figure_header(figure, title, summary, load + "  |  Three repeats: median and observed min–max")
    for axis, scenario in zip(axes.flat, summary["scenarios"]):
        selected = [group for group in summary["groups"] if group["model"] == model and group["scenario"] == scenario]
        labels = []
        minimum = math.inf
        maximum = 0
        for position, group in enumerate(selected):
            values = group["metrics"][metric]
            labels.append(group_label(group))
            color = VARIANT_COLORS[position % len(VARIANT_COLORS)]
            if values["median"] is None:
                axis.text(0.02, position, "N/A", transform=axis.get_yaxis_transform(),
                          va="center", fontsize=9, color="#667085")
                continue
            if axis_scale == "log":
                require(values["min"] > 0, "对数延迟图要求全部观测值为正数")
            minimum = min(minimum, values["min"])
            maximum = max(maximum, values["max"])
            axis.errorbar(values["median"], position,
                          xerr=[[values["median"] - values["min"]], [values["max"] - values["median"]]],
                          fmt="o", color=color, markersize=6, capsize=3, elinewidth=1.8)
        axis.set_yticks(range(count), labels, fontsize=9)
        axis.set_ylim(count - 0.4, -0.6)
        if axis_scale == "log":
            axis.set_xscale("log")
            axis.set_xlim((minimum / AXIS_LIMIT_PADDING, maximum * AXIS_LIMIT_PADDING)
                          if maximum > 0 else EMPTY_LOG_LIMITS)
            axis.xaxis.set_major_locator(LogLocator(base=10, subs=(1, 2, 5), numticks=9))
            # 紧凑数量级标签避免长整数相接；刻度位置、毫秒单位和完整观测范围保持不变。
            axis.xaxis.set_major_formatter(EngFormatter(sep="", useOffset=False))
            axis.xaxis.set_minor_formatter(NullFormatter())
        else:
            axis.set_xlim(0, max(maximum * AXIS_LIMIT_PADDING, 1))
            axis.xaxis.set_major_locator(pyplot.MaxNLocator(5))
        axis.set_xlabel(unit + (" · log scale" if axis_scale == "log" else ""), fontsize=10)
        axis.grid(axis="x", color="#eaecf0", linewidth=0.8)
        axis.set_axisbelow(True)
        heading = SCENARIO_LABELS[scenario]
        if model == "open":
            heading += f"  ·  {summary['arrival_rates'][scenario]:,} requests/s"
        axis.set_title(heading, fontsize=13, pad=12)
    note_panel(axes.flat[-1], summary, model, note, axis_scale)
    figure.text(0.055, 0.04, "All completed, audited protocol rounds are retained. Missing metrics appear as N/A.",
                fontsize=10, color="#667085")
    save_figure(figure, destination, name, pyplot)


def compact_range(values):
    if values["median"] is None:
        return "N/A"
    median, lower, upper = values["median"], values["min"], values["max"]
    if median == lower == upper == 0:
        return "0"
    if median == lower == upper:
        return f"{median:.3g}"
    return f"{median:.3g}\n[{lower:.3g}, {upper:.3g}]"


def draw_errors(summary, destination, model):
    pyplot = plotting()
    count = len(summary["variants"])
    figure, axes = pyplot.subplots(3, 2, figsize=(20, max(13, count * 0.45 * 3 + 2)),
                                  gridspec_kw={"hspace": 0.35, "wspace": 0.32})
    figure.subplots_adjust(top=0.90, bottom=0.08, left=0.13, right=0.97)
    figure_header(figure, f"Request outcomes · {model} loop", summary,
                  "Each cell: median percent [min, max] across 3 repeats · categories stay separate · all-zero cells show 0")
    for axis, scenario in zip(axes.flat, summary["scenarios"]):
        selected = [group for group in summary["groups"] if group["model"] == model and group["scenario"] == scenario]
        cells = [[compact_range(group["metrics"][field + "_percent"]) for field in ERROR_FIELDS] for group in selected]
        axis.axis("off")
        axis.set_title(SCENARIO_LABELS[scenario], fontsize=14, pad=14)
        table = axis.table(cellText=cells, rowLabels=[group_label(group) for group in selected],
                           colLabels=[label + " %" for label in ERROR_LABELS], loc="center",
                           cellLoc="center", bbox=[0, 0, 1, 0.95])
        table.auto_set_font_size(False)
        table.set_fontsize(8)
        for (row, column), cell in table.get_celld().items():
            cell.set_edgecolor("#eaecf0")
            cell.set_linewidth(0.5)
            if row == 0:
                cell.set_facecolor("#f2f4f7")
                cell.set_text_props(weight="bold", color="#344054")
            elif column == -1:
                cell.set_text_props(color="#344054", fontsize=8)
            else:
                metric = selected[row - 1]["metrics"][ERROR_FIELDS[column] + "_percent"]
                cell.set_facecolor("#fffaeb" if metric["max"] and metric["max"] > 0 else "white")
                if metric["max"] and metric["max"] > 0:
                    cell.set_text_props(color=ERROR_COLORS[column])
    axes.flat[-1].axis("off")
    axes.flat[-1].text(0.04, 0.95,
        "OUTCOME ACCOUNTING\n\nBusiness / HTTP: unexpected HTTP status\nTransport: request / response I/O failure\n"
        "Semantic: success status but invalid business response\nDropped: planned request rejected by bounded scheduler\n"
        "Not scheduled: expected arrival never scheduled\n\nDenominator: planned + not scheduled\n"
        "Raw counts for every repeat are in rounds.csv.\nCategory medians are not a pooled error rate.\n"
        "Dropped requests are not fabricated as latency samples.", va="top", fontsize=11, linespacing=1.6,
        color="#475467")
    save_figure(figure, destination, "errors_" + model, pyplot)


def draw_cache(summary, destination, model):
    pyplot = plotting()
    count = len(summary["variants"])
    figure, axes = pyplot.subplots(1, 2, figsize=(18, max(6, count * 0.4 + 2)),
                                  gridspec_kw={"wspace": 0.50})
    figure.subplots_adjust(top=0.78, bottom=0.28, left=0.18, right=0.97)
    figure_header(figure, f"Observed detail-cache hit ratio · {model} loop", summary,
                  "Median and min–max across 3 repeats · only responses carrying hit / miss enter the denominator")
    for axis, scenario in zip(axes, ["detail", "mixed"]):
        selected = [group for group in summary["groups"] if group["model"] == model and group["scenario"] == scenario]
        for position, group in enumerate(selected):
            values = group["metrics"]["cache_hit_percent"]
            if values["median"] is not None:
                axis.errorbar(values["median"], position,
                              xerr=[[values["median"] - values["min"]], [values["max"] - values["median"]]],
                              fmt="o", color=VARIANT_COLORS[position % len(VARIANT_COLORS)], capsize=3)
            else:
                axis.text(0, position, "N/A", va="center")
        axis.set_yticks(range(count), [group_label(group) for group in selected], fontsize=9)
        axis.set_ylim(count - 0.4, -0.6)
        axis.set_xlim(0, 100)
        axis.set_xlabel("Cache hits / cache-bearing responses (%)")
        axis.grid(axis="x", color="#eaecf0")
        axis.set_title(SCENARIO_LABELS[scenario], fontsize=14, pad=12)
    figure.text(0.055, 0.10,
        "Catalog, order and ticket scenarios have no detail-cache responses: N/A, not 0%.\n"
        "Mixed traffic may invalidate cached inventory after orders; the observed ratio is reported without forcing a target.",
        fontsize=10, color="#475467", linespacing=1.6)
    save_figure(figure, destination, "cache_" + model, pyplot)


def render_results(source, destination, *, allow_synthetic=False):
    import matplotlib

    result = load_results(source, allow_synthetic=allow_synthetic)
    summary = summarize(result)
    summary["rendering"] = {"matplotlib_version": matplotlib.__version__, "png_dpi": PNG_DPI,
                            "renderer_sha256": digest(__file__), "svg_text": "editable text"}
    destination = pathlib.Path(destination).resolve()
    require(not destination.exists() or not any(destination.iterdir()), "输出目录非空，拒绝覆盖已有结果")
    destination.mkdir(parents=True, exist_ok=True)
    write_tables(destination, result, summary)
    draw_metric(summary, destination, "success_rps", "closed", "closed_throughput",
                "Successful business throughput", "Successful requests / second",
                "Only successes completed inside the\nmeasurement window count toward throughput.")
    draw_metric(summary, destination, "p99_ms", "open", "open_p99",
                "P99 latency at the same offered load", "Scheduled-to-response P99 (ms)",
                "Includes scheduler delay and all completed\nrequests; interpret alongside errors / dropped.")
    for model in MODELS:
        draw_metric(summary, destination, "cpu_ms_per_success", model, "cpu_" + model,
                    f"Application CPU cost · {model} loop", "CPU milliseconds / successful request",
                    "Full invocation CPU / successful requests\nincluding drain; application + Nginx.")
        draw_metric(summary, destination, "peak_memory_mib", model, "memory_" + model,
                    f"Application process-tree memory · {model} loop",
                    "Peak sampled " + summary["memory_metric"].upper() + " (MiB)",
                    "PSS available in every round." if summary["memory_metric"] == "pss" else
                    "RSS fallback in all panels. Shared pages\nmay be counted repeatedly across processes.")
        draw_errors(summary, destination, model)
        draw_cache(summary, destination, model)
    return summary


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--input", required=True, help="完整正式结果目录")
    parser.add_argument("--output", required=True, help="新的图表与数据表目录")
    arguments = parser.parse_args(argv)
    summary = render_results(arguments.input, arguments.output)
    print(json.dumps({"mode": summary["mode"], "groups": len(summary["groups"]),
                      "output": str(pathlib.Path(arguments.output).resolve())}, ensure_ascii=False))


if __name__ == "__main__":
    main()
