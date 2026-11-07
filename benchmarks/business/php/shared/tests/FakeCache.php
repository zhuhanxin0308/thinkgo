<?php
declare(strict_types=1);

namespace Benchmark\Tests;

use Benchmark\Business\Cache;

final class FakeCache implements Cache
{
    public array $values = [];
    public array $events = [];
    public ?\Throwable $failure = null;
    public ?FakeDatabase $database = null;

    public function get(string $key): ?string
    {
        $this->events[] = ['get', $key];
        if ($this->failure) { throw $this->failure; }
        return $this->values[$key] ?? null;
    }
    public function set(string $key, string $value, int $ttl): void
    {
        $this->events[] = ['set', $key, $ttl];
        if ($this->failure) { throw $this->failure; }
        $this->values[$key] = $value;
    }
    public function delete(string ...$keys): void
    {
        // 以可观察事务顺序验证失效操作发生在提交之后。
        if ($this->database) { \PHPUnit\Framework\Assert::assertContains('commit', $this->database->events); }
        $this->events[] = ['delete', ...$keys];
        if ($this->failure) { throw $this->failure; }
        foreach ($keys as $key) { unset($this->values[$key]); }
    }
}
