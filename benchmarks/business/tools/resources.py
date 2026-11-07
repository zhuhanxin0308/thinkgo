"""按进程身份采集本机资源，保留原始证据、读取失败和RSS/PSS口径。"""

import datetime
import math
import threading
import time

import psutil


READ_ERRORS = (psutil.Error, OSError, AttributeError, NotImplementedError)


def _utc(timestamp):
    return datetime.datetime.fromtimestamp(timestamp, datetime.timezone.utc).isoformat()


def _error(pid, operation, error):
    # 只保存异常类型和读操作，不把异常消息中的潜在敏感参数写入证据。
    return {"pid": pid, "operation": operation,
            "error": error if isinstance(error, str) else type(error).__name__}


def _finite(value):
    if not isinstance(value, (int, float)) or isinstance(value, bool):
        return False
    try:
        return math.isfinite(value) and value >= 0
    except OverflowError:
        return False


def _read(process, operation, callback, errors):
    try:
        return callback()
    except READ_ERRORS as error:
        errors.append(_error(process.pid, operation, error))
        return None


def _cpu_seconds(process):
    times = process.cpu_times()
    return times.user + times.system


def _process_snapshot(process):
    errors = []
    row = {"pid": process.pid, "errors": errors}
    for field, operation, callback in (
        ("create_time", "create_time", process.create_time),
        ("ppid", "ppid", process.ppid), ("name", "name", process.name),
        ("cmdline", "cmdline", process.cmdline),
        ("affinity", "cpu_affinity", lambda: process.cpu_affinity()),
        ("cpu_seconds", "cpu_times", lambda: _cpu_seconds(process)),
        ("rss_bytes", "memory_info", lambda: process.memory_info().rss),
        ("pss_bytes", "memory_full_info", lambda: process.memory_full_info().pss),
    ):
        row[field] = _read(process, operation, callback, errors)
    for field in ("create_time", "cpu_seconds", "rss_bytes", "pss_bytes"):
        if row[field] is not None and not _finite(row[field]):
            errors.append(_error(process.pid, field, "InvalidCounter"))
            row[field] = None
    if row["affinity"] is not None:
        row["affinity"] = sorted(set(row["affinity"]))
    return row


class Sampler:
    """采样指定根进程及全部可见后代；同组去重，跨组交叠拒绝重复归因。"""

    def __init__(self, groups, interval):
        if not isinstance(groups, dict) or not groups:
            raise ValueError("资源分组不能为空")
        if not _finite(interval) or interval <= 0:
            raise ValueError("采样间隔必须是有限正数")
        roots, owner = {}, {}
        for name, pids in groups.items():
            if not isinstance(name, str) or not name or not isinstance(pids, (list, tuple)) or not pids:
                raise ValueError("资源组必须包含名称和根PID列表")
            for pid in pids:
                if not isinstance(pid, int) or isinstance(pid, bool) or pid <= 0:
                    raise ValueError("根PID必须是正整数")
                if pid in owner and owner[pid] != name:
                    raise ValueError("同一根PID不能归入多个资源组")
                owner[pid] = name
            roots[name] = sorted(set(pids))
        self.groups = roots
        self.interval = float(interval)
        self._started = False
        self._report = None
        self._stop_event = threading.Event()
        self._thread = None
        self._samples = []
        self._root_identities = {}
        self._missed_intervals = 0
        self._started_wall = None
        self._started_monotonic = None

    def start(self):
        if self._started:
            raise RuntimeError("采样器不能重复启动")
        self._started = True
        self._started_wall = time.time()
        self._started_monotonic = time.monotonic()
        self._safe_capture()
        self._thread = threading.Thread(target=self._loop, name="business-resource-sampler", daemon=True)
        self._thread.start()
        return self

    def stop(self):
        if not self._started:
            raise RuntimeError("采样器尚未启动")
        if self._report is not None:
            return self._report
        self._stop_event.set()
        self._thread.join()
        self._safe_capture()
        finished_wall = time.time()
        elapsed = time.monotonic() - self._started_monotonic
        self._report = {
            "schema_version": 1, "started_at": _utc(self._started_wall), "finished_at": _utc(finished_wall),
            "elapsed_seconds": elapsed, "interval_seconds": self.interval,
            "missed_intervals": self._missed_intervals, "roots": self.groups,
            "samples": self._samples, "groups": {name: self._summarize(name) for name in self.groups},
            "sampling_limits": (
                "CPU只累加每个(pid,create_time)的user+system，不重复累加children时间；既有进程使用首末差，"
                "窗口内出生的已观察进程从零计。相邻采样间完整出生退出的进程无法观察；已观察进程消失时标记CPU不完整。"
                "内存为采样峰值而非连续峰值；RSS逐进程求和包含共享页重复，仅PSS完整时使用PSS。"
            ),
        }
        return self._report

    def _loop(self):
        deadline = self._started_monotonic + self.interval
        while not self._stop_event.wait(max(0, deadline - time.monotonic())):
            self._safe_capture()
            deadline += self.interval
            now = time.monotonic()
            if deadline <= now:
                skipped = math.floor((now - deadline) / self.interval) + 1
                self._missed_intervals += skipped
                deadline += skipped * self.interval

    def _safe_capture(self):
        try:
            self._samples.append(self._capture())
        except Exception as error:
            # 非预期采样异常也形成缺失证据，不让线程静默退出后生成正常摘要。
            self._samples.append({
                "timestamp": _utc(time.time()), "elapsed_seconds": time.monotonic() - self._started_monotonic,
                "collection_seconds": None, "overlapping_processes": [],
                "groups": {name: {"processes": [], "errors": [_error(None, "sampling", error)]}
                           for name in self.groups},
            })

    def _capture(self):
        started = time.monotonic()
        sample = {"timestamp": _utc(time.time()), "elapsed_seconds": started - self._started_monotonic,
                  "groups": {name: {"processes": [], "errors": []} for name in self.groups},
                  "overlapping_processes": []}
        processes, memberships = {}, {}
        for name, pids in self.groups.items():
            errors = sample["groups"][name]["errors"]
            for pid in pids:
                try:
                    process = psutil.Process(pid)
                    created = process.create_time()
                    if not _finite(created):
                        errors.append(_error(pid, "root_identity", "InvalidCounter"))
                        continue
                    known = self._root_identities.get(pid)
                    if known is not None and known != created:
                        errors.append(_error(pid, "root_identity", "RootPIDReused"))
                        continue
                    self._root_identities[pid] = created
                except READ_ERRORS as error:
                    errors.append(_error(pid, "root_identity", error))
                    continue
                candidates = [process]
                try:
                    candidates.extend(process.children(recursive=True))
                except READ_ERRORS as error:
                    errors.append(_error(pid, "children", error))
                for candidate in candidates:
                    processes[candidate.pid] = candidate
                    memberships.setdefault(candidate.pid, set()).add(name)
        for pid in sorted(processes):
            row = _process_snapshot(processes[pid])
            owners = memberships[pid]
            if len(owners) > 1:
                sample["overlapping_processes"].append({"process": row, "groups": sorted(owners)})
                for name in owners:
                    sample["groups"][name]["errors"].append(_error(pid, "membership", "OverlappingProcess"))
                continue
            sample["groups"][next(iter(owners))]["processes"].append(row)
        sample["collection_seconds"] = time.monotonic() - started
        return sample

    def _summarize(self, name):
        states, previous, errors = {}, set(), []
        cpu_complete = True
        rss_complete = self._missed_intervals == 0
        pss_complete = self._missed_intervals == 0
        missing, populated = self._missed_intervals, 0
        rss_peaks, pss_peaks = [], []
        for index, sample in enumerate(self._samples):
            group = sample["groups"][name]
            rows = group["processes"]
            errors.extend({"sample": index, "timestamp": sample["timestamp"], **error}
                          for error in group["errors"])
            current = set()
            if rows:
                populated += 1
            if not rows or group["errors"]:
                cpu_complete = False
            memory_valid = bool(rows) and not group["errors"] and all(
                row["create_time"] is not None and row["rss_bytes"] is not None for row in rows)
            pss_valid = memory_valid and all(row["pss_bytes"] is not None for row in rows)
            missing += int(not memory_valid)
            rss_complete = rss_complete and memory_valid
            pss_complete = pss_complete and pss_valid
            if memory_valid:
                rss_peaks.append(sum(row["rss_bytes"] for row in rows))
            if pss_valid:
                pss_peaks.append(sum(row["pss_bytes"] for row in rows))
            for row in rows:
                errors.extend({"sample": index, "timestamp": sample["timestamp"], **error}
                              for error in row["errors"])
                created, value = row["create_time"], row["cpu_seconds"]
                if created is None:
                    cpu_complete = False
                    continue
                identity = (row["pid"], created)
                current.add(identity)
                if value is None:
                    cpu_complete = False
                    continue
                if identity not in states:
                    # 后出生进程的全部累计CPU均属于窗口；旧进程只计算首样本以后的增量。
                    baseline = 0 if created >= self._started_wall else value
                    states[identity] = {"baseline": baseline, "last": value}
                elif value < states[identity]["last"]:
                    cpu_complete = False
                    errors.append({"sample": index, "timestamp": sample["timestamp"],
                                   **_error(row["pid"], "cpu_times", "CPUCounterDecreased")})
                else:
                    states[identity]["last"] = value
            for pid, _created in previous - current:
                # 退出后的最终CPU无法从轮询中恢复；保留最后观察值并明确其不完整。
                cpu_complete = False
                errors.append({"sample": index, "timestamp": sample["timestamp"],
                               **_error(pid, "membership", "ProcessDisappeared")})
            previous = current
        observed_cpu = sum(value["last"] - value["baseline"] for value in states.values())
        peak_rss = max(rss_peaks) if rss_complete and rss_peaks else None
        peak_pss = max(pss_peaks) if pss_complete and pss_peaks else None
        if self._missed_intervals:
            errors.append({**_error(None, "scheduling", "SamplingIntervalsMissed"),
                           "count": self._missed_intervals})
        return {
            "cpu_seconds": observed_cpu if cpu_complete else None,
            "observed_cpu_seconds": observed_cpu, "cpu_complete": cpu_complete,
            "peak_rss_bytes": peak_rss, "peak_pss_bytes": peak_pss,
            "peak_memory_bytes": peak_pss if pss_complete else peak_rss,
            "memory_metric": "pss" if pss_complete else "rss",
            "pss_complete": bool(pss_complete), "rss_complete": bool(rss_complete),
            "sample_count": len(self._samples), "samples_with_processes": populated,
            "missing_samples": missing, "errors": errors,
        }
