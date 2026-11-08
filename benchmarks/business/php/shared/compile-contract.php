<?php
declare(strict_types=1);

// 启动编译不依赖任一框架或测试 vendor，生产安装可以直接调用。
require __DIR__.'/src/Contract.php';
require __DIR__.'/src/ContractSnapshot.php';

if ($argc!==3) {
    fwrite(STDERR,"用法：php compile-contract.php <合同绝对目录> <快照绝对目录>\n");
    exit(2);
}
try {
    echo json_encode(\Benchmark\Business\ContractSnapshot::compile($argv[1],$argv[2]),JSON_THROW_ON_ERROR|JSON_UNESCAPED_SLASHES)."\n";
} catch (\Throwable $error) {
    fwrite(STDERR,'合同快照生成失败：'.$error->getMessage()."\n");
    exit(1);
}
