package test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/oplog"
)

var scrubVerifyEnv = []string{"CLAUDE_CODE_SESSION_ID=scrub-verify-test"}

// writeVerifyRecipe writes a scrub recipe naming one operation per pattern, in
// the EXACT format `scrub run` takes -- every operation carries a `replace`,
// because that is what the format requires, and `scrub verify` ignores it.
//
// The file is written OUTSIDE the repository under test: a scrub refuses a
// dirty working tree, and an untracked recipe inside the repo would be one.
func writeVerifyRecipe(t *testing.T, patterns ...string) string {
	t.Helper()
	var b strings.Builder
	for _, p := range patterns {
		b.WriteString("[[operations]]\npattern = \"" + p + "\"\nreplace = \"REDACTED\"\n\n")
	}
	path := filepath.Join(t.TempDir(), "recipe.toml")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestScrubVerifyWithoutInputIsAnError pins the whole point of the stateless
// surface: a verification that names nothing has nothing to verify, and must
// never be answerable with a clean bill of health. The old command answered a
// bare `scrub verify` with exit 0 and "No scrub policies found".
func TestScrubVerifyWithoutInputIsAnError(t *testing.T) {
	dir := newRepo(t)

	stdout, stderr, code := runSafegitEnv(t, dir, scrubVerifyEnv, "scrub", "verify")
	if code == 0 {
		t.Fatalf("scrub verify with no input must not exit 0\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
	if !strings.Contains(stderr, "pattern") {
		t.Errorf("the refusal must name --pattern; stderr was:\n%s", stderr)
	}
	if strings.Contains(stdout, "No scrub policies found") {
		t.Errorf("the vacuous no-policies pass must be gone; stdout was:\n%s", stdout)
	}
}

// TestScrubVerifyScopeAloneIsAnError pins that --scope is a modifier on
// --pattern and can never be the only input: a scope with no pattern would
// otherwise read as a verification of nothing.
func TestScrubVerifyScopeAloneIsAnError(t *testing.T) {
	dir := newRepo(t)

	_, stderr, code := runSafegitEnv(t, dir, scrubVerifyEnv, "scrub", "verify", "--scope", "*.env")
	if code == 0 {
		t.Fatalf("scrub verify --scope with no pattern must not exit 0; stderr:\n%s", stderr)
	}
}

// TestScrubVerifyPatternPassesOnACleanStore verifies that a pattern absent from
// every object passes and exits 0.
func TestScrubVerifyPatternPassesOnACleanStore(t *testing.T) {
	dir := newRepo(t)
	commitFileEnv(t, dir, scrubVerifyEnv, "config.txt", "password=hunter2\n", "add config")

	stdout, stderr, code := runSafegitEnv(t, dir, scrubVerifyEnv,
		"scrub", "verify", "--pattern", "NEVER_EXISTED_TOKEN")
	if code != 0 {
		t.Fatalf("verify of an absent pattern failed (code %d)\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "PASS") {
		t.Errorf("expected PASS in stdout, got:\n%s", stdout)
	}
	if !strings.Contains(stdout, "1 passed") {
		t.Errorf("expected '1 passed' in stdout, got:\n%s", stdout)
	}
}

// TestScrubVerifyPatternDetectsResurrection scrubs a secret, confirms the store
// is clean, then re-introduces the secret in a new commit and confirms the
// --pattern form reports it and exits nonzero.
func TestScrubVerifyPatternDetectsResurrection(t *testing.T) {
	dir := newRepo(t)
	const secret = "SUPER_SECRET_42"
	commitFileEnv(t, dir, scrubVerifyEnv, "config.txt", "token="+secret+"\n", "add secret")

	if _, stderr, code := runSafegitEnv(t, dir, scrubVerifyEnv,
		"--approve-consequential", "scrub", "match",
		"--pattern", secret, "--replace", "REDACTED",
		"--reason", "remove token", "--entire-history",
	); code != 0 {
		t.Fatalf("scrub match failed (code %d): %s", code, stderr)
	}

	if _, stderr, code := runSafegitEnv(t, dir, scrubVerifyEnv,
		"scrub", "verify", "--pattern", secret); code != 0 {
		t.Fatalf("verify must pass immediately after the scrub; stderr:\n%s", stderr)
	}

	commitFileEnv(t, dir, scrubVerifyEnv, "config.txt", "token="+secret+"\n", "oops reintroduced")

	stdout, stderr, code := runSafegitEnv(t, dir, scrubVerifyEnv,
		"scrub", "verify", "--pattern", secret)
	if code == 0 {
		t.Fatalf("verify must fail after the secret came back\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
	if !strings.Contains(stderr, "FAIL") {
		t.Errorf("expected FAIL in stderr, got:\n%s", stderr)
	}
}

// TestScrubVerifyRecipeDetectsResurrection is the same property through the
// other input member: the recipe file `scrub run` already takes, read
// unchanged, with its replace field present and ignored.
func TestScrubVerifyRecipeDetectsResurrection(t *testing.T) {
	dir := newRepo(t)
	const secret = "RECIPE_SECRET_77"
	commitFileEnv(t, dir, scrubVerifyEnv, "config.txt", "token="+secret+"\n", "add secret")

	recipe := writeVerifyRecipe(t, secret)

	stdout, stderr, code := runSafegitEnv(t, dir, scrubVerifyEnv, "scrub", "verify", recipe)
	if code == 0 {
		t.Fatalf("recipe verify must fail while the secret is present\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
	if !strings.Contains(stderr, "FAIL") {
		t.Errorf("expected FAIL in stderr, got:\n%s", stderr)
	}

	if _, stderr, code := runSafegitEnv(t, dir, scrubVerifyEnv,
		"--approve-consequential", "scrub", "match",
		"--pattern", secret, "--replace", "REDACTED",
		"--reason", "remove token", "--entire-history",
	); code != 0 {
		t.Fatalf("scrub match failed (code %d): %s", code, stderr)
	}

	// The recipe file itself is an untracked working-tree file, so it is not in
	// the object store and cannot be what the scan finds.
	stdout, stderr, code = runSafegitEnv(t, dir, scrubVerifyEnv, "scrub", "verify", recipe)
	if code != 0 {
		t.Fatalf("recipe verify must pass after the scrub (code %d)\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
}

// TestScrubVerifyRecipeChecksEveryOperation pins that a multi-operation recipe
// verifies each of its operations, not just the first.
func TestScrubVerifyRecipeChecksEveryOperation(t *testing.T) {
	dir := newRepo(t)
	commitFileEnv(t, dir, scrubVerifyEnv, "a.txt", "alpha_secret_one\n", "add a")

	recipe := writeVerifyRecipe(t, "beta_secret_two", "alpha_secret_one")

	stdout, _, code := runSafegitEnv(t, dir, scrubVerifyEnv, "--json", "scrub", "verify", recipe)
	if code == 0 {
		t.Fatal("verify must fail: the second operation's pattern is still present")
	}
	var result struct {
		Patterns int `json:"patterns"`
		Passed   int `json:"passed"`
		Failed   int `json:"failed"`
		Results  []struct {
			Pattern string `json:"pattern"`
			Source  string `json:"source"`
			Pass    bool   `json:"pass"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(jsonPayload(t, stdout)), &result); err != nil {
		t.Fatalf("parsing the payload: %v\nstdout:\n%s", err, stdout)
	}
	if result.Patterns != 2 || result.Passed != 1 || result.Failed != 1 {
		t.Errorf("patterns/passed/failed = %d/%d/%d, want 2/1/1", result.Patterns, result.Passed, result.Failed)
	}
	for _, r := range result.Results {
		if r.Source != "recipe" {
			t.Errorf("pattern %q source = %q, want recipe", r.Pattern, r.Source)
		}
	}
}

// TestScrubVerifyScopeLimitsWhatCounts pins that --scope narrows a --pattern to
// the blob paths it matches: the same secret is a violation inside the scope and
// not one outside it.
func TestScrubVerifyScopeLimitsWhatCounts(t *testing.T) {
	dir := newRepo(t)
	const secret = "SCOPED_SECRET_31"
	commitFileEnv(t, dir, scrubVerifyEnv, "notes.txt", "value="+secret+"\n", "add notes")

	if _, _, code := runSafegitEnv(t, dir, scrubVerifyEnv,
		"scrub", "verify", "--pattern", secret, "--scope", "*.env"); code != 0 {
		t.Error("a secret that lives only outside the scope must not be a violation")
	}
	if _, _, code := runSafegitEnv(t, dir, scrubVerifyEnv,
		"scrub", "verify", "--pattern", secret, "--scope", "*.txt"); code == 0 {
		t.Error("a secret inside the scope must be a violation")
	}
}

// TestScrubVerifyBothInputsAreVerified pins that the input selection is
// at-least-one and not exactly-one: giving a --pattern and a recipe checks all
// of them in one run.
func TestScrubVerifyBothInputsAreVerified(t *testing.T) {
	dir := newRepo(t)
	commitFileEnv(t, dir, scrubVerifyEnv, "a.txt", "nothing interesting\n", "add a")

	recipe := writeVerifyRecipe(t, "FROM_RECIPE_ABSENT")

	stdout, _, code := runSafegitEnv(t, dir, scrubVerifyEnv, "--json",
		"scrub", "verify", "--pattern", "FROM_FLAG_ABSENT", recipe)
	if code != 0 {
		t.Fatalf("both patterns are absent, so the run must pass; stdout:\n%s", stdout)
	}
	var result struct {
		Patterns int `json:"patterns"`
		Passed   int `json:"passed"`
		Results  []struct {
			Pattern string `json:"pattern"`
			Source  string `json:"source"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(jsonPayload(t, stdout)), &result); err != nil {
		t.Fatalf("parsing the payload: %v\nstdout:\n%s", err, stdout)
	}
	if result.Patterns != 2 || result.Passed != 2 {
		t.Fatalf("patterns/passed = %d/%d, want 2/2", result.Patterns, result.Passed)
	}
	sources := map[string]string{}
	for _, r := range result.Results {
		sources[r.Pattern] = r.Source
	}
	if sources["FROM_FLAG_ABSENT"] != "flag" {
		t.Errorf("flag-supplied pattern source = %q, want flag", sources["FROM_FLAG_ABSENT"])
	}
	if sources["FROM_RECIPE_ABSENT"] != "recipe" {
		t.Errorf("recipe-supplied pattern source = %q, want recipe", sources["FROM_RECIPE_ABSENT"])
	}
}

// TestScrubVerifyPayloadShape pins the redefined payload: per-pattern records
// keyed by `patterns` rather than `policies`, and NO `reason` member anywhere --
// the field the policy store carried and the stateless command cannot know.
func TestScrubVerifyPayloadShape(t *testing.T) {
	dir := newRepo(t)
	commitFileEnv(t, dir, scrubVerifyEnv, "a.txt", "plain\n", "add a")

	stdout, stderr, code := runSafegitEnv(t, dir, scrubVerifyEnv, "--json",
		"scrub", "verify", "--pattern", "ABSENT_TOKEN")
	if code != 0 {
		t.Fatalf("verify failed (code %d): %s", code, stderr)
	}
	payload := jsonPayload(t, stdout)

	var raw map[string]interface{}
	if err := json.Unmarshal([]byte(payload), &raw); err != nil {
		t.Fatalf("parsing the payload: %v\npayload:\n%s", err, payload)
	}
	if _, ok := raw["policies"]; ok {
		t.Error("the payload must no longer carry a `policies` count")
	}
	if raw["patterns"] != float64(1) {
		t.Errorf("patterns = %v, want 1", raw["patterns"])
	}
	results, ok := raw["results"].([]interface{})
	if !ok || len(results) != 1 {
		t.Fatalf("results = %v, want one record", raw["results"])
	}
	record := results[0].(map[string]interface{})
	if _, ok := record["reason"]; ok {
		t.Error("the per-pattern record must no longer carry a `reason`")
	}
	if record["pattern"] != "ABSENT_TOKEN" || record["source"] != "flag" || record["pass"] != true {
		t.Errorf("record = %v", record)
	}
}

// TestScrubMatchWritesNoPolicyFile pins that the store is gone: a scrub creates
// no scrub-policies.jsonl for anything to read back.
func TestScrubMatchWritesNoPolicyFile(t *testing.T) {
	dir := newRepo(t)
	commitFileEnv(t, dir, scrubVerifyEnv, "c.txt", "k=POLICY_STORE_GONE\n", "add secret")

	if _, stderr, code := runSafegitEnv(t, dir, scrubVerifyEnv,
		"--approve-consequential", "scrub", "match",
		"--pattern", "POLICY_STORE_GONE", "--replace", "REDACTED",
		"--reason", "no store", "--entire-history",
	); code != 0 {
		t.Fatalf("scrub match failed (code %d): %s", code, stderr)
	}

	policyPath := filepath.Join(dir, ".git", "safegit", "scrub-policies.jsonl")
	if _, err := os.Stat(policyPath); err == nil {
		t.Errorf("%s exists; the policy store was deleted and nothing may write it", policyPath)
	}
}

// TestScrubMatchOplogRecordsNoContent pins the retention rule the oplog now
// keeps: metadata only. Neither the pattern nor the replacement text may appear
// anywhere in the oplog line, while the metadata that IS kept -- op, reason,
// scope, mode, counts -- is still there.
func TestScrubMatchOplogRecordsNoContent(t *testing.T) {
	dir := newRepo(t)
	const secret = "OPLOG_LEAK_CANDIDATE_88"
	const replacement = "ROTATED_VALUE_99"
	commitFileEnv(t, dir, scrubVerifyEnv, "conf/app.env", "k="+secret+"\n", "add secret")

	if _, stderr, code := runSafegitEnv(t, dir, scrubVerifyEnv,
		"--approve-consequential", "scrub", "match",
		"--pattern", secret, "--replace", replacement,
		"--scope", "conf/**",
		"--reason", "retention test", "--entire-history",
	); code != 0 {
		t.Fatalf("scrub match failed (code %d): %s", code, stderr)
	}

	sgDir := filepath.Join(dir, ".git", "safegit")
	raw, err := os.ReadFile(oplog.Path(sgDir))
	if err != nil {
		t.Fatalf("reading the oplog: %v", err)
	}
	if strings.Contains(string(raw), secret) {
		t.Errorf("the oplog contains the scrub pattern verbatim:\n%s", raw)
	}
	if strings.Contains(string(raw), replacement) {
		t.Errorf("the oplog contains the replacement text verbatim:\n%s", raw)
	}

	entries, skipped, err := oplog.Read(sgDir)
	if err != nil {
		t.Fatalf("reading the oplog: %v", err)
	}
	if skipped != 0 {
		t.Fatalf("%d unparseable oplog line(s)", skipped)
	}
	var scrubEntry *oplog.Entry
	for i := range entries {
		if entries[i].Op == "scrub-match" {
			scrubEntry = &entries[i]
		}
	}
	if scrubEntry == nil {
		t.Fatal("no scrub-match oplog entry was written")
	}
	if _, ok := scrubEntry.Extra["pattern"]; ok {
		t.Error("the oplog entry still carries extra.pattern")
	}
	if _, ok := scrubEntry.Extra["replace"]; ok {
		t.Error("the oplog entry still carries extra.replace")
	}
	// The metadata the entry DOES keep. reason in particular must stay: it is
	// what the oplog's own no-cap coverage uses as its oversized carrier.
	if scrubEntry.Extra["reason"] != "retention test" {
		t.Errorf("extra.reason = %v, want the --reason text", scrubEntry.Extra["reason"])
	}
	if scrubEntry.Extra["scope"] != "conf/**" {
		t.Errorf("extra.scope = %v, want conf/**", scrubEntry.Extra["scope"])
	}
	if scrubEntry.Extra["mode"] != "replace" {
		t.Errorf("extra.mode = %v, want replace", scrubEntry.Extra["mode"])
	}
}

// TestScrubOutputCarriesTheRotationNotice pins that a completed scrub says the
// thing it cannot do -- un-leak a pushed secret -- and hands back the exact
// re-check command.
func TestScrubOutputCarriesTheRotationNotice(t *testing.T) {
	dir := newRepo(t)
	const secret = "ROTATE_ME_TOKEN_5"
	commitFileEnv(t, dir, scrubVerifyEnv, "c.txt", "k="+secret+"\n", "add secret")

	stdout, stderr, code := runSafegitEnv(t, dir, scrubVerifyEnv,
		"--approve-consequential", "scrub", "match",
		"--pattern", secret, "--replace", "REDACTED",
		"--reason", "rotation notice", "--entire-history",
	)
	if code != 0 {
		t.Fatalf("scrub match failed (code %d): %s", code, stderr)
	}
	for _, want := range []string{
		"Rotate the credential",
		"does not un-leak",
		"safegit scrub verify --pattern '" + secret + "'",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("scrub output must contain %q; stdout was:\n%s", want, stdout)
		}
	}
}

// TestScrubOutputCarriesTheScopeLine pins that a completed scrub states its
// actual scope: which ref's history it walked, and that every other ref was
// left alone. Without it an operator reads "Scrub complete" as "the secret is
// gone from this repository", when a branch the walk never visited still holds
// it.
func TestScrubOutputCarriesTheScopeLine(t *testing.T) {
	dir := newRepo(t)
	const secret = "SCOPE_LINE_TOKEN_1"
	commitFileEnv(t, dir, scrubVerifyEnv, "c.txt", "k="+secret+"\n", "add secret")

	stdout, stderr, code := runSafegitEnv(t, dir, scrubVerifyEnv,
		"--approve-consequential", "scrub", "match",
		"--pattern", secret, "--replace", "REDACTED",
		"--reason", "scope line", "--entire-history",
	)
	if code != 0 {
		t.Fatalf("scrub match failed (code %d): %s", code, stderr)
	}
	if !strings.Contains(stdout, "Scope:") {
		t.Errorf("scrub output must carry a scope line; stdout was:\n%s", stdout)
	}
	if !strings.Contains(stdout, "refs/heads/main") {
		t.Errorf("the scope line must name the rewritten ref refs/heads/main; stdout was:\n%s", stdout)
	}
	if !strings.Contains(stdout, "other refs were not rewritten") {
		t.Errorf("the scope line must say other refs were not rewritten; stdout was:\n%s", stdout)
	}
}

// TestScrubFileOutputCarriesTheScopeLine is the `scrub file` twin.
func TestScrubFileOutputCarriesTheScopeLine(t *testing.T) {
	dir := newRepo(t)
	commitFileEnv(t, dir, scrubVerifyEnv, "keep.txt", "keep\n", "seed")
	commitFileEnv(t, dir, scrubVerifyEnv, "secret.env", "TOKEN=abc\n", "add secret file")

	stdout, stderr, code := runSafegitEnv(t, dir, scrubVerifyEnv,
		"--approve-consequential", "scrub", "file",
		"secret.env", "--delete",
		"--reason", "scope line", "--entire-history",
	)
	if code != 0 {
		t.Fatalf("scrub file failed (code %d): %s", code, stderr)
	}
	if !strings.Contains(stdout, "Scope:") || !strings.Contains(stdout, "refs/heads/main") {
		t.Errorf("scrub file output must carry a scope line naming refs/heads/main; stdout was:\n%s", stdout)
	}
}

// TestDoctorReportsAndFixesALegacyPolicyFile pins the migration path for a
// repository scrubbed by an older safegit: the leftover file is an ERROR naming
// what it contains, and --action fix deletes it.
func TestDoctorReportsAndFixesALegacyPolicyFile(t *testing.T) {
	dir := newRepo(t)

	// Auto-initialize .git/safegit before planting the file.
	runSafegitEnv(t, dir, scrubVerifyEnv, "config", "show")

	policyPath := filepath.Join(dir, ".git", "safegit", "scrub-policies.jsonl")
	line := `{"type":"match","pattern":"LEGACY_SECRET_PATTERN","reason":"old scrub","created_at":"2026-01-01T00:00:00Z"}` + "\n"
	if err := os.WriteFile(policyPath, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, _, _ := runSafegitEnv(t, dir, scrubVerifyEnv, "doctor", "--action", "diagnose")
	if !strings.Contains(stdout, "[FAIL] legacy_scrub_policies") {
		t.Errorf("doctor must report the leftover policy file as an error; stdout was:\n%s", stdout)
	}
	if !strings.Contains(stdout, "scrub pattern") {
		t.Errorf("the finding must name what the file contains; stdout was:\n%s", stdout)
	}
	if _, err := os.Stat(policyPath); err != nil {
		t.Fatal("diagnose must not delete anything")
	}

	stdout, _, _ = runSafegitEnv(t, dir, scrubVerifyEnv, "doctor", "--action", "fix")
	if !strings.Contains(stdout, "removed legacy scrub-policy file") {
		t.Errorf("--action fix must say it removed the file; stdout was:\n%s", stdout)
	}
	if _, err := os.Stat(policyPath); !os.IsNotExist(err) {
		t.Errorf("--action fix must delete %s (stat err: %v)", policyPath, err)
	}
}
