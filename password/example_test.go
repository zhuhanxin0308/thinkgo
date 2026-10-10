package password_test

import (
	"context"
	"fmt"

	"github.com/zhuhanxin0308/thinkgo/v3/password"
)

func ExampleHasher() {
	// Construct once at startup, then share across registration/login requests.
	hasher, err := password.New(password.DefaultConfig())
	if err != nil {
		panic(err)
	}
	ctx := context.Background() // An HTTP handler should use the request context.
	encoded, err := hasher.Hash(ctx, "a long passphrase for this example")
	if err != nil {
		panic(err)
	}
	// Store encoded, never the plaintext. Load it from the user's record to check.
	matched, err := hasher.Check(ctx, encoded, "a long passphrase for this example")
	if err != nil {
		panic(err)
	}
	fmt.Println("matched:", matched)
	upgrade, err := hasher.NeedsRehash(encoded)
	if err != nil {
		panic(err)
	}
	fmt.Println("upgrade:", upgrade)
	// Output:
	// matched: true
	// upgrade: false
}
