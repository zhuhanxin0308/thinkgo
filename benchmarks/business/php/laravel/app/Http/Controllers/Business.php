<?php
declare(strict_types=1);
namespace App\Http\Controllers;

use Benchmark\Business\Factory;
use Benchmark\Business\Input;
use Benchmark\Business\Result;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;

final class Business
{
    private function response(Result $result): JsonResponse { return response()->json($result->body,$result->status,$result->headers,JSON_UNESCAPED_UNICODE); }
    public function health(): JsonResponse { return response()->json(['status'=>'ok']); }
    public function catalog(Request $request): JsonResponse
    {
        // 使用原始查询串保留重复参数证据，不能依赖已经合并参数的查询数组。
        return $this->response(Factory::service()->catalog($request->attributes->get('benchmark.identity'),Factory::input()->catalog((string)$request->server->get('QUERY_STRING',''))));
    }
    public function product(Request $request, string $id): JsonResponse
    {
        return $this->response(Factory::service()->product($request->attributes->get('benchmark.identity'),Input::id($id)));
    }
    public function order(Request $request): JsonResponse
    {
        Input::media((string)$request->header('Content-Type',''),(string)$request->header('Content-Encoding',''));
        $key = (string)$request->header('Idempotency-Key','');
        $items = Factory::input()->order($request->getContent(),$key);
        return $this->response(Factory::service()->order($request->attributes->get('benchmark.identity'),$key,$items));
    }
    public function comment(Request $request, string $id): JsonResponse
    {
        Input::media((string)$request->header('Content-Type',''),(string)$request->header('Content-Encoding',''));
        $body = Factory::input()->comment($request->getContent());
        return $this->response(Factory::service()->comment($request->attributes->get('benchmark.identity'),Input::id($id),$body));
    }
}
