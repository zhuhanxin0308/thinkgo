<?php
declare(strict_types=1);

namespace Benchmark\Tests;

use Benchmark\Business\ApiError;
use Benchmark\Business\Contract;
use Benchmark\Business\Service;
use PHPUnit\Framework\Attributes\DataProvider;
use PHPUnit\Framework\TestCase;

final class ServiceTest extends TestCase
{
    private const IDENTITY = ['tenant_id'=>1,'user_id'=>1];
    private const KEY = 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa';
    private function service(FakeDatabase $db, ?FakeCache $cache = null): Service
    {
        return new Service(Contract::load(dirname(__DIR__, 3) . '/contracts'), $db, $cache ?? new FakeCache(), 'unit');
    }

    public function testCatalogUsesLookaheadAndOnlyPublicColumns(): void
    {
        $db = new FakeDatabase([['all','catalog',[1,1,0,100000000,0,0,3], [[1,'一',101,'分类',9],[2,'二',102,'分类',8],[3,'三',103,'分类',7]]]]);
        $result = $this->service($db)->catalog(self::IDENTITY, ['category_id'=>1,'min_price'=>0,'max_price'=>100000000,'cursor_price'=>0,'cursor_id'=>0,'limit'=>2]);
        self::assertSame(200, $result->status);
        self::assertCount(2, $result->body['items']);
        self::assertSame(['id','name','price_cents','category','stock'], array_keys($result->body['items'][0]));
        self::assertSame(['price_cents'=>102,'id'=>2], $result->body['next_cursor']);
        self::assertSame([], $db->expected);
    }

    public function testEmptyCatalogHasNullCursor(): void
    {
        $db = new FakeDatabase([['all','catalog',[1,1,0,100,0,0,2], []]]);
        self::assertSame(['items'=>[],'next_cursor'=>null], $this->service($db)->catalog(self::IDENTITY, ['category_id'=>1,'min_price'=>0,'max_price'=>100,'cursor_price'=>0,'cursor_id'=>0,'limit'=>1])->body);
    }

    public function testProductMissPerformsTwoQueriesAndSubsequentHitUsesCache(): void
    {
        $db = new FakeDatabase([['one','product',[1,1],[1,'商品',123,8,'分类','描述']],['all','specifications',[1,3],[['颜色','红']]]]);
        $cache = new FakeCache();
        $service = $this->service($db,$cache);
        $miss = $service->product(self::IDENTITY,1);
        self::assertSame(['X-Benchmark-Cache'=>'miss'], $miss->headers);
        self::assertSame(['id'=>1,'name'=>'商品','price_cents'=>123,'stock'=>8,'category'=>'分类','description'=>'描述','specs'=>[['name'=>'颜色','value'=>'红']]],$miss->body);
        self::assertSame(['set','unit:product:1:1',3600],$cache->events[1]);
        $hit = $service->product(self::IDENTITY,1);
        self::assertSame(['X-Benchmark-Cache'=>'hit'],$hit->headers);
        self::assertSame($miss->body,$hit->body);
        self::assertSame([], $db->expected);
    }

    public function testMissingProductIsNotCached(): void
    {
        $db = new FakeDatabase([['one','product',[1,9],null]]);
        $cache = new FakeCache();
        try { $this->service($db,$cache)->product(self::IDENTITY,9); self::fail(); }
        catch (ApiError $error) { self::assertSame(404,$error->status); }
        self::assertCount(1,$cache->events);
    }

    public function testOrderLocksInIdOrderAndInvalidatesAfterCommit(): void
    {
        $db = new FakeDatabase([
            ['one','reserve_order',[1,1,self::KEY],[7]],
            ['one','lock_product',[1,1],[100,10]],['execute','deduct_stock',[1,1,1,1],1],['execute','insert_order_item',[7,1,1,100],1],
            ['one','lock_product',[1,9],[200,10]],['execute','deduct_stock',[2,1,9,2],1],['execute','insert_order_item',[7,9,2,200],1],
            ['execute','finish_order',[500,7],1],['execute','order_audit',[7,1,500],1],
        ]);
        $cache = new FakeCache(); $cache->database = $db;
        $input = [['product_id'=>9,'quantity'=>2],['product_id'=>1,'quantity'=>1]];
        $result = $this->service($db,$cache)->order(self::IDENTITY,self::KEY,$input);
        self::assertSame(201,$result->status);
        self::assertSame(500,$result->body['total_cents']);
        self::assertSame('created',$result->body['status']);
        self::assertSame(9,$input[0]['product_id']);
        self::assertSame([['delete','unit:product:1:1','unit:product:1:9']],$cache->events);
        self::assertSame('commit',end($db->events));
        self::assertSame([],$db->expected);
    }

    public function testIdempotentReplayKeepsStockAndRetriesCacheInvalidation(): void
    {
        $db = new FakeDatabase([['one','reserve_order',[1,1,self::KEY],null],['one','existing_order',[1,1,self::KEY],[7,200,'created']],['all','existing_order_items',[7],[[1,2,100]]]]);
        $cache = new FakeCache();
        $result = $this->service($db,$cache)->order(self::IDENTITY,self::KEY,[['product_id'=>1,'quantity'=>2]]);
        self::assertSame(200,$result->status);
        self::assertSame(['order_id'=>7,'total_cents'=>200,'status'=>'created','items'=>[['product_id'=>1,'quantity'=>2,'unit_price_cents'=>100]]],$result->body);
        self::assertSame([['delete','unit:product:1:1']],$cache->events);
    }

    public function testIdempotencyPayloadMismatchRollsBack(): void
    {
        $db = new FakeDatabase([['one','reserve_order',[1,1,self::KEY],null],['one','existing_order',[1,1,self::KEY],[7,200,'created']],['all','existing_order_items',[7],[[1,2,100]]]]);
        try { $this->service($db)->order(self::IDENTITY,self::KEY,[['product_id'=>1,'quantity'=>1]]); self::fail(); }
        catch(ApiError $error) { self::assertSame(409,$error->status); }
        self::assertSame('rollback',end($db->events));
    }

    public function testCacheFailureAfterCommitCanBeRecoveredByIdempotentReplay(): void
    {
        $db = new FakeDatabase([
            ['one','reserve_order',[1,1,self::KEY],[7]],
            ['one','lock_product',[1,1],[100,10]],
            ['execute','deduct_stock',[1,1,1,1],1],
            ['execute','insert_order_item',[7,1,1,100],1],
            ['execute','finish_order',[100,7],1],['execute','order_audit',[7,1,100],1],
            ['one','reserve_order',[1,1,self::KEY],null],
            ['one','existing_order',[1,1,self::KEY],[7,100,'created']],
            ['all','existing_order_items',[7],[[1,1,100]]],
        ]);
        $cache = new FakeCache(); $cache->database = $db;
        $cache->failure = new \RuntimeException('Redis 暂时不可用');
        $service = $this->service($db,$cache);
        $items = [['product_id'=>1,'quantity'=>1]];
        try { $service->order(self::IDENTITY,self::KEY,$items); self::fail('缓存错误被吞掉'); }
        catch(\RuntimeException $error) { self::assertSame($cache->failure,$error); }
        self::assertSame('commit',end($db->events));
        self::assertNotContains('rollback',$db->events);
        $cache->failure = null;
        self::assertSame(200,$service->order(self::IDENTITY,self::KEY,$items)->status);
        self::assertSame([['delete','unit:product:1:1'],['delete','unit:product:1:1']],$cache->events);
        self::assertSame([],$db->expected);
    }

    #[DataProvider('invalidProducts')]
    public function testUnavailableProductRollsBackWithoutCacheInvalidation(?array $product,int $status): void
    {
        $db = new FakeDatabase([['one','reserve_order',[1,1,self::KEY],[7]],['one','lock_product',[1,1],$product]]);
        $cache = new FakeCache();
        try { $this->service($db,$cache)->order(self::IDENTITY,self::KEY,[['product_id'=>1,'quantity'=>2]]); self::fail(); }
        catch(ApiError $error) { self::assertSame($status,$error->status); }
        self::assertSame('rollback',end($db->events)); self::assertSame([],$cache->events);
    }
    public static function invalidProducts(): array { return [[null,404],[[100,1],409]]; }

    public function testDatabaseExceptionRollsBack(): void
    {
        $failure = new \RuntimeException('内部数据库信息');
        $db = new FakeDatabase([['one','reserve_order',[1,1,self::KEY],$failure]]);
        try { $this->service($db)->order(self::IDENTITY,self::KEY,[['product_id'=>1,'quantity'=>1]]); self::fail(); }
        catch(\RuntimeException $error) { self::assertSame($failure,$error); }
        self::assertSame('rollback',end($db->events));
    }

    public function testTicketCommentUpdatesVersionAtomically(): void
    {
        $body = str_repeat('评',16);
        $db = new FakeDatabase([['one','lock_ticket',[1,1],[1,'open',3]],['one','insert_comment',[1,1,$body],[99]],['one','update_ticket',[1,1],[4]]]);
        $result = $this->service($db)->comment(self::IDENTITY,1,$body);
        self::assertSame(201,$result->status);
        self::assertSame(['comment_id'=>99,'ticket_id'=>1,'version'=>4,'status'=>'open'],$result->body);
        self::assertSame('commit',end($db->events));
    }

    #[DataProvider('inaccessibleTickets')]
    public function testTicketAuthorizationAndStateRollback(?array $row,int $status): void
    {
        $db = new FakeDatabase([['one','lock_ticket',[1,1],$row]]);
        try { $this->service($db)->comment(self::IDENTITY,1,str_repeat('评',16)); self::fail(); }
        catch(ApiError $error) { self::assertSame($status,$error->status); }
        self::assertSame('rollback',end($db->events));
    }
    public static function inaccessibleTickets(): array { return [[null,404],[[2,'open',3],403],[[1,'closed',3],409]]; }
}
