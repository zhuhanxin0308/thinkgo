"""正式测量按 Go 工具链选择的本地输入，核对固定提交中的路径和字节。"""

from contextlib import contextmanager
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile

from provenance import BUSINESS_GO_PATH, committed_sources, definitions, digest, parse_build_info, require


# 与 Go 构建动作计算包输入摘要时读取的原始文件字段保持一致。
PACKAGE_INPUTS = (
    "GoFiles", "CgoFiles", "CFiles", "CXXFiles", "FFiles", "MFiles", "HFiles",
    "SFiles", "SysoFiles", "SwigFiles", "SwigCXXFiles", "EmbedFiles",
)
PACKAGE_FIELDS = ("Name", "ImportPath", "Deps", "DepOnly", "Dir", "Standard", "Module", "Incomplete", "Error",
                  *PACKAGE_INPUTS)
ARCH_ENV = ("GO386", "GOAMD64", "GOARM", "GOARM64", "GOMIPS", "GOMIPS64", "GOPPC64", "GORISCV64", "GOWASM")
CGO_FLAGS = ("CGO_CFLAGS", "CGO_CPPFLAGS", "CGO_CXXFLAGS", "CGO_LDFLAGS")
SELECTOR_ENV = ("GOOS", "GOARCH", "CGO_ENABLED", "GOEXPERIMENT", "GOFIPS140", *ARCH_ENV, *CGO_FLAGS)
BUILD_FLAGS = ("-tags", "-compiler", "-buildmode", "-trimpath", "-race", "-cover", "-asan", "-msan",
               "-asmflags", "-gcflags", "-gccgoflags", "-ldflags")


def command(argv, cwd, env):
    try:
        return subprocess.run(argv, cwd=cwd, env=env, check=True, stdout=subprocess.PIPE,
                              stderr=subprocess.PIPE, encoding="utf-8", errors="surrogateescape").stdout
    except subprocess.CalledProcessError as exc:
        detail = (exc.stderr or "").strip().splitlines()
        raise ValueError(f"正式构建输入核验失败: {argv[0]}: {detail[-1] if detail else exc.returncode}") from exc
    except OSError as exc:
        raise ValueError(f"无法执行正式构建输入核验: {argv[0]}: {exc}") from exc


def objects(raw):
    decoder = json.JSONDecoder()
    offset = 0
    while offset < len(raw):
        if raw[offset].isspace():
            offset += 1
            continue
        obj, offset = decoder.raw_decode(raw, offset)
        require(isinstance(obj, dict), "Go 依赖清单不是对象")
        yield obj


def replay(go, go_dir, info):
    settings = info["settings"]
    require(all(settings.get(k) for k in ("GOOS", "GOARCH", "CGO_ENABLED")), "Go 构建缺少平台或 CGO 设置")
    env = os.environ.copy()
    for key in (*SELECTOR_ENV, "GOROOT"):
        env.pop(key, None)
    env.update(GOWORK="off", GOENV="off", GOFLAGS="", GOTOOLCHAIN="local", GO111MODULE="on")
    env.update({key: settings[key] for key in SELECTOR_ENV if key in settings})
    require(command([go, "env", "GOVERSION"], go_dir, env).strip() == info["go_version"],
            "枚举依赖的实际 Go 工具链与二进制不一致")
    flags = [f"{key}={settings[key]}" for key in BUILD_FLAGS if key in settings]
    return env, flags


def owned_path(root, name):
    path = Path(name)
    require(path.is_absolute() and ".." not in path.parts and path.is_relative_to(root),
            f"本地模块或输入位于固定仓库之外: {path}")
    # 正式核验只接受能直接映射到 Git 普通文件的路径；链接不能借目标名称通过。
    require(path.resolve(strict=True) == path, f"正式构建输入不支持符号链接路径: {path}")
    return path


def regular_sources(repository, commit, sources):
    listing = subprocess.check_output(["git", "ls-tree", "-r", "--full-tree", "-z", commit], cwd=repository)
    result = {}
    for item in listing.split(b"\0"):
        if item:
            metadata, name = item.split(b"\t", 1)
            mode, kind, _ = metadata.split()
            if kind == b"blob" and mode in (b"100644", b"100755"):
                key = name.decode("utf-8", errors="surrogateescape")
                result[key] = sources[key]
    return result


def binary_modules(raw):
    result = {}
    previous = None
    for line in raw.splitlines()[1:]:
        fields = line.removeprefix("\t").split("\t")
        tag = fields[0]
        if tag == "dep":
            require(len(fields) in (3, 4) and all(fields[1:3]), "Go 二进制依赖记录无效")
            path, version = fields[1:3]
            require(path not in result, f"Go 二进制重复声明依赖: {path}")
            result[path] = (version, "", "")
            previous = path
        elif tag == "=>":
            require(previous is not None and len(fields) in (3, 4) and all(fields[1:3]),
                    "Go 二进制替换记录无效")
            result[previous] = (result[previous][0], fields[1], fields[2])
            previous = None
        else:
            previous = None
    return result


def listed_module(module):
    require(isinstance(module, dict) and module.get("Path"), "Go 包没有可验证的模块身份")
    replacement = module.get("Replace")
    return module["Path"], (module.get("Version") or "(devel)", replacement["Path"] if replacement else "",
                            (replacement.get("Version") or "(devel)") if replacement else "")


def module_directory(repo, module, selected, sources):
    replacement = module.get("Replace")
    target = replacement if replacement is not None else module
    if not module.get("Main") and target.get("Version"):
        # 即使下载缓存放在仓库内部，有版本的外部模块也不属于本地源码。
        return None
    require(module.get("Main") or replacement is not None, "无法确认无版本 Go 模块的本地来源")
    require(isinstance(target.get("Dir"), str) and target["Dir"], "本地 Go 模块目录缺失")
    directory = owned_path(repo, target["Dir"])
    modfile = directory / "go.mod"
    require(target.get("GoMod") == str(modfile), f"本地模块使用了非预期的 go.mod: {directory}")
    selected.add(modfile)
    if module.get("Main"):
        # 关闭工作区后，Go 只读取当前主模块的 go.sum。
        sumfile = directory / "go.sum"
        if sumfile.exists() or sumfile.is_symlink() or sumfile.relative_to(repo).as_posix() in sources:
            selected.add(sumfile)
    return directory


def verify_module_graphs(receipts, module, roots, modules_by_import):
    for cmd, by_identifier in receipts.items():
        dependencies = {}
        for path in roots[module + "/cmd/" + cmd]:
            require(path in modules_by_import, f"Go 依赖包未完整列出: {path}")
            item = modules_by_import[path]
            if item is not None and item[0] != module:
                key, identity = item
                require(key not in dependencies or dependencies[key] == identity, f"同一模块身份不一致: {key}")
                dependencies[key] = identity
        for identifier, embedded in by_identifier.items():
            require(embedded == dependencies, f"Go 二进制依赖或替换与固定构建命令不符: {identifier}")


def enumerate_inputs(go, repo, sources, receipts, module, info, env, flags):
    go_dir = owned_path(repo, repo / BUSINESS_GO_PATH)
    selected = set()
    expected = {module + "/cmd/" + name for name in receipts}
    roots = {}
    modules_by_import = {}
    # 同一次查询覆盖多个命令，但每个二进制只与自己实际导入的包和模块比较。
    args = [go, "list", "-mod=readonly", "-buildvcs=false", *flags, "-deps",
            "-json=" + ",".join(PACKAGE_FIELDS), *("./cmd/" + name for name in sorted(receipts))]
    profile = info["settings"].get("-pgo")
    require(profile in (None, "default.pgo"), "无法按固定构建命令定位自定义 PGO 输入")
    for pkg in objects(command(args, go_dir, env)):
        require(not pkg.get("Incomplete") and not pkg.get("Error"), "Go 依赖清单包含不完整的包")
        name = pkg["ImportPath"]
        if pkg.get("Standard"):
            modules_by_import[name] = None
            continue
        mod = pkg.get("Module")
        modules_by_import[name] = listed_module(mod)
        directory = module_directory(repo, mod, selected, sources)
        if directory is None:
            continue
        require(isinstance(pkg.get("Dir"), str), "本地 Go 包目录缺失")
        location = owned_path(directory, pkg["Dir"])
        if not pkg.get("DepOnly"):
            require(pkg.get("Name") == "main" and name in expected, "枚举到了协议之外的 Go 命令")
            roots[name] = frozenset(pkg.get("Deps", ()))
            pgo = location / "default.pgo"
            exists = pgo.exists() or pgo.is_symlink()
            require(exists == bool(profile), f"工作树默认 PGO 输入与二进制不符: {name}")
            if exists:
                selected.add(pgo)
        for field in PACKAGE_INPUTS:
            for filename in pkg.get(field, ()):
                relative = Path(filename)
                require(not relative.is_absolute() and ".." not in relative.parts, "Go 输入不是包内相对路径")
                selected.add(location / relative)
    require(roots.keys() == expected, "未能完整枚举协议中的 Go 命令")
    verify_module_graphs(receipts, module, roots, modules_by_import)
    names = set()
    for path in sorted(selected):
        key = path.relative_to(repo).as_posix()
        require(key in sources, f"正式 Go 构建输入未作为普通文件归入固定提交: {key}")
        owned_path(repo, path)
        require(path.is_file() and digest(path) == sources[key], f"正式 Go 构建输入与固定提交字节不符: {key}")
        names.add(key)
    return names, roots


@contextmanager
def fixed_checkout(repository, commit):
    with tempfile.TemporaryDirectory(prefix="benchmark-go-inputs-") as temporary:
        temp = Path(temporary).resolve()
        target = temp / "source"
        hooks = temp / "hooks"
        hooks.mkdir()
        env = {key: value for key, value in os.environ.items() if not key.startswith("GIT_")}
        env.update(GIT_NO_LAZY_FETCH="1", GIT_TERMINAL_PROMPT="0", GIT_CONFIG_NOSYSTEM="1",
                   GIT_CONFIG_GLOBAL=os.devnull, GIT_ATTR_NOSYSTEM="1")
        git = ["git", "-c", f"core.hooksPath={hooks}", "-c", "submodule.recurse=false",
               "-c", "core.autocrlf=false", "-c", "protocol.allow=never"]
        objects_dir = command([*git, "-c", f"safe.directory={repository}", "rev-parse", "--path-format=absolute",
                               "--git-path", "objects"], repository, env).removesuffix("\n")
        command([*git, "init", "--quiet", "--template=", str(target)], temp, env)
        # alternates 中只写固定相对路径；真实仓库路径中的换行符不会拆成多个目录。
        admin = target / ".git"
        (admin / "borrowed-objects").symlink_to(Path(objects_dir).resolve(strict=True), target_is_directory=True)
        (admin / "objects/info/alternates").write_bytes(b"../borrowed-objects\n")
        command([*git, "checkout", "--quiet", "--detach", commit], target, env)
        require(command([*git, "rev-parse", "HEAD"], target, env).strip() == commit, "临时检出的构建提交不符")
        yield target


def verify_inputs(repository, protocol, records, current_commit, current_sources, go_tool):
    try:
        repository = Path(repository).resolve(strict=True)
        executable = shutil.which(str(go_tool))
        require(executable is not None, f"找不到用于枚举构建依赖的 Go 工具: {go_tool}")
        go = str(Path(executable).resolve(strict=True))
        groups = {}
        for identifier, variant in definitions(protocol).items():
            record = records[identifier]
            actual = record.get("build_commit", record["source_commit"])
            # 工具链和非 VCS 构建设置已统一验证；依赖收据仍须区分每个实际二进制。
            info, receipts = groups.setdefault(actual, (parse_build_info(record["build_info"]), {}))
            cmd = variant.get("command", identifier)
            receipts.setdefault(cmd, {})[identifier] = binary_modules(record["build_info"])
        module = protocol["build_provenance"]["module"]
        for commit, (info, receipts) in groups.items():
            sources = current_sources if commit == current_commit else committed_sources(repository, commit)
            sources = regular_sources(repository, commit, sources)

            def inspect(root):
                go_dir = owned_path(root, root / BUSINESS_GO_PATH)
                env, flags = replay(go, go_dir, info)
                return enumerate_inputs(go, root, sources, receipts, module, info, env, flags)

            live = inspect(repository) if commit == current_commit else None
            with fixed_checkout(repository, commit) as fixed:
                archived = inspect(fixed)
            if live is not None:
                # 固定树必须能重现实际命令的输入和传递包图，即使 Go 没有读取完整模块图。
                require(live == archived, "工作树与固定提交的 Go 构建输入或包选择不同")
                require(inspect(repository) == live, "采集期间 Go 构建输入或包选择已变化")
    except (OSError, RuntimeError, subprocess.CalledProcessError) as exc:
        raise ValueError(f"无法核对正式 Go 构建输入: {exc}") from exc
