<?php
// 此 API 不提供文件管理路由，关闭框架默认的本地磁盘 HTTP 发布。
return ['default'=>'local','disks'=>['local'=>['driver'=>'local','root'=>storage_path('app/private'),'serve'=>false,'throw'=>true]]];
