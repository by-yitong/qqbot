package utils

import (
	"os"
	"reflect"
	"testing"
	"time"
)

func TestEnvHelpers(t *testing.T) {
	t.Setenv("UTILS_TEST_STR", "x")
	if got := EnvOr("UTILS_TEST_STR", "def"); got != "x" {
		t.Errorf("EnvOr 应取环境值: %q", got)
	}
	if got := EnvOr("UTILS_TEST_MISSING", "def"); got != "def" {
		t.Errorf("EnvOr 空值应用默认: %q", got)
	}

	t.Setenv("UTILS_TEST_INT", "42")
	if got := EnvInt("UTILS_TEST_INT", 7); got != 42 {
		t.Errorf("EnvInt = %d, want 42", got)
	}
	t.Setenv("UTILS_TEST_INT", "abc")
	if got := EnvInt("UTILS_TEST_INT", 7); got != 7 {
		t.Errorf("EnvInt 非法值应用默认: %d", got)
	}
	if got := EnvInt("UTILS_TEST_MISSING", 7); got != 7 {
		t.Errorf("EnvInt 空值应用默认: %d", got)
	}

	t.Setenv("UTILS_TEST_SEC", "90")
	if got := EnvDurationSec("UTILS_TEST_SEC", 0); got != 90*time.Second {
		t.Errorf("EnvDurationSec = %v, want 90s", got)
	}
	t.Setenv("UTILS_TEST_SEC", "-1")
	if got := EnvDurationSec("UTILS_TEST_SEC", 30); got != 0 {
		t.Errorf("EnvDurationSec <=0 应返回 0（用调用方默认）: %v", got)
	}
	_ = os.Unsetenv("UTILS_TEST_SEC")
}

func TestSplitList(t *testing.T) {
	if got := SplitList("a, b ,,c"); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Errorf("SplitList = %v", got)
	}
	if got := SplitList(""); got != nil {
		t.Errorf("空串应返回 nil: %v", got)
	}
}
