<?php
declare(strict_types=1);
namespace Benchmark\Business;

// 由框架适配器转换为各自原生 JSON 响应，不直接写 HTTP 输出。
final readonly class Result
{
    public function __construct(public int $status, public array $body, public array $headers = []) {}
}
