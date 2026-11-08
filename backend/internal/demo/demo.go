// Package demo contains only owned, non-persistent synthetic examples and the standard library.
package demo

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strings"
	"unicode/utf8"
)

const Version = "demo-v1"

type Fact struct {
	Label string `json:"label"`
	Value string `json:"value"`
	Unit  string `json:"unit"`
}
type Scenario struct {
	Version           string   `json:"version"`
	Synthetic         bool     `json:"synthetic"`
	Persistent        bool     `json:"persistent"`
	ProductionEffects bool     `json:"production_effects"`
	Scenario          string   `json:"scenario"`
	Title             string   `json:"title"`
	Facts             []Fact   `json:"facts"`
	Outcome           string   `json:"outcome"`
	Explanation       string   `json:"explanation"`
	Limitations       []string `json:"limitations"`
}

func Example(name string) (Scenario, bool) {
	out := Scenario{Version: Version, Synthetic: true, Scenario: name, Facts: []Fact{}, Limitations: []string{"仅为本系统自有合成资料；没有读取真实用户、账号或数据库。", "操作不会连接 Sub2API 或邮件，不保存结果，不能作为业务验收。"}}
	switch name {
	case "overview":
		out.Title = "合成被动观察"
		out.Facts = []Fact{{"示例账号", "sample-account", "合成标识"}, {"短周期已用", "37.25", "%"}, {"上次读取", "成功", "合成状态"}}
		out.Outcome = "read_only"
		out.Explanation = "观察百分比与原生钱包是不同事实；示例不会产生真实用量或容量估计。"
	case "wallet":
		out.Title = "合成零目标保护"
		out.Facts = []Fact{{"示例钱包", "18.75", "原生钱包单位"}, {"示例冻结", "2.50", "原生钱包单位"}, {"目标", "0", "原生钱包单位"}}
		out.Outcome = "blocked"
		out.Explanation = "零目标不可写入；不会改用其他金额。钱包与冻结独立保留，不能据此推算可发放金额。"
	case "restore":
		out.Title = "合成恢复门禁"
		out.Facts = []Fact{{"文件兼容性", "匹配", "合成条件"}, {"当前采集", "运行中", "合成条件"}, {"恢复前副本", "未下载", "合成条件"}}
		out.Outcome = "blocked"
		out.Explanation = "运行中采集和缺少恢复前副本均阻止恢复；切换示例不执行恢复，也不创建真实文件。"
	default:
		return Scenario{}, false
	}
	return out, true
}
func failure(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"defined": true, "code": code, "status": status, "message": code, "data": map[string]any{"retry_at": nil}})
}

type Handler struct{}

func (Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if len(r.Header.Values("Cookie")) != 0 || len(r.Header.Values("Authorization")) != 0 {
		failure(w, 403, "FORBIDDEN")
		return
	}
	if r.URL.RawQuery != "" || r.URL.ForceQuery {
		failure(w, 400, "BAD_REQUEST")
		return
	}
	if r.URL.Path == "/health" && r.Method == http.MethodGet {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{\"status\":\"synthetic_demo\"}"))
		return
	}
	if r.URL.Path != "/api/v1/demo/scenario" {
		failure(w, 404, "NOT_FOUND")
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		failure(w, 405, "METHOD_NOT_SUPPORTED")
		return
	}
	kind, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || kind != "application/json" || len(params) > 1 || len(params) == 1 && !strings.EqualFold(params["charset"], "utf-8") || r.Header.Get("Content-Encoding") != "" || len(r.Header.Values("Content-Type")) != 1 {
		failure(w, 400, "BAD_REQUEST")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	if err != nil || !utf8.Valid(body) {
		failure(w, 400, "BAD_REQUEST")
		return
	}
	// Exactly one known field rejects duplicates, aliases, nulls and trailing JSON.
	d := json.NewDecoder(bytes.NewReader(body))
	open, err := d.Token()
	var name string
	if err != nil || open != json.Delim('{') {
		failure(w, 400, "BAD_REQUEST")
		return
	}
	key, err := d.Token()
	if err != nil || key != "scenario" || d.Decode(&name) != nil {
		failure(w, 400, "BAD_REQUEST")
		return
	}
	close, err := d.Token()
	if err != nil || close != json.Delim('}') || d.Decode(new(any)) != io.EOF {
		failure(w, 400, "BAD_REQUEST")
		return
	}
	out, ok := Example(name)
	if !ok {
		failure(w, 400, "BAD_REQUEST")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}
