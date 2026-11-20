<?php
declare(strict_types=1);
namespace Benchmark\Business;

final readonly class Contract
{
    public const SNAPSHOT_VERSION = 1;
    public const MAX_POSTGRES_INTEGER = 2147483647;
    private const MAX_POSTGRES_SMALLINT = 32767;
    private const SEED_PRICE_BASE = 100;
    private const SEED_PRICE_STRIDE = 7919;
    private function __construct(private array $dataset, private array $queries) {}
    public static function load(string $directory): self
    {
        $source = self::source($directory);
        return new self($source['dataset'],$source['queries']);
    }
    public static function source(string $directory): array
    {
        // 仅供启动编译和合同测试读取原始来源，请求工厂必须加载预生成快照。
        if (!str_starts_with($directory, '/') || !is_dir($directory)) { throw new \RuntimeException('合同目录必须是已存在的绝对路径'); }
        $datasetBytes = file_get_contents($directory . '/dataset.json');
        $queryBytes = file_get_contents($directory . '/queries.json');
        if ($datasetBytes===false || $queryBytes===false) { throw new \RuntimeException('合同来源读取失败'); }
        $dataset = json_decode($datasetBytes, true, flags: JSON_THROW_ON_ERROR);
        $queries = json_decode($queryBytes, true, flags: JSON_THROW_ON_ERROR);
        if (!is_array($dataset) || !is_array($queries)) { throw new \RuntimeException('合同格式无效'); }
        self::validateDataset($dataset);
        return ['dataset'=>$dataset,'queries'=>$queries,'source_sha256'=>['dataset.json'=>hash('sha256',$datasetBytes),'queries.json'=>hash('sha256',$queryBytes)]];
    }
    public static function loadSnapshot(string $path): self
    {
        if (!str_starts_with($path,'/')) { throw new \RuntimeException('合同快照必须是绝对路径'); }
        // 不在请求中读取 JSON、计算摘要或检查 mtime；不可变数组脚本由 OPcache 缓存。
        $snapshot = require $path;
        if (!is_array($snapshot) || ($snapshot['schema_version']??null)!==self::SNAPSHOT_VERSION || !is_array($snapshot['dataset']??null) || !is_array($snapshot['queries']??null)) { throw new \RuntimeException('合同快照格式无效'); }
        self::validateDataset($snapshot['dataset']);
        return new self($snapshot['dataset'],$snapshot['queries']);
    }
    private static function validateDataset(array $dataset): void
    {
        if (array_key_exists('specifications_per_product', $dataset)) {
            $specs = $dataset['specifications_per_product'];
            if (!is_int($specs) || $specs < 1 || $specs > self::MAX_POSTGRES_SMALLINT) {
                throw new \RuntimeException('合同 specifications_per_product 超出 PostgreSQL smallint 位置列范围');
            }
        }
        if (array_key_exists('seed', $dataset) || array_key_exists('products_per_tenant', $dataset)) {
            $seed = $dataset['seed'] ?? null;
            $products = $dataset['products_per_tenant'] ?? null;
            // 算乘积之前先验界，避免 PHP 溢出转成浮点后丢失精度。
            if (!is_int($products) || $products < 2 || $products > intdiv(PHP_INT_MAX, self::SEED_PRICE_STRIDE)
                || !is_int($seed) || $seed < 1 - self::SEED_PRICE_BASE - self::SEED_PRICE_STRIDE
                || $seed > PHP_INT_MAX - $products * self::SEED_PRICE_STRIDE) {
                throw new \RuntimeException('合同 seed 与 products_per_tenant 无法生成正价的 PostgreSQL bigint 商品数据');
            }
        }
        if (array_key_exists('maximum_item_quantity', $dataset)) {
            $maximumQuantity = $dataset['maximum_item_quantity'];
            if (!is_int($maximumQuantity) || $maximumQuantity < 1 || $maximumQuantity > self::MAX_POSTGRES_INTEGER) {
                throw new \RuntimeException('合同 maximum_item_quantity 超出 PostgreSQL integer 范围');
            }
        }
    }
    public function integer(string $name): int
    {
        $value = $this->dataset[$name] ?? null;
        if (!is_int($value) || ($name !== 'seed' && $value <= 0)) { throw new \RuntimeException('合同整数配置无效'); }
        return $value;
    }
    public function query(string $name): string
    {
        $query = $this->queries[$name] ?? null;
        if (!is_string($query) || $query === '') { throw new \RuntimeException('合同 SQL 不存在'); }
        return $query;
    }
}
