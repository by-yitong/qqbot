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

func TestSanitizeSkillName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Baozang Roulette", "baozang-roulette"},
		{"  原神抽卡!! ", ""},
		{"a--b///c", "a-b-c"},
		{"ok_name-1", "ok-name-1"},
	}
	for _, c := range cases {
		if got := sanitizeSkillName(c.in); got != c.want {
			t.Errorf("sanitizeSkillName(%q)=%q want %q", c.in, got, c.want)
		}
	}
}

func TestCreateSkillPersistAndReload(t *testing.T) {
	dir := t.TempDir()
	a := New(Config{SkillsDir: dir})

	sk, err := a.CreateSkill("Baozang Roulette", "描述: 触发场景", "步骤1. 抽卡")
	if err != nil {
		t.Fatal(err)
	}
	if sk.Name != "baozang-roulette" {
		t.Fatalf("name=%q", sk.Name)
	}
	// 运行时立即可见
	found := false
	for _, s := range a.Skills() {
		if s.Name == "baozang-roulette" {
			found = true
		}
	}
	if !found {
		t.Fatal("创建后应立出现在 Skills() 里")
	}

	// 落盘后能被 LoadSkills 重新解析（frontmatter 含冒号也不炸）
	loaded, err := LoadSkills(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 || loaded[0].Name != "baozang-roulette" ||
		loaded[0].Description != "描述: 触发场景" || loaded[0].Content != "步骤1. 抽卡" {
		t.Fatalf("reload: %+v", loaded)
	}

	// 同名覆盖更新
	if _, err := a.CreateSkill("baozang-roulette", "v2", "新正文"); err != nil {
		t.Fatal(err)
	}
	loaded2, _ := LoadSkills(dir)
	if len(loaded2) != 1 || loaded2[0].Content != "新正文" {
		t.Fatalf("overwrite: %+v", loaded2)
	}

	// 删除：运行时与文件一并移除
	if err := a.DeleteSkill("baozang-roulette"); err != nil {
		t.Fatal(err)
	}
	if got := a.Skills(); len(got) != 0 {
		t.Fatalf("删除后 Skills()=%+v", got)
	}
	loaded3, _ := LoadSkills(dir)
	if len(loaded3) != 0 {
		t.Fatalf("删除后文件应移除: %+v", loaded3)
	}

	// 未配置 SkillsDir：只进运行时、不落盘
	a2 := New(Config{})
	sk2, err := a2.CreateSkill("x", "d", "c")
	if err != nil {
		t.Fatal(err)
	}
	if sk2.FilePath != "" {
		t.Fatalf("FilePath 应为空: %+v", sk2)
	}

	// 非法名 / 空正文
	if _, err := a.CreateSkill("///", "d", "c"); err == nil {
		t.Fatal("纯符号名应报错")
	}
	if _, err := a.CreateSkill("good", "d", "  "); err == nil {
		t.Fatal("空正文应报错")
	}
}

func TestLoadSkillsDirectoryShape(t *testing.T) {
	dir := t.TempDir()
	// 顶层单文件技能 + 目录型技能（SKILL.md + references/）
	os.MkdirAll(filepath.Join(dir, "hallmark", "references"), 0o755)
	os.WriteFile(filepath.Join(dir, "flat-skill.md"), []byte("---\nname: flat\ndescription: 平的\n---\n正文"), 0o644)
	os.WriteFile(filepath.Join(dir, "hallmark", "SKILL.md"), []byte("---\nname: hallmark\ndescription: 设计\n---\n主正文"), 0o644)
	os.WriteFile(filepath.Join(dir, "hallmark", "references", "color.md"), []byte("颜色参考"), 0o644)
	os.WriteFile(filepath.Join(dir, "hallmark", "notes.txt"), []byte("非 md"), 0o644)

	got, err := LoadSkills(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("应加载 2 个技能（flat + hallmark），实际 %d: %+v", len(got), got)
	}
	for _, s := range got {
		if s.Name == "hallmark" && s.Content != "主正文" {
			t.Errorf("hallmark 正文: %q", s.Content)
		}
	}

	// read_skill 按「技能名/相对路径」读引用材料；越界路径被拒。
	a := New(Config{Skills: got})
	out, err := a.ReadSkillAsset("hallmark/references/color.md")
	if err != nil || out != "颜色参考" {
		t.Fatalf("ReadSkillAsset: %q, %v", out, err)
	}
	if _, err := a.ReadSkillAsset("hallmark/../../etc/passwd"); err == nil {
		t.Fatal("越界路径应被拒绝")
	}
	if _, err := a.ReadSkillAsset("flat-skill/references/x.md"); err == nil {
		t.Fatal("未知技能应报错")
	}
}
