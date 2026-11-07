<?php
declare(strict_types=1);

require dirname(__DIR__) . '/vendor/autoload.php';

// 使用原生 HTTP 启动、发送和结束生命周期，FPM 与常驻扩展共用业务路由。
$http = (new think\App(dirname(__DIR__)))->http;
$response = $http->run();
$response->send();
$http->end($response);
