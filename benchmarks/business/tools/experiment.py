"""冻结实验矩阵、共同到达率和测量完整性规则，不筛除慢轮次或业务失败。"""

import json
import math
import pathlib
import random
import re
import statistics
from fractions import Fraction


SCENARIOS = ("catalog", "detail", "order", "ticket", "mixed")
MODELS = ("closed", "open")
COUNTERS = (
    "planned", "started", "completed", "success", "success_within_window",
    "status_errors", "transport_errors", "semantic_errors", "dropped", "cache_hits", "cache_misses",
)
IDENTIFIER = re.compile(r"[A-Za-z0-9][A-Za-z0-9_-]{0,95}\Z")
MAXIMUM_PROTOCOL_BYTES = 1024 * 1024
NANOSECONDS_PER_SECOND = 1000000000


def _require(condition, message):
    if not condition:
        raise ValueError(message)


def _mapping(value, label):
    _require(isinstance(value, dict), f"{label} 必须为对象")
    return value


def _integer(value, label, minimum=0):
    _require(isinstance(value, int) and not isinstance(value, bool) and value >= minimum,
             f"{label} 必须为不小于 {minimum} 的整数")
    return value


def _number(value, label, positive=False):
    _require(isinstance(value, (int, float)) and not isinstance(value, bool), f"{label} 必须为数值")
    try:
        finite = math.isfinite(value)
    except OverflowError:
        finite = False
    _require(finite and (value > 0 if positive else value >= 0), f"{label} 必须为有限{'正' if positive else '非负'}数")
    return value


def _unique_object(pairs):
    result = {}
    for key, value in pairs:
        _require(key not in result, f"JSON 字段重复: {key}")
        result[key] = value
    return result


def load_protocol(path):
    """读取并校验协议；重复字段和 JSON 非有限常量均拒绝。"""
    encoded = pathlib.Path(path).read_bytes()
    _require(len(encoded) <= MAXIMUM_PROTOCOL_BYTES, "实验协议超过读取预算")

    def reject_constant(value):
        raise ValueError(f"JSON 不允许常量 {value}")

    protocol = json.loads(encoded, object_pairs_hook=_unique_object, parse_constant=reject_constant)
    validate_protocol(protocol)
    return protocol


def validate_protocol(protocol):
    """验证调度所需的完整核心配置；基础设施与解释元数据保留给相应工具验证。"""
    _mapping(protocol, "protocol")
    _require(protocol.get("schema_version") == 1 and type(protocol.get("schema_version")) is int,
             "protocol.schema_version 必须为1")
    _integer(protocol.get("seed"), "seed")
    _integer(protocol.get("repetitions"), "repetitions", 3)
    for field in ("closed_concurrency", "open_max_inflight", "warmup_rate"):
        _integer(protocol.get(field), field, 1)
    for field in ("warmup_seconds", "measurement_seconds", "sample_interval_seconds"):
        _number(protocol.get(field), field, positive=True)
    ratio = _number(protocol.get("open_rate_ratio"), "open_rate_ratio", positive=True)
    _require(ratio <= 1, "open_rate_ratio 不得大于1")
    scenarios = protocol.get("scenarios")
    _require(isinstance(scenarios, list) and len(scenarios) == len(SCENARIOS) and
             all(isinstance(item, str) for item in scenarios) and set(scenarios) == set(SCENARIOS),
             "scenarios 必须且只能包含五个不重复业务场景")
    variants = protocol.get("variants")
    _require(isinstance(variants, list) and len(variants) > 0, "variants 不能为空")
    identifiers = set()
    for variant in variants:
        _mapping(variant, "variant")
        for field in ("id", "framework", "version", "engine", "kind"):
            _require(isinstance(variant.get(field), str) and variant[field].strip(), f"variant.{field} 不能为空")
        identifier = variant["id"]
        _require(IDENTIFIER.fullmatch(identifier) is not None and identifier not in identifiers,
                 "variant.id 必须是安全且不重复的路径标识")
        identifiers.add(identifier)
        if "app" in variant:
            _require(isinstance(variant["app"], str) and IDENTIFIER.fullmatch(variant["app"]) is not None,
                     "variant.app 不是合法应用名称")
    if "ports" in protocol:
        ports = _mapping(protocol["ports"], "ports")
        for value in ports.values():
            _require(_integer(value, "port", 1) <= 65535, "port 超出端口范围")
        _require(len(set(ports.values())) == len(ports), "基础设施端口重复")
    if "cpu_affinity" in protocol:
        for group, cpus in _mapping(protocol["cpu_affinity"], "cpu_affinity").items():
            _require(isinstance(cpus, list) and cpus, f"{group} 的CPU集合不能为空")
            for cpu in cpus:
                _integer(cpu, "CPU编号")
            _require(len(set(cpus)) == len(cpus), f"{group} 的CPU编号重复")
    for field in ("request_timeout_seconds", "gateway_workers", "gateway_connections", "gateway_upstream_keepalive"):
        if field in protocol:
            _integer(protocol[field], field, 1)
    if "cooldown_seconds" in protocol:
        _number(protocol["cooldown_seconds"], "cooldown_seconds")


def build_schedule(protocol, model):
    """每个重复块包含完整变体×场景矩阵，块内使用同一固定种子PRNG依次洗牌。"""
    validate_protocol(protocol)
    _require(model in MODELS, "model 必须为 closed 或 open")
    generator = random.Random(protocol["seed"])
    schedule = []
    for repeat in range(1, protocol["repetitions"] + 1):
        block = []
        for variant in protocol["variants"]:
            for scenario in protocol["scenarios"]:
                block.append({"id": f"{model}-{scenario}-{variant['id']}-{repeat}", "model": model,
                              "variant": variant["id"], "scenario": scenario, "repeat": repeat})
        generator.shuffle(block)
        schedule.extend(block)
    return schedule


def choose_rates(protocol, samples):
    """必须获得全部独立闭环重复；每场景取最小变体中位数的指定比例并向下取整。"""
    validate_protocol(protocol)
    variants = {variant["id"] for variant in protocol["variants"]}
    groups = {(variant, scenario): [] for variant in variants for scenario in protocol["scenarios"]}
    repeats = {key: set() for key in groups}
    identifiers = set()
    for sample in samples:
        _mapping(sample, "校准样本")
        model = sample.get("model", "closed")
        _require(model in MODELS, "校准样本model未知")
        if model == "open":
            continue
        key = (sample.get("variant"), sample.get("scenario"))
        _require(key in groups, "校准样本包含未知变体或场景")
        _require(sample.get("valid") is True, "校准样本未通过完整性审计，不能删除后继续校准")
        value = _number(sample.get("success_rps"), "success_rps")
        if "repeat" in sample:
            repeat = _integer(sample["repeat"], "repeat", 1)
            _require(repeat <= protocol["repetitions"] and repeat not in repeats[key], "校准重复编号缺失、越界或重复")
            repeats[key].add(repeat)
        if "id" in sample:
            _require(isinstance(sample["id"], str) and sample["id"] not in identifiers, "校准轮次id重复")
            identifiers.add(sample["id"])
        groups[key].append(value)
    for key, values in groups.items():
        _require(len(values) == protocol["repetitions"], f"校准矩阵不完整: {key}")
        _require(not repeats[key] or len(repeats[key]) == protocol["repetitions"], f"校准重复编号不完整: {key}")
    return {scenario: max(1, math.floor(min(statistics.median(groups[(variant, scenario)])
                                          for variant in variants) * protocol["open_rate_ratio"]))
            for scenario in protocol["scenarios"]}


def _counts(value, label, conservation=True):
    value = _mapping(value, label)
    for field in COUNTERS:
        _integer(value.get(field), f"{label}.{field}")
    if conservation:
        _require(value["planned"] == value["started"] + value["dropped"], f"{label} 计划请求不守恒")
        _require(value["started"] == value["completed"], f"{label} 在途请求未排空")
        _require(value["completed"] == sum(value[field] for field in
                 ("success", "status_errors", "transport_errors", "semantic_errors")), f"{label} 完成结果不守恒")
    _require(value["success_within_window"] <= value["success"], f"{label} 窗口成功数越界")
    _require(value["cache_hits"] + value["cache_misses"] <= value["completed"], f"{label} 缓存响应数越界")
    return value


def _histogram(value, expected, label):
    value = _mapping(value, label)
    _require(_integer(value.get("overflow"), f"{label}.overflow") == 0, f"{label} 存在直方图溢出")
    _require(_integer(value.get("count"), f"{label}.count") == expected, f"{label} 延迟样本数量不守恒")
    for field in ("p50_ms", "p95_ms", "p99_ms", "max_ms", "mean_ms"):
        _number(value.get(field), f"{label}.{field}")
    _require(value["p50_ms"] <= value["p95_ms"] <= value["p99_ms"] <= value["max_ms"] and
             value["mean_ms"] <= value["max_ms"], f"{label} 分位数顺序无效")
    if not expected:
        _require(all(value[field] == 0 for field in ("p50_ms", "p95_ms", "p99_ms", "max_ms", "mean_ms")),
                 f"{label} 无样本却存在延迟数值")
    snapshot = _mapping(value.get("hdr_microseconds"), f"{label}.hdr_microseconds")
    buckets = snapshot.get("Counts")
    _require(isinstance(buckets, list) and buckets, f"{label} 缺少完整HDR快照")
    for count in buckets:
        _integer(count, f"{label}.Counts")
    _require(sum(buckets) == expected, f"{label} HDR快照样本数不符")
    _require(_integer(snapshot.get("LowestTrackableValue"), "HDR最低值") <=
             _integer(snapshot.get("HighestTrackableValue"), "HDR最高值", 1), "HDR范围无效")
    _require(_integer(snapshot.get("SignificantFigures"), "HDR精度", 1) <= 5, "HDR精度无效")


def _metric_group(group, counts, label):
    for name, count in (("latency", counts["completed"]), ("success_latency", counts["success"]),
                        ("http_duration", counts["completed"]), ("scheduler_lag", counts["planned"])):
        _histogram(group.get(name), count, f"{label}.{name}")


def _same_number(actual, expected, label):
    _number(actual, label)
    _require(math.isclose(actual, expected, rel_tol=1e-9, abs_tol=1e-9), f"{label} 与原始计数或时间不符")


def check_load_report(report):
    """拒绝损坏或未完成的测量；合法业务错误与过载丢弃按原值保留。"""
    _mapping(report, "load report")
    _require(report.get("schema_version") == 1 and type(report.get("schema_version")) is int,
             "load report.schema_version 必须为1")
    _require(report.get("canceled") is False, "取消的测量不能进入正式结果")
    _require(_integer(report.get("not_scheduled"), "not_scheduled") == 0, "仍有未调度计划")
    configuration = _mapping(report.get("config"), "load config")
    model = report.get("model")
    _require(model in MODELS and configuration.get("scenario") in SCENARIOS, "负载模型或场景无效")
    window = _number(report.get("window_seconds"), "window_seconds", positive=True)
    elapsed = _number(report.get("elapsed_seconds"), "elapsed_seconds", positive=True)
    _require(elapsed >= window, "总测量时间短于发送窗口")
    _same_number(report.get("drain_seconds"), elapsed - window, "drain_seconds")
    duration = _integer(configuration.get("duration_ns"), "duration_ns", 1)
    _same_number(window, duration / NANOSECONDS_PER_SECOND, "window_seconds")
    counts = _counts(report.get("totals"), "totals")
    _require(counts["planned"] > 0, "没有执行任何请求的轮次不能作为测量")
    rate = _number(configuration.get("rate_per_second"), "rate_per_second")
    expected = _integer(report.get("expected"), "expected")
    if model == "open":
        _require(rate > 0 and expected == counts["planned"], "开放负载计划数量不符")
        planned = math.ceil(Fraction(str(rate)) * duration / NANOSECONDS_PER_SECOND)
        _require(expected == planned, "开放计划数与速率及窗口不符")
    else:
        _require(rate == 0 and expected == 0 and counts["dropped"] == 0, "闭环负载出现开放调度数据")
    _same_number(report.get("success_rps"), counts["success_within_window"] / window, "success_rps")
    _same_number(report.get("issued_success_rps"), counts["success"] / window, "issued_success_rps")
    _same_number(report.get("elapsed_success_rps"), counts["success"] / elapsed, "elapsed_success_rps")
    cached = counts["cache_hits"] + counts["cache_misses"]
    _same_number(report.get("cache_hit_ratio"), counts["cache_hits"] / cached if cached else 0, "cache_hit_ratio")
    statuses = _mapping(report.get("statuses"), "statuses")
    for status, count in statuses.items():
        _require(isinstance(status, str) and re.fullmatch(r"[1-9][0-9]{2}", status), "HTTP状态码无效")
        _integer(count, "HTTP状态数量")
    # 读取正文时失败仍可能已收到HTTP状态，因此状态总数可以包含部分transport错误。
    _require(counts["completed"] - counts["transport_errors"] <= sum(statuses.values()) <= counts["completed"],
             "HTTP状态数量与完成请求不符")
    reasons = _mapping(report.get("drop_reasons"), "drop_reasons")
    for reason, count in reasons.items():
        _require(isinstance(reason, str) and reason, "丢弃原因为空")
        _integer(count, "丢弃数量")
    _require(sum(reasons.values()) == counts["dropped"], "丢弃原因数量不守恒")
    _metric_group(report, counts, "total")
    scenarios = _mapping(report.get("scenarios"), "scenarios")
    _require(scenarios, "缺少分场景测量")
    sums = dict.fromkeys(COUNTERS, 0)
    for scenario, group in scenarios.items():
        _require(scenario in SCENARIOS, "分场景名称未知")
        group = _mapping(group, scenario)
        values = _counts(group.get("totals"), scenario)
        _metric_group(group, values, scenario)
        for field in COUNTERS:
            sums[field] += values[field]
    _require(sums == {field: counts[field] for field in COUNTERS}, "分场景计数之和不等于总数")
    seconds = report.get("seconds")
    _require(isinstance(seconds, list) and seconds, "缺少逐秒计数")
    sums, observed = dict.fromkeys(COUNTERS, 0), set()
    for item in seconds:
        item = _mapping(item, "逐秒样本")
        second = _integer(item.get("second"), "second")
        _require(second not in observed, "逐秒采样桶重复")
        observed.add(second)
        values = _counts(item.get("counts"), "逐秒计数", conservation=False)
        for field in COUNTERS:
            sums[field] += values[field]
    _require(sums == {field: counts[field] for field in COUNTERS}, "逐秒计数之和不等于总数")
    unsuccessful = counts["planned"] - counts["success"]
    return {"unsuccessful": unsuccessful, "failure_fraction": unsuccessful / counts["planned"]}
