<?php
declare(strict_types=1);
namespace App\Http\Middleware;

use Benchmark\Business\Factory;
use Closure;
use Illuminate\Http\Request;
use Symfony\Component\HttpFoundation\Response;

final class Authenticate
{
    public function handle(Request $request, Closure $next): Response
    {
        $request->attributes->set('benchmark.identity',Factory::authentication()->authenticate((string)$request->header('Authorization',''),time()));
        return $next($request);
    }
}
