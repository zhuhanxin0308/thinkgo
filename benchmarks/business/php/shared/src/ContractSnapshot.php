<?php
declare(strict_types=1);
namespace Benchmark\Business;

final class ContractSnapshot
{
    public static function compile(string $directory,string $cacheDirectory): array
    {
        $source = Contract::source($directory);
        if (!str_starts_with($cacheDirectory,'/')) { throw new \RuntimeException('快照目录必须是绝对路径'); }
        if (!is_dir($cacheDirectory) && !@mkdir($cacheDirectory,0770,true) && !is_dir($cacheDirectory)) { throw new \RuntimeException('快照目录无法创建'); }
        $cacheDirectory = realpath($cacheDirectory);
        if ($cacheDirectory===false) { throw new \RuntimeException('快照目录无法解析'); }
        $hashes = $source['source_sha256'];
        // 固定版本前缀与两个固定长度摘要区分源内容；文件路径永远不会原地换版本。
        $contentHash = hash('sha256','thinkgo-contract-v'.Contract::SNAPSHOT_VERSION."\0".$hashes['dataset.json'].$hashes['queries.json']);
        $snapshot = ['schema_version'=>Contract::SNAPSHOT_VERSION,'source_sha256'=>$hashes,'dataset'=>$source['dataset'],'queries'=>$source['queries']];
        $bytes = "<?php\ndeclare(strict_types=1);\n// 由共同 JSON 合同生成的不可变配置，禁止手工修改。\nreturn ".var_export($snapshot,true).";\n";
        $path = $cacheDirectory.'/contract-'.$contentHash.'.php';
        self::publish($path,$bytes);
        return ['schema_version'=>Contract::SNAPSHOT_VERSION,'path'=>$path,'file_sha256'=>hash('sha256',$bytes),'content_sha256'=>$contentHash,'source_sha256'=>$hashes];
    }

    private static function publish(string $path,string $bytes): void
    {
        if (file_exists($path)) {
            self::assertExisting($path,$bytes);
            return;
        }
        $temporary = tempnam(dirname($path),'.contract-');
        if ($temporary===false) { throw new \RuntimeException('无法创建合同快照临时文件'); }
        try {
            if (file_put_contents($temporary,$bytes)!==strlen($bytes)) { throw new \RuntimeException('合同快照写入不完整'); }
            // 硬链接原子发布完整文件；并发生成相同内容可复用，冲突内容拒绝覆盖。
            if (!@link($temporary,$path)) { self::assertExisting($path,$bytes); }
        } finally {
            unlink($temporary);
        }
    }

    private static function assertExisting(string $path,string $bytes): void
    {
        if (!is_file($path) || file_get_contents($path)!==$bytes) { throw new \RuntimeException('已有合同快照内容不匹配，拒绝覆盖'); }
    }
}
