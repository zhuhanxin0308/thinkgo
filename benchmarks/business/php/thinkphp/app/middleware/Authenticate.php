<?php
declare(strict_types=1);
namespace app\middleware;

use Benchmark\Business\Factory;
use Closure;
use think\Request;
use think\Response;

final class Authenticate
{
    public function handle(Request $request, Closure $next): Response
    {
        $identity = Factory::authentication()->authenticate((string)$request->header('authorization',''),time());
        $request->withMiddleware(['benchmark.identity'=>$identity]);
        return $next($request);
    }
}
