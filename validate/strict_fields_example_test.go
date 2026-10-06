package validate_test

import (
	"fmt"

	"github.com/zhuhanxin0308/thinkgo/v3/validate"
)

func ExampleDisallowUnknownFields() {
	v := validate.NewValidator().SetRules(map[string]string{"name": "required"})
	input := map[string]interface{}{"name": "Alice", "is_admin": true}
	result, err := v.Validate(input, validate.DisallowUnknownFields())
	if err != nil {
		panic(err) // Rule configuration errors are separate from rejected input.
	}
	fmt.Println(result.Valid())
	fmt.Println(result.Violations()[0].Field, result.Violations()[0].Rule)
	// Output:
	// false
	// is_admin unknown_field
}
