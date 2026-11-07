<?php
// 异常日志保留在服务端，响应不携带内部错误信息。
return ['default'=>'stderr','channels'=>['stderr'=>['driver'=>'monolog','handler'=>Monolog\Handler\StreamHandler::class,'with'=>['stream'=>'php://stderr'],'level'=>'error']]];
