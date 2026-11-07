<?php
use App\Http\Controllers\Business;
use App\Http\Middleware\Authenticate;
use Illuminate\Support\Facades\Route;

Route::get('/healthz',[Business::class,'health']);
Route::middleware(Authenticate::class)->group(static function(): void {
    Route::get('/api/catalog',[Business::class,'catalog']);
    Route::get('/api/products/{id}',[Business::class,'product']);
    Route::post('/api/orders',[Business::class,'order']);
    Route::post('/api/tickets/{id}/comments',[Business::class,'comment']);
});
