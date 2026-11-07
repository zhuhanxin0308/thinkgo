<?php
declare(strict_types=1);
namespace Benchmark\Business;

final class Factory
{
    private static ?Contract $contract = null;
    private static ?Service $service = null;
    public static function environment(string $key): string
    {
        $value = getenv($key);
        if ($value===false || $value==='') { throw new \RuntimeException('缺少运行配置：' . $key); }
        return $value;
    }
    public static function contract(): Contract { return self::$contract ??= Contract::loadSnapshot(self::environment('BENCH_CONTRACT_SNAPSHOT')); }
    public static function input(): Input { return new Input(self::contract()); }
    public static function authentication(): Authentication { return new Authentication(self::contract(),self::environment('BENCH_SECRET')); }
    public static function service(): Service
    {
        // 常驻 worker 可以复用无请求状态的业务依赖，身份始终作为显式参数传入。
        if (self::$service !== null) { return self::$service; }
        $contract = self::contract(); $namespace = self::environment('BENCH_NAMESPACE');
        if (!preg_match('/\A[A-Za-z0-9:_-]+\z/',$namespace)) { throw new \RuntimeException('缓存命名空间无效'); }
        return self::$service = new Service($contract,new PdoDatabase($contract,self::environment('BENCH_DATABASE_URL')),new RedisCache(self::environment('BENCH_REDIS_ADDR'),$namespace,$contract->integer('request_timeout_seconds')),$namespace);
    }
}
