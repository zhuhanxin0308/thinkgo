<?php
declare(strict_types=1);

namespace Benchmark\Tests;

use Benchmark\Business\Database;
use PHPUnit\Framework\Assert;

final class FakeDatabase implements Database
{
    public array $events = [];
    public function __construct(public array $expected) {}

    private function next(string $method, string $query, array $parameters): mixed
    {
        // 按合同核对查询顺序和参数，避免仅复制实现的空壳测试。
        $step = array_shift($this->expected);
        Assert::assertNotNull($step, '出现合同以外的数据库操作');
        Assert::assertSame([$method, $query, $parameters], array_slice($step, 0, 3));
        $this->events[] = $query;
        if ($step[3] instanceof \Throwable) { throw $step[3]; }
        return $step[3];
    }

    public function one(string $query, array $parameters): ?array { return $this->next('one', $query, $parameters); }
    public function all(string $query, array $parameters): array { return $this->next('all', $query, $parameters); }
    public function execute(string $query, array $parameters): int { return $this->next('execute', $query, $parameters); }
    public function transaction(callable $operation): mixed
    {
        $this->events[] = 'begin';
        try { $result = $operation(); $this->events[] = 'commit'; return $result; }
        catch (\Throwable $error) { $this->events[] = 'rollback'; throw $error; }
    }
}
