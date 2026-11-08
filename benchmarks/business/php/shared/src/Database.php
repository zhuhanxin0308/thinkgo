<?php
declare(strict_types=1);
namespace Benchmark\Business;

// 接口表达真实持久化操作，测试替身可以验证事务和 SQL 合同顺序。
interface Database
{
    public function one(string $query, array $parameters): ?array;
    public function all(string $query, array $parameters): array;
    public function execute(string $query, array $parameters): int;
    public function transaction(callable $operation): mixed;
}
