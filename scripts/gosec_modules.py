"""在各自 Go 模块上下文中执行安全扫描，并严格校验每份结果。"""

import argparse
import json
import os
import subprocess
import sys
from pathlib import Path


GOSEC_TOOL = "github.com/securego/gosec/v2/cmd/gosec@v2.25.0"
EXCLUDED_PARTS = frozenset({"vendor", "testdata", "tmp", "bin"})
MAX_VISIBLE_FINDINGS = 20
MAX_ERROR_TEXT_LENGTH = 4096


def discover_modules(raw_paths: bytes) -> list[Path]:
    """使用 Git 的零字节分隔清单，不把嵌套模块误归到根模块。"""
    if not raw_paths or not raw_paths.endswith(b"\0"):
        raise ValueError("Git 文件清单为空或未以零字节终止")
    modules = set()
    for raw_path in raw_paths[:-1].split(b"\0"):
        parts = os.fsdecode(raw_path).split("/")
        if parts[-1] != "go.mod":
            continue
        if any(part in {"", ".", ".."} for part in parts):
            raise ValueError("模块清单包含非规范路径")
        if EXCLUDED_PARTS.intersection(parts[:-1]):
            continue
        modules.add(Path(*parts[:-1]))
    if Path(".") not in modules:
        raise ValueError("仓库根模块不在受版本控制的扫描范围内")
    return sorted(modules, key=lambda path: (len(path.parts), str(path)))


def package_directories(raw_paths: str, module: Path) -> list[str]:
    """只允许本模块的显式包目录，避免扫描器再次递归穿过模块边界。"""
    module = module.resolve()
    if not (module / "go.mod").is_file():
        raise ValueError("模块缺少 go.mod")
    directories = []
    for raw_path in raw_paths.splitlines():
        directory = Path(raw_path).resolve()
        relative = directory.relative_to(module)
        if EXCLUDED_PARTS.intersection(relative.parts):
            continue
        if not directory.is_dir():
            raise ValueError("包目录不存在")
        owner = next((path for path in (directory, *directory.parents) if (path / "go.mod").is_file()), None)
        if owner != module:
            raise ValueError("包目录跨越了独立模块边界")
        directories.append(str(directory))
    if not directories:
        raise ValueError("模块没有可扫描的 Go 包，拒绝以空扫描通过")
    return directories


def module_package_patterns(directories: list[str], module: Path) -> list[str]:
    """将已核验的绝对目录改为模块相对包模式，避免 go/packages 构造 command-line-arguments。"""
    module = module.resolve()
    # 中文注释：再次验证目录归属，禁止外部调用绕过前面的 go list 边界检查。
    checked = package_directories("\n".join(directories), module)
    if checked != directories or len(set(checked)) != len(checked):
        raise ValueError("扫描目录未完整通过归属校验或存在重复")
    patterns = []
    for directory in checked:
        relative = Path(directory).relative_to(module)
        patterns.append("." if relative == Path(".") else f"./{relative.as_posix()}")
    return patterns


def validate_report(result: object) -> None:
    """缺字段、空覆盖、发现、加载错误及格式错误均不能通过门禁。"""
    if not isinstance(result, dict) or not {"Stats", "Issues", "Golang errors"}.issubset(result):
        raise ValueError("安全扫描报告缺少必须字段")
    stats, issues, errors = result["Stats"], result["Issues"], result["Golang errors"]
    if not isinstance(stats, dict) or any(type(stats.get(key)) is not int for key in ("files", "found")):
        raise ValueError("安全扫描统计格式无效")
    if issues is not None and not isinstance(issues, list):
        raise ValueError("安全发现列表格式无效")
    if errors is not None and not isinstance(errors, dict):
        raise ValueError("Go 包加载错误格式无效")
    issues, errors = issues or [], errors or {}
    print("安全扫描统计：" + json.dumps(stats, ensure_ascii=True), file=sys.stderr)
    for issue in issues[:MAX_VISIBLE_FINDINGS]:
        print(json.dumps(issue, ensure_ascii=True), file=sys.stderr)
    if len(issues) > MAX_VISIBLE_FINDINGS:
        print(f"另有 {len(issues) - MAX_VISIBLE_FINDINGS} 项发现未逐条打印", file=sys.stderr)
    if errors:
        print("Go 包加载错误：" + json.dumps(errors, ensure_ascii=True)[:MAX_ERROR_TEXT_LENGTH], file=sys.stderr)
    if stats["files"] <= 0 or stats["found"] != 0 or issues or errors:
        raise ValueError("安全扫描未覆盖源码或存在错误，拒绝发布")


def scan_module(module: Path, report_path: Path, *, nested: bool = False) -> None:
    """只读解析本模块依赖；保留扫描器退出失败，不复用旧报告。"""
    environment = {**os.environ, "GOWORK": "off", "CGO_ENABLED": "0"}
    if nested:
        # 中文注释：嵌套基准模块保留冻结的 go.mod 版本，仅该模块允许自动获取工具链。
        environment["GOTOOLCHAIN"] = "auto"
    packages = subprocess.run(
        ["go", "list", "-mod=readonly", "-f", "{{.Dir}}", "./..."],
        cwd=module, env=environment, check=True, stdout=subprocess.PIPE, text=True,
    )
    directories = package_directories(packages.stdout, module)
    patterns = module_package_patterns(directories, module)
    report_path.unlink(missing_ok=True)
    scanned = subprocess.run(
        ["go", "run", "-mod=readonly", GOSEC_TOOL, "-no-fail", "-fmt=json", f"-out={report_path}", *patterns],
        cwd=module, env=environment, check=False,
    )
    with report_path.open(encoding="utf-8") as report:
        validate_report(json.load(report))
    if scanned.returncode != 0:
        raise ValueError(f"安全扫描器退出失败：{scanned.returncode}")


def run_scans(repository: Path, reports_directory: Path) -> bool:
    """所有受版本控制的非夹具模块都必须扫描，单模块失败不跳过其余模块。"""
    repository, reports_directory = repository.resolve(), reports_directory.resolve()
    tracked = subprocess.run(["git", "ls-files", "-z"], cwd=repository, check=True, stdout=subprocess.PIPE)
    modules = discover_modules(tracked.stdout)
    reports_directory.mkdir(parents=True, exist_ok=True)
    passed = True
    for index, relative in enumerate(modules):
        report_path = reports_directory / f"module-{index}.json"
        print(f"扫描模块 {str(relative)!r}，报告 {str(report_path)!r}", file=sys.stderr)
        try:
            module = (repository / relative).resolve()
            module.relative_to(repository)
            scan_module(module, report_path, nested=relative != Path("."))
        except (OSError, subprocess.CalledProcessError, ValueError) as error:
            passed = False
            print(f"模块 {str(relative)!r} 未通过：{error!r}", file=sys.stderr)
    return passed


def main(arguments: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repository", type=Path, default=Path("."))
    parser.add_argument("--reports-dir", type=Path, required=True)
    args = parser.parse_args(arguments)
    try:
        return 0 if run_scans(args.repository, args.reports_dir) else 1
    except (OSError, subprocess.CalledProcessError, ValueError) as error:
        print(f"无法完成安全扫描：{error!r}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
