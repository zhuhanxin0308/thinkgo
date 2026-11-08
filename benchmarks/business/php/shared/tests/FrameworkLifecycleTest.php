<?php
declare(strict_types=1);
namespace Benchmark\Tests;

use PHPUnit\Framework\Attributes\DataProvider;
use PHPUnit\Framework\Attributes\PreserveGlobalState;
use PHPUnit\Framework\Attributes\RunTestsInSeparateProcesses;
use PHPUnit\Framework\TestCase;

#[RunTestsInSeparateProcesses]
#[PreserveGlobalState(false)]
final class FrameworkLifecycleTest extends TestCase
{
    public static function frameworks(): array { return [['thinkphp'],['laravel']]; }

    #[DataProvider('frameworks')]
    public function testNativeRoutingMiddlewareControllersAndResponses(string $framework): void
    {
        if (!getenv('BENCH_TEST_DATABASE_URL')) { self::markTestSkipped('框架生命周期测试需要隔离的真实后端'); }
        putenv('BENCH_DATABASE_URL='.getenv('BENCH_TEST_DATABASE_URL'));
        putenv('BENCH_NAMESPACE=php_lifecycle:'.bin2hex(random_bytes(8)));
        require dirname(__DIR__,2).'/'.$framework.'/vendor/autoload.php';
        $payload = '1.1.'.(time()+60);
        $authorization = 'Bearer '.$payload.'.'.hash_hmac('sha256',$payload,getenv('BENCH_SECRET'));
        $health = $this->dispatch($framework,'GET','/healthz');
        self::assertSame([200,['status'=>'ok']],[$health[0],$health[1]]);
        $unauthorized = $this->dispatch($framework,'GET','/api/catalog');
        self::assertSame(401,$unauthorized[0]);
        self::assertSame('unauthorized',$unauthorized[1]['error']['code']);
        $catalog = $this->dispatch($framework,'GET','/api/catalog?limit=2','',$authorization);
        self::assertSame(200,$catalog[0]); self::assertCount(2,$catalog[1]['items']);
        $invalid = $this->dispatch($framework,'GET','/api/catalog?limit=1&limit=2','',$authorization);
        self::assertSame(422,$invalid[0]);
        $miss = $this->dispatch($framework,'GET','/api/products/1','',$authorization);
        self::assertSame(200,$miss[0]); self::assertSame('miss',$miss[2]['x-benchmark-cache']);
        $hit = $this->dispatch($framework,'GET','/api/products/1','',$authorization);
        self::assertSame($miss[1],$hit[1]); self::assertSame('hit',$hit[2]['x-benchmark-cache']);
        $key = bin2hex(random_bytes(16));
        $body = '{"items":[{"product_id":1,"quantity":1}]}';
        $order = $this->dispatch($framework,'POST','/api/orders',$body,$authorization,$key);
        self::assertSame(201,$order[0]);
        $replay = $this->dispatch($framework,'POST','/api/orders',$body,$authorization,$key);
        self::assertSame(200,$replay[0]); self::assertSame($order[1],$replay[1]);
        $comment = $this->dispatch($framework,'POST','/api/tickets/1/comments',json_encode(['body'=>str_repeat('评',16)]),$authorization);
        self::assertSame(201,$comment[0]);
        self::assertSame(['comment_id','ticket_id','version','status'],array_keys($comment[1]));
        foreach (['{"body":"1234567890123456","body":"abcdefghijklmnop"}',
                  '{"body":"1234567890123456","b\\u006fdy":"abcdefghijklmnop"}'] as $duplicate) {
            self::assertSame(422,$this->dispatch($framework,'POST','/api/tickets/1/comments',$duplicate,$authorization)[0]);
        }
        $duplicate = '{"items":[{"product_id":1,"quantity":1,"quantity":2}]}';
        self::assertSame(422,$this->dispatch($framework,'POST','/api/orders',$duplicate,$authorization,bin2hex(random_bytes(16)))[0]);
        foreach ([['text/plain',''],['application/json','gzip'],['application/json; charset=latin1','']] as [$media,$encoding]) {
            self::assertSame(422,$this->dispatch($framework,'POST','/api/orders',$body,$authorization,bin2hex(random_bytes(16)),$media,$encoding)[0]);
            self::assertSame(422,$this->dispatch($framework,'POST','/api/tickets/1/comments','{"body":"1234567890123456"}',$authorization,'',$media,$encoding)[0]);
        }
        self::assertSame(201,$this->dispatch($framework,'POST','/api/tickets/1/comments','{"body":"1234567890123456"}',$authorization,'','Application/JSON; Charset="UTF-8"','identity')[0]);
        self::assertSame(404,$this->dispatch($framework,'GET','/absent')[0]);
    }

    private function dispatch(string $framework,string $method,string $uri,string $body='',string $authorization='',string $key='',string $media='application/json',string $encoding=''): array
    {
        $root = dirname(__DIR__,2).'/'.$framework;
        // 只替换传输来源；框架原生 HTTP 内核仍完成路由、中间件和响应处理。
        try {
            if ($framework==='thinkphp') {
                $request = (new \think\Request())->setMethod($method)
                    ->setPathinfo(ltrim(parse_url($uri,PHP_URL_PATH),'/'))
                    ->withServer(['REQUEST_METHOD'=>$method,'QUERY_STRING'=>parse_url($uri,PHP_URL_QUERY)??''])
                    ->withHeader(['authorization'=>$authorization,'idempotency-key'=>$key,'content-type'=>$media,'content-encoding'=>$encoding])
                    ->withInput($body);
                $http = (new \think\App($root))->http;
                $response = $http->run($request);
                $result = [$response->getCode(),json_decode($response->getContent(),true,flags:JSON_THROW_ON_ERROR),array_change_key_case($response->getHeader())];
                $http->end($response);
                return $result;
            }
            $app = require $root.'/bootstrap/app.php';
            $request = \Illuminate\Http\Request::create($uri,$method,server:['HTTP_AUTHORIZATION'=>$authorization,'HTTP_IDEMPOTENCY_KEY'=>$key,'CONTENT_TYPE'=>$media,'HTTP_CONTENT_ENCODING'=>$encoding],content:$body);
            $kernel = $app->make(\Illuminate\Contracts\Http\Kernel::class);
            $response = $kernel->handle($request);
            $headers = array_map(static fn($values)=>$values[0],$response->headers->all());
            $result = [$response->getStatusCode(),json_decode($response->getContent(),true,flags:JSON_THROW_ON_ERROR),$headers];
            $kernel->terminate($request,$response);
            return $result;
        } finally {
            // 原生启动器注册的全局错误处理器需要在测试结束时还原给 PHPUnit。
            restore_error_handler();
            restore_exception_handler();
        }
    }
}
