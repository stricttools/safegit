package exitcode

import (
	"strings"
	"testing"
)

func TestRenderDocumentReplacesOnlyTheMarkedRegion(t *testing.T) {
	doc := "before\n\n" + DocBeginMarker + "\n\n| Code | Meaning |\n|------|---------|\n| 999 | stale |\n\n" + DocEndMarker + "\n\nafter\n"

	got, err := RenderDocument(doc)
	if err != nil {
		t.Fatalf("RenderDocument: %v", err)
	}
	if !strings.HasPrefix(got, "before\n\n") {
		t.Errorf("content before the region was not preserved:\n%s", got)
	}
	if !strings.HasSuffix(got, DocEndMarker+"\n\nafter\n") {
		t.Errorf("content after the region was not preserved:\n%s", got)
	}
	if strings.Contains(got, "999 | stale") {
		t.Error("the stale row survived the render")
	}
	if !strings.Contains(got, MarkdownTable()) {
		t.Errorf("the rendered document does not carry the registry table:\n%s", got)
	}
}

func TestRenderDocumentIsIdempotent(t *testing.T) {
	doc := DocBeginMarker + "\n\n" + DocEndMarker + "\n"
	once, err := RenderDocument(doc)
	if err != nil {
		t.Fatalf("RenderDocument: %v", err)
	}
	twice, err := RenderDocument(once)
	if err != nil {
		t.Fatalf("RenderDocument (second pass): %v", err)
	}
	if once != twice {
		t.Errorf("rendering twice changed the document:\n--- once ---\n%s\n--- twice ---\n%s", once, twice)
	}
}

func TestRenderDocumentRefusesAMissingMarker(t *testing.T) {
	for name, doc := range map[string]string{
		"no markers":  "just prose\n",
		"no end":      DocBeginMarker + "\ntable\n",
		"no begin":    "table\n" + DocEndMarker + "\n",
		"end < begin": DocEndMarker + "\n" + DocBeginMarker + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := RenderDocument(doc); err == nil {
				t.Error("expected an error, got none: a document with no region to generate into must not render silently")
			}
		})
	}
}
