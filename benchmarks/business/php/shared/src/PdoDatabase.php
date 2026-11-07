<?php
declare(strict_types=1);
namespace Benchmark\Business;

use PDO;

final class PdoDatabase implements Database
{
    private ?PDO $connection = null;
    public function __construct(private readonly Contract $contract, private readonly string $url) {}
    public function connection(): PDO
    {
        if ($this->connection !== null) { return $this->connection; }
        $parts = parse_url($this->url);
        if (!is_array($parts) || !in_array($parts['scheme']??'', ['postgres','postgresql'],true)) { throw new \RuntimeException('数据库连接 URL 无效'); }
        $host = $parts['host']??''; $database = rawurldecode(ltrim($parts['path']??'','/'));
        if (!preg_match('/\A[A-Za-z0-9_.:\[\]-]+\z/',$host) || !preg_match('/\A[A-Za-z0-9_-]+\z/',$database)) { throw new \RuntimeException('数据库主机或库名无效'); }
        parse_str($parts['query']??'', $options);
        $ssl = $options['sslmode']??'prefer';
        if (!in_array($ssl,['disable','allow','prefer','require','verify-ca','verify-full'],true)) { throw new \RuntimeException('数据库 SSL 模式无效'); }
        $port = $parts['port']??5432;
        $timeout = $this->contract->integer('request_timeout_seconds');
        $dsn = "pgsql:host=$host;port=$port;dbname=$database;sslmode=$ssl;connect_timeout=$timeout";
        // FPM 每个 worker 复用一个连接；正式运行固定24个 worker 以遵守总预算。
        return $this->connection = new PDO($dsn,rawurldecode($parts['user']??''),rawurldecode($parts['pass']??''),[
            PDO::ATTR_PERSISTENT=>true, PDO::ATTR_ERRMODE=>PDO::ERRMODE_EXCEPTION,
            PDO::ATTR_DEFAULT_FETCH_MODE=>PDO::FETCH_NUM, PDO::ATTR_STRINGIFY_FETCHES=>false,
            PDO::ATTR_EMULATE_PREPARES=>false, \Pdo\Pgsql::ATTR_DISABLE_PREPARES=>true,
        ]);
    }
    private function statement(string $query, array $parameters): \PDOStatement
    {
        $statement = $this->connection()->prepare($this->contract->query($query));
        $statement->execute($parameters);
        return $statement;
    }
    public function one(string $query, array $parameters): ?array
    {
        $row = $this->statement($query,$parameters)->fetch();
        return $row === false ? null : $row;
    }
    public function all(string $query, array $parameters): array { return $this->statement($query,$parameters)->fetchAll(); }
    public function execute(string $query, array $parameters): int { return $this->statement($query,$parameters)->rowCount(); }
    public function transaction(callable $operation): mixed
    {
        $pdo = $this->connection();
        $pdo->beginTransaction();
        try { $result = $operation(); $pdo->commit(); return $result; }
        catch(\Throwable $error) { if ($pdo->inTransaction()) { $pdo->rollBack(); } throw $error; }
    }
}
