<?php
// 复用官方完整监听器与状态清理配置，明确使用 RoadRunner 运行模式。
$config = require dirname(__DIR__) . '/vendor/laravel/octane/config/octane.php';
$config['server'] = 'roadrunner';
$config['max_execution_time'] = \Benchmark\Business\Factory::contract()->integer('request_timeout_seconds');
// 与 FPM 共用总连接预算，基准期间不按请求计数循环重启 worker。
$config['workers'] = \Benchmark\Business\Factory::contract()->integer('database_connection_budget');
$config['max_requests'] = 0;
return $config;
