<?php
declare(strict_types=1);
namespace Benchmark\Business;

final readonly class Authentication
{
    private const MINIMUM_SECRET_BYTES = 32;
    private const MAXIMUM_HEADER_BYTES = 256;
    public function __construct(private Contract $contract, private string $secret)
    {
        if (strlen($secret) < self::MINIMUM_SECRET_BYTES) { throw new \RuntimeException('认证密钥至少需要32字节'); }
    }
    public function authenticate(string $authorization, ?int $now = null): array
    {
        // 验证原始规范十进制载荷，恒定时间比较 HMAC，防止跨租户身份混淆。
        if (strlen($authorization)>self::MAXIMUM_HEADER_BYTES || !preg_match('/\A(?i:Bearer) ((?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*))\.([a-f0-9]{64})\z/D', $authorization, $matches)) { throw new ApiError(401); }
        if (!hash_equals(hash_hmac('sha256',$matches[1],$this->secret),$matches[2])) { throw new ApiError(401); }
        try { [$tenant,$user,$expiry] = array_map(fn($part)=>Input::decimal($part,0,PHP_INT_MAX),explode('.',$matches[1])); }
        catch(ApiError) { throw new ApiError(401); }
        $users = $this->contract->integer('users_per_tenant');
        if ($tenant < 1 || $tenant > $this->contract->integer('tenant_count') || $user <= ($tenant-1)*$users || $user > $tenant*$users || $expiry <= ($now ?? time())) { throw new ApiError(401); }
        return ['tenant_id'=>$tenant,'user_id'=>$user];
    }
}
