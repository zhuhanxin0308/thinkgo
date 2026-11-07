<?php
declare(strict_types=1);
namespace app;

use Benchmark\Business\ApiError;
use think\Request;
use think\Response;
use Throwable;

final class ExceptionHandle extends \think\exception\Handle
{
    public function render(Request $request, Throwable $error): Response
    {
        if ($error instanceof \think\exception\HttpException && $error->getStatusCode()===404) {
            $error = new ApiError(404);
        }
        // 客户端只收到固定公共错误，不包含数据库、路径或堆栈信息。
        $public = ApiError::from($error);
        return json($public->body(), $public->status);
    }
    public function report(Throwable $error): void
    {
        if (!$error instanceof ApiError) { parent::report($error); }
    }
}
