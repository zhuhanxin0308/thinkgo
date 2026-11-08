<?php
declare(strict_types=1);
namespace app\controller;

use Benchmark\Business\Factory;
use Benchmark\Business\Input;
use Benchmark\Business\Result;
use think\Request;
use think\Response;

final class Business
{
    private function response(Result $result): Response { return json($result->body,$result->status,$result->headers); }
    public function health(): Response { return json(['status'=>'ok']); }
    public function catalog(Request $request): Response
    {
        return $this->response(Factory::service()->catalog($request->middleware('benchmark.identity'),Factory::input()->catalog($request->query())));
    }
    public function product(Request $request, string $id): Response
    {
        return $this->response(Factory::service()->product($request->middleware('benchmark.identity'),Input::id($id)));
    }
    public function order(Request $request): Response
    {
        Input::media((string)$request->header('content-type',''),(string)$request->header('content-encoding',''));
        $key = (string)$request->header('idempotency-key','');
        // 读取框架保留的原始 JSON，避免字符串、浮点数被隐式转换为整数。
        $items = Factory::input()->order($request->getContent(),$key);
        return $this->response(Factory::service()->order($request->middleware('benchmark.identity'),$key,$items));
    }
    public function comment(Request $request, string $id): Response
    {
        Input::media((string)$request->header('content-type',''),(string)$request->header('content-encoding',''));
        $body = Factory::input()->comment($request->getContent());
        return $this->response(Factory::service()->comment($request->middleware('benchmark.identity'),Input::id($id),$body));
    }
}
