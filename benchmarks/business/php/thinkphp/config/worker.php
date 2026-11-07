<?php
use Benchmark\Business\Factory;

$address = parse_url('tcp://'.Factory::environment('BENCH_ADDR'));
return [
    'http'=>[
        'enable'=>true,
        'host'=>$address['host'],
        'port'=>$address['port'],
        'worker_num'=>Factory::contract()->integer('database_connection_budget'),
    ],
    'websocket'=>['enable'=>false],
    'queue'=>['enable'=>false],
    'hot_update'=>['enable'=>false],
];
