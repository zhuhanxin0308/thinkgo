<?php
declare(strict_types=1);
namespace Benchmark\Business;

final class RedisCache implements Cache
{
    private ?\Redis $client = null;
    public function __construct(private readonly string $address, private readonly string $namespace, private readonly int $timeout) {}
    private function client(): \Redis
    {
        if ($this->client !== null) { return $this->client; }
        $parts = parse_url('tcp://' . $this->address);
        if (!is_array($parts) || !isset($parts['host'],$parts['port'])) { throw new \RuntimeException('Redis 地址无效'); }
        $client = new \Redis();
        // 通过固定持久连接标识，使每个 PHP worker 只持有一个 Redis 连接。
        if (!$client->pconnect($parts['host'],$parts['port'],$this->timeout,$this->namespace,0,$this->timeout)) { throw new \RuntimeException('Redis 连接失败'); }
        return $this->client = $client;
    }
    public function get(string $key): ?string
    {
        $value = $this->client()->get($key);
        return $value === false ? null : $value;
    }
    public function set(string $key, string $value, int $ttl): void
    {
        if (!$this->client()->setEx($key,$ttl,$value)) { throw new \RuntimeException('Redis 写入失败'); }
    }
    public function delete(string ...$keys): void
    {
        if ($this->client()->del($keys) === false) { throw new \RuntimeException('Redis 删除失败'); }
    }
}
