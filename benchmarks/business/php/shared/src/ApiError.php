<?php
declare(strict_types=1);
namespace Benchmark\Business;

final class ApiError extends \RuntimeException
{
    // 对外错误采用固定文案，不拼接底层 SQL、凭据或用户输入。
    private const ERRORS = [
        422 => ['validation_error','Invalid request'], 401 => ['unauthorized','Unauthorized'],
        404 => ['not_found','Not found'], 403 => ['forbidden','Forbidden'],
        409 => ['conflict','Conflict'], 500 => ['internal_error','Internal server error'],
    ];
    public readonly string $errorCode;
    public function __construct(public readonly int $status)
    {
        [$this->errorCode, $message] = self::ERRORS[$status] ?? self::ERRORS[500];
        parent::__construct($message);
    }
    public function body(): array { return ['error'=>['code'=>$this->errorCode,'message'=>$this->getMessage()]]; }
    public static function from(\Throwable $error): self { return $error instanceof self ? $error : new self(500); }
}
