package inco

import (
	"testing"
)

func TestParseDirective_RequireND(t *testing.T) {
	tests := []struct {
		input    string
		wantVars []string
	}{
		{"// @require -nd x", []string{"x"}},
		{"// @require -nd x, y", []string{"x", "y"}},
		{"// @require -nd   a ,  b , c ", []string{"a", "b", "c"}},
		{"  // @require -nd ptr  ", []string{"ptr"}},
	}
	for _, tt := range tests {
		d := ParseDirective(tt.input)
		if d == nil {
			t.Fatalf("ParseDirective(%q) = nil, want directive", tt.input)
		}
		if d.Kind != Require {
			t.Errorf("Kind = %v, want Require", d.Kind)
		}
		if !d.ND {
			t.Errorf("ND = false, want true")
		}
		if len(d.Vars) != len(tt.wantVars) {
			t.Errorf("Vars = %v, want %v", d.Vars, tt.wantVars)
			continue
		}
		for i, v := range d.Vars {
			if v != tt.wantVars[i] {
				t.Errorf("Vars[%d] = %q, want %q", i, v, tt.wantVars[i])
			}
		}
	}
}

func TestParseDirective_RequireExpr(t *testing.T) {
	tests := []struct {
		input   string
		expr    string
		message string
	}{
		{"// @require len(x) > 0", "len(x) > 0", ""},
		{`// @require age > 0, "age must be positive"`, "age > 0", "age must be positive"},
		{"// @require a > b", "a > b", ""},
		{`// @require x != nil, "x required"`, "x != nil", "x required"},
	}
	for _, tt := range tests {
		d := ParseDirective(tt.input)
		if d == nil {
			t.Fatalf("ParseDirective(%q) = nil", tt.input)
		}
		if d.Kind != Require {
			t.Errorf("Kind = %v, want Require", d.Kind)
		}
		if d.ND {
			t.Errorf("ND = true, want false")
		}
		if d.Expr != tt.expr {
			t.Errorf("Expr = %q, want %q", d.Expr, tt.expr)
		}
		if d.Message != tt.message {
			t.Errorf("Message = %q, want %q", d.Message, tt.message)
		}
	}
}

func TestParseDirective_EnsureND(t *testing.T) {
	d := ParseDirective("// @ensure -nd result")
	if d == nil {
		t.Fatal("ParseDirective returned nil")
	}
	if d.Kind != Ensure {
		t.Errorf("Kind = %v, want Ensure", d.Kind)
	}
	if !d.ND {
		t.Error("ND = false, want true")
	}
	if len(d.Vars) != 1 || d.Vars[0] != "result" {
		t.Errorf("Vars = %v, want [result]", d.Vars)
	}
}

func TestParseDirective_EnsureExpr(t *testing.T) {
	d := ParseDirective(`// @ensure result != nil, "must return value"`)
	if d == nil {
		t.Fatal("ParseDirective returned nil")
	}
	if d.Kind != Ensure {
		t.Errorf("Kind = %v, want Ensure", d.Kind)
	}
	if d.Expr != "result != nil" {
		t.Errorf("Expr = %q, want %q", d.Expr, "result != nil")
	}
	if d.Message != "must return value" {
		t.Errorf("Message = %q, want %q", d.Message, "must return value")
	}
}

func TestParseDirective_Must(t *testing.T) {
	for _, input := range []string{"// @must", "  // @must  ", "/* @must */"} {
		d := ParseDirective(input)
		if d == nil {
			t.Fatalf("ParseDirective(%q) = nil", input)
		}
		if d.Kind != Must {
			t.Errorf("Kind = %v, want Must for %q", d.Kind, input)
		}
	}
}

func TestParseDirective_BlockComment(t *testing.T) {
	d := ParseDirective("/* @require -nd db */")
	if d == nil {
		t.Fatal("ParseDirective returned nil for block comment")
	}
	if d.Kind != Require || !d.ND {
		t.Errorf("got Kind=%v ND=%v, want Require/true", d.Kind, d.ND)
	}
	if len(d.Vars) != 1 || d.Vars[0] != "db" {
		t.Errorf("Vars = %v, want [db]", d.Vars)
	}
}

func TestParseDirective_NotADirective(t *testing.T) {
	inputs := []string{
		"// regular comment",
		"// @unknown directive",
		"x + y",
		"",
		"// just some text",
	}
	for _, input := range inputs {
		d := ParseDirective(input)
		if d != nil {
			t.Errorf("ParseDirective(%q) = %+v, want nil", input, d)
		}
	}
}

func TestParseDirective_NDNoVars(t *testing.T) {
	d := ParseDirective("// @require -nd")
	if d == nil {
		t.Fatal("ParseDirective returned nil")
	}
	if !d.ND {
		t.Error("ND = false, want true")
	}
	if len(d.Vars) != 0 {
		t.Errorf("Vars = %v, want empty", d.Vars)
	}
}

func TestParseDirective_EmptyRequire(t *testing.T) {
	d := ParseDirective("// @require")
	if d == nil {
		t.Fatal("ParseDirective returned nil")
	}
	if d.Kind != Require {
		t.Errorf("Kind = %v, want Require", d.Kind)
	}
	if d.ND || d.Expr != "" || len(d.Vars) != 0 {
		t.Errorf("unexpected fields: ND=%v Expr=%q Vars=%v", d.ND, d.Expr, d.Vars)
	}
}

func TestKindString(t *testing.T) {
	tests := []struct {
		k    Kind
		want string
	}{
		{Require, "require"},
		{Ensure, "ensure"},
		{Must, "must"},
		{Kind(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.k.String(); got != tt.want {
			t.Errorf("Kind(%d).String() = %q, want %q", tt.k, got, tt.want)
		}
	}
}

func TestParseDirective_ExprWithCommaNoMessage(t *testing.T) {
	d := ParseDirective("// @require f(a, b) > 0")
	if d == nil {
		t.Fatal("ParseDirective returned nil")
	}
	if d.Expr != "f(a, b) > 0" {
		t.Errorf("Expr = %q, want %q", d.Expr, "f(a, b) > 0")
	}
	if d.Message != "" {
		t.Errorf("Message = %q, want empty", d.Message)
	}
}

func TestParseDirective_ExprWithCommaAndMessage(t *testing.T) {
	d := ParseDirective(`// @require f(a, b) > 0, "must be positive"`)
	if d == nil {
		t.Fatal("ParseDirective returned nil")
	}
	if d.Expr != "f(a, b) > 0" {
		t.Errorf("Expr = %q, want %q", d.Expr, "f(a, b) > 0")
	}
	if d.Message != "must be positive" {
		t.Errorf("Message = %q, want %q", d.Message, "must be positive")
	}
}
