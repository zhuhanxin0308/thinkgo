<?php
use app\controller\Business;
use app\middleware\Authenticate;
use think\facade\Route;

Route::get('healthz',[Business::class,'health']);
Route::get('api/catalog',[Business::class,'catalog'])->middleware(Authenticate::class);
Route::get('api/products/:id',[Business::class,'product'])->middleware(Authenticate::class);
Route::post('api/orders',[Business::class,'order'])->middleware(Authenticate::class);
Route::post('api/tickets/:id/comments',[Business::class,'comment'])->middleware(Authenticate::class);
