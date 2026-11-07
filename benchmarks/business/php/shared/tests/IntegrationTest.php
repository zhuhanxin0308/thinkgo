<?php
declare(strict_types=1);
namespace Benchmark\Tests;

use Benchmark\Business\ApiError;
use Benchmark\Business\Contract;
use Benchmark\Business\Factory;
use Benchmark\Business\PdoDatabase;
use Benchmark\Business\RedisCache;
use Benchmark\Business\Service;
use PHPUnit\Framework\TestCase;

final class IntegrationTest extends TestCase
{
    private PdoDatabase $database;
    private RedisCache $cache;
    private Service $service;
    private string $namespace;
    private const IDENTITY = ['tenant_id'=>1,'user_id'=>1];

    protected function setUp(): void
    {
        if (!getenv('BENCH_TEST_DATABASE_URL') || !getenv('BENCH_REDIS_ADDR')) {
            self::markTestSkipped('真实后端测试需要隔离的 BENCH_TEST_DATABASE_URL 和 BENCH_REDIS_ADDR');
        }
        $contract = Contract::load(dirname(__DIR__,3).'/contracts');
        $this->namespace = 'php_test:'.bin2hex(random_bytes(8));
        $this->database = new PdoDatabase($contract,getenv('BENCH_TEST_DATABASE_URL'));
        $this->cache = new RedisCache(getenv('BENCH_REDIS_ADDR'),'php_test',5);
        $this->service = new Service($contract,$this->database,$this->cache,$this->namespace);
    }

    public function testRealCatalogCacheOrderReplayAndInvalidation(): void
    {
        $input = Factory::input();
        $page = $this->service->catalog(self::IDENTITY,$input->catalog('limit=2'));
        self::assertCount(2,$page->body['items']);
        self::assertNotNull($page->body['next_cursor']);
        self::assertSame(['id','name','price_cents','category','stock'],array_keys($page->body['items'][0]));
        $miss = $this->service->product(self::IDENTITY,1);
        self::assertSame('miss',$miss->headers['X-Benchmark-Cache']);
        self::assertCount(3,$miss->body['specs']);
        self::assertSame($miss->body,$this->service->product(self::IDENTITY,1)->body);
        self::assertSame('hit',$this->service->product(self::IDENTITY,1)->headers['X-Benchmark-Cache']);
        self::assertArrayNotHasKey('cost_cents',$miss->body);
        $key = bin2hex(random_bytes(16));
        $items = [['product_id'=>2,'quantity'=>1],['product_id'=>1,'quantity'=>2]];
        $created = $this->service->order(self::IDENTITY,$key,$items);
        self::assertSame(201,$created->status);
        self::assertNull($this->cache->get($this->namespace.':product:1:1'));
        $replay = $this->service->order(self::IDENTITY,$key,array_reverse($items));
        self::assertSame(200,$replay->status);
        self::assertSame($created->body,$replay->body);
        $after = $this->service->product(self::IDENTITY,1);
        self::assertSame($miss->body['stock']-2,$after->body['stock']);
        self::assertSame('miss',$after->headers['X-Benchmark-Cache']);
        $this->cache->delete($this->namespace.':product:1:1');
        $this->expectException(ApiError::class);
        $this->service->order(self::IDENTITY,$key,[['product_id'=>1,'quantity'=>1]]);
    }

    public function testRollbackRestoresStockAndReservationAfterLaterItemFailure(): void
    {
        $before = $this->database->one('product',[1,1]);
        $key = bin2hex(random_bytes(16));
        try {
            // 第二件商品不存在，第一件已经扣减的库存也必须回滚。
            $this->service->order(self::IDENTITY,$key,[['product_id'=>1,'quantity'=>1],['product_id'=>PHP_INT_MAX,'quantity'=>1]]);
            self::fail('不存在商品被接受');
        } catch(ApiError $error) { self::assertSame(404,$error->status); }
        self::assertSame($before,$this->database->one('product',[1,1]));
        self::assertNull($this->database->one('existing_order',[1,1,$key]));
        self::assertFalse($this->database->connection()->inTransaction());
        self::assertSame(201,$this->service->order(self::IDENTITY,$key,[['product_id'=>1,'quantity'=>1]])->status);
    }

    public function testTenantIsolationAndTicketVersionAreEnforcedByRealDatabase(): void
    {
        $before = $this->database->one('lock_ticket',[1,1]);
        $comment = $this->service->comment(self::IDENTITY,1,str_repeat('评',16));
        self::assertSame(201,$comment->status);
        self::assertSame((int)$before[2]+1,$comment->body['version']);
        foreach ([[['tenant_id'=>2,'user_id'=>1001],1,404],[['tenant_id'=>1,'user_id'=>2],1,403]] as [$identity,$id,$status]) {
            try { $this->service->comment($identity,$id,str_repeat('评',16)); self::fail('工单越权被接受'); }
            catch(ApiError $error) { self::assertSame($status,$error->status); }
        }
        self::assertSame((int)$before[2]+1,(int)$this->database->one('lock_ticket',[1,1])[2]);
        $this->expectException(ApiError::class);
        $this->service->product(['tenant_id'=>2,'user_id'=>1001],1);
    }

    public function testFactoryUsesRealPersistentConnectionsAndRejectsMissingConfiguration(): void
    {
        self::assertTrue($this->database->connection()->getAttribute(\PDO::ATTR_PERSISTENT));
        self::assertFalse($this->database->connection()->getAttribute(\PDO::ATTR_EMULATE_PREPARES));
        self::assertSame(Factory::service(),Factory::service());
        $payload = '1.1.'.(time()+60);
        $token = 'Bearer '.$payload.'.'.hash_hmac('sha256',$payload,getenv('BENCH_SECRET'));
        self::assertSame(self::IDENTITY,Factory::authentication()->authenticate($token,time()));
        self::assertSame(200,Factory::service()->catalog(self::IDENTITY,Factory::input()->catalog('limit=1'))->status);
        self::assertSame(['error'=>['code'=>'internal_error','message'=>'Internal server error']],ApiError::from(new \RuntimeException('private database detail'))->body());
        $this->expectException(\RuntimeException::class);
        Factory::environment('BENCH_TEST_DELIBERATELY_MISSING');
    }

    public function testConcurrentSameKeyCreatesOneOrderAndDeductsStockOnce(): void
    {
        $before = $this->database->one('product',[1,1]);
        $key = bin2hex(random_bytes(16));
        // 独立 PHP 进程各自建立连接，避免 fork 继承父进程的 PDO 套接字。
        $code = <<<'PHP'
        require $argv[1];
        $result = \Benchmark\Business\Factory::service()->order(['tenant_id'=>1,'user_id'=>1],$argv[2],[['product_id'=>1,'quantity'=>2]]);
        echo json_encode(['status'=>$result->status,'body'=>$result->body],JSON_THROW_ON_ERROR);
        PHP;
        $environment = getenv();
        $environment['BENCH_DATABASE_URL'] = getenv('BENCH_TEST_DATABASE_URL');
        $environment['BENCH_NAMESPACE'] = $this->namespace;
        $children = [];
        for ($index=0;$index<2;$index++) {
            $process = proc_open([PHP_BINARY,'-r',$code,dirname(__DIR__,2).'/vendor/autoload.php',$key],[0=>['pipe','r'],1=>['pipe','w'],2=>['pipe','w']],$pipes,null,$environment);
            self::assertIsResource($process);
            fclose($pipes[0]);
            $children[] = [$process,$pipes];
        }
        $responses = [];
        foreach ($children as [$process,$pipes]) {
            $output = stream_get_contents($pipes[1]); $errors = stream_get_contents($pipes[2]);
            fclose($pipes[1]); fclose($pipes[2]);
            self::assertSame(0,proc_close($process),$errors);
            $responses[] = json_decode($output,true,flags:JSON_THROW_ON_ERROR);
        }
        $statuses = array_column($responses,'status'); sort($statuses);
        self::assertSame([200,201],$statuses);
        self::assertSame($responses[0]['body'],$responses[1]['body']);
        self::assertSame((int)$before[3]-2,(int)$this->database->one('product',[1,1])[3]);
    }
}
