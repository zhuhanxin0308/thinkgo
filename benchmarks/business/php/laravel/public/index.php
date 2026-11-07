<?php
declare(strict_types=1);
define('LARAVEL_START', microtime(true));
require dirname(__DIR__) . '/vendor/autoload.php';
// 使用框架原生请求处理和终止阶段，保留完整中间件与响应生命周期。
$app = require dirname(__DIR__) . '/bootstrap/app.php';
$app->handleRequest(Illuminate\Http\Request::capture());
