"""在同一进程树中启动专用 PostgreSQL、Redis、应用和 Nginx。"""

from __future__ import annotations

import hashlib
import json
import os
import pathlib
import re
import signal
import socket
import subprocess
import time
import urllib.error
import urllib.request


OWNER = "thinkgo-business-benchmark-v1"
DATABASE = "benchmark_run"
DATABASE_USER = "benchmark_owner"
STARTUP_SECONDS = 45
READINESS_PROBE_SECONDS = 1
READINESS_POLL_SECONDS = 0.1
PSQL_CONNECTION_FAILURE = 2
STOP_SECONDS = 15
PHP_CONTRACT_SCHEMA_VERSION = 1
PHP_CONTRACT_SOURCE_FILES = ("dataset.json", "queries.json")
PHP_CONTRACT_DIGEST_PREFIX = b"thinkgo-contract-v1\0"


def read_php_contract_snapshot(metadata, contracts, *, snapshot=None):
    """核验固定元数据、来源与文件字节；恢复时允许读取已归档的副本。"""
    fields = {"schema_version", "path", "file_sha256", "content_sha256", "source_sha256"}
    if not isinstance(metadata, dict) or set(metadata) != fields:
        raise ValueError("PHP 合同快照元数据字段不完整或存在未知字段")
    if type(metadata["schema_version"]) is not int or metadata["schema_version"] != PHP_CONTRACT_SCHEMA_VERSION:
        raise ValueError("PHP 合同快照版本无效")
    sources = metadata["source_sha256"]
    if not isinstance(sources, dict) or set(sources) != set(PHP_CONTRACT_SOURCE_FILES):
        raise ValueError("PHP 合同快照来源清单不完整或存在未知字段")
    for digest in [metadata["file_sha256"], metadata["content_sha256"], *sources.values()]:
        if not isinstance(digest, str) or re.fullmatch(r"[0-9a-f]{64}", digest) is None:
            raise ValueError("PHP 合同快照摘要格式无效")
    if not isinstance(metadata["path"], str):
        raise ValueError("PHP 合同快照路径无效")
    original = pathlib.Path(metadata["path"])
    if not original.is_absolute() or original.name != f"contract-{metadata['content_sha256']}.php":
        raise ValueError("PHP 合同快照路径必须使用绝对路径和内容摘要文件名")
    identity = PHP_CONTRACT_DIGEST_PREFIX + "".join(sources[name] for name in PHP_CONTRACT_SOURCE_FILES).encode("ascii")
    if hashlib.sha256(identity).hexdigest() != metadata["content_sha256"]:
        raise ValueError("PHP 合同快照内容摘要与来源不一致")
    path = original if snapshot is None else pathlib.Path(snapshot)
    try:
        for name in PHP_CONTRACT_SOURCE_FILES:
            if hashlib.sha256((pathlib.Path(contracts) / name).read_bytes()).hexdigest() != sources[name]:
                raise ValueError(f"PHP 合同快照来源摘要不一致: {name}")
        data = path.read_bytes()
    except OSError as error:
        raise ValueError("PHP 合同快照或来源文件不可读取") from error
    if hashlib.sha256(data).hexdigest() != metadata["file_sha256"]:
        raise ValueError("PHP 合同快照文件摘要不一致")
    return data


def postgres_config(protocol):
    settings = protocol["postgres"]
    return f"""listen_addresses = '127.0.0.1'
port = {protocol['ports']['postgres']}
unix_socket_directories = ''
max_connections = {settings['max_connections']}
shared_buffers = '{settings['shared_buffers']}'
work_mem = '{settings['work_mem']}'
fsync = on
synchronous_commit = on
full_page_writes = on
checkpoint_timeout = '{settings['checkpoint_timeout']}'
max_wal_size = '{settings['max_wal_size']}'
shared_preload_libraries = 'pg_stat_statements'
pg_stat_statements.track = all
pg_stat_statements.track_utility = off
track_io_timing = on
log_statement = 'none'
log_min_messages = warning
"""


def redis_config(protocol):
    return f"""bind 127.0.0.1
port {protocol['ports']['redis']}
protected-mode yes
daemonize no
save ""
appendonly no
maxmemory {protocol['redis_maxmemory']}
maxmemory-policy noeviction
loglevel warning
"""


def _path(value):
    text = str(value)
    if any(character in text for character in "\n\r\"'"):
        raise ValueError("运行目录不支持引号或换行")
    return text


def gateway_config(protocol, dataset, work, root, variant):
    ports = protocol["ports"]
    timeout = protocol["request_timeout_seconds"]
    if variant["kind"] == "fpm":
        document = _path(root / "php" / variant["app"] / "public")
        upstream = ""
        handler = f"""fastcgi_pass 127.0.0.1:{ports['fpm']};
            fastcgi_keep_conn on;
            fastcgi_read_timeout {timeout}s;
            fastcgi_send_timeout {timeout}s;
            fastcgi_param SCRIPT_FILENAME {document}/index.php;
            fastcgi_param SCRIPT_NAME /index.php;
            fastcgi_param DOCUMENT_ROOT {document};
            fastcgi_param REQUEST_METHOD $request_method;
            fastcgi_param CONTENT_TYPE $content_type;
            fastcgi_param CONTENT_LENGTH $content_length;
            fastcgi_param QUERY_STRING $query_string;
            fastcgi_param REQUEST_URI $request_uri;
            fastcgi_param DOCUMENT_URI $document_uri;
            fastcgi_param PATH_INFO $uri;
            fastcgi_param SERVER_PROTOCOL $server_protocol;
            fastcgi_param REQUEST_SCHEME $scheme;
            fastcgi_param GATEWAY_INTERFACE CGI/1.1;
            fastcgi_param SERVER_SOFTWARE nginx;
            fastcgi_param REMOTE_ADDR $remote_addr;
            fastcgi_param REMOTE_PORT $remote_port;
            fastcgi_param SERVER_ADDR $server_addr;
            fastcgi_param SERVER_PORT $server_port;
            fastcgi_param SERVER_NAME $host;
            fastcgi_param HTTP_AUTHORIZATION $http_authorization;
            fastcgi_param HTTP_IDEMPOTENCY_KEY $http_idempotency_key;
            fastcgi_param REDIRECT_STATUS 200;"""
    else:
        upstream = f"""upstream benchmark_app {{
        server 127.0.0.1:{ports['application']};
        keepalive {protocol['gateway_upstream_keepalive']};
    }}"""
        handler = f"""proxy_pass http://benchmark_app;
            proxy_http_version 1.1;
            proxy_set_header Connection "";
            proxy_set_header Host $host;
            proxy_read_timeout {timeout}s;
            proxy_send_timeout {timeout}s;"""
    return f"""worker_processes {protocol['gateway_workers']};
daemon off;
pid {_path(work / 'nginx.pid')};
error_log {_path(work / 'logs/nginx-error.log')} warn;
events {{ worker_connections {protocol['gateway_connections']}; }}
http {{
    access_log off;
    default_type application/json;
    sendfile on;
    tcp_nodelay on;
    gzip off;
    keepalive_timeout 60;
    keepalive_requests 1000000;
    client_max_body_size {dataset['maximum_body_bytes']};
    client_body_temp_path {_path(work / 'temp/client')};
    proxy_temp_path {_path(work / 'temp/proxy')};
    fastcgi_temp_path {_path(work / 'temp/fastcgi')};
    {upstream}
    server {{
        listen 127.0.0.1:{ports['gateway']};
        server_name localhost;
        location / {{ {handler} }}
    }}
}}
"""


def fpm_config(protocol, dataset, work):
    return f"""[global]
pid = {_path(work / 'php-fpm.pid')}
error_log = {_path(work / 'logs/php-fpm-error.log')}
daemonize = no
[benchmark]
listen = 127.0.0.1:{protocol['ports']['fpm']}
listen.allowed_clients = 127.0.0.1
pm = static
pm.max_children = {dataset['database_connection_budget']}
pm.max_requests = 0
clear_env = no
catch_workers_output = yes
decorate_workers_output = no
request_terminate_timeout = {protocol['request_timeout_seconds']}s
php_admin_flag[display_errors] = off
php_admin_flag[log_errors] = on
php_admin_flag[opcache.enable] = on
php_admin_flag[opcache.validate_timestamps] = off
php_admin_value[opcache.jit] = 0
"""


def octane_command(runtime, protocol, dataset):
    return [str(runtime / "php/bin/php"), "artisan", "octane:start", "--server=roadrunner",
            "--host=127.0.0.1", f"--port={protocol['ports']['application']}",
            f"--rpc-port={protocol['ports']['rpc']}", f"--workers={dataset['database_connection_budget']}",
            "--max-requests=0", "--rr-config=.rr.yaml", "--no-interaction"]


class ServiceNotReady(Exception):
    """仅表示有界启动等待中允许重试的暂时不可用状态。"""


class Process:
    """只终止自身创建的会话，日志始终保留供失败分析。"""

    def __init__(self, command, cwd, environment, log, cpus, stop_signal=signal.SIGTERM):
        self.command = list(map(str, command))
        self.log_path = pathlib.Path(log)
        self.log = self.log_path.open("ab")
        self.stop_signal = stop_signal
        launched = ["taskset", "--cpu-list", ",".join(map(str, cpus)), *self.command]
        try:
            self.child = subprocess.Popen(launched, cwd=cwd, env=environment, stdout=self.log,
                                          stderr=subprocess.STDOUT, start_new_session=True)
        except BaseException:
            self.log.close()
            raise

    @property
    def pid(self):
        return self.child.pid

    def check(self):
        status = self.child.poll()
        if status is not None:
            tail = self.log_path.read_text(errors="replace")[-6000:]
            raise RuntimeError(f"进程提前退出({status}): {self.command}\n{tail}")

    def stop(self):
        if self.child.poll() is None:
            os.killpg(self.child.pid, self.stop_signal)
            try:
                self.child.wait(timeout=STOP_SECONDS)
            except subprocess.TimeoutExpired:
                os.killpg(self.child.pid, signal.SIGKILL)
                self.child.wait(timeout=STOP_SECONDS)
        self.log.close()


def wait_for(check, process):
    deadline = time.monotonic() + STARTUP_SECONDS
    last = "就绪条件尚未满足"
    while True:
        process.check()
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            break
        try:
            ready = check(timeout=min(READINESS_PROBE_SECONDS, remaining))
        except ServiceNotReady as error:
            last = error
            ready = False
        except BaseException as error:
            # 致命探针错误和用户中断保持原样，进程退出只作为附加诊断。
            try:
                process.check()
            except Exception as process_error:
                error.add_note(str(process_error))
            raise
        # 成功响应或暂时连接失败都不能掩盖受管进程已经退出的事实。
        process.check()
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            break
        if ready:
            return
        time.sleep(min(READINESS_POLL_SECONDS, remaining))
    raise TimeoutError(f"服务启动超时: {process.command}; {last}")


def tcp_ready(port, *, timeout=READINESS_PROBE_SECONDS):
    try:
        with socket.create_connection(("127.0.0.1", port), timeout=timeout):
            return True
    except (ConnectionError, TimeoutError) as error:
        raise ServiceNotReady(str(error)) from error


class Environment:
    def __init__(self, root, runtime, work, protocol):
        from experiment import validate_execution_protocol
        validate_execution_protocol(protocol)
        self.root = pathlib.Path(root).resolve()
        self.runtime = pathlib.Path(runtime).resolve()
        self.work = pathlib.Path(work).resolve()
        self.protocol = protocol
        self.dataset = json.loads((self.root / "contracts/dataset.json").read_text())
        self.infrastructure = {}
        self.application = {}
        self.php_contract_snapshot = None
        self.base_env = dict(os.environ)
        self.base_env.update(PGHOST="127.0.0.1", PGPORT=str(protocol["ports"]["postgres"]),
                             PGUSER=DATABASE_USER, PGSSLMODE="disable", LC_ALL="C.UTF-8")

    def binary(self, component, name):
        return self.runtime / component / "bin" / name

    def prepare(self):
        self.work.mkdir(parents=True, exist_ok=True)
        marker = self.work / ".benchmark-owner"
        if not marker.exists() and any(self.work.iterdir()):
            raise ValueError("拒绝复用未标记为本实验所有的目录")
        if marker.exists() and marker.read_text() != OWNER:
            raise ValueError("运行目录所有权标记不匹配")
        marker.write_text(OWNER)
        for folder in ["logs", "config", "temp/client", "temp/proxy", "temp/fastcgi"]:
            (self.work / folder).mkdir(parents=True, exist_ok=True)
        available = set(os.sched_getaffinity(0))
        wanted = {cpu for group in self.protocol["cpu_affinity"].values() for cpu in group}
        if not wanted <= available:
            raise ValueError(f"协议CPU集合不可用: wanted={sorted(wanted)}, available={sorted(available)}")
        data = self.work / "postgres"
        if not (data / "PG_VERSION").exists():
            self.command([self.binary("postgresql", "initdb"), "-D", data, "-U", DATABASE_USER,
                          "--auth=trust", "--locale=C.UTF-8", "--encoding=UTF8"])
        (self.work / "config/postgresql.conf").write_text(postgres_config(self.protocol))
        (self.work / "config/redis.conf").write_text(redis_config(self.protocol))

    def command(self, command, *, database=None, input=None, timeout=120, environment=None, cwd=None):
        environment = dict(self.base_env if environment is None else environment)
        if database:
            environment["PGDATABASE"] = database
        result = subprocess.run(list(map(str, command)), input=input, text=True, capture_output=True,
                                env=environment, cwd=self.root if cwd is None else cwd, timeout=timeout)
        if result.returncode:
            raise RuntimeError(f"命令失败({result.returncode}): {command}\n{result.stdout}\n{result.stderr}")
        return result.stdout.strip()

    def sql(self, query, database=DATABASE):
        return self.command([self.binary("postgresql", "psql"), "-XAtq", "-v", "ON_ERROR_STOP=1", "-c", query],
                            database=database)

    def postgres_ready(self, *, timeout=READINESS_PROBE_SECONDS):
        """维护库可执行 SQL 且恢复结束后，才允许进入模板建库流程。"""
        environment = {**self.base_env, "PGDATABASE": "postgres"}
        query = "SELECT NOT pg_is_in_recovery() AND current_setting('transaction_read_only') = 'off'"
        command = [str(self.binary("postgresql", "psql")), "-XAtq", "-w", "-v", "ON_ERROR_STOP=1", "-c", query]
        try:
            result = subprocess.run(command, stdin=subprocess.DEVNULL, text=True, capture_output=True,
                                    env=environment, cwd=self.root, timeout=timeout)
        except subprocess.TimeoutExpired as error:
            raise ServiceNotReady(f"PostgreSQL SQL 探测超时({timeout}s): {error.stderr}") from error
        if result.returncode == PSQL_CONNECTION_FAILURE:
            raise ServiceNotReady(f"PostgreSQL 连接尚未就绪: {result.stderr.strip()}")
        if result.returncode:
            raise RuntimeError(f"PostgreSQL 就绪探针失败({result.returncode}): {result.stderr.strip()}")
        value = result.stdout.strip()
        if value == "f":
            raise ServiceNotReady("PostgreSQL 尚未退出恢复或当前事务只读")
        if value != "t":
            raise ValueError(f"PostgreSQL 就绪探针输出无效: {value!r}")
        return True

    def start(self):
        self.prepare()
        affinity = self.protocol["cpu_affinity"]
        postgres = Process([self.binary("postgresql", "postgres"), "-D", self.work / "postgres",
                            "-c", f"config_file={self.work / 'config/postgresql.conf'}"],
                           self.work, self.base_env, self.work / "logs/postgres.log", affinity["postgres"], signal.SIGINT)
        self.infrastructure["postgres"] = postgres
        wait_for(self.postgres_ready, postgres)
        redis = Process([self.binary("redis", "redis-server"), self.work / "config/redis.conf"],
                        self.work, self.base_env, self.work / "logs/redis.log", affinity["redis"])
        self.infrastructure["redis"] = redis
        wait_for(lambda timeout: tcp_ready(self.protocol["ports"]["redis"], timeout=timeout), redis)
        contracts = self.root / "contracts"
        digest = hashlib.sha256()
        for name in ["dataset.json", "schema.sql", "seed.sql"]:
            digest.update((contracts / name).read_bytes())
        self.template = "benchmark_seed_" + digest.hexdigest()[:12]
        exists = self.sql(f"SELECT 1 FROM pg_database WHERE datname='{self.template}'", "postgres")
        if exists != "1":
            self.sql(f"CREATE DATABASE {self.template}", "postgres")
            self.sql("CREATE EXTENSION pg_stat_statements", self.template)
            arguments = [self.binary("postgresql", "psql"), "-Xq", "-v", "ON_ERROR_STOP=1"]
            for key, value in self.dataset.items():
                if isinstance(value, int):
                    arguments.extend(["-v", f"{key}={value}"])
            arguments.extend(["-f", contracts / "schema.sql", "-f", contracts / "seed.sql"])
            self.command(arguments, database=self.template)
            self.sql("CHECKPOINT", self.template)
        expected = {
            "tenants": self.dataset["tenant_count"],
            "users": self.dataset["tenant_count"] * self.dataset["users_per_tenant"],
            "products": self.dataset["tenant_count"] * self.dataset["products_per_tenant"],
            "specifications": self.dataset["tenant_count"] * self.dataset["products_per_tenant"] * self.dataset["specifications_per_product"],
            "tickets": self.dataset["tenant_count"] * self.dataset["tickets_per_tenant"],
            "comments": self.dataset["tenant_count"] * self.dataset["tickets_per_tenant"] * self.dataset["initial_comments_per_ticket"],
            "orders": 0,
        }
        columns = ",".join(f"'{name}',(SELECT count(*) FROM bench_{name})" for name in expected)
        self.seed_counts = json.loads(self.sql("SELECT json_build_object(" + columns + ")", self.template))
        if self.seed_counts != expected:
            raise RuntimeError(f"模板数据库不完整，拒绝基于残留数据运行: {self.seed_counts}")

    def reset(self):
        if self.application:
            raise RuntimeError("应用必须关闭后才可重置业务数据")
        self.sql(f"DROP DATABASE IF EXISTS {DATABASE} WITH (FORCE)", "postgres")
        self.sql(f"CREATE DATABASE {DATABASE} TEMPLATE {self.template}", "postgres")
        self.command([self.binary("redis", "redis-cli"), "-h", "127.0.0.1", "-p",
                      self.protocol["ports"]["redis"], "FLUSHDB"])

    def application_env(self, namespace, secret):
        environment = dict(self.base_env)
        ports = self.protocol["ports"]
        environment.update(BENCH_ADDR=f"127.0.0.1:{ports['application']}",
                           BENCH_CONTRACT_DIR=str(self.root / "contracts"),
                           BENCH_DATABASE_URL=f"postgres://{DATABASE_USER}@127.0.0.1:{ports['postgres']}/{DATABASE}?sslmode=disable",
                           BENCH_REDIS_ADDR=f"127.0.0.1:{ports['redis']}", BENCH_NAMESPACE=namespace,
                           BENCH_SECRET=secret, GOMAXPROCS=str(len(self.protocol["cpu_affinity"]["application"])),
                           GIN_MODE="release", APP_ENV="production", APP_DEBUG="false",
                           PHP_INI_SCAN_DIR=str(self.runtime / "php/etc/conf.d"))
        environment["PATH"] = os.pathsep.join([str(self.runtime / "php/bin"), str(self.runtime / "roadrunner"),
                                               environment.get("PATH", "")])
        return environment

    def prepare_php(self, variant, environment):
        directory = self.root / "php" / variant["app"]
        php = self.binary("php", "php")
        snapshots = self.work / "config/contracts"
        metadata = json.loads(self.command([php, self.root / "php/shared/compile-contract.php",
                                           self.root / "contracts", snapshots], environment=environment))
        read_php_contract_snapshot(metadata, self.root / "contracts")
        if pathlib.Path(metadata["path"]).parent != snapshots:
            raise ValueError("PHP 合同快照不在本实验的配置目录内")
        # 编译与核验成功后才注入缓存构建及实际服务的同一份环境。
        environment["BENCH_CONTRACT_SNAPSHOT"] = metadata["path"]
        self.php_contract_snapshot = metadata
        if variant["app"] == "laravel":
            for folder in ["storage/logs", "storage/framework/cache", "storage/framework/sessions", "storage/framework/views"]:
                (directory / folder).mkdir(parents=True, exist_ok=True)
            commands = [[php, "artisan", "config:cache"], [php, "artisan", "route:cache"]]
        else:
            commands = [[php, "think", "optimize:config"], [php, "think", "optimize:route"]]
        for command in commands:
            self.command(command, environment=environment, cwd=directory)

    def start_app(self, variant, namespace, secret):
        self.php_contract_snapshot = None
        environment = self.application_env(namespace, secret)
        environment.pop("BENCH_CONTRACT_SNAPSHOT", None)
        affinity = self.protocol["cpu_affinity"]["application"]
        if variant["kind"] in {"fpm", "octane"}:
            self.prepare_php(variant, environment)
        if variant["kind"] == "go":
            command = [self.root / "bin" / variant["id"]]
            cwd = self.root
        elif variant["kind"] == "fpm":
            configuration = self.work / "config/php-fpm.conf"
            configuration.write_text(fpm_config(self.protocol, self.dataset, self.work))
            command = [self.runtime / "php/sbin/php-fpm", "--nodaemonize", "--fpm-config", configuration]
            cwd = self.root / "php" / variant["app"]
        elif variant["kind"] == "octane":
            command = octane_command(self.runtime, self.protocol, self.dataset)
            cwd = self.root / "php" / variant["app"]
        else:
            raise ValueError(f"尚未验证的运行模式: {variant['kind']}")
        app = Process(command, cwd, environment, self.work / f"logs/{namespace}-app.log", affinity)
        self.application["app"] = app
        port = self.protocol["ports"]["fpm" if variant["kind"] == "fpm" else "application"]
        wait_for(lambda timeout: tcp_ready(port, timeout=timeout), app)
        configuration = self.work / "config/nginx.conf"
        configuration.write_text(gateway_config(self.protocol, self.dataset, self.work, self.root, variant))
        nginx = Process([self.runtime / "nginx/sbin/nginx", "-p", self.work, "-c", configuration],
                        self.work, environment, self.work / f"logs/{namespace}-gateway.log", affinity, signal.SIGQUIT)
        self.application["gateway"] = nginx
        wait_for(self.healthy, nginx)
        return environment

    @property
    def url(self):
        return f"http://127.0.0.1:{self.protocol['ports']['gateway']}"

    def healthy(self, *, timeout=READINESS_PROBE_SECONDS):
        # urllib 超时约束单次 socket 阻塞；完整响应返回后再由等待器复核总期限。
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        try:
            with opener.open(self.url + "/healthz", timeout=timeout) as response:
                return response.status == 200 and json.load(response) == {"status": "ok"}
        except urllib.error.URLError as error:
            if isinstance(error.reason, (FileNotFoundError, PermissionError)):
                raise error.reason from error
            raise ServiceNotReady(str(error)) from error
        except (ConnectionError, TimeoutError, json.JSONDecodeError) as error:
            raise ServiceNotReady(str(error)) from error

    def stop_app(self):
        for name in reversed(list(self.application)):
            self.application.pop(name).stop()

    def close(self):
        self.stop_app()
        for name in reversed(list(self.infrastructure)):
            self.infrastructure.pop(name).stop()
