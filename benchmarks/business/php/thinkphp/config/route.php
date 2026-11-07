<?php
// 仅执行明确注册的业务路由，不开放默认控制器访问。
return ['url_route_must'=>true,'route_complete_match'=>true,'url_html_suffix'=>'','default_route_pattern'=>'[^/]+'];
