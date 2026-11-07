<?php
declare(strict_types=1);
namespace Benchmark\Business;

final readonly class Service
{
    public function __construct(private Contract $contract, private Database $database, private Cache $cache, private string $namespace) {}
    private function productKey(int $tenant, int $id): string { return $this->namespace . ":product:$tenant:$id"; }
    public function catalog(array $identity, array $query): Result
    {
        $rows = $this->database->all('catalog',[$identity['tenant_id'],$query['category_id'],$query['min_price'],$query['max_price'],$query['cursor_price'],$query['cursor_id'],$query['limit']+1]);
        $more = count($rows)>$query['limit'];
        if ($more) { array_pop($rows); }
        $items = array_map(fn($row)=>['id'=>(int)$row[0],'name'=>$row[1],'price_cents'=>(int)$row[2],'category'=>$row[3],'stock'=>(int)$row[4]],$rows);
        $last = $more ? $items[array_key_last($items)] : null;
        return new Result(200,['items'=>$items,'next_cursor'=>$last ? ['price_cents'=>$last['price_cents'],'id'=>$last['id']] : null]);
    }
    public function product(array $identity, int $id): Result
    {
        $key = $this->productKey($identity['tenant_id'],$id);
        $cached = $this->cache->get($key);
        if ($cached !== null) { return new Result(200,json_decode($cached,true,flags:JSON_THROW_ON_ERROR),['X-Benchmark-Cache'=>'hit']); }
        $row = $this->database->one('product',[$identity['tenant_id'],$id]);
        if ($row === null) { throw new ApiError(404); }
        $specs = $this->database->all('specifications',[$id,$this->contract->integer('specifications_per_product')]);
        $body = ['id'=>(int)$row[0],'name'=>$row[1],'price_cents'=>(int)$row[2],'stock'=>(int)$row[3],'category'=>$row[4],'description'=>$row[5],'specs'=>array_map(fn($spec)=>['name'=>$spec[0],'value'=>$spec[1]],$specs)];
        // 未命中严格执行两次查询后 SETEX，不做请求合并或额外查询。
        $this->cache->set($key,json_encode($body,JSON_THROW_ON_ERROR|JSON_UNESCAPED_UNICODE),$this->contract->integer('cache_ttl_seconds'));
        return new Result(200,$body,['X-Benchmark-Cache'=>'miss']);
    }
    public function order(array $identity, string $key, array $items): Result
    {
        $ordered = $items;
        usort($ordered,fn($left,$right)=>$left['product_id']<=>$right['product_id']);
        $tenant = $identity['tenant_id']; $user = $identity['user_id'];
        $result = $this->database->transaction(function() use($tenant,$user,$key,$ordered): Result {
            $reserved = $this->database->one('reserve_order',[$tenant,$user,$key]);
            if ($reserved === null) { return $this->replay($tenant,$user,$key,$ordered); }
            $orderId = (int)$reserved[0]; $total = 0; $output = [];
            // 锁定输入副本的升序 ID，保留调用方输入并减少并发死锁。
            foreach ($ordered as $item) {
                $id = $item['product_id']; $quantity = $item['quantity'];
                $product = $this->database->one('lock_product',[$tenant,$id]);
                if ($product === null) { throw new ApiError(404); }
                [$price,$stock] = array_map('intval',$product);
                if ($stock<$quantity) { throw new ApiError(409); }
                if ($price<=0 || $price>intdiv(PHP_INT_MAX,$quantity) || $total>PHP_INT_MAX-$price*$quantity) { throw new \RuntimeException('订单金额超出整数范围'); }
                if ($this->database->execute('deduct_stock',[$quantity,$tenant,$id,$quantity])!==1) { throw new ApiError(409); }
                $this->database->execute('insert_order_item',[$orderId,$id,$quantity,$price]);
                $total += $price*$quantity;
                $output[] = ['product_id'=>$id,'quantity'=>$quantity,'unit_price_cents'=>$price];
            }
            if ($this->database->execute('finish_order',[$total,$orderId])!==1) { throw new \RuntimeException('订单状态更新失败'); }
            $this->database->execute('order_audit',[$orderId,$user,$total]);
            return new Result(201,['order_id'=>$orderId,'total_cents'=>$total,'status'=>'created','items'=>$output]);
        });
        // 提交后一次 DEL 失效所有商品，幂等重放也重试失效，Redis 错误不能被吞掉。
        $this->cache->delete(...array_map(fn($item)=>$this->productKey($tenant,$item['product_id']),$ordered));
        return $result;
    }
    private function replay(int $tenant, int $user, string $key, array $items): Result
    {
        $order = $this->database->one('existing_order',[$tenant,$user,$key]);
        if ($order === null || $order[2]!=='created') { throw new \RuntimeException('已存在订单状态无效'); }
        $rows = $this->database->all('existing_order_items',[(int)$order[0]]);
        $existing = array_map(fn($row)=>['product_id'=>(int)$row[0],'quantity'=>(int)$row[1]],$rows);
        if ($existing!==$items) { throw new ApiError(409); }
        return new Result(200,['order_id'=>(int)$order[0],'total_cents'=>(int)$order[1],'status'=>'created','items'=>array_map(fn($row)=>['product_id'=>(int)$row[0],'quantity'=>(int)$row[1],'unit_price_cents'=>(int)$row[2]],$rows)]);
    }
    public function comment(array $identity, int $id, string $body): Result
    {
        return $this->database->transaction(function() use($identity,$id,$body): Result {
            $ticket = $this->database->one('lock_ticket',[$identity['tenant_id'],$id]);
            if ($ticket === null) { throw new ApiError(404); }
            if ((int)$ticket[0]!==$identity['user_id']) { throw new ApiError(403); }
            if ($ticket[1]!=='open') { throw new ApiError(409); }
            $comment = $this->database->one('insert_comment',[$id,$identity['user_id'],$body]);
            $updated = $this->database->one('update_ticket',[$identity['tenant_id'],$id]);
            if ($comment===null || $updated===null) { throw new \RuntimeException('评论写入失败'); }
            return new Result(201,['comment_id'=>(int)$comment[0],'ticket_id'=>$id,'version'=>(int)$updated[0],'status'=>'open']);
        });
    }
}
