<?php
return [
    'name'=>'Business Benchmark',
    'env'=>'production',
    'debug'=>false,
    'url'=>'http://localhost',
    'timezone'=>'UTC',
    'locale'=>'en',
    'fallback_locale'=>'en',
    // Octane 原生预热会实例化加密器；用独立 HKDF 上下文派生 AES-256 密钥。
    'key'=>env('APP_KEY','base64:'.base64_encode(hash_hkdf('sha256',\Benchmark\Business\Factory::environment('BENCH_SECRET'),32,'benchmark-laravel-encryption'))),
    'cipher'=>'AES-256-CBC',
];
