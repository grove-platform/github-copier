package services

import "testing"

// verifySuggestedRule is the invariant the AI suggester relies on: every
// generated rule must produce the user's target path when applied to their
// source path. If it doesn't, the UI surfaces a "not verified" warning
// instead of silently showing a broken rule.
//
// These tests assert that invariant holds for each transform type.

func TestVerifySuggestedRule_Move(t *testing.T) {
	t.Run("matching prefix rename", func(t *testing.T) {
		s := &llmSuggestedRule{
			TransformType: "move",
			TransformFrom: "agg/python",
			TransformTo:   "shared/python",
		}
		ok, computed, err := verifySuggestedRule(s, "agg/python/models/user.py", "shared/python/models/user.py")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !ok {
			t.Fatalf("expected match; computed=%q", computed)
		}
		if computed != "shared/python/models/user.py" {
			t.Errorf("computed=%q", computed)
		}
	})
	t.Run("source doesn't start with from", func(t *testing.T) {
		s := &llmSuggestedRule{
			TransformType: "move",
			TransformFrom: "agg/python",
			TransformTo:   "shared/python",
		}
		ok, _, err := verifySuggestedRule(s, "other/path.py", "shared/python/other/path.py")
		if err == nil || ok {
			t.Fatalf("want error and no match; got ok=%v err=%v", ok, err)
		}
	})
	t.Run("target mismatch", func(t *testing.T) {
		s := &llmSuggestedRule{
			TransformType: "move",
			TransformFrom: "agg/python",
			TransformTo:   "shared/python",
		}
		ok, _, err := verifySuggestedRule(s, "agg/python/x.py", "different/target.py")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ok {
			t.Fatalf("want verification failure for target mismatch")
		}
	})
}

func TestVerifySuggestedRule_Copy(t *testing.T) {
	t.Run("exact file rename", func(t *testing.T) {
		s := &llmSuggestedRule{
			TransformType: "copy",
			TransformFrom: "mflix/README-JAVA-SPRING.md",
			TransformTo:   "README.md",
		}
		ok, _, err := verifySuggestedRule(s, "mflix/README-JAVA-SPRING.md", "README.md")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !ok {
			t.Fatalf("want match")
		}
	})
	t.Run("source doesn't equal from", func(t *testing.T) {
		s := &llmSuggestedRule{
			TransformType: "copy",
			TransformFrom: "mflix/README.md",
			TransformTo:   "README.md",
		}
		ok, _, err := verifySuggestedRule(s, "different/README.md", "README.md")
		if err == nil || ok {
			t.Fatalf("want error and no match; got ok=%v err=%v", ok, err)
		}
	})
}

func TestVerifySuggestedRule_Glob(t *testing.T) {
	t.Run("wildcard with relative_path", func(t *testing.T) {
		s := &llmSuggestedRule{
			TransformType:  "glob",
			Pattern:        "agg/python/**/*.py",
			TransformTempl: "shared/python/${relative_path}",
		}
		ok, computed, err := verifySuggestedRule(s, "agg/python/models/user.py", "shared/python/models/user.py")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !ok {
			t.Fatalf("expected match; computed=%q", computed)
		}
	})
	t.Run("pattern doesn't match source", func(t *testing.T) {
		s := &llmSuggestedRule{
			TransformType:  "glob",
			Pattern:        "agg/python/**/*.py",
			TransformTempl: "shared/python/${relative_path}",
		}
		ok, _, err := verifySuggestedRule(s, "not-matching/file.txt", "shared/python/file.txt")
		if err == nil || ok {
			t.Fatalf("want error; got ok=%v err=%v", ok, err)
		}
	})
}

func TestVerifySuggestedRule_Regex(t *testing.T) {
	t.Run("named captures in template", func(t *testing.T) {
		s := &llmSuggestedRule{
			TransformType:  "regex",
			Pattern:        `tutorials/v(?P<ver>[0-9]+)/(?P<slug>.+)\.mdx`,
			TransformTempl: "docs/${slug}-v${ver}.mdx",
		}
		ok, computed, err := verifySuggestedRule(s, "tutorials/v2/getting-started.mdx", "docs/getting-started-v2.mdx")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !ok {
			t.Fatalf("expected match; computed=%q", computed)
		}
	})
	t.Run("invalid regex", func(t *testing.T) {
		s := &llmSuggestedRule{
			TransformType:  "regex",
			Pattern:        `[unclosed`,
			TransformTempl: "anything",
		}
		ok, _, err := verifySuggestedRule(s, "src", "dst")
		if err == nil || ok {
			t.Fatalf("want error for invalid regex; got ok=%v err=%v", ok, err)
		}
	})
}

func TestVerifySuggestedRule_UnknownType(t *testing.T) {
	s := &llmSuggestedRule{TransformType: "symlink"}
	ok, _, err := verifySuggestedRule(s, "a", "b")
	if err == nil || ok {
		t.Fatalf("want error for unknown type; got ok=%v err=%v", ok, err)
	}
}

func TestTruncate_RuneSafe(t *testing.T) {
	// ASCII path still works
	if got := truncate("hello world", 5); got != "hello…" {
		t.Errorf("ascii: got %q", got)
	}
	// Multi-byte runes must not be cut mid-byte
	s := "日本語テスト" // 6 runes, 18 bytes
	got := truncate(s, 3)
	if got != "日本語…" {
		t.Errorf("multibyte: got %q (len=%d)", got, len(got))
	}
	// Short input returned unchanged, no ellipsis
	if got := truncate("hi", 5); got != "hi" {
		t.Errorf("short input changed: %q", got)
	}
}
