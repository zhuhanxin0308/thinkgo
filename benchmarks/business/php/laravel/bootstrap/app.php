<?php
declare(strict_types=1);

use Benchmark\Business\ApiError;
use Illuminate\Foundation\Application;
use Illuminate\Foundation\Configuration\Exceptions;
use Illuminate\Foundation\Configuration\Middleware;
use Illuminate\Http\Request;
use Symfony\Component\HttpKernel\Exception\NotFoundHttpException;

return Application::configure(basePath:dirname(__DIR__))
    ->withRouting(using:static function(): void { require dirname(__DIR__) . '/routes/api.php'; })
    ->withMiddleware(static function(Middleware $middleware): void {
        // 保留框架默认全局中间件；业务认证作为显式路由中间件执行。
    })
    ->withExceptions(static function(Exceptions $exceptions): void {
        $exceptions->dontReport([ApiError::class]);
        $exceptions->render(static function(Throwable $error, Request $request) {
            if ($error instanceof NotFoundHttpException) { $error = new ApiError(404); }
            $public = ApiError::from($error);
            return response()->json($public->body(),$public->status);
        });
    })->create();
