package scheduler

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.json")
	st := &store{path: path}

	f := storeFile{NextID: 3, Tasks: []Task{{
		ID: "t1", Name: "每日新闻", Handler: HandlerAgent,
		Schedule:  Schedule{Type: ScheduleDaily, At: "08:00"},
		Payload:   "总结今日新闻",
		Target:    &Target{Source: "group", ID: "G1"},
		Enabled:   true,
		CreatedAt: time.Date(2026, 8, 14, 12, 0, 0, 0, loc),
		NextRun:   time.Date(2026, 8, 15, 8, 0, 0, 0, loc),
	}}}
	if err := st.save(f); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := st.load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.NextID != 3 || len(got.Tasks) != 1 {
		t.Fatalf("round-trip 丢失: %+v", got)
	}
	task := got.Tasks[0]
	if task.ID != "t1" || task.Schedule.At != "08:00" ||
		task.Target == nil || task.Target.Source != "group" || task.Target.ID != "G1" {
		t.Errorf("round-trip 字段错: %+v", task)
	}
	// 时间应无损（RFC3339 精度：纳秒会被截到秒，用秒级比较）
	if !task.NextRun.Equal(f.Tasks[0].NextRun.Truncate(time.Second)) {
		t.Errorf("NextRun: got %v", task.NextRun)
	}
}

func TestStoreLoadNotExist(t *testing.T) {
	st := &store{path: filepath.Join(t.TempDir(), "absent.json")}
	f, err := st.load()
	if err != nil || len(f.Tasks) != 0 || f.NextID != 1 {
		t.Errorf("不存在文件应视为空: %+v, %v", f, err)
	}
}

func TestStoreLoadCorrupt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	st := &store{path: path}
	f, err := st.load()
	if err == nil {
		t.Error("坏文件应返回告警错误")
	}
	if len(f.Tasks) != 0 || f.NextID != 1 {
		t.Errorf("坏文件应从空开始: %+v", f)
	}
	if _, serr := os.Stat(path + ".bak"); serr != nil {
		t.Errorf("坏文件应备份为 .bak: %v", serr)
	}
	// 备份后原路径已空，再 load 正常
	if f2, err := st.load(); err != nil || len(f2.Tasks) != 0 {
		t.Errorf("备份后再 load: %+v, %v", f2, err)
	}
}

func TestStoreMemOnly(t *testing.T) {
	st := &store{}
	if err := st.save(storeFile{NextID: 2}); err != nil {
		t.Errorf("空路径应 no-op: %v", err)
	}
}
