package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// skillNameMax 限制技能名长度（同时也是落盘文件名，防失控）。
const skillNameMax = 64

// Skill 是一份技能定义（SKILL.md：YAML frontmatter + markdown 正文）。
type Skill struct {
	Name        string // 稳定标识（frontmatter.name；缺省用文件名）
	Description string // 何时使用（frontmatter.description）
	Content     string // 正文（去掉 frontmatter 后的 markdown）
	FilePath    string // 源文件路径
}

// LoadSkills 从给定目录加载技能：
//   - 顶层的 *.md（如 skills/foo.md）
//   - 一级子目录里的 SKILL.md（如 skills/hallmark/SKILL.md）
//
// 子目录里的其他文件（references/ 等）是技能的按需引用材料，不作为独立技能——
// 模型用 read_skill 按「技能名/相对路径」读取（见 readSkillTool）。
// 目录不存在或为空不算错误；单个文件解析失败会被跳过（不致命）。
func LoadSkills(dirs ...string) ([]Skill, error) {
	var skills []Skill
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		info, err := os.Stat(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		if !info.IsDir() {
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if e.IsDir() {
				p := filepath.Join(dir, e.Name(), "SKILL.md")
				if st, err := os.Stat(p); err == nil && !st.IsDir() {
					if s, perr := parseSkillFile(p); perr == nil {
						skills = append(skills, s)
					}
				}
				continue
			}
			if !strings.HasSuffix(strings.ToLower(e.Name()), ".md") {
				continue
			}
			if s, perr := parseSkillFile(filepath.Join(dir, e.Name())); perr == nil {
				skills = append(skills, s)
			}
		}
	}
	return skills, nil
}

// parseSkillFile 解析一个 SKILL.md：分离 YAML frontmatter 与正文。
func parseSkillFile(path string) (Skill, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Skill{}, err
	}
	name, desc, body := splitFrontmatter(string(data))
	if name == "" {
		base := filepath.Base(path)
		name = strings.TrimSuffix(base, filepath.Ext(base))
	}
	return Skill{Name: name, Description: desc, Content: body, FilePath: path}, nil
}

// splitFrontmatter 分离 frontmatter（首尾 --- 包裹的 YAML）与正文。
// 无 frontmatter 时，三者中 name/desc 为空，body 为原文。
func splitFrontmatter(s string) (name, desc, body string) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "---") {
		return "", "", s
	}
	rest := strings.TrimPrefix(s, "---")
	rest = strings.TrimPrefix(rest, "\r\n")
	rest = strings.TrimPrefix(rest, "\n")
	idx := strings.Index(rest, "\n---")
	if idx < 0 {
		return "", "", strings.TrimSpace(s) // 没有结束分隔，整体当正文
	}
	front := rest[:idx]
	body = strings.TrimSpace(rest[idx+len("\n---"):])

	m := map[string]string{}
	if err := yaml.Unmarshal([]byte(front), &m); err != nil {
		return "", "", body // frontmatter 解析失败则忽略，仅留正文
	}
	return m["name"], m["description"], body
}

// readSkillToolName 是按需加载技能正文的工具名（见 control_tools.go 的 readSkillTool）。
const readSkillToolName = "read_skill"

// SkillsPrompt 把技能列表渲染成注入 system 的「技能索引」文本块（XML，对齐 pi）。
//
// 采用 pi 同款的懒加载（Agent Skills 标准）：system 里只贴每个技能的 name + description，
// 不塞正文。模型判断任务匹配某技能时，用 read_skill 工具按 name 现读正文，再按指引行事。
// 这样技能多/正文长时，不会每轮都把全部正文摊进 context 烧 token。
//
// 无技能时返回空串。
func SkillsPrompt(skills []Skill) string {
	if len(skills) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("以下是可用的技能。当任务与某技能的描述匹配时，先用 ")
	b.WriteString(readSkillToolName)
	b.WriteString(" 工具按 name 加载它的详细指引正文，再按其中指引行事；不需要时不必加载。\n")
	b.WriteString("<available_skills>\n")
	for _, s := range skills {
		b.WriteString("  <skill>\n")
		b.WriteString("    <name>")
		b.WriteString(s.Name)
		b.WriteString("</name>\n")
		if s.Description != "" {
			b.WriteString("    <description>")
			b.WriteString(s.Description)
			b.WriteString("</description>\n")
		}
		b.WriteString("  </skill>\n")
	}
	b.WriteString("</available_skills>")
	return b.String()
}

// skillStore 是线程安全的技能容器，支持运行时动态增删（用于「发 URL 自动加技能」）。
type skillStore struct {
	mu    sync.RWMutex
	items []Skill
}

// newSkillStore 用初始技能列表构造（初始列表会被拷贝，互不影响）。
func newSkillStore(initial []Skill) *skillStore {
	return &skillStore{items: append([]Skill(nil), initial...)}
}

// Add 追加一个技能。
func (s *skillStore) Add(sk Skill) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items = append(s.items, sk)
}

// Put 写入一个技能：同名已存在则原位替换（更新），否则追加。
func (s *skillStore) Put(sk Skill) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, item := range s.items {
		if item.Name == sk.Name {
			s.items[i] = sk
			return
		}
	}
	s.items = append(s.items, sk)
}

// List 返回当前技能的拷贝。
func (s *skillStore) List() []Skill {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]Skill(nil), s.items...)
}

// Len 返回技能数量。
func (s *skillStore) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.items)
}

// sanitizeSkillName 规整技能名为安全文件名：小写、字母数字与连字符，
// 其余字符逐个替换为 '-'，压缩连续 '-'，去首尾 '-'，限长 skillNameMax。
func sanitizeSkillName(name string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(name) {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-'
		if !ok {
			r = '-'
		}
		if r == '-' {
			if lastDash || b.Len() == 0 {
				continue
			}
			lastDash = true
		} else {
			lastDash = false
		}
		b.WriteRune(r)
		if b.Len() >= skillNameMax {
			break
		}
	}
	return strings.Trim(b.String(), "-")
}

// CreateSkill 创建（或同名更新）一个技能：写入 SkillsDir 下的 <name>.md
// （带 YAML frontmatter，重启后由 LoadSkills 自动加载），并立即注册到运行时。
// SkillsDir 为空则只注册运行时、不落盘（重启丢失）。返回规整后的名称与落盘路径。
func (a *Agent) CreateSkill(name, description, content string) (Skill, error) {
	name = sanitizeSkillName(name)
	if name == "" {
		return Skill{}, fmt.Errorf("技能名无效（需包含字母/数字）")
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return Skill{}, fmt.Errorf("技能正文不能为空")
	}

	sk := Skill{Name: name, Description: strings.TrimSpace(description), Content: content}

	if a.cfg.SkillsDir != "" {
		var b strings.Builder
		b.WriteString("---\nname: " + name + "\n")
		if sk.Description != "" {
			b.WriteString("description: " + yamlQuote(sk.Description) + "\n")
		}
		b.WriteString("---\n\n" + content + "\n")
		path := filepath.Join(a.cfg.SkillsDir, name+".md")
		if err := os.MkdirAll(a.cfg.SkillsDir, 0o755); err != nil {
			return Skill{}, fmt.Errorf("创建技能目录失败: %w", err)
		}
		if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
			return Skill{}, fmt.Errorf("写入技能文件失败: %w", err)
		}
		sk.FilePath = path
	}

	a.skills.Put(sk)
	return sk, nil
}

// DeleteSkill 按名删除一个技能：从运行时移除，并删除 SkillsDir 下的源文件（若有）。
func (a *Agent) DeleteSkill(name string) error {
	a.skills.mu.Lock()
	found := false
	items := a.skills.items[:0]
	for _, s := range a.skills.items {
		if s.Name == name {
			found = true
			continue
		}
		items = append(items, s)
	}
	a.skills.items = items
	a.skills.mu.Unlock()
	if !found {
		return fmt.Errorf("未知技能 %q", name)
	}
	if a.cfg.SkillsDir != "" {
		// 目录型技能删整目录（含 references/），单文件技能删 <name>.md。
		if dir := filepath.Join(a.cfg.SkillsDir, sanitizeSkillName(name)); isDir(dir) {
			if err := os.RemoveAll(dir); err != nil {
				return fmt.Errorf("删除技能目录失败: %w", err)
			}
		} else if path := filepath.Join(a.cfg.SkillsDir, sanitizeSkillName(name)+".md"); isFile(path) {
			if err := os.Remove(path); err != nil {
				return fmt.Errorf("删除技能文件失败: %w", err)
			}
		}
	}
	return nil
}

// isDir / isFile 是 os.Stat 的小包装（不存在返回 false）。
func isDir(p string) bool  { st, err := os.Stat(p); return err == nil && st.IsDir() }
func isFile(p string) bool { st, err := os.Stat(p); return err == nil && !st.IsDir() }

// ReadSkillAsset 按需读取一个技能的引用材料：name 形如「技能名/相对路径」
// （如 hallmark/references/color.md），在该技能所在目录内解析，拒绝越界路径。
// 返回文件内容。
func (a *Agent) ReadSkillAsset(name string) (string, error) {
	slash := strings.Index(name, "/")
	if slash <= 0 || slash == len(name)-1 {
		return "", fmt.Errorf("格式应为 技能名/相对路径，如 hallmark/references/color.md")
	}
	skillName, rel := name[:slash], name[slash+1:]
	rel = filepath.Clean(rel)
	if rel == "." || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return "", fmt.Errorf("非法路径 %q", rel)
	}
	for _, sk := range a.skills.List() {
		if sk.Name != skillName {
			continue
		}
		if sk.FilePath == "" {
			return "", fmt.Errorf("技能「%s」不是目录型技能，无引用材料", skillName)
		}
		data, err := os.ReadFile(filepath.Join(filepath.Dir(sk.FilePath), rel))
		if err != nil {
			return "", fmt.Errorf("读取失败: %w", err)
		}
		return string(data), nil
	}
	return "", fmt.Errorf("未知技能 %q", skillName)
}

// yamlQuote 给 frontmatter 的 description 加引号（含冒号/井号等 YAML 特殊字符也安全）。
func yamlQuote(s string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`) + `"`
}
