<?php
declare(strict_types=1);
namespace Benchmark\Business;

// 缓存失败向上抛出，避免把基础设施错误伪装为缓存未命中。
interface Cache
{
    public function get(string $key): ?string;
    public function set(string $key, string $value, int $ttl): void;
    public function delete(string ...$keys): void;
}
