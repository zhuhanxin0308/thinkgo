<?php
declare(strict_types=1);
namespace Benchmark\Business;

final class JsonMembers
{
    public static function assertUniqueInValidDocument(string $document): void
    {
        // 调用前先由 json_decode 校验完整语法、UTF-8 与嵌套深度。
        // 此扫描仅维护逐对象成员集合，不替代原生 JSON 解码器。
        $frames = [];
        $length = strlen($document);
        for ($offset=0;$offset<$length;$offset++) {
            $token = $document[$offset];
            if ($token==='{') {
                $frames[] = ['object'=>true,'expect_key'=>true,'keys'=>[]];
            } elseif ($token==='[') {
                $frames[] = ['object'=>false];
            } elseif ($token==='}' || $token===']') {
                array_pop($frames);
            } elseif ($token===',') {
                $current = array_key_last($frames);
                if ($current!==null && $frames[$current]['object']) {
                    $frames[$current]['expect_key'] = true;
                }
            } elseif ($token==='"') {
                $start = $offset;
                // 跳过整个字符串，包括转义引号、反斜线和 Unicode 转义。
                for ($offset++;$offset<$length;$offset++) {
                    if ($document[$offset]==='\\') { $offset++; }
                    elseif ($document[$offset]==='"') { break; }
                }
                $current = array_key_last($frames);
                if ($current!==null && $frames[$current]['object'] && $frames[$current]['expect_key']) {
                    $name = json_decode(substr($document,$start,$offset-$start+1),flags:JSON_THROW_ON_ERROR);
                    if (array_key_exists($name,$frames[$current]['keys'])) { throw new ApiError(422); }
                    $frames[$current]['keys'][$name] = true;
                    $frames[$current]['expect_key'] = false;
                }
            }
        }
    }
}
