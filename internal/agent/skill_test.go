package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSplitFrontmatter(t *testing.T) {
	src := "---\nname: weather\ndescription: Get weather\n---\n# Weather\nDo this.\n"
	name, desc, body := splitFrontmatter(src)
	if name != "weather" {
		t.Errorf("name=%q", name)
	}
	if desc != "Get weather" {
		t.Errorf("desc=%q", desc)
	}
	if !strings.Contains(body, "Do this.") {
		t.Errorf("body=%q", body)
	}
}

func TestSplitFrontmatterNone(t *testing.T) {
	name, desc, body := splitFrontmatter("just text")
	if name != "" || desc != "" {
		t.Errorf("无 frontmatter 时 name/desc 应为空")
	}
	if body != "just text" {
		t.Errorf("body=%q", body)
	}
}

func TestLoadSkills(t *testing.T) {
	dir := t.TempDir()
	skill := "---\nname: greet\ndescription: 问候\n---\n打招呼时要热情。\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(skill), 0o644); err != nil {
		t.Fatal(err)
	}
	// 非 md 文件应被忽略。
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	skills, err := LoadSkills(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(skills) != 1 {
		t.Fatalf("len=%d", len(skills))
	}
	if skills[0].Name != "greet" || skills[0].Description != "问候" {
		t.Errorf("技能: %+v", skills[0])
	}
	if !strings.Contains(skills[0].Content, "热情") {
		t.Errorf("content=%q", skills[0].Content)
	}
}

func TestLoadSkillsMissingDir(t *testing.T) {
	skills, err := LoadSkills("/nonexistent/path/xyz")
	if err != nil {
		t.Fatal(err)
	}
	if len(skills) != 0 {
		t.Errorf("不存在的目录应返回空切片")
	}
}

func TestLoadSkillsDefaultName(t *testing.T) {
	// 无 frontmatter：用文件名作 name。
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "tips.md"), []byte("一些提示"), 0o644); err != nil {
		t.Fatal(err)
	}
	skills, _ := LoadSkills(dir)
	if len(skills) != 1 || skills[0].Name != "tips" {
		t.Fatalf("name=%q", skills[0].Name)
	}
}

func TestSkillsPrompt(t *testing.T) {
	out := SkillsPrompt([]Skill{{Name: "greet", Description: "问候", Content: "要热情"}})
	for _, want := range []string{"greet", "问候", readSkillToolName, "available_skills"} {
		if !strings.Contains(out, want) {
			t.Errorf("prompt 缺少 %q: %s", want, out)
		}
	}
	// 懒加载：正文不应出现在索引里（应通过 read_skill 现读）。
	if strings.Contains(out, "要热情") {
		t.Errorf("正文不应塞进 system prompt: %s", out)
	}
	if SkillsPrompt(nil) != "" {
		t.Error("nil 技能应返回空串")
	}
}
