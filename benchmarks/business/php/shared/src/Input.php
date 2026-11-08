<?php
declare(strict_types=1);
namespace Benchmark\Business;

final readonly class Input
{
    public function __construct(private Contract $contract) {}
    public static function media(string $contentType,string $encoding): void
    {
        // 两个框架共用相同媒体白名单，只接受未压缩 UTF-8 JSON。
        $json = '~\A[ \t]*application/json[ \t]*(?:;[ \t]*charset[ \t]*=[ \t]*(?:utf-8|"utf-8")[ \t]*)?\z~iD';
        $encoding = strtolower(trim($encoding," \t"));
        if (($encoding!=='' && $encoding!=='identity') || !preg_match($json,$contentType)) { throw new ApiError(422); }
    }
    public static function decimal(string $value, int $minimum, int $maximum): int
    {
        // 先检查字符串和长度，再转为整数，拒绝浮点、指数、符号和整数溢出。
        $ceiling = (string)$maximum;
        if (!preg_match('/\A(?:0|[1-9][0-9]*)\z/D',$value) || strlen($value)>strlen($ceiling) || (strlen($value)===strlen($ceiling) && strcmp($value,$ceiling)>0)) { throw new ApiError(422); }
        $integer = (int)$value;
        if ($integer < $minimum) { throw new ApiError(422); }
        return $integer;
    }
    public static function id(string $value): int { return self::decimal($value,1,PHP_INT_MAX); }
    public function catalog(string $query): array
    {
        $values = ['category_id'=>1,'min_price'=>0,'max_price'=>$this->contract->integer('maximum_price_cents'),'cursor_price'=>0,'cursor_id'=>0,'limit'=>$this->contract->integer('catalog_page_size')];
        $ranges = ['category_id'=>[1,$this->contract->integer('categories_per_tenant')],'min_price'=>[0,$values['max_price']],'max_price'=>[0,$values['max_price']],'cursor_price'=>[0,$values['max_price']],'cursor_id'=>[0,PHP_INT_MAX],'limit'=>[1,$this->contract->integer('maximum_page_size')]];
        $seen = [];
        if ($query !== '') {
            foreach (explode('&',$query) as $pair) {
                if (!str_contains($pair,'=') || preg_match('/%(?![0-9a-fA-F]{2})/',$pair)) { throw new ApiError(422); }
                [$name,$value] = array_map('urldecode',explode('=',$pair,2));
                if (!isset($ranges[$name]) || isset($seen[$name])) { throw new ApiError(422); }
                $seen[$name] = true;
                $values[$name] = self::decimal($value,...$ranges[$name]);
            }
        }
        if (isset($seen['cursor_price']) !== isset($seen['cursor_id']) || ($values['cursor_price']===0) !== ($values['cursor_id']===0) || $values['min_price']>$values['max_price']) { throw new ApiError(422); }
        return $values;
    }
    private function object(string $body, array $fields): \stdClass
    {
        if (strlen($body)>$this->contract->integer('maximum_body_bytes')) { throw new ApiError(422); }
        try { $value = json_decode($body, false, flags: JSON_THROW_ON_ERROR); }
        catch(\JsonException) { throw new ApiError(422); }
        if (!$value instanceof \stdClass || array_diff(array_keys((array)$value),$fields) || array_diff($fields,array_keys((array)$value))) { throw new ApiError(422); }
        JsonMembers::assertUniqueInValidDocument($body);
        return $value;
    }
    public function order(string $body, string $key): array
    {
        if (!preg_match('/\A[a-f0-9]{32}\z/D',$key)) { throw new ApiError(422); }
        $document = $this->object($body,['items']);
        if (!is_array($document->items) || count($document->items)<1 || count($document->items)>$this->contract->integer('maximum_order_items')) { throw new ApiError(422); }
        $items = []; $seen = [];
        foreach ($document->items as $item) {
            if (!$item instanceof \stdClass || count((array)$item)!==2 || !isset($item->product_id,$item->quantity) || !is_int($item->product_id) || $item->product_id<1 || !is_int($item->quantity) || $item->quantity<1 || $item->quantity>$this->contract->integer('maximum_item_quantity') || isset($seen[$item->product_id])) { throw new ApiError(422); }
            $seen[$item->product_id] = true;
            $items[] = ['product_id'=>$item->product_id,'quantity'=>$item->quantity];
        }
        return $items;
    }
    public function comment(string $body): string
    {
        $document = $this->object($body,['body']);
        if (!is_string($document->body) || !mb_check_encoding($document->body,'UTF-8')) { throw new ApiError(422); }
        // Unicode 空白与其他语言的 UTF-8 trim 语义一致，字符计数不按字节。
        $text = preg_replace('/\A[\p{Z}\x{0009}-\x{000D}\x{0085}]+|[\p{Z}\x{0009}-\x{000D}\x{0085}]+\z/u','',$document->body);
        $length = mb_strlen($text,'UTF-8');
        if ($length<$this->contract->integer('minimum_comment_characters') || $length>$this->contract->integer('maximum_comment_characters')) { throw new ApiError(422); }
        return $text;
    }
}
