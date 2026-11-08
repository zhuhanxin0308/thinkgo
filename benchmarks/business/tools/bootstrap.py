"""从固定官方归档在用户目录构建本次基准依赖，不启动服务或性能负载。"""

from __future__ import annotations

import argparse
import fcntl
import hashlib
import json
import os
import pathlib
import platform
import re
import shutil
import ssl
import subprocess
import sys
import tarfile
import tempfile
import urllib.parse

ROOT = pathlib.Path(__file__).resolve().parents[1]
DEFAULT_MANIFEST = ROOT / "contracts/runtime-sources.json"
PYTHON_REQUIREMENTS = ROOT / "tools/requirements.txt"
OWNER = "thinkgo-business-runtime-v1"
JOBS = 2


def digest(path):
    with pathlib.Path(path).open("rb") as source:
        return hashlib.file_digest(source, "sha256").hexdigest()


def fingerprint(value):
    return hashlib.sha256(json.dumps(value, sort_keys=True, separators=(",", ":")).encode()).hexdigest()


def python_requirements():
    result = {}
    for line in PYTHON_REQUIREMENTS.read_text().splitlines():
        if not line.strip() or line.startswith("#"):
            continue
        match = re.fullmatch(r"([a-z0-9-]+)==([0-9.]+) --hash=sha256:[a-f0-9]{64}", line)
        if not match or match[1] in result:
            raise ValueError("Python 锁文件必须包含唯一精确版本和归档摘要")
        result[match[1]] = match[2]
    return result


def write_json(path, value):
    path = pathlib.Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    # 状态先完整落盘再原子发布，已有证据一律不覆盖。
    temporary = None
    try:
        with tempfile.NamedTemporaryFile(mode="w", dir=path.parent, delete=False) as output:
            temporary = pathlib.Path(output.name)
            json.dump(value, output, ensure_ascii=False, sort_keys=True, indent=2)
            output.flush()
            os.fsync(output.fileno())
        os.link(temporary, path)
    finally:
        if temporary is not None:
            temporary.unlink(missing_ok=True)


def load_manifest(path):
    manifest = json.loads(pathlib.Path(path).read_text())
    if manifest.get("schema_version") != 1 or manifest.get("jobs") != JOBS:
        raise ValueError("运行清单版本或固定双任务构建参数不匹配")
    names = set()
    for component in manifest["components"]:
        name = component["name"]
        parsed = urllib.parse.urlsplit(component["url"])
        if (not re.fullmatch(r"[a-z0-9-]+", name) or name in names or
                not re.fullmatch(r"[a-f0-9]{64}", component["sha256"]) or
                parsed.scheme != "https" or not parsed.hostname or parsed.username or parsed.fragment or
                pathlib.PurePosixPath(component["prefix"]).name != component["prefix"]):
            raise ValueError("运行清单包含无效名称、来源或校验值")
        names.add(name)
    select(manifest, None, True)
    return manifest


def select(manifest, requested, pcov):
    components = {item["name"]: item for item in manifest["components"]}
    wanted = list(requested) if requested is not None else [name for name, item in components.items() if not item.get("optional")]
    if pcov and "pcov" not in wanted:
        wanted.append("pcov")
    ordered, active, completed = [], set(), set()
    def visit(name):
        if name not in components or name in active:
            raise ValueError(f"未知组件或循环依赖: {name}")
        if name in completed:
            return
        active.add(name)
        for dependency in components[name]["dependencies"]:
            visit(dependency)
        active.remove(name)
        completed.add(name)
        ordered.append(components[name])
    for name in wanted:
        visit(name)
    return ordered


def preflight(manifest, components):
    if platform.system() != manifest["platform"]["system"] or platform.machine() != manifest["platform"]["machine"]:
        raise ValueError("固定二进制归档仅支持 Linux x86_64")
    compiled = any(item["kind"] not in {"archive", "composer"} for item in components)
    required = ["curl"] + (["cc", "c++", "make", "perl", "pkg-config"] if compiled else [])
    if any(item["name"] == "composer" for item in components):
        required.extend(["git", "unzip"])
    missing = [name for name in required if shutil.which(name) is None]
    if missing:
        raise ValueError("缺少主机工具，需由环境提供后重试: " + ", ".join(missing))
    if not compiled:
        return
    # 固定构建依赖系统开发库；失败时解释缺口，不安装系统软件或提权。
    source = """#define PCRE2_CODE_UNIT_WIDTH 8
#include <openssl/ssl.h>
#include <zlib.h>
#include <pcre2.h>
#include <curl/curl.h>
#include <gmp.h>
#include <zstd.h>
int main(void) { SSL_CTX *c=SSL_CTX_new(TLS_method()); SSL_CTX_free(c);
return (int)(zlibVersion()==0 || pcre2_config(PCRE2_CONFIG_UNICODE, 0)<0 ||
curl_version()==0 || ZSTD_versionNumber()==0 || __gmp_version==0); }
"""
    with tempfile.TemporaryDirectory(prefix="benchmark-headers-") as directory:
        result = subprocess.run(["cc", "-x", "c", "-", "-o", str(pathlib.Path(directory) / "check"),
                                 "-lssl", "-lcrypto", "-lz", "-lpcre2-8", "-lcurl", "-lgmp", "-lzstd"],
                                input=source, text=True, capture_output=True)
    if result.returncode:
        raise ValueError("缺少 OpenSSL/zlib/PCRE2/curl/GMP/zstd 开发头文件或链接库:\n" + result.stderr)


class Installer:
    def __init__(self, runtime, manifest, cache=None):
        self.runtime = pathlib.Path(runtime).resolve()
        self.manifest = manifest
        self.cache = pathlib.Path(cache).resolve() if cache else None
        self.lock = None

    def prepare(self):
        self.runtime.mkdir(parents=True, exist_ok=True)
        marker = self.runtime / ".bootstrap-owner.json"
        owner = {"owner": OWNER, "manifest_sha256": fingerprint(self.manifest)}
        if not marker.exists():
            if any(self.runtime.iterdir()):
                raise ValueError("拒绝覆盖未由本安装入口标记的非空运行目录")
            write_json(marker, owner)
        elif json.loads(marker.read_text()) != owner:
            raise ValueError("已有运行目录的所有权或固定运行清单不匹配")
        self.lock = (self.runtime / ".bootstrap.lock").open("a")
        try:
            fcntl.flock(self.lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError as error:
            self.close()
            raise ValueError("已有安装进程正在操作此运行目录") from error
        for directory in ["downloads", "sources", "builds", "receipts", "claims", "logs"]:
            (self.runtime / directory).mkdir(exist_ok=True)

    def close(self):
        if self.lock is not None:
            self.lock.close()
            self.lock = None

    def values(self, component):
        return {"runtime": str(self.runtime), "prefix": str(self.runtime / component["prefix"])}

    def expand(self, arguments, component):
        return [argument.format_map(self.values(component)) for argument in arguments]

    def environment(self, component):
        environment = dict(os.environ)
        for key in ["MAKEFLAGS", "MFLAGS", "CFLAGS", "CXXFLAGS", "CPPFLAGS", "LDFLAGS", "PKG_CONFIG_PATH", "PHP_INI_SCAN_DIR"]:
            environment.pop(key, None)
        environment["PATH"] = os.pathsep.join([str(self.runtime / "build-tools/bin"), str(self.runtime / "php/bin"),
                                                str(self.runtime / "go-1.27.3/bin"), environment.get("PATH", "")])
        environment["PHP_INI_SCAN_DIR"] = str(self.runtime / "php/etc/conf.d")
        environment["LC_ALL"] = "C.UTF-8"
        for key, value in component.get("environment", {}).items():
            environment[key] = value.format_map(self.values(component))
        return environment

    def command(self, arguments, *, cwd=None, environment=None, label="bootstrap"):
        arguments = list(map(str, arguments))
        log = self.runtime / "logs" / (label + ".log")
        with log.open("a") as stream:
            stream.write(json.dumps({"command": arguments, "cwd": str(cwd or self.runtime)}, ensure_ascii=False) + "\n")
            stream.flush()
            result = subprocess.run(arguments, cwd=cwd or self.runtime, env=environment,
                                    stdout=stream, stderr=subprocess.STDOUT, timeout=7200)
        if result.returncode:
            raise RuntimeError(f"构建命令失败({result.returncode})，完整日志: {log}")
        return ""

    def download(self, component):
        filename = component.get("filename", pathlib.PurePosixPath(urllib.parse.urlsplit(component["url"]).path).name)
        if pathlib.Path(filename).name != filename:
            raise ValueError("归档文件名无效")
        target = self.runtime / "downloads" / filename
        if target.exists():
            if digest(target) != component["sha256"]:
                raise ValueError(f"已有下载摘要不匹配，拒绝覆盖: {target}")
            return target
        temporary = target.with_suffix(target.suffix + ".part")
        try:
            cached = self.cache / filename if self.cache else None
            if cached is not None and cached.exists():
                if digest(cached) != component["sha256"]:
                    raise ValueError(f"下载缓存摘要不匹配: {cached}")
                shutil.copyfile(cached, temporary)
            else:
                self.command(["curl", "--fail", "--location", "--retry", "3", "--proto", "=https",
                              "--proto-redir", "=https", "--output", temporary, component["url"]], label="download-" + component["name"])
            if digest(temporary) != component["sha256"]:
                raise ValueError(f"官方归档下载摘要不匹配: {component['name']}")
            os.link(temporary, target)
            return target
        finally:
            temporary.unlink(missing_ok=True)

    def unpack(self, component, archive):
        destination = self.runtime / "sources" / (component["name"] + "-" + component["version"])
        marker = destination / ".archive.json"
        expected = {"sha256": component["sha256"]}
        if destination.exists():
            if not marker.exists() or json.loads(marker.read_text()) != expected:
                raise ValueError(f"已有源码目录缺少匹配来源标记: {destination}")
            return destination / component["archive_root"]
        with tempfile.TemporaryDirectory(prefix=".extract-", dir=destination.parent) as directory:
            stage = pathlib.Path(directory) / "source"
            stage.mkdir()
            with tarfile.open(archive) as stream:
                stream.extractall(stage, filter="data")
            if not (stage / component["archive_root"]).is_dir():
                raise ValueError("官方归档目录结构与固定清单不一致")
            write_json(stage / ".archive.json", expected)
            stage.rename(destination)
        return destination / component["archive_root"]

    def claim_prefix(self, component):
        prefix = self.runtime / component["prefix"]
        marker = self.runtime / "claims" / (component["prefix"] + ".json")
        if not marker.exists():
            if prefix.exists():
                raise ValueError(f"拒绝覆盖未知安装目录: {prefix}")
            write_json(marker, {"prefix": component["prefix"]})
        prefix.mkdir(exist_ok=True)

    def configure_php(self, component):
        prefix = self.runtime / component["prefix"]
        (prefix / "etc/conf.d").mkdir(parents=True, exist_ok=True)
        (prefix / "var/log").mkdir(parents=True, exist_ok=True)
        authority = os.environ.get("CURL_CA_BUNDLE") or os.environ.get("SSL_CERT_FILE") or ssl.get_default_verify_paths().cafile
        if not authority or not pathlib.Path(authority).is_file():
            raise ValueError("缺少有效的系统 TLS 信任根文件")
        configuration = f"""; 所有 PHP 模式共用生产配置，测试覆盖扩展只允许单独命令行启用。
date.timezone = UTC
expose_php = Off
display_errors = Off
display_startup_errors = Off
log_errors = On
error_log = {prefix}/var/log/php-errors.log
error_reporting = E_ALL
memory_limit = 512M
max_execution_time = 30
default_socket_timeout = 30
variables_order = "GPCS"
post_max_size = 8M
upload_max_filesize = 8M
session.use_strict_mode = 1
session.cookie_httponly = 1
session.cookie_samesite = Lax
zend.assertions = -1
realpath_cache_size = 4096K
realpath_cache_ttl = 600
openssl.cafile = {authority}
curl.cainfo = {authority}
opcache.enable = 1
opcache.enable_cli = 1
opcache.memory_consumption = 256
opcache.interned_strings_buffer = 32
opcache.max_accelerated_files = 20000
opcache.validate_timestamps = 1
opcache.revalidate_freq = 0
opcache.jit = disable
opcache.jit_buffer_size = 0
"""
        self.write_configuration(prefix / "etc/php.ini", configuration)

    @staticmethod
    def write_configuration(path, content):
        path.parent.mkdir(parents=True, exist_ok=True)
        if path.exists():
            if path.read_text() != content:
                raise ValueError(f"拒绝覆盖内容不匹配的已有配置: {path}")
        else:
            with path.open("x") as stream:
                stream.write(content)

    def build(self, component, source):
        prefix = self.runtime / component["prefix"]
        environment = self.environment(component)
        kind, name = component["kind"], component["name"]
        if kind == "archive":
            shutil.copytree(source, prefix, dirs_exist_ok=True, symlinks=True)
            return
        if kind == "composer":
            target = prefix / "bin/composer"
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(source, target)
            target.chmod(0o755)
            return
        build = self.runtime / "builds" / name
        marker = build / ".recipe.json"
        expected = {"recipe_sha256": fingerprint(component)}
        if build.exists():
            if not marker.exists() or json.loads(marker.read_text()) != expected:
                raise ValueError(f"已有构建目录来源不匹配: {build}")
        else:
            build.mkdir()
            write_json(marker, expected)
        work = build / "work"
        if not work.exists():
            # 源码归档保持独立，构建系统只在单独工作副本中生成文件。
            if kind == "postgresql":
                work.mkdir()
            else:
                shutil.copytree(source, work, symlinks=True)
        def run(arguments, directory=work):
            self.command(arguments, cwd=directory, environment=environment, label=name)
        if kind == "extension":
            run([prefix / "bin/phpize"])
        if kind == "redis":
            run(self.expand(component["build"], component))
            run(self.expand(component["install"], component))
        else:
            configure = str(source / "configure") if kind == "postgresql" else "./configure"
            run([configure, *self.expand(component["configure"], component)])
            run(["make", f"-j{JOBS}"])
            run(["make", "install"])
            if kind == "postgresql":
                run(["make", f"-j{JOBS}", "-C", "contrib/pg_stat_statements"])
                run(["make", "-C", "contrib/pg_stat_statements", "install"])
        if kind == "php":
            self.configure_php(component)
        if kind == "extension" and component["extension"] == "redis":
            self.write_configuration(prefix / "etc/conf.d/20-redis.ini", "; 两个 PHP 生产模式共用 phpredis。\nextension = redis\n")

    def installed_files(self, component):
        result = {}
        for pattern in component["outputs"]:
            matched = [path for path in self.runtime.glob(pattern) if path.is_file()]
            if not matched:
                raise ValueError(f"构建产物缺失: {pattern}")
            for path in matched:
                if not path.resolve().is_relative_to(self.runtime):
                    raise ValueError("构建产物不能链接到安装目录之外")
                result[str(path.relative_to(self.runtime))] = digest(path)
        return result

    def install(self, component):
        receipt = self.runtime / "receipts" / (component["name"] + ".json")
        expected = fingerprint(component)
        if receipt.exists():
            saved = json.loads(receipt.read_text())
            if saved.get("recipe_sha256") != expected or saved.get("files_sha256") != self.installed_files(component):
                raise ValueError(f"已安装组件被修改或不符合固定配方: {component['name']}")
            return "reused"
        self.claim_prefix(component)
        archive = self.download(component)
        source = archive if component["kind"] == "composer" else self.unpack(component, archive)
        self.build(component, source)
        result = subprocess.run(self.expand(component["probe"], component), env=self.environment(component),
                                text=True, capture_output=True, timeout=30)
        output = (result.stdout + result.stderr).strip()
        if result.returncode or not re.search(r"(?<![0-9.])" + re.escape(component["version"]) + r"(?![0-9.])", output):
            raise RuntimeError(f"安装后版本自检失败: {component['name']}: {output}")
        write_json(receipt, {"name": component["name"], "version": component["version"], "recipe_sha256": expected,
                             "archive_sha256": component["sha256"], "files_sha256": self.installed_files(component), "version_output": output})
        return "installed"

    def composer_install(self, tests):
        php = self.runtime / "php/bin/php"
        composer = self.runtime / "php/bin/composer"
        environment = self.environment({"prefix": "php"})
        for name in ["thinkphp", "laravel", *([""] if tests else [])]:
            directory = ROOT / "php" / name
            lock = directory / "composer.lock"
            before = digest(lock)
            arguments = [php, composer, "install", "--prefer-dist", "--no-interaction", "--no-progress", "--no-scripts", "--optimize-autoloader"]
            if name:
                arguments.append("--no-dev")
            self.command(arguments, cwd=directory, environment=environment, label="composer-" + (name or "tests"))
            self.command([php, composer, "check-platform-reqs", *(["--no-dev"] if name else [])],
                         cwd=directory, environment=environment, label="composer-" + (name or "tests"))
            if digest(lock) != before:
                raise RuntimeError(f"Composer 锁文件发生非预期变化: {lock}")
        self.command([php, "-r", "if (extension_loaded('pcov') || extension_loaded('swoole') || ini_get('opcache.jit_buffer_size') != '0') { exit(1); }"],
                     environment=environment, label="production-php")

    def python_environment(self):
        environment = dict(os.environ)
        environment["PYTHONPATH"] = os.pathsep.join(filter(None, [str(self.runtime / "python-packages"), environment.get("PYTHONPATH", "")]))
        return environment

    def python_versions(self):
        # 子进程读取当前解析路径，避免当前进程已经导入的旧版本污染核验。
        source = """import importlib.metadata,json,sys
versions={}
for name in json.loads(sys.argv[1]):
 try: versions[name]=importlib.metadata.version(name)
 except importlib.metadata.PackageNotFoundError: continue
print(json.dumps(versions))
"""
        result = subprocess.run([sys.executable, "-c", source, json.dumps(list(python_requirements()))],
                                env=self.python_environment(), text=True, capture_output=True, check=True)
        return json.loads(result.stdout)

    def python_install(self):
        if sys.version_info[:2] != (3, 12):
            raise ValueError("固定 Python wheel 摘要要求 Python 3.12；本次测量使用 3.12.14")
        expected = python_requirements()
        if self.python_versions() == expected:
            return {"state": "reused", "pythonpath": self.python_environment()["PYTHONPATH"], "versions": expected}
        prefix = self.runtime / "python-packages"
        self.claim_prefix({"prefix": "python-packages"})
        if any(prefix.iterdir()):
            raise ValueError("已有 Python 用户目录版本不匹配，保留原目录并拒绝覆盖")
        # wheel 先在隔离目录按固定摘要安装，成功后再发布到用户前缀。
        with tempfile.TemporaryDirectory(prefix=".python-stage-", dir=self.runtime) as directory:
            stage = pathlib.Path(directory) / "packages"
            stage.mkdir()
            self.command([sys.executable, "-m", "pip", "install", "--require-hashes", "--only-binary=:all:",
                          "--no-deps", "--target", stage, "-r", PYTHON_REQUIREMENTS], label="python-packages")
            prefix.rmdir()
            stage.rename(prefix)
        if self.python_versions() != expected:
            raise RuntimeError("Python 用户目录安装后的精确版本自检失败")
        write_json(self.runtime / "receipts/python-packages.json", {"requirements_sha256": digest(PYTHON_REQUIREMENTS), "versions": expected})
        return {"state": "installed", "pythonpath": self.python_environment()["PYTHONPATH"], "versions": expected}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--runtime", required=True, help="新建或本入口已拥有的用户运行目录")
    parser.add_argument("--download-cache", help="只读复用经过固定 SHA256 校验的归档目录")
    parser.add_argument("--components", help="仅安装逗号分隔组件及其完整依赖；默认安装全部生产组件")
    parser.add_argument("--with-pcov", action="store_true", help="额外构建 PCOV，但不在生产配置加载")
    parser.add_argument("--with-tests", action="store_true", help="额外安装 PHP 单测 Composer 锁依赖")
    parser.add_argument("--python-only", action="store_true", help="只检查或安装固定 Python 用户目录依赖")
    parser.add_argument("--plan", action="store_true", help="只显示固定安装顺序和参数，不写入目录")
    args = parser.parse_args()
    manifest = load_manifest(DEFAULT_MANIFEST)
    components = [] if args.python_only else select(manifest, args.components.split(",") if args.components else None, args.with_pcov)
    if args.plan:
        print(json.dumps(components, ensure_ascii=False, indent=2))
        return
    preflight(manifest, components)
    installer = Installer(args.runtime, manifest, args.download_cache)
    try:
        installer.prepare()
        for component in components:
            print(json.dumps({"component": component["name"], "state": "installing"}), flush=True)
            status = installer.install(component)
            print(json.dumps({"component": component["name"], "state": status, "version": component["version"]}), flush=True)
        if any(component["name"] == "composer" for component in components):
            installer.composer_install(args.with_tests)
        if args.python_only or args.components is None:
            print(json.dumps(installer.python_install(), ensure_ascii=False), flush=True)
    finally:
        installer.close()


if __name__ == "__main__":
    main()
