"""仅检查指定安装目录的发行包与文件身份，不导入第三方代码。"""

import hashlib
import importlib.metadata
import json
import pathlib
import re
import sys
import sysconfig


def snapshot(prefix):
    prefix = pathlib.Path(prefix)
    if prefix.is_symlink() or not prefix.is_dir():
        raise ValueError("受控 Python 目录不存在或是符号链接")
    root = prefix.resolve()
    versions = {}
    for distribution in importlib.metadata.distributions(path=[str(root)]):
        if pathlib.Path(distribution.locate_file("")).resolve() != root:
            raise ValueError("Python 发行包来源不在受控目录")
        name = re.sub(r"[-_.]+", "-", distribution.metadata["Name"] or "").lower()
        if not name or name in versions:
            raise ValueError("Python 发行包名称为空或重复")
        versions[name] = distribution.version
    files = {}
    for path in sorted(root.rglob("*")):
        if path.is_symlink():
            raise ValueError("受控 Python 依赖不允许符号链接")
        relative = path.relative_to(root)
        # 运行时可生成字节码缓存；源码、顶层字节码、元数据和所有其他文件仍逐一核验。
        if "__pycache__" in relative.parts or path.is_dir():
            continue
        if not path.is_file() or not path.resolve().is_relative_to(root):
            raise ValueError("Python 依赖包含非法文件")
        with path.open("rb") as source:
            files[relative.as_posix()] = hashlib.file_digest(source, "sha256").hexdigest()
    if not versions or not files:
        raise ValueError("受控 Python 目录没有完整发行包")
    return {"versions": versions, "files_sha256": files, "python": sys.version.split()[0],
            "platform": sysconfig.get_platform(), "cache_tag": sys.implementation.cache_tag}


if __name__ == "__main__":
    print(json.dumps(snapshot(sys.argv[1]), sort_keys=True))
