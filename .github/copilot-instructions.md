# Inco DSL — Copilot Instructions

## Overview

Inco is a Design-by-Contract (DbC) toolkit for Go. It uses special comment directives (`@require`, `@ensure`, `@must`) embedded in standard Go comments. These directives are parsed at build time and transformed into runtime assertions via `go build -overlay`. Source files remain untouched — generated shadow files live in `.inco_cache/`.

## Directive Reference

### `@require` — Precondition

Checked at function entry. Place at the top of a function body.

At gen time, if a `@require` expression is a compile-time constant that evaluates to `false`, Inco emits a warning.

#### Non-defaulted mode (`-nd`)

```go
// @require -nd var1, var2
```

- Asserts that each listed variable is **not zero-valued** (type-aware: uses `go/types` to generate the correct check per type).
- Multiple variables are comma-separated.
- Generates one type-appropriate zero-value check per variable:
  - pointer/slice/map/chan/func/interface → `if var == nil`
  - string → `if var == ""`
  - integer → `if var == 0`
  - float → `if var == 0.0`
  - bool → `if !var`
  - comparable struct → `if var == (T{})`
  - comparable array → `if var == ([N]T{})`
  - comparable type param → `if var == *new(T)`
  - non-comparable type param (`any`) → `if reflect.ValueOf(&var).Elem().IsZero()` (auto-imports `reflect`)

#### Expression mode

```go
// @require <expr>
// @require <expr>, "custom message"
```

- Asserts that `<expr>` evaluates to `true`.
- Generates `if !(<expr>) { panic(...) }`.
- An optional quoted string after a comma provides a custom panic message.
- If no message is given, a default message including the expression text is used.

### `@ensure` — Postcondition

Checked at function exit via `defer`. Typically used with named return values.

#### Non-defaulted mode (`-nd`)

```go
// @ensure -nd result
```

- Wraps the check in `defer func() { if result == nil { panic(...) } }()`.
- The named return variable must be declared in the function signature.

#### Expression mode

```go
// @ensure <expr>
// @ensure <expr>, "custom message"
```

- Same as `@require` expression mode, but wrapped in a `defer`.

### `@must` — Error assertion

Asserts that an error returned from a call is nil. Supports two placement styles:

#### Inline mode (same line as assignment)

```go
res, _ := db.Exec(query) // @must
```

- The `_` (blank identifier) is replaced with a generated error variable `_inco_err_<line>`.
- Generates `if _inco_err_<line> != nil { panic("inco // must violation at <loc>: " + _inco_err_<line>.Error()) }` immediately after the assignment.
- If the LHS has an explicit `err` variable instead of `_`, the check uses `err` directly.

#### Block mode (directive on its own line, applies to the next statement)

```go
// @must
res, _ := db.Query(
    "SELECT * FROM users WHERE id = ?",
)
```

- The directive on its own line applies to the **next assignment statement**.
- Useful for multi-line function calls.

## Syntax Rules

1. Directives must appear as **Go line comments** (`// @directive`) or block comments (`/* @directive */`).
2. The `@` prefix is mandatory and must immediately follow the comment marker (after optional whitespace).
3. `@require` and `@ensure` directives are standalone comments on their own line, placed inside a function body.
4. `@must` can be either inline (trailing comment on an assignment) or standalone (preceding line).
5. `-nd` flag must appear immediately after the directive keyword, before any variable list.
6. In expression mode, the custom message must be a **double-quoted string** and must be the last comma-separated token.
7. Directives work inside closures/anonymous functions and nested scopes.

## Placement Rules

| Directive | Where to place | Scope |
|-----------|----------------|-------|
| `@require -nd` | Top of function body, before logic | Function entry |
| `@require <expr>` | Top of function body, before logic | Function entry |
| `@ensure -nd` | Inside function body (generates `defer`) | Function exit |
| `@ensure <expr>` | Inside function body (generates `defer`) | Function exit |
| `@must` (inline) | Trailing comment on assignment with `_` or `err` | Immediately after assignment |
| `@must` (block) | Own line, before an assignment statement | Immediately after next assignment |

## Generated Code Patterns

### `@require -nd var`

```go
// Type-aware: the check depends on the resolved type of var.
// Examples for different types:

// pointer/interface/slice/map/chan:
if var == nil {
    panic("inco // require -nd violation: [var] is defaulted (nil) at file.go:LINE")
}

// string:
if var == "" {
    panic("inco // require -nd violation: [var] is defaulted (empty string) at file.go:LINE")
}

// integer:
if var == 0 {
    panic("inco // require -nd violation: [var] is defaulted (zero) at file.go:LINE")
}

// bool:
if !var {
    panic("inco // require -nd violation: [var] is defaulted (false) at file.go:LINE")
}

// comparable type param T:
if var == *new(T) {
    panic("inco // require -nd violation: [var] is defaulted (zero value of type param T) at file.go:LINE")
}

// non-comparable type param T (any):
if reflect.ValueOf(&var).Elem().IsZero() {
    panic("inco // require -nd violation: [var] is defaulted (zero value of type param T (reflect)) at file.go:LINE")
}
```

### `@require expr, "msg"`

```go
if !(expr) {
    panic("msg at file.go:LINE")
}
```

### `@ensure -nd result`

```go
// Same type-aware checks as @require -nd, wrapped in defer:
defer func() {
    if result == nil {  // or type-appropriate zero check
        panic("inco // ensure -nd violation: [result] is defaulted (nil) at file.go:LINE")
    }
}()
```

### `res, _ := fn() // @must`

```go
res, _inco_err_LINE := fn()
if _inco_err_LINE != nil {
    panic("inco // must violation at file.go:LINE: " + _inco_err_LINE.Error())
}
```

## Code Generation Guidelines for Copilot

When writing Go code in this project:

1. **Use `@require -nd` for nil-guard preconditions** on pointer, interface, slice, map, and channel parameters instead of manual `if x == nil` checks.
2. **Use `@require <expr>` for value-range preconditions** (e.g., `amount > 0`, `len(name) > 0`) instead of inline validation boilerplate.
3. **Use `@ensure -nd` for postcondition guarantees** on named return values that must not be nil at function exit.
4. **Use `// @must` on assignments that discard errors with `_`** when the error should never occur (replacing silent drops with fail-fast panics).
5. **Keep function bodies clean** — defensive checks belong in directives, not in business logic.
6. **Do not manually write assertion code** that Inco generates; use directives instead.
7. **Never modify files in `.inco_cache/`** — they are auto-generated.
8. **Named return values are required** for `@ensure` directives to reference.
9. The project is self-bootstrapping: source files in `internal/inco/` and `cmd/inco/` use Inco directives themselves.

## CLI Commands

```bash
inco gen [dir]    # Scan .go files, generate shadow files + overlay.json (default: .)
inco build ./...  # Build with contracts enforced (go build -overlay)
inco test ./...   # Test with contracts enforced
inco run .        # Run with contracts enforced
inco clean [dir]  # Remove .inco_cache (default: .)
```

## Project Structure

```
cmd/inco/           CLI entry point (exec.go, main.go)
internal/inco/      Core engine:
  contract.go         Directive parsing (ParseDirective)
  engine.go           AST injection, overlay generation, //line mapping
  typecheck.go        Type resolution, zero-value check generation, generics support
example/            Demo files:
  demo.go             Basic directives (require, ensure, must)
  transfer.go         Full directive set showcase
  edge_cases.go       Closures, multi-line @must, nested ensure
  generics.go         Type parameters: comparable, any, mixed, expression mode
.inco_cache/        Generated shadow files + overlay.json (git-ignored)
```

## Examples

### Full directive set

```go
func Transfer(from *Account, to *Account, amount int) {
    // @require -nd from, to
    // @require amount > 0, "amount must be positive"

    res, _ := db.Exec(query) // @must

    // @ensure -nd res

    fmt.Printf("Transfer %d\n", amount)
}
```

### Closure with directives

```go
func ProcessWithCallback(db *DB) {
    handler := func(u *User) {
        // @require -nd u
        fmt.Println(u.Name)
    }
    u, _ := db.Query("SELECT 1")
    handler(u)
}
```

### Multi-line @must

```go
func FetchMultiLine(db *DB) *User {
    // @must
    res, _ := db.Query(
        "SELECT * FROM users WHERE id = ?",
    )
    return res
}
```

### Generics with directives

```go
// comparable type param → *new(T) zero check
func FirstNonZero[T comparable](items []T) (result T) {
    // @ensure -nd result
    for _, v := range items {
        return v
    }
    return
}

// any type param → reflect.ValueOf zero check (import auto-added)
func MustNotBeZero[T any](v T) T {
    // @require -nd v
    return v
}

// Expression mode works with type params
func Clamp[N Number](val, lo, hi N) N {
    // @require lo <= hi, "lo must not exceed hi"
    if val < lo { return lo }
    if val > hi { return hi }
    return val
}
```

## Shadow File Features

### `//line` Directives

Generated shadow files include `//line` directives so that panic stack traces and compiler errors point back to the original source file and line numbers. This ensures a seamless debugging experience.

### Static Expression Warnings

During `inco gen`, if a `@require` expression is a compile-time constant that evaluates to `false`, Inco emits a warning to stderr:
```
inco: expression "1 > 2" is always false (compile-time constant) at main.go:10
```

### Auto-Import

When generated code requires additional imports (e.g., `reflect` for non-comparable type params, or cross-package types in struct/array composite literals), the imports are automatically added to the shadow file.
