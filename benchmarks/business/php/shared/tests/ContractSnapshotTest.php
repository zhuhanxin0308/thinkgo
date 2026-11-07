<?php
declare(strict_types=1);
namespace Benchmark\Tests;

use Benchmark\Business\Contract;
use Benchmark\Business\ContractSnapshot;
use PHPUnit\Framework\TestCase;

final class ContractSnapshotTest extends TestCase
{
    private string $temporary;
    private string $source;
    private string $cache;

    protected function setUp(): void
    {
        $this->temporary = sys_get_temp_dir().'/thinkgo-contract-'.bin2hex(random_bytes(8));
        $this->source = $this->temporary.'/source';
        $this->cache = $this->temporary.'/cache';
        mkdir($this->source,0770,true);
        foreach (['dataset.json','queries.json'] as $name) {
            copy(dirname(__DIR__,3).'/contracts/'.$name,$this->source.'/'.$name);
        }
    }

    protected function tearDown(): void
    {
        // 只清理本测试独占的临时副本，不触碰仓库合同或运行证据。
        $iterator = new \RecursiveIteratorIterator(new \RecursiveDirectoryIterator($this->temporary,\FilesystemIterator::SKIP_DOTS),\RecursiveIteratorIterator::CHILD_FIRST);
        foreach ($iterator as $entry) { $entry->isDir() ? rmdir($entry->getPathname()) : unlink($entry->getPathname()); }
        rmdir($this->temporary);
    }

    public function testSnapshotPreservesEveryContractValueAndQuery(): void
    {
        $metadata = ContractSnapshot::compile($this->source,$this->cache);
        $contract = Contract::loadSnapshot($metadata['path']);
        $snapshot = require $metadata['path'];
        foreach (json_decode(file_get_contents($this->source.'/dataset.json'),true,flags:JSON_THROW_ON_ERROR) as $name=>$value) {
            self::assertSame($value,$snapshot['dataset'][$name]);
            if (is_int($value)) { self::assertSame($value,$contract->integer($name)); }
        }
        foreach (json_decode(file_get_contents($this->source.'/queries.json'),true,flags:JSON_THROW_ON_ERROR) as $name=>$query) {
            self::assertSame($query,$contract->query($name));
        }
        self::assertSame(hash_file('sha256',$metadata['path']),$metadata['file_sha256']);
        self::assertSame(1,$metadata['schema_version']);
        foreach (['dataset.json','queries.json'] as $name) {
            self::assertSame(hash_file('sha256',$this->source.'/'.$name),$metadata['source_sha256'][$name]);
        }
        self::assertSame('contract-'.$metadata['content_sha256'].'.php',basename($metadata['path']));
    }

    public function testSnapshotExportsSqlEscapesUnicodeAndLargeIntegersLosslessly(): void
    {
        $query = "SELECT '中文', 'it''s', E'\\\\', :id\n-- \$variable 与双引号 \"保留\"";
        file_put_contents($this->source.'/dataset.json',json_encode(['large'=>PHP_INT_MAX],JSON_THROW_ON_ERROR));
        file_put_contents($this->source.'/queries.json',json_encode(['escaped'=>$query],JSON_THROW_ON_ERROR));
        $metadata = ContractSnapshot::compile($this->source,$this->cache);
        $contract = Contract::loadSnapshot($metadata['path']);
        self::assertSame(PHP_INT_MAX,$contract->integer('large'));
        self::assertSame($query,$contract->query('escaped'));
    }

    public function testContentAddressIgnoresMtimeAndChangesForEitherSource(): void
    {
        $first = ContractSnapshot::compile($this->source,$this->cache);
        touch($this->source.'/dataset.json',time()-100);
        self::assertSame($first,ContractSnapshot::compile($this->source,$this->cache));
        file_put_contents($this->source.'/dataset.json',"\n",FILE_APPEND);
        $datasetChange = ContractSnapshot::compile($this->source,$this->cache);
        self::assertNotSame($first['path'],$datasetChange['path']);
        file_put_contents($this->source.'/queries.json',"\n",FILE_APPEND);
        $queryChange = ContractSnapshot::compile($this->source,$this->cache);
        self::assertNotSame($datasetChange['path'],$queryChange['path']);
        self::assertSame($first['file_sha256'],hash_file('sha256',$first['path']));
        self::assertCount(3,glob($this->cache.'/contract-*.php'));
    }

    public function testConflictingExistingSnapshotIsPreservedAndRejected(): void
    {
        $metadata = ContractSnapshot::compile($this->source,$this->cache);
        file_put_contents($metadata['path'],'existing evidence');
        try { ContractSnapshot::compile($this->source,$this->cache); self::fail('快照冲突未被拒绝'); }
        catch (\RuntimeException) { self::assertSame('existing evidence',file_get_contents($metadata['path'])); }
        self::assertSame([],glob($this->cache.'/.contract-*'));
    }

    public function testRelativePathsAndInvalidSnapshotSchemaAreRejected(): void
    {
        foreach ([['relative',$this->cache],[$this->source,'relative']] as [$source,$cache]) {
            try { ContractSnapshot::compile($source,$cache); self::fail('相对路径被接受'); }
            catch (\RuntimeException) { self::addToAssertionCount(1); }
        }
        foreach (['return null;','return ["schema_version"=>999,"dataset"=>[],"queries"=>[]];',
                  'return ["schema_version"=>1,"dataset"=>"bad","queries"=>[]];'] as $code) {
            $path = $this->temporary.'/invalid-'.bin2hex(random_bytes(4)).'.php';
            file_put_contents($path,'<?php '.$code);
            try { Contract::loadSnapshot($path); self::fail('错误快照格式被接受'); }
            catch (\RuntimeException) { self::addToAssertionCount(1); }
        }
        $this->expectException(\RuntimeException::class);
        Contract::loadSnapshot('relative.php');
    }

    public function testFreshPhpProcessUsesSnapshotWithoutOriginalJsonOrFallback(): void
    {
        $metadata = ContractSnapshot::compile($this->source,$this->cache);
        unlink($this->source.'/dataset.json'); unlink($this->source.'/queries.json');
        $code = <<<'PHP'
        require $argv[1];
        $first=\Benchmark\Business\Factory::contract();
        $second=\Benchmark\Business\Factory::contract();
        echo json_encode([$first===$second,$first->integer('database_connection_budget'),$first->query('product')],JSON_THROW_ON_ERROR);
        PHP;
        $environment = getenv();
        $environment['BENCH_CONTRACT_DIR'] = $this->source;
        $environment['BENCH_CONTRACT_SNAPSHOT'] = $metadata['path'];
        [$status,$output,$errors] = $this->process([PHP_BINARY,'-r',$code,dirname(__DIR__,2).'/vendor/autoload.php'],$environment);
        self::assertSame(0,$status,$errors);
        $result = json_decode($output,true,flags:JSON_THROW_ON_ERROR);
        self::assertSame([true,24],array_slice($result,0,2));
        self::assertNotEmpty($result[2]);
        unset($environment['BENCH_CONTRACT_SNAPSHOT']);
        $environment['BENCH_CONTRACT_DIR'] = dirname(__DIR__,3).'/contracts';
        [$status] = $this->process([PHP_BINARY,'-r',$code,dirname(__DIR__,2).'/vendor/autoload.php'],$environment);
        self::assertNotSame(0,$status);
    }

    public function testGeneratedArrayScriptCanBeCachedByOpcache(): void
    {
        $metadata = ContractSnapshot::compile($this->source,$this->cache);
        $code = 'require $argv[1]; \Benchmark\Business\Contract::loadSnapshot($argv[2]); echo json_encode(opcache_is_script_cached($argv[2]));';
        [$status,$output,$errors] = $this->process([PHP_BINARY,'-d','opcache.enable_cli=1','-d','opcache.file_update_protection=0','-r',$code,dirname(__DIR__,2).'/vendor/autoload.php',$metadata['path']],getenv());
        self::assertSame(0,$status,$errors);
        self::assertSame('true',$output);
    }

    public function testCliPublishesMetadataWithoutFrameworkOrTestVendorBootstrap(): void
    {
        $entry = dirname(__DIR__).'/compile-contract.php';
        [$status,$output,$errors] = $this->process([PHP_BINARY,$entry,$this->source,$this->cache],getenv());
        self::assertSame(0,$status,$errors);
        self::assertSame(ContractSnapshot::compile($this->source,$this->cache),json_decode($output,true,flags:JSON_THROW_ON_ERROR));
        [$status,,$errors] = $this->process([PHP_BINARY,$entry],getenv());
        self::assertSame(2,$status);
        self::assertStringContainsString('用法',$errors);
        [$status,,$errors] = $this->process([PHP_BINARY,$entry,'relative',$this->cache],getenv());
        self::assertSame(1,$status);
        self::assertStringContainsString('合同快照生成失败',$errors);
    }

    private function process(array $command,array $environment): array
    {
        $process = proc_open($command,[0=>['pipe','r'],1=>['pipe','w'],2=>['pipe','w']],$pipes,null,$environment);
        self::assertIsResource($process); fclose($pipes[0]);
        $output = stream_get_contents($pipes[1]); $errors = stream_get_contents($pipes[2]);
        fclose($pipes[1]); fclose($pipes[2]);
        return [proc_close($process),$output,$errors];
    }

    public function testMalformedSourceAndUnavailableDestinationFailBeforePublishing(): void
    {
        file_put_contents($this->source.'/dataset.json','{');
        try { ContractSnapshot::compile($this->source,$this->cache); self::fail('非法 JSON 被接受'); }
        catch (\JsonException) { self::assertDirectoryDoesNotExist($this->cache); }
        file_put_contents($this->source.'/dataset.json','null');
        try { ContractSnapshot::compile($this->source,$this->cache); self::fail('非对象合同被接受'); }
        catch (\RuntimeException) { self::assertDirectoryDoesNotExist($this->cache); }
        file_put_contents($this->source.'/dataset.json','{"limit":1}');
        file_put_contents($this->cache,'blocked');
        $this->expectException(\RuntimeException::class);
        ContractSnapshot::compile($this->source,$this->cache);
    }
}
