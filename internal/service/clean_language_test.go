package service

import "testing"

// TestCleanLanguage 覆盖 cleanLanguage 的回退顺序与边界。
func TestCleanLanguage(t *testing.T) {
	cases := []struct {
		name     string
		requested string
		detected  string
		want      string
	}{
		{"用户指定优先", "ja", "en", "ja"},
		{"请求为空用检测值", "", "ja", "ja"},
		{"请求为 auto 用检测值", "auto", "ja", "ja"},
		{"auto 大小写不敏感", "AUTO", "ja", "ja"},
		{"两者都空返回空", "", "", ""},
		{"两者都 auto 返回空", "auto", "auto", ""},
		{"空白被裁剪", "  ja  ", "", "ja"},
		{"检测值空白也算空", "", "   ", ""},
		{"请求空白时回退检测值", "  ", "ko", "ko"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := cleanLanguage(tc.requested, tc.detected)
			if got != tc.want {
				t.Errorf("cleanLanguage(%q, %q) = %q，期望 %q", tc.requested, tc.detected, got, tc.want)
			}
		})
	}
}
