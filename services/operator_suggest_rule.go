package services

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/grove-platform/github-copier/types"
)

// operatorSuggestRuleRequest is what the operator UI sends when asking the LLM
// to generate a copier rule from a source→target example.
type operatorSuggestRuleRequest struct {
	SourcePath string `json:"source_path"`
	TargetPath string `json:"target_path"`
	TargetRepo string `json:"target_repo,omitempty"` // optional
	SourceRepo string `json:"source_repo,omitempty"` // optional, for context
}

// operatorSuggestRuleResponse is what the handler returns: the generated rule,
// an explanation, and a verification check against the user's example.
type operatorSuggestRuleResponse struct {
	RuleYAML     string `json:"rule_yaml"`
	Explanation  string `json:"explanation,omitempty"`
	Verified     bool   `json:"verified"`                // true if the rule produces target_path from source_path
	ComputedPath string `json:"computed_path,omitempty"` // actual target path the rule would produce
	VerifyError  string `json:"verify_error,omitempty"`  // reason verification failed (if any)
	Warning      string `json:"warning,omitempty"`       // any non-fatal concern
	Error        string `json:"error,omitempty"`
}

// llmSuggestedRule is the structured JSON we ask the LLM to return.
type llmSuggestedRule struct {
	Name           string            `json:"name"`
	DestRepo       string            `json:"destination_repo"`
	DestBranch     string            `json:"destination_branch,omitempty"`
	TransformType  string            `json:"transform_type"` // "move" | "copy" | "glob" | "regex"
	TransformFrom  string            `json:"transform_from,omitempty"`
	TransformTo    string            `json:"transform_to,omitempty"`
	Pattern        string            `json:"pattern,omitempty"`
	TransformTempl string            `json:"transform_template,omitempty"`
	CommitStrategy string            `json:"commit_strategy,omitempty"` // "direct" or "pull_request"
	Explanation    string            `json:"explanation,omitempty"`
	Extra          map[string]string `json:"-"`
}

// handleSuggestRule accepts a source/target pair and asks the configured LLM to
// generate a copier workflow rule that would produce that transformation.
// The generated rule is self-verified against the example before returning.
func (o *operatorUI) handleSuggestRule(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(operatorSuggestRuleResponse{Error: "method not allowed"})
		return
	}
	if o.llm == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(operatorSuggestRuleResponse{
			Error: "LLM client not initialized on server (check startup logs)",
		})
		return
	}

	// Per-user rate limit caps Anthropic token spend. In github mode keyed by
	// hashed PAT; in kanopy mode keyed by hashed login (no PAT available).
	// Either way the raw credential never sits in the bucket map.
	var rlKey string
	if pat := bearerToken(r); pat != "" {
		rlKey = hashToken(pat)
	} else if u := operatorUserFromCtx(r); u != nil {
		rlKey = hashToken(u.Login)
	}
	if rlKey != "" && o.suggestLimiter != nil {
		allowed, resetAt := o.suggestLimiter.Allow(rlKey)
		if !allowed {
			retry := time.Until(resetAt).Round(time.Second)
			w.Header().Set("Retry-After", fmt.Sprintf("%d", int(retry.Seconds())))
			w.WriteHeader(http.StatusTooManyRequests)
			_ = json.NewEncoder(w).Encode(operatorSuggestRuleResponse{
				Error: fmt.Sprintf("rate limit exceeded — try again in %s", retry),
			})
			return
		}
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 4096))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(operatorSuggestRuleResponse{Error: "read body"})
		return
	}
	var req operatorSuggestRuleRequest
	if err := json.Unmarshal(body, &req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(operatorSuggestRuleResponse{Error: "invalid json"})
		return
	}
	req.SourcePath = strings.TrimSpace(req.SourcePath)
	req.TargetPath = strings.TrimSpace(req.TargetPath)
	req.TargetRepo = strings.TrimSpace(req.TargetRepo)
	req.SourceRepo = strings.TrimSpace(req.SourceRepo)
	if req.SourcePath == "" || req.TargetPath == "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(operatorSuggestRuleResponse{Error: "source_path and target_path are required"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()

	suggestion, err := o.askLLMForRule(ctx, req)
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(operatorSuggestRuleResponse{Error: err.Error()})
		return
	}

	ruleYAML := renderRuleYAML(suggestion, req)
	verified, computed, vErr := verifySuggestedRule(suggestion, req.SourcePath, req.TargetPath)

	resp := operatorSuggestRuleResponse{
		RuleYAML:     ruleYAML,
		Explanation:  suggestion.Explanation,
		Verified:     verified,
		ComputedPath: computed,
	}
	if vErr != nil {
		resp.VerifyError = vErr.Error()
	}
	if !verified {
		resp.Warning = "Generated rule did not produce the expected target path from your example. Review and adjust before saving."
	}
	_ = json.NewEncoder(w).Encode(resp)
}

// SuggestRuleSystemPrompt is the system prompt used by the AI rule suggester.
// Exported so cmd/test-llm can exercise the real prompt end-to-end against
// the configured provider (same prompt writers will hit via the UI).
const SuggestRuleSystemPrompt = `You are a configuration generator for GitHub Copier workflows.

Given a single source→target file transformation example, output ONLY a valid JSON object — no markdown, no prose outside the JSON. Generate ONE rule describing ONE transformation.

Transform types (prefer the simplest that works — move > copy > glob > regex):
- "move"  — rename a directory prefix. Matches any file under transform_from; replaces the prefix with transform_to. Use when the source and target share the subpath below the renamed prefix.
- "copy"  — rename ONE exact file. Use when the example is a specific file pair, not a pattern.
- "glob"  — wildcards in pattern (e.g. "dir/**/*.ext"). Use "${relative_path}" in transform_template to preserve subdir structure after the matched prefix.
- "regex" — Go RE2 regex with named captures (e.g. "(?P<name>.+)"). Use ONLY when move/copy/glob cannot express the rename.

Response shape (omit fields that don't apply to the chosen transform_type):
{
  "name": "kebab-case-rule-name",
  "destination_repo": "org/dest-repo",
  "destination_branch": "main",
  "commit_strategy": "pull_request",
  "transform_type": "move" | "copy" | "glob" | "regex",
  "transform_from": "<for move or copy>",
  "transform_to":   "<for move or copy>",
  "pattern":             "<for glob or regex>",
  "transform_template":  "<for glob or regex>",
  "explanation": "one sentence describing what this rule does"
}

Rules:
- destination_branch defaults to "main"; commit_strategy defaults to "pull_request" (use "direct" only if the user's intent is clearly a direct commit).
- If the user did not provide a target repo, use a placeholder like "org/target-repo" so the writer can fill it in.
- name should be short and kebab-case, derived from the source directory or file.
- The rule MUST produce the user's target path when applied to their source path. Verify the logic before responding.

Examples

Input:  source=mflix/server/java-spring/App.java  target=server/App.java  repo=mongodb/sample-app-java-mflix
Output: {"name":"mflix-java-spring-server","destination_repo":"mongodb/sample-app-java-mflix","destination_branch":"main","commit_strategy":"pull_request","transform_type":"move","transform_from":"mflix/server/java-spring","transform_to":"server","explanation":"Renames the mflix/server/java-spring prefix to server when copying into the target repo."}

Input:  source=mflix/README-JAVA-SPRING.md  target=README.md  repo=mongodb/sample-app-java-mflix
Output: {"name":"mflix-readme","destination_repo":"mongodb/sample-app-java-mflix","destination_branch":"main","commit_strategy":"pull_request","transform_type":"copy","transform_from":"mflix/README-JAVA-SPRING.md","transform_to":"README.md","explanation":"Copies one specific README file and renames it in the destination."}

Input:  source=agg/python/models/user.py  target=shared/python/models/user.py  repo=org/shared-examples
Output: {"name":"agg-python","destination_repo":"org/shared-examples","destination_branch":"main","commit_strategy":"pull_request","transform_type":"glob","pattern":"agg/python/**/*.py","transform_template":"shared/python/${relative_path}","explanation":"Matches any .py file under agg/python and preserves the subdirectory structure under shared/python."}

Input:  source=tutorials/v2/getting-started.mdx  target=docs/getting-started-v2.mdx  repo=org/docs-site
Output: {"name":"tutorials-versioned","destination_repo":"org/docs-site","destination_branch":"main","commit_strategy":"pull_request","transform_type":"regex","pattern":"tutorials/v(?P<ver>[0-9]+)/(?P<slug>.+)\\.mdx","transform_template":"docs/${slug}-v${ver}.mdx","explanation":"Extracts version and slug from the source path and rebuilds the target filename with the version as a suffix."}`

// askLLMForRule sends a structured prompt to the LLM and parses the JSON response.
func (o *operatorUI) askLLMForRule(ctx context.Context, req operatorSuggestRuleRequest) (*llmSuggestedRule, error) {
	userPrompt := fmt.Sprintf(`Generate a copier rule for this transformation:

Source file: %s
Target file: %s
Target repo: %s

Return ONLY a JSON object with the fields documented above. No prose outside the JSON.`,
		req.SourcePath, req.TargetPath, defaultIfEmpty(req.TargetRepo, "(user did not specify — use a placeholder like \"org/target-repo\")"))

	raw, err := o.llm.GenerateJSON(ctx, SuggestRuleSystemPrompt, userPrompt)
	if err != nil {
		return nil, fmt.Errorf("LLM error: %w", err)
	}

	var suggestion llmSuggestedRule
	if err := json.Unmarshal([]byte(raw), &suggestion); err != nil {
		return nil, fmt.Errorf("LLM returned invalid JSON: %w (response: %s)", err, truncate(raw, 200))
	}
	suggestion.TransformType = strings.ToLower(strings.TrimSpace(suggestion.TransformType))
	if suggestion.DestRepo == "" && req.TargetRepo != "" {
		suggestion.DestRepo = req.TargetRepo
	}
	if suggestion.DestBranch == "" {
		suggestion.DestBranch = "main"
	}
	if suggestion.CommitStrategy == "" {
		suggestion.CommitStrategy = "pull_request"
	}
	if suggestion.Name == "" {
		suggestion.Name = "generated-rule"
	}
	return &suggestion, nil
}

// verifySuggestedRule tests whether the suggested rule, applied to sourcePath,
// produces targetPath. Returns (matched, computedPath, error).
func verifySuggestedRule(s *llmSuggestedRule, sourcePath, targetPath string) (bool, string, error) {
	transformer := NewPathTransformer()

	switch s.TransformType {
	case "move":
		if s.TransformFrom == "" || s.TransformTo == "" {
			return false, "", fmt.Errorf("move rule missing from/to")
		}
		from := strings.TrimSuffix(s.TransformFrom, "/")
		if !strings.HasPrefix(sourcePath, from) {
			return false, "", fmt.Errorf("source path does not start with %q", from)
		}
		// Boundary check: the prefix must end at a path separator (or be the
		// whole path). Without this, from="agg/py" would falsely "match"
		// sourcePath="agg/python/foo.py" and produce a bogus rel of
		// "thon/foo.py" — verification would pass for a rule that means
		// something entirely different from what the LLM intended.
		rest := sourcePath[len(from):]
		if rest != "" && !strings.HasPrefix(rest, "/") {
			return false, "", fmt.Errorf("source path %q does not match move from %q at a path boundary", sourcePath, from)
		}
		rel := strings.TrimPrefix(rest, "/")
		computed := strings.TrimSuffix(s.TransformTo, "/") + "/" + rel
		computed = strings.TrimSuffix(computed, "/")
		return computed == targetPath, computed, nil

	case "copy":
		if s.TransformFrom == "" || s.TransformTo == "" {
			return false, "", fmt.Errorf("copy rule missing from/to")
		}
		if sourcePath != s.TransformFrom {
			return false, "", fmt.Errorf("source path %q does not equal copy from %q", sourcePath, s.TransformFrom)
		}
		return s.TransformTo == targetPath, s.TransformTo, nil

	case "glob":
		if s.Pattern == "" || s.TransformTempl == "" {
			return false, "", fmt.Errorf("glob rule missing pattern/transform")
		}
		matcher := NewPatternMatcher()
		result := matcher.Match(sourcePath, types.SourcePattern{Type: types.PatternTypeGlob, Pattern: s.Pattern})
		if !result.Matched {
			return false, "", fmt.Errorf("glob pattern %q does not match %q", s.Pattern, sourcePath)
		}
		// Add relative_path (server-side glob transform convention): strip prefix before first wildcard
		vars := result.Variables
		if vars == nil {
			vars = make(map[string]string)
		}
		vars["relative_path"] = computeGlobRelativePath(sourcePath, s.Pattern)
		computed, err := transformer.Transform(sourcePath, s.TransformTempl, vars)
		if err != nil {
			return false, "", fmt.Errorf("apply transform: %w", err)
		}
		return computed == targetPath, computed, nil

	case "regex":
		if s.Pattern == "" || s.TransformTempl == "" {
			return false, "", fmt.Errorf("regex rule missing pattern/transform")
		}
		re, err := regexp.Compile(s.Pattern)
		if err != nil {
			return false, "", fmt.Errorf("invalid regex: %w", err)
		}
		match := re.FindStringSubmatch(sourcePath)
		if match == nil {
			return false, "", fmt.Errorf("regex %q does not match %q", s.Pattern, sourcePath)
		}
		vars := map[string]string{"matched_pattern": s.Pattern}
		for i, name := range re.SubexpNames() {
			if i > 0 && name != "" {
				vars[name] = match[i]
			}
		}
		computed, err := transformer.Transform(sourcePath, s.TransformTempl, vars)
		if err != nil {
			return false, "", fmt.Errorf("apply transform: %w", err)
		}
		return computed == targetPath, computed, nil

	default:
		return false, "", fmt.Errorf("unknown transform type: %q", s.TransformType)
	}
}

// computeGlobRelativePath mirrors the server-side convention: strip the
// longest literal prefix (before the first wildcard) from the source path.
func computeGlobRelativePath(sourcePath, pattern string) string {
	// Find the first wildcard character in the pattern
	idx := strings.IndexAny(pattern, "*?[")
	if idx < 0 {
		return ""
	}
	prefix := pattern[:idx]
	// Trim to the last '/' before the wildcard to get a clean directory prefix
	if slash := strings.LastIndex(prefix, "/"); slash >= 0 {
		prefix = prefix[:slash+1]
	}
	return strings.TrimPrefix(sourcePath, prefix)
}

// renderRuleYAML produces a YAML snippet for the operator UI to display.
func renderRuleYAML(s *llmSuggestedRule, req operatorSuggestRuleRequest) string {
	var sb strings.Builder
	sb.WriteString("- name: \"")
	sb.WriteString(s.Name)
	sb.WriteString("\"\n")
	if req.SourceRepo != "" {
		sb.WriteString("  source:\n")
		sb.WriteString("    repo: \"")
		sb.WriteString(req.SourceRepo)
		sb.WriteString("\"\n")
	}
	sb.WriteString("  destination:\n")
	sb.WriteString("    repo: \"")
	sb.WriteString(s.DestRepo)
	sb.WriteString("\"\n")
	sb.WriteString("    branch: \"")
	sb.WriteString(s.DestBranch)
	sb.WriteString("\"\n")
	sb.WriteString("  transformations:\n")
	switch s.TransformType {
	case "move":
		fmt.Fprintf(&sb, "    - move: { from: %q, to: %q }\n", s.TransformFrom, s.TransformTo)
	case "copy":
		fmt.Fprintf(&sb, "    - copy: { from: %q, to: %q }\n", s.TransformFrom, s.TransformTo)
	case "glob":
		sb.WriteString("    - glob:\n")
		fmt.Fprintf(&sb, "        pattern: %q\n", s.Pattern)
		fmt.Fprintf(&sb, "        transform: %q\n", s.TransformTempl)
	case "regex":
		sb.WriteString("    - regex:\n")
		fmt.Fprintf(&sb, "        pattern: %q\n", s.Pattern)
		fmt.Fprintf(&sb, "        transform: %q\n", s.TransformTempl)
	}
	sb.WriteString("  commit_strategy:\n")
	sb.WriteString("    type: \"")
	sb.WriteString(s.CommitStrategy)
	sb.WriteString("\"\n")
	return sb.String()
}

func defaultIfEmpty(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// truncate shortens s to at most n runes, appending an ellipsis when cut.
// Rune-aware (not byte-aware) so multi-byte glyphs in LLM output aren't
// cut in half when we truncate for logging.
func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}
