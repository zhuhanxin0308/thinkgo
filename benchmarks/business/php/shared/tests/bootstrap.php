<?php
declare(strict_types=1);

require dirname(__DIR__,2).'/vendor/autoload.php';

// 测试与生产工厂使用同一快照入口；子进程继承现有路径，不重复生成或删除父进程文件。
if (!getenv('BENCH_CONTRACT_SNAPSHOT')) {
    $cacheDirectory = sys_get_temp_dir().'/thinkgo-test-contract-'.bin2hex(random_bytes(8));
    $snapshot = \Benchmark\Business\ContractSnapshot::compile(dirname(__DIR__,3).'/contracts',$cacheDirectory);
    putenv('BENCH_CONTRACT_SNAPSHOT='.$snapshot['path']);
    register_shutdown_function(static function () use ($snapshot,$cacheDirectory): void {
        unlink($snapshot['path']);
        rmdir($cacheDirectory);
    });
}
