package agent

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// Skill 是一份技能定义（SKILL.md：YAML frontmatter + markdown 正文）。
type Skill struct {
	Name        string // 稳定标识（frontmatter.name；缺省用文件名）
	Description string // 何时使用（frontmatter.description）
	Content     string // 正文（去掉 frontmatter 后的 markdown）
	FilePath    string // 源文件路径
}

// LoadSkills 从给定目录递归加载所有 *.md 文件为技能。
// 目录不存在或为空不算错误，返回空切片；单个文件解析失败会被跳过（不致命）。
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
		err = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(strings.ToLower(d.Name()), ".md") {
				return nil
			}
			s, perr := parseSkillFile(path)
			if perr != nil {
				return nil // 跳过坏文件
			}
			skills = append(skills, s)
			return nil
		})
		if err != nil {
			return nil, err
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
