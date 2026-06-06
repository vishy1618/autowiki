package chat

import (
	"strings"
	"testing"
)

func TestBuildSystemPrompt_MentionsWritePages(t *testing.T) {
	got := buildSystemPrompt("", "")
	if !strings.Contains(got, "write_pages") {
		t.Errorf("system prompt missing %q", "write_pages")
	}
}

func TestBuildSystemPrompt_MentionsPatchPage(t *testing.T) {
	got := buildSystemPrompt("", "")
	if !strings.Contains(got, "patch_page") {
		t.Errorf("system prompt missing %q", "patch_page")
	}
}

func TestBuildSystemPrompt_MentionsAppendToSection(t *testing.T) {
	got := buildSystemPrompt("", "")
	if !strings.Contains(got, "append_to_section") {
		t.Errorf("system prompt missing %q", "append_to_section")
	}
}

func TestBuildSystemPrompt_DownloadLinkInstructions(t *testing.T) {
	tests := []struct {
		name    string
		wantStr string
	}{
		{
			name:    "includes download link URL pattern",
			wantStr: "/api/vault/files/",
		},
		{
			name:    "instructs proactive binary file links",
			wantStr: "proactively offer a download link",
		},
		{
			name:    "instructs text files only on explicit request",
			wantStr: "only offer a download link when the user explicitly asks to download",
		},
		{
			name:    "instructs to use list_vault or search_vault when path unknown",
			wantStr: "list_vault or search_vault",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildSystemPrompt("", "")
			if !strings.Contains(got, tt.wantStr) {
				t.Errorf("system prompt missing %q", tt.wantStr)
			}
		})
	}
}
