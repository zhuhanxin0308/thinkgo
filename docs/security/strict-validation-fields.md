# Explicit validation field allowlists

Issue #37 is addressed by the per-call `validate.DisallowUnknownFields()` option.
Normal validation continues to check declared rules without filtering the input.
Do not pass the original request map directly to a database write just because
ordinary validation succeeded.

```go
v := validate.NewValidator().SetRules(map[string]string{
    "name|Display name": "required|max:100",
})
result, err := v.Validate(input, validate.DisallowUnknownFields())
// Handle configuration errors (err) and rejected input (!result.Valid())
// separately before performing any write or other side effect.
```

Strict mode uses actual field names in the compiled rule plan, not display
aliases. With `WithScene`, only the chosen scene's fields are allowed, including
when other rules exist outside that scene. Include every expected top-level
field in the selected plan. Empty plans accept empty input only.

Unknown fields produce `unknown_field` violations in lexical field-name order.
Without `CollectAllErrors`, the first unknown field is returned. With it, all
unknown fields precede normal rule violations. Configuration errors still take
precedence. Messages identify field names, not submitted values. Existing custom-message
and language lookup apply to `unknown_field`, including field-specific messages.

The option does not mutate the input or shared Validator. The same Validator,
option and cached `ValidateRules` rules may serve strict and non-strict calls.
Callers must still synchronize their own concurrent input mutations.

This is a top-level field allowlist, not recursive schema validation, a data
filter, authorization or a safe-to-persist marker. Declaring a map-valued field
does not allowlist its nested keys. Use dedicated input DTOs or validate nested
objects explicitly, authorize writes, and map only intended editable fields.

Example and regression commands:

```sh
go test -race -mod=readonly -shuffle=on -count=20 ./validate ./lang
go test -mod=readonly -run '^ExampleDisallowUnknownFields$' ./validate
```

This is an additive API. Existing defaults and stored data are unchanged. The
issue is closed only after merge into main and successful main CI/regressions.
