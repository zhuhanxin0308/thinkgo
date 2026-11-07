<?php
declare(strict_types=1);

namespace Benchmark\Tests;

use Benchmark\Business\ApiError;
use Benchmark\Business\Authentication;
use Benchmark\Business\Contract;
use Benchmark\Business\Input;
use PHPUnit\Framework\Attributes\DataProvider;
use PHPUnit\Framework\TestCase;

final class InputAuthTest extends TestCase
{
    private function contract(): Contract
    {
        return Contract::load(dirname(__DIR__, 3) . '/contracts');
    }

    public function testTokenRequiresSignatureExpiryAndTenantMembership(): void
    {
        // 认证必须同时验证签名、时效和租户内用户范围。
        $auth = new Authentication($this->contract(), str_repeat('s', 32));
        $payload = '2.1001.2000';
        $token = 'Bearer ' . $payload . '.' . hash_hmac('sha256', $payload, str_repeat('s', 32));
        self::assertSame(['tenant_id' => 2, 'user_id' => 1001], $auth->authenticate($token, 1000));
        self::assertSame(['tenant_id' => 2, 'user_id' => 1001], $auth->authenticate('bEaReR '.substr($token,7), 1000));
        foreach (['', str_replace('1001', '1000', $token), str_replace('2.', '3.', $token), $token . '0'] as $invalid) {
            try { $auth->authenticate($invalid, 1000); self::fail('非法令牌被接受'); }
            catch (ApiError $error) { self::assertSame(401, $error->status); }
        }
        $this->expectException(ApiError::class);
        $auth->authenticate($token, 2000);
    }

    public function testSignedCrossTenantAndOverflowTokensAreRejected(): void
    {
        $auth = new Authentication($this->contract(), str_repeat('s', 32));
        foreach (['0.1.2000', '5.4001.2000', '2.1.2000', '1.1001.2000', '1.1.99999999999999999999999'] as $payload) {
            $token = 'Bearer ' . $payload . '.' . hash_hmac('sha256', $payload, str_repeat('s', 32));
            try { $auth->authenticate($token, 1000); self::fail('越界身份被接受'); }
            catch (ApiError $error) { self::assertSame('unauthorized', $error->errorCode); }
        }
    }

    public function testCatalogDefaultsAndCompleteCursor(): void
    {
        $input = new Input($this->contract());
        self::assertSame(['category_id'=>1,'min_price'=>0,'max_price'=>100000000,'cursor_price'=>0,'cursor_id'=>0,'limit'=>20], $input->catalog(''));
        self::assertSame(50, $input->catalog('category_id=32&cursor_price=100&cursor_id=9&limit=50')['limit']);
    }

    #[DataProvider('invalidQueries')]
    public function testInvalidCatalogQueryIsRejected(string $query): void
    {
        $this->expectException(ApiError::class);
        (new Input($this->contract()))->catalog($query);
    }

    public static function invalidQueries(): array
    {
        // 参数污染、溢出和游标缺失不能被 PHP 的宽松类型转换掩盖。
        return array_map(fn($value) => [$value], ['category_id=0','category_id=33','limit=0','limit=51','limit=-1','limit=1.0','limit=1e1','limit=+1','limit=01','limit[]=2','limit=2&limit=3','unknown=1','cursor_id=1','cursor_price=1','min_price=2&max_price=1','max_price=100000001','cursor_id=999999999999999999999&cursor_price=1','limit=%GG','limit','&limit=2','limit=2&']);
    }

    public function testValidOrderPreservesInputOrder(): void
    {
        $items = (new Input($this->contract()))->order('{"items":[{"product_id":9,"quantity":2},{"product_id":1,"quantity":1}]}', str_repeat('a', 32));
        self::assertSame([['product_id'=>9,'quantity'=>2],['product_id'=>1,'quantity'=>1]], $items);
    }

    #[DataProvider('invalidBodies')]
    public function testOrderValidationRejectsInvalidShapeAndBounds(string $body): void
    {
        $this->expectException(ApiError::class);
        (new Input($this->contract()))->order($body, str_repeat('a', 32));
    }

    public static function invalidBodies(): array
    {
        return array_map(fn($value) => [$value], ['{','null','[]','{}','{"items":[]}','{"items":{}}','{"items":[{}]}','{"items":[{"product_id":"1","quantity":1}]}','{"items":[{"product_id":1,"quantity":1.0}]}','{"items":[{"product_id":1,"quantity":0}]}','{"items":[{"product_id":0,"quantity":1}]}','{"items":[{"product_id":1,"quantity":11}]}','{"items":[{"product_id":1,"quantity":1,"cost":1}]}','{"items":[{"product_id":1,"quantity":1}],"tenant_id":2}','{"items":[{"product_id":1,"quantity":1},{"product_id":1,"quantity":2}]}', str_repeat(' ', 16385)]);
    }

    public function testOrderItemLimitAndIdempotencyKey(): void
    {
        $input = new Input($this->contract());
        foreach (['', str_repeat('A', 32), str_repeat('a', 31)] as $key) {
            try { $input->order('{"items":[{"product_id":1,"quantity":1}]}', $key); self::fail('非法幂等键被接受'); }
            catch (ApiError $error) { self::assertSame(422, $error->status); }
        }
        $body = json_encode(['items'=>array_map(fn($id)=>['product_id'=>$id,'quantity'=>1], range(1,9))]);
        $this->expectException(ApiError::class);
        $input->order($body, str_repeat('a', 32));
    }

    public function testCommentIsTrimmedAndCountedAsUtf8Characters(): void
    {
        $input = new Input($this->contract());
        $body = str_repeat('评', 16);
        self::assertSame($body, $input->comment(json_encode(['body'=>" \n" . $body . "\t "])));
        self::assertSame($body, $input->comment(json_encode(['body'=>"\u{2003}\u{0085}" . $body . "\u{3000}"])));
        self::assertSame(2000, mb_strlen($input->comment(json_encode(['body'=>str_repeat('评',2000)]))));
        foreach (['{"body":1}', '{"body":"短"}', '{"body":"1234567890123456","owner":1}', json_encode(['body'=>str_repeat('x',2001)]), "{\"body\":\"\xff\"}"] as $invalid) {
            try { $input->comment($invalid); self::fail('非法评论被接受'); }
            catch (ApiError $error) { self::assertSame(422, $error->status); }
        }
    }

    public function testPathIdsRejectInvalidDecimals(): void
    {
        self::assertSame(42, Input::id('42'));
        foreach (['0','-1','01','1.1','999999999999999999999999'] as $value) {
            try { Input::id($value); self::fail('非法标识被接受'); }
            catch (ApiError $error) { self::assertSame(422, $error->status); }
        }
    }

    public function testCursorPresenceAndZeroPairMustBeConsistent(): void
    {
        $input = new Input($this->contract());
        self::assertSame(0,$input->catalog('cursor_price=0&cursor_id=0')['cursor_id']);
        foreach (['cursor_price=0','cursor_id=0','cursor_price=0&cursor_id=1','cursor_price=1&cursor_id=0'] as $query) {
            try { $input->catalog($query); self::fail('不完整或混合零游标被接受'); }
            catch(ApiError $error) { self::assertSame(422,$error->status); }
        }
    }

    #[DataProvider('duplicateMembers')]
    public function testDuplicateJsonMembersAreRejectedEvenWhenBothValuesAreValid(string $body,bool $order): void
    {
        $input = new Input($this->contract());
        $this->expectException(ApiError::class);
        if ($order) { $input->order($body,str_repeat('a',32)); }
        else { $input->comment($body); }
    }

    public static function duplicateMembers(): array
    {
        return [
            ['{"body":"1234567890123456","body":"abcdefghijklmnop"}',false],
            ['{"body":"1234567890123456","b\\u006fdy":"abcdefghijklmnop"}',false],
            ['{"items":[{"product_id":1,"quantity":1}],"items":[{"product_id":2,"quantity":1}]}',true],
            ['{"items":[{"product_id":1,"product_id":2,"quantity":1}]}',true],
            ['{"items":[{"product_id":1,"quantity":1,"quanti\\u0074y":2}]}',true],
        ];
    }

    public function testJsonMemberScannerKeepsStringsAndSeparateObjectsIndependent(): void
    {
        $input = new Input($this->contract());
        // 正文中出现看似 JSON 的文本、转义引号和反斜线，不构成重复成员。
        $text = '评论正文中包含 {"body":"a","body":"b"}、反斜线 \\ 与中文。';
        self::assertSame($text,$input->comment(json_encode(['body'=>$text],JSON_THROW_ON_ERROR)));
        $body = '{"items":[{"product_id":1,"quantity":1},{"product_id":2,"quantity":2}]}';
        self::assertSame([['product_id'=>1,'quantity'=>1],['product_id'=>2,'quantity'=>2]],$input->order($body,str_repeat('a',32)));
    }

    public function testJsonMediaAcceptsUtf8AndRejectsOtherRepresentations(): void
    {
        foreach (['application/json','Application/JSON',' application/json ; Charset = UTF-8 ', 'application/json; charset="uTf-8"'] as $media) {
            Input::media($media,'');
            Input::media($media,'identity');
            Input::media($media," \tIdEnTiTy\t ");
            self::addToAssertionCount(3);
        }
        foreach ([['',''],['text/plain',''],['application/json;charset=iso-8859-1',''],
                  ['application/json','gzip'],['application/json',"identity\n"],['application/json','identity, gzip'],
                  ['application/json; charset="utf-8',''],['application/json; charset=utf-8; charset=latin1',''],
                  ['application/json;charset',''],['application/json, text/plain',''],['application/json; charset=""',''],
                  ['application/json; charset=utf-8; charset=utf-8',''],['application/json; foo=bar','']] as [$media,$encoding]) {
            try { Input::media($media,$encoding); self::fail('非法媒体表示被接受'); }
            catch (ApiError $error) { self::assertSame(422,$error->status); }
        }
    }
}
