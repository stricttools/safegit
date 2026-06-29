package trailer

import (
	"os"
	"strings"
	"testing"
)

func TestInject_EnvAbsent(t *testing.T) {
	os.Unsetenv(envVar)
	msg := "some commit message"
	got := Inject(msg)
	if got != msg {
		t.Errorf("expected message unchanged, got %q", got)
	}
}

func TestInject_EnvPresent(t *testing.T) {
	t.Setenv(envVar, "session-abc-123")
	msg := "add new feature"
	got := Inject(msg)

	if !strings.Contains(got, "Claude-Code-Session-Id: session-abc-123") {
		t.Errorf("expected trailer in output, got %q", got)
	}

	// Should have blank line separator between subject and trailer
	if !strings.Contains(got, "\n\nClaude-Code-Session-Id: session-abc-123\n") {
		t.Errorf("expected blank line before trailer, got %q", got)
	}
}

func TestInject_Dedup_SameSession(t *testing.T) {
	t.Setenv(envVar, "session-abc-123")
	msg := "add new feature\n\nClaude-Code-Session-Id: session-abc-123\n"
	got := Inject(msg)
	if got != msg {
		t.Errorf("expected message unchanged (dedup), got %q", got)
	}
}

func TestInject_DifferentSession_BothPresent(t *testing.T) {
	t.Setenv(envVar, "session-def-456")
	msg := "add new feature\n\nClaude-Code-Session-Id: session-abc-123\n"
	got := Inject(msg)

	if !strings.Contains(got, "Claude-Code-Session-Id: session-abc-123") {
		t.Errorf("expected original trailer preserved, got %q", got)
	}
	if !strings.Contains(got, "Claude-Code-Session-Id: session-def-456") {
		t.Errorf("expected new trailer added, got %q", got)
	}

	// Both trailers should be in the same block (no extra blank line between them)
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	// Last two lines should be the two trailers
	if len(lines) < 2 {
		t.Fatalf("expected at least 2 lines, got %d", len(lines))
	}
	last := lines[len(lines)-1]
	secondLast := lines[len(lines)-2]
	if !strings.HasPrefix(last, "Claude-Code-Session-Id:") || !strings.HasPrefix(secondLast, "Claude-Code-Session-Id:") {
		t.Errorf("expected last two lines to be trailers, got %q and %q", secondLast, last)
	}
}

func TestInject_MultiLineBody(t *testing.T) {
	t.Setenv(envVar, "session-xyz")
	msg := "subject line\n\nThis is the body.\nIt has multiple lines."
	got := Inject(msg)

	// Trailer should be after a blank line following the body
	expected := "subject line\n\nThis is the body.\nIt has multiple lines.\n\nClaude-Code-Session-Id: session-xyz\n"
	if got != expected {
		t.Errorf("expected:\n%q\ngot:\n%q", expected, got)
	}
}

func TestInject_ExistingOtherTrailers(t *testing.T) {
	t.Setenv(envVar, "session-xyz")
	msg := "subject line\n\nSigned-off-by: Test User <test@test.com>\nReviewed-by: Other <other@test.com>\n"
	got := Inject(msg)

	// New trailer should be appended in the same block (no extra blank line)
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	last := lines[len(lines)-1]
	if last != "Claude-Code-Session-Id: session-xyz" {
		t.Errorf("expected last line to be session trailer, got %q", last)
	}

	// The line before should be Reviewed-by (existing trailer block)
	secondLast := lines[len(lines)-2]
	if !strings.HasPrefix(secondLast, "Reviewed-by:") {
		t.Errorf("expected second-to-last line to be existing trailer, got %q", secondLast)
	}

	// Should NOT have two blank lines (trailer appended to existing block)
	if strings.Contains(got, "\n\n\n") {
		t.Errorf("should not have double blank lines, got %q", got)
	}
}

func TestInject_EmptyEnvVar(t *testing.T) {
	t.Setenv(envVar, "")
	msg := "some message"
	got := Inject(msg)
	if got != msg {
		t.Errorf("expected message unchanged for empty env var, got %q", got)
	}
}

func TestAppendCustom_EmptyTrailers(t *testing.T) {
	msg := "some commit message"
	got := AppendCustom(msg, nil)
	if got != msg {
		t.Errorf("expected message unchanged for nil trailers, got %q", got)
	}

	got = AppendCustom(msg, []string{})
	if got != msg {
		t.Errorf("expected message unchanged for empty trailers, got %q", got)
	}
}

func TestAppendCustom_OneTrailer(t *testing.T) {
	msg := "add new feature"
	got := AppendCustom(msg, []string{"Agent: test123"})

	expected := "add new feature\n\nAgent: test123\n"
	if got != expected {
		t.Errorf("expected:\n%q\ngot:\n%q", expected, got)
	}
}

func TestAppendCustom_MultipleTrailers(t *testing.T) {
	msg := "add new feature"
	got := AppendCustom(msg, []string{"Agent: test123", "Review: approved"})

	expected := "add new feature\n\nAgent: test123\nReview: approved\n"
	if got != expected {
		t.Errorf("expected:\n%q\ngot:\n%q", expected, got)
	}
}

func TestAppendCustom_ExistingTrailerBlock(t *testing.T) {
	msg := "subject line\n\nSigned-off-by: Test User <test@test.com>\n"
	got := AppendCustom(msg, []string{"Agent: test123"})

	// Should append directly to the existing trailer block (no extra blank line)
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	last := lines[len(lines)-1]
	if last != "Agent: test123" {
		t.Errorf("expected last line to be custom trailer, got %q", last)
	}

	secondLast := lines[len(lines)-2]
	if !strings.HasPrefix(secondLast, "Signed-off-by:") {
		t.Errorf("expected second-to-last line to be existing trailer, got %q", secondLast)
	}

	// Should NOT have two blank lines
	if strings.Contains(got, "\n\n\n") {
		t.Errorf("should not have double blank lines, got %q", got)
	}
}

func TestInject_TrailingNewlines(t *testing.T) {
	t.Setenv(envVar, "session-123")
	msg := "subject\n\n\n"
	got := Inject(msg)

	// Should trim trailing newlines, add blank line, trailer
	if !strings.Contains(got, "Claude-Code-Session-Id: session-123") {
		t.Errorf("expected trailer, got %q", got)
	}
	// Should not have excessive blank lines
	if strings.Contains(got, "\n\n\n") {
		t.Errorf("should not have triple newlines, got %q", got)
	}
}

func TestSplitBodyTrailers_EmptyMessage(t *testing.T) {
	body, trailers := SplitBodyTrailers("")
	if body != "" {
		t.Errorf("expected empty body, got %q", body)
	}
	if trailers != "" {
		t.Errorf("expected empty trailers, got %q", trailers)
	}
}

func TestSplitBodyTrailers_NoTrailers(t *testing.T) {
	msg := "subject line\n\nThis is the body.\nIt has multiple lines.\n"
	body, trailers := SplitBodyTrailers(msg)
	if body != msg {
		t.Errorf("expected body = entire message %q, got %q", msg, body)
	}
	if trailers != "" {
		t.Errorf("expected empty trailers, got %q", trailers)
	}
}

func TestSplitBodyTrailers_SubjectOnly(t *testing.T) {
	msg := "just a subject"
	body, trailers := SplitBodyTrailers(msg)
	if body != msg {
		t.Errorf("expected body = entire message %q, got %q", msg, body)
	}
	if trailers != "" {
		t.Errorf("expected empty trailers, got %q", trailers)
	}
}

func TestSplitBodyTrailers_WithTrailers(t *testing.T) {
	msg := "subject line\n\nBody paragraph.\n\nSigned-off-by: Test <test@test.com>\nReviewed-by: Other <other@test.com>\n"
	body, trailers := SplitBodyTrailers(msg)

	expectedBody := "subject line\n\nBody paragraph.\n\n"
	expectedTrailers := "Signed-off-by: Test <test@test.com>\nReviewed-by: Other <other@test.com>\n"

	if body != expectedBody {
		t.Errorf("expected body %q, got %q", expectedBody, body)
	}
	if trailers != expectedTrailers {
		t.Errorf("expected trailers %q, got %q", expectedTrailers, trailers)
	}
}

func TestSplitBodyTrailers_SubjectAndTrailers(t *testing.T) {
	msg := "subject line\n\nSigned-off-by: Test <test@test.com>\n"
	body, trailers := SplitBodyTrailers(msg)

	expectedBody := "subject line\n\n"
	expectedTrailers := "Signed-off-by: Test <test@test.com>\n"

	if body != expectedBody {
		t.Errorf("expected body %q, got %q", expectedBody, body)
	}
	if trailers != expectedTrailers {
		t.Errorf("expected trailers %q, got %q", expectedTrailers, trailers)
	}
}

func TestSplitBodyTrailers_AllTrailerMessage(t *testing.T) {
	// Entire message is trailer-format lines with no blank-line separator
	msg := "Signed-off-by: Test <test@test.com>\nReviewed-by: Other <other@test.com>\n"
	body, trailers := SplitBodyTrailers(msg)

	if body != "" {
		t.Errorf("expected empty body for all-trailer message, got %q", body)
	}
	expectedTrailers := "Signed-off-by: Test <test@test.com>\nReviewed-by: Other <other@test.com>\n"
	if trailers != expectedTrailers {
		t.Errorf("expected trailers %q, got %q", expectedTrailers, trailers)
	}
}

func TestSplitBodyTrailers_SingleTrailerLine(t *testing.T) {
	// Single line that is a trailer
	msg := "Signed-off-by: Test <test@test.com>"
	body, trailers := SplitBodyTrailers(msg)

	if body != "" {
		t.Errorf("expected empty body for single trailer line, got %q", body)
	}
	if trailers != "Signed-off-by: Test <test@test.com>\n" {
		t.Errorf("expected trailer %q, got %q", "Signed-off-by: Test <test@test.com>\n", trailers)
	}
}

func TestSplitBodyTrailers_ContinuationLines(t *testing.T) {
	msg := "subject line\n\nSigned-off-by: Very Long Name\n  <email@example.com>\nReviewed-by: Other <other@test.com>\n"
	body, trailers := SplitBodyTrailers(msg)

	expectedBody := "subject line\n\n"
	expectedTrailers := "Signed-off-by: Very Long Name\n  <email@example.com>\nReviewed-by: Other <other@test.com>\n"

	if body != expectedBody {
		t.Errorf("expected body %q, got %q", expectedBody, body)
	}
	if trailers != expectedTrailers {
		t.Errorf("expected trailers %q, got %q", expectedTrailers, trailers)
	}
}

func TestSplitBodyTrailers_ContinuationAtEnd(t *testing.T) {
	msg := "subject line\n\nSigned-off-by: Very Long Name\n\tindented with tab\n"
	body, trailers := SplitBodyTrailers(msg)

	expectedBody := "subject line\n\n"
	expectedTrailers := "Signed-off-by: Very Long Name\n\tindented with tab\n"

	if body != expectedBody {
		t.Errorf("expected body %q, got %q", expectedBody, body)
	}
	if trailers != expectedTrailers {
		t.Errorf("expected trailers %q, got %q", expectedTrailers, trailers)
	}
}

func TestSplitBodyTrailers_TrailingNewlines(t *testing.T) {
	// Extra trailing newlines should be trimmed
	msg := "subject\n\nSigned-off-by: Test <test@test.com>\n\n\n"
	body, trailers := SplitBodyTrailers(msg)

	expectedBody := "subject\n\n"
	expectedTrailers := "Signed-off-by: Test <test@test.com>\n"

	if body != expectedBody {
		t.Errorf("expected body %q, got %q", expectedBody, body)
	}
	if trailers != expectedTrailers {
		t.Errorf("expected trailers %q, got %q", expectedTrailers, trailers)
	}
}

func TestSplitBodyTrailers_NoBlankLineSeparator_NonTrailerBody(t *testing.T) {
	// Body text immediately followed by trailer-looking lines (no blank line)
	// should NOT be detected as trailers
	msg := "this is body text\nSigned-off-by: Test <test@test.com>\n"
	body, trailers := SplitBodyTrailers(msg)

	if body != msg {
		t.Errorf("expected body = entire message %q, got %q", msg, body)
	}
	if trailers != "" {
		t.Errorf("expected no trailers (no blank line separator), got %q", trailers)
	}
}

func TestSplitBodyTrailers_MultipleTrailerBlocks(t *testing.T) {
	// Only the last trailer block should be split off
	msg := "subject\n\nSigned-off-by: First <first@test.com>\n\nSome body text\n\nReviewed-by: Second <second@test.com>\n"
	body, trailers := SplitBodyTrailers(msg)

	expectedBody := "subject\n\nSigned-off-by: First <first@test.com>\n\nSome body text\n\n"
	expectedTrailers := "Reviewed-by: Second <second@test.com>\n"

	if body != expectedBody {
		t.Errorf("expected body %q, got %q", expectedBody, body)
	}
	if trailers != expectedTrailers {
		t.Errorf("expected trailers %q, got %q", expectedTrailers, trailers)
	}
}

// --- ReplaceIdentity tests ---

func TestReplaceIdentity_BothNameAndEmail(t *testing.T) {
	msg := "fix bug\n\nSigned-off-by: Old Name <old@test.com>\n"
	got := ReplaceIdentity(msg, "Old Name", "New Name", "old@test.com", "new@test.com")
	want := "fix bug\n\nSigned-off-by: New Name <new@test.com>\n"
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

func TestReplaceIdentity_EmailOnly(t *testing.T) {
	msg := "fix bug\n\nCo-authored-by: Alice <old@test.com>\n"
	got := ReplaceIdentity(msg, "", "", "old@test.com", "new@test.com")
	want := "fix bug\n\nCo-authored-by: Alice <new@test.com>\n"
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

func TestReplaceIdentity_NameOnly(t *testing.T) {
	msg := "fix bug\n\nReviewed-by: Old Name <keep@test.com>\n"
	got := ReplaceIdentity(msg, "Old Name", "New Name", "", "")
	want := "fix bug\n\nReviewed-by: New Name <keep@test.com>\n"
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

func TestReplaceIdentity_NoTrailers(t *testing.T) {
	msg := "just a subject line"
	got := ReplaceIdentity(msg, "Old Name", "New Name", "old@test.com", "new@test.com")
	if got != msg {
		t.Errorf("expected message unchanged, got %q", got)
	}
}

func TestReplaceIdentity_MultipleIdentityTrailers(t *testing.T) {
	msg := "fix bug\n\nSigned-off-by: Old Name <old@test.com>\nCo-authored-by: Old Name <old@test.com>\nAcked-by: Other Person <other@test.com>\n"
	got := ReplaceIdentity(msg, "Old Name", "New Name", "old@test.com", "new@test.com")
	want := "fix bug\n\nSigned-off-by: New Name <new@test.com>\nCo-authored-by: New Name <new@test.com>\nAcked-by: Other Person <other@test.com>\n"
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

func TestReplaceIdentity_NonIdentityTrailersUnchanged(t *testing.T) {
	msg := "fix bug\n\nFixes: #123\nSigned-off-by: Old Name <old@test.com>\nClaude-Code-Session-Id: session-abc\n"
	got := ReplaceIdentity(msg, "Old Name", "New Name", "old@test.com", "new@test.com")
	want := "fix bug\n\nFixes: #123\nSigned-off-by: New Name <new@test.com>\nClaude-Code-Session-Id: session-abc\n"
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

func TestReplaceIdentity_NoMatchReturnsUnchanged(t *testing.T) {
	msg := "fix bug\n\nSigned-off-by: Someone Else <else@test.com>\n"
	got := ReplaceIdentity(msg, "Old Name", "New Name", "old@test.com", "new@test.com")
	if got != msg {
		t.Errorf("expected message unchanged when no match, got %q", got)
	}
}

func TestReplaceIdentity_BodyPreserved(t *testing.T) {
	msg := "fix bug\n\nThis body mentions Old Name <old@test.com> but should not change.\n\nSigned-off-by: Old Name <old@test.com>\n"
	got := ReplaceIdentity(msg, "Old Name", "New Name", "old@test.com", "new@test.com")
	// Only the trailer should change, not the body
	want := "fix bug\n\nThis body mentions Old Name <old@test.com> but should not change.\n\nSigned-off-by: New Name <new@test.com>\n"
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

func TestSplitBodyTrailers_SessionTrailer(t *testing.T) {
	msg := "add feature\n\nClaude-Code-Session-Id: session-abc-123\n"
	body, trailers := SplitBodyTrailers(msg)

	expectedBody := "add feature\n\n"
	expectedTrailers := "Claude-Code-Session-Id: session-abc-123\n"

	if body != expectedBody {
		t.Errorf("expected body %q, got %q", expectedBody, body)
	}
	if trailers != expectedTrailers {
		t.Errorf("expected trailers %q, got %q", expectedTrailers, trailers)
	}
}
