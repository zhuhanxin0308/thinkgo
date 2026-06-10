package cache

import "time"

// Driver interface for cache drivers
type Driver interface {
	// Get gets a value from cache
	Get(key string) interface{}
	
	// Set sets a value in cache
	Set(key string, val interface{}, ttl time.Duration)
	
	// Has checks if key exists
	Has(key string) bool
	
	// Delete deletes a key
	Delete(key string)
	
	// Clear clears the cache
	Clear()
	
	// Inc increments a key
	Inc(key string, step int64) int64
	
	// Dec decrements a key
	Dec(key string, step int64) int64
}
